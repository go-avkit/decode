// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "math"

// The float64 envelope adjustment of v0.17.1, kept as the oracle of the binary32 one
// (aac_sbr_adj.go). The decoder's signal path calls it instead of the binary32 code
// when a test installs sbrNewAdjOracle: the oracle keeps its own state per channel,
// computes the adjusted high band in float64 and rounds it once into the state the
// assembly reads, which is the rounding the synthesis applied to it in v0.17.1 (it
// narrowed its input to binary32), so that the PCM of the two paths differs by the
// arithmetic of the adjustment alone. TestSBRAdjustBound states how much.

// oracle constants of v0.17.1.
const oracleEpsilon = 1e-12

var (
	oracleLimiterGain = [4]float64{0.70795, 1.0, 1.41254, 1e10}
	oracleSmooth      = func() (h [sbrSmoothTaps]float64) {
		for n := range h {
			h[n] = (1 + math.Cos(float64(n)*math.Pi/5)) / 6
		}
		return h
	}()
)

// sbrAdjOracleF64 is the v0.17.1 adjustment: the state of both channels.
type sbrAdjOracleF64 struct{ ch [2]oracleChan }

type oracleChan struct {
	eOrig [sbrMaxEnv][sbrMaxBands]float64
	qOrig [2][sbrMaxNoise]float64

	hist     [sbrSmoothOrder][2][sbrSynBands]float64
	histPos  int
	noiseIdx int
	sineIdx  int
	prevS    [sbrSynBands]bool
	y        [sbrHFSlots][sbrSynBands]complex128
}

func installAdjOracle() func() {
	sbrNewAdjOracle = func() sbrAdjOracle { return new(sbrAdjOracleF64) }
	return func() { sbrNewAdjOracle = nil }
}

// dequantise computes the transmitted energies of both channels of the element.
func (o *sbrAdjOracleF64) dequantise(s *sbrState, nch int) {
	for ch := 0; ch < nch; ch++ {
		c := &s.ch[ch]
		a := 2.0
		if c.ampRes != 0 {
			a = 1
		}
		d := &o.ch[ch]
		for e := 0; e < c.numEnv; e++ {
			for i := 0; i < s.t.n[c.freqRes[e+1]]; i++ {
				q := float64(c.envQ[e+1][i])
				d.eOrig[e][i] = 64 * math.Exp2(q/a)
			}
		}
		for k := 0; k < c.numNoise; k++ {
			for i := 0; i < s.t.nQ; i++ {
				d.qOrig[k][i] = math.Exp2(sbrNoiseOffset - float64(c.noiseQ[k+1][i]))
			}
		}
	}
	if !s.coupling || nch < 2 {
		return
	}
	c0, c1 := &s.ch[0], &s.ch[1]
	a, pan := 2.0, 24.0
	if c0.ampRes != 0 {
		a, pan = 1, 12
	}
	l, r := &o.ch[0], &o.ch[1]
	for e := 0; e < c0.numEnv; e++ {
		for i := 0; i < s.t.n[c0.freqRes[e+1]]; i++ {
			level := 64 * math.Exp2(float64(c0.envQ[e+1][i])/a+1)
			bal := float64(c1.envQ[e+1][i])
			l.eOrig[e][i] = level / (1 + math.Exp2((pan-bal)/a))
			r.eOrig[e][i] = level / (1 + math.Exp2((bal-pan)/a))
		}
	}
	for k := 0; k < c0.numNoise; k++ {
		for i := 0; i < s.t.nQ; i++ {
			level := math.Exp2(sbrNoiseOffset - float64(c0.noiseQ[k+1][i]) + 1)
			bal := float64(c1.noiseQ[k+1][i])
			l.qOrig[k][i] = level / (1 + math.Exp2(12-bal))
			r.qOrig[k][i] = level / (1 + math.Exp2(bal-12))
		}
	}
}

// adjust is v0.17.1's adjust, unchanged but for the state it keeps and for rounding
// its result into the decoder's adjusted band at the end.
func (o *sbrAdjOracleF64) adjust(s *sbrState, ch int) {
	d := s.channelDSP(ch)
	a := &o.ch[ch]
	out := &d.adj
	y := &a.y // persistent: slots a frame does not adjust keep the last frame's
	c := &s.ch[ch]
	t := &s.t
	kx, m := t.kx[1], t.m[1]
	smoothing := s.smoothing == 0
	copy(out.tail[:], out.y[sbrQMFSlots:])
	defer func() {
		for i := range y {
			for k := range y[i] {
				out.y[i][k] = complex(float32(real(y[i][k])), float32(imag(y[i][k])))
			}
		}
	}()
	for e := 0; e < c.numEnv; e++ {
		var (
			eo, qo, eCurr [sbrSynBands]float64
			gain, qm, sm  [sbrSynBands]float64
			band          [sbrSynBands]bool
			onset         [sbrSynBands]bool
		)
		res := c.freqRes[e+1]
		tab := t.fLow[:t.n[0]+1]
		if res != 0 {
			tab = t.fHigh[:t.n[1]+1]
		}
		lo, hi := 2*int(c.tEnv[e]), min(2*int(c.tEnv[e+1]), sbrHFSlots)
		for i := 0; i+1 < len(tab); i++ {
			for k := tab[i]; k < tab[i+1]; k++ {
				eo[k-kx] = a.eOrig[e][i]
			}
		}
		noiseSet := 0
		if c.numNoise > 1 && c.tEnv[e] >= c.tQ[1] {
			noiseSet = 1
		}
		for i := 0; i < t.nQ; i++ {
			for k := t.fNoise[i]; k < t.fNoise[i+1]; k++ {
				qo[k-kx] = a.qOrig[noiseSet][i]
			}
		}
		if c.harmonic {
			for i := 0; i < t.n[1]; i++ {
				mid := (t.fHigh[i] + t.fHigh[i+1]) >> 1
				onset[mid-kx] = c.addHarm[i] != 0 && (e >= int(c.eA[1]) || a.prevS[mid-kx])
			}
		}
		for i := 0; i+1 < len(tab); i++ {
			any := false
			for k := tab[i]; k < tab[i+1]; k++ {
				any = any || onset[k-kx]
			}
			for k := tab[i]; k < tab[i+1]; k++ {
				band[k-kx] = any
			}
		}
		n := float64(hi - lo)
		if s.interpolFreq != 0 {
			for j := 0; j < m; j++ {
				for i := lo; i < hi; i++ {
					eCurr[j] += sbrAbs2(d.high[kx+j][i])
				}
				eCurr[j] /= n
			}
		} else {
			for i := 0; i+1 < len(tab); i++ {
				var sum float64
				for k := tab[i]; k < tab[i+1]; k++ {
					for sl := lo; sl < hi; sl++ {
						sum += sbrAbs2(d.high[k][sl])
					}
				}
				mean := sum / (n * float64(tab[i+1]-tab[i]))
				for k := tab[i]; k < tab[i+1]; k++ {
					eCurr[k-kx] = mean
				}
			}
		}
		noAttack := e != int(c.eA[1]) && e != int(c.eA[0])
		for j := 0; j < m; j++ {
			share := eo[j] / (1 + qo[j])
			qm[j] = math.Sqrt(share * qo[j])
			if onset[j] {
				sm[j] = math.Sqrt(share)
			}
			switch {
			case band[j]:
				gain[j] = math.Sqrt(eo[j] * qo[j] / ((1 + eCurr[j]) * (1 + qo[j])))
			case noAttack:
				gain[j] = math.Sqrt(eo[j] / ((1 + eCurr[j]) * (1 + qo[j])))
			default:
				gain[j] = math.Sqrt(eo[j] / (1 + eCurr[j]))
			}
			if !noAttack {
				qm[j] = 0
			}
		}
		for b := 0; b < t.nLim; b++ {
			j0, j1 := t.fLim[b]-kx, t.fLim[b+1]-kx
			var sumO, sumC float64
			for j := j0; j < j1; j++ {
				sumO += eo[j]
				sumC += eCurr[j]
			}
			gmax := math.Min(oracleLimiterGain[s.limGains]*math.Sqrt((oracleEpsilon+sumO)/(oracleEpsilon+sumC)), sbrMaxGain)
			var sumNew float64
			for j := j0; j < j1; j++ {
				if gain[j] > gmax {
					scale := gmax / gain[j]
					qm[j] *= scale
					gain[j] = gmax
				}
				sumNew += eCurr[j]*gain[j]*gain[j] + sm[j]*sm[j]
				if sm[j] == 0 {
					sumNew += qm[j] * qm[j]
				}
			}
			boost := math.Min(math.Sqrt((oracleEpsilon+sumO)/(oracleEpsilon+sumNew)), sbrMaxGainBoost)
			for j := j0; j < j1; j++ {
				gain[j] *= boost
				qm[j] *= boost
				sm[j] *= boost
			}
		}
		smooth := smoothing && noAttack
		for i := lo; i < hi; i++ {
			if s.refill[ch] {
				for k := range a.hist {
					for j := 0; j < m; j++ {
						a.hist[k][0][j], a.hist[k][1][j] = gain[j], qm[j]
					}
				}
				s.refill[ch] = false
				a.noiseIdx = 0
			}
			for j := 0; j < m; j++ {
				g, q := gain[j]*oracleSmooth[0], qm[j]*oracleSmooth[0]
				if smooth {
					for k := 1; k <= sbrSmoothOrder; k++ {
						h := &a.hist[(a.histPos+1-k+2*sbrSmoothOrder)%sbrSmoothOrder]
						g += h[0][j] * oracleSmooth[k]
						q += h[1][j] * oracleSmooth[k]
					}
				} else {
					g, q = gain[j], qm[j]
				}
				a.noiseIdx = (a.noiseIdx + 1) & 511
				z := d.high[kx+j][i] * complex(g, 0)
				if sm[j] != 0 {
					z += oracleSinusoid(sm[j], a.sineIdx, kx+j)
				} else {
					nz := sbrNoiseTable[a.noiseIdx]
					z += complex(nz[0]*q, nz[1]*q)
				}
				y[i][kx+j] = z
			}
			a.sineIdx = (a.sineIdx + 1) & 3
			a.histPos = (a.histPos + 1) % sbrSmoothOrder
			for j := 0; j < m; j++ {
				a.hist[a.histPos][0][j], a.hist[a.histPos][1][j] = gain[j], qm[j]
			}
		}
		a.prevS = onset
	}
}

func oracleSinusoid(lv float64, ph, k int) complex128 {
	sign := 1.0
	if k&1 != 0 {
		sign = -1
	}
	switch ph {
	case 0:
		return complex(lv, 0)
	case 1:
		return complex(0, sign*lv)
	case 2:
		return complex(-lv, 0)
	}
	return complex(0, -sign*lv)
}

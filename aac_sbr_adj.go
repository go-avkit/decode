// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "math"

// SBR envelope adjustment (HE-AAC v1): the generated high band is given the spectral
// envelope the stream describes. Per envelope and subband the energy of the
// generated signal is measured, a gain brings it to the transmitted energy, noise
// and sinusoids the stream asks for are added, a limiter keeps a gain from growing
// far above its neighbours' and a gain boost restores the energy the limiter took,
// and over time the gains and noise levels are smoothed.
//
// Written from the algorithm of ISO/IEC 14496-3 subpart 4 as the author knows it
// (clauses not verified; the reference decoders' source was not reopened while this
// was written; ffmpeg n9.0.2 aacsbr.c:239-259 and 296-347, the gain formulas and the
// smoothing filter, came up in a search and were read). Where the algorithm leaves a
// choice that an arithmetic check cannot settle, the choice is the one that makes the
// decoder's output equal ffmpeg 9.0.2's on the HE-AAC fixtures to float32 rounding,
// found by trying the alternatives (TestSBRAdjust): the default bs_smoothing_mode 1 is
// no smoothing; the noise index starts at 1 and steps once per subband and slot; the
// sinusoid phase index starts at 0 and steps once per slot; the limiter caps the noise
// level with the gain but not the sinusoid. The two tables are the standard's: the
// noise table (aac_sbr_noise_table.go, bare values) and the smoothing filter, which is
// computed.
//
// Everything is float64 and the synthesis rounds once.

// Constants of the algorithm.
const (
	sbrEpsilon      = 1e-12       // keeps a ratio of sums finite
	sbrMaxGainBoost = 1.584893192 // 10^0.2: the boost never exceeds +2 dB
	sbrMaxGain      = 1e5         // no limiter band's cap on a gain is above this
	sbrNoiseOffset  = 6           // noise floor scalefactor zero is 2^6
	sbrSmoothTaps   = 5           // taps of the smoothing filter
	sbrSmoothOrder  = sbrSmoothTaps - 1
)

// sbrLimiterGain is the largest gain relative to the band's average gain, by
// bs_limiter_gains: -3 dB, 0 dB, +3 dB, no limit.
var sbrLimiterGain = [4]float64{0.70795, 1.0, 1.41254, 1e10}

// sbrSmooth is the smoothing filter, a raised cosine: (1+cos(n*pi/5))/6, which sums
// to 1 over n = 0..4 (0.3333, 0.3015, 0.2182, 0.1152, 0.0318).
var sbrSmooth = func() (h [sbrSmoothTaps]float64) {
	for n := range h {
		h[n] = (1 + math.Cos(float64(n)*math.Pi/5)) / 6
	}
	return h
}()

// sbrAdjState is what the adjustment keeps from one frame to the next, per channel.
type sbrAdjState struct {
	eOrig [sbrMaxEnv][sbrMaxBands]float64 // dequantised envelope energies, by table band
	qOrig [2][sbrMaxNoise]float64         // dequantised noise floor, by noise band

	hist     [sbrSmoothOrder][2][sbrSynBands]float64 // gains and noise levels of the last 4 slots
	histPos  int                                     // the newest entry of hist
	noiseIdx int
	sineIdx  int
	prevS    [sbrSynBands]bool // sinusoid present in the last envelope of the previous frame

	// y is the adjusted high band by slot and band (Y), and tail the last 6 slots of
	// the previous frame's, which the first slots of this frame still carry.
	y       [sbrHFSlots][sbrSynBands]complex128
	tail    [sbrHFSlots - sbrQMFSlots][sbrSynBands]complex128
	prevKx  int
	prevM   int
	prevEnd int // the last envelope border of the previous frame, 0 for none
}

// dequantise computes the transmitted energies of both channels of the element:
// E_orig = 64*2^(q/a), a = 2 for a 1.5 dB step and 1 for 3 dB; the noise floor
// 2^(6-q); for a coupled pair the second channel carries the balance, and the left
// and right values split the first channel's by it.
func (s *sbrState) dequantise(nch int) {
	for ch := 0; ch < nch; ch++ {
		c := &s.ch[ch]
		a := 2.0
		if c.ampRes != 0 {
			a = 1
		}
		d := &s.channelDSP(ch).adj
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
	// coupled: ch 0 holds the sum (level), ch 1 the balance
	c0, c1 := &s.ch[0], &s.ch[1]
	a, pan := 2.0, 24.0
	if c0.ampRes != 0 {
		a, pan = 1, 12
	}
	l, r := &s.channelDSP(0).adj, &s.channelDSP(1).adj
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

// adjust computes the adjusted high band of channel ch for the envelopes of this
// frame, from the generated band and the dequantised energies.
func (s *sbrState) adjust(ch int) {
	d := s.channelDSP(ch)
	a := &d.adj
	c := &s.ch[ch]
	t := &s.t
	kx, m := t.kx[1], t.m[1]
	smoothing := s.smoothing == 0
	copy(a.tail[:], a.y[sbrQMFSlots:])
	for e := 0; e < c.numEnv; e++ {
		var (
			eo, qo, eCurr [sbrSynBands]float64
			gain, qm, sm  [sbrSynBands]float64
			band          [sbrSynBands]bool // a sinusoid in the table band holding the subband
			onset         [sbrSynBands]bool // the sinusoid itself, at its band's midpoint
		)
		res := c.freqRes[e+1]
		tab := t.fLow[:t.n[0]+1]
		if res != 0 {
			tab = t.fHigh[:t.n[1]+1]
		}
		lo, hi := 2*int(c.tEnv[e]), min(2*int(c.tEnv[e+1]), sbrHFSlots)
		// energies and noise floor onto subbands
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
		// sinusoids: at the midpoint of a high resolution band, from the attack on
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
		// measured energy of the generated band
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
		// gain, noise level and sinusoid level of each subband
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
				qm[j] = 0 // no noise is added in an attack envelope, nor counted in its boost
			}
		}
		// limiter bands: cap the gains, then boost what is left to the energy asked
		for b := 0; b < t.nLim; b++ {
			j0, j1 := t.fLim[b]-kx, t.fLim[b+1]-kx
			var sumO, sumC float64
			for j := j0; j < j1; j++ {
				sumO += eo[j]
				sumC += eCurr[j]
			}
			gmax := math.Min(sbrLimiterGain[s.limGains]*math.Sqrt((sbrEpsilon+sumO)/(sbrEpsilon+sumC)), sbrMaxGain)
			var sumNew float64
			for j := j0; j < j1; j++ {
				if gain[j] > gmax {
					scale := gmax / gain[j]
					qm[j] *= scale // the sinusoid is not limited, the noise is
					gain[j] = gmax
				}
				sumNew += eCurr[j]*gain[j]*gain[j] + sm[j]*sm[j]
				if sm[j] == 0 {
					sumNew += qm[j] * qm[j]
				}
			}
			boost := math.Min(math.Sqrt((sbrEpsilon+sumO)/(sbrEpsilon+sumNew)), sbrMaxGainBoost)
			for j := j0; j < j1; j++ {
				gain[j] *= boost
				qm[j] *= boost
				sm[j] *= boost
			}
		}
		// slots: smoothed gain and noise level, noise, sinusoid
		smooth := smoothing && noAttack
		for i := lo; i < hi; i++ {
			if s.refill[ch] {
				// after a header reset the history is the first slot's own gains, so the
				// filter starts at rest
				for k := range a.hist {
					for j := 0; j < m; j++ {
						a.hist[k][0][j], a.hist[k][1][j] = gain[j], qm[j]
					}
				}
				s.refill[ch] = false
			}
			for j := 0; j < m; j++ {
				g, q := gain[j]*sbrSmooth[0], qm[j]*sbrSmooth[0]
				if smooth {
					for k := 1; k <= sbrSmoothOrder; k++ {
						h := &a.hist[(a.histPos+1-k+2*sbrSmoothOrder)%sbrSmoothOrder]
						g += h[0][j] * sbrSmooth[k]
						q += h[1][j] * sbrSmooth[k]
					}
				} else {
					g, q = gain[j], qm[j]
				}
				a.noiseIdx = (a.noiseIdx + 1) & 511
				z := d.high[kx+j][i] * complex(g, 0)
				if sm[j] != 0 {
					z += sbrSinusoid(sm[j], a.sineIdx, kx+j)
				} else {
					nz := sbrNoiseTable[a.noiseIdx]
					z += complex(nz[0]*q, nz[1]*q)
				}
				a.y[i][kx+j] = z
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

// sbrSinusoid is a sinusoid of level lv at phase index ph (a quarter turn per slot)
// in the absolute subband k: the real part {1, 0, -1, 0}, the imaginary part
// {0, 1, 0, -1}, negated in odd subbands.
func sbrSinusoid(lv float64, ph, k int) complex128 {
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

// frame runs the signal path over the nch channels of an element: analysis of the
// core signal pcm, then for a frame that carries SBR data the high band generation
// and adjustment, then synthesis into out (2048 samples each, at twice the rate).
// A frame without SBR data is pure upsampling.
func (s *sbrState) frame(nch int, pcm, out [2][]float32) { s.process(nch, pcm, out, s.started) }

// process is frame, with the choice of using the SBR data the state holds or not: a
// frame whose fill element carried none is upsampled even when an earlier frame's
// data is still in the state.
func (s *sbrState) process(nch int, pcm, out [2][]float32, withSBR bool) {
	// a state that a later fill element of the same frame turned off (two elements
	// of one type and tag share a state) holds no valid tables
	withSBR = withSBR && s.started
	if withSBR {
		s.dequantise(nch)
	}
	for ch := 0; ch < nch; ch++ {
		d := s.channelDSP(ch)
		d.analyse(pcm[ch])
		kx, m, end := 32, 0, 0
		if withSBR {
			s.hfGenerate(ch)
			s.adjust(ch)
			kx, m, end = s.t.kx[1], s.t.m[1], int(s.ch[ch].tEnv[s.ch[ch].numEnv])
		}
		d.assemble(&d.x, kx, m, end)
		if s.psHybridTest && nch == 1 {
			s.psRoundTrip(d, kx)
		}
		d.syn.process(out[ch], &d.x)
	}
}

// assemble builds the 64 bands of the slots of a frame: the low band from the
// analysis (delayed by sbrHFAdj slots) below the start frequency, the adjusted high
// band above it, and over the first slots, which the previous frame's last envelope
// border carries, the previous frame's tail and start frequency. SBR off is kx = 32.
func (d *sbrChanDSP) assemble(x *[sbrQMFSlots][sbrSynBands]complex128, kx, m, end int) {
	a := &d.adj
	carry := max(2*a.prevEnd-sbrQMFSlots, 0)
	for l := range x {
		k0, m0 := kx, m
		if l < carry {
			k0, m0 = a.prevKx, a.prevM
		}
		row := &x[l]
		*row = [sbrSynBands]complex128{}
		for k := 0; k < k0; k++ {
			row[k] = d.low[k][l+sbrHFAdj]
		}
		for k := k0; k < k0+m0; k++ {
			if l < carry {
				row[k] = a.tail[l][k]
			} else {
				row[k] = a.y[l][k]
			}
		}
	}
	a.prevKx, a.prevM, a.prevEnd = kx, m, end
}

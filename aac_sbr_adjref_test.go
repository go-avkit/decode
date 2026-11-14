// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
)

// The reference of the envelope adjustment, float64, written for clarity and not for
// speed, and shaped differently from the decoder: every quantity is a function of an
// envelope and an ABSOLUTE subband, found by scanning the tables (nothing is mapped
// onto arrays first), the limiter sums are taken where they are needed, the history
// of the smoothing is a list of every slot processed so far, and the noise and sine
// indexes are computed from counters of slots and subbands.

type refAdjChan struct {
	gains, noises [][]float64 // per processed slot: the boosted gain and noise level, by absolute subband
	slots         int         // processed slots
	bands         int         // subbands visited by the noise generator
	prevOn        map[int]bool
	refill        bool
	prevY         [][sbrSynBands]complex128 // slots 32.. of the previous frame's Y
	prevKx, prevM int
	prevEnd       int
}

func newRefAdjChan() *refAdjChan { return &refAdjChan{prevOn: map[int]bool{}} }

// refEnergies returns the dequantised envelope energies [channel][envelope][band]
// and noise floors [channel][set][band] of an element's channels, by the formulas of
// the standard written as powers.
func refEnergies(s *sbrState, nch int) (env [2][sbrMaxEnv][]float64, noise [2][2][]float64) {
	for ch := 0; ch < nch; ch++ {
		c := &s.ch[ch]
		step := 0.5 // exponent per scalefactor unit
		if c.ampRes != 0 {
			step = 1
		}
		for e := 0; e < c.numEnv; e++ {
			n := s.t.n[c.freqRes[e+1]]
			env[ch][e] = make([]float64, n)
			for i := range env[ch][e] {
				env[ch][e][i] = math.Pow(2, 6+step*float64(c.envQ[e+1][i]))
			}
		}
		for k := 0; k < c.numNoise; k++ {
			noise[ch][k] = make([]float64, s.t.nQ)
			for i := range noise[ch][k] {
				noise[ch][k][i] = math.Pow(2, 6-float64(c.noiseQ[k+1][i]))
			}
		}
	}
	if s.coupling && nch == 2 {
		c0, c1 := &s.ch[0], &s.ch[1]
		step, centre := 0.5, 12.0 // balance 0 is a ratio of 2^-12 in the exponent
		if c0.ampRes != 0 {
			step, centre = 1, 12
		}
		for e := 0; e < c0.numEnv; e++ {
			for i := range env[0][e] {
				both := math.Pow(2, 7+step*float64(c0.envQ[e+1][i]))
				b := step * float64(c1.envQ[e+1][i])
				env[0][e][i] = both / (1 + math.Pow(2, centre-b))
				env[1][e][i] = both / (1 + math.Pow(2, b-centre))
			}
		}
		for k := 0; k < c0.numNoise; k++ {
			for i := range noise[0][k] {
				both := math.Pow(2, 7-float64(c0.noiseQ[k+1][i]))
				b := float64(c1.noiseQ[k+1][i])
				noise[0][k][i] = both / (1 + math.Pow(2, 12-b))
				noise[1][k][i] = both / (1 + math.Pow(2, b-12))
			}
		}
	}
	return env, noise
}

// frame adjusts one channel's frame: eo and qo are its energies from refEnergies,
// high the generated band (by absolute subband and slot).
func (r *refAdjChan) frame(s *sbrState, ch int, eo [sbrMaxEnv][]float64, qo [2][]float64, high *[sbrSynBands][sbrHFSlots]complex128) (y [sbrHFSlots][sbrSynBands]complex128) {
	t, c := &s.t, &s.ch[ch]
	kx, kEnd := t.kx[1], t.kx[1]+t.m[1]
	table := func(e int) []int {
		if c.freqRes[e+1] != 0 {
			return t.fHigh[:t.n[1]+1]
		}
		return t.fLow[:t.n[0]+1]
	}
	// index of the entry of tab with tab[i] <= k < tab[i+1]
	find := func(tab []int, k int) int {
		for i := 0; i+1 < len(tab); i++ {
			if tab[i] <= k && k < tab[i+1] {
				return i
			}
		}
		return -1
	}
	noiseBand := func(k int) int { return find(t.fNoise[:t.nQ+1], k) }
	isAttack := func(e int) bool { return e == int(c.eA[1]) || e == int(c.eA[0]) }

	on := func(e, k int) bool { // a sinusoid at subband k in envelope e
		if !c.harmonic {
			return false
		}
		i := find(t.fHigh[:t.n[1]+1], k)
		if i < 0 || (t.fHigh[i]+t.fHigh[i+1])>>1 != k || c.addHarm[i] == 0 {
			return false
		}
		return e >= int(c.eA[1]) || r.prevOn[k]
	}
	inBand := func(e, k int) bool { // a sinusoid anywhere in the table band of k
		tab := table(e)
		i := find(tab, k)
		for q := tab[i]; q < tab[i+1]; q++ {
			if on(e, q) {
				return true
			}
		}
		return false
	}
	slotsOf := func(e int) (int, int) { return 2 * int(c.tEnv[e]), min(2*int(c.tEnv[e+1]), sbrHFSlots) }
	measured := func(e, k int) float64 {
		lo, hi := slotsOf(e)
		if s.interpolFreq != 0 {
			var sum float64
			for sl := lo; sl < hi; sl++ {
				z := high[k][sl]
				sum += real(z)*real(z) + imag(z)*imag(z)
			}
			return sum / float64(hi-lo)
		}
		tab := table(e)
		i := find(tab, k)
		var sum float64
		for q := tab[i]; q < tab[i+1]; q++ {
			for sl := lo; sl < hi; sl++ {
				z := high[q][sl]
				sum += real(z)*real(z) + imag(z)*imag(z)
			}
		}
		return sum / float64((hi-lo)*(tab[i+1]-tab[i]))
	}
	target := func(e, k int) float64 { return eo[e][find(table(e), k)] }
	noiseOf := func(e, k int) float64 {
		set := 0
		if c.numNoise > 1 && c.tEnv[e] >= c.tQ[1] {
			set = 1
		}
		return qo[set][noiseBand(k)]
	}
	rawGain := func(e, k int) float64 {
		E, Q, M := target(e, k), noiseOf(e, k), measured(e, k)
		switch {
		case inBand(e, k):
			return math.Sqrt(E / (1 + M) * Q / (1 + Q))
		case isAttack(e):
			return math.Sqrt(E / (1 + M))
		}
		return math.Sqrt(E / (1 + M) / (1 + Q))
	}
	// the noise level asked of subband k; an attack envelope has none, and so adds
	// none and counts none
	noiseLevel := func(e, k int) float64 {
		allowed := 1.0
		if isAttack(e) {
			allowed = 0
		}
		return allowed * math.Sqrt(target(e, k)/(1+noiseOf(e, k))*noiseOf(e, k))
	}
	sineLevel := func(e, k int) float64 {
		if !on(e, k) {
			return 0
		}
		return math.Sqrt(target(e, k) / (1 + noiseOf(e, k)))
	}
	limits := [4]float64{math.Pow(10, -0.15), 1, math.Pow(10, 0.15), 1e10}
	limiter := func(k int) (lo, hi int) {
		for b := 0; b < t.nLim; b++ {
			if t.fLim[b] <= k && k < t.fLim[b+1] {
				return t.fLim[b], t.fLim[b+1]
			}
		}
		return k, k
	}
	// capped gain, capped noise level, boost of a subband
	cap := func(e, k int) (g, q float64) {
		lo, hi := limiter(k)
		var sumT, sumM float64
		for q := lo; q < hi; q++ {
			sumT += target(e, q)
			sumM += measured(e, q)
		}
		gmax := limits[s.limGains] * math.Sqrt((1e-12+sumT)/(1e-12+sumM))
		if gmax > 1e5 {
			gmax = 1e5
		}
		g, q = rawGain(e, k), noiseLevel(e, k)
		if g > gmax {
			q *= gmax / g
			g = gmax
		}
		return g, q
	}
	boosted := func(e, k int) (g, q, sn float64) {
		lo, hi := limiter(k)
		var sumT, sumOut float64
		for p := lo; p < hi; p++ {
			gp, qp := cap(e, p)
			sp := sineLevel(e, p)
			sumT += target(e, p)
			sumOut += measured(e, p)*gp*gp + sp*sp
			if sp == 0 {
				sumOut += qp * qp
			}
		}
		b := math.Min(math.Sqrt((1e-12+sumT)/(1e-12+sumOut)), 1.584893192)
		g, q = cap(e, k)
		return g * b, q * b, sineLevel(e, k) * b
	}

	for e := 0; e < c.numEnv; e++ {
		lo, hi := slotsOf(e)
		for sl := lo; sl < hi; sl++ {
			// the gains of every subband for this slot, then smoothed over the slots before
			gs, qs := make([]float64, sbrSynBands), make([]float64, sbrSynBands)
			sines := make([]float64, sbrSynBands)
			for k := kx; k < kEnd; k++ {
				gs[k], qs[k], sines[k] = boosted(e, k)
			}
			if r.refill { // after a header reset the history is four copies of this first slot
				r.gains = [][]float64{gs, gs, gs, gs}
				r.noises = [][]float64{qs, qs, qs, qs}
				r.refill = false
				r.bands = 0 // the noise table starts again at a header reset, the sinusoid phase does not
			}
			for k := kx; k < kEnd; k++ {
				g, q := gs[k], qs[k]
				if s.smoothing == 0 && !isAttack(e) {
					g, q = 0, 0
					for n := 0; n < sbrSmoothTaps; n++ {
						w := (1 + math.Cos(float64(n)*math.Pi/5)) / 6
						if n == 0 {
							g, q = g+w*gs[k], q+w*qs[k]
						} else if idx := len(r.gains) - n; idx >= 0 {
							g, q = g+w*r.gains[idx][k], q+w*r.noises[idx][k]
						}
					}
				}
				r.bands++
				v := high[k][sl] * complex(g, 0)
				if sines[k] != 0 {
					sign := 1.0
					if k%2 == 1 {
						sign = -1
					}
					switch r.slots & 3 {
					case 0:
						v += complex(sines[k], 0)
					case 1:
						v += complex(0, sign*sines[k])
					case 2:
						v -= complex(sines[k], 0)
					case 3:
						v -= complex(0, sign*sines[k])
					}
				} else {
					n := sbrNoiseTable[r.bands&511]
					v += complex(n[0]*q, n[1]*q)
				}
				y[sl][k] = v
			}
			r.gains, r.noises = append(r.gains, gs), append(r.noises, qs)
			r.slots++
		}
		// the sinusoids of this envelope are the next one's "previous"
		for k := kx; k < kEnd; k++ {
			r.prevOn[k] = on(e, k)
		}
	}
	return y
}

// assemble gives the 64 bands of the 32 slots of a frame from the low band (analysis
// of the core signal, slots l+2 of the 40) and the adjusted band.
func (r *refAdjChan) assemble(low *[sbrAnaBands][sbrLowSlots]complex128, y *[sbrHFSlots][sbrSynBands]complex128, kx, m, end int) (x [sbrQMFSlots][sbrSynBands]complex128) {
	carry := 2*r.prevEnd - sbrQMFSlots
	for l := 0; l < sbrQMFSlots; l++ {
		fromOld := l < carry
		k0, m0 := kx, m
		if fromOld {
			k0, m0 = r.prevKx, r.prevM
		}
		for k := 0; k < k0; k++ {
			x[l][k] = low[k][l+2]
		}
		for k := k0; k < k0+m0; k++ {
			if fromOld {
				x[l][k] = r.prevY[l][k]
			} else {
				x[l][k] = y[l][k]
			}
		}
	}
	r.prevY = make([][sbrSynBands]complex128, sbrHFSlots-sbrQMFSlots)
	for l := range r.prevY {
		r.prevY[l] = y[sbrQMFSlots+l]
	}
	r.prevKx, r.prevM, r.prevEnd = kx, m, end
	return x
}

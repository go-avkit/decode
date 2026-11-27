// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"unsafe"

	"github.com/go-simd/floats"
)

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
// Everything from the dequantisation to the adjusted band is binary32 (the float64
// adjustment of v0.17.1 is the oracle of the tests, aac_sbr_adjf64_test.go, which
// assert a stated bound on the PCM, TestSBRAdjustBound). The high band the generator
// leaves in float64 is narrowed once per frame, as it is transposed to the slot-major
// rows the application reads; that pass also measures the energy of every envelope.
//
// The application of a slot is vector work on rows of 2m binary32 (m complex): the
// high band row times the gain row (each gain repeated for the real and imaginary
// part), plus the noise table's window times the noise level row, plus the few
// sinusoids. A row of the smoothing filter's input is the envelope's own row, so the
// filter is evaluated only for the first four slots of an envelope, whose history
// reaches into the previous one; for the others the weights, which sum to one, give
// the row back, and it is used as it stands.

// Constants of the algorithm.
const (
	sbrEpsilon      = 1e-12       // keeps a ratio of sums finite
	sbrMaxGainBoost = 1.584893192 // 10^0.2: the boost never exceeds +2 dB
	sbrMaxGain      = 1e5         // no limiter band's cap on a gain is above this
	sbrNoiseOffset  = 6           // noise floor scalefactor zero is 2^6
	sbrSmoothTaps   = 5           // taps of the smoothing filter
	sbrSmoothOrder  = sbrSmoothTaps - 1
	// sbrMaxEnergy bounds a measured energy: a corrupt stream can make the generated
	// band huge, and an infinite energy times a zero gain would be NaN.
	sbrMaxEnergy = 1e30
	// sbrMaxDequant is the largest dequantised energy or noise level taken as it is:
	// a corrupt scalefactor beyond it is replaced by 1. At 1e30 no sum or square the
	// adjustment forms from two such values leaves binary32 (the gain is capped at 1e5
	// before it multiplies an energy).
	sbrMaxDequant = 1e30
	// sbrNoisePad is the number of complex entries the padded noise table repeats
	// after its end, so that a row of up to sbrSynBands entries never wraps.
	sbrNoisePad = sbrSynBands
	// sbrAdjRows is the height of the gain and noise level tables: the four slots of
	// history, then the slots of the frame.
	sbrAdjRows = sbrSmoothOrder + sbrHFSlots
)

// sbrLimiterGain is the largest gain relative to the band's average gain, by
// bs_limiter_gains: -3 dB, 0 dB, +3 dB, no limit.
var sbrLimiterGain = [4]float32{0.70795, 1.0, 1.41254, 1e10}

// sbrSmooth is the smoothing filter, a raised cosine: (1+cos(n*pi/5))/6, which sums
// to 1 over n = 0..4 (0.3333, 0.3015, 0.2182, 0.1152, 0.0318).
var sbrSmooth = func() (h [sbrSmoothTaps]float32) {
	for n := range h {
		h[n] = float32((1 + math.Cos(float64(n)*math.Pi/5)) / 6)
	}
	return h
}()

// sbrNoise32 is the noise table as interleaved binary32 followed by its first
// sbrNoisePad entries again, so that a window of a row's length never wraps.
var sbrNoise32 = func() (t [2 * (512 + sbrNoisePad)]float32) {
	for i := range 512 + sbrNoisePad {
		t[2*i], t[2*i+1] = float32(sbrNoiseTable[i&511][0]), float32(sbrNoiseTable[i&511][1])
	}
	return t
}()

// sbrHalf is 2^(r/2) for r = 0, 1: the half step of a 1.5 dB scalefactor.
var sbrHalf = [2]float32{1, math.Sqrt2}

// sbrPow2 is 2^e built in the exponent field, exact; the exponent is held to the
// range where the result is a normal binary32 with room for the factors it meets.
func sbrPow2(e int) float32 {
	return math.Float32frombits(uint32(min(max(e, -100), 100)+127) << 23)
}

// sbrExp2 is 2^(q/a) for a scalefactor q and a step a of 1 or 2: an integer power of
// two, times sqrt(2) for the odd q of the half step. q>>1 is the floor, so negative
// q are right.
func sbrExp2(q, a int) float32 {
	if a == 1 {
		return sbrPow2(q)
	}
	return sbrPow2(q>>1) * sbrHalf[q&1]
}

// sbrGuard replaces a dequantised value beyond what a stream can mean by 1.
func sbrGuard(v float32) float32 {
	if v > sbrMaxDequant {
		return 1
	}
	return v
}

// sbrAdjState is what the adjustment keeps from one frame to the next, per channel.
type sbrAdjState struct {
	eOrig [sbrMaxEnv][sbrMaxBands]float32 // dequantised envelope energies, by table band
	qOrig [2][sbrMaxNoise]float32         // dequantised noise floor, by noise band

	// gain and noise level of each slot, by subband, every value written twice (real
	// and imaginary part) so that a row multiplies a row of complex values; the first
	// sbrSmoothOrder rows are the last slots of the previous frame (the history of the
	// smoothing filter), the others the slots of this one.
	gain, noise [sbrAdjRows][2 * sbrSynBands]float32
	noiseIdx    int
	sineIdx     int
	prevS       [sbrSynBands]bool // sinusoid present in the last envelope of the previous frame

	// scratch of one call: the generated band narrowed and transposed (slot by band,
	// interleaved), the energy of each envelope by band, a smoothed row and a masked one
	hf    [sbrHFSlots][2 * sbrSynBands]float32
	eSum  [sbrMaxEnv][sbrSynBands]float32
	gRow  [2 * sbrSynBands]float32
	qRow  [2 * sbrSynBands]float32
	nzRow [2 * sbrSynBands]float32

	// y is the adjusted high band by slot and band (Y), and tail the last 6 slots of
	// the previous frame's, which the first slots of this frame still carry.
	y       [sbrHFSlots][sbrSynBands]complex64
	tail    [sbrHFSlots - sbrQMFSlots][sbrSynBands]complex64
	prevKx  int
	prevM   int
	prevEnd int // the last envelope border of the previous frame, 0 for none
}

// sbrAdjOracle is the float64 adjustment of v0.17.1, which the tests install in place
// of the binary32 one (aac_sbr_adjf64_test.go); nil in the decoder.
type sbrAdjOracle interface {
	dequantise(s *sbrState, nch int)
	adjust(s *sbrState, ch int)
}

// sbrNewAdjOracle, when a test sets it, makes every new SBR state use its oracle.
var sbrNewAdjOracle func() sbrAdjOracle

// dequantise computes the transmitted energies of both channels of the element:
// E_orig = 64*2^(q/a), a = 2 for a 1.5 dB step and 1 for 3 dB; the noise floor
// 2^(6-q); for a coupled pair the second channel carries the balance, and the left
// and right values split the first channel's by it: with the mean level M and the
// ratio r = 2^((pan-bal)/a) the left is M/(1+r) and the right the left times r.
// Powers of two are built, not computed (sbrExp2).
func (s *sbrState) dequantise(nch int) {
	if s.adjOracle != nil {
		s.adjOracle.dequantise(s, nch)
		return
	}
	for ch := 0; ch < nch; ch++ {
		c := &s.ch[ch]
		a := 2
		if c.ampRes != 0 {
			a = 1
		}
		d := &s.channelDSP(ch).adj
		for e := 0; e < c.numEnv; e++ {
			for i := 0; i < s.t.n[c.freqRes[e+1]]; i++ {
				d.eOrig[e][i] = sbrGuard(64 * sbrExp2(int(c.envQ[e+1][i]), a))
			}
		}
		for k := 0; k < c.numNoise; k++ {
			for i := 0; i < s.t.nQ; i++ {
				d.qOrig[k][i] = sbrGuard(sbrPow2(sbrNoiseOffset - int(c.noiseQ[k+1][i])))
			}
		}
	}
	if !s.coupling || nch < 2 {
		return
	}
	// coupled: ch 0 holds the sum (level), ch 1 the balance
	c0, c1 := &s.ch[0], &s.ch[1]
	a, pan := 2, 24
	if c0.ampRes != 0 {
		a, pan = 1, 12
	}
	l, r := &s.channelDSP(0).adj, &s.channelDSP(1).adj
	for e := 0; e < c0.numEnv; e++ {
		for i := 0; i < s.t.n[c0.freqRes[e+1]]; i++ {
			level := 128 * sbrExp2(int(c0.envQ[e+1][i]), a)
			ratio := sbrExp2(pan-int(c1.envQ[e+1][i]), a)
			left := level / (1 + ratio)
			l.eOrig[e][i], r.eOrig[e][i] = sbrGuard(left), sbrGuard(left*ratio)
		}
	}
	for k := 0; k < c0.numNoise; k++ {
		for i := 0; i < s.t.nQ; i++ {
			level := sbrPow2(sbrNoiseOffset - int(c0.noiseQ[k+1][i]) + 1)
			ratio := sbrPow2(12 - int(c1.noiseQ[k+1][i]))
			left := level / (1 + ratio)
			l.qOrig[k][i], r.qOrig[k][i] = sbrGuard(left), sbrGuard(left*ratio)
		}
	}
}

// f32s is a view of n complex64 values as 2n binary32.
func f32s(p *complex64, n int) []float32 { return unsafe.Slice((*float32)(unsafe.Pointer(p)), 2*n) }

// adjust computes the adjusted high band of channel ch for the envelopes of this
// frame, from the generated band and the dequantised energies.
func (s *sbrState) adjust(ch int) {
	if s.adjOracle != nil {
		s.adjOracle.adjust(s, ch)
		return
	}
	d := s.channelDSP(ch)
	a := &d.adj
	c := &s.ch[ch]
	t := &s.t
	kx, m := t.kx[1], t.m[1]
	smoothing := s.smoothing == 0
	copy(a.tail[:], a.y[sbrQMFSlots:])
	first := 2 * int(c.tEnv[0]) // the first slot adjusted: slot i is row sbrSmoothOrder+i-first
	last := min(2*int(c.tEnv[c.numEnv]), sbrHFSlots)

	// The generated band, narrowed and transposed to slot-major rows, and the energy
	// of every envelope in each subband.
	for j := 0; j < m; j++ {
		row := &d.high[kx+j]
		for e := 0; e < c.numEnv; e++ {
			var sum float32
			for i, hi := 2*int(c.tEnv[e]), min(2*int(c.tEnv[e+1]), sbrHFSlots); i < hi; i++ {
				re, im := float32(real(row[i])), float32(imag(row[i]))
				a.hf[i][2*j], a.hf[i][2*j+1] = re, im
				sum += re*re + im*im
			}
			a.eSum[e][j] = sum
		}
	}

	for e := 0; e < c.numEnv; e++ {
		var (
			eo, qo, eCurr [sbrSynBands]float32
			gain, qm, sm  [sbrSynBands]float32
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
		// measured energy of the generated band: one multiply by a reciprocal
		var inv float32
		if hi > lo {
			inv = 1 / float32(hi-lo)
		}
		if s.interpolFreq != 0 {
			for j := 0; j < m; j++ {
				eCurr[j] = min(a.eSum[e][j]*inv, sbrMaxEnergy)
			}
		} else {
			for i := 0; i+1 < len(tab); i++ {
				var sum float32
				for k := tab[i]; k < tab[i+1]; k++ {
					sum += a.eSum[e][k-kx]
				}
				mean := min(sum*(inv/float32(tab[i+1]-tab[i])), sbrMaxEnergy)
				for k := tab[i]; k < tab[i+1]; k++ {
					eCurr[k-kx] = mean
				}
			}
		}
		// gain, noise level and sinusoid level of each subband
		noAttack := e != int(c.eA[1]) && e != int(c.eA[0])
		for j := 0; j < m; j++ {
			share := eo[j] / (1 + qo[j])
			qm[j] = sqrt32(share * qo[j])
			if onset[j] {
				sm[j] = sqrt32(share)
			}
			switch {
			case band[j]:
				gain[j] = sqrt32(share * qo[j] / (1 + eCurr[j])) // eo*qo/((1+eCurr)(1+qo)), no product that can overflow
			case noAttack:
				gain[j] = sqrt32(share / (1 + eCurr[j]))
			default:
				gain[j] = sqrt32(eo[j] / (1 + eCurr[j]))
			}
			if !noAttack {
				qm[j] = 0 // no noise is added in an attack envelope, nor counted in its boost
			}
		}
		// limiter bands: cap the gains, then boost what is left to the energy asked
		for b := 0; b < t.nLim; b++ {
			j0, j1 := t.fLim[b]-kx, t.fLim[b+1]-kx
			var sumO, sumC float32
			for j := j0; j < j1; j++ {
				sumO += eo[j]
				sumC += eCurr[j]
			}
			gmax := min(sbrLimiterGain[s.limGains]*sqrt32((sbrEpsilon+sumO)/(sbrEpsilon+sumC)), sbrMaxGain)
			var sumNew float32
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
			boost := min(sqrt32((sbrEpsilon+sumO)/(sbrEpsilon+sumNew)), sbrMaxGainBoost)
			for j := j0; j < j1; j++ {
				gain[j] *= boost
				qm[j] *= boost
				sm[j] *= boost
			}
		}
		if hi <= lo || lo < first { // no slots (borders out of order in a corrupt stream)
			a.prevS = onset
			continue
		}
		// the envelope's rows of the gain and noise level tables
		g0, q0 := &a.gain[sbrSmoothOrder+lo-first], &a.noise[sbrSmoothOrder+lo-first]
		for j := 0; j < m; j++ {
			g0[2*j], g0[2*j+1] = gain[j], gain[j]
			q0[2*j], q0[2*j+1] = qm[j], qm[j]
		}
		if s.refill[ch] {
			// after a header reset the history is the first slot's own gains, so the
			// filter starts at rest
			for k := 0; k < sbrSmoothOrder; k++ {
				a.gain[k], a.noise[k] = *g0, *q0
			}
			s.refill[ch] = false
			// and the noise table starts again at its first entry (the sinusoid
			// phase is not reset): ffmpeg's output on S17 is 3e-4 off without it
			a.noiseIdx = 0
		}
		for i := lo + 1; i < hi; i++ {
			a.gain[sbrSmoothOrder+i-first], a.noise[sbrSmoothOrder+i-first] = *g0, *q0
		}
		// the sinusoids of the envelope, and which slots need the filter
		var sin [sbrSynBands]int
		ns := 0
		for j := 0; j < m; j++ {
			if sm[j] != 0 {
				sin[ns] = j
				ns++
			}
		}
		smooth := smoothing && noAttack
		w := 2 * m
		for i := lo; i < hi; i++ {
			r := sbrSmoothOrder + i - first
			gr, qr := a.gain[r][:w], a.noise[r][:w]
			if smooth && i-lo < sbrSmoothOrder {
				// the history reaches into the previous envelope
				for x := 0; x < w; x++ {
					g, q := gr[x]*sbrSmooth[0], qr[x]*sbrSmooth[0]
					for k := 1; k <= sbrSmoothOrder; k++ {
						g += a.gain[r-k][x] * sbrSmooth[k]
						q += a.noise[r-k][x] * sbrSmooth[k]
					}
					a.gRow[x], a.qRow[x] = g, q
				}
				gr, qr = a.gRow[:w], a.qRow[:w]
			}
			out := f32s(&a.y[i][kx], m)
			floats.Mul32(out, a.hf[i][:w], gr)
			if noAttack {
				if ns > 0 { // where a sinusoid is, no noise
					if &qr[0] != &a.qRow[0] {
						copy(a.qRow[:w], qr)
						qr = a.qRow[:w]
					}
					for _, j := range sin[:ns] {
						qr[2*j], qr[2*j+1] = 0, 0
					}
				}
				p := 2 * ((a.noiseIdx + 1) & 511)
				floats.Mul32(a.nzRow[:w], sbrNoise32[p:p+w], qr)
				floats.Add32(out, out, a.nzRow[:w])
			}
			for _, j := range sin[:ns] {
				v := sm[j]
				if (kx+j)&1 != 0 {
					v = -v
				}
				switch a.sineIdx {
				case 0:
					out[2*j] += sm[j]
				case 1:
					out[2*j+1] += v
				case 2:
					out[2*j] -= sm[j]
				default:
					out[2*j+1] -= v
				}
			}
			a.noiseIdx = (a.noiseIdx + m) & 511
			a.sineIdx = (a.sineIdx + 1) & 3
		}
		a.prevS = onset
	}
	// the last slots of the frame are the history of the next
	n := last - first
	for k := 0; k < sbrSmoothOrder; k++ {
		a.gain[k], a.noise[k] = a.gain[n+k], a.noise[n+k]
	}
}

// sqrt32 is the correctly rounded binary32 square root.
func sqrt32(x float32) float32 { return float32(math.Sqrt(float64(x))) }

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
		switch {
		case s.psOn && !s.psOffTest && nch == 1:
			s.psApplyFrame(d, kx, m, withSBR, out)
			continue
		case s.psHybridTest && nch == 1:
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
				row[k] = complex128(a.tail[l][k])
			} else {
				row[k] = complex128(a.y[l][k])
			}
		}
	}
	a.prevKx, a.prevM, a.prevEnd = kx, m, end
}

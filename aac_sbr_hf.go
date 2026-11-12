// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

// SBR high frequency generation (HE-AAC v1): from the analysis of the core signal
// (the low band) it builds the subband samples above the crossover by copying
// low-band subbands up, patch by patch, through a prediction-error filter whose
// strength the stream chooses per noise floor band (the inverse filtering modes).
//
// Written from the algorithm of ISO/IEC 14496-3 subpart 4 (clause not verified), and
// derived rather than recalled where it can be: the two filter coefficients of a
// subband are the least-squares solution of
//
//	e(n) = x(n) + a0*x(n-1) + a1*x(n-2),   minimising the sum of |e(n)|^2,
//
// over the 38 samples n = 2..39 of the 40 the subband holds (the last 8 slots of the
// previous frame and the 32 of this one), the covariance method. With
// R(i,j) = sum over n of x(n-i)*conj(x(n-j)) the normal equations are
//
//	R(0,1) + a0*R(1,1) + a1*R(2,1) = 0
//	R(0,2) + a0*R(1,2) + a1*R(2,2) = 0
//
// whose solution is the pair of formulas in inverseFilter. The standard's own
// constants are kept: the 1+1e-6 regularisation of the determinant, the limit of 4
// on a coefficient's magnitude (beyond it both coefficients are zeroed, the pair is
// a failed estimate), the chirp factor table with its smoothing. ffmpeg n9.0.2
// libavcodec/aacsbr.c:149-226, sbrdsp.c:134-184 and aacsbr_template.c:1491-1523 were
// read to confirm these and the patch/noise-band bookkeeping; read, not copied.
//
// Everything is float64; ffmpeg's float32 evaluation of the same estimate (a 1e-6
// regularisation leaves six digits of float32 in a tonal band) is noticeably less
// accurate, which is what its source calls "not numerically stable".

// Layout of the subband arrays.
const (
	// sbrLowSlots is the width of the low band array: sbrHFGen slots of the
	// previous frame, then the slots of this one.
	sbrLowSlots = sbrHFGen + sbrQMFSlots
	sbrHFGen    = 8 // time slots of history the generator needs (t_HFGen)
	// sbrHFAdj is the delay of the generated and adjusted bands against the
	// low band array's slot index: output slot i is built from low-band slots
	// i, i+1, i+2 of the array (t_HFAdj = 2).
	sbrHFAdj = 2
	// sbrHFSlots is the number of slots of the high band: twice the largest
	// envelope border, 19 (the frame's 16 and the 3 a variable grid may add).
	sbrHFSlots = 38
)

// sbrChanDSP is the signal state of one SBR channel, per decoder: the two QMF
// banks, the low band, the generated high band, the filter coefficients and the
// chirp factors that live from one frame to the next.
type sbrChanDSP struct {
	ana sbrAnalysis
	syn sbrSynthesis
	// low is the analysis of the core signal by band and slot (X_low). Bands
	// at or above the start frequency are kept too: they are cheap and the
	// standard defines them.
	low [sbrAnaBands][sbrLowSlots]complex128
	// high[k][i] is the generated band k at envelope slot i (X_high), for k from kx
	// to kx+m-1 and the slots from 2*tEnv[0] up to 2*tEnv[numEnv]: zero in the other
	// slots and in the bands above the last patch. Other rows are not touched.
	high  [sbrSynBands][sbrHFSlots]complex128
	alpha [sbrAnaBands][2]complex128 // a0, a1 of each low band below k0
	bw    [sbrMaxNoise]float64       // chirp factors, one per noise floor band
}

// analyse runs the analysis bank on the 1024 core samples of a frame, keeping the
// last 8 slots of the previous frame at the front of the low band.
func (d *sbrChanDSP) analyse(pcm []float32) {
	for k := range d.low {
		copy(d.low[k][:sbrHFGen], d.low[k][sbrQMFSlots:])
	}
	d.ana.process(pcm, &d.low, sbrHFGen)
}

// inverseFilter estimates the coefficients a0, a1 of the low bands 0 to k0-1 from
// the low band array.
func (d *sbrChanDSP) inverseFilter(k0 int) {
	for k := 0; k < k0; k++ {
		d.alpha[k][0], d.alpha[k][1] = sbrPredictor(&d.low[k])
	}
}

// sbrPredictor returns the coefficients a0, a1 of the prediction-error filter of
// the 40 samples x, zero for a degenerate or failed estimate.
func sbrPredictor(x *[sbrLowSlots]complex128) (a0, a1 complex128) {
	var r11, r22 float64
	var r01, r12, r02 complex128
	for i := 0; i < sbrLowSlots-2; i++ {
		c := sbrConj(x[i])
		r22 += sbrAbs2(x[i])
		r12 += x[i+1] * c
		r02 += x[i+2] * c
	}
	for i := 1; i < sbrLowSlots-1; i++ {
		r11 += sbrAbs2(x[i])
		r01 += x[i+1] * sbrConj(x[i])
	}
	det := r11*r22 - sbrAbs2(r12)/(1+1e-6)
	if det != 0 {
		a1 = sbrDiv(r01*r12-r02*complex(r11, 0), det)
	}
	if r11 != 0 {
		a0 = -sbrDiv(r01+a1*sbrConj(r12), r11)
	}
	if sbrAbs2(a1) >= 16 || sbrAbs2(a0) >= 16 {
		return 0, 0
	}
	return a0, a1
}

func sbrConj(z complex128) complex128 { return complex(real(z), -imag(z)) }

func sbrAbs2(z complex128) float64 { return real(z)*real(z) + imag(z)*imag(z) }

// sbrDiv divides by a real number: two float64 divisions, not the runtime's complex
// division with its overflow scaling.
func sbrDiv(z complex128, f float64) complex128 { return complex(real(z)/f, imag(z)/f) }

// sbrChirpTarget is the chirp factor an inverse filtering mode asks for.
var sbrChirpTarget = [4]float64{0, 0.75, 0.9, 0.98}

// chirp updates the chirp factors of the nQ noise floor bands from the inverse
// filtering modes of this frame and the previous one: a change between none and
// low asks for 0.6, and the factor follows its target faster when it rises than
// when it falls; a factor under 1/64 is zero.
func (d *sbrChanDSP) chirp(c *sbrChannel, nQ int) {
	for i := 0; i < nQ; i++ {
		target := sbrChirpTarget[c.invf[0][i]]
		if c.invf[0][i]+c.invf[1][i] == 1 {
			target = 0.6
		}
		if target < d.bw[i] {
			target = 0.75*target + 0.25*d.bw[i]
		} else {
			target = 0.90625*target + 0.09375*d.bw[i]
		}
		if target < 0.015625 {
			target = 0
		}
		d.bw[i] = target
	}
}

// generate builds the high band from the low band, the coefficients and the
// chirp factors, patch by patch: the band k of the patch comes from the low band p,
// filtered with a0*bw and a1*bw^2 of the noise floor band that holds k:
//
//	X_high[k][i] = X_low[p][i] + a0*bw*X_low[p][i-1] + a1*bw^2*X_low[p][i-2]
//
// (X_low in the slot numbering of the array's offset sbrHFAdj).
func (d *sbrChanDSP) generate(t *sbrTables, c *sbrChannel) {
	start, end := 2*int(c.tEnv[0]), min(2*int(c.tEnv[c.numEnv]), sbrHFSlots)
	k, g := t.kx[1], 0
	for j := 0; j < t.numPatches; j++ {
		for x := 0; x < t.patchNum[j]; x, k = x+1, k+1 {
			p := t.patchStart[j] + x
			for g+1 < t.nQ && k >= t.fNoise[g+1] {
				g++
			}
			bw := d.bw[g]
			a0, a1 := d.alpha[p][0]*complex(bw, 0), d.alpha[p][1]*complex(bw*bw, 0)
			row, src := &d.high[k], &d.low[p]
			clear(row[:])
			for i := start; i < end; i++ {
				row[i] = src[i+sbrHFAdj] + a0*src[i+sbrHFAdj-1] + a1*src[i+sbrHFAdj-2]
			}
		}
	}
	for ; k < t.kx[1]+t.m[1]; k++ {
		clear(d.high[k][:])
	}
}

// channelDSP returns the signal state of channel ch, allocated on first use: the
// ~100 kB of subband arrays exist only for streams that use SBR.
func (s *sbrState) channelDSP(ch int) *sbrChanDSP {
	if s.dsp[ch] == nil {
		s.dsp[ch] = new(sbrChanDSP)
	}
	return s.dsp[ch]
}

// hfGenerate runs the generator for channel ch of a started SBR state: inverse
// filtering of the low band, chirp factors, patching.
func (s *sbrState) hfGenerate(ch int) {
	d := s.channelDSP(ch)
	c := &s.ch[ch]
	d.inverseFilter(min(s.t.k[0], sbrAnaBands))
	d.chirp(c, s.t.nQ)
	d.generate(&s.t, c)
}

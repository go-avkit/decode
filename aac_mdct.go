// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "github.com/go-fft/fft"

// The inverse MDCT of AAC-LC (clause of the standard not verified):
//
//	x[n] = (1/M) * sum over k < M of X[k] cos(pi/M * (n + 1/2 + M/2) * (k + 1/2)), n < 2M
//
// with M = 1024 coefficients (long) or 128 (short). Of the 2M samples the decoder
// keeps M, and those M are, rearranged, a DCT-IV of the spectrum:
//
//	C[m] = sum over k < M of X[k] cos(pi/M * (m + 1/2) * (k + 1/2)),  m < M
//
// which is go-fft's DCT-IV (type 4) on a plan built once per size, in its mixed
// form (DCTMixed, go-fft v0.30): float32 in and out, the arithmetic in float64, the
// conversions fused into the pre- and post-twiddle passes. Each output is the
// float64 result rounded to float32 once, so the error is that rounding (half an
// ulp of the sample) on top of the float64 transform's, not the accumulation of
// log2(M) float32 stages. A reversal and the scale 1/M/32768, which makes a sample
// come out in the range -1 to 1 when the spectrum is in 16-bit units, finish it.
// That scale over two (go-fft's DCT-IV is twice the sum) is a power of two for both
// sizes, so applying it to the float32 is exact.
//
// With C = (s, t) in halves of M/2, the M samples are (t_r, s_r), r the reversal:
// the IMDCT of the spectrum is (t, -t_r, -s_r, -s) and the overlap-add of the
// decoder reads those two quarters.

// aacMDCT is the inverse MDCT of m coefficients to m samples (half of the block).
// It is immutable and shared by every decoder.
type aacMDCT struct {
	m     int
	plan  *fft.DCTPlan // DCT-IV of length m
	scale float32      // 1/m/32768 over two, a power of two
}

// aacMDCTScratch is the working memory of one transform call. An aacMDCT is
// immutable and shared by every decoder; each decoder owns the scratch it passes
// in, so concurrent decoders do not touch each other's.
type aacMDCTScratch struct {
	out []float32 // m values
}

// newScratch returns working memory for this transform.
func (m *aacMDCT) newScratch() *aacMDCTScratch {
	return &aacMDCTScratch{out: make([]float32, m.m)}
}

func newAACMDCT(m int) *aacMDCT {
	return &aacMDCT{m: m, plan: fft.NewDCTPlan(m, 4), scale: float32(1.0 / float64(m) / 32768 / 2)}
}

// inverse writes the m samples of the spectrum -src into dst (the sign of the
// reference decoder's inverse quantiser, which applies its scalefactor gains with a
// negative sign; the transform is linear, so it is applied to the output, in the
// pass that reverses and scales it, not as a pass of its own over the spectrum).
// It is written 0 - x, not -x: that makes -0 of +0, and silence would come out as
// -0. It works in sc, which came from newScratch of this transform and is not shared
// between goroutines.
func (t *aacMDCT) inverse(dst, src []float32, sc *aacMDCTScratch) {
	m, h := t.m, t.m/2
	c := t.plan.DCTMixed(sc.out, src[:m], fft.NormBackward)
	// (t_r, s_r) from C = (s, t), negated (below), in one pass over the output with
	// the slices cut to h so that no index needs a bounds check
	lo, hi := dst[:h], dst[h:m]
	cs, ct := c[:h], c[h:m]
	k := t.scale
	for i := range lo {
		lo[i] = 0 - ct[h-1-i]*k
		hi[i] = 0 - cs[h-1-i]*k
	}
}

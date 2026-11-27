// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"

	"github.com/go-fft/fft"
)

// The error bounds of the float32 QMF banks (go-avkit/decode#77). The banks run in
// single precision: the history, the windows, the window sums (floats.MulAddRows32, one
// fused float32 step per tap) and the DCT-IV (fft.DCTPlan32.DCTPair and DCTQuad,
// relative 2-norm error at most eps32*log2(64) of its stream, asserted against
// double-double by go-fft). The bounds below are derived from those parts, per slot, and
// asserted against the definition evaluated in float64 (refAnalysis, refSynthesis: 1e-13
// of the signal, so the oracle's own error is a thousand times under the bound).
//
// Synthesis, per slot with v the exact 128 values and v_b the blocks of the last ten
// slots: Delta v has 2-norm at most epsV*|v|, epsV = eps32*6 (the transform) + 2^-24
// (the inputs rounded to float32) + 2^-24 (the butterfly's one float32 subtraction or
// sum per value; the 2^-22 scale is exact), because the butterfly is a map of 2-norm
// sqrt(2) on both sides. The window sum then adds, per output q,
//
//	|Delta out_q| <= sum_b |c_bq| |Delta v_b| + 11*2^-24 * sum_b |c_bq g_bq|
//
// (ten fused steps, plus the window rounded to float32), and the bound asserted is the
// 2-norm over the 64 outputs of a slot, using |Delta g_bq| <= |Delta v_b|_2 and
// |c_b|_inf for the first term.
//
// Analysis, per slot: the window sum has |Delta u_n| <= 6*2^-24 * sum_j |c x| (five fused
// steps and the window rounded), the fold is a signed permutation, the one float32
// transform (DCTQuad, same stated bound as DCTPair) adds sbrDCT32Bound times the 2-norm
// of its output, and the 64 outputs are the 64 values of the 32 bands (real parts and
// negated reversed imaginary parts), so the 2-norm over the bands is that of the
// transform: at most (6*2^-24 + sbrDCT32Bound) * g * |m|_2 with m_n = sum_j |c x| and g
// the gain of the transform with its factor 2 (|DCT x| = g |x|).
const (
	sbrEps32      = 0x1p-23
	sbrDCT32Bound = 6 * sbrEps32 // eps32*log2(64), the stated bound of DCTPair
	sbrEpsV       = sbrDCT32Bound + 2*0x1p-24
	sbrBoundSlack = 1 + 1e-5 // the second-order terms (rows*2^-24 against 1) and rounding of the bound itself
)

// sbrDCTGain is g: the 2-norm gain of the 64-point DCT-IV the banks use.
func sbrDCTGain() float64 {
	var e, out [sbrSynBands]float64
	e[0] = 1
	sbrDCT4.DCT(out[:], e[:], fft.NormBackward)
	var s float64
	for _, x := range out {
		s += x * x
	}
	return math.Sqrt(s)
}

// sbrAnalysisBound is the bound on the 2-norm over the 32 bands of one analysis slot,
// from h, the 320 samples of its window newest first, in 16-bit units.
func sbrAnalysisBound(h []float64) float64 {
	var m2 float64
	for n := 0; n < sbrSynBands; n++ {
		var m float64
		for j := 0; j < 5; j++ {
			m += math.Abs(sbrAnaWindow[n+64*j] * h[n+64*j])
		}
		m2 += m * m
	}
	return (6*0x1p-24 + sbrDCT32Bound) * sbrBoundSlack * sbrDCTGain() * math.Sqrt(m2)
}

// sbrBandsError is the 2-norm over the bands of the difference of two slots.
func sbrBandsError(got, want func(k int) complex128) float64 {
	var s float64
	for k := 0; k < sbrAnaBands; k++ {
		d := got(k) - want(k)
		s += real(d)*real(d) + imag(d)*imag(d)
	}
	return math.Sqrt(s)
}

// bound is the bound, in +-1.0 output units, on the 2-norm over the 64 outputs of the
// slot just taken by r.slot.
func (r *refSynthesis) bound() float64 {
	var s1 float64
	var mag [sbrSynBands]float64
	for b := 0; b < len(r.blocks) && b < 10; b++ {
		off := 64 * (b % 2)
		var maxc, vn float64
		for q := 0; q < sbrSynBands; q++ {
			c := math.Abs(sbrQMFWindow[64*b+q])
			maxc = math.Max(maxc, c)
			mag[q] += c * math.Abs(r.blocks[b][off+q])
		}
		for _, x := range r.blocks[b] {
			vn += x * x
		}
		s1 += maxc * math.Sqrt(vn)
	}
	var m2 float64
	for _, m := range mag {
		m2 += m * m
	}
	return (sbrEpsV*s1 + 11*0x1p-24*math.Sqrt(m2)) * sbrBoundSlack / sbrPCMUnit
}

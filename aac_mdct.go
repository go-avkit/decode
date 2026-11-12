// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"

	"github.com/go-fft/fft"
)

// The inverse MDCT of AAC-LC (clause of the standard not verified):
//
//	x[n] = (2/N) * sum over k < N/2 of X[k] cos(2*pi/N * (n + n0) * (k + 1/2))
//
// computed through a complex FFT of a quarter of the window length: pre-twiddle,
// FFT, post-twiddle. The FFT is a go-fft plan on complex128, built once per window
// size; the twiddle factors are float64 cos and sin, and the result is rounded to
// float32 once, at the end: the error of the transform is then that single rounding
// (half an ulp of the sample), not the accumulation of log2(N) float32 stages. The
// output is scaled by 1/(N/2)/32768 inside the transform, so that a sample comes
// out in the range -1 to 1 when the spectrum is in 16-bit units.

// aacMDCT is the inverse MDCT of n coefficients to n samples.
type aacMDCT struct {
	n     int
	plan  *fft.Plan
	small *aacSmallMDCT // set for windows below aacGoFFTMin
	pre   []complex128  // n/2 twiddles
	post  []complex128  // n/2 twiddles
}

// aacMDCTScratch is the working memory of one transform call. An aacMDCT is
// immutable and shared by every decoder; each decoder owns the scratch it passes
// in, so concurrent decoders do not touch each other's.
type aacMDCTScratch struct {
	c []complex128 // the go-fft path
	s []cpx        // the split-radix path
}

// newScratch returns working memory for this transform.
func (m *aacMDCT) newScratch() *aacMDCTScratch {
	if m.small != nil {
		return &aacMDCTScratch{s: make([]cpx, m.n/2)}
	}
	return &aacMDCTScratch{c: make([]complex128, m.n/2)}
}

// aacGoFFTMin is the smallest window length that goes through a go-fft plan.
const aacGoFFTMin = 512

func newAACMDCT(n int) *aacMDCT {
	if n < aacGoFFTMin {
		return &aacMDCT{n: n, small: newAACSmallMDCT(n)}
	}
	h := n / 2
	m := &aacMDCT{n: n, plan: fft.NewPlan(h), pre: make([]complex128, h), post: make([]complex128, h)}
	scale := math.Sqrt(1.0 / float64(n) / 32768)
	for i := 0; i < h; i++ {
		alpha := math.Pi / 2 * (float64(i) + 1.0/8.0) / float64(h)
		e := complex(math.Cos(alpha)*scale, math.Sin(alpha)*scale)
		m.pre[i] = e * complex(float64(h), 0) // IFFT divides by h
		m.post[i] = e
	}
	return m
}

// inverse writes the n samples of the spectrum src into dst, working in sc, which
// came from newScratch of this transform and is not shared between goroutines.
func (m *aacMDCT) inverse(dst, src []float32, sc *aacMDCTScratch) {
	if m.small != nil {
		m.small.inverse(dst, src, sc.s)
		return
	}
	h, q := m.n/2, m.n/4
	z := sc.c
	for i := range z {
		z[i] = complex(float64(src[m.n-1-2*i]), float64(src[2*i])) * m.pre[i]
	}
	m.plan.IFFT(z, z)
	for i := 0; i < q; i++ {
		i0, i1 := q+i, q-i-1
		s1 := complex(imag(z[i1]), real(z[i1]))
		s0 := complex(imag(z[i0]), real(z[i0]))
		e1, e0 := m.post[i1], m.post[i0]
		z[i1] = complex(real(s1)*imag(e1)-imag(s1)*real(e1), real(s0)*real(e0)+imag(s0)*imag(e0))
		z[i0] = complex(real(s0)*imag(e0)-imag(s0)*real(e0), real(s1)*real(e1)+imag(s1)*imag(e1))
	}
	for i := 0; i < h; i++ {
		dst[2*i], dst[2*i+1] = float32(real(z[i])), float32(imag(z[i]))
	}
}

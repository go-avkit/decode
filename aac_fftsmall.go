// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/bits"
)

// The same transform as aac_mdct.go, with a split-radix FFT of its own in float64
// instead of a go-fft plan.

// cpx is a complex float64.
type cpx struct{ re, im float64 }

// aacSplitRadixPerm is the input permutation of the split-radix FFT: the position
// of input i in a transform of the given length.
func aacSplitRadixPerm(i, n int) int {
	n >>= 1
	if n <= 1 {
		return i & 1
	}
	if i&n == 0 {
		return aacSplitRadixPerm(i, n) * 2
	}
	n >>= 1
	if i&n == 0 {
		return aacSplitRadixPerm(i, n)*4 + 1
	}
	return aacSplitRadixPerm(i, n)*4 - 1
}

// aacFFT is a split-radix FFT of a power of two length, in place, over a
// permuted input.
type aacFFT struct {
	n    int
	tabs [][]cpx // tabs[bits.Len(m)] holds, for k < m/4, cos(2*pi*k/m) and cos(2*pi*(m/4-k)/m), for every m from 8 to n
	perm []int32 // perm[i] is the input that goes to position i
}

func newAACFFT(n int) *aacFFT {
	f := &aacFFT{n: n, tabs: make([][]cpx, bits.Len(uint(n))+1), perm: make([]int32, n)}
	for m := 8; m <= n; m <<= 1 {
		t := make([]float64, m/4+1)
		freq := 2 * math.Pi / float64(m)
		for i := 0; i < m/4; i++ {
			t[i] = math.Cos(float64(i) * freq)
		}
		w := make([]cpx, m/4)
		for k := range w {
			w[k] = cpx{t[k], t[m/4-k]}
		}
		f.tabs[bits.Len(uint(m))] = w
	}
	for i := range f.perm {
		f.perm[i] = int32(-aacSplitRadixPerm(i, n) & (n - 1))
	}
	return f
}

// run transforms z in place. z must hold the input already permuted by f.perm.
func (f *aacFFT) run(z []cpx) { f.transform(z, f.n) }

func (f *aacFFT) transform(z []cpx, n int) {
	switch n {
	case 2:
		fft2(z)
		return
	case 4:
		fft4(z)
		return
	}
	if n == 8 {
		fft4(z[:4])
		fft2(z[4:6])
		fft2(z[6:8])
		f.combine(z, n)
		return
	}
	f.transform(z[:n/2], n/2)
	f.transform(z[n/2:3*n/4], n/4)
	f.transform(z[3*n/4:n], n/4)
	f.combine(z, n)
}

// fft2 is the two-point transform.
func fft2(z []cpx) {
	a, b := z[0], z[1]
	z[0] = cpx{a.re + b.re, a.im + b.im}
	z[1] = cpx{a.re - b.re, a.im - b.im}
}

// fft4 is the four-point transform of a permuted input.
func fft4(z []cpx) {
	s0, s1, s2, s3 := z[0], z[1], z[2], z[3]
	d0 := s0.re - s1.re
	d1 := s0.re + s1.re
	e0 := s3.re - s2.re
	e1 := s3.re + s2.re
	g0 := s0.im - s1.im
	g1 := s0.im + s1.im
	h0 := s2.im - s3.im
	h1 := s2.im + s3.im
	z[2].re, z[0].re = d1-e1, d1+e1
	z[3].im, z[1].im = g0-e0, g0+e0
	z[3].re, z[1].re = d0-h0, d0+h0
	z[2].im, z[0].im = g1-h1, g1+h1
}

// combine merges the three sub-transforms of z (n/2 points, then two of n/4) into
// one of n points.
func (f *aacFFT) combine(z []cpx, n int) {
	q := n / 4
	tab := f.tabs[bits.Len(uint(n))][:q]
	z0, z1, z2, z3 := z[:q], z[q:2*q], z[2*q:3*q], z[3*q:4*q]
	z1, z2, z3 = z1[:len(z0)], z2[:len(z0)], z3[:len(z0)]
	for k := range z0 {
		wr, wi := tab[k].re, tab[k].im
		a0, a1, a2, a3 := &z0[k], &z1[k], &z2[k], &z3[k]
		// a2 times conj(w) and a3 times w
		t1 := a2.re*wr + a2.im*wi
		t2 := a2.im*wr - a2.re*wi
		t5 := a3.re*wr - a3.im*wi
		t6 := a3.re*wi + a3.im*wr
		r0, i0, r1, i1 := a0.re, a0.im, a1.re, a1.im
		t3 := t5 - t1
		t5 += t1
		a2.re, a0.re = r0-t5, r0+t5
		a3.im, a1.im = i1-t3, i1+t3
		t4 := t2 - t6
		t6 += t2
		a3.re, a1.re = r1-t4, r1+t4
		a2.im, a0.im = i0-t6, i0+t6
	}
}

// aacSmallMDCT is the inverse MDCT of n coefficients to n samples through the
// split-radix FFT above. It serves the short windows: go-fft's plans cost more
// per call than this FFT does for 64 points (measured, see the pull request).
type aacSmallMDCT struct {
	n    int
	fft  *aacFFT
	pre  []cpx   // n/2 twiddles, in the order of the permuted input
	post []cpx   // n/2 twiddles
	idx  []int32 // for each position i, twice the input permutation
	z    []cpx
}

func newAACSmallMDCT(n int) *aacSmallMDCT {
	h := n / 2
	m := &aacSmallMDCT{n: n, fft: newAACFFT(h), pre: make([]cpx, h), post: make([]cpx, h), idx: make([]int32, h), z: make([]cpx, h)}
	scale := math.Sqrt(1.0 / float64(n) / 32768)
	exp := make([]cpx, h)
	for i := range exp {
		alpha := math.Pi / 2 * (float64(i) + 1.0/8.0) / float64(h)
		exp[i] = cpx{math.Cos(alpha) * scale, math.Sin(alpha) * scale}
	}
	for i := 0; i < h; i++ {
		m.pre[i] = exp[m.fft.perm[i]]
		m.post[i] = exp[i]
		m.idx[i] = m.fft.perm[i] * 2
	}
	return m
}

// inverse writes the n samples of the spectrum src into dst.
func (m *aacSmallMDCT) inverse(dst, src []float32) {
	h, q := m.n/2, m.n/4
	z := m.z
	for i := 0; i < h; i++ {
		k := int(m.idx[i])
		re, im := float64(src[m.n-1-k]), float64(src[k])
		e := m.pre[i]
		z[i] = cpx{re*e.re - im*e.im, re*e.im + im*e.re}
	}
	m.fft.run(z)
	for i := 0; i < q; i++ {
		i0, i1 := q+i, q-i-1
		s1 := cpx{z[i1].im, z[i1].re}
		s0 := cpx{z[i0].im, z[i0].re}
		e1, e0 := m.post[i1], m.post[i0]
		z[i1].re = s1.re*e1.im - s1.im*e1.re
		z[i0].im = s1.re*e1.re + s1.im*e1.im
		z[i0].re = s0.re*e0.im - s0.im*e0.re
		z[i1].im = s0.re*e0.re + s0.im*e0.im
	}
	for i := 0; i < h; i++ {
		dst[2*i], dst[2*i+1] = float32(z[i].re), float32(z[i].im)
	}
}

// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "math"

// The inverse MDCT of AAC-LC, ISO/IEC 14496-3 subpart 4, 4.6.11:
//
//	x[n] = (2/N) * sum over k < N/2 of X[k] cos(2*pi/N * (n + n0) * (k + 1/2))
//
// computed through a complex FFT of a quarter of the window length: pre-twiddle,
// FFT, post-twiddle. The float32 result depends on the order of the additions
// and subtractions, so the FFT is a split-radix one, with the butterflies written
// in a fixed order, and the twiddle factors are rounded from float64 once, as
// float32. The output is scaled by 1/(N/2)/32768 inside the transform, so
// that a sample comes out in the range -1 to 1 when the spectrum is in 16-bit
// units.

// cpx is a complex float32.
type cpx struct{ re, im float32 }

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
	// the inverse transform takes the sign that gives 4*p + 1 - 2*(not bit)
	b := 0
	if i&n == 0 {
		b = 1
	}
	return aacSplitRadixPerm(i, n)*4 + 1 - 2*(b^1)
}

// aacFFT is a split-radix FFT of a power of two length, in place, over a
// permuted input.
type aacFFT struct {
	n    int
	tabs map[int][]float32 // cos(2*pi*i/m) for i < m/4, then 0, for every m from 8 to n
	perm []int32           // perm[i] is the input that goes to position i
}

func newAACFFT(n int) *aacFFT {
	f := &aacFFT{n: n, tabs: map[int][]float32{}, perm: make([]int32, n)}
	for m := 8; m <= n; m <<= 1 {
		t := make([]float32, m/4+1)
		freq := 2 * math.Pi / float64(m)
		for i := 0; i < m/4; i++ {
			t[i] = float32(math.Cos(float64(i) * freq))
		}
		f.tabs[m] = t
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
		a, b := z[0], z[1]
		z[0] = cpx{a.re + b.re, a.im + b.im}
		z[1] = cpx{a.re - b.re, a.im - b.im}
		return
	case 4:
		fft4(z)
		return
	}
	f.transform(z[:n/2], n/2)
	f.transform(z[n/2:3*n/4], n/4)
	f.transform(z[3*n/4:n], n/4)
	f.combine(z, n)
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
	tab := f.tabs[n]
	q := n / 4
	for k := 0; k < q; k++ {
		wr, wi := tab[k], tab[q-k]
		a0, a1, a2, a3 := &z[k], &z[k+q], &z[k+2*q], &z[k+3*q]
		// a2 times conj(w) and a3 times w
		t1 := msub(a2.re, wr, a2.im, -wi)
		t2 := madd(a2.re, -wi, a2.im, wr)
		t5 := msub(a3.re, wr, a3.im, wi)
		t6 := madd(a3.re, wi, a3.im, wr)
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

// aacMDCT is the inverse MDCT of n coefficients to n samples.
type aacMDCT struct {
	n    int
	fft  *aacFFT
	pre  []cpx   // n/2 twiddles, in the order of the permuted input
	post []cpx   // n/2 twiddles
	idx  []int32 // for each position i, twice the input permutation
	z    []cpx
}

func newAACMDCT(n int) *aacMDCT {
	h := n / 2
	m := &aacMDCT{n: n, fft: newAACFFT(h), pre: make([]cpx, h), post: make([]cpx, h), idx: make([]int32, h), z: make([]cpx, h)}
	scale := math.Sqrt(1.0 / float64(n) / 32768)
	q := n / 2 // the table length
	exp := make([]cpx, q)
	for i := range exp {
		alpha := math.Pi / 2 * (float64(i) + 1.0/8.0) / float64(q)
		exp[i] = cpx{float32(math.Cos(alpha) * scale), float32(math.Sin(alpha) * scale)}
	}
	for i := 0; i < h; i++ {
		m.pre[i] = exp[m.fft.perm[i]]
		m.post[i] = exp[i]
		m.idx[i] = m.fft.perm[i] * 2
	}
	return m
}

// inverse writes the n samples of the spectrum src into dst.
func (m *aacMDCT) inverse(dst, src []float32) {
	h, q := m.n/2, m.n/4
	z := m.z
	for i := 0; i < h; i++ {
		k := int(m.idx[i])
		re, im := src[m.n-1-k], src[k]
		e := m.pre[i]
		z[i] = cpx{msub(re, e.re, im, e.im), madd(re, e.im, im, e.re)}
	}
	m.fft.run(z)
	for i := 0; i < q; i++ {
		i0, i1 := q+i, q-i-1
		s1 := cpx{z[i1].im, z[i1].re}
		s0 := cpx{z[i0].im, z[i0].re}
		e1, e0 := m.post[i1], m.post[i0]
		z[i1].re = msub(s1.re, e1.im, s1.im, e1.re)
		z[i0].im = madd(s1.re, e1.re, s1.im, e1.im)
		z[i0].re = msub(s0.re, e0.im, s0.im, e0.re)
		z[i1].im = madd(s0.re, e0.re, s0.im, e0.im)
	}
	for i := 0; i < h; i++ {
		dst[2*i], dst[2*i+1] = z[i].re, z[i].im
	}
}

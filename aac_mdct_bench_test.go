// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "testing"

// BenchmarkAACIMDCT times one long (1024 coefficients) and eight short (128)
// transforms, the work of one long frame and one eight-short frame of a channel.
func BenchmarkAACIMDCT(b *testing.B) {
	for _, n := range []int{1024, 128} {
		m := newAACMDCT(n)
		src := make([]float32, n)
		dst := make([]float32, n)
		for i := range src {
			src[i] = float32(i%37-18) * 11
		}
		b.Run(map[int]string{1024: "long", 128: "short"}[n], func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				m.inverse(dst, src)
			}
		})
	}
}

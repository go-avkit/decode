// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/rand"
	"testing"
)

// aacIMDCTDefinition is the inverse MDCT of src in float64 as TestAACMDCTMatchesDefinition
// states it (the spectrum negated, scaled by 1/n/32768), with the largest magnitude of the
// output and the sum of the absolute inputs.
func aacIMDCTDefinition(src []float32) (out []float64, peak, sum float64) {
	n := len(src)
	half := n / 2
	scale := 1.0 / float64(n) / 32768
	phase := math.Pi / (4.0 * float64(n))
	out = make([]float64, n)
	for _, v := range src {
		sum += math.Abs(float64(v))
	}
	for i := 0; i < half; i++ {
		var down, up float64
		id := phase * float64(4*half-2*i-1)
		iu := phase * float64(3*n+2*i+1)
		for j := 0; j < n; j++ {
			a := float64(2*j + 1)
			down += math.Cos(a*id) * float64(src[j])
			up += math.Cos(a*iu) * float64(src[j])
		}
		out[i], out[i+half] = -down*scale, up*scale
		peak = max(peak, math.Abs(down*scale), math.Abs(up*scale))
	}
	return out, peak, sum
}

// TestAACMDCTPairMatchesDefinition: the 128-point transform of two short windows at once
// (one DCTMixedPair) agrees with the definition in float64 within the bound of the single
// transform (the rounding to float32 plus 4*log2(N) half ulps of the sum of the inputs),
// for both of the pair. The pair is not bit for bit two single calls.
func TestAACMDCTPairMatchesDefinition(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const n = 128
	m := newAACMDCT(n)
	sc := m.newScratch()
	for trial := 0; trial < 20; trial++ {
		var src [2][]float32
		for p := range src {
			src[p] = make([]float32, n)
			for i := range src[p] {
				src[p][i] = float32(rng.NormFloat64() * 1000)
			}
		}
		var dst [2][]float32
		dst[0], dst[1] = make([]float32, n), make([]float32, n)
		m.inversePair(dst[0], dst[1], src[0], src[1], sc)
		scale := 1.0 / float64(n) / 32768
		for p := range dst {
			want, peak, sum := aacIMDCTDefinition(src[p])
			bound := peak*0x1p-24 + 4*math.Log2(n)*0x1p-53*scale*sum
			var worst float64
			for i := range want {
				worst = max(worst, math.Abs(float64(dst[p][i])-want[i]))
			}
			if worst > bound {
				t.Fatalf("trial %d window %d: error %g exceeds %g", trial, p, worst, bound)
			}
			if trial == 0 {
				t.Logf("window %d: largest error %g, bound %g", p, worst, bound)
			}
		}
	}
}

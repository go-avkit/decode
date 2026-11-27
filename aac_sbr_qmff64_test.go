// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"github.com/go-fft/fft"
	"github.com/go-simd/floats"
)

// The analysis bank of go-avkit/decode#133 and before, kept as the oracle of the one in
// aac_sbr_qmf.go: the same history and window sums, then TWO 64-point transforms per slot
// in float64 (DCTMixedPair: widen, compute, narrow), one for the real parts and one for
// the imaginary parts, the latter on the input reversed. TestSBRAnalysisBound and
// TestSBRAnalysisBoundStreams hold the decoder's single-transform bank to a stated bound of
// it, and installAnaOracle routes a whole decode through it (the hook of aac_sbr_hf.go).

// sbrDCT4 is the float64 plan of the oracle and of the tests' gains.
var sbrDCT4 = fft.NewDCTPlan(sbrSynBands, 4)

type sbrAnaOracleF64 struct {
	hist    [sbrAnaKeep + aacFrameLen]float32
	u, v, w [sbrSynBands]float32
	c, s    [sbrSynBands]float32
}

func (a *sbrAnaOracleF64) process(in []float32, low *[sbrAnaBands][sbrLowSlots]complex128, first int) {
	copy(a.hist[aacFrameLen:], a.hist[:sbrAnaKeep])
	for i, s := range in[:aacFrameLen] {
		a.hist[aacFrameLen-1-i] = s * sbrPCMUnit
	}
	for l := 0; l < sbrQMFSlots; l++ {
		h := a.hist[aacFrameLen-32-32*l : aacFrameLen-32-32*l+sbrAnaTaps]
		a.u = [sbrSynBands]float32{}
		floats.MulAddRows32(a.u[:], sbrAnaWindow32[:], 64, h, 64, 5)
		// the cosine part on w folded onto odd angles, the sine part as the DCT-IV of the
		// reversed fold (DST-IV(x)[k] = (-1)^k DCT-IV(reversed x)[k])
		a.v[0], a.w[63] = a.u[0], -a.u[0]
		for n := 1; n <= 32; n++ {
			a.v[2*n-1], a.w[64-2*n] = a.u[n], a.u[n]
		}
		for n := 33; n < sbrSynBands; n++ {
			a.v[128-2*n], a.w[2*n-65] = -a.u[n], a.u[n]
		}
		sbrDCT4.DCTMixedPair(a.c[:], a.s[:], a.v[:], a.w[:], fft.NormBackward)
		for k := 0; k < sbrAnaBands; k += 2 {
			low[k][first+l] = complex(float64(a.c[k]), float64(a.s[k]))
			low[k+1][first+l] = complex(float64(a.c[k+1]), -float64(a.s[k+1]))
		}
	}
}

// installAnaOracle makes every channel made from now on run its analysis through the
// two-transform bank; the returned function restores the decoder's.
func installAnaOracle() func() {
	old := sbrNewAnaOracle
	sbrNewAnaOracle = func() sbrAnaOracle { return new(sbrAnaOracleF64) }
	return func() { sbrNewAnaOracle = old }
}

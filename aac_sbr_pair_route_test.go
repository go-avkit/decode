// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/rand"
	"runtime"
	"testing"

	"github.com/go-fft/fft"
	"golang.org/x/sys/cpu"
)

// TestSBRDCTPairIsRouted: on a platform where go-fft routes DCTPair to its fused kernel
// (arm64 NEON, amd64 with AVX2+FMA) the QMF banks must reach it, and the only observation
// the public API gives without timing is the one go-fft documents: the pair is within the
// DCT error bound but NOT bit for bit two single calls (the kernel fuses multiply-adds
// differently). If the two never differ over many random inputs, the pair is running as two
// calls, the kernel is not routed, and the speed this decoder is measured at is not there.
// The test fails, not skips, when the CPU should route it. (go-fft has no exported routing
// flag; this is the probe its documentation allows.)
func TestSBRDCTPairIsRouted(t *testing.T) {
	var want bool
	switch runtime.GOARCH {
	case "arm64":
		want = true
	case "amd64":
		want = cpu.X86.HasAVX2 && cpu.X86.HasFMA
	}
	if !want {
		t.Skipf("go-fft routes DCTPair to a kernel on arm64 and on amd64 with AVX2+FMA, not on %s (AVX2 %v, FMA %v)", runtime.GOARCH, cpu.X86.HasAVX2, cpu.X86.HasFMA)
	}
	rng := rand.New(rand.NewSource(77))
	differ := 0
	for trial := 0; trial < 200; trial++ {
		var a, b, p0, p1, s0, s1 [sbrSynBands]float64
		for i := range a {
			a[i], b[i] = rng.NormFloat64()*1000, rng.NormFloat64()*1000
		}
		sbrDCT4.DCTPair(p0[:], p1[:], a[:], b[:], fft.NormBackward)
		sbrDCT4.DCT(s0[:], a[:], fft.NormBackward)
		sbrDCT4.DCT(s1[:], b[:], fft.NormBackward)
		for i := range p0 {
			if math.Float64bits(p0[i]) != math.Float64bits(s0[i]) || math.Float64bits(p1[i]) != math.Float64bits(s1[i]) {
				differ++
				break
			}
		}
	}
	if differ == 0 {
		t.Fatalf("on %s (AVX2 %v, FMA %v) DCTPair equals two DCT calls bit for bit in all 200 trials: go-fft is not routing the fused pair kernel", runtime.GOARCH, cpu.X86.HasAVX2, cpu.X86.HasFMA)
	}
	t.Logf("%s: DCTPair differs from two DCT calls in %d of 200 trials: the fused kernel is routed", runtime.GOARCH, differ)
}

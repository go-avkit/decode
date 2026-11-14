// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "testing"

// BenchmarkSBRSynthesis times one frame (32 slots) of the 64-band synthesis bank.
func BenchmarkSBRSynthesis(b *testing.B) {
	var in [sbrQMFSlots][sbrSynBands]complex128
	x := uint32(7)
	for l := range in {
		for k := range in[l] {
			x = x*1664525 + 1013904223
			re := float64(int32(x)) / float64(1<<31) * 3000
			x = x*1664525 + 1013904223
			in[l][k] = complex(re, float64(int32(x))/float64(1<<31)*3000)
		}
	}
	var syn sbrSynthesis
	out := make([]float32, sbrQMFSlots*sbrSynBands)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		syn.process(out, &in)
	}
}

// BenchmarkSBRAnalysis times one frame of the 32-band analysis bank.
func BenchmarkSBRAnalysis(b *testing.B) {
	pcm := sbrTestPCM(1, 3)
	var low [sbrAnaBands][sbrLowSlots]complex128
	var an sbrAnalysis
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		an.process(pcm[:aacFrameLen], &low, 0)
	}
}

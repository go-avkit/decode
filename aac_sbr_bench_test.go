// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "testing"

// BenchmarkSBRChain is one stereo frame (2 x 1024 core samples in, 2 x 2048 samples
// out) through the SBR signal path of he44-st.aac's tables: analysis, high frequency
// generation, envelope adjustment, synthesis, for both channels. The core decoder is
// not in it.
func BenchmarkSBRChain(b *testing.B) {
	var s *sbrState
	sbrFixtureParser(b, "he44-st.aac", func(i int, p *aacParser, els []aacElement, f []byte) {
		if els[0].sbr != nil {
			s = els[0].sbr
		}
	})
	pcm := sbrTestPCM(1, 9)
	var out [2][]float32
	for c := range out {
		out[c] = make([]float32, sbrQMFSlots*sbrSynBands)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.frame(2, [2][]float32{pcm, pcm}, out)
	}
}

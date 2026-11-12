// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "testing"

// BenchmarkSBRChain is one stereo frame (2 x 1024 core samples in, 2 x 2048 samples
// out) through the SBR signal path of he44-st.aac's tables: analysis, high frequency
// generation, synthesis, for both channels. The core decoder and the envelope
// adjustment are not in it.
func BenchmarkSBRChain(b *testing.B) {
	var s *sbrState
	sbrFixtureParser(b, "he44-st.aac", func(i int, p *aacParser, els []aacElement, f []byte) {
		if els[0].sbr != nil {
			s = els[0].sbr
		}
	})
	pcm := sbrTestPCM(1, 9)
	out := make([]float32, sbrQMFSlots*sbrSynBands)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for c := 0; c < 2; c++ {
			d := s.channelDSP(c)
			d.analyse(pcm)
			s.hfGenerate(c)
			x := sbrX(&d.low, &d.high, s.t.kx[1], s.t.m[1])
			d.syn.process(out, &x)
		}
	}
}

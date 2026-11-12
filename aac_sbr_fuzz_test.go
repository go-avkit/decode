// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// checkSBRState checks what a parse that left SBR on must have built.
func checkSBRState(t *testing.T, s *sbrState) {
	t.Helper()
	if !s.started {
		return
	}
	for c := range s.ch {
		ch := &s.ch[c]
		if ch.numEnv < 0 || ch.numEnv > sbrMaxEnv || ch.numNoise < 0 || ch.numNoise > 2 {
			t.Fatalf("channel %d: %d envelopes, %d noise floors", c, ch.numEnv, ch.numNoise)
		}
	}
	if s.haveSpe {
		checkSBRTables(t, &s.t, s.spec)
	}
}

// FuzzSBRPayload feeds sbr_extension_data to the parser, three times in a row so
// that the state a frame leaves is read back: the first byte picks the core rate,
// the element, the CRC flag; the rest is the payload. Whatever the bits say, the
// parse stops inside the payload, leaves tables inside their arrays, and an error
// leaves SBR off.
func FuzzSBRPayload(f *testing.F) {
	tb := sbrPayload{}.tables(f, sbrTestCore, defaultSBRHdr())
	for _, elem := range []int{aacIDSCE, aacIDCPE} {
		for _, g := range []sbrGrid{gridFixFix(1, 1), gridFixVar(1, []int{0, 1}, 1, []int{0, 1, 0}), gridVarVar(1, 0, []int{0}, []int{1}, 2, []int{1, 0, 1})} {
			p := sbrBasic()
			p.elem, p.grids = elem, [2]sbrGrid{g, g}
			p.ext = &[2]int{2, sbrExtPS}
			b, _ := p.build(f, tb)
			f.Add(append([]byte{byte(elem<<5) | 7}, b...))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 1 {
			return
		}
		core := aacRates[int(data[0]&15)%len(aacRates)]
		elem := aacIDSCE + int(data[0]>>5&1)
		s := newSBRState(core)
		for i := 0; i < 3; i++ {
			r := newAACBits(data[1:])
			nbits := 8 * (len(data) - 1)
			err := s.parse(r, data[0]>>4&1 != 0, nbits, elem)
			if err != nil && s.started {
				t.Fatalf("an error left SBR on: %v", err)
			}
			if r.bitPos() > nbits+32 { // a read may overrun, a skip does not run on
				t.Fatalf("read %d bits of %d", r.bitPos(), nbits)
			}
			checkSBRState(t, s)
		}
	})
}

// FuzzSBRChain feeds sbr_extension_data to the parser as FuzzSBRPayload does and, with
// whatever state the parse left, runs the signal path (analysis, high frequency
// generation when SBR is on, synthesis) over a core signal for each channel, three
// frames in a row: whatever the stream chose (tables, borders, filtering modes), the
// path stays inside its arrays and its output is finite and bounded (the prediction
// filter is limited to a gain of 1+4+16 times the bandwidth factors).
func FuzzSBRChain(f *testing.F) {
	tb := sbrPayload{}.tables(f, sbrTestCore, defaultSBRHdr())
	for _, elem := range []int{aacIDSCE, aacIDCPE} {
		for _, g := range []sbrGrid{gridFixFix(1, 1), gridFixVar(1, []int{0, 1}, 1, []int{0, 1, 0}), gridVarVar(1, 0, []int{0}, []int{1}, 2, []int{1, 0, 1})} {
			p := sbrBasic()
			p.elem, p.grids = elem, [2]sbrGrid{g, g}
			b, _ := p.build(f, tb)
			f.Add(append([]byte{byte(elem<<5) | 7}, b...))
		}
	}
	pcm := sbrTestPCM(1, 5)
	out := make([]float32, sbrQMFSlots*sbrSynBands)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 1 {
			return
		}
		core := aacRates[int(data[0]&15)%len(aacRates)]
		elem := aacIDSCE + int(data[0]>>5&1)
		s := newSBRState(core)
		for i := 0; i < 3; i++ {
			r := newAACBits(data[1:])
			_ = s.parse(r, data[0]>>4&1 != 0, 8*(len(data)-1), elem)
			for c := 0; c <= elem-aacIDSCE; c++ {
				d := s.channelDSP(c)
				d.analyse(pcm)
				kx, m := 32, 0
				if s.started {
					s.hfGenerate(c)
					kx, m = s.t.kx[1], s.t.m[1]
				}
				x := sbrX(&d.low, &d.high, kx, m)
				d.syn.process(out, &x)
				for _, v := range out {
					if v != v || v > 1e3 || v < -1e3 {
						t.Fatalf("frame %d channel %d: sample %v", i, c, v)
					}
				}
			}
		}
	})
}

// FuzzSBRFrame feeds raw_data_block bytes to the parser with SBR on, twice in a
// row: the first byte picks the sampling frequency, the second the channel limit.
func FuzzSBRFrame(f *testing.F) {
	for _, fx := range sbrFixtures {
		_, frames := aacADTSFrames(f, filepath.Join("testdata", "aac", fx.name))
		for _, fr := range frames[:3] {
			f.Add(append([]byte{byte(rateIndexOf(fx.rate)), 2}, fr...))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 2 {
			return
		}
		cfg := aacConfig{rateIndex: int(data[0]) % len(aacRates), channels: 1}
		cfg.rate = aacRates[cfg.rateIndex]
		p, err := newAACParser(cfg, int(data[1])%8+1)
		if err != nil {
			t.Fatal(err)
		}
		p.enableSBR()
		for i := 0; i < 2; i++ {
			els, err := p.parseFrame(data[2:])
			if err != nil {
				return
			}
			for j := range els {
				if els[j].sbr != nil {
					checkSBRState(t, els[j].sbr)
				}
			}
		}
	})
}

func rateIndexOf(rate int) int {
	for i, r := range aacRates {
		if r == rate {
			return i
		}
	}
	return -1
}

// TestSBRWriteCorpus writes the seed corpus of FuzzSBRFrame (frames cut from the
// HE-AAC fixtures) and FuzzSBRPayload (written payloads) when SBR_WRITE_CORPUS is
// set to testdata/fuzz.
func TestSBRWriteCorpus(t *testing.T) {
	dir := os.Getenv("SBR_WRITE_CORPUS")
	if dir == "" {
		t.Skip("SBR_WRITE_CORPUS is not set")
	}
	write := func(target, name string, data []byte) {
		d := filepath.Join(dir, target)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("go test fuzz v1\n[]byte(%q)\n", data)
		if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, fx := range sbrFixtures {
		_, frames := aacADTSFrames(t, filepath.Join("testdata", "aac", fx.name))
		for i, fr := range frames[:4] {
			write("FuzzSBRFrame", fmt.Sprintf("%s-%d", fx.name, i), append([]byte{byte(rateIndexOf(fx.rate)), 2}, fr...))
		}
	}
	tb := sbrTestTables(t)
	n := 0
	for name, p := range sbrErrorCases(t) {
		b, _ := p.build(t, tb)
		write("FuzzSBRPayload", "error-"+name, append([]byte{7}, b...))
		write("FuzzSBRChain", "error-"+name, append([]byte{7}, b...))
		n++
	}
	good := sbrBasic()
	good.elem = aacIDCPE
	good.grids[1] = good.grids[0]
	good.coupling = true
	good.ext = &[2]int{4, sbrExtPS}
	good.harm = [2][]int{{1}, {1, 0}}
	b, _ := good.build(t, tb)
	write("FuzzSBRPayload", "pair-coupled", append([]byte{0x20 | 7}, b...))
	write("FuzzSBRChain", "pair-coupled", append([]byte{0x20 | 7}, b...))
	for hi, h := range sbrTestHeaders() {
		for _, elem := range []int{aacIDSCE, aacIDCPE} {
			p := sbrBasic()
			p.hdr, p.elem = &h, elem
			p.grids = [2]sbrGrid{gridFixFix(2, 1), gridFixVar(1, []int{0, 1}, 1, []int{0, 1, 0})}
			tbh := p.tables(t, sbrTestCore, h)
			b, _ := p.build(t, tbh)
			write("FuzzSBRChain", fmt.Sprintf("header%d-elem%d", hi, elem), append([]byte{byte(elem<<5) | 7}, b...))
		}
	}
}

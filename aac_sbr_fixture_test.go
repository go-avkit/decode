// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"testing"
)

// sbrFixtures are HE-AAC streams from Apple's encoder (testdata/aac/README), ADTS
// with the core rate in the header.
var sbrFixtures = []struct {
	name     string
	channels int // of the core
	rate     int // of the core
	ps       bool
}{
	{"he44-st.aac", 2, 22050, false},
	{"he32-st.aac", 2, 16000, false},
	{"he48-mono.aac", 1, 24000, false},
	{"he44-ps.aac", 1, 22050, true},
}

// sbrFixtureParser parses every frame of an HE-AAC fixture with SBR enabled and
// calls fn after each frame.
func sbrFixtureParser(t testing.TB, name string, fn func(i int, p *aacParser, els []aacElement, frame []byte)) {
	t.Helper()
	cfg, frames := aacADTSFrames(t, filepath.Join("testdata", "aac", name))
	p, err := newAACParser(cfg, DefaultMaxAudioChannels)
	if err != nil {
		t.Fatal(err)
	}
	p.enableSBR()
	for i, f := range frames {
		els, err := p.parseFrame(f)
		if err != nil {
			t.Fatalf("%s frame %d: %v", name, i, err)
		}
		if p.used != len(f) {
			t.Fatalf("%s frame %d: parsed %d bytes of %d", name, i, p.used, len(f))
		}
		fn(i, p, els, f)
	}
}

// TestSBRParseFixtures parses the SBR data of every frame of real HE-AAC streams.
// A wrong parse does not stop on the right bit: the payload size is stated by the
// fill element, so a field order, a Huffman code or a delta rule that is wrong ends
// in an out-of-range scalefactor, a bad time border or a payload over-read within a
// few frames.
func TestSBRParseFixtures(t *testing.T) {
	for _, fx := range sbrFixtures {
		var frames, withSBR, ps, coupled int
		var envs [6]int
		var classes [4]int
		var last *sbrState
		sbrFixtureParser(t, fx.name, func(i int, p *aacParser, els []aacElement, f []byte) {
			frames++
			if p.sbrErr != nil {
				t.Fatalf("%s frame %d: %v", fx.name, i, p.sbrErr)
			}
			if len(els) != 1 {
				t.Fatalf("%s frame %d: %d elements", fx.name, i, len(els))
			}
			s := els[0].sbr
			if s == nil {
				return // the first frames may carry no SBR data
			}
			withSBR++
			last = s
			if s.psSeen {
				ps++
			}
			if s.coupling {
				coupled++
			}
			for c := 0; c < int(els[0].nch); c++ {
				envs[s.ch[c].numEnv]++
				classes[s.ch[c].frameClass]++
			}
			if s.t.rate != 2*fx.rate {
				t.Fatalf("%s: SBR rate %d", fx.name, s.t.rate)
			}
		})
		t.Logf("%s: %d frames, %d with SBR, %d with PS, %d coupled pairs; envelopes per channel by count %v, frame classes %v; tables: k %v kx %d m %d n %v nq %d nlim %d patches %d",
			fx.name, frames, withSBR, ps, coupled, envs, classes, last.t.k, last.t.kx[1], last.t.m[1], last.t.n, last.t.nQ, last.t.nLim, last.t.numPatches)
		if withSBR < frames-2 {
			t.Errorf("%s: SBR in %d of %d frames", fx.name, withSBR, frames)
		}
		if fx.ps != (ps > 0) {
			t.Errorf("%s: %d frames with PS, want PS %v", fx.name, ps, fx.ps)
		}
	}
}

// sbrDigest folds what the parse of a fixture produced into one number.
func sbrDigest(t testing.TB, name string) uint64 {
	h := uint64(14695981039346656037)
	mix := func(v uint64) { h = (h ^ v) * 1099511628211 }
	sbrFixtureParser(t, name, func(i int, p *aacParser, els []aacElement, f []byte) {
		for _, e := range els {
			if e.sbr == nil {
				continue
			}
			for c := 0; c < int(e.nch); c++ {
				ch := &e.sbr.ch[c]
				mix(uint64(ch.numEnv)<<8 | uint64(ch.frameClass))
				for _, row := range ch.envQ[:ch.numEnv+1] {
					for _, v := range row[:e.sbr.t.n[1]] {
						mix(uint64(v))
					}
				}
				for _, row := range ch.noiseQ[:ch.numNoise+1] {
					for _, v := range row[:e.sbr.t.nQ] {
						mix(uint64(v))
					}
				}
			}
		}
	})
	return h
}

// TestSBRConcurrentParsers: parsers share nothing (the Huffman codes are read
// only), so goroutines that each parse the fixtures with their own parser must all
// reach the digest the single goroutine reached; under -race a shared table that is
// written is also a reported race.
func TestSBRConcurrentParsers(t *testing.T) {
	want := map[string]uint64{}
	for _, fx := range sbrFixtures {
		want[fx.name] = sbrDigest(t, fx.name)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 3; round++ {
				for _, fx := range sbrFixtures {
					if got := sbrDigest(t, fx.name); got != want[fx.name] {
						t.Errorf("%s: digest %x, want %x", fx.name, got, want[fx.name])
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}

// TestSBRParseDoesNotAllocate: a parse that succeeds uses the state's arrays only.
func TestSBRParseDoesNotAllocate(t *testing.T) {
	tb := sbrTestTables(t)
	p := sbrBasic()
	p.elem = aacIDCPE
	p.grids[1] = p.grids[0]
	p.ext = &[2]int{20, sbrExtPS}
	b, n := p.build(t, tb)
	s := newSBRState(sbrTestCore)
	if err := s.parse(newAACBits(b), false, n, aacIDCPE); err != nil {
		t.Fatal(err)
	}
	p.hdr = nil
	b, n = p.build(t, tb)
	r := newAACBits(b)
	if a := testing.AllocsPerRun(100, func() {
		r = newAACBits(b)
		if err := s.parse(r, false, n, aacIDCPE); err != nil {
			t.Fatal(err)
		}
	}); a > 2 { // the reader itself
		t.Errorf("%v allocations per parse", a)
	}
}

// TestSBRBomb: payloads of the largest size a fill element can state, made of
// garbage, cost a bounded amount of memory whatever they say: nothing the stream
// states sizes an allocation. It runs under a 64 MiB memory limit.
func TestSBRBomb(t *testing.T) {
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(64 << 20))
	payload := make([]byte, 270) // a fill element's count byte (15 + 255) less the type
	rnd := uint32(1)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < 20000; i++ {
		for j := range payload {
			rnd = rnd*1664525 + 1013904223
			payload[j] = byte(rnd >> 24)
		}
		s := newSBRState(22050)
		for _, elem := range []int{aacIDSCE, aacIDCPE} {
			_ = s.parse(newAACBits(payload), i&1 != 0, 8*len(payload), elem)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 8<<20 {
		t.Errorf("heap grew by %d bytes", grown)
	}
}

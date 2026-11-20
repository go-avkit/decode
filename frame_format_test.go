// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "testing"

// TestFrameAccessors states, for every chroma format and every pair of depths a Frame can
// carry (8 to 16 each, unequal included, and Chroma400's 0), what SampleBytes and ChromaSize
// answer. The expectations are written out here, not computed the way the code does.
func TestFrameAccessors(t *testing.T) {
	sizes := []struct{ w, h int }{{16, 8}, {17, 9}, {1, 1}, {2, 3}}
	chromas := []struct {
		name string
		c    Chroma
		size func(w, h int) (int, int)
	}{
		{"4:2:0", Chroma420, func(w, h int) (int, int) { return (w + 1) / 2, (h + 1) / 2 }},
		{"4:2:2", Chroma422, func(w, h int) (int, int) { return (w + 1) / 2, h }},
		{"4:4:4", Chroma444, func(w, h int) (int, int) { return w, h }},
	}
	bytesOf := func(depth int) int {
		if depth == 8 {
			return 1
		}
		return 2
	}
	for depth := 8; depth <= 16; depth++ {
		for depthC := 8; depthC <= 16; depthC++ {
			for _, ch := range chromas {
				for _, s := range sizes {
					f := Frame{Width: s.w, Height: s.h, Chroma: ch.c, BitDepth: depth, BitDepthC: depthC}
					wantW, wantH := ch.size(s.w, s.h)
					if w, h := f.ChromaSize(); w != wantW || h != wantH {
						t.Fatalf("%s %dx%d: ChromaSize %dx%d, want %dx%d", ch.name, s.w, s.h, w, h, wantW, wantH)
					}
					for plane, want := range [3]int{bytesOf(depth), bytesOf(depthC), bytesOf(depthC)} {
						if got := f.SampleBytes(plane); got != want {
							t.Fatalf("%s %d/%d plane %d: SampleBytes %d, want %d", ch.name, depth, depthC, plane, got, want)
						}
					}
					if f.SampleBytes(-1) != 0 || f.SampleBytes(3) != 0 {
						t.Fatalf("%s: a plane index out of range has bytes", ch.name)
					}
				}
			}
		}
		for _, s := range sizes {
			f := Frame{Width: s.w, Height: s.h, Chroma: Chroma400, BitDepth: depth}
			if w, h := f.ChromaSize(); w != 0 || h != 0 {
				t.Fatalf("4:0:0 %dx%d: ChromaSize %dx%d, want none", s.w, s.h, w, h)
			}
			if f.SampleBytes(0) != bytesOf(depth) || f.SampleBytes(1) != 0 || f.SampleBytes(2) != 0 || f.SampleBytes(3) != 0 {
				t.Fatalf("4:0:0 at %d bits: SampleBytes %d %d %d", depth, f.SampleBytes(0), f.SampleBytes(1), f.SampleBytes(2))
			}
		}
	}
	// A chroma value outside the four has no chroma planes either.
	if w, h := (Frame{Width: 4, Height: 4, Chroma: 9}).ChromaSize(); w != 0 || h != 0 {
		t.Fatalf("unknown chroma: ChromaSize %dx%d", w, h)
	}
}

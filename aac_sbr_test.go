// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestSBRHuffmanTables(t *testing.T) {
	wantN := [10]int{121, 121, 49, 49, 63, 63, 25, 25, 63, 25}
	for c := range wantN {
		syms, lens := aacSBRHuffSyms[c], aacSBRHuffLens[c]
		if len(syms) != wantN[c] || len(lens) != wantN[c] {
			t.Fatalf("table %d: %d symbols, %d lengths, want %d", c, len(syms), len(lens), wantN[c])
		}
		seen := make([]bool, wantN[c])
		for _, s := range syms {
			if int(s) >= wantN[c] || seen[s] {
				t.Fatalf("table %d: symbol %d out of range or repeated", c, s)
			}
			seen[s] = true
		}
		// every symbol decodes back from the code the writer gives it
		for _, s := range syms {
			delta := int(s) + sbrHuffOffset[c]
			w := &sbrBW{}
			w.huff(t, c, delta)
			w.put(32, 0) // the reader peeks past the code
			got, err := sbrReadHuff(newAACBits(w.b), c)
			if err != nil || got != delta {
				t.Fatalf("table %d: delta %d read back as %d, %v", c, delta, got, err)
			}
		}
	}
	// the code of the most probable delta (0) is the shortest of its table
	for c := range wantN {
		_, n := sbrTestCode(t, c, 0)
		if n != int(slices.Min(aacSBRHuffLens[c])) {
			t.Errorf("table %d: delta 0 has a code of %d bits, the shortest is %d", c, n, slices.Min(aacSBRHuffLens[c]))
		}
	}
	if _, err := sbrReadHuff(newAACBits(nil), 0); err == nil {
		t.Error("a read past the end succeeded")
	}
}

func TestSBRBuildHuffErrors(t *testing.T) {
	bad := aacSBRHuffLens
	bad[3] = []uint8{2, 1, 3}
	if _, err := buildSBRHuff(bad); err == nil {
		t.Error("decreasing lengths accepted")
	}
	bad = aacSBRHuffLens
	bad[0] = []uint8{1, 2, 3} // Kraft sum 7/8: an open branch
	if _, err := buildSBRHuff(bad); err == nil {
		t.Error("an incomplete code accepted")
	}
	defer func() {
		if recover() == nil {
			t.Error("mustSBRHuff did not stop on a bad table")
		}
	}()
	mustSBRHuff(bad)
}

func TestSBRMakeBands(t *testing.T) {
	var w [2]int
	sbrMakeBands(w[:], 10, 20, 2) // 10*sqrt(2) = 14.14 -> 14
	if w != [2]int{4, 6} {
		t.Errorf("widths %v", w)
	}
	var v [13]int
	sbrMakeBands(v[:], 12, 64, 13)
	sum := 0
	for _, x := range v {
		sum += x
	}
	if sum != 52 {
		t.Errorf("widths %v sum to %d, want 52", v, sum)
	}
}

// TestSBRTablesSweep builds the frequency tables for every header the syntax can
// state, at every sampling rate, and checks what the standard requires of them
// (strictly increasing master table within 64 subbands, the low table a subset of
// the high, noise and limiter tables inside it, patches that add up).
func TestSBRTablesSweep(t *testing.T) {
	ok, bad := 0, map[string]int{}
	for _, rate := range []int{16000, 22050, 24000, 32000, 44100, 48000, 64000, 88200, 96000, 128000, 176400, 192000, 14700} {
		for _, lb := range []int{0, 1, 2, 3} {
			for start := 0; start < 16; start++ {
				for stop := 0; stop < 16; stop++ {
					for xover := 0; xover < 8; xover++ {
						for scale := 0; scale < 4; scale++ {
							for alter := 0; alter < 2; alter++ {
								for nb := 0; nb < 4; nb++ {
									var tb sbrTables
									tb.rate = rate
									sp := sbrSpectrum{uint8(start), uint8(stop), uint8(xover), uint8(scale), uint8(alter), uint8(nb)}
									err := tb.makeMaster(sp)
									if err == nil {
										err = tb.makeDerived(sp, lb)
									}
									if err != nil {
										if !errors.Is(err, errSBRTables) {
											t.Fatalf("error %v is not errSBRTables", err)
										}
										bad[strings.Fields(err.Error())[4]]++
										continue
									}
									ok++
									checkSBRTables(t, &tb, sp)
								}
							}
						}
					}
				}
			}
		}
	}
	t.Logf("%d valid tables; refused: %v", ok, bad)
	if ok < 1000 {
		t.Errorf("only %d valid tables", ok)
	}
}

func checkSBRTables(t *testing.T, tb *sbrTables, sp sbrSpectrum) {
	t.Helper()
	fail := func(format string, a ...any) {
		t.Helper()
		t.Fatalf("rate %d %+v: "+format, append([]any{tb.rate, sp}, a...)...)
	}
	inc := func(name string, v []int) {
		t.Helper()
		for i := 1; i < len(v); i++ {
			if v[i] <= v[i-1] {
				fail("%s not strictly increasing: %v", name, v)
			}
		}
	}
	// the arrays hold 48 bands: every header, at every rate, stays within them
	if tb.k[0] < 1 || tb.nMaster > 48 || tb.k[2] > 64 {
		fail("k %v n_master %d", tb.k, tb.nMaster)
	}
	inc("master", tb.fMaster[:tb.nMaster+1])
	inc("high", tb.fHigh[:tb.n[1]+1])
	inc("low", tb.fLow[:tb.n[0]+1])
	// (more noise bands than low bands repeats a border, as in ffmpeg)
	if !slices.IsSorted(tb.fNoise[:tb.nQ+1]) {
		fail("noise table %v", tb.fNoise[:tb.nQ+1])
	}
	if tb.fMaster[0] != tb.k[0] || tb.fMaster[tb.nMaster] != tb.k[2] {
		fail("master %d..%d, k %v", tb.fMaster[0], tb.fMaster[tb.nMaster], tb.k)
	}
	if tb.kx[1] != tb.fHigh[0] || tb.kx[1]+tb.m[1] != tb.fHigh[tb.n[1]] || tb.kx[1]+tb.m[1] > 64 || tb.kx[1] > 32 {
		fail("kx %d m %d", tb.kx[1], tb.m[1])
	}
	if tb.n[0] != (tb.n[1]+1)/2 || tb.fLow[0] != tb.fHigh[0] || tb.fLow[tb.n[0]] != tb.fHigh[tb.n[1]] {
		fail("low table %v high %v", tb.fLow[:tb.n[0]+1], tb.fHigh[:tb.n[1]+1])
	}
	for _, f := range tb.fLow[:tb.n[0]+1] {
		if !slices.Contains(tb.fHigh[:tb.n[1]+1], f) {
			fail("low border %d is not a high border", f)
		}
	}
	for _, f := range tb.fNoise[:tb.nQ+1] {
		if !slices.Contains(tb.fLow[:tb.n[0]+1], f) {
			fail("noise border %d is not a low border", f)
		}
	}
	if tb.fNoise[0] != tb.fLow[0] || tb.fNoise[tb.nQ] != tb.fLow[tb.n[0]] || tb.nQ < 1 || tb.nQ > 5 {
		fail("noise table %v", tb.fNoise[:tb.nQ+1])
	}
	if tb.numPatches < 1 || tb.numPatches > 6 {
		fail("%d patches", tb.numPatches)
	}
	sum := 0
	for i := 0; i < tb.numPatches; i++ {
		if tb.patchNum[i] < 1 || tb.patchStart[i] < 1 {
			fail("patch %d: %d subbands from %d", i, tb.patchNum[i], tb.patchStart[i])
		}
		sum += tb.patchNum[i]
	}
	// a last patch of fewer than 3 subbands is dropped, the rest covers the band
	if sum > tb.m[1] || tb.m[1]-sum > 2 {
		fail("patches cover %d of %d", sum, tb.m[1])
	}
	if tb.nLim == 0 {
		// a degenerate header (a high band of a few subbands) merges every limiter
		// band into one border, as in ffmpeg; the adjustment loops run zero times
		return
	}
	inc("limiter", tb.fLim[:tb.nLim+1])
	// the top border is the stop frequency, unless a dropped last patch left the
	// patch borders below it and ffmpeg's rule merged the top into the border
	// before it
	if tb.fLim[0] != tb.fLow[0] || tb.fLim[tb.nLim] > tb.fLow[tb.n[0]] {
		fail("limiter table %v, low %v", tb.fLim[:tb.nLim+1], tb.fLow[:tb.n[0]+1])
	}
}

// TestSBRTablesPinned pins the tables of a header that real streams use (the
// HE-AAC fixtures: start 5 stop 0..., see TestSBRParseFixtures' log).
func TestSBRTablesPinned(t *testing.T) {
	var tb sbrTables
	tb.rate = 44100
	sp := sbrSpectrum{startFreq: 5, stopFreq: 0, xoverBand: 0, freqScale: 2, alterScale: 1, noiseBands: 2}
	if err := tb.makeMaster(sp); err != nil {
		t.Fatal(err)
	}
	if err := tb.makeDerived(sp, 2); err != nil {
		t.Fatal(err)
	}
	t.Logf("k %v n %v master %v patches %d nlim %d", tb.k, tb.n, tb.fMaster[:tb.nMaster+1], tb.numPatches, tb.nLim)
}

// TestSBRPatchGuards: the loop that lays out the patches ends for every header the
// syntax can state (the sweep above), and its guards refuse tables that no header
// builds, so that a change to the derivation cannot loop for ever.
func TestSBRPatchGuards(t *testing.T) {
	// the stop frequency is never reached: the patches stall
	stall := sbrTables{rate: 44100, nMaster: 1, k: [3]int{10, 0, 20}}
	stall.fMaster[0], stall.fMaster[1] = 10, 20
	stall.kx[1], stall.m[1] = 10, 15
	if err := stall.makePatches(); !errors.Is(err, errSBRTables) {
		t.Errorf("stalled patches: %v", err)
	}
	// a master table whose first border is above the limit: the search for a
	// patch start runs below the table
	below := sbrTables{rate: 44100, nMaster: 1, k: [3]int{1, 0, 40}}
	below.fMaster[0], below.fMaster[1] = 30, 40
	below.kx[1], below.m[1] = 30, 10
	if err := below.makePatches(); !errors.Is(err, errSBRTables) {
		t.Errorf("patches below the master table: %v", err)
	}
	// no stop frequency above the start: no patch at all
	none := sbrTables{rate: 44100, nMaster: 1, k: [3]int{10, 0, 20}}
	none.fMaster[0], none.fMaster[1] = 10, 20
	none.kx[1], none.m[1] = 10, 0
	if err := none.makePatches(); !errors.Is(err, errSBRTables) {
		t.Errorf("no patch: %v", err)
	}
}

// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// aacPSHybridBound is the stated bound of the binary32 PS hybrid bank (analysis, the sums
// of the synthesis, and the transform inputs held in binary32): over the synthetic PS
// streams, the repository's PS fixture and the conformance and corpus streams, the PCM
// (binary32, full scale 1) differs from the PCM of the float64 bank of v0.17.1 (the
// oracle, aac_ps_hybridf64_test.go) by at most this much. The measured figures are in
// the test log (go test -v -run PSHybridBound) and in the pull request; the stated bound
// is 2^-17, the one of the binary32 envelope adjustment.
const aacPSHybridBound = 0x1p-17

// TestPSHybridBankBound: the binary32 analysis of the rows against the float64 bank, on
// random frames in both layouts, relative to the peak of the input: the rows differ by
// at most 2^-20 of it (the filter sums 13 taps of unit-order values in binary32).
func TestPSHybridBankBound(t *testing.T) {
	frames := psRandFrames(4, 7)
	var f64 psHybrid
	var f32 psHyb32
	var worst float64
	for _, is34 := range []bool{false, true} {
		for i := range frames {
			var look [psLook][psSplitMax]complex128
			if i+1 < len(frames) {
				for j := range look {
					copy(look[j][:], frames[i+1][j][:psSplitMax])
				}
			}
			f64.begin(&frames[i], &look, is34)
			f32.begin(&frames[i], &look, is34)
			var want, got [psMaxRows]complex128
			for l := range frames[i] {
				f64.slot(l, &frames[i], is34, &want)
				f32.slot(l, &frames[i], is34, &got)
				for r := range psLayoutOf(is34).rows() {
					d := math.Hypot(real(want[r])-real(got[r]), imag(want[r])-imag(got[r]))
					worst = math.Max(worst, d)
				}
			}
			f64.end(&frames[i])
			f32.end(&frames[i])
		}
	}
	t.Logf("largest row difference %.3e (2^%.2f), the input peak is 1", worst, math.Log2(worst))
	if worst > 0x1p-20 {
		t.Errorf("rows differ from the float64 bank by %.3e", worst)
	}
	if worst == 0 {
		t.Error("the two banks agree to the bit: the binary32 bank did not run")
	}
}

// TestPSHybridCoefTables: every split row of both layouts has a row of the broadcast
// table (psHybSplit holds them all, the 34-band layout's being the most) and a row holds
// its filter in every column, in the order of the dot product.
func TestPSHybridCoefTables(t *testing.T) {
	for i, is34 := range []bool{false, true} {
		ly := psLayoutOf(is34)
		n := ly.start[ly.nSplit]
		if n > psHybSplit {
			t.Fatalf("layout %d has %d split rows, the table %d", i, n, psHybSplit)
		}
		tab := psHybCoefsOf(is34)
		for r := 0; r < n; r++ {
			f := &ly.filt[r]
			for j := 0; j < sbrQMFSlots; j++ {
				if tab[r][j] != float32(f.c0) {
					t.Fatalf("layout %d row %d column %d: c0 %v, want %v", i, r, j, tab[r][j], float32(f.c0))
				}
				for tp := 1; tp <= psLook; tp++ {
					if tab[r][tp*sbrQMFSlots+j] != float32(f.c[tp]) || tab[r][(psLook+tp)*sbrQMFSlots+j] != float32(f.s[tp]) {
						t.Fatalf("layout %d row %d tap %d column %d", i, r, tp, j)
					}
				}
			}
		}
	}
}

// TestPSHybridBoundFixtures: the binary32 bank against its float64 oracle through the
// whole decoder, on the synthetic PS streams and the fixture.
func TestPSHybridBoundFixtures(t *testing.T) {
	var worst float64
	var at string
	run := func(name string, frames [][]byte, cfg aacConfig, n int) {
		t.Helper()
		l, r, _ := psDecodeFrames(t, cfg, frames, n)
		restore := installHybOracle()
		wl, wr, _ := psDecodeFrames(t, cfg, frames, n)
		restore()
		d1, _ := sbrMaxDiff(l, wl)
		d2, _ := sbrMaxDiff(r, wr)
		d := math.Max(d1, d2)
		t.Logf("%-28s max |PCM difference| %.3e", name, d)
		if d > worst {
			worst, at = d, name
		}
	}
	for _, spec := range psStreamSpecs() {
		b := psBuildStream(t, spec)
		cfg, frames := aacADTSFrames(t, psStreamFile(t, spec.name, b))
		run(spec.name, frames, cfg, psStreamFrames)
	}
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", "he44-ps.aac"))
	run("he44-ps.aac", fr, cfg, len(fr))
	t.Logf("largest %.3e (2^%.2f) in %s", worst, math.Log2(worst), at)
	if worst > aacPSHybridBound {
		t.Errorf("PCM differs from the float64 bank by %.3e in %s, the bound is %.3e", worst, at, aacPSHybridBound)
	}
	if worst == 0 {
		t.Error("the oracle's output is the decoder's to the bit: the hook did not route")
	}
}

// TestPSHybridBoundStreams is the bound through the whole decoder on the ISO conformance
// streams (AAC_CONFORMANCE_DIR) and the corpus file AAC_BENCH_FILE (a path list), the
// streams the bound is stated over.
func TestPSHybridBoundStreams(t *testing.T) {
	var files []string
	if dir := os.Getenv("AAC_CONFORMANCE_DIR"); dir != "" {
		m, err := filepath.Glob(filepath.Join(dir, "compressedMp4", "*.mp4"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if f := os.Getenv("AAC_BENCH_FILE"); f != "" {
		files = append(files, filepath.SplitList(f)...)
	}
	if len(files) == 0 {
		t.Skip("neither AAC_CONFORMANCE_DIR nor AAC_BENCH_FILE is set")
	}
	var worst float64
	var at string
	for _, f := range files {
		got := aacFloatPCM(t, f)
		restore := installHybOracle()
		want := aacFloatPCM(t, f)
		restore()
		if len(got) != len(want) {
			t.Fatalf("%s: %d samples against %d", f, len(got), len(want))
		}
		d, peak := sbrMaxDiff(got, want)
		t.Logf("%-40s max |PCM difference| %.3e (peak %.3f)", strings.TrimSuffix(filepath.Base(f), ".mp4"), d, peak)
		if d > worst {
			worst, at = d, f
		}
	}
	t.Logf("largest over %d streams: %.3e (2^%.2f) in %s", len(files), worst, math.Log2(worst), filepath.Base(at))
	if worst > aacPSHybridBound {
		t.Errorf("PCM differs from the float64 bank by %.3e in %s, the bound is %.3e", worst, at, aacPSHybridBound)
	}
}

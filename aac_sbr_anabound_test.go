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

// aacSBRAnalysisBound is the stated bound of the single-transform binary32 analysis bank
// (aac_sbr_qmf.go): over the repository's HE-AAC streams, the PS fixtures and the
// conformance and corpus streams, the PCM (binary32, full scale 1) differs from the PCM
// of the two-transform bank of go-avkit/decode#133 (the oracle, aac_sbr_qmff64_test.go)
// by at most this much. The measured figures are in the test log (go test -v -run
// SBRAnalysisBound) and in the pull request; the stated bound is 2^-17, the one of the
// binary32 adjustment and hybrid bank.
const aacSBRAnalysisBound = 0x1p-17

// aacSBRDefinitionBound is the bound for two fixtures whose SBR path amplifies any
// rounding: he48-mono.aac and he44-ps.aac (the PS hybrid's own filters). Against the
// two-transform bank they differ by 1.76e-5 and 9.4e-6, which is two errors of the same
// size, so for them the decoder is held to the float64 definition of the analysis bank
// (refAnalysis, a direct sum with nothing folded) through the whole decoder instead.
// Measured: he48-mono 3.41e-5 (2^-14.9), he44-ps 1.59e-5 (2^-15.9) from the definition;
// the bound is 2^-14 (6.1e-5). The corpus and the conformance streams keep 2^-17
// against the two-transform bank.
const aacSBRDefinitionBound = 0x1p-14

func aacSBRIllConditioned(name string) bool {
	return name == "he48-mono.aac" || name == "he44-ps.aac"
}

// TestSBRAnalysisBound: the analysis bank against its two-transform oracle through the SBR
// path (analysis, generation, adjustment, synthesis) of the repository's HE-AAC fixtures,
// and through the whole decoder (PS included) on the synthetic PS streams.
func TestSBRAnalysisBound(t *testing.T) {
	var worst float64
	var at string
	note := func(name string, d, peak float64) {
		t.Logf("%-28s max |PCM difference| %.3e (peak %.3f)", name, d, peak)
		if d > worst {
			worst, at = d, name
		}
		if aacSBRIllConditioned(name) {
			return // held to the definition instead, by def below
		}
		if d > aacSBRAnalysisBound {
			t.Errorf("%s: PCM differs from the two-transform analysis by %.3e, the bound is %.3e", name, d, aacSBRAnalysisBound)
		}
	}
	// the two ill-conditioned fixtures: the decoder against the float64 definition of the
	// bank (the extended-precision side); the two-transform bank is logged beside it
	def := func(name string, d, peak float64) {
		t.Logf("%-28s against the definition: max |PCM difference| %.3e (peak %.3f)", name, d, peak)
		if d > aacSBRDefinitionBound {
			t.Errorf("%s: PCM differs from the float64 definition by %.3e, the bound is %.3e", name, d, aacSBRDefinitionBound)
		}
	}
	for _, fx := range []struct {
		name   string
		frames int
	}{{"he44-st.aac", 64}, {"he32-st.aac", 64}, {"he48-mono.aac", 64}} {
		got := sbrFixtureOut(t, fx.name, fx.frames, false)
		restore := installAnaOracle()
		want := sbrFixtureOut(t, fx.name, fx.frames, false)
		restore()
		for c := range got {
			d, peak := sbrMaxDiff(got[c], want[c])
			note(fx.name, d, peak)
		}
		if aacSBRIllConditioned(fx.name) {
			restore := installDefOracle()
			ref := sbrFixtureOut(t, fx.name, fx.frames, false)
			restore()
			for c := range got {
				d, peak := sbrMaxDiff(got[c], ref[c])
				def(fx.name, d, peak)
			}
		}
	}
	run := func(name string, frames [][]byte, cfg aacConfig, n int) {
		t.Helper()
		l, r, _ := psDecodeFrames(t, cfg, frames, n)
		restore := installAnaOracle()
		wl, wr, _ := psDecodeFrames(t, cfg, frames, n)
		restore()
		d1, p1 := sbrMaxDiff(l, wl)
		d2, _ := sbrMaxDiff(r, wr)
		note(name, math.Max(d1, d2), p1)
		if aacSBRIllConditioned(name) {
			restore := installDefOracle()
			rl, rr, _ := psDecodeFrames(t, cfg, frames, n)
			restore()
			e1, q1 := sbrMaxDiff(l, rl)
			e2, _ := sbrMaxDiff(r, rr)
			def(name, math.Max(e1, e2), q1)
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
	// two different arithmetics: a difference of exactly zero means the oracle did not run
	if worst == 0 {
		t.Error("the oracle's output is the decoder's to the bit: the hook did not route")
	}
}

// TestSBRAnalysisBoundStreams is the bound through the whole decoder on the ISO
// conformance streams (AAC_CONFORMANCE_DIR) and the corpus file AAC_BENCH_FILE (a path
// list), the streams the bound is stated over.
func TestSBRAnalysisBoundStreams(t *testing.T) {
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
		restore := installAnaOracle()
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
	if worst > aacSBRAnalysisBound {
		t.Errorf("PCM differs from the two-transform analysis by %.3e in %s, the bound is %.3e", worst, at, aacSBRAnalysisBound)
	}
}

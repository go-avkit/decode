// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// aacSBRAdjustBound is the stated bound of the binary32 envelope adjustment: over the
// conformance and corpus streams the PCM (binary32, full scale 1) differs from the PCM
// of the float64 adjustment of v0.17.1 (the oracle, aac_sbr_adjf64_test.go) by at most
// this much. Measured over the seven ISO PS conformance streams and ps4-he2, long1,
// long2 and transients: 1.192e-07 (2^-23, one binary32 ulp at 0.5), on the repository's
// fixtures the same; the stated bound is 2^-17, a margin of 64.
// ps4-he2 is the largest.
const aacSBRAdjustBound = 0x1p-17

// sbrFixtureOut decodes the first frames of an HE-AAC fixture through the SBR path
// (analysis, generation, adjustment, synthesis, without PS), with the oracle's
// adjustment or the decoder's.
func sbrFixtureOut(t testing.TB, name string, frames int, oracle bool) [][]float32 {
	t.Helper()
	if oracle {
		defer installAdjOracle()()
	}
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", name))
	p, err := newAACParser(cfg, DefaultMaxAudioChannels)
	if err != nil {
		t.Fatal(err)
	}
	p.enableSBR()
	synth := newAACSynth(cfg.rateIndex, cfg.channels)
	n := cfg.channels
	out := make([][]float32, n)
	var s *sbrState
	for fi := 0; fi < min(frames, len(fr)); fi++ {
		els, err := p.parseFrame(fr[fi])
		if err != nil {
			t.Fatal(err)
		}
		var pcm [2][aacFrameLen]float32
		synth.element(&els[0], 0, [2][]float32{pcm[0][:], pcm[1][:]})
		if els[0].sbr != nil {
			s = els[0].sbr
		}
		if s == nil {
			t.Fatalf("%s: no SBR data in frame %d", name, fi)
		}
		var got [2][]float32
		for c := 0; c < n; c++ {
			got[c] = make([]float32, sbrQMFSlots*sbrSynBands)
		}
		s.frame(n, [2][]float32{pcm[0][:], pcm[1][:]}, got)
		for c := 0; c < n; c++ {
			out[c] = append(out[c], got[c]...)
		}
	}
	return out
}

// sbrMaxDiff is the largest absolute difference of two PCM buffers, and the peak of the
// second.
func sbrMaxDiff(a, b []float32) (worst, peak float64) {
	for i := range min(len(a), len(b)) {
		worst = math.Max(worst, math.Abs(float64(a[i])-float64(b[i])))
		peak = math.Max(peak, math.Abs(float64(b[i])))
	}
	return worst, peak
}

// TestSBRAdjustBound: the binary32 adjustment against its float64 oracle, on the
// repository's HE-AAC fixtures: the PCM differs by at most aacSBRAdjustBound of full scale.
func TestSBRAdjustBound(t *testing.T) {
	var worst float64
	for _, fx := range []struct {
		name   string
		frames int
	}{{"he44-st.aac", 64}, {"he32-st.aac", 64}, {"he48-mono.aac", 64}} {
		got, want := sbrFixtureOut(t, fx.name, fx.frames, false), sbrFixtureOut(t, fx.name, fx.frames, true)
		for c := range got {
			d, peak := sbrMaxDiff(got[c], want[c])
			t.Logf("%s channel %d: max |PCM difference| %.3e (peak %.3f)", fx.name, c, d, peak)
			worst = math.Max(worst, d)
		}
	}
	if worst > aacSBRAdjustBound {
		t.Errorf("PCM differs from the float64 adjustment by %.3e, the bound is %.3e", worst, aacSBRAdjustBound)
	}
	// the two paths are different arithmetic: a difference of exactly zero means the
	// oracle was not the one that ran
	if worst == 0 {
		t.Error("the oracle's output is the decoder's to the bit: the hook did not route")
	}
}

// TestSBRDequantPowers: the powers of two built in the exponent field are 2^(q/a) to a
// binary32 rounding (exactly for the integer part), for both steps and negative q, and a
// value beyond the guard is replaced by 1.
func TestSBRDequantPowers(t *testing.T) {
	for _, a := range []int{1, 2} {
		for q := -60; q <= 100*a; q++ { // the exponent is held to ±100, tested below
			got, want := float64(sbrExp2(q, a)), math.Exp2(float64(q)/float64(a))
			if math.Abs(got-want) > want*0x1p-23 {
				t.Errorf("sbrExp2(%d, %d) = %v, want %v", q, a, got, want)
			}
		}
	}
	if sbrExp2(10, 1) != 1024 || sbrExp2(10, 2) != 32 || sbrExp2(-3, 1) != 0.125 {
		t.Error("an integer power of two is not exact")
	}
	if sbrPow2(1000) != sbrPow2(100) || sbrPow2(-1000) != sbrPow2(-100) {
		t.Error("the exponent is not held to its range")
	}
	if sbrGuard(sbrMaxDequant) != sbrMaxDequant || sbrGuard(sbrMaxDequant*2) != 1 || sbrGuard(5) != 5 {
		t.Error("the guard on a dequantised value")
	}
}

// aacFloatPCM decodes the first audio track of an MP4 to its binary32 samples.
func aacFloatPCM(t testing.TB, path string) []float32 {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := container.NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	id, err := FirstAudioTrack(r)
	if err != nil {
		t.Fatal(err)
	}
	au, err := DecodeAudioTrack(r, id, AudioConfig{MaxOutputBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	var pcm []float32
	for _, a := range au {
		if a.Format != SampleF32 {
			t.Fatalf("%s: format %v", path, a.Format)
		}
		for i := 0; i+4 <= len(a.Data); i += 4 {
			pcm = append(pcm, math.Float32frombits(binary.LittleEndian.Uint32(a.Data[i:])))
		}
	}
	return pcm
}

// TestSBRAdjustBoundStreams is TestSBRAdjustBound through the whole decoder (PS
// included) on the ISO conformance streams (AAC_CONFORMANCE_DIR) and the corpus file
// AAC_BENCH_FILE (a path list), the streams the bound is stated over.
func TestSBRAdjustBoundStreams(t *testing.T) {
	var files []string
	if dir := os.Getenv("AAC_CONFORMANCE_DIR"); dir != "" {
		m, err := filepath.Glob(filepath.Join(dir, "compressedMp4", "*.mp4"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if f := os.Getenv("AAC_BENCH_FILE"); f != "" {
		files = append(files, filepath.SplitList(f)...) // a path list
	}
	if len(files) == 0 {
		t.Skip("neither AAC_CONFORMANCE_DIR nor AAC_BENCH_FILE is set")
	}
	var worst float64
	var at string
	for _, f := range files {
		got := aacFloatPCM(t, f)
		restore := installAdjOracle()
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
	if worst > aacSBRAdjustBound {
		t.Errorf("PCM differs from the float64 adjustment by %.3e in %s, the bound is %.3e", worst, at, aacSBRAdjustBound)
	}
}

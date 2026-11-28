// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"path/filepath"
	"testing"
)

// The PS output is held to a stated bound of its float64 definition, not to recorded hashes
// (decode#77). The decorrelation and mixing stage runs in binary32 planes (aac_ps_mix32.go)
// and its rounding depends on the route (fused multiply-adds, kernel order), so a hash is a
// property of one machine; the bound is the contract: over every written stream (S01 to
// S19, which exercise the layout switch, the freeze and resume, the carried matrix and the
// fade in of the first matrix), the two streams below in which SBR drops out, and he44-ps.aac,
// the PCM (binary32, full scale 1) differs from the PCM of the float64 pipeline of v0.17.1
// (psHybOracleF64: the float64 hybrid bank, decorrelator and mixer) by at most
// aacPSHybridBound = 2^-17, the bound of the binary32 envelope adjustment and hybrid bank.
// The measured worst case is in the test log.

// psDropoutSpecs are two streams in which SBR drops out, the frames of S16a34 (34 bands,
// IPD/OPD) and of S09c with no SBR fill element at all (withSBR false: PS is suspended
// and both channels are the mono signal until the SBR frames come back): a hole in the
// middle of the stream that ends with SBR resuming, and a stream that never resumes.
func psDropoutSpecs() []psStreamSpec {
	var out []psStreamSpec
	for _, d := range []struct {
		from, name string
		drop       func(i int) bool
	}{
		{"S16a34", "drop-S16a34-4-7", func(i int) bool { return i >= 4 && i < 7 }},
		{"S09c", "drop-S09c-8-", func(i int) bool { return i >= 8 }},
	} {
		for _, s := range psStreamSpecs() {
			if s.name != d.from {
				continue
			}
			s.name = d.name
			s.frames = append([]psStreamFrame(nil), s.frames...)
			for i := range s.frames {
				if d.drop(i) {
					s.frames[i].noSBR = true
				}
			}
			out = append(out, s)
		}
	}
	return out
}

// TestPSOutputBound: the decoded PCM of every PS stream is within aacPSHybridBound of the
// float64 pipeline's, and is not the same to the bit (the binary32 stage ran).
func TestPSOutputBound(t *testing.T) {
	var worst float64
	var at string
	check := func(name string, run func(t *testing.T) (l, r []float32)) {
		t.Helper()
		l, r := run(t)
		restore := installHybOracle()
		wl, wr := run(t)
		restore()
		d1, _ := sbrMaxDiff(l, wl)
		d2, _ := sbrMaxDiff(r, wr)
		d := math.Max(d1, d2)
		if d > worst {
			worst, at = d, name
		}
		if d > aacPSHybridBound {
			t.Errorf("%s: PCM differs from the float64 pipeline by %.3e, the bound is %.3e", name, d, aacPSHybridBound)
		}
	}
	for _, spec := range append(psStreamSpecs(), psDropoutSpecs()...) {
		b := psBuildStream(t, spec)
		cfg, frames := aacADTSFrames(t, psStreamFile(t, spec.name, b))
		check(spec.name, func(t *testing.T) ([]float32, []float32) {
			l, r, _ := psDecodeFramesMode(t, cfg, frames, psStreamFrames, psTestMode{ffImage: spec.ffImage})
			return l, r
		})
	}
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", "he44-ps.aac"))
	check("he44-ps.aac", func(t *testing.T) ([]float32, []float32) {
		l, r, _ := psDecodeFrames(t, cfg, fr, len(fr))
		return l, r
	})
	t.Logf("largest over the streams %.3e (2^%.2f) in %s, the bound is 2^-17", worst, math.Log2(worst), at)
	if worst == 0 {
		t.Error("the float64 pipeline's output is the decoder's to the bit: the oracle did not route")
	}
}

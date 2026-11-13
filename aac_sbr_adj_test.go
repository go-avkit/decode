// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"path/filepath"
	"runtime"
	"testing"
)

// sbrAdjHarness runs the decoder's SBR path (analysis, generation, adjustment,
// synthesis) next to the exact reference path on the same core signal, frame by
// frame, each with its own state.
type sbrAdjHarness struct {
	n        int
	refAna   []*refAnalysis
	refSyn   []*refSynthesis
	refLow   [][sbrAnaBands][sbrLowSlots]complex128
	refBW    [][sbrMaxNoise]float64
	refAdj   []*refAdjChan
	seen     bool
	prevSpec sbrSpectrum
}

func newSBRAdjHarness(n int) *sbrAdjHarness {
	h := &sbrAdjHarness{n: n, refLow: make([][sbrAnaBands][sbrLowSlots]complex128, n), refBW: make([][sbrMaxNoise]float64, n)}
	for c := 0; c < n; c++ {
		h.refAna = append(h.refAna, &refAnalysis{})
		h.refSyn = append(h.refSyn, &refSynthesis{})
		h.refAdj = append(h.refAdj, newRefAdjChan())
	}
	return h
}

// step processes one frame of n channels of core samples with the parsed state s
// (started or not) and returns both paths' outputs.
func (h *sbrAdjHarness) step(s *sbrState, pcm [2][]float32) (got [2][]float32, want [2][]float64) {
	n := h.n
	for c := 0; c < n; c++ {
		got[c] = make([]float32, sbrQMFSlots*sbrSynBands)
	}
	s.frame(n, pcm, got)
	var env [2][sbrMaxEnv][]float64
	var noise [2][2][]float64
	if s.started {
		env, noise = refEnergies(s, n)
	}
	for c := 0; c < n; c++ {
		rl := &h.refLow[c]
		for k := range rl {
			copy(rl[k][:sbrHFGen], rl[k][sbrQMFSlots:])
		}
		ra := h.refAna[c].frame(pcm[c])
		for l := range ra {
			for k := range ra[l] {
				rl[k][sbrHFGen+l] = ra[l][k]
			}
		}
		var y [sbrHFSlots][sbrSynBands]complex128
		kx, m, end := 32, 0, 0
		if s.started {
			if !h.seen || s.spec != h.prevSpec { // a header that changes the tables is a reset
				h.refAdj[c].refill = true
			}
			var alpha [sbrAnaBands][2]complex128
			for k := 0; k < min(s.t.k[0], sbrAnaBands); k++ {
				alpha[k][0], alpha[k][1] = refPredictor(&rl[k])
			}
			chn := &s.ch[c]
			for i := 0; i < s.t.nQ; i++ {
				h.refBW[c][i] = refChirp(chn.invf[0][i], chn.invf[1][i], h.refBW[c][i])
			}
			high := refGenerate(&s.t, chn, rl, &alpha, &h.refBW[c])
			y = h.refAdj[c].frame(s, c, env[c], noise[c], &high)
			kx, m, end = s.t.kx[1], s.t.m[1], int(chn.tEnv[chn.numEnv])
		}
		x := h.refAdj[c].assemble(rl, &y, kx, m, end)
		for l := range x {
			w := h.refSyn[c].slot(&x[l])
			want[c] = append(want[c], w[:]...)
		}
	}
	if s.started {
		h.seen, h.prevSpec = true, s.spec
	}
	return got, want
}

// sbrAdjRun decodes the core of an HE-AAC fixture and runs the harness over its
// first frames: each channel's output of both paths at twice the core rate.
func sbrAdjRun(t testing.TB, name string, frames int) (got [][]float32, want [][]float64) {
	t.Helper()
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", name))
	p, err := newAACParser(cfg, DefaultMaxAudioChannels)
	if err != nil {
		t.Fatal(err)
	}
	p.enableSBR()
	synth := newAACSynth(cfg.rateIndex, cfg.channels)
	n := cfg.channels
	h := newSBRAdjHarness(n)
	got, want = make([][]float32, n), make([][]float64, n)
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
		g, w := h.step(s, [2][]float32{pcm[0][:], pcm[1][:]})
		for c := 0; c < n; c++ {
			got[c], want[c] = append(got[c], g[c]...), append(want[c], w[c]...)
		}
	}
	return got, want
}

type stat struct{ max, rms float64 }

// sbrRefFrames is how many frames the slow reference path runs: all of them, or the
// short count under the race detector and on the architectures CI emulates (a test
// binary has ten minutes, and the reference is a direct sum).
func sbrRefFrames(full, short int) int {
	if sbrEmulated() || raceEnabled {
		return short
	}
	return full
}

// sbrEmulated says whether the architecture is one CI runs under qemu.
func sbrEmulated() bool {
	switch runtime.GOARCH {
	case "ppc64le", "s390x", "riscv64", "loong64":
		return true
	}
	return false
}

// sbrBeyondRounding returns the largest error of got against want beyond one float32
// rounding of want, the RMS error, and the peak of want.
func sbrBeyondRounding(got []float32, want []float64) (s stat, peak float64) {
	var sum float64
	for i, w := range want {
		d := math.Abs(float64(got[i]) - w)
		s.max = math.Max(s.max, d-math.Abs(w)*0x1p-24)
		sum += d * d
		peak = math.Max(peak, math.Abs(w))
	}
	s.rms = math.Sqrt(sum / float64(len(want)))
	return s, peak
}

// TestSBRAdjust: the whole path, with the envelope adjustment, over real HE-AAC
// streams agrees with the exact reference path (a different formulation, float64,
// the estimate in exact rationals) to within one float32 rounding plus 1e-6 of the
// output peak (measured: 3e-7 at most; the margin is the conditioning of the
// prediction-error estimate, which the gains pass on); and ffmpeg 9.0.2's two builds,
// on the same streams, are within an absolute bound of the exact path as well (they
// are at 2e-3 to 1.5e-2 of the peak, their float32 estimate being what part 2's
// TestSBRPredictor measured), so that a wrong constant the decoder and the reference
// share shows as ffmpeg leaving the bound. The noise table is the exception: its
// effect is far below that bound, and TestSBRNoiseTable pins it instead. The decoder
// is at least as accurate as the better ffmpeg build.
func TestSBRAdjust(t *testing.T) {
	fixtures := []struct {
		name   string
		frames int
	}{{"he44-st.aac", 24}, {"he32-st.aac", 16}, {"he48-mono.aac", 16}}
	for _, fx := range fixtures {
		fx.frames = sbrRefFrames(fx.frames, 8)
		got, want := sbrAdjRun(t, fx.name, fx.frames)
		ours, peak := sbrBeyondRounding(got[0], want[0])
		t.Logf("%s channel 0: this decoder against the exact path: max %.2e beyond a float32 rounding, rms %.2e, peak %.3f", fx.name, ours.max, ours.rms, peak)
		if ours.max > 1e-6*peak {
			t.Errorf("%s: error %.2e beyond a float32 rounding, peak %.3f", fx.name, ours.max, peak)
		}
		best := stat{math.Inf(1), math.Inf(1)}
		for label, dir := range map[string]string{"ffmpeg macOS clang": "ref", "ffmpeg linux gcc": "ref-linux"} {
			all := readF32(t, filepath.Join("testdata", "aac", dir, fx.name[:len(fx.name)-4]+".f32"))
			left := make([]float32, min(len(all)/2, len(want[0])))
			for i := range left {
				left[i] = all[2*i]
			}
			// this decoder's output against ffmpeg's, sample by sample, on every channel
			// that ffmpeg decoded: the same signal to 3e-6 (their float32 arithmetic). This
			// also pins every entry of the noise table: a flipped sign moves it to 1.7e-5.
			for c := range got {
				var worst float64
				for i := 0; i < min(len(got[c]), len(all)/2); i++ {
					worst = math.Max(worst, math.Abs(float64(got[c][i])-float64(all[2*i+c])))
				}
				t.Logf("%s %-20s channel %d against this decoder: largest difference %.2e", fx.name, label, c, worst)
				if worst > 3e-6 {
					t.Errorf("%s: %s differs from this decoder by %.2e on channel %d", fx.name, label, worst, c)
				}
			}
			s, _ := sbrBeyondRounding(left, want[0][:len(left)])
			t.Logf("%s %-20s against the exact path: max %.2e beyond a rounding, rms %.2e", fx.name, label, s.max, s.rms)
			best.max, best.rms = math.Min(best.max, s.max), math.Min(best.rms, s.rms)
		}
		if best.max > 1e-5*peak || best.rms > 2e-6*peak {
			t.Errorf("%s: ffmpeg's best build is off the exact path by %.2e (rms %.2e) of a peak %.3f: the constants the decoder and the reference share may be wrong", fx.name, best.max, best.rms, peak)
		}
		if ours.max > best.max || ours.rms > best.rms {
			t.Errorf("%s: this decoder (%.2e, rms %.2e) is less accurate than the best ffmpeg build (%.2e, rms %.2e)", fx.name, ours.max, ours.rms, best.max, best.rms)
		}
	}
}

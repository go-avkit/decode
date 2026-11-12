// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"path/filepath"
	"testing"
)

// ffmpeg's decoded HE-AAC output contains the envelope adjustment, which is the next
// part, so the high band cannot be compared yet. The low band can: below the start
// frequency the decoder's output is the core signal through the analysis and the
// synthesis bank, delayed by (t_HFGen - t_HFAdj) = 6 slots of 64 samples (the delay
// sbrX gives the low band, as ffmpeg's does). Low-passed under the start band,
// ffmpeg's output and ours are the same signal, sample for sample, and the exact
// path (float64 in every stage) says which of them is closer to it.

// sbrLowpass is a Blackman-windowed sinc of n taps with the cut-off cut (radians per
// sample), zero phase: the output has the input's length and no delay.
func sbrLowpass(x []float64, cut float64, n int) []float64 {
	h := make([]float64, n)
	mid := float64(n-1) / 2
	var sum float64
	for i := range h {
		d := float64(i) - mid
		s := cut / math.Pi
		if d != 0 {
			s = math.Sin(cut*d) / (math.Pi * d)
		}
		w := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1)) + 0.08*math.Cos(4*math.Pi*float64(i)/float64(n-1))
		h[i] = s * w
		sum += h[i]
	}
	for i := range h {
		h[i] /= sum
	}
	out := make([]float64, len(x))
	for i := range x {
		var s float64
		for j, c := range h {
			if k := i + n/2 - j; k >= 0 && k < len(x) {
				s += c * x[k]
			}
		}
		out[i] = s
	}
	return out
}

// TestSBRLowBandAgainstFFmpeg (one stream, he44-st.aac, the only one with ffmpeg
// output committed): through the analysis and synthesis banks, the low
// band of he44-st.aac is, in largest and in RMS error against the exact path, at
// least as accurate as the most accurate of ffmpeg 9.0.2's builds (macOS clang with
// its C code paths, linux gcc 14 C code paths), which are 1e-6 of the signal.
func TestSBRLowBandAgainstFFmpeg(t *testing.T) {
	if sbrEmulated() {
		t.Skip("a direct-sum reference and three 1025-tap filters: the native lanes run it, an emulated one would pass the test binary's ten minutes")
	}
	const name, frames = "he44-st.aac", 24
	out, st := sbrChain(t, name, frames, true)
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", name))
	p, err := newAACParser(cfg, DefaultMaxAudioChannels)
	if err != nil {
		t.Fatal(err)
	}
	p.enableSBR()
	var kx int
	for _, f := range fr[:frames] {
		els, err := p.parseFrame(f)
		if err != nil {
			t.Fatal(err)
		}
		if els[0].sbr != nil {
			kx = els[0].sbr.t.kx[1]
		}
	}
	cut := float64(kx-3) / sbrSynBands * math.Pi
	const taps = 1025
	ours := make([]float64, len(out[0]))
	for i, v := range out[0] {
		ours[i] = float64(v)
	}
	exact := sbrLowpass(st.want[0], cut, taps)
	ourLow := sbrLowpass(ours, cut, taps)

	type stat struct{ max, rms float64 }
	measure := func(x []float64) (s stat) {
		n := 0
		for i := 2 * taps; i < len(x)-2*taps && i < len(exact)-2*taps; i++ {
			d := math.Abs(x[i] - exact[i])
			s.max = math.Max(s.max, d)
			s.rms += d * d
			n++
		}
		s.rms = math.Sqrt(s.rms / float64(n))
		return s
	}
	us := measure(ourLow)
	best := stat{math.Inf(1), math.Inf(1)}
	for label, dir := range map[string]string{"ffmpeg macOS clang": "ref", "ffmpeg linux gcc": "ref-linux"} {
		all := readF32(t, filepath.Join("testdata", "aac", dir, "he44-st.f32"))
		left := make([]float64, len(all)/2)
		for i := range left {
			left[i] = float64(all[2*i])
		}
		s := measure(sbrLowpass(left, cut, taps))
		t.Logf("%-20s low band against the exact path: max %.3e  rms %.3e", label, s.max, s.rms)
		best.max, best.rms = math.Min(best.max, s.max), math.Min(best.rms, s.rms)
	}
	t.Logf("%-20s low band against the exact path: max %.3e  rms %.3e (kx %d, signal rms %.3f)", "this decoder", us.max, us.rms, kx, 0.25)
	// The exact path shares sbrQMFWindow with the decoder, so a wrong table is
	// rewarded by the comparison above; ffmpeg's own error is the independent
	// witness: with a wrong centre tap it rises from 6.3e-5 to 6e-4.
	if best.max > 1e-4 || best.rms > 1e-5 {
		t.Errorf("ffmpeg's best build is off the exact path by max %.3e, rms %.3e: the shared window table may be wrong", best.max, best.rms)
	}
	if us.max > best.max || us.rms > best.rms {
		t.Errorf("this decoder (max %.3e, rms %.3e) is less accurate than the best ffmpeg build (max %.3e, rms %.3e)", us.max, us.rms, best.max, best.rms)
	}
}

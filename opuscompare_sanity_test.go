// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/rand/v2"
	"testing"
)

// TestOpusCompareSanity: the port gives 100 for identical signals, and its
// threshold is where RFC 6716 section 6 says it is: quality 0 corresponds to
// additive white noise at 48 dB SNR, so noise well above it fails and well below
// it passes. It also refuses what opus_compare.c refuses.
func TestOpusCompareSanity(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	const n = 48000
	ref := make([]int16, 2*n)
	for i := range n {
		v := 8000*math.Sin(float64(i)*0.05) + 4000*math.Sin(float64(i)*0.31) + 500*(rng.Float64()-0.5)
		ref[2*i], ref[2*i+1] = int16(v), int16(-v/2)
	}
	if q, err := opusCompare(ref, ref, 2, 48000); err != nil || q != 100 {
		t.Fatalf("identical: %v, %v", q, err)
	}
	noisy := func(amp float64) []int16 {
		out := make([]int16, len(ref))
		for i, v := range ref {
			out[i] = int16(float64(v) + amp*(rng.Float64()-0.5)*2)
		}
		return out
	}
	// The signal's power is about 4.0e7: noise of amplitude 3000 is about 10 dB SNR,
	// of amplitude 10 about 70 dB.
	if q, _ := opusCompare(ref, noisy(3000), 2, 48000); q >= 0 {
		t.Fatalf("10 dB SNR scored %v, want a failure", q)
	}
	if q, _ := opusCompare(ref, noisy(10), 2, 48000); q <= 0 || q >= 100 {
		t.Fatalf("70 dB SNR scored %v, want a pass below 100", q)
	}
	// Mono output against the stereo reference, and a lower rate.
	mono := make([]int16, n)
	for i := range mono {
		mono[i] = int16((int(ref[2*i]) + int(ref[2*i+1])) / 2)
	}
	if q, err := opusCompare(ref, mono, 1, 48000); err != nil || q < 50 {
		t.Fatalf("mono: %v, %v", q, err)
	}
	down := make([]int16, 2*(n/4))
	for i := range n / 4 {
		down[2*i], down[2*i+1] = ref[2*4*i], ref[2*4*i+1]
	}
	if _, err := opusCompare(ref, down, 2, 12000); err != nil {
		t.Fatalf("12 kHz: %v", err)
	}
	for _, rate := range []int{8000, 16000, 24000} {
		d := make([]int16, 2*(n/(48000/rate)))
		if _, err := opusCompare(ref, d, 2, rate); err != nil {
			t.Fatalf("%d Hz: %v", rate, err)
		}
	}
	for name, f := range map[string]func() error{
		"bad rate":  func() error { _, err := opusCompare(ref, ref, 2, 44100); return err },
		"lengths":   func() error { _, err := opusCompare(ref, ref[:len(ref)-2], 2, 48000); return err },
		"too short": func() error { _, err := opusCompare(ref[:100], ref[:100], 2, 48000); return err },
	} {
		if f() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

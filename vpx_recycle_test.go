// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"testing"
)

// vpxSamplesOf returns the samples of a committed stream.
func vpxSamplesOf(t testing.TB, name string) [][]byte {
	t.Helper()
	r, id := vpxTrack(t, name)
	ss, err := r.Samples(id)
	if err != nil {
		t.Fatal(err)
	}
	out := make([][]byte, len(ss))
	for i, s := range ss {
		out[i] = s.Data
	}
	return out
}

func newVPXFor(t testing.TB, name string) *Decoder {
	t.Helper()
	mk := NewVP9
	if name[:3] == "vp8" {
		mk = NewVP8
	}
	d, err := mk(Config{})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// decodeAllVPX decodes every sample and returns the pictures' md5s and count; with recycle it gives
// each Frame back as soon as it is hashed.
func decodeAllVPX(t testing.TB, name string, samples [][]byte, recycle bool) (sums []string) {
	t.Helper()
	d := newVPXFor(t, name)
	defer d.Close()
	for i, s := range samples {
		fs, err := d.DecodeSample(s, int64(i))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			sums = append(sums, sum(f))
			if recycle {
				d.Recycle(f)
			}
		}
	}
	return sums
}

// TestVPXRecycleSamePictures: giving every Frame back changes no picture.
func TestVPXRecycleSamePictures(t *testing.T) {
	for _, s := range vpxStreams {
		t.Run(s.name, func(t *testing.T) {
			samples := vpxSamplesOf(t, s.name)
			want := decodeAllVPX(t, s.name, samples, false)
			got := decodeAllVPX(t, s.name, samples, true)
			if len(got) != len(want) || len(want) == 0 {
				t.Fatalf("%d pictures, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("picture %d differs with Recycle", i)
				}
			}
		})
	}
}

// TestVPXRecycleAllocs: Recycle removes the three plane allocations of every picture after the
// first, and a decoder that never recycles keeps allocating them.
func TestVPXRecycleAllocs(t *testing.T) {
	for _, name := range []string{"vp8_altref", "vp9_altref", "vp9_tiles"} {
		t.Run(name, func(t *testing.T) {
			samples := vpxSamplesOf(t, name)
			pictures := len(decodeAllVPX(t, name, samples, false))
			plain := testing.AllocsPerRun(5, func() { decodeAllVPX(t, name, samples, false) })
			recycled := testing.AllocsPerRun(5, func() { decodeAllVPX(t, name, samples, true) })
			t.Logf("%d pictures: %.0f allocs without Recycle, %.0f with (%.2f and %.2f per picture)",
				pictures, plain, recycled, plain/float64(pictures), recycled/float64(pictures))
			// decodeAll also allocates a sums slice and hashes in both runs, so only the difference is
			// asserted: three planes for each picture but the first.
			// AllocsPerRun averages over runs and rounds down, hence the slack of 2.
			if saved, want := plain-recycled, float64(3*(pictures-1)-2); saved < want {
				t.Errorf("Recycle saved %.0f allocations over %d pictures, want at least %.0f", saved, pictures, want)
			}
		})
	}
}

// TestVPXRecycleNotHandedOutTwice: a recycled plane set backs one later picture only, a Frame the
// caller keeps is never overwritten, and giving the same Frame back twice does not duplicate it.
func TestVPXRecycleNotHandedOutTwice(t *testing.T) {
	const name = "vp9_tiles"
	samples := vpxSamplesOf(t, name)
	d := newVPXFor(t, name)
	defer d.Close()
	var kept []Frame // Frames the caller keeps: never recycled
	var kept0 []string
	next := 0
	decode := func() Frame {
		t.Helper()
		for next < len(samples) {
			fs, err := d.DecodeSample(samples[next], int64(next))
			next++
			if err != nil {
				t.Fatal(err)
			}
			if len(fs) == 1 {
				return fs[0]
			}
		}
		t.Fatal("stream too short")
		return Frame{}
	}
	a, b := decode(), decode()
	kept = append(kept, a, b)
	kept0 = append(kept0, sum(a), sum(b))
	c := decode()
	d.Recycle(c)
	d.Recycle(c) // twice: still one set in the free list
	d1, d2 := decode(), decode()
	if &d1.Y[0] != &c.Y[0] {
		t.Fatal("the recycled planes were not reused")
	}
	if &d2.Y[0] == &d1.Y[0] || &d2.Cb[0] == &d1.Cb[0] || &d2.Cr[0] == &d1.Cr[0] {
		t.Fatal("a recycled set was handed out to two pictures")
	}
	for i, f := range kept {
		if &f.Y[0] == &d1.Y[0] || &f.Y[0] == &d2.Y[0] {
			t.Errorf("kept Frame %d shares planes with a later picture", i)
		}
		if sum(f) != kept0[i] {
			t.Errorf("kept Frame %d was overwritten", i)
		}
	}
}

// TestVPXRecycleIgnores: foreign, resliced, empty and surplus Frames are not kept.
func TestVPXRecycleIgnores(t *testing.T) {
	const name = "vp9_altref"
	d := newVPXFor(t, name)
	defer d.Close()
	planes := func() Frame {
		return Frame{Y: make([]byte, 4), Cb: make([]byte, 1), Cr: make([]byte, 1)}
	}
	d.Recycle(Frame{})
	resliced := planes()
	resliced.Y = resliced.Y[:3]
	d.Recycle(resliced)
	if n := len(d.vpx.free); n != 0 {
		t.Fatalf("an empty and a resliced Frame left %d sets", n)
	}
	one := make([]byte, 4)
	cases := map[string]Frame{
		"no luma":       {Cb: make([]byte, 1), Cr: make([]byte, 1)},
		"no cb":         {Y: make([]byte, 4), Cr: make([]byte, 1)},
		"no cr":         {Y: make([]byte, 4), Cb: make([]byte, 1)},
		"aliased y cb":  {Y: one, Cb: one, Cr: make([]byte, 4)},
		"aliased y cr":  {Y: one, Cb: make([]byte, 4), Cr: one},
		"aliased cb cr": {Y: make([]byte, 4), Cb: one, Cr: one},
	}
	for name, f := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: Recycle panicked: %v", name, r)
				}
			}()
			d.Recycle(f)
		}()
		if n := len(d.vpx.free); n != 0 {
			t.Errorf("%s: kept %d sets", name, n)
			d.vpx.free = nil
		}
	}
	for range 2 * maxFreePlanes {
		d.Recycle(planes())
	}
	if n := len(d.vpx.free); n != maxFreePlanes {
		t.Fatalf("%d sets kept, want the bound %d", n, maxFreePlanes)
	}
	f := planes()
	d.Close()
	d.Recycle(f) // after Close: no panic, nothing kept
	h264 := newDecoder(Config{}, 4)
	h264.Recycle(f) // H.264: no-op
}

// TestVPXNoRecycleOwnsPlanes: without Recycle every picture has its own planes.
func TestVPXNoRecycleOwnsPlanes(t *testing.T) {
	const name = "vp9_tiles"
	samples := vpxSamplesOf(t, name)
	d := newVPXFor(t, name)
	defer d.Close()
	seen := map[*byte]bool{}
	for i, s := range samples {
		fs, err := d.DecodeSample(s, int64(i))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			if seen[&f.Y[0]] {
				t.Fatalf("picture %d shares planes with an earlier one", i)
			}
			seen[&f.Y[0]] = true
		}
	}
}

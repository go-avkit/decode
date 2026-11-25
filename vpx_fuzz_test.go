// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The VP8 and VP9 fuzz targets feed whatever bytes a sample may hold to the decoder. The corpus
// in testdata/fuzz is the committed streams' own samples (VPX_WRITE_SEEDS=1 go test -run
// TestVPXWriteSeeds writes it): a key frame, an inter frame and the largest sample of each.

func FuzzVP8Sample(f *testing.F) { fuzzVPX(f, NewVP8) }
func FuzzVP9Sample(f *testing.F) { fuzzVPX(f, NewVP9) }

func fuzzVPX(f *testing.F, mk func(Config) (*Decoder, error)) {
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := mk(fuzzConfig)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		frames, _ := d.DecodeSample(b, 0)
		more, _ := d.DecodeSample(b, 1)
		last, _ := d.Flush()
		checkFrames(t, frames, more, last)
	})
}

// FuzzVP9FrameSize: the header reader the size limit rests on never panics, and a size it
// reports is the 16-bit pair the header holds.
func FuzzVP9FrameSize(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) == 0 {
			return
		}
		for _, s := range vp9Sizes(b) {
			if s[0] < 1 || s[0] > 1<<16 || s[1] < 1 || s[1] > 1<<16 {
				t.Fatalf("size %v", s)
			}
		}
		vp8KeySize(b)
	})
}

func TestVPXFuzzCorpusIsCommitted(t *testing.T) {
	for _, target := range []string{"FuzzVP8Sample", "FuzzVP9Sample", "FuzzVP9FrameSize"} {
		entries, err := os.ReadDir(filepath.Join("testdata", "fuzz", target))
		if err != nil || len(entries) < 6 {
			t.Errorf("%s: corpus of %d entries (%v), want at least 6 (VPX_WRITE_SEEDS=1 go test -run TestVPXWriteSeeds)", target, len(entries), err)
		}
	}
}

// TestVPXWriteSeeds writes the fuzz corpus; it does nothing unless VPX_WRITE_SEEDS is set.
func TestVPXWriteSeeds(t *testing.T) {
	if os.Getenv("VPX_WRITE_SEEDS") == "" {
		t.Skip("VPX_WRITE_SEEDS is not set")
	}
	for _, s := range vpxStreams {
		target := "FuzzVP8Sample"
		if s.codec == "vp09" {
			target = "FuzzVP9Sample"
		}
		r, id := vpxTrack(t, s.name)
		samples, err := r.Samples(id)
		if err != nil {
			t.Fatal(err)
		}
		big := 0
		for i, sm := range samples {
			if len(sm.Data) > len(samples[big].Data) {
				big = i
			}
		}
		for _, i := range []int{0, 1, big} {
			for _, dir := range []string{target, "FuzzVP9FrameSize"} {
				if dir == "FuzzVP9FrameSize" && s.codec != "vp09" {
					continue
				}
				writeSeed(t, dir, fmt.Sprintf("%s_%d", s.name, i), samples[i].Data)
			}
		}
	}
	for name, b := range map[string][]byte{"bomb_vp8": vp8Key(16383, 16383), "bomb_vp9": vp9Key(65536, 65536)} {
		writeSeed(t, "FuzzVP8Sample", name, b)
		writeSeed(t, "FuzzVP9Sample", name, b)
		writeSeed(t, "FuzzVP9FrameSize", name, b)
	}
}

func writeSeed(t *testing.T, target, name string, b []byte) {
	t.Helper()
	dir := filepath.Join("testdata", "fuzz", target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(fmt.Sprintf("go test fuzz v1\n[]byte(%q)\n", b)), 0o644); err != nil {
		t.Fatal(err)
	}
}

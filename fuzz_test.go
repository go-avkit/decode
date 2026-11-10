// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-avkit/avkit/container"
)

type posterSeed struct {
	name string
	data []byte
	t    float64
}

// posterSeeds are the files FuzzPosterFrame starts from: the real files in
// testdata/files, the ones with edit lists in testdata/elst, and a muxed run of a
// conformance stream with a keyframe in the middle, each asked for a time inside,
// at and past the picture.
func posterSeeds(t testing.TB) []posterSeed {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("testdata", "files", "*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no seed files: %v", err)
	}
	edited, err := filepath.Glob(filepath.Join("testdata", "elst", "*.mp4"))
	if err != nil || len(edited) == 0 {
		t.Fatalf("no seed files with edit lists: %v", err)
	}
	files = append(files, edited...)
	stream := filepath.Join("testdata", "streams", "base_ip_qp26.264")
	byName := map[string][]byte{"muxed-two-gops": gopMP4(t, 0, stream, stream)}
	for _, p := range files {
		byName[filepath.Base(p)] = readFile(t, p)
	}
	var out []posterSeed
	for name, data := range byName {
		for _, at := range []float64{0, 0.1, 1e6} {
			out = append(out, posterSeed{fmt.Sprintf("%s-%v", name, at), data, at})
		}
	}
	return out
}

// TestWritePosterSeeds rewrites testdata/fuzz/FuzzPosterFrame when
// DECODE_WRITE_SEEDS is set; otherwise it checks the files are there and match.
func TestWritePosterSeeds(t *testing.T) {
	for _, s := range posterSeeds(t) {
		path := filepath.Join("testdata", "fuzz", "FuzzPosterFrame", "seed-"+s.name)
		body := fmt.Sprintf("go test fuzz v1\n[]byte(%q)\nfloat64(%v)\n", s.data, s.t)
		if os.Getenv("DECODE_WRITE_SEEDS") != "" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := readFile(t, path); string(got) != body {
			t.Errorf("%s is out of date: run with DECODE_WRITE_SEEDS=1", path)
		}
	}
}

// The fuzz targets exercise every entry point that takes bytes from a file: the
// avcC record, a length-prefixed sample, and a raw Annex B chunk. What they
// assert is that nothing panics, and that the work a stream can demand stays
// inside the configured bounds. The corpus is in testdata/fuzz, written by
// `jvt seeds` from the committed streams (real streams cut short, so that
// mutation starts from well formed parameter sets and slice headers).
//
// A frame limit of 396 macroblocks (CIF) keeps each execution fast and is the
// bound under test: a mutated SPS asking for more must be refused, not decoded.
var fuzzConfig = Config{MaxFrameMBs: 396, MaxSampleBytes: 1 << 20}

func FuzzParseAVCC(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := ParseAVCC(b)
		if err != nil {
			return
		}
		for _, s := range append(c.SPS, c.PPS...) {
			if len(s) == 0 || len(s) > len(b) {
				t.Fatalf("parameter set of %d bytes from a record of %d", len(s), len(b))
			}
		}
		if c.LengthSize != 1 && c.LengthSize != 2 && c.LengthSize != 4 {
			t.Fatalf("length size %d", c.LengthSize)
		}
	})
}

func FuzzDecodeSample(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := New(fuzzConfig, 4)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		frames, _ := d.DecodeSample(b, 0)
		more, _ := d.DecodeSample(b, 1)
		last, _ := d.Flush()
		for _, fr := range append(append(frames, more...), last...) {
			if fr.Width <= 0 || fr.Height <= 0 || fr.Width*fr.Height > fuzzConfig.MaxFrameMBs*256 {
				t.Fatalf("picture %dx%d breaks the %d macroblock bound", fr.Width, fr.Height, fuzzConfig.MaxFrameMBs)
			}
		}
	})
}

func FuzzDecodeAnnexB(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := New(fuzzConfig, 4)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		half := len(b) / 2
		frames, _ := d.DecodeAnnexB(b[:half])
		more, _ := d.DecodeAnnexB(b[half:])
		last, _ := d.Flush()
		for _, fr := range append(append(frames, more...), last...) {
			if fr.Width <= 0 || fr.Height <= 0 || fr.Width*fr.Height > fuzzConfig.MaxFrameMBs*256 {
				t.Fatalf("picture %dx%d breaks the %d macroblock bound", fr.Width, fr.Height, fuzzConfig.MaxFrameMBs)
			}
		}
	})
}

// FuzzPosterFrame reads an MP4 or Matroska file from bytes and asks for a
// picture at any time. Whatever the file, the call must return, within the
// bounds below, and a picture it returns must fit the frame bound.
func FuzzPosterFrame(f *testing.F) {
	for _, s := range posterSeeds(f) {
		f.Add(s.data, s.t)
	}
	cfg := PosterConfig{Config: fuzzConfig, MaxSamples: 8, MaxTrackSamples: 1 << 14}
	f.Fuzz(func(t *testing.T, data []byte, at float64) {
		r, err := container.NewReader(data)
		if err != nil {
			return
		}
		id, err := FirstVideoTrack(r)
		if err != nil {
			return
		}
		fr, err := PosterFrame(r, id, at, cfg)
		if err != nil {
			return
		}
		// A sample deeper than 8 bits takes two bytes (Main 10).
		bytesPerSample := (fr.BitDepth + 7) / 8
		if fr.Width <= 0 || fr.Height <= 0 || fr.Width*fr.Height > fuzzConfig.MaxFrameMBs*256 ||
			len(fr.Y) != fr.Width*fr.Height*bytesPerSample || at < 0 || at != at {
			t.Fatalf("picture %dx%d with %d luma bytes for t=%v", fr.Width, fr.Height, len(fr.Y), at)
		}
	})
}

// The committed corpus must exist, or `go test` would run the targets on
// nothing and the fuzzing above would look like it was covered.
func TestFuzzCorpusIsCommitted(t *testing.T) {
	for _, target := range []string{"FuzzParseAVCC", "FuzzDecodeSample", "FuzzDecodeAnnexB", "FuzzPosterFrame"} {
		entries, err := os.ReadDir(filepath.Join("testdata", "fuzz", target))
		if err != nil || len(entries) < 10 {
			t.Errorf("%s: corpus of %d entries (%v), want the seeds from `jvt seeds`", target, len(entries), err)
		}
	}
}

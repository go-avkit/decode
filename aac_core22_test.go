// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// TestAACEditStartAtCoreRate: an implicitly signalled HE-AAC track whose timescale is the
// CORE rate (22050 Hz) and whose edit list starts at media time 1000. 1000 ticks of
// 1/22050 s are 45.35 ms, which is 2000 samples of the 44100 Hz output: they are dropped
// here. ffmpeg 9.0.2 drops 1000 (it applies the start as skip_samples without the rate
// doubling that SBR makes, libavformat/mov.c) and so gives 1000 samples more, the
// stream's end being the same (go-avkit/decode#57; the report to ffmpeg is in the
// issue). The count asserted is the one the container defines; ffmpeg's is information.
//
// core22-he44-st.mp4 is imp-he44-st.mp4 (ffmpeg -c copy from he44-st.aac: timescale
// 44100, packets of 2048, edit start 0) with the fields patchCoreRate sets; the test
// checks that the committed file is exactly that.
func TestAACEditStartAtCoreRate(t *testing.T) {
	const (
		want       = 89520 // 44100 Hz samples per channel: edit start 2000, end 91520
		ffmpegGave = 90520 // ffmpeg 9.0.2: start 1000, same end; information only
		editStart  = 1000  // ticks at the core rate
	)
	b, err := os.ReadFile(filepath.Join("testdata", "aac", "core22-he44-st.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("testdata", "aac", "imp-he44-st.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(patchCoreRate(t, src), b) {
		t.Fatal("core22-he44-st.mp4 is not imp-he44-st.mp4 patched by patchCoreRate")
	}
	r, err := container.NewReader(b)
	if err != nil {
		t.Fatal(err)
	}
	id, err := FirstAudioTrack(r)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := r.TrackConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	if tc.Timescale != 22050 || tc.EditList == nil || tc.EditList.Edits[0].MediaTime != editStart {
		t.Fatalf("fixture changed: timescale %d, %s", tc.Timescale, aacEditSummary(tc))
	}
	bufs, err := DecodeAudioTrack(r, id, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	got := 0
	for _, a := range bufs {
		if a.SampleRate != 44100 {
			t.Fatalf("output rate %d", a.SampleRate)
		}
		got += a.Samples()
	}
	if got != want {
		t.Errorf("%d samples, want %d (ffmpeg 9.0.2 gives %d)", got, want, ffmpegGave)
	}
}

// patchCoreRate rewrites a copy of an MP4 made by `ffmpeg -c copy` from an HE-AAC ADTS
// stream (one track, version 0 boxes, timescale 44100, 46 packets of 2048) so that the
// track runs at the core rate: mdhd timescale 22050 and duration 47104, stts delta 1024,
// and an edit list of one edit, media_time 1000, segment_duration 57293120 in the movie
// timescale 28224000 (2.0299 s: 89520 samples at 44100 Hz).
func patchCoreRate(t *testing.T, mp4 []byte) []byte {
	t.Helper()
	b := bytes.Clone(mp4)
	// box finds the box at path (e.g. "moov/trak/mdia/mdhd") by walking sizes and types,
	// never by searching the bytes, and returns size bytes at off from the start of its
	// type field (the first track's, where a container box repeats).
	box := func(path string, off, size int) []byte {
		lo, hi := 0, len(b)
		for _, name := range strings.Split(path, "/") {
			found := false
			for p := lo; p+8 <= hi; {
				n := int(binary.BigEndian.Uint32(b[p:]))
				if n < 8 || p+n > hi {
					t.Fatalf("bad box size %d at %d", n, p)
				}
				if string(b[p+4:p+8]) == name {
					if name == path[strings.LastIndex(path, "/")+1:] {
						if p+4+off+size > p+n {
							t.Fatalf("%s box too short", name)
						}
						return b[p+4+off : p+4+off+size]
					}
					lo, hi, found = p+8, p+n, true
					break
				}
				p += n
			}
			if !found {
				t.Fatalf("no %s box in %s", name, path)
			}
		}
		t.Fatalf("no box at %s", path)
		return nil
	}
	put := func(p []byte, v uint32) { binary.BigEndian.PutUint32(p, v) }
	// elst: type, version+flags, count, segment_duration, media_time, rate
	e := box("moov/trak/edts/elst", 12, 8)
	put(e, 57293120)
	put(e[4:], 1000)
	// mdhd: type, version+flags, creation, modification, timescale, duration
	m := box("moov/trak/mdia/mdhd", 16, 8)
	put(m, 22050)
	put(m[4:], 47104)
	// stts: type, version+flags, count, then (sample_count, sample_delta)
	const stts = "moov/trak/mdia/minf/stbl/stts"
	if binary.BigEndian.Uint32(box(stts, 8, 4)) != 1 {
		t.Fatal("stts has more than one entry")
	}
	put(box(stts, 16, 4), 1024)
	return b
}

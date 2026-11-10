// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// ffPicture is one line of ffmpeg's framemd5 output.
type ffPicture struct {
	PTS int64
	MD5 string
}

// framemd5 reads a recording of tools/elstfix: ffmpeg 9.0.2 -f framemd5
// -fps_mode passthrough -enc_time_base:v demux -copyts, so that a time is in the
// track's timescale and as the demuxer made it.
func framemd5(t *testing.T, path string) []ffPicture {
	t.Helper()
	var out []ffPicture
	for _, l := range strings.Split(string(readFile(t, path)), "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" || l[0] == '#' {
			continue
		}
		f := strings.Split(l, ",")
		if len(f) != 6 {
			t.Fatalf("%s: line %q", path, l)
		}
		pts, err := strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		out = append(out, ffPicture{pts, strings.TrimSpace(f[5])})
	}
	return out
}

func recording(t *testing.T, name string, ignored bool) []ffPicture {
	kind := ".applied.framemd5"
	if ignored {
		kind = ".ignored.framemd5"
	}
	return framemd5(t, filepath.Join("testdata", "elst", name+kind))
}

// fixturePath finds a fixture in testdata/elst, or in testdata/files for the ones
// committed before.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	for _, dir := range []string{"elst", "files"} {
		if p := filepath.Join("testdata", dir, name); fileExists(p) {
			return p
		}
	}
	t.Fatalf("no fixture %s", name)
	return ""
}

// editFixtures are the files whose recordings are compared, picture by picture
// (MD5) and, where ptsLies is false, time by time. Two kinds of file have times
// ffmpeg's framemd5 states and this package does not:
//   - hevc-main-ipb and hevc-main10-ipb have a B-frame delay and no ctts. ffmpeg
//     labels each picture from the packet it is output with, so its times repeat
//     (three pictures at the last time) and skip two; this package labels a picture
//     with the sample that holds it.
//   - bframes.mkv is Matroska, whose block times are presentation times and
//     which has no edit list: Reader.Samples hands them over delayed by the
//     track's reordering delay (as an MP4 muxer would state with an edit list),
//     which this package leaves in the time as it always did (v0.3.0), and ffmpeg
//     takes out.
var editFixtures = []struct {
	name    string
	ptsLies bool
	noEdits bool // no recording with the edit list ignored
}{
	{"h264-delay.mp4", false, false},
	{"h264-trim.mp4", false, false},
	{"h264-trim-delay.mp4", false, false},
	{"hevc-delay.mp4", false, false},
	{"hevc-trim.mp4", false, false},
	{"hevc-trim-delay.mp4", false, false},
	{"hevc-main-ipb.mp4", true, false},
	{"hevc-main10-ipb.mp4", true, false},
	{"bframes.mp4", false, false},
	{"bframes-delayed.mp4", false, false},
	{"bframes.mkv", true, true},
}

func compareWithFFmpeg(t *testing.T, got []Frame, want []ffPicture, ptsLies bool) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d pictures, ffmpeg returns %d", len(got), len(want))
	}
	for i, f := range got {
		if s := sum(f); s != want[i].MD5 {
			t.Fatalf("picture %d: md5 %s, ffmpeg %s", i, s, want[i].MD5)
		}
		if !ptsLies && f.PTS != want[i].PTS {
			t.Fatalf("picture %d: PTS %d, ffmpeg %d", i, f.PTS, want[i].PTS)
		}
	}
}

func TestEditListMatchesFFmpeg(t *testing.T) {
	for _, fx := range editFixtures {
		t.Run(fx.name, func(t *testing.T) {
			r, id := reader(t, readFile(t, fixturePath(t, fx.name)))
			got, err := DecodeTrack(r, id, Config{})
			if err != nil {
				t.Fatal(err)
			}
			compareWithFFmpeg(t, got, recording(t, fx.name, false), fx.ptsLies)
			if fx.noEdits {
				return
			}
			got, err = DecodeTrack(r, id, Config{IgnoreEditList: true})
			if err != nil {
				t.Fatal(err)
			}
			compareWithFFmpeg(t, got, recording(t, fx.name, true), true)
		})
	}
}

var _ = container.EmptyEdit

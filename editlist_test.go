// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"errors"
	"math"
	"path/filepath"
	"slices"
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
	{"h264-edits.mp4", false, false},
	{"hevc-edits.mp4", false, false},
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

func TestPosterFrameEditListMatchesFFmpeg(t *testing.T) {
	for _, fx := range editFixtures {
		if fx.ptsLies {
			continue
		}
		t.Run(fx.name, func(t *testing.T) {
			r, id := reader(t, readFile(t, fixturePath(t, fx.name)))
			tc, err := r.TrackConfig(id)
			if err != nil {
				t.Fatal(err)
			}
			want := recording(t, fx.name, false)
			for i, w := range want {
				// Half a tick after the picture's time, in presentation time.
				got, err := PosterFrame(r, id, (float64(w.PTS)+0.5)/float64(tc.Timescale), PosterConfig{})
				if err != nil {
					t.Fatalf("picture %d (PTS %d): %v", i, w.PTS, err)
				}
				if sum(got) != w.MD5 || got.PTS != w.PTS {
					t.Fatalf("picture %d (PTS %d): got PTS %d %s, ffmpeg %s", i, w.PTS, got.PTS, sum(got), w.MD5)
				}
			}
			// Before the first picture is the first picture, and past the last the last.
			first, err := PosterFrame(r, id, 0, PosterConfig{})
			if err != nil || sum(first) != want[0].MD5 {
				t.Errorf("t=0: %v", err)
			}
			last, err := PosterFrame(r, id, math.MaxFloat64, PosterConfig{})
			if err != nil || sum(last) != want[len(want)-1].MD5 {
				t.Errorf("t=max: %v", err)
			}
		})
	}
}

// The edit list is the only thing that sets the two readings of a track apart:
// the same file, asked for the track as it is, gives ffmpeg's -ignore_editlist 1.
func TestPosterFrameIgnoreEditList(t *testing.T) {
	r, id := reader(t, readFile(t, fixturePath(t, "h264-delay.mp4")))
	tc, err := r.TrackConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	for i, w := range recording(t, "h264-delay.mp4", true) {
		got, err := PosterFrame(r, id, (float64(w.PTS)+0.5)/float64(tc.Timescale), PosterConfig{Config: Config{IgnoreEditList: true}})
		if err != nil || sum(got) != w.MD5 || got.PTS != w.PTS {
			t.Fatalf("picture %d (PTS %d): got PTS %d, %v", i, w.PTS, got.PTS, err)
		}
	}
}

func TestEditListPosterFrameMatchesDecodeTrack(t *testing.T) {
	for _, name := range []string{"h264-edits.mp4", "hevc-edits.mp4", "h264-trim-delay.mp4"} {
		t.Run(name, func(t *testing.T) {
			r, id := reader(t, readFile(t, fixturePath(t, name)))
			sameAsDecodeTrack(t, r, id, PosterConfig{})
		})
	}
}

// mp4Edits muxes a stream into an MP4 whose track states el, with the samples
// written as the other helpers do.
func mp4Edits(t *testing.T, el *container.EditList) []byte {
	t.Helper()
	aus := accessUnits(splitNALs(readFile(t, "testdata/streams/base_ip_qp26.264")))
	sps, pps, samples := parameterSets(aus)
	var buf bytes.Buffer
	m := container.NewProgressiveMuxer(&buf)
	id, err := m.AddTrack(container.TrackConfig{
		Kind: container.Video, Codec: "avc1", Timescale: 1000, Width: 176, Height: 144, SPS: sps, PPS: pps, EditList: el,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range samples {
		if err := m.WriteSample(id, container.Sample{Data: prefixed(s, 4), Duration: 40, Sync: i == 0}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func edit(dur uint64, media int64) container.Edit {
	return container.Edit{SegmentDuration: dur, MediaTime: media, Rate: container.NormalRate}
}

func TestEditListRefusals(t *testing.T) {
	dwell := edit(400, 0)
	dwell.Rate = 0
	for name, el := range map[string]*container.EditList{
		"a dwell":                  {MovieTimescale: 1000, Edits: []container.Edit{dwell}},
		"empty edit in the middle": {MovieTimescale: 1000, Edits: []container.Edit{edit(200, 0), edit(100, container.EmptyEdit), edit(200, 200)}},
	} {
		t.Run(name, func(t *testing.T) {
			r, id := reader(t, mp4Edits(t, el))
			if _, err := DecodeTrack(r, id, Config{}); !errors.Is(err, ErrEditList) {
				t.Errorf("DecodeTrack: %v, want ErrEditList", err)
			}
			if _, err := PosterFrame(r, id, 0, PosterConfig{}); !errors.Is(err, ErrEditList) {
				t.Errorf("PosterFrame: %v, want ErrEditList", err)
			}
			// The raw track is still there.
			if fs, err := DecodeTrack(r, id, Config{IgnoreEditList: true}); err != nil || len(fs) == 0 {
				t.Errorf("IgnoreEditList: %d pictures, %v", len(fs), err)
			}
			if _, err := PosterFrame(r, id, 0, PosterConfig{Config: Config{IgnoreEditList: true}}); err != nil {
				t.Errorf("PosterFrame IgnoreEditList: %v", err)
			}
		})
	}
}

// An edit that starts after the last sample presents nothing: ffmpeg returns no
// picture, DecodeTrack none, and PosterFrame has no picture to give.
func TestEditListPresentsNothing(t *testing.T) {
	r, id := reader(t, mp4Edits(t, &container.EditList{MovieTimescale: 1000, Edits: []container.Edit{edit(400, 1<<40)}}))
	fs, err := DecodeTrack(r, id, Config{})
	if err != nil || len(fs) != 0 {
		t.Errorf("%d pictures, %v", len(fs), err)
	}
	if _, err := PosterFrame(r, id, 0, PosterConfig{}); !errors.Is(err, ErrEditList) {
		t.Errorf("PosterFrame: %v", err)
	}
}

func TestEditListDecoderErrors(t *testing.T) {
	r, id := reader(t, readFile(t, fixturePath(t, "h264-trim.mp4")))
	if _, err := DecodeTrack(r, id, Config{MaxFrameMBs: 1}); !errors.Is(err, ErrOverLimit) {
		t.Errorf("a picture over the limit: %v", err)
	}
	if _, err := DecodeTrack(r, id, Config{MaxSampleBytes: 100}); !errors.Is(err, ErrOverLimit) {
		t.Errorf("a sample over the limit: %v", err)
	}
}

// synth is a track of n samples of duration 10 with the given composition offset,
// a keyframe at each index of keys.
func synth(n int, offset int32, keys ...int) []container.Sample {
	out := make([]container.Sample, n)
	for i := range out {
		out[i] = container.Sample{Duration: 10, CompositionOffset: offset}
	}
	for _, k := range keys {
		out[k].Sync = true
	}
	return out
}

func trackWith(edits ...container.Edit) container.TrackConfig {
	return container.TrackConfig{Timescale: 1000, EditList: &container.EditList{MovieTimescale: 1000, Edits: edits}}
}

func TestEditRuns(t *testing.T) {
	zero := edit(10, 0)
	zero.Rate = 0
	for _, c := range []struct {
		name    string
		tc      container.TrackConfig
		samples []container.Sample
		want    []editRun
		err     error
	}{
		{"no track timescale", container.TrackConfig{EditList: &container.EditList{MovieTimescale: 1000, Edits: []container.Edit{edit(10, 0)}}},
			synth(4, 0, 0), nil, ErrEditList},
		{"no movie timescale", container.TrackConfig{Timescale: 1000, EditList: &container.EditList{Edits: []container.Edit{edit(10, 0)}}},
			synth(4, 0, 0), nil, ErrEditList},
		{"duration beyond int64", trackWith(edit(math.MaxUint64, 0)), synth(4, 0, 0), nil, ErrEditList},
		{"durations beyond int64", trackWith(edit(math.MaxInt64, container.EmptyEdit), edit(100, 0)), synth(4, 0, 0), nil, ErrEditList},
		{"empty edit after media", trackWith(edit(10, 0), edit(10, container.EmptyEdit)), synth(4, 0, 0), nil, ErrEditList},
		{"media time before the track", trackWith(edit(10, -5)), synth(4, 0, 0), nil, ErrEditList},
		{"media rate", trackWith(zero), synth(4, 0, 0), nil, ErrEditList},
		{"to the end of the media", trackWith(edit(0, 0)), synth(4, 0, 0), nil, ErrEditList},
		// A duration that overflows with the media time is none, as in ffmpeg: nothing
		// is presented.
		{"end beyond int64", trackWith(edit(100, math.MaxInt64-5)), synth(4, 0, 0), nil, nil},
		// The whole track, after an empty edit of 5: shifted to start there.
		{"leading empty edit", trackWith(edit(5, container.EmptyEdit), edit(40, 0)), synth(4, 0, 0),
			[]editRun{{first: 0, end: 4, dts: 0, lo: 0, hi: 40, shift: 5}}, nil},
		// Composition offsets of 20: the edit keeps what composes in [30, 70), which
		// is samples 1 to 4, starts decoding at the keyframe 0, and walks to the second
		// keyframe after its end (10) for the B-frames that can follow the first (5). The
		// smallest presentation time is 20, taken off so that sample 1 is at 0.
		{"with composition offsets", trackWith(edit(40, 30)), synth(12, 20, 0, 5, 10),
			[]editRun{{first: 0, end: 11, dts: 0, lo: 30, hi: 70, shift: -30}}, nil},
		// Without them the walk stops at the first keyframe at or after the end (8).
		{"without composition offsets", trackWith(edit(40, 30)), synth(12, 0, 0, 8),
			[]editRun{{first: 0, end: 9, dts: 0, lo: 30, hi: 70, shift: -30}}, nil},
		// The last sample lasts as long as the edit, not as long as it says.
		{"to the last sample", trackWith(edit(100, 0)), synth(3, 0, 0), []editRun{{first: 0, end: 3, dts: 0, lo: 0, hi: 100, shift: 0}}, nil},
		// No keyframe at or before the media time that composes at or before it: the
		// first sample.
		{"no keyframe", trackWith(edit(10, 10)), append([]container.Sample{{Duration: 10, CompositionOffset: 100, Sync: true}}, synth(3, 100)...),
			nil, nil},
		// Zero-length samples share a decoding time; the walk back to the keyframe
		// takes the earliest keyframe of them, and not an earlier frame that is not one.
		{"shared decoding times", trackWith(edit(10, 10)), []container.Sample{{Duration: 10, Sync: true}, {Duration: 0, Sync: true}, {Duration: 10, Sync: true}, {Duration: 10}},
			[]editRun{{first: 1, end: 3, dts: 10, lo: 10, hi: 20, shift: -10}}, nil},
		{"shared decoding times, not keyframes", trackWith(edit(10, 10)), []container.Sample{{Duration: 10, Sync: true}, {Duration: 0}, {Duration: 10, Sync: true}, {Duration: 10}},
			[]editRun{{first: 2, end: 3, dts: 10, lo: 10, hi: 20, shift: -10}}, nil},
		// No keyframe at all: any sample will do, the earliest of those that share
		// a decoding time.
		{"no keyframes", trackWith(edit(10, 10)), []container.Sample{{Duration: 10}, {Duration: 0}, {Duration: 10}, {Duration: 10}},
			[]editRun{{first: 1, end: 4, dts: 10, lo: 10, hi: 20, shift: -10}}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := editRuns(c.tc, c.samples)
			if !errors.Is(err, c.err) {
				t.Fatalf("error %v, want %v", err, c.err)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

// An edit list that repeats a stretch of the track many times is refused before
// the decoder is asked to walk it.
func TestEditRunsWorkBound(t *testing.T) {
	edits := make([]container.Edit, 40)
	for i := range edits {
		edits[i] = edit(1<<30, 0)
	}
	if _, err := editRuns(trackWith(edits...), synth(20, 0, 0)); !errors.Is(err, ErrOverLimit) {
		t.Errorf("got %v, want ErrOverLimit", err)
	}
}

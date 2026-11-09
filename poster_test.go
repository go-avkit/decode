// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"errors"
	"math"
	"path/filepath"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// gopMP4 muxes the access units of the streams, one after the other, into an MP4
// whose sync samples are the ones holding an IDR slice. Repeating a stream gives
// a track with a keyframe in the middle.
func gopMP4(t testing.TB, offset int32, paths ...string) []byte {
	t.Helper()
	var sps, pps, samples = [][]byte(nil), [][]byte(nil), [][][]byte(nil)
	for i, p := range paths {
		s, pp, sm := parameterSets(accessUnits(splitNALs(readFile(t, p))))
		if i == 0 {
			sps, pps = s, pp
		}
		samples = append(samples, sm...)
	}
	var buf bytes.Buffer
	m := container.NewProgressiveMuxer(&buf)
	id, err := m.AddTrack(container.TrackConfig{
		Kind: container.Video, Codec: "avc1", Timescale: 1000, Width: 176, Height: 144, SPS: sps, PPS: pps,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range samples {
		idr := false
		for _, n := range s {
			idr = idr || n[0]&31 == 5
		}
		if err := m.WriteSample(id, container.Sample{
			Data: prefixed(s, 4), Duration: 40, Sync: idr, CompositionOffset: offset,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func reader(t testing.TB, data []byte) (*container.Reader, uint32) {
	t.Helper()
	r, err := container.NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	id, err := FirstVideoTrack(r)
	if err != nil {
		t.Fatal(err)
	}
	return r, id
}

// sameAsDecodeTrack asks PosterFrame for every picture DecodeTrack returns, at a
// time half a tick after its presentation time, and wants the same picture.
func sameAsDecodeTrack(t *testing.T, r *container.Reader, id uint32, cfg PosterConfig) {
	t.Helper()
	tc, err := r.TrackConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	want, err := DecodeTrack(r, id, cfg.Config)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) < 2 {
		t.Fatalf("oracle holds %d pictures", len(want))
	}
	for i, w := range want {
		got, err := PosterFrame(r, id, (float64(w.PTS)+0.5)/float64(tc.Timescale), cfg)
		if err != nil {
			t.Fatalf("picture %d (PTS %d): %v", i, w.PTS, err)
		}
		if got.PTS != w.PTS || sum(got) != sum(w) || got.Width != w.Width || got.Height != w.Height {
			t.Fatalf("picture %d (PTS %d): got PTS %d %s, want %s", i, w.PTS, got.PTS, sum(got), sum(w))
		}
	}
}

func TestPosterFrameMatchesDecodeTrack(t *testing.T) {
	for _, name := range []string{"base_ip_qp26", "main_ipb_cabac", "high_ipb_8x8_cabac", "main_cif_pyramid"} {
		path := filepath.Join("testdata", "streams", name+".264")
		for _, offset := range []int32{0, 80} {
			t.Run(name, func(t *testing.T) {
				// Twice, so that the second copy starts at a keyframe in the
				// middle of the track.
				r, id := reader(t, gopMP4(t, offset, path, path))
				sameAsDecodeTrack(t, r, id, PosterConfig{})
			})
		}
	}
}

func TestPosterFrameRealFiles(t *testing.T) {
	for _, name := range []string{"bframes.mp4", "bframes.mkv", "bframes-delayed.mp4"} {
		t.Run(name, func(t *testing.T) {
			r, err := container.NewReader(readFile(t, filepath.Join("testdata", "files", name)))
			if err != nil {
				t.Fatal(err)
			}
			id, err := FirstVideoTrack(r)
			if err != nil {
				t.Fatal(err)
			}
			sameAsDecodeTrack(t, r, id, PosterConfig{})
		})
	}
}

// The second keyframe is at the 11th sample of a 2 x base_ip_qp26 track: a
// picture just after it is reached from there, in a run that the limit would
// refuse if it started at the first sample.
func TestPosterFrameStartsAtTheLastKeyframe(t *testing.T) {
	path := filepath.Join("testdata", "streams", "base_ip_qp26.264")
	_, _, one := parameterSets(accessUnits(splitNALs(readFile(t, path))))
	r, id := reader(t, gopMP4(t, 0, path, path))
	second := float64(len(one)+2)*0.040 + 0.001
	got, err := PosterFrame(r, id, second, PosterConfig{MaxSamples: 4})
	if err != nil {
		t.Fatal(err)
	}
	if got.PTS != int64(len(one)+2)*40 {
		t.Fatalf("PTS %d", got.PTS)
	}
	// With no sync flag at all, the run starts at the first sample.
	r, id = reader(t, mp4(t, path, 0))
	if _, err := PosterFrame(r, id, second, PosterConfig{MaxSamples: 4}); !errors.Is(err, ErrPosterLimit) {
		t.Fatalf("a distant keyframe: %v", err)
	}
	if _, err := PosterFrame(r, id, 0.001, PosterConfig{MaxSamples: 4}); err != nil {
		t.Fatalf("near the start: %v", err)
	}
}

func TestPosterFrameClamps(t *testing.T) {
	path := filepath.Join("testdata", "streams", "base_ip_qp26.264")
	r, id := reader(t, gopMP4(t, 80, path))
	all, err := DecodeTrack(r, id, Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		t    float64
		want Frame
	}{
		{"zero, before the first presentation time", 0, all[0]},
		{"the first presentation time", 0.080, all[0]},
		{"beyond the end", 1e6, all[len(all)-1]},
		{"infinity", math.Inf(1), all[len(all)-1]},
		{"overflowing the timescale", math.MaxFloat64, all[len(all)-1]},
	} {
		got, err := PosterFrame(r, id, c.t, PosterConfig{})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.PTS != c.want.PTS || sum(got) != sum(c.want) {
			t.Errorf("%s: PTS %d, want %d", c.name, got.PTS, c.want.PTS)
		}
	}
}

func TestPosterFrameErrors(t *testing.T) {
	path := filepath.Join("testdata", "streams", "base_ip_qp26.264")
	r, id := reader(t, gopMP4(t, 0, path))
	for _, bad := range []float64{-0.001, math.Inf(-1), math.NaN()} {
		if _, err := PosterFrame(r, id, bad, PosterConfig{}); !errors.Is(err, ErrTime) {
			t.Errorf("t=%v: %v", bad, err)
		}
	}
	if _, err := PosterFrame(r, 99, 0, PosterConfig{}); !errors.Is(err, container.ErrUnknownTrack) {
		t.Errorf("unknown track: %v", err)
	}
	// Bounds are refused before a decoder is built.
	if _, err := PosterFrame(r, id, 1e6, PosterConfig{MaxSamples: 2}); !errors.Is(err, ErrPosterLimit) {
		t.Errorf("MaxSamples: %v", err)
	}
	if _, err := PosterFrame(r, id, 0, PosterConfig{MaxTrackSamples: 2}); !errors.Is(err, ErrPosterLimit) {
		t.Errorf("MaxTrackSamples: %v", err)
	}
	// The decoder's own bounds apply.
	if _, err := PosterFrame(r, id, 0, PosterConfig{Config: Config{MaxFrameMBs: 1}}); !errors.Is(err, ErrOverLimit) {
		t.Errorf("MaxFrameMBs: %v", err)
	}
	// The decoder outputs a picture one sample after the sample holding it, so
	// a run of one sample is refused although the keyframe is the target, and
	// the count it is held to includes that lookahead.
	if _, err := PosterFrame(r, id, 0, PosterConfig{MaxSamples: 1}); !errors.Is(err, ErrPosterLimit) {
		t.Errorf("lookahead beyond MaxSamples: %v", err)
	}
	if _, err := PosterFrame(r, id, 0, PosterConfig{MaxSamples: 2}); err != nil {
		t.Errorf("one sample and its lookahead: %v", err)
	}
	// A sample that breaks a decoder bound stops the run with its error.
	if _, err := PosterFrame(r, id, 0, PosterConfig{Config: Config{MaxSampleBytes: 100}}); !errors.Is(err, ErrOverLimit) {
		t.Errorf("MaxSampleBytes: %v", err)
	}
}

func TestPosterFrameUnsupported(t *testing.T) {
	for _, name := range []string{"hvc1.mp4", "ffmpeg-aaclc.mp4"} {
		r, err := container.NewReader(readFile(t, filepath.Join("testdata", "files", name)))
		if err != nil {
			t.Fatal(err)
		}
		id := r.TrackIDs()[0]
		if _, err := PosterFrame(r, id, 0, PosterConfig{}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestFirstVideoTrackNone(t *testing.T) {
	r, err := container.NewReader(readFile(t, filepath.Join("testdata", "files", "ffmpeg-aaclc.mp4")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FirstVideoTrack(r); !errors.Is(err, ErrNoVideo) {
		t.Fatalf("an audio file: %v", err)
	}
}

// PosterFrame must cost the same on a long track as on a short one: it decodes
// from the keyframe, so its allocations do not grow with the track. The counts
// are exact under load and are recorded in the pull request.
func TestPosterFrameAllocations(t *testing.T) {
	path := filepath.Join("testdata", "streams", "base_ip_qp26.264")
	short, idShort := reader(t, gopMP4(t, 0, path))
	long, idLong := reader(t, gopMP4(t, 0, path, path, path, path, path, path, path, path))
	at := func(r *container.Reader, id uint32, sec float64) float64 {
		return testing.AllocsPerRun(5, func() {
			if _, err := PosterFrame(r, id, sec, PosterConfig{}); err != nil {
				t.Fatal(err)
			}
		})
	}
	a, b := at(short, idShort, 0), at(long, idLong, 0)
	t.Logf("allocations per PosterFrame at t=0: %v on the short track, %v on the track 8 times as long", a, b)
	if b > a+4 {
		t.Errorf("allocations grew with the track: %v then %v", a, b)
	}
}

func BenchmarkPosterFrame(b *testing.B) {
	path := filepath.Join("testdata", "streams", "base_ip_qp26.264")
	r, id := reader(b, gopMP4(b, 0, path, path))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := PosterFrame(r, id, 0.5, PosterConfig{}); err != nil {
			b.Fatal(err)
		}
	}
}

// A sample table that points outside the file is the reader's error.
func TestPosterFrameSampleTableOutsideTheFile(t *testing.T) {
	data := gopMP4(t, 0, filepath.Join("testdata", "streams", "base_ip_qp26.264"))
	// stco: type, version and flags, entry count, then the first chunk offset.
	i := bytes.Index(data, []byte("stco"))
	if i < 0 {
		t.Fatal("no stco")
	}
	copy(data[i+4+4+4:], []byte{0xff, 0xff, 0xff, 0xf0})
	r, id := reader(t, data)
	if _, err := PosterFrame(r, id, 0, PosterConfig{}); !errors.Is(err, container.ErrSampleData) {
		t.Fatalf("got %v", err)
	}
}

// A track whose samples hold no picture ends in an error, not in a zero Frame.
func TestPosterFrameNoPicture(t *testing.T) {
	sps, pps, _ := parameterSets(accessUnits(splitNALs(readFile(t, filepath.Join("testdata", "streams", "base_ip_qp26.264")))))
	var buf bytes.Buffer
	m := container.NewProgressiveMuxer(&buf)
	id, err := m.AddTrack(container.TrackConfig{
		Kind: container.Video, Codec: "avc1", Timescale: 1000, Width: 176, Height: 144, SPS: sps, PPS: pps,
	})
	if err != nil {
		t.Fatal(err)
	}
	// An access unit delimiter alone.
	if err := m.WriteSample(id, container.Sample{Data: prefixed([][]byte{{0x09, 0xf0}}, 4), Duration: 40, Sync: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	r, id := reader(t, buf.Bytes())
	if _, err := PosterFrame(r, id, 0, PosterConfig{}); !errors.Is(err, ErrSample) {
		t.Fatalf("got %v", err)
	}
}

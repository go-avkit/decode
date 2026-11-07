// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// mp4 muxes a stream's access units into an MP4 with avkit's own muxer.
func mp4(t *testing.T, path string, offset int32) []byte {
	t.Helper()
	aus := accessUnits(splitNALs(readFile(t, path)))
	sps, pps, samples := parameterSets(aus)
	var buf bytes.Buffer
	m := container.NewProgressiveMuxer(&buf)
	id, err := m.AddTrack(container.TrackConfig{
		Kind: container.Video, Codec: "avc1", Timescale: 1000, Width: 176, Height: 144, SPS: sps, PPS: pps,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range samples {
		if err := m.WriteSample(id, container.Sample{
			Data: prefixed(s, 4), Duration: 40, Sync: i == 0, CompositionOffset: offset,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecodeTrack(t *testing.T) {
	path := "testdata/streams/base_ip_qp26.264"
	r, err := container.NewReader(mp4(t, path, 80))
	if err != nil {
		t.Fatal(err)
	}
	frames, err := DecodeTrack(r, r.TrackIDs()[0], Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sums(frames), reference(t, path)) {
		t.Fatal("pictures differ from ffmpeg")
	}
	for i, f := range frames {
		if want := int64(i)*40 + 80; f.PTS != want {
			t.Fatalf("picture %d: PTS %d, want %d", i, f.PTS, want)
		}
	}
}

func TestDecodeTrackErrors(t *testing.T) {
	path := "testdata/streams/base_ip_qp26.264"
	r, err := container.NewReader(mp4(t, path, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTrack(r, 99, Config{}); err == nil {
		t.Error("an unknown track was decoded")
	}
	// A frame size over the limit is refused when the track's SPS is loaded.
	if _, err := DecodeTrack(r, r.TrackIDs()[0], Config{MaxFrameMBs: 1}); !errors.Is(err, ErrOverLimit) {
		t.Errorf("limit: %v", err)
	}
	// A sample that decodes to an error stops the walk, keeping what was out.
	if _, err := DecodeTrack(r, r.TrackIDs()[0], Config{MaxSampleBytes: 100}); !errors.Is(err, ErrOverLimit) {
		t.Errorf("sample limit: %v", err)
	}
}

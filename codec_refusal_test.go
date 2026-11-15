// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// A video track whose codec is not decoded is refused by name. It used to fall
// through to the H.264 parser, which reported "NAL unit of 2185855810 bytes" and
// called a sound VP9 file corrupt (reported from go-perceptual).

// TestUnsupportedVideoCodecFiles: real files, made with ffmpeg
// (testsrc2 160x90, 15 fps, 1 s), refused with the codec in the message.
func TestUnsupportedVideoCodecFiles(t *testing.T) {
	for _, tc := range []struct{ file, codec string }{
		{"vp9.webm", "vp09"},
		{"vp8.webm", "vp08"},
		{"mp4v.mp4", "mp4v"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			r, err := container.NewReader(readFile(t, filepath.Join("testdata", "codecs", tc.file)))
			if err != nil {
				t.Fatal(err)
			}
			id, err := FirstVideoTrack(r)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := r.TrackConfig(id)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Codec != tc.codec {
				t.Fatalf("the container states %q, want %q", cfg.Codec, tc.codec)
			}
			frames, err := DecodeTrack(r, id, Config{})
			wantCodecRefusal(t, err, tc.codec)
			if len(frames) != 0 {
				t.Errorf("%d pictures from a refused track", len(frames))
			}
			_, err = PosterFrame(r, id, 0, PosterConfig{})
			wantCodecRefusal(t, err, tc.codec)
		})
	}
}

// TestUnsupportedVideoCodecs: every sample entry avkit lists that is not H.264 or
// HEVC, and a few it does not, is named by NewTrack, whatever bytes follow.
func TestUnsupportedVideoCodecs(t *testing.T) {
	for _, codec := range []string{
		"vp08", "vp09", "av01", "mjpg", "mp4v", "jpeg", "apch", "s263", "svc1", "mvc1",
		"dvh1", "hvc2", "avc5", "mp4a", "Opus", "xxxx", "VP90", " ",
	} {
		t.Run(codec, func(t *testing.T) {
			tc := container.TrackConfig{Kind: container.Video, Codec: codec, SPS: [][]byte{{0x67, 1, 2, 3}}, PPS: [][]byte{{0x68, 1}}}
			d, err := NewTrack(tc, Config{})
			if d != nil {
				t.Errorf("a decoder for %q", codec)
			}
			wantCodecRefusal(t, err, codec)
		})
	}
}

// TestSupportedVideoCodecsAreNotRefused: the sample entries that are decoded
// still reach their decoder (a bad parameter set is ErrConfig or a decode error,
// never a CodecError).
func TestSupportedVideoCodecsAreNotRefused(t *testing.T) {
	for _, codec := range []string{"", "avc1", "avc2", "avc3", "avc4", "hvc1", "hev1"} {
		_, err := NewTrack(container.TrackConfig{Codec: codec, SPS: [][]byte{{0xFF}}}, Config{})
		var ce *CodecError
		if errors.As(err, &ce) {
			t.Errorf("%q refused as a codec: %v", codec, err)
		}
	}
}

func wantCodecRefusal(t testing.TB, err error, codec string) {
	t.Helper()
	var ce *CodecError
	switch {
	case err == nil:
		t.Fatalf("%q decoded, want a refusal", codec)
	case !errors.As(err, &ce) || ce.Codec != codec:
		t.Fatalf("%v, want a *CodecError for %q", err, codec)
	case !errors.Is(err, ErrUnsupported):
		t.Fatalf("%v is not ErrUnsupported", err)
	case !strings.Contains(err.Error(), `codec "`+codec+`" is not supported`):
		t.Fatalf("%q does not name the codec %q", err, codec)
	case errors.Is(err, ErrSample) || strings.Contains(err.Error(), "NAL"):
		t.Fatalf("%v calls the file corrupt", err)
	}
}

// FuzzNewTrack: whatever codec and parameter sets a track states, NewTrack
// either returns a decoder or an error, and a codec that is neither H.264 nor
// HEVC is refused by name before any parameter set is read.
func FuzzNewTrack(f *testing.F) {
	f.Fuzz(func(t *testing.T, codec string, sps, pps []byte) {
		tc := container.TrackConfig{Codec: codec, SPS: [][]byte{sps}, PPS: [][]byte{pps}}
		d, err := NewTrack(tc, fuzzConfig)
		if d != nil {
			d.Close()
		}
		if isAVC(tc) || isHEVC(tc) {
			return
		}
		var ce *CodecError
		if d != nil || !errors.As(err, &ce) || ce.Codec != codec {
			t.Fatalf("codec %q: %v, want a *CodecError", codec, err)
		}
	})
}

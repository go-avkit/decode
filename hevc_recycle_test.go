// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"slices"
	"testing"

	"github.com/go-avkit/h265"
)

// TestHEVCFramesOutliveTheirPictures: the HEVC decoder recycles each picture's planes as soon as
// it is copied into a Frame, so a Frame that is kept must not change while the rest of the stream
// decodes, twice over with one decoder (the second pass reuses every plane of the first). Each
// stream, with and without reordering, B pyramids, wavefronts and slices, decodes to ffmpeg's md5
// of every frame, and the frames hashed when they came out still hash the same at the end.
func TestHEVCFramesOutliveTheirPictures(t *testing.T) {
	for _, name := range []string{"main_ipb", "main10_ipb", "main_intra", "main_wpp_slices", "main10_wpp_slices"} {
		t.Run(name, func(t *testing.T) {
			raw := readFile(t, "testdata/hevc/streams/"+name+".265")
			want := reference(t, "testdata/hevc/streams/"+name+".264")
			d, err := NewHEVC(Config{}, 4)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			var kept []Frame
			var at [][]string
			for pass := 0; pass < 2; pass++ {
				fs, err := d.DecodeAnnexB(raw)
				if err == nil {
					var rest []Frame
					rest, err = d.Flush()
					fs = append(fs, rest...)
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := sums(fs); !slices.Equal(got, want) {
					t.Fatalf("pass %d: %d frames, ffmpeg has %d, or one differs", pass, len(got), len(want))
				}
				kept = append(kept, fs...)
				at = append(at, sums(fs))
			}
			if later := append(sums(kept[:len(kept)/2]), sums(kept[len(kept)/2:])...); !slices.Equal(later, slices.Concat(at...)) {
				t.Error("a Frame changed after it was returned")
			}
		})
	}
}

// TestHEVCFramesOfANilPicture: a picture the decoder did not give is refused as a format error,
// and releasing it is skipped, not a nil dereference.
func TestHEVCFramesOfANilPicture(t *testing.T) {
	d, _ := NewHEVC(Config{}, 4)
	var fs []Frame
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("a nil picture panicked: %v", r)
			}
		}()
		fs, err = d.hevcFrames([]h265.Output{{}})
	}()
	if len(fs) != 0 || !errors.Is(err, ErrUnsupported) {
		t.Errorf("a nil picture: %d frames, %v", len(fs), err)
	}
}

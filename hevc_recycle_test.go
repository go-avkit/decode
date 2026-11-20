// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"os"
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

// TestHEVCFramesOutliveTheirPicturesInEveryLayout is the same promise for the layouts that came
// with the chroma formats (4:0:0, 4:2:2, 4:4:4, 16 bits and unequal depths): a decoder decodes a
// stream twice, so that the second pass reuses the planes of the first, every Frame is kept, and
// none changes (its hash when it came out is its hash at the end), and both passes agree. The
// 4:4:4 stream is committed; the others come from DECODE_HEVC_DIR, as for the conformance test.
func TestHEVCFramesOutliveTheirPicturesInEveryLayout(t *testing.T) {
	streams := map[string]string{"yuv444_unsupported": "testdata/hevc/streams/yuv444_unsupported.265"}
	if dir := os.Getenv("DECODE_HEVC_DIR"); dir != "" {
		for _, name := range []string{
			"GENERAL_8b_400_RExt_Sony_1", "GENERAL_16b_400_RExt_Sony_1", "GENERAL_12b_422_RExt_Sony_1",
			"GENERAL_16b_444_RExt_Sony_2", "Bitdepth_A_RExt_Sony_1", "TSUNEQBD_A_MAIN10_Technicolor_2",
		} {
			if p := conformanceStream(dir, name); p != "" {
				streams[name] = p
			}
		}
	}
	for name, path := range streams {
		t.Run(name, func(t *testing.T) {
			raw := readFile(t, path)
			d, err := NewHEVC(Config{MaxFrameMBs: 1 << 17}, 4)
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
				if err != nil || len(fs) == 0 {
					t.Fatalf("pass %d: %d frames, %v", pass, len(fs), err)
				}
				kept = append(kept, fs...)
				at = append(at, sums(fs))
			}
			if !slices.Equal(at[0], at[1]) {
				t.Error("the second pass, on recycled planes, decoded differently")
			}
			n := len(kept) / 2
			if later := append(sums(kept[:n]), sums(kept[n:])...); !slices.Equal(later, slices.Concat(at...)) {
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

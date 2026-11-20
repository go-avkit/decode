// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"testing"

	"github.com/go-avkit/h265"
)

// TestHEVCFrameBytesAreBounded bounds the BYTES of a Frame, not its samples, on both sides, and
// the same on a 32-bit host (CI's 386 lane): at 10 bits a 4:2:0 frame takes 3 bytes a luma
// sample, so with 8 columns the last height that fits is 89478480 (a multiple of the 8-sample
// coding block) and the next one is over. w*h*2 wraps a 32-bit int long before that (#108).
func TestHEVCFrameBytesAreBounded(t *testing.T) {
	const width = 8
	for _, tc := range []struct {
		height uint64
		over   bool
	}{{89478480, false}, {89478488, true}} {
		// the SPS, when it arrives
		d, _ := NewHEVC(Config{MaxFrameMBs: 1 << 23}, 4)
		u, ok := hevcUnit(formatSPS(width, tc.height, 1, 10, 10, 0), 0)
		if !ok {
			t.Fatal("SPS is not a unit")
		}
		err := d.hevcFormat(u)
		if got := errors.Is(err, ErrOverLimit); got != tc.over || (!tc.over && err != nil) {
			t.Fatalf("SPS %dx%d: %v, over limit %v", width, tc.height, err, tc.over)
		}
		// the picture, as the decoder hands it: its planes are made-up (empty), so a picture
		// that fits is refused later, as a malformed one, and one that does not is over limit
		o := h265.Output{Picture: &h265.DecodedPicture{
			Components: 3, BitDepth: [3]int{10, 10, 10},
			Planes: [3]h265.Plane{
				{Width: width, Height: int(tc.height), Stride: width},
				{Width: width / 2, Height: int(tc.height+1) / 2, Stride: width / 2},
				{Width: width / 2, Height: int(tc.height+1) / 2, Stride: width / 2},
			},
		}}
		_, err = d.hevcFrame(o)
		if got := errors.Is(err, ErrOverLimit); got != tc.over || !errors.Is(err, ErrOverLimit) && !errors.Is(err, ErrBitstream) {
			t.Fatalf("picture %dx%d: %v, over limit %v", width, tc.height, err, tc.over)
		}
	}
}

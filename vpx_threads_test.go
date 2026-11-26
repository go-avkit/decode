// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"testing"

	"github.com/thesyncim/govpx"
)

// TestVPXThreadsMatchSerial: on every committed stream, Config.Threads gives the serial
// decoder's pictures, byte for byte, with the same count and timestamps.
func TestVPXThreadsMatchSerial(t *testing.T) {
	for _, s := range vpxStreams {
		t.Run(s.name, func(t *testing.T) {
			r, id := vpxTrack(t, s.name)
			serial, err := DecodeTrack(r, id, Config{})
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range []int{1, 2, 4, 8} {
				got, err := DecodeTrack(r, id, Config{Threads: n})
				if err != nil {
					t.Fatalf("Threads %d: %v", n, err)
				}
				if len(got) != len(serial) {
					t.Fatalf("Threads %d: %d pictures, serial %d", n, len(got), len(serial))
				}
				for i := range serial {
					if sum(got[i]) != sum(serial[i]) || got[i].PTS != serial[i].PTS {
						t.Errorf("Threads %d: picture %d differs from the serial decoder's", n, i)
					}
				}
			}
		})
	}
}

// TestVPXThreadsOptions pins the mapping to govpx: serial below 2; VP9 Threads with row-MT and
// never loop-filter opt; VP8 two threads at most.
func TestVPXThreadsOptions(t *testing.T) {
	for _, tc := range []struct {
		threads int
		t9      int
		rowMT   bool
		t8      int
	}{{0, 0, false, 0}, {1, 0, false, 0}, {2, 2, true, 2}, {4, 4, true, 2}, {8, 8, true, 2}} {
		o9 := vp9Options(Config{Threads: tc.threads}, 64)
		o8 := vp8Options(Config{Threads: tc.threads}, 64)
		if o9.Threads != tc.t9 || o9.DecoderRowMT != tc.rowMT || o9.DecoderLoopFilterOpt || o8.Threads != tc.t8 {
			t.Errorf("Threads %d: VP9 %d rowMT %v lfopt %v, VP8 %d; want VP9 %d rowMT %v, VP8 %d",
				tc.threads, o9.Threads, o9.DecoderRowMT, o9.DecoderLoopFilterOpt, o8.Threads, tc.t9, tc.rowMT, tc.t8)
		}
	}
}

// TestVPXThreadsPassedToGovpx: the constructors receive the mapped options.
func TestVPXThreadsPassedToGovpx(t *testing.T) {
	old8, old9 := newGovpx8, newGovpx9
	defer func() { newGovpx8, newGovpx9 = old8, old9 }()
	var got8 govpx.DecoderOptions
	var got9 govpx.VP9DecoderOptions
	newGovpx8 = func(o govpx.DecoderOptions) (*govpx.VP8Decoder, error) { got8 = o; return old8(o) }
	newGovpx9 = func(o govpx.VP9DecoderOptions) (*govpx.VP9Decoder, error) { got9 = o; return old9(o) }
	for _, mk := range []func(Config) (*Decoder, error){NewVP8, NewVP9} {
		d, err := mk(Config{Threads: 4})
		if err != nil {
			t.Fatal(err)
		}
		d.Close()
	}
	if got8.Threads != 2 || got9.Threads != 4 || !got9.DecoderRowMT {
		t.Errorf("govpx got VP8 %+v, VP9 %+v", got8, got9)
	}
}

func TestVPXThreadsNegativeRefused(t *testing.T) {
	for _, mk := range []func(Config) (*Decoder, error){NewVP8, NewVP9} {
		if _, err := mk(Config{Threads: -1}); !errors.Is(err, ErrConfig) {
			t.Errorf("Threads -1: %v, want ErrConfig", err)
		}
	}
}

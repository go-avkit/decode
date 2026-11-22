// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// failOnPanic turns a crash into the failure it stands for: a parser that reads
// past its input instead of refusing it has lost a bound, and the row says which.
func failOnPanic(t *testing.T) {
	t.Helper()
	if p := recover(); p != nil {
		t.Fatalf("read past the input instead of refusing it: %v", p)
	}
}

// Each bound of ParseAVCC and avcSets, asserted on both sides: the record one
// byte short is refused with the error that names it, and the record that just
// fits is accepted. A guard that only a crash defends is one an edit can drop.
// Profile 66 (Baseline) keeps the High-profile extension out of these records.
func TestParseAVCCBounds(t *testing.T) {
	head := []byte{1, 66, 0, 30, 0xff} // version, profile, compat, level, 4-byte lengths
	rec := func(tail ...byte) []byte { return append(append([]byte(nil), head...), tail...) }
	for _, tc := range []struct {
		name string
		b    []byte
		want string // "" = accepted
	}{
		{"6 bytes, below the fixed part", rec(0xe0), "fixed part is 7"},
		{"7 bytes, no sets", rec(0xe0, 0), ""},
		{"ends inside an SPS length", rec(0xe1, 0), "ends inside a SPS length"},
		{"SPS of 0 bytes", rec(0xe1, 0, 0, 0), "SPS of 0 bytes"},
		{"SPS longer than what is left", rec(0xe1, 0, 2, 0x67), "SPS of 2 bytes with 1 left"},
		{"ends before the PPS count", rec(0xe1, 0, 1, 0x67), "ends before numOfPictureParameterSets"},
		{"SPS that just fits, no PPS", rec(0xe1, 0, 1, 0x67, 0), ""},
		{"ends inside a PPS length", rec(0xe1, 0, 1, 0x67, 1, 0), "ends inside a PPS length"},
		{"PPS that just fits", rec(0xe1, 0, 1, 0x67, 1, 0, 1, 0x68), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer failOnPanic(t)
			c, err := ParseAVCC(tc.b)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if c.LengthSize != 4 {
					t.Fatalf("LengthSize %d, want 4", c.LengthSize)
				}
				return
			}
			if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want ErrConfig naming %q", err, tc.want)
			}
		})
	}
}

// Each bound of splitSample, on both sides: a length prefix cut short, a zero
// length and a length past the end are refused with ErrSample, and a sample whose
// last unit ends exactly at its end is split whole.
func TestSplitSampleBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  []byte
		want  string // "" = accepted
		units int
	}{
		{"3 bytes, a length is 4", []byte{0, 0, 0}, "3 bytes left, a length is 4", 0},
		{"unit of 0 bytes", []byte{0, 0, 0, 0}, "NAL unit of 0 bytes", 0},
		{"unit past the end", []byte{0, 0, 0, 2, 9}, "NAL unit of 2 bytes with 1 left", 0},
		{"unit that just fits", []byte{0, 0, 0, 1, 9}, "", 1},
		{"two units, the second just fits", []byte{0, 0, 0, 1, 9, 0, 0, 0, 2, 9, 9}, "", 2},
		{"empty sample", nil, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer failOnPanic(t)
			units, err := splitSample(tc.data, 4)
			if tc.want == "" {
				if err != nil || len(units) != tc.units {
					t.Fatalf("%d units, %v; want %d units", len(units), err, tc.units)
				}
				return
			}
			if !errors.Is(err, ErrSample) || !strings.Contains(err.Error(), tc.want) || units != nil {
				t.Fatalf("%d units, err %v; want no unit and ErrSample naming %q", len(units), err, tc.want)
			}
		})
	}
}

// walkSpan refuses a track it has no decoder for with NewTrack's *CodecError,
// before any sample is read and without emitting anything.
func TestWalkSpanRefusesUnknownCodec(t *testing.T) {
	// Without the refusal the walk goes on with no decoder and crashes; that is
	// reported here as the failure it is, not left to the crash.
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("walkSpan went on without a decoder and panicked: %v", p)
		}
	}()
	emitted := 0
	stopped, err := walkSpan(container.TrackConfig{Codec: "av01"}, nil, 0, Config{}, nil, func(Frame) bool {
		emitted++
		return true
	})
	var ce *CodecError
	if !errors.As(err, &ce) || ce.Codec != "av01" || stopped || emitted != 0 {
		t.Fatalf("stopped %v, %d emitted, err %v; want a *CodecError for av01", stopped, emitted, err)
	}
}

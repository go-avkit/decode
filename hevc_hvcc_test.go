// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"testing"
)

func TestParseHVCC(t *testing.T) {
	rec := hvcC([][]byte{{0x40, 1, 9}}, [][]byte{{0x42, 1, 8}}, [][]byte{{0x44, 1, 7}, {0x44, 1, 6}}, 4)
	rec[1] = 0x20 | 2 // tier, profile_idc 2
	rec[12] = 93
	rec[16], rec[17], rec[18] = 1, 2, 2
	c, err := ParseHVCC(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Tier || c.Profile != 2 || c.ProfileSpace != 0 || c.Level != 93 || c.ChromaFormat != 1 ||
		c.BitDepthLuma != 10 || c.BitDepthChroma != 10 || c.LengthSize != 4 ||
		len(c.VPS) != 1 || len(c.SPS) != 1 || len(c.PPS) != 2 || c.PPS[1][2] != 6 {
		t.Fatalf("%+v", c)
	}

	// An array of another type (a prefix SEI) is skipped.
	sei := append(append([]byte(nil), rec...), 0x80|39, 0, 1, 0, 2, 0x4e, 1)
	sei[22] = 4
	if c, err = ParseHVCC(sei); err != nil || len(c.VPS)+len(c.SPS)+len(c.PPS) != 4 {
		t.Fatalf("an SEI array: %v %+v", err, c)
	}

	cases := map[string]func([]byte) []byte{
		"short":             func(b []byte) []byte { return b[:22] },
		"version 2":         func(b []byte) []byte { b[0] = 2; return b },
		"3-byte lengths":    func(b []byte) []byte { b[21] = 0xfe; return b },
		"no array header":   func(b []byte) []byte { return b[:24] },
		"no NAL length":     func(b []byte) []byte { return b[:27] },
		"a NAL cut short":   func(b []byte) []byte { return b[:len(b)-1] },
		"an empty NAL":      func(b []byte) []byte { b[26], b[27] = 0, 0; return b },
		"more arrays named": func(b []byte) []byte { b[22] = 9; return b },
	}
	for name, mutate := range cases {
		if _, err := ParseHVCC(mutate(append([]byte(nil), rec...))); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

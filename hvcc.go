// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"fmt"

	"github.com/go-avkit/avkit/container"
)

// HEVC NAL unit types that carry parameter sets (ITU-T H.265 Table 7-1).
const (
	nalVPS = 32
	nalSPS = 33
	nalPPS = 34
)

// HVCConfig is an HEVCDecoderConfigurationRecord, the content of the hvcC box
// (ISO/IEC 14496-15, clause 8.3.3). The standard's text is paywalled and was not
// opened; the layout is that of mp4ff v0.62.0,
// hevc/hevcdecoderconfigurationrecord.go (DecodeHEVCDecConfRec and the DecConfRec
// type), which was.
type HVCConfig struct {
	// ProfileSpace, Tier, Profile and Level are general_profile_space,
	// general_tier_flag, general_profile_idc and general_level_idc, as the
	// sequence parameter set states them.
	ProfileSpace uint8
	Tier         bool
	Profile      uint8
	Level        uint8
	// ChromaFormat is chroma_format_idc; BitDepthLuma and BitDepthChroma are the
	// sample bit depths (the record stores them minus eight).
	ChromaFormat                 uint8
	BitDepthLuma, BitDepthChroma uint8
	// LengthSize is the size in bytes of each NAL unit's length prefix:
	// lengthSizeMinusOne + 1: 1, 2 or 4 (3 is reserved).
	LengthSize int
	// VPS, SPS and PPS are the parameter sets of the record's arrays, as NAL
	// units without a prefix. Arrays of any other NAL unit type (SEI) are
	// skipped.
	VPS, SPS, PPS [][]byte
}

// hvcFixed is the size of the fixed part of the record, up to and including
// numOfArrays.
const hvcFixed = 23

// ParseHVCC parses an HEVCDecoderConfigurationRecord. Every length it reads is
// checked against what is left of the record before anything is kept, so a
// record that announces more than it holds is refused, never trusted.
func ParseHVCC(b []byte) (HVCConfig, error) {
	var c HVCConfig
	if len(b) < hvcFixed {
		return c, fmt.Errorf("%w: hvcC of %d bytes, the fixed part is %d", ErrConfig, len(b), hvcFixed)
	}
	if b[0] != 1 {
		return c, fmt.Errorf("%w: hvcC configurationVersion %d, want 1", ErrConfig, b[0])
	}
	c.ProfileSpace, c.Tier, c.Profile = b[1]>>6, b[1]&0x20 != 0, b[1]&0x1f
	c.Level = b[12]
	c.ChromaFormat = b[16] & 3
	c.BitDepthLuma, c.BitDepthChroma = b[17]&7+8, b[18]&7+8
	c.LengthSize = int(b[21]&3) + 1
	if c.LengthSize == 3 {
		return c, fmt.Errorf("%w: hvcC lengthSizeMinusOne 2 (3-byte lengths) is not allowed", ErrConfig)
	}
	pos := hvcFixed
	for range int(b[22]) {
		if pos+3 > len(b) {
			return c, fmt.Errorf("%w: hvcC ends inside an array header", ErrConfig)
		}
		typ, n := b[pos]&0x3f, int(binary.BigEndian.Uint16(b[pos+1:]))
		pos += 3
		for range n {
			if pos+2 > len(b) {
				return c, fmt.Errorf("%w: hvcC ends inside a NAL unit length", ErrConfig)
			}
			l := int(binary.BigEndian.Uint16(b[pos:]))
			pos += 2
			if l == 0 || l > len(b)-pos {
				return c, fmt.Errorf("%w: hvcC NAL unit of %d bytes with %d left", ErrConfig, l, len(b)-pos)
			}
			switch typ {
			case nalVPS:
				c.VPS = append(c.VPS, b[pos:pos+l])
			case nalSPS:
				c.SPS = append(c.SPS, b[pos:pos+l])
			case nalPPS:
				c.PPS = append(c.PPS, b[pos:pos+l])
			}
			pos += l
		}
	}
	return c, nil
}

// NewHVCC returns an HEVC decoder configured by an hvcC record, as MP4 stores
// it. Samples are length-prefixed with the record's length size.
func NewHVCC(hvcC []byte, cfg Config) (*Decoder, error) {
	c, err := ParseHVCC(hvcC)
	if err != nil {
		return nil, err
	}
	return newHEVCWithSets(cfg, c.LengthSize, c.VPS, c.SPS, c.PPS)
}

// isHEVC reports whether a container track's sample entry names HEVC.
func isHEVC(tc container.TrackConfig) bool {
	return tc.Codec == "hvc1" || tc.Codec == "hev1"
}

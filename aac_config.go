// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"fmt"
)

// AudioSpecificConfig and the ADTS header, from ISO/IEC 14496-3 as known to the
// author (clause and table numbers not verified). Only AAC-LC (audio object type 2) is decoded: Main, SSR, LTP,
// SBR and PS are refused with ErrAudioUnsupported.

// aacRates are the sampling frequencies of sampling_frequency_index 0 to 12.
var aacRates = [13]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

// aacChannelsOf is the channel count of channel_configuration 0 to 7; 0 means a
// program_config_element states it. Configurations 11, 12 and 14 are in
// aacChannelConfig.
var aacChannelsOf = [8]int{0, 1, 2, 3, 4, 5, 6, 8}

// Audio object types.
const (
	aacObjMain = 1
	aacObjLC   = 2
	aacObjSBR  = 5
	aacObjPS   = 29
)

// Syntactic element ids (id_syn_ele).
const (
	aacIDSCE = iota
	aacIDCPE
	aacIDCCE
	aacIDLFE
	aacIDDSE
	aacIDPCE
	aacIDFIL
	aacIDEND
)

// aacPCEElem is one channel element a program_config_element lists.
type aacPCEElem struct {
	id  uint8 // aacIDSCE, aacIDCPE or aacIDLFE
	tag uint8 // element_instance_tag
	// pos is 0 front, 1 side, 2 back, 3 lfe.
	pos uint8
}

// aacPCE is a program_config_element, reduced to what a decoder needs: the
// channel elements in the order they are listed.
type aacPCE struct {
	rateIndex int
	elems     []aacPCEElem
}

// channels returns how many audio channels the elements make.
func (p *aacPCE) channels() int {
	n := 0
	for _, e := range p.elems {
		n++
		if e.id == aacIDCPE {
			n++
		}
	}
	return n
}

// parsePCE reads a program_config_element (Table 4.2) after its element id.
func parsePCE(r *aacBits) (*aacPCE, error) {
	r.skip(4) // element_instance_tag
	r.skip(2) // object_type
	p := &aacPCE{rateIndex: int(r.read(4))}
	nFront, nSide, nBack, nLFE := int(r.read(4)), int(r.read(4)), int(r.read(4)), int(r.read(2))
	nAssoc, nCC := int(r.read(3)), int(r.read(4))
	if r.bit() != 0 { // mono_mixdown_present
		r.skip(4)
	}
	if r.bit() != 0 { // stereo_mixdown_present
		r.skip(4)
	}
	if r.bit() != 0 { // matrix_mixdown_idx_present
		r.skip(3)
	}
	for _, g := range [3]struct{ n, pos int }{{nFront, 0}, {nSide, 1}, {nBack, 2}} {
		for i := 0; i < g.n; i++ {
			id := uint8(aacIDSCE)
			if r.bit() != 0 {
				id = aacIDCPE
			}
			p.elems = append(p.elems, aacPCEElem{id: id, tag: uint8(r.read(4)), pos: uint8(g.pos)})
		}
	}
	for i := 0; i < nLFE; i++ {
		p.elems = append(p.elems, aacPCEElem{id: aacIDLFE, tag: uint8(r.read(4)), pos: 3})
	}
	r.skip(4 * nAssoc)
	r.skip(5 * nCC)
	r.align()
	r.skip(8 * int(r.read(8))) // comment_field_bytes and the comment
	if r.overrun {
		return nil, fmt.Errorf("%w: truncated program_config_element", ErrAudioConfig)
	}
	return p, nil
}

// aacConfig is what an AudioSpecificConfig states that AAC-LC decoding uses.
type aacConfig struct {
	rateIndex int // sampling_frequency_index, 0 to 12
	rate      int // Hz
	channels  int
	// config is channel_configuration; pce is set when it is 0.
	config int
	pce    *aacPCE
}

// aacChannelConfig returns the channel count of a channel_configuration
// or 0 when it is reserved or 0 (a PCE follows).
func aacChannelConfig(c int) int {
	switch {
	case c >= 1 && c <= 7:
		return aacChannelsOf[c]
	case c == 11:
		return 7
	case c == 12 || c == 14:
		return 8
	}
	return 0
}

// aacObjectType reads GetAudioObjectType().
func aacObjectType(r *aacBits) int {
	t := int(r.read(5))
	if t == 31 {
		t = 32 + int(r.read(6))
	}
	return t
}

// aacRateIndex reads a samplingFrequencyIndex and the explicit rate that goes
// with index 15, and returns the index of the table row the rate is in.
func aacRateIndex(r *aacBits) (int, error) {
	i := int(r.read(4))
	if i == 15 {
		hz := int(r.read(24))
		for j, f := range aacRates {
			if f == hz {
				return j, nil
			}
		}
		return 0, fmt.Errorf("%w: explicit sampling frequency %d Hz", ErrAudioUnsupported, hz)
	}
	if i >= len(aacRates) {
		return 0, fmt.Errorf("%w: sampling_frequency_index %d is reserved", ErrAudioConfig, i)
	}
	return i, nil
}

// parseAudioSpecificConfig reads an AudioSpecificConfig. It accepts
// AAC-LC only. An explicit SBR or PS signal, or a backward compatible one after the
// GASpecificConfig, is HE-AAC: ErrAudioUnsupported.
func parseAudioSpecificConfig(b []byte) (aacConfig, error) {
	var c aacConfig
	r := newAACBits(b)
	obj := aacObjectType(r)
	ri, err := aacRateIndex(r)
	if err != nil {
		return c, err
	}
	c.config = int(r.read(4))
	if r.overrun {
		return c, fmt.Errorf("%w: truncated AudioSpecificConfig", ErrAudioConfig)
	}
	switch obj {
	case aacObjLC:
	case aacObjSBR, aacObjPS:
		return c, fmt.Errorf("%w: HE-AAC (audio object type %d): SBR and PS are not decoded", ErrAudioUnsupported, obj)
	default:
		return c, fmt.Errorf("%w: AAC audio object type %d, only AAC-LC (2)", ErrAudioUnsupported, obj)
	}
	// GASpecificConfig (Table 4.1).
	if r.bit() != 0 {
		return c, fmt.Errorf("%w: AAC-LC with 960-sample frames", ErrAudioUnsupported)
	}
	if r.bit() != 0 { // dependsOnCoreCoder
		return c, fmt.Errorf("%w: AAC-LC over a core coder", ErrAudioUnsupported)
	}
	r.skip(1) // extensionFlag: its fields exist for the error resilient types only
	if c.config == 0 {
		p, err := parsePCE(r)
		if err != nil {
			return c, err
		}
		c.pce = p
		if c.channels = p.channels(); c.channels == 0 {
			return c, fmt.Errorf("%w: a program_config_element with no channel", ErrAudioConfig)
		}
	} else if c.channels = aacChannelConfig(c.config); c.channels == 0 {
		return c, fmt.Errorf("%w: channel_configuration %d", ErrAudioConfig, c.config)
	}
	// The three flags above cannot overrun: the bytes that held the fields before
	// them hold three more bits, whatever the escapes made the fields take.
	//
	// syncExtensionType 0x2b7 then extensionAudioObjectType 5 and sbrPresentFlag:
	// backward compatible SBR. 0x548 is PS. Both are HE-AAC.
	if r.left() >= 16 {
		switch r.read(11) {
		case 0x2b7:
			if aacObjectType(r) == aacObjSBR && r.bit() != 0 {
				return c, fmt.Errorf("%w: HE-AAC (backward compatible SBR signalled)", ErrAudioUnsupported)
			}
		case 0x548:
			if r.bit() != 0 {
				return c, fmt.Errorf("%w: HE-AAC v2 (PS signalled)", ErrAudioUnsupported)
			}
		}
	}
	c.rateIndex, c.rate = ri, aacRates[ri]
	return c, nil
}

// aacADTS is a parsed ADTS fixed and variable header.
type aacADTS struct {
	mpeg2         bool // ID: MPEG-2 (1) or MPEG-4 (0)
	protected     bool // a 16-bit CRC follows the header
	profile       int  // audio object type minus one
	rateIndex     int
	channelConfig int
	frameLength   int // bytes, header included
	blocks        int // number_of_raw_data_blocks_in_frame + 1
	headerLength  int // 7, or 9 with a CRC
}

// parseADTS reads the ADTS header at the start of b.
func parseADTS(b []byte) (aacADTS, error) {
	var h aacADTS
	if len(b) < 7 {
		return h, fmt.Errorf("%w: ADTS header needs 7 bytes, have %d", ErrAudioSample, len(b))
	}
	r := newAACBits(b)
	if r.read(12) != 0xfff {
		return h, fmt.Errorf("%w: no ADTS syncword", ErrAudioSample)
	}
	h.mpeg2 = r.bit() == 1
	if r.read(2) != 0 {
		return h, fmt.Errorf("%w: ADTS layer is not 0", ErrAudioSample)
	}
	h.protected = r.bit() == 0
	h.profile = int(r.read(2))
	h.rateIndex = int(r.read(4))
	r.skip(1) // private_bit
	h.channelConfig = int(r.read(3))
	r.skip(4) // original_copy, home, copyright_identification_bit and _start
	h.frameLength = int(r.read(13))
	r.skip(11) // adts_buffer_fullness
	h.blocks = int(r.read(2)) + 1
	h.headerLength = 7
	if h.protected {
		h.headerLength = 9
	}
	switch {
	case h.rateIndex >= len(aacRates):
		return h, fmt.Errorf("%w: ADTS sampling_frequency_index %d", ErrAudioSample, h.rateIndex)
	case h.frameLength < h.headerLength || h.frameLength > len(b):
		return h, fmt.Errorf("%w: ADTS frame of %d bytes in %d", ErrAudioSample, h.frameLength, len(b))
	}
	return h, nil
}

// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"testing"
)

// asc builds an AudioSpecificConfig: object type, sampling index, channel
// configuration, the GASpecificConfig flags, then whatever tail writes.
func asc(obj, rate, ch int, flags [3]uint, tail func(w *bitWriter)) []byte {
	w := &bitWriter{}
	if obj >= 31 {
		w.bits(31, 5)
		w.bits(uint(obj-32), 6)
	} else {
		w.bits(uint(obj), 5)
	}
	w.bits(uint(rate), 4)
	if rate == 15 {
		w.bits(44100, 24)
	}
	w.bits(uint(ch), 4)
	w.bits(flags[0], 1)
	w.bits(flags[1], 1)
	w.bits(flags[2], 1)
	if tail != nil {
		tail(w)
	}
	return w.bytes()
}

// ascHE builds an AudioSpecificConfig with explicit SBR (object type 5) or PS (29): the
// core rate and channel configuration, the extension rate, the object type of the
// core, and the GASpecificConfig flags.
func ascHE(ps bool, core, ch, ext, inner int) []byte {
	w := &bitWriter{}
	obj := uint(5)
	if ps {
		obj = 29
	}
	w.bits(obj, 5)
	w.bits(uint(core), 4)
	w.bits(uint(ch), 4)
	w.bits(uint(ext), 4)
	w.bits(uint(inner), 5)
	w.bits(0, 3)
	return w.bytes()
}

// ascBackward builds an AAC-LC AudioSpecificConfig with the backward compatible SBR
// extension (extension rate index ext), and the PS extension after it when ps.
func ascBackward(core, ch, ext int, ps bool) []byte {
	return asc(2, core, ch, [3]uint{}, func(w *bitWriter) {
		w.bits(0x2b7, 11)
		w.bits(5, 5)
		w.bits(1, 1)
		w.bits(uint(ext), 4)
		if ps {
			w.bits(0x548, 11)
			w.bits(1, 1)
		}
	})
}

// TestAACConfigHE: the ways an AudioSpecificConfig signals SBR and PS, and the
// output rate they make.
func TestAACConfigHE(t *testing.T) {
	for name, tc := range map[string]struct {
		b             []byte
		rate, out, ch int
		ps            bool
	}{
		"explicit SBR":                {ascHE(false, 7, 2, 4, 2), 22050, 44100, 2, false},
		"explicit PS over mono":       {ascHE(true, 7, 1, 4, 2), 22050, 44100, 1, true},
		"backward compatible SBR":     {ascBackward(7, 2, 4, false), 22050, 44100, 2, false},
		"backward compatible SBR, PS": {ascBackward(7, 1, 4, true), 22050, 44100, 1, true},
		"PS extension alone":          {asc(2, 7, 1, [3]uint{}, func(w *bitWriter) { w.bits(0x548, 11); w.bits(1, 1) }), 22050, 44100, 1, true},
		"explicit SBR at 8 kHz":       {ascHE(false, 11, 1, 8, 2), 8000, 16000, 1, false},
		"explicit SBR at 48 kHz":      {ascHE(false, 3, 2, 0, 2), 48000, 96000, 2, false},
	} {
		c, err := parseAudioSpecificConfig(tc.b)
		if err != nil || !c.sbr || c.rate != tc.rate || c.outRate() != tc.out || c.channels != tc.ch || c.ps != tc.ps {
			t.Errorf("%s: %+v %v", name, c, err)
		}
	}
	if c, _ := parseAudioSpecificConfig(asc(2, 7, 1, [3]uint{}, nil)); c.sbr || c.outRate() != 22050 {
		t.Errorf("AAC-LC: %+v", c)
	}
}

func TestAACConfigAccepted(t *testing.T) {
	c, err := parseAudioSpecificConfig(asc(2, 4, 2, [3]uint{}, nil))
	if err != nil || c.rate != 44100 || c.rateIndex != 4 || c.channels != 2 || c.config != 2 || c.pce != nil {
		t.Fatalf("%+v %v", c, err)
	}
	for cfg, want := range map[int]int{1: 1, 2: 2, 3: 3, 4: 4, 5: 5, 6: 6, 7: 8, 11: 7, 12: 8, 14: 8} {
		c, err := parseAudioSpecificConfig(asc(2, 3, cfg, [3]uint{}, nil))
		if err != nil || c.channels != want {
			t.Errorf("channel configuration %d: %d channels, %v, want %d", cfg, c.channels, err, want)
		}
	}
	// an explicit sampling frequency that is a table rate
	c, err = parseAudioSpecificConfig(asc(2, 15, 1, [3]uint{}, nil))
	if err != nil || c.rateIndex != 4 {
		t.Fatalf("explicit rate: %+v %v", c, err)
	}
	// the channels come from a program_config_element when the configuration is 0
	c, err = parseAudioSpecificConfig(asc(2, 3, 0, [3]uint{}, func(w *bitWriter) { pcePayload(w) }))
	if err != nil || c.channels != 6 || c.pce == nil || c.config != 0 {
		t.Fatalf("pce: %+v %v", c, err)
	}
	// extensions that say there is no SBR or PS change nothing
	for name, tail := range map[string]func(w *bitWriter){
		"sbr absent":        func(w *bitWriter) { w.bits(0x2b7, 11); w.bits(5, 5); w.bits(0, 1) },
		"another extension": func(w *bitWriter) { w.bits(0x2b7, 11); w.bits(22, 5); w.bits(1, 1) },
		"ps absent":         func(w *bitWriter) { w.bits(0x548, 11); w.bits(0, 1); w.bits(0, 4) },
		"unknown sync":      func(w *bitWriter) { w.bits(0x123, 11); w.bits(0, 5) },
	} {
		if _, err := parseAudioSpecificConfig(asc(2, 4, 2, [3]uint{}, tail)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAACConfigRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		b    []byte
		kind error
		text string
	}{
		"empty":                {nil, ErrAudioConfig, "truncated"},
		"downsampled SBR":      {ascHE(false, 4, 2, 4, 2), ErrAudioUnsupported, "downsampled"},
		"SBR at the wrong ext": {ascHE(false, 7, 2, 7, 2), ErrAudioUnsupported, "downsampled"},
		"SBR over 7350 Hz":     {asc(2, 12, 1, [3]uint{}, func(w *bitWriter) { w.bits(0x548, 11); w.bits(1, 1) }), ErrAudioUnsupported, "SBR over a core at 7350"},
		"PS over stereo":       {ascHE(true, 7, 2, 4, 2), ErrAudioUnsupported, "Parametric Stereo over 2"},
		"SBR over Main":        {ascHE(false, 7, 2, 4, 1), ErrAudioUnsupported, "object type 1"},
		"cut after the object": {ascHE(false, 7, 2, 4, 2)[:3], ErrAudioConfig, "truncated"},
		"cut in the extension": {ascHE(false, 7, 2, 4, 2)[:2], ErrAudioConfig, "truncated"},
		"bad extension rate":   {ascHE(false, 7, 2, 13, 2), ErrAudioConfig, "reserved"},
		"Main":                 {asc(1, 4, 2, [3]uint{}, nil), ErrAudioUnsupported, "object type 1"},
		"LTP":                  {asc(4, 4, 2, [3]uint{}, nil), ErrAudioUnsupported, "object type 4"},
		"USAC (escape)":        {asc(42, 4, 2, [3]uint{}, nil), ErrAudioUnsupported, "object type 42"},
		"reserved rate":        {asc(2, 13, 2, [3]uint{}, nil), ErrAudioConfig, "reserved"},
		"odd rate": {func() []byte {
			w := &bitWriter{}
			w.bits(2, 5)
			w.bits(15, 4)
			w.bits(12345, 24)
			w.bits(2, 4)
			return w.bytes()
		}(), ErrAudioUnsupported, "12345 Hz"},
		"960 frames":                         {asc(2, 4, 2, [3]uint{1, 0, 0}, nil), ErrAudioUnsupported, "960"},
		"core coder":                         {asc(2, 4, 2, [3]uint{0, 1, 0}, nil), ErrAudioUnsupported, "core coder"},
		"reserved chans":                     {asc(2, 4, 8, [3]uint{}, nil), ErrAudioConfig, "channel_configuration 8"},
		"no channels":                        {asc(2, 4, 15, [3]uint{}, nil), ErrAudioConfig, "channel_configuration 15"},
		"bad PCE":                            {asc(2, 4, 0, [3]uint{}, func(w *bitWriter) { w.bits(0xffff, 16) }), ErrAudioConfig, "program_config_element"},
		"truncated":                          {[]byte{0x12}, ErrAudioConfig, "truncated"},
		"PCE of nothing":                     {asc(2, 4, 0, [3]uint{}, func(w *bitWriter) { w.bits(0, 32); w.bits(0, 24) }), ErrAudioConfig, "no channel"},
		"backward compatible, downsampled":   {ascBackward(4, 2, 4, false), ErrAudioUnsupported, "downsampled"},
		"backward compatible PS over stereo": {ascBackward(7, 2, 4, true), ErrAudioUnsupported, "Parametric Stereo over 2"},
		"backward compatible, cut":           {asc(2, 7, 2, [3]uint{}, func(w *bitWriter) { w.bits(0x2b7, 11); w.bits(5, 5); w.bits(1, 1) })[:4], ErrAudioConfig, "truncated"},
		"backward compatible, bad rate":      {asc(2, 7, 2, [3]uint{}, func(w *bitWriter) { w.bits(0x2b7, 11); w.bits(5, 5); w.bits(1, 1); w.bits(13, 4) }), ErrAudioConfig, "reserved"},
		"PCE then cut":                       {asc(2, 4, 0, [3]uint{}, func(w *bitWriter) { pcePayload(w) })[:5], ErrAudioConfig, "program_config_element"},
	} {
		_, err := parseAudioSpecificConfig(tc.b)
		wantErr(t, name, err, tc.kind, tc.text)
	}
}

func TestAACChannelConfig(t *testing.T) {
	for c, want := range [16]int{0, 1, 2, 3, 4, 5, 6, 8, 0, 0, 0, 7, 8, 0, 8, 0} {
		if got := aacChannelConfig(c); got != want {
			t.Errorf("configuration %d: %d, want %d", c, got, want)
		}
	}
}

// adts builds a 7 byte ADTS header followed by body, which it counts in the length.
func adts(prot bool, rate, ch, blocks, bodyLen int, mutate func(h []byte)) []byte {
	w := &bitWriter{}
	w.bits(0xfff, 12)
	w.bits(0, 1)
	w.bits(0, 2)
	if prot {
		w.bits(0, 1)
	} else {
		w.bits(1, 1)
	}
	w.bits(1, 2) // profile: LC
	w.bits(uint(rate), 4)
	w.bits(0, 1)
	w.bits(uint(ch), 3)
	w.bits(0, 4)
	hl := 7
	if prot {
		hl = 9
	}
	w.bits(uint(hl+bodyLen), 13)
	w.bits(0x7ff, 11)
	w.bits(uint(blocks-1), 2)
	b := w.bytes()
	b = append(b, make([]byte, hl-7+bodyLen)...)
	if mutate != nil {
		mutate(b)
	}
	return b
}

func TestAACADTS(t *testing.T) {
	h, err := parseADTS(adts(false, 4, 2, 1, 10, nil))
	if err != nil || h.headerLength != 7 || h.frameLength != 17 || h.rateIndex != 4 || h.channelConfig != 2 || h.profile != 1 || h.blocks != 1 || h.protected || h.mpeg2 {
		t.Fatalf("%+v %v", h, err)
	}
	h, err = parseADTS(adts(true, 3, 1, 3, 0, func(b []byte) { b[1] |= 8 }))
	if err != nil || h.headerLength != 9 || !h.protected || !h.mpeg2 || h.blocks != 3 || h.frameLength != 9 {
		t.Fatalf("%+v %v", h, err)
	}
	for name, tc := range map[string]struct {
		b    []byte
		text string
	}{
		"short":          {make([]byte, 6), "7 bytes"},
		"no sync":        {adts(false, 4, 2, 1, 0, func(b []byte) { b[0] = 0xfe }), "syncword"},
		"layer":          {adts(false, 4, 2, 1, 0, func(b []byte) { b[1] |= 2 }), "layer"},
		"reserved rate":  {adts(false, 13, 2, 1, 0, nil), "sampling_frequency_index 13"},
		"length < head":  {adts(true, 4, 2, 1, 0, func(b []byte) { b[3], b[4], b[5] = 0, 0, 0x1f }), "ADTS frame of"},
		"length > data":  {adts(false, 4, 2, 1, 10, func(b []byte) { b[3] |= 3; b[4] = 0xff }), "ADTS frame of"},
		"length no data": {adts(false, 4, 2, 1, 0, func(b []byte) { b[3] &^= 3; b[4] = 0; b[5] &= 0x1f }), "ADTS frame of"},
	} {
		_, err := parseADTS(tc.b)
		wantErr(t, name, err, ErrAudioSample, tc.text)
	}
}

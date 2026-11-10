// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// silentFrame writes a raw_data_block with the elements of a channel
// configuration, each channel an empty spectrum (global gain 100, no band).
func silentFrame(config int) []byte {
	w := &bitWriter{}
	for i, e := range aacConfigMaps[config] {
		w.bits(uint(e.id), 3)
		w.bits(uint(i), 4)
		if e.id == aacIDCPE {
			w.bits(0, 1)
		}
		for c := 0; c < aacChannelsOfID(e.id); c++ {
			ch := tChan{gain: 100}
			ch.write(w, false)
		}
	}
	return w.finish()
}

func aacTrack(rateIndex, config int) container.TrackConfig {
	return container.TrackConfig{
		Kind: container.Audio, Codec: "mp4a", CodecConfig: asc(2, rateIndex, config, [3]uint{}, nil),
		Timescale: uint32(aacRates[rateIndex]), SampleRate: aacRates[rateIndex], Channels: aacChannelConfig(config),
	}
}

func TestAACDecoderLayouts(t *testing.T) {
	for config, want := range map[int]ChannelLayout{
		1: LayoutMono, 2: LayoutStereo, 3: Layout3, 4: Layout3 | BackCenter,
		5: Layout5Back, 6: Layout51Back, 7: Layout51Back | FrontLeftOfCenter | FrontRightOfCenter,
	} {
		d, err := NewAudioTrack(aacTrack(aacRate44, config), AudioConfig{})
		if err != nil {
			t.Fatal(err)
		}
		out, err := d.DecodeSample(container.Sample{Data: silentFrame(config), Duration: 1024})
		if err != nil || len(out) != 1 {
			t.Fatalf("configuration %d: %v, %d buffers", config, err, len(out))
		}
		a := out[0]
		if a.Layout != want || a.Channels != want.Channels() || a.Format != SampleF32 || a.SampleRate != 44100 || a.Samples() != 1024 || a.PTS != 0 {
			t.Errorf("configuration %d: %+v", config, a)
		}
		for _, b := range a.Data {
			if b != 0 {
				t.Fatalf("configuration %d: a silent frame decoded to sound", config)
			}
		}
	}
	// where the channels of config 7 go in an interleaved sample: FL FR FC LFE BL BR FLC FRC
	c, err := newAAC(aacTrack(aacRate44, 7), AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.(*aacDecoder).slot; len(got) != 5 || got[0][0] != 2 || got[1] != [2]int{6, 7} || got[2] != [2]int{0, 1} || got[3] != [2]int{4, 5} || got[4][0] != 3 {
		t.Errorf("slots of configuration 7: %v", got)
	}
}

func TestAACDecoderRefusals(t *testing.T) {
	pce := asc(2, 3, 0, [3]uint{}, func(w *bitWriter) { pcePayload(w) })
	for name, tc := range map[string]struct {
		tc   container.TrackConfig
		cfg  AudioConfig
		kind error
		text string
	}{
		"no configuration": {container.TrackConfig{Codec: "mp4a"}, AudioConfig{}, ErrAudioConfig, "AudioSpecificConfig"},
		"HE-AAC":           {container.TrackConfig{Codec: "mp4a", CodecConfig: asc(5, 3, 2, [3]uint{}, nil)}, AudioConfig{}, ErrAudioUnsupported, "HE-AAC"},
		"a PCE":            {container.TrackConfig{Codec: "mp4a", CodecConfig: pce}, AudioConfig{}, ErrAudioUnsupported, "no fixed layout"},
		"configuration 11": {container.TrackConfig{Codec: "mp4a", CodecConfig: asc(2, 3, 11, [3]uint{}, nil)}, AudioConfig{}, ErrAudioUnsupported, "no fixed layout"},
		"block limit":      {aacTrack(aacRate44, 2), AudioConfig{MaxBlockSamples: 512}, ErrOverLimit, "1024"},
		"channel limit":    {aacTrack(aacRate44, 2), AudioConfig{MaxChannels: 1}, ErrOverLimit, "2 channels"},
	} {
		_, err := NewAudioTrack(tc.tc, tc.cfg)
		wantErr(t, name, err, tc.kind, tc.text)
	}
	d, err := newAAC(aacTrack(aacRate44, 2), AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// a frame that is not one, one with too few elements, one of the wrong kind
	w := &bitWriter{}
	w.bits(aacIDFIL, 3)
	w.bits(0, 4)
	_, err = d.decode(w.finish())
	wantErr(t, "no element", err, ErrAudioSample, "0 channel elements")
	_, err = d.decode(silentFrame(1))
	wantErr(t, "an SCE where a CPE is due", err, ErrAudioSample, "type 0")
	_, err = d.decode([]byte{0x00})
	wantErr(t, "a frame cut short", err, ErrAudioSample, "truncated")
}

// TestAACDecodeTrack decodes a fixture as an MP4 track: its first 1024 samples
// are the encoder's priming, which the edit list drops, and the edit list ends the
// presentation after 20000 samples.
func TestAACDecodeTrack(t *testing.T) {
	cfg, frames := aacADTSFrames(t, filepath.Join("testdata", "aac", "st44.aac"))
	ref, err := os.ReadFile(filepath.Join("testdata", "aac", "ref", "st44.f32"))
	if err != nil {
		t.Fatal(err)
	}
	tc := aacTrack(cfg.rateIndex, cfg.config)
	var samples []container.Sample
	for _, f := range frames {
		samples = append(samples, container.Sample{Data: f, Duration: 1024})
	}
	pcm := func(as []Audio) []byte {
		var b []byte
		for _, a := range as {
			b = append(b, a.Data...)
		}
		return b
	}
	whole, err := decodeAudio(tc, samples, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := pcm(whole); len(got) != len(frames)*1024*2*4 {
		t.Fatalf("no edit list: %d bytes for %d frames", len(got), len(frames))
	}
	tc.EditList = &container.EditList{MovieTimescale: 44100, Edits: []container.Edit{{SegmentDuration: 20000, MediaTime: 1024}}}
	var cut []Audio
	aacWithLibmSine(func() { cut, err = decodeAudio(tc, samples, AudioConfig{}) })
	if err != nil {
		t.Fatal(err)
	}
	got := pcm(cut)
	if len(got) != 20000*2*4 {
		t.Fatalf("with an edit list: %d bytes, want %d", len(got), 20000*2*4)
	}
	for i := 0; i < len(got)/4; i++ {
		// with the oracle platform's sine, as in TestAACOracle, the presented
		// samples are the reference's from sample 1024 on, bit for bit
		a := binary.LittleEndian.Uint32(got[i*4:])
		b := binary.LittleEndian.Uint32(ref[(1024*2+i)*4:])
		if a != b {
			t.Fatalf("sample %d: %v, reference %v", i, math.Float32frombits(a), math.Float32frombits(b))
		}
	}
	if cut[0].PTS != 0 {
		t.Errorf("first buffer starts at %d", cut[0].PTS)
	}
	// the decoder's own view: nothing to drop and no end of its own
	c, _ := newAAC(tc, AudioConfig{})
	if c.priming() != 0 || c.end(tc, samples) != -1 || c.sampleRate() != 44100 {
		t.Error("priming, end or rate")
	}
	if out, err := c.flush(); out != nil || err != nil {
		t.Errorf("flush: %v %v", out, err)
	}
}

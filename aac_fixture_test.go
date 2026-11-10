// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"os"
	"path/filepath"
	"testing"
)

// aacFixtures are AAC-LC streams made by ffmpeg's encoder from synthetic signals
// (see testdata/aac/README), as ADTS.
var aacFixtures = []struct {
	name     string
	channels int
	rate     int
}{
	{"st44.aac", 2, 44100},
	{"st44-tools.aac", 2, 44100},
	{"mono22.aac", 1, 22050},
	{"surround51.aac", 6, 48000},
	{"mono8.aac", 1, 8000},
}

// aacADTSFrames splits an ADTS file into its configuration and raw data blocks.
func aacADTSFrames(t testing.TB, path string) (aacConfig, [][]byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg aacConfig
	var frames [][]byte
	for len(b) > 0 {
		h, err := parseADTS(b)
		if err != nil {
			t.Fatalf("%s: frame %d: %v", path, len(frames), err)
		}
		if h.blocks != 1 || h.profile != aacObjLC-1 {
			t.Fatalf("%s: frame %d has %d blocks, profile %d", path, len(frames), h.blocks, h.profile)
		}
		cfg = aacConfig{rateIndex: h.rateIndex, rate: aacRates[h.rateIndex], config: h.channelConfig, channels: aacChannelConfig(h.channelConfig)}
		frames = append(frames, b[h.headerLength:h.frameLength])
		b = b[h.frameLength:]
	}
	return cfg, frames
}

// TestAACParseFixtures parses every frame of every fixture. A frame that parses
// wrongly does not end on its end element at its last byte, which an ADTS frame
// length states exactly, so this checks the Huffman decoding, the sections, the
// scalefactors and every side-information syntax element against a real encoder.
func TestAACParseFixtures(t *testing.T) {
	var tools [6]int // short windows, pulse, TNS, noise bands, intensity bands, M/S
	for _, fx := range aacFixtures {
		cfg, frames := aacADTSFrames(t, filepath.Join("testdata", "aac", fx.name))
		if cfg.channels != fx.channels || cfg.rate != fx.rate {
			t.Fatalf("%s: %d channels at %d Hz, want %d at %d", fx.name, cfg.channels, cfg.rate, fx.channels, fx.rate)
		}
		p, err := newAACParser(cfg, DefaultMaxAudioChannels)
		if err != nil {
			t.Fatal(err)
		}
		for i, f := range frames {
			els, err := p.parseFrame(f)
			if err != nil {
				t.Fatalf("%s frame %d: %v", fx.name, i, err)
			}
			if p.used != len(f) {
				t.Fatalf("%s frame %d: parsed %d bytes of %d", fx.name, i, p.used, len(f))
			}
			n := 0
			for _, e := range els {
				n += int(e.nch)
				for c := 0; c < int(e.nch); c++ {
					ch := &e.ch[c]
					if ch.ics.short() {
						tools[0]++
					}
					if ch.pulse.n > 0 {
						tools[1]++
					}
					if ch.tns.present {
						tools[2]++
					}
					for g := 0; g < int(ch.ics.numGroups); g++ {
						for sfb := 0; sfb < int(ch.ics.maxSfb); sfb++ {
							switch cb := ch.cb[g][sfb]; {
							case cb == aacBandNoise:
								tools[3]++
							case cb >= aacBandIntens:
								tools[4]++
							}
						}
					}
				}
				if e.msMask != 0 {
					tools[5]++
				}
			}
			if n != fx.channels {
				t.Fatalf("%s frame %d: %d channels in the elements, want %d", fx.name, i, n, fx.channels)
			}
		}
		t.Logf("%s: %d frames parsed to their last byte", fx.name, len(frames))
	}
	t.Logf("tools met: short windows %d, pulses %d, TNS %d, noise bands %d, intensity bands %d, M/S elements %d", tools[0], tools[1], tools[2], tools[3], tools[4], tools[5])
}

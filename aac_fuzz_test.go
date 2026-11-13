// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"
)

// checkAACElements walks what a parse returned: the sizes the syntax fixes, and a
// dequantised spectrum that is finite.
func checkAACElements(t *testing.T, els []aacElement, maxCh int) {
	t.Helper()
	n := 0
	var out [aacFrameLen]float32
	for i := range els {
		e := &els[i]
		n += int(e.nch)
		for c := 0; c < int(e.nch); c++ {
			ch := &e.ch[c]
			if ch.ics.numGroups < 1 || ch.ics.numGroups > aacMaxGroups || int(ch.ics.maxSfb) > aacMaxSfb {
				t.Fatalf("ics = %+v", ch.ics)
			}
			total := 0
			for g := 0; g < int(ch.ics.numGroups); g++ {
				total += int(ch.ics.groupLen[g])
			}
			if total != int(ch.ics.numWindows) {
				t.Fatalf("groups hold %d windows of %d", total, ch.ics.numWindows)
			}
			noise := uint32(aacNoiseSeed)
			ch.dequantise(&out, &noise)
			for _, v := range out {
				if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
					t.Fatalf("non finite coefficient %v", v)
				}
			}
		}
	}
	if n > maxCh {
		t.Fatalf("%d channels, the limit is %d", n, maxCh)
	}
}

// FuzzAACFrame feeds raw_data_block bytes to the parser: the first byte picks the
// sampling frequency, the second the channel limit.
func FuzzAACFrame(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 2 {
			return
		}
		cfg := aacConfig{rateIndex: int(data[0]) % len(aacRates)}
		cfg.rate = aacRates[cfg.rateIndex]
		maxCh := int(data[1])%8 + 1
		cfg.channels = 1
		p, err := newAACParser(cfg, maxCh)
		if err != nil {
			t.Fatal(err)
		}
		els, err := p.parseFrame(data[2:])
		if err != nil {
			return
		}
		checkAACElements(t, els, maxCh)
		used := p.used
		// a second parse of the same bytes, with the buffers reused, is the same parse
		els2, err := p.parseFrame(data[2:])
		if err != nil || p.used != used || len(els2) != len(els) {
			t.Fatalf("second parse: %v, used %d then %d", err, used, p.used)
		}
	})
}

// FuzzAACDecode feeds raw_data_block bytes to the whole decoder, parser and
// synthesis, twice in a row so that the state a frame leaves behind is read: the
// first byte picks the sampling frequency, the second the channel configuration.
func FuzzAACDecode(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 2 {
			return
		}
		tc := aacTrack(int(data[0])%len(aacRates), int(data[1])%7+1)
		c, err := newAAC(tc, AudioConfig{})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			out, err := c.decode(data[2:])
			if err != nil {
				return
			}
			// 1024 samples, or 2048 once a frame has carried SBR data
			if len(out) != 1 || (out[0].Samples() != aacSamplesPerFrame && out[0].Samples() != 2*aacSamplesPerFrame) || out[0].Channels != tc.Channels || out[0].Layout.Channels() != tc.Channels {
				t.Fatalf("output %+v", out)
			}
		}
	})
}

// FuzzAACHE decodes frames over an AudioSpecificConfig that signals SBR (and PS over
// a mono core): the first byte picks the core rate, the second mono or stereo, the
// third PS; the rest is a raw_data_block, decoded twice. Whatever it holds, the
// output is 2048 samples per channel at twice the rate or an error.
func FuzzAACHE(f *testing.F) {
	for _, fx := range aacHEFixtures {
		_, fr := aacADTSFrames(f, filepath.Join("testdata", "aac", fx.name))
		for _, frame := range fr[:3] {
			f.Add(append([]byte{byte(fx.rate), byte(aacChannelConfig(fx.config) - 1), 0}, frame...))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 3 {
			return
		}
		rate, ch := int(data[0])%len(aacRates), int(data[1])%2+1
		ext := -1
		for j, r := range aacRates {
			if r == 2*aacRates[rate] {
				ext = j
			}
		}
		if ext < 0 {
			return
		}
		tc := aacTrack(rate, ch)
		tc.CodecConfig = ascHE(data[2]&1 != 0 && ch == 1, rate, ch, ext, 2)
		c, err := newAAC(tc, AudioConfig{})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			out, err := c.decode(data[3:])
			if err != nil {
				return
			}
			if len(out) != 1 || out[0].Samples() != 2*aacSamplesPerFrame || out[0].SampleRate != 2*aacRates[rate] || out[0].Channels != ch {
				t.Fatalf("output %+v", out)
			}
		}
	})
}

// FuzzAACConfig feeds AudioSpecificConfig bytes to the parser.
func FuzzAACConfig(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := parseAudioSpecificConfig(data)
		if err != nil {
			return
		}
		if c.rateIndex < 0 || c.rateIndex >= len(aacRates) || c.rate != aacRates[c.rateIndex] || c.channels < 1 {
			t.Fatalf("config %+v", c)
		}
		if _, err := newAACParser(c, DefaultMaxAudioChannels); err != nil && c.channels <= DefaultMaxAudioChannels {
			t.Fatal(err)
		}
	})
}

// FuzzAACADTS feeds an ADTS header and what follows to the header parser, and the
// raw data block it frames to the frame parser.
func FuzzAACADTS(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		h, err := parseADTS(data)
		if err != nil {
			return
		}
		if h.frameLength < h.headerLength || h.frameLength > len(data) || h.rateIndex >= len(aacRates) || h.blocks < 1 || h.blocks > 4 {
			t.Fatalf("header %+v in %d bytes", h, len(data))
		}
		cfg := aacConfig{rateIndex: h.rateIndex, rate: aacRates[h.rateIndex], channels: max(aacChannelConfig(h.channelConfig), 1)}
		p, err := newAACParser(cfg, DefaultMaxAudioChannels)
		if err != nil {
			return
		}
		if els, err := p.parseFrame(data[h.headerLength:h.frameLength]); err == nil {
			checkAACElements(t, els, DefaultMaxAudioChannels)
		}
	})
}

func TestAACFuzzCorpusIsCommitted(t *testing.T) {
	for _, target := range []string{"FuzzAACFrame", "FuzzAACDecode", "FuzzAACConfig", "FuzzAACADTS"} {
		entries, err := os.ReadDir(filepath.Join("testdata", "fuzz", target))
		if err != nil || len(entries) < 4 {
			t.Errorf("%s: corpus of %d entries (%v), want the seeds of testdata/aac/README", target, len(entries), err)
		}
	}
}

// TestAACBomb: what a stream says never sizes an allocation. A frame of a million
// bytes that names channel elements one after another is refused at the first
// channel past the limit, with the memory limit (the equivalent of GOMEMLIMIT) at
// 64 MiB, and a program_config_element that lists ninety-three channels is refused
// when the parser is built.
func TestAACBomb(t *testing.T) {
	old := debug.SetMemoryLimit(64 << 20)
	defer debug.SetMemoryLimit(old)

	// the same empty element, over and over, up to a megabyte
	c := tChan{gain: 100}
	w := &bitWriter{}
	for w.nbit < 8<<20 {
		w.bits(aacIDSCE, 3)
		w.bits(0, 4)
		c.write(w, false)
	}
	p := newTestParser(t, aacRate44, 2)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := p.parseFrame(w.bytes())
	runtime.ReadMemStats(&after)
	wantErr(t, "a frame of elements", err, ErrOverLimit, "channels")
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Errorf("refusing the frame allocated %d bytes", grew)
	}

	pce := &bitWriter{}
	pce.bits(0, 4)
	pce.bits(1, 2)
	pce.bits(3, 4)
	pce.bits(15, 4) // front
	pce.bits(15, 4) // side
	pce.bits(15, 4) // back
	pce.bits(3, 2)  // lfe
	pce.bits(0, 3)
	pce.bits(0, 4)
	pce.bits(0, 3)
	for i := 0; i < 45; i++ {
		pce.bits(1, 1) // a pair
		pce.bits(uint(i%16), 4)
	}
	for i := 0; i < 3; i++ {
		pce.bits(uint(i), 4)
	}
	for pce.nbit%8 != 0 {
		pce.bits(0, 1)
	}
	pce.bits(0, 8)
	cfg, err := parseAudioSpecificConfig(asc(2, 3, 0, [3]uint{}, func(w *bitWriter) { w.buf = append(w.buf[:len(w.buf):len(w.buf)], pce.bytes()...) }))
	if err != nil || cfg.channels != 93 {
		t.Fatalf("a PCE of 93 channels was read as %d (%v)", cfg.channels, err)
	}
	_, err = newAACParser(cfg, DefaultMaxAudioChannels)
	wantErr(t, "a configuration of many channels", err, ErrOverLimit, "93 channels")
}

// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// crc8 is the CRC of a FLAC frame header (RFC 9639 9.1.8): polynomial 0x07.
func crc8(b []byte) byte {
	var c byte
	for _, x := range b {
		c ^= x
		for range 8 {
			if c&0x80 != 0 {
				c = c<<1 ^ 0x07
			} else {
				c <<= 1
			}
		}
	}
	return c
}

// crc16 is the CRC of a whole FLAC frame (RFC 9639 9.3): polynomial 0x8005.
func crc16(b []byte) uint16 {
	var c uint16
	for _, x := range b {
		c ^= uint16(x) << 8
		for range 8 {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x8005
			} else {
				c <<= 1
			}
		}
	}
	return c
}

// headerLen is the length of the frame header at the start of f.
func headerLen(f []byte) int {
	n := 5
	for lead := f[4]; lead&0x80 != 0; lead <<= 1 {
		n++
	}
	switch f[2] >> 4 {
	case 6:
		n++
	case 7:
		n += 2
	}
	switch f[2] & 15 {
	case 12:
		n++
	case 13, 14:
		n += 2
	}
	return n + 1
}

// reseal rewrites both checksums of a frame after its header was edited.
func reseal(f []byte) []byte {
	f = bytes.Clone(f)
	n := headerLen(f)
	f[n-1] = crc8(f[:n-1])
	c := crc16(f[:len(f)-2])
	f[len(f)-2], f[len(f)-1] = byte(c>>8), byte(c)
	return f
}

// flacConfigOf returns a track configuration for FLAC with the given STREAMINFO
// fields.
func flacConfigOf(rate, channels, depth, maxBlock int, marker bool) container.TrackConfig {
	body := make([]byte, 34)
	body[2], body[3] = byte(maxBlock>>8), byte(maxBlock)
	packed := uint64(rate)<<44 | uint64(channels-1)<<41 | uint64(depth-1)<<36
	for i := range 8 {
		body[10+i] = byte(packed >> (56 - 8*i))
	}
	cfg := append([]byte{0x80, 0, 0, 34}, body...)
	if marker {
		cfg = append([]byte("fLaC"), cfg...)
	}
	return container.TrackConfig{Codec: "fLaC", CodecConfig: cfg}
}

func TestFLACConfig(t *testing.T) {
	bad := func(tc container.TrackConfig, cfg AudioConfig, want error) {
		t.Helper()
		if _, err := NewAudioTrack(tc, cfg); !errors.Is(err, want) {
			t.Errorf("%v, want %v", err, want)
		}
	}
	good := flacConfigOf(44100, 2, 16, 4096, true)
	if d, err := NewAudioTrack(good, AudioConfig{}); err != nil || d == nil {
		t.Fatalf("a good configuration: %v", err)
	}
	// An MP4's dfLa box holds the blocks without the marker, and a maximum block
	// size of zero means it is not stated.
	if _, err := NewAudioTrack(flacConfigOf(44100, 2, 16, 0, false), AudioConfig{}); err != nil {
		t.Fatalf("no marker: %v", err)
	}
	bad(container.TrackConfig{Codec: "fLaC"}, AudioConfig{}, ErrAudioConfig)
	bad(container.TrackConfig{Codec: "fLaC", CodecConfig: good.CodecConfig[:30]}, AudioConfig{}, ErrAudioConfig)
	notInfo := flacConfigOf(44100, 2, 16, 4096, true)
	notInfo.CodecConfig[4] = 0x84 // a SEEKTABLE first
	bad(notInfo, AudioConfig{}, ErrAudioConfig)
	badSize := flacConfigOf(44100, 2, 16, 4096, true)
	badSize.CodecConfig[7] = 35
	bad(badSize, AudioConfig{}, ErrAudioConfig)
	bad(flacConfigOf(0, 2, 16, 4096, true), AudioConfig{}, ErrAudioConfig)
	bad(flacConfigOf(44100, 2, 3, 4096, true), AudioConfig{}, ErrAudioConfig)
	bad(flacConfigOf(44100, 2, 16, 8, true), AudioConfig{}, ErrAudioConfig)
	bad(flacConfigOf(44100, 2, 32, 4096, true), AudioConfig{}, ErrAudioUnsupported)
	bad(flacConfigOf(44100, 2, 25, 4096, true), AudioConfig{}, ErrAudioUnsupported)
	bad(good, AudioConfig{MaxChannels: 1}, ErrOverLimit)
}

// flacBits packs fields most significant bit first, as a FLAC frame does.
type flacBits struct {
	b []byte
	n int
}

func (w *flacBits) put(v uint64, bits int) {
	for i := bits - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>uint(i)&1 != 0 {
			w.b[len(w.b)-1] |= 0x80 >> (w.n % 8)
		}
		w.n++
	}
}

// TestFLACLPCOrderBeyondBlock: a frame of 16 samples whose subframe is a
// 32nd-order predictor has more warm-up samples than the block. The decoder
// refuses it, which is what keeps the interleaving, which indexes by the block
// size, inside its buffer.
func TestFLACLPCOrderBeyondBlock(t *testing.T) {
	w := &flacBits{}
	w.put(0, 1)    // padding
	w.put(0x3F, 6) // LPC, order 32
	w.put(0, 1)    // no wasted bits
	for range 32 {
		w.put(0, 16) // warm-up samples
	}
	w.put(3, 4) // coefficient precision 4
	w.put(0, 5) // shift 0
	for range 32 {
		w.put(0, 4) // coefficients
	}
	w.put(0, 2+4+4+100) // Rice residual, partition order 0
	f := append([]byte{0xFF, 0xF8, 0x60, 0x08, 0x00, 15, 0}, w.b...)
	f = reseal(append(f, 0, 0))
	d, err := NewAudioTrack(flacConfigOf(44100, 1, 16, 0, true), AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DecodeSample(container.Sample{Data: f}); !errors.Is(err, ErrAudioSample) {
		t.Fatalf("%v, want ErrAudioSample", err)
	}
}

// TestFLAC32BitIsRefused: the 32-bit fixture is refused by name, not mis-decoded.
func TestFLAC32BitIsRefused(t *testing.T) {
	r, id, _ := audioFixture(t, "flac_s32_stereo.flac")
	if _, err := DecodeAudioTrack(r, id, AudioConfig{}); !errors.Is(err, ErrAudioUnsupported) {
		t.Fatalf("%v, want ErrAudioUnsupported", err)
	}
}

func TestFLACFrameChecks(t *testing.T) {
	tc, samples, _ := flacTrack(t, "flac_s16_stereo.mka")
	mono, monoSamples, _ := flacTrack(t, "flac_s24_mono.flac")
	_ = mono
	frame := samples[0].Data
	decode := func(tc container.TrackConfig, cfg AudioConfig, data []byte) error {
		d, err := NewAudioTrack(tc, cfg)
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.DecodeSample(container.Sample{Data: data})
		return err
	}

	// A header that states no bit depth takes the one STREAMINFO states.
	d, _ := NewAudioTrack(tc, AudioConfig{})
	want, err := d.DecodeSample(container.Sample{Data: frame})
	if err != nil {
		t.Fatal(err)
	}
	zeroDepth := bytes.Clone(frame)
	zeroDepth[3] &^= 0x0E // sample size code 0: from STREAMINFO
	d, _ = NewAudioTrack(tc, AudioConfig{})
	got, err := d.DecodeSample(container.Sample{Data: reseal(zeroDepth)})
	if err != nil || !bytes.Equal(got[0].Data, want[0].Data) {
		t.Fatalf("a header without a bit depth: %v", err)
	}

	for name, c := range map[string]struct {
		data []byte
		cfg  AudioConfig
		want error
	}{
		"empty sample":     {nil, AudioConfig{}, io.ErrUnexpectedEOF},
		"no sync code":     {bytes.Repeat([]byte{0x55}, 40), AudioConfig{}, ErrAudioSample},
		"mono in stereo":   {monoSamples[0].Data, AudioConfig{}, ErrAudioSample},
		"block over limit": {frame, AudioConfig{MaxBlockSamples: 100}, ErrOverLimit},
		"corrupt body":     {func() []byte { b := bytes.Clone(frame); b[len(b)/2] ^= 0xFF; return b }(), AudioConfig{}, ErrAudioSample},
		"truncated body":   {frame[:len(frame)/2], AudioConfig{}, io.ErrUnexpectedEOF},
		"block of 65536":   {block65536(), AudioConfig{}, ErrAudioSample},
		"depth differs":    {depth24Stereo(t), AudioConfig{}, ErrAudioSample},
	} {
		if err := decode(tc, c.cfg, c.data); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

// header builds a frame header of the given channel code and block size field,
// sealed with its CRC-8, followed by body bytes.
func header(chanCode, bpsCode byte, sizeCode byte, ext []byte, body []byte) []byte {
	f := []byte{0xFF, 0xF8, sizeCode << 4, chanCode<<4 | bpsCode<<1, 0x00}
	f = append(f, ext...)
	f = append(f, 0)
	f = append(f, body...)
	return reseal(append(f, 0, 0))
}

func block65536() []byte { return header(1, 4, 7, []byte{0xFF, 0xFF}, nil) }

// depth24Stereo is a stereo frame header that states 24 bits in a 16-bit stream.
func depth24Stereo(t *testing.T) []byte { return header(1, 6, 8, nil, nil) }

// TestFLACBlockBomb: a frame header of a few bytes that states a block of 65535
// samples in eight channels at 24 bits, the 2 MiB the subframes would take, is
// refused by a decoder limited to 4608 samples before they are allocated, and the
// heap does not grow. It runs under a memory limit.
func TestFLACBlockBomb(t *testing.T) {
	old := debug.SetMemoryLimit(128 << 20)
	defer debug.SetMemoryLimit(old)
	tc := flacConfigOf(48000, 8, 24, 0, true)
	bomb := header(7, 6, 7, []byte{0xFF, 0xFE}, nil)
	for range 100 {
		d, err := NewAudioTrack(tc, AudioConfig{MaxBlockSamples: 4608})
		if err != nil {
			t.Fatal(err)
		}
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err = d.DecodeSample(container.Sample{Data: bomb})
		runtime.ReadMemStats(&after)
		if !errors.Is(err, ErrOverLimit) {
			t.Fatalf("%v, want ErrOverLimit", err)
		}
		if grown := int64(after.TotalAlloc) - int64(before.TotalAlloc); grown > 1<<20 {
			t.Fatalf("a refused frame allocated %d bytes", grown)
		}
	}
}

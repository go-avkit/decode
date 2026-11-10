// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// MP3 is a float decoder (and ffmpeg's is not minimp3), so the PCM is compared with
// ffmpeg's f32le within a stated tolerance, and the sample count, which the LAME
// tag's priming and padding decide, exactly. The largest error and the SNR
// measured are logged.
const (
	mp3MaxErr = 1e-5 // absolute, on samples nominally in [-1, 1]
	mp3MinSNR = 100  // dB
)

func checkMP3(t *testing.T, got, want []byte, channels int) {
	t.Helper()
	maxErr, snr := errStats(t, got, want)
	t.Logf("%d samples, max error %.3g, SNR %.1f dB", len(got)/4/channels, maxErr, snr)
	if maxErr > mp3MaxErr || snr < mp3MinSNR {
		t.Fatalf("max error %.3g (limit %g), SNR %.1f dB (limit %d)", maxErr, mp3MaxErr, snr, mp3MinSNR)
	}
}

func TestMP3MatchesFFmpeg(t *testing.T) {
	for _, c := range []struct {
		name     string
		rate, ch int
	}{
		{"mp3_stereo_44k.mp3", 44100, 2},
		{"mp3_stereo_48k_192k.mp3", 48000, 2},
		{"mp3_stereo_32k_joint.mp3", 32000, 2},
		{"mp3_stereo_indep.mp3", 44100, 2},
		{"mp3_mono_22k.mp3", 22050, 1},
		{"mp3_stereo_vbr.mp3", 44100, 2},
		{"mp3_stereo_noxing.mp3", 44100, 2},
		{"mp3_stereo.mp4", 44100, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, id, want := audioFixture(t, c.name)
			as, err := DecodeAudioTrack(r, id, AudioConfig{})
			if err != nil {
				t.Fatal(err)
			}
			layout := LayoutStereo
			if c.ch == 1 {
				layout = LayoutMono
			}
			for _, a := range as {
				if a.SampleRate != c.rate || a.Channels != c.ch || a.Format != SampleF32 || a.Layout != layout {
					t.Fatalf("%d Hz, %d ch, %v, layout %#x", a.SampleRate, a.Channels, a.Format, uint32(a.Layout))
				}
			}
			got := pcmBytes(t, as)
			if len(got) != len(want) {
				t.Fatalf("%d samples, ffmpeg decodes %d", len(got)/4/c.ch, len(want)/4/c.ch)
			}
			checkMP3(t, got, want, c.ch)
		})
	}
}

// TestMP3InMatroska: a Matroska track states the encoder delay as CodecDelay, to
// the nanosecond, and its end only as the milliseconds of its samples' durations,
// so the start is exact and the end is not: the track is as long as ffmpeg's to
// within one millisecond (44 samples at 44.1 kHz), measured at 2 samples here, and
// the PCM that is there matches.
func TestMP3InMatroska(t *testing.T) {
	r, id, want := audioFixture(t, "mp3_stereo.mka")
	as, err := DecodeAudioTrack(r, id, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	got := pcmBytes(t, as)
	diff := (len(want) - len(got)) / 8
	t.Logf("%d samples against ffmpeg's %d", len(got)/8, len(want)/8)
	if diff < -44 || diff > 44 {
		t.Fatalf("%d samples against ffmpeg's %d", len(got)/8, len(want)/8)
	}
	n := min(len(got), len(want))
	checkMP3(t, got[:n], want[:n], 2)
}

// mp3Frames cuts a Layer III stream into frames by their headers (ISO/IEC 11172-3
// 2.4.1.3, 13818-3), dropping the Xing or Info frame a LAME stream opens with.
func mp3Frames(t *testing.T, b []byte) (frames [][]byte, rate int) {
	t.Helper()
	rates := [][3]int{3: {44100, 48000, 32000}, 2: {22050, 24000, 16000}, 0: {11025, 12000, 8000}}
	kbps := [][15]int{
		3: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},
		2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
	}
	if bytes.HasPrefix(b, []byte("ID3")) { // an ID3v2 tag, its size four 7-bit bytes
		b = b[10+int(b[6])<<21+int(b[7])<<14+int(b[8])<<7+int(b[9]):]
	}
	for len(b) >= 4 {
		if b[0] != 0xFF || b[1]&0xE0 != 0xE0 || b[1]>>1&3 != 1 {
			t.Fatalf("no Layer III header at %x", b[:4])
		}
		v := int(b[1] >> 3 & 3)
		rate = rates[v][b[2]>>2&3]
		table := kbps[max(v, 2)]
		coef := 72
		if v == 3 {
			coef = 144
		}
		n := coef*table[b[2]>>4]*1000/rate + int(b[2]>>1&1)
		if n > len(b) {
			break
		}
		if !bytes.Contains(b[:min(n, 64)], []byte("Info")) && !bytes.Contains(b[:min(n, 64)], []byte("Xing")) {
			frames = append(frames, b[:n])
		}
		b = b[n:]
	}
	return frames, rate
}

// TestMP3Mpeg25: avkit's reader does not read MPEG 2.5 (11.025 kHz) files, so the
// frames are cut here and the decoder is given a track by hand. The 1105 samples
// the LAME tag says to drop (576 of encoder delay and 529 of decoder delay) are
// dropped by the test, as the tag would drive the edit list.
func TestMP3Mpeg25(t *testing.T) {
	file := readFile(t, "testdata/audio/mp3_mono_11k.mp3")
	want := readFile(t, "testdata/audio/mp3_mono_11k.mp3.raw")
	frames, rate := mp3Frames(t, file)
	d, err := NewAudioTrack(container.TrackConfig{Codec: "mp3", SampleRate: rate, Channels: 1, Timescale: uint32(rate)}, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var all []Audio
	for _, f := range frames {
		as, err := d.DecodeSample(container.Sample{Data: f})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, as...)
	}
	got := pcmBytes(t, all)
	if len(got) < 1105*4+len(want) {
		t.Fatalf("%d samples decoded, %d needed", len(got)/4, 1105+len(want)/4)
	}
	if rate != 11025 || all[0].Layout != LayoutMono {
		t.Fatalf("%d Hz, layout %#x", rate, uint32(all[0].Layout))
	}
	checkMP3(t, got[1105*4:1105*4+len(want)], want, 1)
}

// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"testing"
)

// TestVorbisMatchesFFmpeg: Vorbis is a float decoder, so the PCM is compared with
// ffmpeg's f32le within a stated tolerance, and the sample count, which trimming
// decides, exactly. The largest error and the SNR measured are logged.
const (
	vorbisMaxErr = 1e-5 // absolute, on samples nominally in [-1, 1]
	vorbisMinSNR = 100  // dB
)

func TestVorbisMatchesFFmpeg(t *testing.T) {
	for _, c := range []struct {
		name string
		rate int
	}{
		{"vorbis_stereo.ogg", 44100},
		{"vorbis_stereo_22k.ogg", 22050},
		{"vorbis_stereo_192k.ogg", 48000},
		{"vorbis_stereo_32k.ogg", 32000},
		{"vorbis_stereo.mka", 44100},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, id, want := audioFixture(t, c.name)
			as, err := DecodeAudioTrack(r, id, AudioConfig{})
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range as {
				if a.SampleRate != c.rate || a.Channels != 2 || a.Format != SampleF32 || a.Layout != LayoutStereo {
					t.Fatalf("%d Hz, %d ch, %v, layout %#x", a.SampleRate, a.Channels, a.Format, uint32(a.Layout))
				}
			}
			got := pcmBytes(t, as)
			if len(got) != len(want) {
				t.Fatalf("%d samples, ffmpeg decodes %d", len(got)/8, len(want)/8)
			}
			maxErr, snr := errStats(t, got, want)
			t.Logf("%d samples, max error %.3g, SNR %.1f dB", len(got)/8, maxErr, snr)
			if maxErr > vorbisMaxErr || snr < vorbisMinSNR {
				t.Fatalf("max error %.3g (limit %g), SNR %.1f dB (limit %d)", maxErr, vorbisMaxErr, snr, vorbisMinSNR)
			}
		})
	}
}

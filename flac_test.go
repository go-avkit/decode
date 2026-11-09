// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"testing"
)

// TestFLACMatchesFFmpeg: FLAC is lossless, so every fixture must decode to the very
// bytes ffmpeg decodes it to, in the sample format ffmpeg's decoder outputs.
func TestFLACMatchesFFmpeg(t *testing.T) {
	for _, c := range []struct {
		name     string
		rate, ch int
		format   SampleFormat
		layout   ChannelLayout
	}{
		{"flac_s16_stereo.flac", 44100, 2, SampleS16, LayoutStereo},
		{"flac_s24_mono.flac", 48000, 1, SampleS32, LayoutMono},
		{"flac_s16_51.flac", 32000, 6, SampleS16, Layout51},
		{"flac_s16_fixed.flac", 22050, 2, SampleS16, LayoutStereo},
		{"flac_s16_best.flac", 44100, 2, SampleS16, LayoutStereo},
		{"flac_s16_indep.flac", 44100, 2, SampleS16, LayoutStereo},
		{"flac_s16_leftside.flac", 44100, 2, SampleS16, LayoutStereo},
		{"flac_s16_rightside.flac", 44100, 2, SampleS16, LayoutStereo},
		{"flac_s16_midside.flac", 44100, 2, SampleS16, LayoutStereo},
		{"flac_s16_stereo.mka", 44100, 2, SampleS16, LayoutStereo},
		{"flac_s16_stereo.mp4", 44100, 2, SampleS16, LayoutStereo},
		{"flac_s16_8ch.flac", 48000, 8, SampleS16, Layout71},
		{"flac_s16_voice8k.flac", 8000, 1, SampleS16, LayoutMono},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, id, want := audioFixture(t, c.name)
			as, err := DecodeAudioTrack(r, id, AudioConfig{})
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range as {
				if a.SampleRate != c.rate || a.Channels != c.ch || a.Format != c.format || a.Layout != c.layout {
					t.Fatalf("%d Hz, %d ch, %v, layout %#x", a.SampleRate, a.Channels, a.Format, uint32(a.Layout))
				}
			}
			if got := pcmBytes(t, as); !bytes.Equal(got, want) {
				t.Fatalf("%d bytes differ from ffmpeg's %d", len(got), len(want))
			}
		})
	}
}

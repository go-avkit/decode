// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"path/filepath"
	"testing"
)

// The conformance gate of Opus is opus_conformance_test.go (the official vectors and
// opus_compare, RFC 6716 section 6). This test is information and a regression
// check on the container handling: the PCM of ffmpeg-made fixtures is compared with
// ffmpeg's native Opus decoder and with libopus (through ffmpeg), and the largest
// error and SNR are LOGGED, not gated. The sample count, which pre-skip and the end
// trimming decide, is checked exactly where the container states the end to the
// sample. Measured: CELT streams agree with libopus to 2.2e-5 (90 dB); SILK and
// hybrid streams to 1.4e-2 (40.9 dB) at worst, libopus decoding SILK in fixed point;
// ffmpeg's native decoder resamples SILK with a filter of its own and is 15 to 22 dB
// from libopus there.

func TestOpusMatchesFFmpeg(t *testing.T) {
	for _, c := range []struct {
		name string
		ch   int
		// count is how the sample count is checked: 0 equal to ffmpeg's (Ogg
		// states the end to the sample), the samples of the edit list for MP4 (see
		// TestOpusInMP4), or a tolerance in samples for Matroska (see
		// TestOpusInMatroska).
		count int
	}{
		{"opus_music_stereo.ogg", 2, 0},
		{"opus_music_mono_24k.ogg", 1, 0},
		{"opus_silk_voip.ogg", 1, 0},
		{"opus_hybrid_voip.ogg", 1, 0},
		{"opus_silk_stereo_60ms.ogg", 2, 0},
		{"opus_celt_2_5ms.ogg", 2, 0},
		{"opus_celt_10ms.ogg", 2, 0},
		{"opus_stereo_cbr.ogg", 2, 0},
		{"opus_stereo.mka", 2, 96},
		{"opus_stereo.mp4", 2, -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, id, native := audioFixture(t, c.name)
			libopus := readFile(t, filepath.Join("testdata", "audio", c.name+".libopus.raw"))
			tc, err := r.TrackConfig(id)
			if err != nil {
				t.Fatal(err)
			}
			samples, err := r.Samples(id)
			if err != nil {
				t.Fatal(err)
			}
			as, err := decodeAudio(tc, samples, AudioConfig{})
			if err != nil {
				t.Fatal(err)
			}
			layout := LayoutStereo
			if c.ch == 1 {
				layout = LayoutMono
			}
			for _, a := range as {
				if a.SampleRate != 48000 || a.Channels != c.ch || a.Format != SampleF32 || a.Layout != layout {
					t.Fatalf("%d Hz, %d ch, %v, layout %#x", a.SampleRate, a.Channels, a.Format, uint32(a.Layout))
				}
			}
			got := pcmBytes(t, as)
			frame := 4 * c.ch
			n := min(len(got), len(libopus))
			switch {
			case c.count == 0 && len(got) != len(libopus):
				t.Fatalf("%d samples, libopus decodes %d (ffmpeg's native decoder %d)", len(got)/frame, len(libopus)/frame, len(native)/frame)
			case c.count > 0 && (len(got)-len(libopus) > c.count*frame || len(libopus)-len(got) > c.count*frame):
				t.Fatalf("%d samples, libopus decodes %d, the allowance is %d", len(got)/frame, len(libopus)/frame, c.count)
			case c.count < 0:
				// The edit list states 19200 samples; ffmpeg delivers every packet.
				if len(got)/frame != 19200 {
					t.Fatalf("%d samples, the edit list states 19200", len(got)/frame)
				}
			}
			maxErr, snr := errStats(t, got[:n], libopus[:n])
			t.Logf("%d samples (libopus %d); against libopus max error %.3g, SNR %.1f dB", len(got)/frame, len(libopus)/frame, maxErr, snr)
			if len(native) >= n {
				nMax, nSNR := errStats(t, got[:n], native[:n])
				t.Logf("against ffmpeg's native decoder max error %.3g, SNR %.1f dB", nMax, nSNR)
			}
		})
	}
}

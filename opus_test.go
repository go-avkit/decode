// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"path/filepath"
	"testing"
)

// Opus is a float decoder, so the PCM is compared with ffmpeg's f32le within a stated
// tolerance, and the sample count, which pre-skip and the end trimming decide,
// exactly where the container states the end to the sample. There are two oracles,
// both ffmpeg: its native decoder and libopus, the reference implementation, which
// ffmpeg can decode with. The specification leaves the resampling of SILK open and
// ffmpeg's native decoder does it with a filter of its own, so on SILK and hybrid
// packets only libopus is a sample-for-sample oracle (the native decoder is 15 to
// 22 dB from it and from this one); on CELT packets all three agree.
//
// The tolerances were chosen before the fixtures were decoded and stated by mode:
//   - CELT-only streams: 1e-4 absolute, 80 dB. Measured 2.2e-5 and 90 dB at worst.
//   - streams with SILK or hybrid packets: 2e-2 absolute, 40 dB. libopus decodes
//     SILK in fixed point, this decoder in float, so this is rounding noise rather
//     than float precision. Measured 1.4e-2 and 40.9 dB at worst (60 ms stereo
//     frames), 5.7e-3 and 51 dB on 20 ms speech.
const (
	opusCeltMaxErr = 1e-4
	opusCeltMinSNR = 80
	opusSilkMaxErr = 2e-2
	opusSilkMinSNR = 40
)

// opusHasSilk reports whether any packet is SILK or hybrid (RFC 6716 3.1: the
// configuration, the five top bits of the TOC byte, is below 16).
func opusHasSilk(smp [][]byte) bool {
	for _, p := range smp {
		if len(p) > 0 && p[0]>>3 < 16 {
			return true
		}
	}
	return false
}

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
			var packets [][]byte
			for _, s := range samples {
				packets = append(packets, s.Data)
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
			limitErr, limitSNR := opusCeltMaxErr, float64(opusCeltMinSNR)
			if opusHasSilk(packets) {
				limitErr, limitSNR = opusSilkMaxErr, float64(opusSilkMinSNR)
			}
			if maxErr > limitErr || snr < limitSNR {
				t.Fatalf("max error %.3g (limit %g), SNR %.1f dB (limit %g)", maxErr, limitErr, snr, limitSNR)
			}
		})
	}
}

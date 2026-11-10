// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// The audio fuzz targets: a whole file through DecodeAudioTrack, one coded sample
// of each codec, and each codec's configuration record. The corpus in
// testdata/fuzz is written by `audiofix seeds` from the committed fixtures. The
// limits of audioFuzzConfig are the bounds under test: whatever a mutated stream
// states, no buffer may exceed them.
var audioFuzzConfig = AudioConfig{MaxChannels: 8, MaxBlockSamples: 8192, MaxSampleBytes: 1 << 20, MaxOutputBytes: 8 << 20}

// checkAudio holds every buffer a decoder returns to its contract: contiguous
// presentation times, whole samples, and sizes within the configured bounds.
func checkAudio(t *testing.T, cfg AudioConfig, sets ...[]Audio) {
	t.Helper()
	var total int
	for _, as := range sets {
		for _, a := range as {
			size := a.Format.Size()
			if size == 0 || a.Channels < 1 || a.Channels > cfg.channels() || a.SampleRate <= 0 {
				t.Fatalf("buffer of %d channels at %d Hz in %v", a.Channels, a.SampleRate, a.Format)
			}
			if len(a.Data) == 0 || len(a.Data)%(size*a.Channels) != 0 {
				t.Fatalf("%d bytes is not whole samples of %d channels in %v", len(a.Data), a.Channels, a.Format)
			}
			if a.Samples() > cfg.block()*2 {
				t.Fatalf("a buffer of %d samples breaks the %d block bound", a.Samples(), cfg.block())
			}
			if a.Layout != 0 && a.Layout.Channels() != a.Channels {
				t.Fatalf("layout %#x names %d channels of %d", uint32(a.Layout), a.Layout.Channels(), a.Channels)
			}
			total += len(a.Data)
		}
	}
	if total > 4*cfg.output() {
		t.Fatalf("%d bytes of PCM from one sample", total)
	}
}

func FuzzAudioTrack(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := container.NewReader(data)
		if err != nil {
			return
		}
		for _, tr := range r.File().Tracks {
			if tr.Kind != container.Audio {
				continue
			}
			as, _ := DecodeAudioTrack(r, tr.ID, audioFuzzConfig)
			var pos int64
			var bytes int
			for _, a := range as {
				if a.PTS != pos {
					t.Fatalf("buffer at %d, want %d", a.PTS, pos)
				}
				pos += int64(a.Samples())
				bytes += len(a.Data)
			}
			if bytes > audioFuzzConfig.output() {
				t.Fatalf("%d bytes of PCM, the bound is %d", bytes, audioFuzzConfig.output())
			}
			checkAudio(t, audioFuzzConfig, as)
		}
	})
}

// fuzzFLACConfig is the track every FuzzFLACSample sample is decoded as.
var fuzzFLACConfig = flacConfigOf(44100, 2, 16, 0, true)

func FuzzFLACSample(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := NewAudioTrack(fuzzFLACConfig, audioFuzzConfig)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		first, _ := d.DecodeSample(container.Sample{Data: data})
		second, _ := d.DecodeSample(container.Sample{Data: data})
		last, _ := d.Flush()
		checkAudio(t, audioFuzzConfig, first, second, last)
	})
}

func FuzzFLACConfig(f *testing.F) {
	f.Fuzz(func(t *testing.T, conf []byte) {
		d, err := NewAudioTrack(container.TrackConfig{Codec: "fLaC", CodecConfig: conf}, audioFuzzConfig)
		if err == nil {
			d.Close()
		}
	})
}

func FuzzMP3Sample(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := NewAudioTrack(container.TrackConfig{Codec: "mp3", SampleRate: 44100, Channels: 2, Timescale: 44100}, audioFuzzConfig)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		first, _ := d.DecodeSample(container.Sample{Data: data})
		second, _ := d.DecodeSample(container.Sample{Data: data})
		last, _ := d.Flush()
		checkAudio(t, audioFuzzConfig, first, second, last)
	})
}

func TestAudioFuzzCorpusIsCommitted(t *testing.T) {
	for _, target := range []string{"FuzzAudioTrack", "FuzzFLACSample", "FuzzFLACConfig", "FuzzMP3Sample"} {
		entries, err := os.ReadDir(filepath.Join("testdata", "fuzz", target))
		if err != nil || len(entries) < 4 {
			t.Errorf("%s: corpus of %d entries (%v), want the seeds from `audiofix seeds`", target, len(entries), err)
		}
	}
}

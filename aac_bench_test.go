// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// BenchmarkAACDecode decodes a pinned conformance stream (stereo, 48 kHz, about 7.4
// s of audio) through the track path, allocation of every output buffer included.
func BenchmarkAACDecode(b *testing.B) {
	dir := os.Getenv("AAC_CONFORMANCE_DIR")
	if dir == "" {
		b.Skip("AAC_CONFORMANCE_DIR is not set")
	}
	name := "al05_48"
	if n := os.Getenv("AAC_BENCH_STREAM"); n != "" {
		name = n // another pinned stream, for a longer run
	}
	data, err := os.ReadFile(filepath.Join(dir, "compressedMp4", name+".mp4"))
	if err != nil {
		b.Fatal(err)
	}
	r, err := container.NewReader(data)
	if err != nil {
		b.Fatal(err)
	}
	id, _ := FirstAudioTrack(r)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DecodeAudioTrack(r, id, AudioConfig{}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAACDecodeReuse is the decoder's own work on the file AAC_BENCH_FILE (an
// MP4 with an AAC-LC track): every frame through decodeInto, one output buffer for
// all. It is what to put beside ffmpeg's `-benchmark` utime on the same file.
func BenchmarkAACDecodeReuse(b *testing.B) {
	path := os.Getenv("AAC_BENCH_FILE")
	if path == "" {
		b.Skip("AAC_BENCH_FILE is not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	r, err := container.NewReader(data)
	if err != nil {
		b.Fatal(err)
	}
	id, _ := FirstAudioTrack(r)
	tc, err := r.TrackConfig(id)
	if err != nil {
		b.Fatal(err)
	}
	samples, err := r.Samples(id)
	if err != nil {
		b.Fatal(err)
	}
	c, err := newAAC(tc, AudioConfig{})
	if err != nil {
		b.Fatal(err)
	}
	d := c.(*aacDecoder)
	dst := make([]byte, aacSamplesPerFrame*d.cfg.channels*4)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, s := range samples {
			if err := d.decodeInto(s.Data, dst); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(samples)), "ns/frame")
}

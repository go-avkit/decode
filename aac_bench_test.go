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
// s of audio) through the track path, for the comparison with ffmpeg in the README.
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

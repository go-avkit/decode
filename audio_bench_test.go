// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"os"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// BenchmarkAudioTrack decodes the file named by DECODE_BENCH_FILE (a long .mp3 or
// .flac, not committed) once per iteration, for the comparison with ffmpeg in
// decode#2. It is skipped when the variable is unset.
func BenchmarkAudioTrack(b *testing.B) {
	path := os.Getenv("DECODE_BENCH_FILE")
	if path == "" {
		b.Skip("DECODE_BENCH_FILE is not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	r, err := container.NewReader(data)
	if err != nil {
		b.Fatal(err)
	}
	id, err := FirstAudioTrack(r)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	var bytes int
	for range b.N {
		as, err := DecodeAudioTrack(r, id, AudioConfig{MaxOutputBytes: 1 << 30})
		if err != nil {
			b.Fatal(err)
		}
		bytes = 0
		for _, a := range as {
			bytes += len(a.Data)
		}
	}
	b.SetBytes(int64(bytes))
}

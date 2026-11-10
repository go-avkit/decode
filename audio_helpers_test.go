// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// audioFixture opens testdata/audio/name and returns its reader, the ID of its
// audio track and the PCM ffmpeg decoded it to (tools/audiofix wrote it).
func audioFixture(t testing.TB, name string) (*container.Reader, uint32, []byte) {
	t.Helper()
	path := filepath.Join("testdata", "audio", name)
	r, err := container.NewReader(readFile(t, path))
	if err != nil {
		t.Fatal(err)
	}
	id, err := FirstAudioTrack(r)
	if err != nil {
		t.Fatal(err)
	}
	return r, id, readFile(t, path+".raw")
}

// pcmBytes joins the buffers, and checks that they follow one another.
func pcmBytes(t testing.TB, as []Audio) []byte {
	t.Helper()
	var out []byte
	var pos int64
	for i, a := range as {
		if a.PTS != pos {
			t.Fatalf("buffer %d starts at %d, want %d", i, a.PTS, pos)
		}
		pos += int64(a.Samples())
		out = append(out, a.Data...)
	}
	return out
}

// errStats compares two buffers of float samples and returns the largest absolute
// difference and the signal to noise ratio of got against want, in dB (+Inf for
// equal buffers).
func errStats(t testing.TB, got, want []byte) (maxErr, snr float64) {
	t.Helper()
	if len(got) != len(want) || len(got)%4 != 0 {
		t.Fatalf("%d bytes against %d", len(got), len(want))
	}
	var sig, noise float64
	for i := 0; i < len(got); i += 4 {
		g := float64(math.Float32frombits(binary.LittleEndian.Uint32(got[i:])))
		w := float64(math.Float32frombits(binary.LittleEndian.Uint32(want[i:])))
		d := g - w
		maxErr = max(maxErr, math.Abs(d))
		sig += w * w
		noise += d * d
	}
	if noise == 0 {
		return 0, math.Inf(1)
	}
	return maxErr, 10 * math.Log10(sig/noise)
}

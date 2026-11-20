// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"runtime"
	"runtime/debug"
	"testing"
)

// TestHEVCSizeBomb: a stream of a few dozen bytes whose sequence parameter set
// declares a picture of 8192x4320 luma samples, which a decoder that trusted it
// would answer with some hundred megabytes of planes. It must be refused, and the
// heap must not grow by anything near that. The test runs under a memory limit
// (the equivalent of GOMEMLIMIT) so that a decoder that allocated would be slowed
// to a crawl or killed rather than take the host's memory.
func TestHEVCSizeBomb(t *testing.T) {
	old := debug.SetMemoryLimit(256 << 20)
	defer debug.SetMemoryLimit(old)

	for _, size := range [][2]uint64{{8192, 4320}, {16384, 16384}, {1 << 20, 1 << 20}} {
		d, err := NewHEVC(Config{}, 4)
		if err != nil {
			t.Fatal(err)
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		var refused error
		for _, n := range [][]byte{bigSPS(size[0], size[1]), bigPPS(), bigIDR()} {
			if _, err := d.DecodeSample(prefixed([][]byte{n}, 4), 0); err != nil {
				refused = err
			}
		}
		runtime.ReadMemStats(&after)
		d.Close()
		if !errors.Is(refused, ErrUnsupported) && !errors.Is(refused, ErrBitstream) {
			t.Fatalf("%dx%d: %v, want a refusal", size[0], size[1], refused)
		}
		if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 32<<20 {
			t.Fatalf("%dx%d: the heap grew by %d bytes", size[0], size[1], grown)
		}
	}
}

// TestHEVCWideFormatBomb: the formats a Frame carries since the chroma design weigh up to six
// bytes a luma sample (4:4:4 at 16 bits), and the picture-size limit counts macroblocks, not
// bytes. With that limit raised out of the way, a stream declaring 20000x20000 samples of 4:4:4
// at 16 bits (2.4 GB of planes) is refused for its BYTES, as ErrOverLimit, before anything is
// allocated, under a memory limit (the equivalent of GOMEMLIMIT; CI also runs the suite under
// ulimit -v on the 386 lane) so that a decoder that allocated would be slowed to a crawl rather
// than take the host's memory.
func TestHEVCWideFormatBomb(t *testing.T) {
	old := debug.SetMemoryLimit(256 << 20)
	defer debug.SetMemoryLimit(old)

	for _, tc := range []struct {
		chroma, depth, depthC uint64
	}{{3, 16, 16}, {3, 16, 12}, {2, 16, 16}} {
		d, err := NewHEVC(Config{MaxFrameMBs: 1 << 30}, 4)
		if err != nil {
			t.Fatal(err)
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		var refused error
		// 4:2:2 at 16 bits is four bytes a sample: 1.6 GB at 20000x20000 fits under the cap, so
		// it needs the larger picture of 26000x26000 (2.7 GB)
		side := uint64(20000)
		if tc.chroma == 2 {
			side = 26000
		}
		for _, n := range [][]byte{formatSPS(side, side, tc.chroma, tc.depth, tc.depthC, 0), bigPPS(), bigIDR()} {
			// the first refusal is the one that counts: the PPS and the slice that follow a
			// refused SPS are refused for want of one
			if _, err := d.DecodeSample(prefixed([][]byte{n}, 4), 0); err != nil && refused == nil {
				refused = err
			}
		}
		runtime.ReadMemStats(&after)
		d.Close()
		if !errors.Is(refused, ErrOverLimit) {
			t.Fatalf("chroma %d, %d/%d bits: %v, want ErrOverLimit", tc.chroma, tc.depth, tc.depthC, refused)
		}
		if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 32<<20 {
			t.Fatalf("chroma %d, %d/%d bits: the heap grew by %d bytes", tc.chroma, tc.depth, tc.depthC, grown)
		}
	}
}

// TestHEVCSizeBombIsTheLimit pins down which refusal the 8192x4320 stream gets:
// the picture size limit, not a parse failure of the made-up parameter sets.
func TestHEVCSizeBombIsTheLimit(t *testing.T) {
	d, _ := NewHEVC(Config{}, 4)
	defer d.Close()
	var err error
	for _, n := range [][]byte{bigSPS(8192, 4320), bigPPS(), bigIDR()} {
		_, err = d.DecodeSample(prefixed([][]byte{n}, 4), 0)
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("%v, want the size limit", err)
	}
	// With the limit raised the same stream is no longer refused for its size.
	big, _ := NewHEVC(Config{MaxFrameMBs: 1 << 20}, 4)
	defer big.Close()
	for _, n := range [][]byte{bigSPS(1024, 768), bigPPS(), bigIDR()} {
		_, err = big.DecodeSample(prefixed([][]byte{n}, 4), 0)
	}
	if errors.Is(err, ErrUnsupported) {
		t.Fatalf("a 1024x768 picture was refused as too big: %v", err)
	}
}

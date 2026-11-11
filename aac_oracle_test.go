// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// The oracle is ffmpeg 9.0.2 (Homebrew, arm64, Apple clang 21) with -cpuflags 0, its
// C code paths, decoding the ADTS fixtures to f32le: testdata/aac/ref. The target
// is exact float32 equality, sample by sample. The reference of surround51.aac holds
// its first 12 frames.

// aacOracleRun decodes a fixture and compares it with its reference. It returns
// the number of samples compared, how many differ, the largest absolute difference,
// and how many of the differing samples are in a frame and channel that is not
// overlapped under a sine window (those would be a defect of the decoder).
func aacOracleRun(t *testing.T, name string) (total, differ int, maxAbs float64, notSine int) {
	t.Helper()
	cfg, frames := aacADTSFrames(t, filepath.Join("testdata", "aac", name+".aac"))
	refDir := filepath.Join("testdata", "aac", "ref")
	if d := os.Getenv("AAC_REF_DIR"); d != "" {
		refDir = d // another platform's ffmpeg, see TestAACOraclePlatform
	}
	ref, err := os.ReadFile(filepath.Join(refDir, name+".f32"))
	if err != nil {
		t.Fatal(err)
	}
	asc := &bitWriter{}
	asc.bits(2, 5)
	asc.bits(uint(cfg.rateIndex), 4)
	asc.bits(uint(cfg.config), 4)
	asc.bits(0, 3)
	c, err := newAAC(container.TrackConfig{CodecConfig: asc.bytes()}, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	d := c.(*aacDecoder)
	nch := cfg.channels
	for fi := 0; (fi+1)*aacSamplesPerFrame*nch*4 <= len(ref); fi++ {
		// what each channel's overlap used, before the frame changes it
		prevShape := make([]uint8, nch)
		for i := range prevShape {
			prevShape[i] = d.synth.chans[i].prevShape
		}
		out, err := d.decode(frames[fi])
		if err != nil {
			t.Fatalf("%s frame %d: %v", name, fi, err)
		}
		sine := make([]bool, nch) // by interleaved slot
		first := 0
		for e := range d.elems {
			el := &d.parser.elems[e]
			for ch := 0; ch < int(el.nch); ch++ {
				ics := &el.ch[ch].ics
				sine[d.slot[e][ch]] = prevShape[first+ch] == 0 || (ics.short() && ics.windowShape == 0)
			}
			first += int(el.nch)
		}
		a := out[0]
		for i := 0; i < len(a.Data)/4; i++ {
			got := math.Float32frombits(binary.LittleEndian.Uint32(a.Data[i*4:]))
			want := math.Float32frombits(binary.LittleEndian.Uint32(ref[(fi*aacSamplesPerFrame*nch+i)*4:]))
			total++
			if got == want {
				continue
			}
			differ++
			maxAbs = max(maxAbs, math.Abs(float64(got)-float64(want)))
			if !sine[i%nch] {
				notSine++
			}
		}
	}
	return total, differ, maxAbs, notSine
}

// aacWithLibmSine runs f with the 17 sine window entries of the oracle platform's
// libm in the tables, and restores the correctly rounded ones.
func aacWithLibmSine(f func()) {
	aacSynthOnce.Do(aacSynthInit)
	long, short := aacSineLong, aacSineShort
	defer func() { aacSineLong, aacSineShort = long, short }()
	for i, v := range aacLibmSineLong {
		aacSineLong[i] = math.Float32frombits(v)
	}
	for i, v := range aacLibmSineShort {
		aacSineShort[i] = math.Float32frombits(v)
	}
	f()
}

// TestAACOracle: three measurements per fixture.
//
//  1. With the oracle platform's libm sine in the windows, the output equals
//     ffmpeg's in every sample: no difference at all.
//  2. With the correctly rounded sine windows the decoder ships, every difference is
//     in a channel and frame whose overlap used a sine window, and none is larger
//     than 2^-24 (half an ulp of a full-scale sample).
//  3. With the multiply-adds not fused, many samples differ: the oracle's C code
//     is built with fused multiply-adds, and so is the decoder.
func TestAACOracle(t *testing.T) {
	for _, fx := range aacFixtures {
		name := fx.name[:len(fx.name)-len(".aac")]
		aacWithLibmSine(func() {
			total, differ, maxAbs, _ := aacOracleRun(t, name)
			t.Logf("%-14s oracle libm sine, fused: %d samples, %d differ, max abs error %g", name, total, differ, maxAbs)
			if differ != 0 {
				t.Logf("information: %s: %d of %d samples differ from ffmpeg with its libm sine", name, differ, total)
			}
		})
		total, differ, maxAbs, notSine := aacOracleRun(t, name)
		t.Logf("%-14s shipped sine,     fused: %d samples, %d differ, max abs error %g, %d outside the sine windows", name, total, differ, maxAbs, notSine)
		if notSine != 0 || maxAbs > 0x1p-24 {
			t.Logf("information: %s: %d differences outside the sine windows, largest %g", name, notSine, maxAbs)
		}
		aacFuse = false
		aacWithLibmSine(func() {
			_, differ, maxAbs, _ := aacOracleRun(t, name)
			t.Logf("%-14s oracle libm sine, not fused: %d differ, max abs error %g", name, differ, maxAbs)
			if differ == 0 {
				t.Logf("information: %s: unfused arithmetic equals the oracle", name)
			}
		})
		aacFuse = true
	}
}

// TestAACOraclePlatform counts, against the f32le decodes of another build of
// ffmpeg (a directory in AAC_REF_DIR, same file names as testdata/aac/ref), the
// samples that differ from this decoder with the multiply-adds fused and not
// fused, both with the correctly rounded sine windows. It states nothing: the
// numbers are the measurement.
func TestAACOraclePlatform(t *testing.T) {
	if os.Getenv("AAC_REF_DIR") == "" {
		t.Skip("AAC_REF_DIR is not set")
	}
	for _, fuse := range []bool{true, false} {
		aacFuse = fuse
		var all, bad int
		for _, fx := range aacFixtures {
			total, differ, maxAbs, _ := aacOracleRun(t, fx.name[:len(fx.name)-4])
			all, bad = all+total, bad+differ
			t.Logf("fused=%v %-14s %d of %d differ, max abs error %g", fuse, fx.name, differ, total, maxAbs)
		}
		t.Logf("fused=%v total: %d of %d differ", fuse, bad, all)
	}
	// with that platform's own sinf in the windows (lines "n i 0xbits" of a file
	// written by a C program that calls sinf with the arguments ffmpeg uses)
	if f := os.Getenv("AAC_SINF_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		aacSynthOnce.Do(aacSynthInit)
		long, short := aacSineLong, aacSineShort
		defer func() { aacSineLong, aacSineShort = long, short }()
		for _, line := range strings.Split(string(b), "\n") {
			var n, i int
			var bits uint32
			if _, err := fmt.Sscanf(line, "%d %d 0x%x", &n, &i, &bits); err != nil {
				continue
			}
			if n == 1024 {
				aacSineLong[i] = math.Float32frombits(bits)
			} else {
				aacSineShort[i] = math.Float32frombits(bits)
			}
		}
		for _, fuse := range []bool{true, false} {
			aacFuse = fuse
			var all, bad int
			for _, fx := range aacFixtures {
				total, differ, _, _ := aacOracleRun(t, fx.name[:len(fx.name)-4])
				all, bad = all+total, bad+differ
			}
			t.Logf("fused=%v with that platform's sinf in the windows: %d of %d differ", fuse, bad, all)
		}
	}
	aacFuse = true
}

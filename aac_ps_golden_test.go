// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/cpu"
)

// psOutputSHA is the SHA-256 of the left then the right PCM, as float32 bits.
func psOutputSHA(l, r []float32) string {
	h := sha256.New()
	var b [4]byte
	for _, ch := range [2][]float32{l, r} {
		for _, v := range ch {
			binary.LittleEndian.PutUint32(b[:], math.Float32bits(v))
			h.Write(b[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// The golden hashes are the SHA-256 of the decoded PCM of every written stream (S01 to
// S19, which exercise the layout switch, the freeze and resume, the carried matrix and
// the fade in of the first matrix), of the two streams below in which SBR drops out, and
// of he44-ps.aac. They pin the output bit for bit: a restructuring that keeps the
// arithmetic of every sample (the order of the additions, the fused multiply-adds) leaves
// them unchanged.
//
// One file per route of the float32 arithmetic, testdata/aac/psgolden/<route>.txt, a
// line "<stream> <sha256>" each. The routes (go-avkit/decode#77): arm64 (the NEON kernels
// of go-fft's DCT pair and of floats.MulAddRows32), amd64 with AVX2+FMA, and "portable"
// for everything else where the pair is two single transforms and the row sums are Go
// code (amd64 without AVX2 and the like); the lanes where the compiler fuses
// multiply-adds on its own or the integers are 32 bits (386, loong64, ppc64le, riscv64,
// s390x) and arm64 under the race detector have a file of their own. The banks' PCM is
// float32 arithmetic, so the order of the fused steps of each kernel reaches it and the
// routes do not agree bit for bit; every route meets the bounds of
// aac_sbr_qmf_bound_test.go and aac_sbr_adjbound_test.go. A change of the arithmetic on
// purpose records them again and says why: PS_GOLDEN_RECORD=1 on each route rewrites its
// file.

// psPairFused reports whether go-fft runs the DCT pair as one fused kernel here.
func psPairFused() bool {
	switch runtime.GOARCH {
	case "arm64":
		return true
	case "amd64":
		return cpu.X86.HasAVX2 && cpu.X86.HasFMA
	}
	return false
}

// psGoldenRoute is the name of this machine's route.
func psGoldenRoute() string {
	route := runtime.GOARCH
	if psRaceBuild {
		route += "race"
	}
	switch route {
	case "386", "arm64race", "loong64", "ppc64le", "riscv64", "s390x":
		return route
	}
	switch {
	case runtime.GOARCH == "arm64":
		return "arm64"
	case psPairFused():
		return "amd64"
	}
	return "portable"
}

// psGoldenLoad reads a route's file.
func psGoldenLoad(t *testing.T, route string) map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "aac", "psgolden", route+".txt"))
	if os.IsNotExist(err) {
		return nil // no file for this route yet: the test dumps what it decodes
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	golden := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, sum, ok := strings.Cut(sc.Text(), " ")
		if !ok || len(sum) != 64 {
			t.Fatalf("%s: line %q is not \"<stream> <sha256>\"", route, sc.Text())
		}
		golden[name] = sum
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return golden
}

// psDropoutSpecs are two streams in which SBR drops out, the frames of S16a34 (34 bands,
// IPD/OPD) and of S09c with no SBR fill element at all (withSBR false: PS is suspended
// and both channels are the mono signal until the SBR frames come back): a hole in the
// middle of the stream that ends with SBR resuming, and a stream that never resumes.
func psDropoutSpecs() []psStreamSpec {
	var out []psStreamSpec
	for _, d := range []struct {
		from, name string
		drop       func(i int) bool
	}{
		{"S16a34", "drop-S16a34-4-7", func(i int) bool { return i >= 4 && i < 7 }},
		{"S09c", "drop-S09c-8-", func(i int) bool { return i >= 8 }},
	} {
		for _, s := range psStreamSpecs() {
			if s.name != d.from {
				continue
			}
			s.name = d.name
			s.frames = append([]psStreamFrame(nil), s.frames...)
			for i := range s.frames {
				if d.drop(i) {
					s.frames[i].noSBR = true
				}
			}
			out = append(out, s)
		}
	}
	return out
}

// TestPSOutputGolden: the decoded PCM is the recorded one, bit for bit.
func TestPSOutputGolden(t *testing.T) {
	route := psGoldenRoute()
	record := os.Getenv("PS_GOLDEN_RECORD") != ""
	var golden map[string]string
	var recorded strings.Builder
	if !record {
		golden = psGoldenLoad(t, route)
	}
	seen := 0
	check := func(name, got string) {
		t.Helper()
		seen++
		fmt.Fprintf(&recorded, "%s %s\n", name, got)
		if record || golden == nil {
			return
		}
		if want, ok := golden[name]; !ok || want != got {
			t.Errorf("%s (route %s): output SHA-256 %s, recorded %q", name, route, got, want)
		}
	}
	for _, spec := range append(psStreamSpecs(), psDropoutSpecs()...) {
		b := psBuildStream(t, spec)
		cfg, frames := aacADTSFrames(t, psStreamFile(t, spec.name, b))
		l, r, _ := psDecodeFramesMode(t, cfg, frames, psStreamFrames, psTestMode{ffImage: spec.ffImage})
		check(spec.name, psOutputSHA(l, r))
	}
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", "he44-ps.aac"))
	l, r, _ := psDecodeFrames(t, cfg, fr, len(fr))
	check("he44-ps.aac", psOutputSHA(l, r))
	if record {
		path := filepath.Join("testdata", "aac", "psgolden", route+".txt")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(recorded.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d streams for route %s", seen, route)
		return
	}
	if golden == nil {
		// a lane that cannot be run by hand (qemu in CI) records from its log
		for _, ln := range strings.Split(strings.TrimSpace(recorded.String()), "\n") {
			t.Logf("PSGOLDEN %s %s", route, ln)
		}
		t.Errorf("route %s has no testdata/aac/psgolden/%s.txt", route, route)
		return
	}
	if seen != len(golden) {
		t.Errorf("route %s: %d streams decoded, %d recorded", route, seen, len(golden))
	}
}

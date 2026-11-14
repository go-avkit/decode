// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// psDecodeFrames decodes the frames of a mono AAC-LC stream through the SBR path with PS
// applied (the test switch) and returns the left and right PCM, frames frames at most,
// and the SBR state. cfg says PS may be carried, as an explicit or backward compatible
// signalling would.
func psDecodeFrames(t testing.TB, cfg aacConfig, fr [][]byte, frames int) (l, r []float32, s *sbrState) {
	t.Helper()
	cfg.sbr = true
	p, err := newAACParser(cfg, DefaultMaxAudioChannels)
	if err != nil {
		t.Fatal(err)
	}
	p.enableSBR()
	synth := newAACSynth(cfg.rateIndex, cfg.channels)
	for fi := 0; fi < min(frames, len(fr)); fi++ {
		els, err := p.parseFrame(fr[fi])
		if err != nil {
			t.Fatal(err)
		}
		var pcm [2][aacFrameLen]float32
		synth.element(&els[0], 0, [2][]float32{pcm[0][:], pcm[1][:]})
		s = p.sbrOf(els[0].id, els[0].tag)
		s.psApplyTest = true
		got := [2][]float32{make([]float32, sbrQMFSlots*sbrSynBands), make([]float32, sbrQMFSlots*sbrSynBands)}
		s.process(1, [2][]float32{pcm[0][:], pcm[1][:]}, got, els[0].sbr != nil)
		l, r = append(l, got[0]...), append(r, got[1]...)
	}
	return l, r, s
}

// psDecodeADTS decodes the ADTS file at path.
func psDecodeADTS(t testing.TB, path string, frames int) (l, r []float32) {
	t.Helper()
	cfg, fr := aacADTSFrames(t, path)
	l, r, _ = psDecodeFrames(t, cfg, fr, frames)
	return l, r
}

func psReadF32Stereo(t testing.TB, path string) (l, r []float32) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return psSplitF32(b)
}

// psSplitF32 splits interleaved little-endian float32 stereo.
func psSplitF32(b []byte) (l, r []float32) {
	for i := 0; i+8 <= len(b); i += 8 {
		l = append(l, math.Float32frombits(uint32(b[i])|uint32(b[i+1])<<8|uint32(b[i+2])<<16|uint32(b[i+3])<<24))
		r = append(r, math.Float32frombits(uint32(b[i+4])|uint32(b[i+5])<<8|uint32(b[i+6])<<16|uint32(b[i+7])<<24))
	}
	return l, r
}

// psMaxDiff is the largest absolute difference of two PCM buffers over their common part.
func psMaxDiff(a, b []float32) (d float64) {
	for i := range min(len(a), len(b)) {
		d = math.Max(d, math.Abs(float64(a[i])-float64(b[i])))
	}
	return d
}

// psHE44Bound is the bound on he44-ps.aac, the library-like file: ffmpeg's own two builds
// (C paths and NEON) are 2.4e-5 apart on it, in the blocks where one channel is 25 dB
// above the other (their float32 all-pass chain), and this decoder is within the same
// 4e-5 of both; 5e-5 is the bound, the per-block numbers are logged.
const psHE44Bound = 5e-5

// TestPSHE44AgainstFFmpeg: he44-ps.aac, the committed HE-AAC v2 file, decoded with PS
// applied against ffmpeg 9.0.2's PCM (testdata/aac/ref/he44-ps.f32, its C code paths):
// the first frames (the fade in of the first matrix) and the whole file.
func TestPSHE44AgainstFFmpeg(t *testing.T) {
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", "he44-ps.aac"))
	l, r, s := psDecodeFrames(t, cfg, fr, len(fr))
	fl, fr2 := psReadF32Stereo(t, filepath.Join("testdata", "aac", "ref", "he44-ps.f32"))
	if len(l) != len(fl) || s == nil || !s.ps.start {
		t.Fatalf("%d samples, ffmpeg %d, PS started %v", len(l), len(fl), s != nil && s.ps.start)
	}
	dl, dr := psMaxDiff(l, fl), psMaxDiff(r, fr2)
	first := math.Max(psMaxDiff(l[:2*2048], fl), psMaxDiff(r[:2*2048], fr2))
	t.Logf("he44-ps.aac, %d frames: largest difference L %.2e R %.2e; first two frames %.2e", len(l)/2048, dl, dr, first)
	if dl > psHE44Bound || dr > psHE44Bound {
		t.Errorf("differs from ffmpeg by L %.2e, R %.2e (bound %.0e)", dl, dr, psHE44Bound)
	}
	if first > 1e-6 {
		t.Errorf("the first two frames, which fade the first matrix in, differ by %.2e", first)
	}
}

// TestPSStreamSpread measures, for every written stream, how far ffmpeg's two builds of
// the same binary (C code paths, and the default NEON ones) are from each other and from
// this decoder, and the peak of the signal: the float32 terms of docs/aac-ps-mixing.md
// section 7.5, before any bound is asserted. PS_RECORD=<directory> of the recording.
func TestPSStreamSpread(t *testing.T) {
	dir := os.Getenv("PS_RECORD")
	if dir == "" {
		t.Skip("PS_RECORD=<directory> of the recording")
	}
	for _, spec := range psStreamSpecs() {
		b := psBuildStream(t, spec)
		l, r := psDecodeADTS(t, psStreamFile(t, spec.name, b), psStreamFrames)
		cl, cr := psReadF32Stereo(t, filepath.Join(dir, spec.name+".f32"))
		nl, nr := psReadF32Stereo(t, filepath.Join(dir, spec.name+".neon.f32"))
		ours := math.Max(psMaxDiff(l, cl), psMaxDiff(r, cr))
		oursNeon := math.Max(psMaxDiff(l, nl), psMaxDiff(r, nr))
		between := math.Max(psMaxDiff(cl, nl), psMaxDiff(cr, nr))
		peak := 0.0
		for _, v := range cl {
			peak = math.Max(peak, math.Abs(float64(v)))
		}
		t.Logf("%-7s peak %.3f  this decoder vs ffmpeg C paths %.2e, vs ffmpeg NEON %.2e, ffmpeg C vs NEON %.2e", spec.name, peak, ours, oursNeon, between)
	}
}

// psRealBound is the bound for the library files: the SBR bound. Measured on 8 files,
// the largest difference is 1.1e-6 (ffmpeg's float32 arithmetic); he44-ps.aac is the
// outlier, at 3.3e-5 in the blocks where its channels are 25 dB apart.
const psRealBound = 3e-6

// TestPSRealFiles: a sample of the real HE-AAC v2 files under AAC_REAL_HE_DIR (MP4,
// mono core with PS data), the first 300 frames of each decoded with PS applied against
// ffmpeg (AAC_REAL_HE_FFMPEG, the binary; the stream is copied out as ADTS first, so that
// no edit list or gapless tag moves the start). The files are taken in the order of the
// SHA-256 of their bytes, the first 8; only counts, hashes and errors are logged, never
// names. Nothing of the files is committed.
func TestPSRealFiles(t *testing.T) {
	dir, ff := os.Getenv("AAC_REAL_HE_DIR"), os.Getenv("AAC_REAL_HE_FFMPEG")
	if dir == "" || ff == "" {
		t.Skip("AAC_REAL_HE_DIR and AAC_REAL_HE_FFMPEG are not both set")
	}
	type file struct {
		hash   string
		path   string
		tc     container.TrackConfig
		frames [][]byte
	}
	var files []file
	aacRealHEFiles(t, dir, func(path string, b []byte, r *container.Reader, id uint32, tc container.TrackConfig) {
		ac, err := parseAudioSpecificConfig(tc.CodecConfig)
		if err != nil || ac.channels != 1 || !aacIsHE(r, id, tc, ac) {
			return
		}
		samples, err := r.Samples(id)
		if err != nil || len(samples) < 20 {
			return
		}
		f := file{path: path, tc: tc}
		sum := sha256.Sum256(b)
		f.hash = hex.EncodeToString(sum[:6])
		for _, s := range samples[:min(len(samples), 300)] {
			f.frames = append(f.frames, s.Data)
		}
		files = append(files, f)
	})
	sort.Slice(files, func(i, j int) bool { return files[i].hash < files[j].hash })
	done := 0
	for _, f := range files {
		if done == 8 {
			break
		}
		ac, _ := parseAudioSpecificConfig(f.tc.CodecConfig)
		l, r, s := psDecodeFrames(t, ac, f.frames, len(f.frames))
		if s == nil || !s.ps.start {
			continue // no PS data in these frames
		}
		done++
		adts := filepath.Join(t.TempDir(), "x.aac")
		if out, err := exec.Command(ff, "-nostdin", "-v", "error", "-y", "-i", f.path, "-vn", "-map", "0:a:0", "-c:a", "copy", "-f", "adts", adts).CombinedOutput(); err != nil {
			t.Logf("file %s: ffmpeg could not copy the stream: %v %s", f.hash, err, bytes.TrimSpace(out))
			continue
		}
		var buf bytes.Buffer
		cmd := exec.Command(ff, "-nostdin", "-v", "error", "-i", adts, "-f", "f32le", "-")
		cmd.Stdout = &buf
		if err := cmd.Run(); err != nil {
			t.Logf("file %s: ffmpeg: %v", f.hash, err)
			continue
		}
		fl, fr := psSplitF32(buf.Bytes())
		n := min(len(l), len(fl))
		dl, dr := psMaxDiff(l[:n], fl[:n]), psMaxDiff(r[:n], fr[:n])
		t.Logf("PS file %s: %d frames (%d samples per channel, ffmpeg's first %d): largest difference L %.2e R %.2e; 34 bands %v", f.hash, len(f.frames), len(l), len(fl), dl, dr, s.ps.is34)
		if dl > psRealBound || dr > psRealBound {
			t.Errorf("PS file %s differs from ffmpeg by L %.2e, R %.2e (bound %.0e)", f.hash, dl, dr, psRealBound)
		}
	}
	t.Logf("%d PS files compared out of %d mono HE-AAC files", done, len(files))
}

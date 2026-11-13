// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// TestAACRealHE decodes the HE-AAC audio of the real files under AAC_REAL_HE_DIR (MP4
// files up to 400 MB, three levels down) and logs what it finds, by number and never
// by name: channels, rate, samples, the SHA-256 of the output. With AAC_REAL_HE_FFMPEG
// set to an ffmpeg binary it also decodes each file with it and logs the largest
// difference (the same channel count only: ffmpeg decodes PS to stereo). A decode that
// fails, or differs from ffmpeg's by more than 3e-6 on a stream of the same channel
// count and length, fails the test. Skipped when the directory is not set.
func TestAACRealHE(t *testing.T) {
	dir := os.Getenv("AAC_REAL_HE_DIR")
	if dir == "" {
		t.Skip("AAC_REAL_HE_DIR is not set")
	}
	ffmpeg := os.Getenv("AAC_REAL_HE_FFMPEG")
	n := 0
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if e.IsDir() {
			if strings.Count(strings.TrimPrefix(path, dir), string(filepath.Separator)) > 3 {
				return fs.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".mp4", ".m4a", ".m4v", ".mov":
		default:
			return nil
		}
		info, err := e.Info()
		if err != nil || info.Size() > 400<<20 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		r, err := container.NewReader(b)
		if err != nil {
			return nil
		}
		id, err := FirstAudioTrack(r)
		if err != nil {
			return nil
		}
		tc, err := r.TrackConfig(id)
		if err != nil || tc.Codec != "mp4a" {
			return nil
		}
		ac, err := parseAudioSpecificConfig(tc.CodecConfig)
		if err != nil || !ac.sbr {
			return nil
		}
		n++
		bufs, err := DecodeAudioTrack(r, id, AudioConfig{MaxOutputBytes: 1 << 30})
		if errors.Is(err, ErrOverLimit) {
			t.Logf("HE-AAC file %d: over a gigabyte of PCM, skipped", n)
			return nil
		}
		if err != nil {
			t.Errorf("HE-AAC file %d: %v", n, err)
			return nil
		}
		h := sha256.New()
		var all []float32
		ch, rate := 0, 0
		for _, a := range bufs {
			h.Write(a.Data)
			ch, rate = a.Channels, a.SampleRate
			for i := 0; i+4 <= len(a.Data); i += 4 {
				all = append(all, math.Float32frombits(binary.LittleEndian.Uint32(a.Data[i:])))
			}
		}
		t.Logf("HE-AAC file %d: %d channels at %d Hz (core %d Hz, PS signalled %v), %d samples per channel, SHA-256 %s", n, ch, rate, ac.rate, ac.ps, len(all)/max(ch, 1), hex.EncodeToString(h.Sum(nil)))
		if ffmpeg == "" {
			return nil
		}
		cmd := exec.Command(ffmpeg, "-v", "error", "-i", path, "-vn", "-map", "0:a:0", "-f", "f32le", "-")
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Logf("HE-AAC file %d: ffmpeg: %v", n, err)
			return nil
		}
		ref := out.Bytes()
		if ac.ps {
			t.Logf("HE-AAC file %d: PS signalled, ffmpeg decodes it to stereo and this decoder to the SBR mono channel: not compared", n)
			return nil
		}
		// ffmpeg gives a mono SBR stream as stereo (a PS stream it cannot rule out) with
		// the same samples in both channels: compare channel by channel on its layout
		fch := ch
		if ch == 1 && len(ref)/8 > len(all)-2048 && len(ref)/8 < len(all)+2048 && len(ref)/4 > len(all)+2048 {
			fch = 2
		}
		fs := len(ref) / (4 * fch)
		mine := len(all) / ch
		if len(ref)%(4*fch) != 0 || fs < mine-2048 || fs > mine+2048 {
			t.Logf("HE-AAC file %d: ffmpeg gives %d samples of %d channels, this decoder %d of %d: not compared", n, fs, fch, mine, ch)
			return nil
		}
		// where the two differ in length ours has more at one end: try both ends
		best := math.Inf(1)
		for _, skip := range []int{0, max(mine-fs, 0)} {
			m := min(mine-skip, fs)
			var worst float64
			for i := 0; i < m; i++ {
				for c := 0; c < ch; c++ {
					a := all[(skip+i)*ch+c]
					b := math.Float32frombits(binary.LittleEndian.Uint32(ref[4*(i*fch+c):]))
					worst = math.Max(worst, math.Abs(float64(a)-float64(b)))
				}
			}
			best = math.Min(best, worst)
		}
		t.Logf("HE-AAC file %d: %d samples against ffmpeg's %d, largest difference %.2e", n, mine, fs, best)
		if best > 3e-6 {
			t.Errorf("HE-AAC file %d differs from ffmpeg by %.2e", n, best)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d HE-AAC files", n)
}

// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// aacRealHEFiles walks dir for MP4 files up to 400 MB (three levels down) whose first
// audio track is mp4a and calls f with the file's bytes, reader and track.
func aacRealHEFiles(t *testing.T, dir string, f func(path string, b []byte, r *container.Reader, id uint32, tc container.TrackConfig)) {
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
		f(path, b, r, id, tc)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// aacIsHE says whether a track is HE-AAC, however it signals it: its configuration, or
// else the rate its first frames come out at.
func aacIsHE(r *container.Reader, id uint32, tc container.TrackConfig, ac aacConfig) bool {
	if ac.sbr {
		return true
	}
	samples, err := r.Samples(id)
	if err != nil {
		return false
	}
	d, err := NewAudioTrack(tc, AudioConfig{})
	if err != nil {
		return false
	}
	defer d.Close()
	for i := 0; i < min(len(samples), 40); i++ {
		out, err := d.DecodeSample(samples[i])
		if err != nil {
			return false
		}
		for _, a := range out {
			if a.SampleRate != ac.rate {
				return true
			}
		}
	}
	return false
}

// aacSMPB is what a file says in its iTunSMPB tag, or "none": the three numbers the
// encoder wrote (priming, padding, original length), as the file has them.
func aacSMPB(b []byte) string {
	i := bytes.Index(b, []byte("iTunSMPB"))
	if i < 0 {
		return "none"
	}
	rest := b[i+8:]
	j := bytes.Index(rest[:min(len(rest), 120)], []byte("data"))
	if j < 0 {
		return "present"
	}
	s := rest[j+4+8 : min(len(rest), j+4+8+64)]
	var out []byte
	for _, c := range s {
		if c < 32 || c > 126 {
			break
		}
		out = append(out, c)
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

// aacEditSummary says in a few words what a track's edit list holds, in timescale units.
func aacEditSummary(tc container.TrackConfig) string {
	el := tc.EditList
	if el == nil {
		return "no edit list"
	}
	s := fmt.Sprintf("movie timescale %d, %d edits", el.MovieTimescale, len(el.Edits))
	for _, e := range el.Edits {
		s += fmt.Sprintf(" [media %d duration %d]", e.MediaTime, e.SegmentDuration)
	}
	return s
}

// TestAACRealHE decodes the HE-AAC audio of the real files under AAC_REAL_HE_DIR (MP4
// files up to 400 MB, three levels down), signalled or not, and logs what it finds, by
// number and never by name: channels, rate, samples, the SHA-256 of the output, the
// track timescale, the edit list, the iTunSMPB tag. With AAC_REAL_HE_FFMPEG set to an
// ffmpeg binary it also decodes each file with it and requires the same number of
// samples, whatever the codec says (a PS stream's are stereo in ffmpeg and mono here, a
// mono SBR stream's stereo in ffmpeg, the channels equal), and for streams without PS
// the same values to 3e-6. Skipped when the directory is not set.
func TestAACRealHE(t *testing.T) {
	dir := os.Getenv("AAC_REAL_HE_DIR")
	if dir == "" {
		t.Skip("AAC_REAL_HE_DIR is not set")
	}
	ffmpeg := os.Getenv("AAC_REAL_HE_FFMPEG")
	n := 0
	aacRealHEFiles(t, dir, func(path string, b []byte, r *container.Reader, id uint32, tc container.TrackConfig) {
		ac, err := parseAudioSpecificConfig(tc.CodecConfig)
		if err != nil || !aacIsHE(r, id, tc, ac) {
			return
		}
		n++
		bufs, err := DecodeAudioTrack(r, id, AudioConfig{MaxOutputBytes: 1 << 30})
		if errors.Is(err, ErrOverLimit) {
			t.Logf("HE-AAC file %d: over a gigabyte of PCM, skipped", n)
			return
		}
		if err != nil {
			t.Errorf("HE-AAC file %d: %v", n, err)
			return
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
		mine := len(all) / max(ch, 1)
		t.Logf("HE-AAC file %d: %d channels at %d Hz (core %d Hz, signalled %v, PS %v), %d samples per channel, SHA-256 %s; track timescale %d, %s, iTunSMPB %s",
			n, ch, rate, ac.rate, ac.sbr, ac.ps, mine, hex.EncodeToString(h.Sum(nil)), tc.Timescale, aacEditSummary(tc), aacSMPB(b))
		if ffmpeg == "" {
			return
		}
		cmd := exec.Command(ffmpeg, "-v", "error", "-i", path, "-vn", "-map", "0:a:0", "-f", "f32le", "-")
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Logf("HE-AAC file %d: ffmpeg: %v", n, err)
			return
		}
		ref := out.Bytes()
		fch := 0
		switch {
		case len(ref)/4/max(ch, 1) == mine && len(ref)%(4*max(ch, 1)) == 0:
			fch = ch
		case ch == 1 && len(ref)/8 == mine && len(ref)%8 == 0:
			fch = 2
		default:
			// where the lengths differ say where: the values with both ends aligned (ours
			// the longer), so that the cause can be read off the log
			fch = ch
			if ch == 1 && len(ref)/8 > mine/2 && len(ref)%8 == 0 {
				fch = 2
			}
			fs := len(ref) / (4 * fch)
			var atStart, atEnd = math.NaN(), math.NaN()
			if d := mine - fs; d > 0 && d < 4096 && len(ref)%(4*fch) == 0 {
				for k, skip := range []int{0, d} {
					var worst float64
					for i := 0; i < fs; i++ {
						for c := 0; c < ch; c++ {
							a := all[(skip+i)*ch+c]
							r := math.Float32frombits(binary.LittleEndian.Uint32(ref[4*(i*fch+c):]))
							worst = math.Max(worst, math.Abs(float64(a)-float64(r)))
						}
					}
					if k == 0 {
						atStart = worst
					} else {
						atEnd = worst
					}
				}
			}
			t.Errorf("HE-AAC file %d (PS %v): ffmpeg gives %d samples, this decoder %d (%d more); largest difference with the starts aligned %.2e, with the ends aligned %.2e", n, ac.ps, fs, mine, mine-fs, atStart, atEnd)
			return
		}
		if ac.ps {
			return // same length; the values differ by the stereo image PS makes
		}
		var worst float64
		for i := 0; i < mine; i++ {
			for c := 0; c < ch; c++ {
				a := all[i*ch+c]
				r := math.Float32frombits(binary.LittleEndian.Uint32(ref[4*(i*fch+c):]))
				worst = math.Max(worst, math.Abs(float64(a)-float64(r)))
			}
		}
		t.Logf("HE-AAC file %d: largest difference to ffmpeg %.2e", n, worst)
		if worst > 3e-6 {
			t.Errorf("HE-AAC file %d differs from ffmpeg by %.2e", n, worst)
		}
	})
	t.Logf("%d HE-AAC files", n)
}

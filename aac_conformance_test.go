// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// ISO/IEC 14496-26 audio conformance streams (AAC-LC, MP4) and their reference
// waveforms. They are electronic inserts that ISO lets one use unmodified: they are
// not in this repository, only their SHA-256 (testdata/aac-conformance.txt).
// `go run ./aacconf fetch DIR ../testdata/aac-conformance.txt` in tools downloads
// them, CI does that, and AAC_CONFORMANCE_DIR points here.
//
// The bound is PROVISIONAL (docs/aac-design.md, G3): the criterion is in the text of
// 14496-26, which has not been read. Until it has, a sample must be within one least
// significant bit of the 16-bit reference.
const aacConformanceLSB = 1.0

// aacWAV returns the samples of a 16 or 24-bit PCM WAVE file as fractions of full
// scale, and its channel count and rate.
func aacWAV(b []byte) (pcm []float64, channels, rate int, err error) {
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0, 0, fmt.Errorf("not a WAVE file")
	}
	bits := 0
	for p := 12; p+8 <= len(b); {
		id, n := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:]))
		body := b[p+8 : min(p+8+n, len(b))]
		switch id {
		case "fmt ":
			if len(body) < 16 {
				return nil, 0, 0, fmt.Errorf("short fmt chunk")
			}
			channels, rate, bits = int(binary.LittleEndian.Uint16(body[2:])), int(binary.LittleEndian.Uint32(body[4:])), int(binary.LittleEndian.Uint16(body[14:]))
		case "data":
			if bits != 16 && bits != 24 {
				return nil, 0, 0, fmt.Errorf("%d-bit samples", bits)
			}
			w := bits / 8
			pcm = make([]float64, len(body)/w)
			for i := range pcm {
				if bits == 16 {
					pcm[i] = float64(int16(binary.LittleEndian.Uint16(body[i*2:]))) / 32768
				} else {
					v := int32(body[i*3])<<8 | int32(body[i*3+1])<<16 | int32(body[i*3+2])<<24
					pcm[i] = float64(v>>8) / (1 << 23)
				}
			}
			return pcm, channels, rate, nil
		}
		p += 8 + n + n&1
	}
	return nil, 0, 0, fmt.Errorf("no data chunk")
}

// aacConformanceOpen lists the vectors this decoder does not yet reproduce, and
// why. They are measured, not excused: the test fails if a vector listed here
// starts to pass (remove it) and if any other fails.
var aacConformanceOpen = map[string]string{
	"al03_48": "window sequences that break the start/short/stop rule (a start followed by a long window, a stop with no short before it): the decoder overlaps as ffmpeg does, treating those transitions as short to short, and the reference windows each block on its own",
	"al03_96": "the same",
	"al03_11": "the same",
	"al18_48": "perceptual noise substitution: the reference uses the ISO generator, this decoder ffmpeg's (docs/aac-design.md, G5)",
	"al19_48": "the same, with short windows",
	"al17_48": "a program_config_element that puts two elements on one speaker is refused",
}

func TestAACConformance(t *testing.T) {
	dir := os.Getenv("AAC_CONFORMANCE_DIR")
	if dir == "" {
		t.Skip("AAC_CONFORMANCE_DIR is not set")
	}
	pins := map[string]string{}
	var names []string
	f, err := os.Open(filepath.Join("testdata", "aac-conformance.txt"))
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fs := strings.Fields(sc.Text()); len(fs) == 2 && !strings.HasPrefix(fs[0], "#") {
			pins[fs[1]] = fs[0]
			if strings.HasPrefix(fs[1], "compressedMp4/") {
				names = append(names, strings.TrimSuffix(strings.TrimPrefix(fs[1], "compressedMp4/"), ".mp4"))
			}
		}
	}
	f.Close()
	read := func(rel string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if h := sha256.Sum256(b); hex.EncodeToString(h[:]) != pins[rel] {
			t.Fatalf("%s: SHA-256 %x, the pin is %s", rel, h, pins[rel])
		}
		return b
	}
	for _, name := range names {
		mp4, wav := read("compressedMp4/"+name+".mp4"), read("referencesWav/"+name+".wav")
		ref, rch, rrate, err := aacWAV(wav)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		r, err := container.NewReader(mp4)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		id, err := FirstAudioTrack(r)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		bufs, err := DecodeAudioTrack(r, id, AudioConfig{})
		if err != nil {
			if why, ok := aacConformanceOpen[name]; ok && errors.Is(err, ErrAudioUnsupported) {
				t.Logf("%s: open: %s (%v)", name, why, err)
				continue
			}
			t.Errorf("%s: %v", name, err)
			continue
		}
		var got []float32
		ch, rate := 0, 0
		for _, a := range bufs {
			ch, rate = a.Channels, a.SampleRate
			for i := 0; i < len(a.Data)/4; i++ {
				got = append(got, math.Float32frombits(binary.LittleEndian.Uint32(a.Data[i*4:])))
			}
		}
		if ch != rch || rate != rrate {
			t.Errorf("%s: %d channels at %d Hz, the reference has %d at %d", name, ch, rate, rch, rrate)
			continue
		}
		// The reference waveforms leave out the first two frames (2048 samples of
		// every channel) of what the decoder delivers: measured on the mono vectors,
		// where a shift of 2048 gives 0.004 LSB and 1024 gives thousands.
		got = got[min(2048*ch, len(got)):]
		n := min(len(got), len(ref))
		var worst float64
		over := 0
		for i := 0; i < n; i++ {
			d := math.Abs(float64(got[i])-ref[i]) * 32768 // in 16-bit least significant bits
			worst = math.Max(worst, d)
			if d > aacConformanceLSB {
				over++
			}
		}
		t.Logf("%-8s %d ch %5d Hz: %d samples, reference %d; max error %.3f LSB, %d samples over %g LSB", name, ch, rate, len(got), len(ref), worst, over, aacConformanceLSB)
		failed := len(got) != len(ref) || over != 0
		if why, ok := aacConformanceOpen[name]; ok {
			if !failed {
				t.Errorf("%s now passes: remove it from aacConformanceOpen", name)
			} else {
				t.Logf("%s: open: %s", name, why)
			}
			continue
		}
		if failed {
			t.Errorf("%s: %d samples against %d, %d beyond the provisional bound (max %.3f LSB)", name, len(got), len(ref), over, worst)
		}
	}
}

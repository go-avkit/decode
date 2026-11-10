// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// TestAACRealFiles decodes the clips of real files listed in
// testdata/aac-real-files.txt (c1.mp4, c2.mp4, ... in AAC_REAL_FILES_DIR, in that
// order) and compares the bytes of the output with the hash of ffmpeg's. It runs
// with the oracle platform's libm sine, as TestAACOracle does, so it only means
// something on the machine the hashes came from.
func TestAACRealFiles(t *testing.T) {
	dir := os.Getenv("AAC_REAL_FILES_DIR")
	if dir == "" {
		t.Skip("AAC_REAL_FILES_DIR is not set")
	}
	saved := aacTNSMap
	defer func() { aacTNSMap = saved }()
	for k, v := range aacOracleTNS {
		aacTNSMap[k[0]][k[1]] = v
	}
	f, err := os.Open(filepath.Join("testdata", "aac-real-files.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n++
		var ch, rate, size int
		var sum string
		if _, err := fmt.Sscanf(line, "%d %d %d %s", &ch, &rate, &size, &sum); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("c%d.mp4", n)))
		if err != nil {
			t.Fatal(err)
		}
		r, err := container.NewReader(b)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := FirstAudioTrack(r)
		var bufs []Audio
		aacWithLibmSine(func() { bufs, err = DecodeAudioTrack(r, id, AudioConfig{}) })
		if err != nil {
			t.Fatalf("clip %d: %v", n, err)
		}
		h := sha256.New()
		total := 0
		for _, a := range bufs {
			if a.Channels != ch || a.SampleRate != rate {
				t.Fatalf("clip %d: %d channels at %d Hz, want %d at %d", n, a.Channels, a.SampleRate, ch, rate)
			}
			h.Write(a.Data[:min(len(a.Data), max(size-total, 0))])
			total += len(a.Data)
		}
		// at most one frame more than what is compared
		if got := hex.EncodeToString(h.Sum(nil)); total < size || total > size+aacSamplesPerFrame*ch*4 || got != sum {
			t.Errorf("clip %d (%d channels, %d Hz): %d bytes, SHA-256 %s; ffmpeg's output is %d bytes, %s", n, ch, rate, total, got, size, sum)
		} else {
			t.Logf("clip %d (%d channels, %d Hz): %d bytes, identical to ffmpeg's", n, ch, rate, total)
		}
	}
}

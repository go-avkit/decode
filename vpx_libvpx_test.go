// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// libvpxManifests is the directory of libvpx's per-frame md5 files, one NAME.md5 for each decoder
// test vector NAME that libvpx's test/test_vectors.cc lists (vp80, vp90, vp91, vp92 and vp93),
// copied unchanged from https://storage.googleapis.com/downloads.webmproject.org/test_data/libvpx/.
const libvpxManifests = "testdata/libvpx"

// libvpxVectors is how many vectors the manifests hold (libvpx test/test_vectors.cc at the
// revision tools/vpxfetch was run against).
const libvpxVectors = 376

// A libvpx md5 file has one line per output picture, "MD5  NAME-WxH-NNNN.i420" (libvpx
// test/test_vector_test.cc reads it with fscanf "%s  %s"); the MD5 is that of the picture's three
// planes, each cropped to the display size, chroma rounded up (libvpx test/md5_helper.h, MD5::Add).
// That is the hash the Frame layout of this package gives, so the comparison is picture for picture.
func libvpxReference(t testing.TB, name string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(string(readFile(t, filepath.Join(libvpxManifests, name+".md5"))), "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}

// ivfSamples cuts an IVF file (a 32-byte header whose bytes 6-7 give its length, then frames of a
// 4-byte size, an 8-byte pts and the data) into its frames.
func ivfSamples(b []byte) ([][]byte, error) {
	if len(b) < 32 || string(b[:4]) != "DKIF" {
		return nil, errors.New("not an IVF file")
	}
	pos := int(binary.LittleEndian.Uint16(b[6:8]))
	if pos < 32 || pos > len(b) {
		return nil, errors.New("bad IVF header length")
	}
	var out [][]byte
	for pos < len(b) {
		if len(b)-pos < 12 {
			return nil, errors.New("truncated IVF frame header")
		}
		n := int(binary.LittleEndian.Uint32(b[pos:]))
		pos += 12
		if n < 0 || n > len(b)-pos {
			return nil, errors.New("truncated IVF frame")
		}
		out = append(out, b[pos:pos+n])
		pos += n
	}
	return out, nil
}

// libvpxSamples reads a vector's frames: IVF directly, WebM through the container reader.
func libvpxSamples(path string) ([][]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".ivf") {
		return ivfSamples(b)
	}
	return webmBlocks(b)
}

// webmBlocks lists the frames of the blocks of a WebM file with one track, in file order. The
// container package's reader refuses some of these files (libvpx's older muxer writes the
// FrameRate element, 0x2383E3, which it does not know), so the vectors are read here: the
// EBML elements (ID, size) are walked flat, entering Segment, Cluster and BlockGroup (whatever
// their size, which may be unknown) and skipping the rest, and each SimpleBlock or Block gives
// one frame after its track number, 16-bit timecode and flags byte. Lacing is refused.
func webmBlocks(b []byte) ([][]byte, error) {
	const (
		segment, cluster, blockGroup = 0x18538067, 0x1F43B675, 0xA0
		simpleBlock, block           = 0xA3, 0xA1
	)
	var out [][]byte
	pos := 0
	vint := func(keepMarker bool) (v uint64, unknown bool, err error) {
		if pos >= len(b) || b[pos] == 0 {
			return 0, false, errors.New("bad EBML variable-length integer")
		}
		n := 1
		for b[pos]&(0x80>>(n-1)) == 0 {
			n++
		}
		if pos+n > len(b) {
			return 0, false, errors.New("truncated EBML variable-length integer")
		}
		v = uint64(b[pos])
		if !keepMarker {
			v &= 0xff >> n
		}
		allOnes := b[pos]&(0xff>>n) == 0xff>>n
		for _, c := range b[pos+1 : pos+n] {
			v = v<<8 | uint64(c)
			allOnes = allOnes && c == 0xff
		}
		pos += n
		return v, allOnes, nil
	}
	for pos < len(b) {
		id, _, err := vint(true)
		if err != nil {
			return nil, err
		}
		size, unknown, err := vint(false)
		if err != nil {
			return nil, err
		}
		switch id {
		case segment, cluster, blockGroup:
			continue // enter it
		}
		if unknown || size > uint64(len(b)-pos) {
			return nil, fmt.Errorf("element %#x has size %d past the end of the file", id, size)
		}
		body := b[pos : pos+int(size)]
		pos += int(size)
		if id != simpleBlock && id != block {
			continue
		}
		if len(body) < 4 {
			return nil, errors.New("short block")
		}
		track := 1
		for body[0]&(0x80>>(track-1)) == 0 && track < 8 {
			track++
		}
		if len(body) < track+3 || body[track+2]&0x06 != 0 {
			return nil, errors.New("laced or short block")
		}
		out = append(out, body[track+3:])
	}
	return out, nil
}

// libvpxDecode decodes every sample and returns the pictures' md5s; it stops at the first error.
func libvpxDecode(name string, samples [][]byte, threads int) (sums []string, err error) {
	mk := NewVP9
	if strings.HasPrefix(name, "vp80") {
		mk = NewVP8
	}
	d, err := mk(Config{Threads: threads})
	if err != nil {
		return nil, err
	}
	defer d.Close()
	for i, s := range samples {
		frames, err := d.DecodeSample(s, int64(i))
		for _, f := range frames {
			sums = append(sums, sum(f))
		}
		if err != nil {
			return sums, fmt.Errorf("sample %d: %w", i, err)
		}
	}
	return sums, nil
}

func libvpxNames(t testing.TB) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(libvpxManifests, "*.md5"))
	if err != nil || len(m) == 0 {
		t.Fatalf("libvpx manifests: %d, %v", len(m), err)
	}
	var names []string
	for _, p := range m {
		names = append(names, strings.TrimSuffix(filepath.Base(p), ".md5"))
	}
	return names
}

// TestLibvpxVectors decodes libvpx's decoder test vectors and compares every picture's md5 with the
// one libvpx states. The streams are not in this repository: set LIBVPX_VECTORS_DIR to a directory
// holding them, as `go run ./tools/vpxfetch` (in tools) downloads them. VP8 vectors (vp80) and VP9
// profile 0 to 3 vectors (vp90 to vp93) must match picture for picture, except the 4:4:0 ones,
// which must be refused as ErrUnsupported (Frame has no 4:4:0).
func TestLibvpxVectors(t *testing.T) { libvpxVectorsAt(t, 0) }

// TestLibvpxVectorsThreaded is the same oracle with Config.Threads 4: threads must not change a picture.
func TestLibvpxVectorsThreaded(t *testing.T) { libvpxVectorsAt(t, 4) }

func libvpxVectorsAt(t *testing.T, threads int) {
	dir := os.Getenv("LIBVPX_VECTORS_DIR")
	if dir == "" {
		t.Skip("LIBVPX_VECTORS_DIR is not set: the libvpx test vectors were NOT run")
	}
	matched, refused := 0, 0
	defer func() {
		t.Logf("libvpx vectors, %d threads: %d match libvpx's md5s, %d 4:4:0 refused, of %d", threads, matched, refused, libvpxVectors)
	}()
	for _, name := range libvpxNames(t) {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			if !fileExists(path) {
				// With the directory named, a vector that is missing is a hole in the guard, not a skip.
				t.Fatalf("%s is not in %s: this vector was NOT run (go run ./tools/vpxfetch downloads them)", name, dir)
			}
			samples, err := libvpxSamples(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := libvpxDecode(name, samples, threads)
			if strings.Contains(name, "yuv440") {
				// 4:4:0 has no Chroma value: refused on the first picture, nothing decoded.
				if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "4:4:0") || len(got) != 0 {
					t.Fatalf("a 4:4:0 vector gave %v after %d pictures, want ErrUnsupported naming 4:4:0", err, len(got))
				}
				refused++
				return
			}
			want := libvpxReference(t, name)
			var problems []string
			if err != nil {
				problems = append(problems, fmt.Sprintf("%v, after %d of %d pictures", err, len(got), len(want)))
			}
			for i := range want {
				if i >= len(got) {
					problems = append(problems, fmt.Sprintf("%d pictures, libvpx has %d", len(got), len(want)))
					break
				}
				if got[i] != want[i] {
					problems = append(problems, fmt.Sprintf("first differing picture %d of %d: md5 %s, libvpx %s", i, len(want), got[i], want[i]))
					break
				}
			}
			if len(got) > len(want) {
				problems = append(problems, fmt.Sprintf("%d pictures, libvpx has %d", len(got), len(want)))
			}
			// No vector is allowed to differ: there is no list of known failures.
			if len(problems) > 0 {
				t.Error(strings.Join(problems, "; "))
			} else {
				matched++
			}
		})
	}
}

func TestLibvpxManifestsAreCommitted(t *testing.T) {
	names := libvpxNames(t)
	if len(names) != libvpxVectors {
		t.Errorf("%d libvpx manifests, want %d", len(names), libvpxVectors)
	}
	for _, n := range names {
		if !strings.HasPrefix(n, "vp80-") && !strings.HasPrefix(n, "vp90-") && !strings.HasPrefix(n, "vp91-") &&
			!strings.HasPrefix(n, "vp92-") && !strings.HasPrefix(n, "vp93-") {
			t.Errorf("%s is not a vp80, vp90, vp91, vp92 or vp93 vector", n)
		}
		if got := libvpxReference(t, n); len(got) == 0 {
			t.Errorf("%s: empty manifest", n)
		}
	}
}

func TestIVFSamples(t *testing.T) {
	hdr := make([]byte, 32)
	copy(hdr, "DKIF")
	binary.LittleEndian.PutUint16(hdr[6:], 32)
	frame := func(data string) []byte {
		h := make([]byte, 12)
		binary.LittleEndian.PutUint32(h, uint32(len(data)))
		return append(h, data...)
	}
	good := append(append(append([]byte{}, hdr...), frame("ab")...), frame("")...)
	got, err := ivfSamples(good)
	if err != nil || len(got) != 2 || string(got[0]) != "ab" || len(got[1]) != 0 {
		t.Fatalf("ivfSamples = %q, %v", got, err)
	}
	for name, b := range map[string][]byte{
		"short":             hdr[:10],
		"magic":             append([]byte("XXXX"), hdr[4:]...),
		"header too long":   func() []byte { h := append([]byte{}, hdr...); binary.LittleEndian.PutUint16(h[6:], 99); return h }(),
		"header too short":  func() []byte { h := append([]byte{}, hdr...); binary.LittleEndian.PutUint16(h[6:], 8); return h }(),
		"frame header cut":  append(append([]byte{}, hdr...), 1, 2, 3),
		"frame data cut":    good[:len(good)-13],
		"frame size absurd": append(append([]byte{}, hdr...), 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 0, 0, 0, 0),
	} {
		if _, err := ivfSamples(b); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// webmBlocks gives the frames the container reader gives, on every committed stream.
func TestWebmBlocksMatchContainer(t *testing.T) {
	for _, s := range vpxStreams {
		r, id := vpxTrack(t, s.name)
		ss, err := r.Samples(id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := webmBlocks(readFile(t, filepath.Join("testdata", "vpx", s.name+".webm")))
		if err != nil || len(got) != len(ss) {
			t.Fatalf("%s: %d blocks, %v; the container reader has %d samples", s.name, len(got), err, len(ss))
		}
		for i := range ss {
			if string(got[i]) != string(ss[i].Data) {
				t.Fatalf("%s: block %d differs", s.name, i)
			}
		}
	}
}

func TestWebmBlocksRefusals(t *testing.T) {
	for name, b := range map[string][]byte{
		"zero byte":           {0x00},
		"truncated integer":   {0x18, 0x53},
		"size past the end":   {0xA3, 0x85, 1, 2},
		"unknown size":        {0xA3, 0xFF},
		"short block":         {0xA3, 0x82, 0x81, 0},
		"laced block":         {0xA3, 0x86, 0x81, 0, 0, 0x02, 1, 2},
		"truncated size":      {0xA3, 0x40},
		"block shorter track": {0xA3, 0x84, 0x01, 0, 0, 0},
	} {
		if _, err := webmBlocks(b); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	got, err := webmBlocks([]byte{0xA3, 0x85, 0x81, 0, 0, 0, 7, 0xEC, 0x81, 0})
	if err != nil || len(got) != 1 || len(got[0]) != 1 || got[0][0] != 7 {
		t.Errorf("a block and a skipped element: %v, %v", got, err)
	}
}

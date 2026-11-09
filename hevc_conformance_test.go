// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// suiteOracle lists the JCT-VC conformance bitstreams on which ffmpeg 9.0.2 does
// NOT agree with the suite, so that the suite is the oracle: the MD5 of the whole
// decoded output (every picture's planes, in output order, cropped) that the
// stream's own md5 file in the conformance package states. In each of the three
// this decoder's output equals it.
//
//   - SAODBLK_A and SAODBLK_B (MainConcept): ffmpeg's output differs from the
//     suite's MD5 from picture 0 on, with and without its assembly (-cpuflags 0).
//   - VPSSPSPPS_A (MainConcept): ffmpeg logs "SPS 2 does not exist" and similar
//     and outputs 2 pictures where the stream, per the suite, has 6.
var suiteOracle = map[string]string{
	"SAODBLK_A_MainConcept_4":   "b9c98937bcb6be445348fdc89c208f1e",
	"SAODBLK_B_MainConcept_4":   "031043495e9b9f4ce120afa316aa5705",
	"VPSSPSPPS_A_MainConcept_1": "1ddf74263cb4953cfdfcf99c563d88ea",
}

// suiteRefused is the one stream of the suite this decoder refuses, and ffmpeg
// does too ("Luma bit depth (10) is different from chroma bit depth (9)"): its
// luma and chroma bit depths differ.
const suiteRefused = "TSUNEQBD_A_MAIN10_Technicolor_2"

func conformanceStream(dir, name string) string {
	for _, ext := range []string{".bit", ".bin", ".bitstream", ".hevc", ".h265", ".265"} {
		if p := filepath.Join(dir, name, name+ext); fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// TestHEVCConformance decodes the JCT-VC HEVC_v1 conformance bitstreams (ITU-T
// H.265.1) and compares every picture's MD5 with what ffmpeg -f framemd5
// produced (testdata/hevc-conformance/NAME.md5). The bitstreams are not in this
// repository: set DECODE_HEVC_DIR to a directory holding NAME/NAME.bit (or
// .bin) as `jvt --codec hevc fetch --all` unpacks them, from
// https://www.itu.int/wftp3/av-arch/jctvc-site/bitstream_exchange/draft_conformance/HEVC_v1/
func TestHEVCConformance(t *testing.T) {
	dir := os.Getenv("DECODE_HEVC_DIR")
	if dir == "" {
		t.Skip("DECODE_HEVC_DIR is not set")
	}
	manifests, err := filepath.Glob(filepath.Join("testdata", "hevc-conformance", "*.md5"))
	if err != nil || len(manifests) < 140 {
		t.Fatalf("manifests: %d, %v", len(manifests), err)
	}
	names := []string{suiteRefused}
	for _, m := range manifests {
		names = append(names, strings.TrimSuffix(filepath.Base(m), ".md5"))
	}
	for name := range suiteOracle {
		names = append(names, name)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			stream := conformanceStream(dir, name)
			if stream == "" {
				t.Skipf("%s is not in %s", name, dir)
			}
			d, err := NewHEVC(Config{MaxFrameMBs: 1 << 17}, 4)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			frames, err := d.DecodeAnnexB(readFile(t, stream))
			if err == nil {
				var rest []Frame
				rest, err = d.Flush()
				frames = append(frames, rest...)
			}
			switch {
			case name == suiteRefused:
				if !errors.Is(err, ErrUnsupported) || len(frames) != 0 {
					t.Fatalf("%d pictures, %v, want a refusal", len(frames), err)
				}
			case err != nil:
				t.Fatal(err)
			case suiteOracle[name] != "":
				h := md5.New()
				for _, f := range frames {
					h.Write(f.Y)
					h.Write(f.Cb)
					h.Write(f.Cr)
				}
				if got := hex.EncodeToString(h.Sum(nil)); got != suiteOracle[name] {
					t.Fatalf("output md5 %s, the suite's is %s", got, suiteOracle[name])
				}
			default:
				want := reference(t, filepath.Join("testdata", "hevc-conformance", name+".264"))
				if got := sums(frames); !slices.Equal(got, want) {
					t.Fatalf("%d pictures, ffmpeg has %d, or a picture differs", len(got), len(want))
				}
			}
		})
	}
}

func TestHEVCConformanceManifestsAreCommitted(t *testing.T) {
	m, err := filepath.Glob(filepath.Join("testdata", "hevc-conformance", "*.md5"))
	if err != nil || len(m) != 143 {
		t.Errorf("%d manifests, want the 143 streams on which ffmpeg and this decoder agree (%v)", len(m), err)
	}
	for name := range suiteOracle {
		if fileExists(filepath.Join("testdata", "hevc-conformance", name+".md5")) {
			t.Errorf("%s has a ffmpeg manifest, but ffmpeg is not its oracle", name)
		}
	}
}

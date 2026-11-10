// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// pendingUpstream lists the conformance bitstreams that fail with the pinned
// go.264 and pass with go-oops1/go.264 PR "decoder: handle MMCO 5 and gaps in
// frame_num". They are expected to fail here until that fix is released and
// go.mod is moved to it; the test then fails on the unexpected pass, so this
// list cannot go stale.
var pendingUpstream = map[string]bool{
	"MR2_TANDBERG_E": true,
	"MR3_TANDBERG_B": true,
	"MR4_TANDBERG_C": true,
	"MR5_TANDBERG_C": true,
}

// bitstreamIn returns the largest file of dir with a bitstream extension, which
// is the stream: some archives name it otherwise than the archive
// (cabac_mot_frm0_full.zip holds camp_mot_frm0_full.26l). "" when there is none.
func bitstreamIn(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best, size := "", int64(-1)
	for _, e := range entries {
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".264", ".jsv", ".jvt", ".avc", ".26l", ".h264", ".bit", ".bits":
			if info, err := e.Info(); err == nil && info.Size() > size {
				best, size = filepath.Join(dir, e.Name()), info.Size()
			}
		}
	}
	return best
}

// TestJVTConformance decodes ITU-T H.264.1 conformance bitstreams and compares
// every picture's MD5 with what ffmpeg -f framemd5 produced for it (the
// testdata/jvt/NAME.md5 files). The bitstreams are not in this repository: set
// DECODE_JVT_DIR to a directory holding NAME/NAME.264 (or .jsv, .jvt, ...) as
// `jvt fetch` unpacks them, from
// https://www.itu.int/wftp3/av-arch/jvt-site/draft_conformance/AVCv1/
func TestJVTConformance(t *testing.T) {
	dir := os.Getenv("DECODE_JVT_DIR")
	if dir == "" {
		t.Skip("DECODE_JVT_DIR is not set")
	}
	manifests, err := filepath.Glob(filepath.Join("testdata", "jvt", "*.md5"))
	if err != nil || len(manifests) == 0 {
		t.Fatalf("no manifests: %v", err)
	}
	for _, m := range manifests {
		name := strings.TrimSuffix(filepath.Base(m), ".md5")
		t.Run(name, func(t *testing.T) {
			stream := bitstreamIn(filepath.Join(dir, name))
			if stream == "" {
				t.Skipf("%s is not in %s", name, dir)
			}
			d, err := New(Config{MaxFrameMBs: 1 << 17}, 4)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			frames, err := d.DecodeAnnexB(readFile(t, stream))
			if err != nil {
				t.Fatal(err)
			}
			rest, err := d.Flush()
			if err != nil {
				t.Fatal(err)
			}
			got := sums(append(frames, rest...))
			want := reference(t, strings.TrimSuffix(m, ".md5")+".264")
			match := slices.Equal(got, want)
			switch {
			case pendingUpstream[name] && match:
				t.Fatalf("%s now matches: remove it from pendingUpstream", name)
			case pendingUpstream[name]:
				t.Skipf("%s: known failure with the pinned go.264, fixed upstream", name)
			case !match:
				t.Fatalf("%d pictures, ffmpeg has %d, or a picture differs", len(got), len(want))
			}
		})
	}
}

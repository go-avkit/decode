// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseFrameMD5(t *testing.T) {
	out := "#format: frame checksums\n#version: 2\n\n0, 0, 0, 1, 38016, aaaa\n0,  1,  1,   1, 38016, bbbb\n"
	got, err := parseFrameMD5([]byte(out))
	if err != nil || !slices.Equal(got, []string{"aaaa", "bbbb"}) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := parseFrameMD5([]byte("0, 1, 2\n")); err == nil {
		t.Fatal("a short line was accepted")
	}
}

func TestProfileOf(t *testing.T) {
	cases := map[string]struct {
		sps  []byte
		want string
	}{
		"cb":   {[]byte{0, 0, 1, 0x67, 66, 0xc0, 12}, "ConstrainedBaseline L1.2"},
		"main": {[]byte{0, 0, 0, 1, 0x67, 77, 0, 31}, "Main L3.1"},
		"odd":  {[]byte{0, 0, 1, 0x67, 55, 0, 10}, "profile55 L1.0"},
		"none": {[]byte{1, 2, 3}, "?"},
	}
	for name, c := range cases {
		if got := profileOf(c.sps); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

func TestUnzipAndFind(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"x/../../evil.264": "evil", "readme.txt": "r", "dir/": ""} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	dir := filepath.Join(t.TempDir(), "s")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unzip(buf.Bytes(), dir); err != nil {
		t.Fatal(err)
	}
	p, err := findBitstream(dir)
	if err != nil || filepath.Dir(p) != dir || filepath.Base(p) != "evil.264" {
		t.Fatalf("%q %v: an entry escaped or was missed", p, err)
	}
	if err := unzip([]byte("not a zip"), dir); err == nil {
		t.Fatal("garbage unzipped")
	}
	if _, err := findBitstream(t.TempDir()); err == nil {
		t.Fatal("empty dir has a bitstream")
	}
	if _, err := findBitstream(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing dir has a bitstream")
	}
}

func TestEqualAndManifest(t *testing.T) {
	if !equal([]string{"a"}, []string{"a"}) || equal([]string{"a"}, []string{"b"}) || equal([]string{"a"}, nil) {
		t.Fatal("equal")
	}
	p := filepath.Join(t.TempDir(), "m.md5")
	if err := writeManifest(p, []string{"x", "y"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), "# ffmpeg") || !strings.HasSuffix(string(b), "x\ny\n") {
		t.Fatalf("%q", b)
	}
}

// With ffmpeg installed the bits command must agree on a committed stream.
func TestBitsAgainstFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	var out bytes.Buffer
	cmd := RootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"bits", "../../testdata/streams/base_ip_qp26.264"})
	// bits writes its manifest beside the stream; it must reproduce the one
	// that is committed.
	before, _ := os.ReadFile("../../testdata/streams/base_ip_qp26.md5")
	if err := cmd.Execute(); err != nil {
		t.Fatal(err, out.String())
	}
	after, _ := os.ReadFile("../../testdata/streams/base_ip_qp26.md5")
	if !bytes.Equal(before, after) || !strings.Contains(out.String(), "10/10 | true") {
		t.Fatalf("%s", out.String())
	}
}

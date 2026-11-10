// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixtureTable(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range fixtures {
		if seen[f.Name] {
			t.Errorf("%s twice", f.Name)
		}
		seen[f.Name] = true
		if f.Fragment {
			if !seen[f.Src] {
				t.Errorf("%s: %s is not made before it", f.Name, f.Src)
			}
		} else if f.Src != "" {
			if _, ok := sources[f.Src]; !ok {
				t.Errorf("%s: no source %s", f.Name, f.Src)
			}
		}
	}
}

func TestArgs(t *testing.T) {
	got := strings.Join(fixture{Delay: "0.1", Seek: "1", To: "2"}.makeArgs("in", "out"), " ")
	for _, want := range []string{"-itsoffset 0.1", "-ss 1 -to 2", "-i in", "-c copy", "out"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lacks %q", got, want)
		}
	}
	if got := strings.Join(recordArgs("f", true), " "); !strings.Contains(got, "-ignore_editlist 1") || !strings.Contains(got, "-copyts") {
		t.Errorf("%q", got)
	}
	if got := strings.Join(recordArgs("f", false), " "); strings.Contains(got, "-ignore_editlist") {
		t.Errorf("%q", got)
	}
}

func TestRequiresFlags(t *testing.T) {
	cmd := RootCmd()
	cmd.SetArgs(nil)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("no flags accepted")
	}
}

// TestCheck asks the local ffmpeg for the recordings again and compares them with
// the committed ones, when it is the version they were made with: their header
// names the libavformat that wrote them.
func TestCheck(t *testing.T) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	out, err := exec.Command(path, "-version").Output()
	if err != nil || !strings.HasPrefix(string(out), "ffmpeg version 9.0.2 ") {
		t.Skip("not ffmpeg 9.0.2")
	}
	cmd := RootCmd()
	cmd.SetArgs([]string{"--check", "--out", filepath.Join("..", "..", "testdata", "elst"),
		"--files", filepath.Join("..", "..", "testdata", "files"), "--work", t.TempDir()})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

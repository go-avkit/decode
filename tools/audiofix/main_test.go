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
		if f.Rate <= 0 || f.Channels <= 0 || f.Seconds <= 0 || f.Seconds > 1 {
			t.Errorf("%s: %d Hz, %d channels, %g s", f.Name, f.Rate, f.Channels, f.Seconds)
		}
		if got := strings.Count(f.signal(), "|") + 1; got != f.Channels {
			t.Errorf("%s: a signal of %d channels, want %d", f.Name, got, f.Channels)
		}
		switch f.Decode {
		case "s16le", "s32le", "f32le":
		default:
			t.Errorf("%s: reference format %q", f.Name, f.Decode)
		}
	}
}

func TestRequiresOut(t *testing.T) {
	cmd := RootCmd()
	cmd.SetArgs(nil)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("no -out accepted")
	}
}

// TestRoundTrip writes the fixtures with the local ffmpeg and checks them again.
func TestRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := filepath.Join(t.TempDir(), "audio")
	for _, check := range []bool{false, true} {
		cmd := RootCmd()
		args := []string{"--out", dir}
		if check {
			args = append(args, "--check")
		}
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("check=%v: %v", check, err)
		}
	}
}

func TestSeeds(t *testing.T) {
	cmd := RootCmd()
	cmd.SetArgs([]string{"seeds"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("seeds without flags accepted")
	}
	out := t.TempDir()
	cmd = RootCmd()
	cmd.SetArgs([]string{"seeds", "--fixtures", filepath.Join("..", "..", "testdata", "audio"), "--out", out})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"FuzzAudioTrack", "FuzzFLACSample", "FuzzFLACConfig", "FuzzMP3Sample"} {
		if got, _ := filepath.Glob(filepath.Join(out, target, "seed-*")); len(got) < 4 {
			t.Errorf("%s: %d seeds (%s)", target, len(got), stdout.String())
		}
	}
}

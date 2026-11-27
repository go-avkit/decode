// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	h1 = "1111111111111111111111111111111111111111111111111111111111111111"
	h2 = "2222222222222222222222222222222222222222222222222222222222222222"
	h3 = "3333333333333333333333333333333333333333333333333333333333333333"
	h4 = "4444444444444444444444444444444444444444444444444444444444444444"
)

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := RootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestMismatchLogKeepsOrderAndPassedStreams(t *testing.T) {
	dir := t.TempDir()
	golden := filepath.Join(dir, "amd64.txt")
	if err := os.WriteFile(golden, []byte("S01 "+h4+"\nS02 "+h4+"\nS03 "+h4+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "lane.log")
	text := "2026 --- FAIL: TestPSOutputGolden (0.14s)\n" +
		"2026     aac_ps_golden_test.go:166: S03 (route amd64): output SHA-256 " + h1 + ", recorded \"" + h4 + "\"\n" +
		"2026     aac_ps_golden_test.go:166: S01 (route amd64): output SHA-256 " + h2 + ", recorded \"" + h4 + "\"\n" +
		"2026     aac_ps_golden_test.go:166: S09 (route amd64): output SHA-256 " + h3 + ", recorded \"\"\n" +
		"2026     aac_ps_golden_test.go:166: S02 (route 386): output SHA-256 " + h3 + ", recorded \"" + h4 + "\"\n"
	if err := os.WriteFile(log, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "--route", "amd64", "--log", log, "--golden", golden); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(golden)
	want := "S01 " + h2 + "\nS02 " + h4 + "\nS03 " + h1 + "\nS09 " + h3 + "\n"
	if string(got) != want {
		t.Errorf("golden:\n%s\nwant:\n%s", got, want)
	}
}

func TestMissingFileLog(t *testing.T) {
	dir := t.TempDir()
	golden := filepath.Join(dir, "s390x.txt")
	log := filepath.Join(dir, "lane.log")
	text := "x PSGOLDEN s390x S01 " + h1 + "\nx PSGOLDEN s390x S02 " + h2 + "\nx PSGOLDEN riscv64 S01 " + h3 + "\n"
	if err := os.WriteFile(log, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, "--route", "s390x", "--log", log, "--golden", golden)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2 stream(s)") {
		t.Errorf("output %q", out)
	}
	got, _ := os.ReadFile(golden)
	if string(got) != "S01 "+h1+"\nS02 "+h2+"\n" {
		t.Errorf("golden %q", got)
	}
}

func TestLogWithoutTheRouteIsAnError(t *testing.T) {
	dir := t.TempDir()
	golden := filepath.Join(dir, "amd64.txt")
	if err := os.WriteFile(golden, []byte("S01 "+h4+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "lane.log")
	if err := os.WriteFile(log, []byte("S01 (route 386): output SHA-256 "+h1+", recorded \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, "--route", "amd64", "--log", log, "--golden", golden); err == nil {
		t.Fatal("a log with no stream of the route was accepted")
	}
	got, _ := os.ReadFile(golden)
	if string(got) != "S01 "+h4+"\n" {
		t.Errorf("the golden file was touched: %q", got)
	}
}

func TestFlagsRequiredAndMissingLog(t *testing.T) {
	if _, err := run(t, "--route", "amd64"); err == nil {
		t.Error("missing flags accepted")
	}
	if _, err := run(t, "--route", "amd64", "--log", filepath.Join(t.TempDir(), "none"), "--golden", "x"); err == nil {
		t.Error("a missing log accepted")
	}
}

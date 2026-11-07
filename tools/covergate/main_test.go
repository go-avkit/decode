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

const full = "mode: set\na.go:1.1,2.2 3 1\nb.go:4.1,5.2 2 7\n"

func TestUncovered(t *testing.T) {
	missed, total, err := Uncovered(strings.NewReader(full))
	if err != nil || len(missed) != 0 || total != 5 {
		t.Fatalf("full profile: %v %d %v", missed, total, err)
	}
	// 1 statement of 1000 not run is 99.9%: the rounded print would say
	// 100.0%, this must not.
	missed, _, err = Uncovered(strings.NewReader(full + "c.go:9.1,9.9 1 0\nd.go:1.1,1.2 0 0\n"))
	if err != nil || len(missed) != 1 || missed[0] != "c.go:9.1,9.9" {
		t.Fatalf("missed %v err %v", missed, err)
	}
	for _, bad := range []string{"", "nonsense\n", "mode: set\nonly two\n", "mode: set\na.go:1.1,2.2 x 1\n", "mode: set\n"} {
		if _, _, err := Uncovered(strings.NewReader(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRootCmd(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run := func(args ...string) (string, string, error) {
		var out, errb bytes.Buffer
		cmd := RootCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&errb)
		err := cmd.Execute()
		return out.String(), errb.String(), err
	}
	if out, _, err := run(write("ok.out", full)); err != nil || !strings.Contains(out, "0 of 5") {
		t.Fatalf("ok: %q %v", out, err)
	}
	if _, errs, err := run(write("bad.out", full+"c.go:9.1,9.9 1 0\n")); err == nil || !strings.Contains(errs, "c.go:9.1,9.9") {
		t.Fatalf("bad: %q %v", errs, err)
	}
	if _, _, err := run(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing profile passed")
	}
	if _, _, err := run(write("junk.out", "junk\n")); err == nil {
		t.Fatal("junk passed")
	}
}

func TestMain(t *testing.T) {
	var code int
	old := osExit
	osExit = func(c int) { code = c }
	defer func() { osExit = old }()
	os.Args = []string{"covergate"}
	main()
	if code != 1 {
		t.Fatalf("exit code %d", code)
	}
}

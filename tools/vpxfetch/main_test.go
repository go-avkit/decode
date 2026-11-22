// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const sampleCC = `const char *const kVP8TestVectors[] = {
  "vp80-00-comprehensive-001.ivf", "vp80-00-comprehensive-002.ivf",
};
  "vp90-2-00-quantizer-00.webm", "vp90-2-00-quantizer-00.webm",
  "vp91-2-04-yuv444.webm", "notvpx-1.webm", "vp90-2-x.mkv",
`

func TestNames(t *testing.T) {
	got := Names(sampleCC)
	want := []string{"vp80-00-comprehensive-001.ivf", "vp80-00-comprehensive-002.ivf", "vp90-2-00-quantizer-00.webm", "vp91-2-04-yuv444.webm"}
	if !slices.Equal(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := RootCmd()
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

func TestFetch(t *testing.T) {
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		if strings.Contains(r.URL.Path, "yuv444") && strings.HasSuffix(r.URL.Path, ".md5") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("body of " + r.URL.Path))
	}))
	defer srv.Close()
	dir := t.TempDir()
	cc := filepath.Join(dir, "test_vectors.cc")
	if err := os.WriteFile(cc, []byte(sampleCC), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out")
	out, err := run(t, "--cc", cc, "--dir", dst, "--base", srv.URL)
	if err == nil || !strings.Contains(err.Error(), "vp91-2-04-yuv444.webm.md5: HTTP 404") {
		t.Fatalf("a missing md5 gave %v, want its HTTP 404", err)
	}
	if !strings.Contains(out, "4 vectors") || !strings.Contains(out, "1 failures") {
		t.Errorf("summary %q", out)
	}
	got, err := os.ReadFile(filepath.Join(dst, "vp80-00-comprehensive-001.ivf.md5"))
	if err != nil || string(got) != "body of /vp80-00-comprehensive-001.ivf.md5" {
		t.Errorf("md5 file: %q, %v", got, err)
	}
	if left, _ := filepath.Glob(filepath.Join(dst, "*.part*")); len(left) != 0 {
		t.Errorf("partial files left: %v", left)
	}
	// A second run downloads only what is missing.
	before := hits["/vp80-00-comprehensive-001.ivf"]
	run(t, "--cc", cc, "--dir", dst, "--base", srv.URL+"/")
	if hits["/vp80-00-comprehensive-001.ivf"] != before {
		t.Errorf("a file already in --dir was downloaded again")
	}
}

func TestFetchRefusals(t *testing.T) {
	if _, err := run(t); err == nil {
		t.Error("no flags: want an error")
	}
	if _, err := run(t, "--cc", filepath.Join(t.TempDir(), "absent.cc"), "--dir", t.TempDir()); err == nil {
		t.Error("absent --cc: want an error")
	}
	empty := filepath.Join(t.TempDir(), "e.cc")
	os.WriteFile(empty, []byte("nothing"), 0o644)
	if _, err := run(t, "--cc", empty, "--dir", t.TempDir()); err == nil || !strings.Contains(err.Error(), "lists no") {
		t.Errorf("empty list: %v", err)
	}
}

func TestFetchRefusalsAreNamed(t *testing.T) {
	cc := filepath.Join(t.TempDir(), "v.cc")
	os.WriteFile(cc, []byte(sampleCC), 0o644)
	for _, args := range [][]string{{"--cc", cc}, {"--dir", t.TempDir()}} {
		if _, err := run(t, args...); err == nil || !strings.Contains(err.Error(), "are required") {
			t.Errorf("%v: %v, want the missing flag named", args, err)
		}
	}
	// ⛔ Judged by the *fs.PathError's fields, not its text: the text is the
	// operating system's ("no such file or directory" on Unix, "The system
	// cannot find the file specified." on Windows), and matching the Unix
	// wording turned windows-latest red on decode#121.
	absent := filepath.Join(t.TempDir(), "absent.cc")
	_, err := run(t, "--cc", absent, "--dir", t.TempDir())
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Op != "open" || pe.Path != absent || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("absent --cc: %v, want the read error naming %s", err, absent)
	}
	file := filepath.Join(t.TempDir(), "afile")
	os.WriteFile(file, nil, 0o644)
	_, err = run(t, "--cc", cc, "--dir", filepath.Join(file, "sub"), "--base", "http://127.0.0.1:1/")
	// MkdirAll names the ancestor that is in the way, on every system.
	if !errors.As(err, &pe) || pe.Op != "mkdir" || pe.Path != file {
		t.Errorf("--dir under a file: %v, want the mkdir error naming %s", err, file)
	}
}

func TestFetchFailures(t *testing.T) {
	// A server that promises more than it sends.
	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("only a few bytes"))
	}))
	defer short.Close()
	dir := t.TempDir()
	if err := fetch(http.DefaultClient, short.URL+"/", dir, "a.ivf"); err == nil || !strings.Contains(err.Error(), "a.ivf") {
		t.Errorf("a truncated body gave %v, want an error naming the file", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "a.ivf*")); len(left) != 0 {
		t.Errorf("a truncated download left %v", left)
	}
	// A directory that is not there: the temporary file cannot be created.
	missing := filepath.Join(t.TempDir(), "missing")
	if err := fetch(http.DefaultClient, short.URL+"/", missing, "a.ivf"); err == nil {
		t.Error("an absent directory: want an error")
	}
	// A server that is gone.
	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL + "/"
	gone.Close()
	if err := fetch(http.DefaultClient, url, t.TempDir(), "a.ivf"); err == nil || strings.Contains(err.Error(), "HTTP") {
		t.Errorf("an unreachable server gave %v, want a connection error", err)
	}
}

func TestMainExits(t *testing.T) {
	old := osExit
	defer func() { osExit = old }()
	code := -1
	osExit = func(c int) { code = c }
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"vpxfetch"}
	main()
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
}

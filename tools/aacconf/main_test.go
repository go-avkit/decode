package main

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// server serves one zip, "arch.zip", with byte ranges, and ISO's MD5 list.
func server(t *testing.T, members map[string][]byte) (*httptest.Server, string) {
	t.Helper()
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	var list strings.Builder
	for name, b := range members {
		w, _ := zw.Create("arch/" + name)
		w.Write(b)
		m := md5.Sum(b)
		fmt.Fprintf(&list, "%s  ./arch/%s\n", hex.EncodeToString(m[:]), name)
	}
	zw.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/arch.zip", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "arch.zip", time.Time{}, bytes.NewReader(zb.Bytes()))
	})
	mux.HandleFunc("/"+md5List, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, list.String()) })
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, s.URL + "/"
}

func TestPinThenFetch(t *testing.T) {
	data := bytes.Repeat([]byte("conformance"), 200000) // beyond one block of 1 MiB
	_, base := server(t, map[string][]byte{"a.bin": data, "b.bin": []byte("b")})
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"-base", base, "pin", dir, "arch/a.bin", "arch/b.bin"}, &out, &errb); code != 0 {
		t.Fatalf("pin: %d %s", code, errb.String())
	}
	pins := filepath.Join(t.TempDir(), "pins")
	os.WriteFile(pins, []byte("# comment\n\n"+out.String()), 0o644)
	dir2 := t.TempDir()
	out.Reset()
	if code := run([]string{"-base", base, "fetch", dir2, pins}, &out, &errb); code != 0 || strings.Count(out.String(), "fetched") != 2 {
		t.Fatalf("fetch: %d %q %s", code, out.String(), errb.String())
	}
	if b, _ := os.ReadFile(filepath.Join(dir2, "arch", "a.bin")); !bytes.Equal(b, data) {
		t.Fatal("content differs")
	}
	out.Reset()
	if code := run([]string{"-base", base, "fetch", dir2, pins}, &out, &errb); code != 0 || strings.Count(out.String(), "have") != 2 {
		t.Fatalf("second fetch: %d %q", code, out.String())
	}
}

func TestRefusals(t *testing.T) {
	_, base := server(t, map[string][]byte{"a.bin": []byte("a")})
	dir := t.TempDir()
	bad := filepath.Join(t.TempDir(), "pins")
	os.WriteFile(bad, []byte(strings.Repeat("0", 64)+"  arch/a.bin\n"), 0o644)
	for name, args := range map[string][]string{
		"no command":        {},
		"unknown command":   {"-base", base, "x", dir},
		"a hash mismatch":   {"-base", base, "fetch", dir, bad},
		"missing pins file": {"-base", base, "fetch", dir, filepath.Join(dir, "none")},
		"fetch arguments":   {"-base", base, "fetch", dir},
		"pin arguments":     {"-base", base, "pin", dir},
		"a missing member":  {"-base", base, "pin", dir, "arch/none.bin"},
		"no archive":        {"-base", base, "pin", dir, "none"},
		"an unsafe path":    {"-base", base, "pin", dir, "arch/../x"},
		"no such archive":   {"-base", base, "pin", dir, "other/a.bin"},
		"a bad flag":        {"-nope"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("%s: exit %d", name, code)
		}
	}
	for name, line := range map[string]string{"short hash": "abc  arch/a.bin", "unsafe path": strings.Repeat("0", 64) + "  ../a", "one field": "x"} {
		p := filepath.Join(t.TempDir(), "pins")
		os.WriteFile(p, []byte(line+"\n"), 0o644)
		if _, _, err := readPins(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// ISO's list disagrees with the member
	_, base2 := server(t, map[string][]byte{"a.bin": []byte("a")})
	bs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".md5") {
			fmt.Fprint(w, "00000000000000000000000000000000  ./arch/a.bin\n")
			return
		}
		http.Redirect(w, r, base2+strings.TrimPrefix(r.URL.Path, "/"), http.StatusFound)
	}))
	defer bs.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"-base", bs.URL + "/", "pin", dir, "arch/a.bin"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "MD5") {
		t.Errorf("an MD5 that ISO's list does not give: %d %s", code, errb.String())
	}
}

func TestMain(m *testing.M) {
	osExit = func(int) {}
	main() // usage, no arguments: exits through osExit
	osExit = os.Exit
	os.Exit(m.Run())
}

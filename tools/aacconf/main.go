// Command aacconf fetches the ISO/IEC 14496-26 audio conformance streams that
// decode's AAC test needs, into a directory outside any repository, and checks
// them against their SHA-256.
//
// ISO publishes the streams only inside very large archives (the reference
// waveforms alone are 9 GB), so a member is read out of the remote archive with
// HTTP range requests, never the whole archive. The streams and waveforms are
// electronic inserts that ISO lets anyone use "in their original format without
// any modifications": they are never committed, only fetched, and only their
// hashes are in this repository (testdata/aac-conformance.txt).
//
//	aacconf fetch DIR PINS         download what PINS lists and verify every hash
//	aacconf pin DIR PATH...        download PATH, check its MD5 against ISO's own list
//	                               and print the pin lines
//
// A PATH is as ISO's MD5 list writes it, for example compressedMp4/al02_48.mp4: its
// first element names the archive (compressedMp4.zip) under the base URL, -base.
package main

import (
	"archive/zip"
	"bufio"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBase = "https://standards.iso.org/iso-iec/14496/-26/ed-2/en/"
	md5List     = "mpeg4audio-conformance2ndEd_20230125.md5"
	maxMember   = 256 << 20 // no vector is larger; a member that says more is refused
	blockSize   = 1 << 20
)

var osExit = os.Exit

func main() { osExit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("aacconf", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", defaultBase, "base URL of the conformance directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) < 2 {
		fmt.Fprintln(stderr, "usage: aacconf [-base URL] fetch DIR PINS | pin DIR PATH...")
		return 2
	}
	var err error
	switch rest[0] {
	case "fetch":
		if len(rest) != 3 {
			err = errors.New("usage: aacconf fetch DIR PINS")
			break
		}
		err = fetch(*base, rest[1], rest[2], stdout)
	case "pin":
		if len(rest) < 3 {
			err = errors.New("usage: aacconf pin DIR PATH...")
			break
		}
		err = pin(*base, rest[1], rest[2:], stdout)
	default:
		err = fmt.Errorf("unknown command %q", rest[0])
	}
	if err != nil {
		fmt.Fprintln(stderr, "aacconf:", err)
		return 2
	}
	return 0
}

// get downloads one small URL whole.
func get(url string, limit int64) ([]byte, error) {
	c := &http.Client{Timeout: 5 * time.Minute}
	r, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, r.Status)
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: more than %d bytes", url, limit)
	}
	return b, nil
}

// rangeReader is an io.ReaderAt over a URL that serves byte ranges. It keeps the
// last block it read, because a zip reader asks for a few bytes at a time.
type rangeReader struct {
	url   string
	size  int64
	c     *http.Client
	off   int64
	block []byte
}

func newRangeReader(url string) (*rangeReader, error) {
	c := &http.Client{Timeout: 5 * time.Minute}
	r, err := c.Head(url)
	if err != nil {
		return nil, err
	}
	r.Body.Close()
	if r.StatusCode != http.StatusOK || r.Header.Get("Accept-Ranges") != "bytes" {
		return nil, fmt.Errorf("%s: %s, ranges %q", url, r.Status, r.Header.Get("Accept-Ranges"))
	}
	n, err := strconv.ParseInt(r.Header.Get("Content-Length"), 10, 64)
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("%s: no length", url)
	}
	return &rangeReader{url: url, size: n, c: c}, nil
}

func (r *rangeReader) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	for n < len(p) {
		pos := off + int64(n)
		if pos >= r.size {
			return n, io.EOF
		}
		if pos < r.off || pos >= r.off+int64(len(r.block)) {
			start := pos / blockSize * blockSize
			end := min(start+blockSize, r.size)
			req, err := http.NewRequest(http.MethodGet, r.url, nil)
			if err != nil {
				return n, err
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end-1))
			resp, err := r.c.Do(req)
			if err != nil {
				return n, err
			}
			b, err := io.ReadAll(io.LimitReader(resp.Body, end-start+1))
			resp.Body.Close()
			if err != nil {
				return n, err
			}
			if resp.StatusCode != http.StatusPartialContent || int64(len(b)) != end-start {
				return n, fmt.Errorf("%s: range %d-%d answered %s with %d bytes", r.url, start, end-1, resp.Status, len(b))
			}
			r.off, r.block = start, b
		}
		n += copy(p[n:], r.block[pos-r.off:])
	}
	return n, nil
}

// member reads one file of a remote archive: rel's first element names the
// archive, the rest the member.
func member(base, rel string) ([]byte, error) {
	arch, name, ok := strings.Cut(rel, "/")
	if !ok {
		return nil, fmt.Errorf("%q has no archive element", rel)
	}
	rr, err := newRangeReader(base + arch + ".zip")
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(rr, rr.size)
	if err != nil {
		return nil, fmt.Errorf("%s.zip: %w", arch, err)
	}
	for _, f := range zr.File {
		if f.Name == rel || f.Name == name || strings.HasSuffix(f.Name, "/"+name) {
			if f.UncompressedSize64 > maxMember {
				return nil, fmt.Errorf("%s: %d bytes", f.Name, f.UncompressedSize64)
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxMember+1))
		}
	}
	return nil, fmt.Errorf("%s.zip has no member %s", arch, name)
}

// safeRel refuses a path that leaves the directory it is joined to.
func safeRel(p string) error {
	if p == "" || filepath.IsAbs(p) || strings.Contains(p, "..") || strings.Contains(p, `\`) {
		return fmt.Errorf("unsafe path %q", p)
	}
	return nil
}

// readPins reads "sha256  path" lines; # starts a comment.
func readPins(path string) (map[string]string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	pins := map[string]string{}
	var order []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			return nil, nil, fmt.Errorf("%s: bad line %q", path, line)
		}
		if err := safeRel(fields[1]); err != nil {
			return nil, nil, err
		}
		pins[fields[1]] = fields[0]
		order = append(order, fields[1])
	}
	return pins, order, sc.Err()
}

func sum256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func store(dir, rel string, b []byte) error {
	dst := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func fetch(base, dir, pinsFile string, out io.Writer) error {
	pins, order, err := readPins(pinsFile)
	if err != nil {
		return err
	}
	for _, rel := range order {
		if b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))); err == nil && sum256(b) == pins[rel] {
			fmt.Fprintf(out, "have %s\n", rel)
			continue
		}
		b, err := member(base, rel)
		if err != nil {
			return err
		}
		if got := sum256(b); got != pins[rel] {
			return fmt.Errorf("%s: SHA-256 %s, the pin is %s", rel, got, pins[rel])
		}
		if err := store(dir, rel, b); err != nil {
			return err
		}
		fmt.Fprintf(out, "fetched %s (%d bytes)\n", rel, len(b))
	}
	return nil
}

func pin(base, dir string, paths []string, out io.Writer) error {
	list, err := get(base+md5List, 8<<20)
	if err != nil {
		return err
	}
	want := map[string]string{}
	for _, line := range strings.Split(string(list), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			want[strings.TrimPrefix(f[1], "./")] = f[0]
		}
	}
	for _, rel := range paths {
		if err := safeRel(rel); err != nil {
			return err
		}
		b, err := member(base, rel)
		if err != nil {
			return err
		}
		m := md5.Sum(b)
		if got := hex.EncodeToString(m[:]); want[rel] == "" || got != want[rel] {
			return fmt.Errorf("%s: MD5 %s, ISO's list says %q", rel, got, want[rel])
		}
		if err := store(dir, rel, b); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s  %s\n", sum256(b), rel)
	}
	return nil
}

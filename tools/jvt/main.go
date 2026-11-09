// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

// Command jvt measures the decoder against ffmpeg.
//
//	jvt fetch   -dir D NAME...        download and unpack JVT conformance streams (ITU-T H.264.1)
//	jvt compare -dir D -manifests M NAME...
//	                                   decode each stream, compare every picture's MD5 with ffmpeg's
//	                                   framemd5, write M/NAME.md5 and print the table
//	jvt file    PATH                   the same for a real MP4/MKV file; prints hashes only
//
// Nothing here is part of the library: it is the oracle harness, kept apart
// (its own module) so the library's dependencies and coverage gate stay small.
package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-avkit/avkit/container"
	"github.com/go-avkit/decode"
	"github.com/spf13/cobra"
)

// codecSpec is what differs between the codecs the harness measures.
type codecSpec struct {
	name      string
	baseURL   string          // the conformance suite's directory
	demuxer   string          // ffmpeg's name for a bare bitstream of this codec
	pixFmt    string          // forced -pix_fmt for ffmpeg; empty keeps the native one
	extension map[string]bool // bitstream file extensions
	newRaw    func(decode.Config, int) (*decode.Decoder, error)
	profile   func([]byte) string
}

var h264Spec = codecSpec{
	name:      "h264",
	baseURL:   "https://www.itu.int/wftp3/av-arch/jvt-site/draft_conformance/AVCv1/",
	demuxer:   "h264",
	pixFmt:    "yuv420p",
	extension: map[string]bool{".264": true, ".jvt": true, ".avc": true, ".26l": true, ".h264": true, ".jsv": true, ".bit": true, ".bits": true},
	newRaw:    decode.New,
	profile:   profileOf,
}

// hevcSpec: the JCT-VC conformance suite (ITU-T H.265.1), and the native pixel
// format, so that a Main 10 stream is compared as yuv420p10le.
var hevcSpec = codecSpec{
	name:      "hevc",
	baseURL:   "https://www.itu.int/wftp3/av-arch/jctvc-site/bitstream_exchange/draft_conformance/HEVC_v1/",
	demuxer:   "hevc",
	extension: map[string]bool{".bit": true, ".bin": true, ".bitstream": true, ".hevc": true, ".h265": true, ".265": true},
	newRaw:    decode.NewHEVC,
	profile:   profileOfHEVC,
}

// spec is the codec of this run, chosen by --codec.
var spec = h264Spec

var osExit = os.Exit

const (
	maxZip     = 300 << 20
	maxExtract = 1 << 30
)

func main() {
	if err := RootCmd().Execute(); err != nil {
		osExit(1)
	}
}

// RootCmd builds the command tree.
func RootCmd() *cobra.Command {
	root := &cobra.Command{Use: "jvt", Short: "measure the H.264 and HEVC decoders against ffmpeg", SilenceUsage: true}
	var dir, manifests, codec string
	root.PersistentFlags().StringVar(&codec, "codec", "h264", "h264 or hevc")
	root.PersistentPreRunE = func(*cobra.Command, []string) error {
		switch codec {
		case "h264":
			spec = h264Spec
		case "hevc":
			spec = hevcSpec
		default:
			return fmt.Errorf("unknown codec %q, want h264 or hevc", codec)
		}
		return nil
	}
	var all bool
	fetch := &cobra.Command{
		Use: "fetch [NAME...]", Short: "download and unpack conformance streams (--all: the whole suite)", Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			names, err := streamNames(all, args)
			if err != nil {
				return err
			}
			failed := 0
			for _, n := range names {
				if _, err := fetchStream(dir, n); err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "FAILED %s: %v\n", n, err)
					failed++
					continue
				}
				fmt.Fprintln(cmd.OutOrStdout(), "fetched", n)
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d streams could not be fetched", failed, len(names))
			}
			return nil
		},
	}
	compare := &cobra.Command{
		Use: "compare [NAME...]", Short: "decode and compare with ffmpeg's framemd5 (--all: the whole suite)", Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			names, err := streamNames(all, args)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "bitstream | profile | frames(ours/ffmpeg) | match")
			bad := 0
			for _, n := range names {
				r := compareStream(dir, manifests, n)
				fmt.Fprintln(out, r.row())
				if !r.Match {
					bad++
				}
			}
			if bad > 0 {
				return fmt.Errorf("%d of %d bitstreams do not match", bad, len(names))
			}
			return nil
		},
	}
	for _, c := range []*cobra.Command{fetch, compare} {
		c.Flags().StringVar(&dir, "dir", "", "durable directory for downloaded streams")
		c.Flags().BoolVar(&all, "all", false, "every stream the codec's conformance suite lists")
		_ = c.MarkFlagRequired("dir")
	}
	compare.Flags().StringVar(&manifests, "manifests", "", "directory to write NAME.md5 into (empty: do not write)")
	var clipFrames int
	var clipDir string
	file := &cobra.Command{
		Use: "file PATH|@CENSUS...", Short: "compare real MP4/MKV files; prints hashes only (@CENSUS: the paths of a census list)", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := expandPaths(args)
			if err != nil {
				return err
			}
			var failed error
			for i, p := range paths {
				label := filepath.Base(p)
				if clipFrames > 0 {
					// The first N packets only, cut into clipDir, and the file left unnamed.
					label = fmt.Sprintf("file %d", i+1)
					dst := filepath.Join(clipDir, fmt.Sprintf("real-%d%s", i+1, filepath.Ext(p)))
					if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", p, "-map", "0:v:0",
						"-c", "copy", "-frames:v", strconv.Itoa(clipFrames), "-an", "-sn", dst).CombinedOutput(); err != nil {
						return fmt.Errorf("clip %d: %w: %.200s", i+1, err, out)
					}
					p = dst
				}
				r, err := compareFile(p)
				fmt.Fprintf(cmd.OutOrStdout(), "%s | %s | frames %d/%d | match %v | md5-of-md5s %s | ours %.1fs | %v\n",
					label, r.Profile, r.Ours, r.Ref, r.Match, r.Digest, r.Seconds, err)
				if err != nil || !r.Match {
					failed = errors.New("a file did not match")
				}
			}
			return failed
		},
	}
	file.Flags().IntVar(&clipFrames, "frames", 0, "decode only the first N video packets (cut with ffmpeg, stream copy) and do not name the file")
	file.Flags().StringVar(&clipDir, "clipdir", "", "durable directory for the clips (with --frames)")
	bits := &cobra.Command{
		Use: "bits PATH...", Short: "compare raw bitstream files; writes PATH-without-extension.md5 beside each", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, paths []string) error {
			bad := 0
			for _, p := range paths {
				name := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
				r := compareBitstream(result{Name: name, Profile: "?"}, p, filepath.Dir(p))
				fmt.Fprintln(cmd.OutOrStdout(), r.row())
				if !r.Match {
					bad++
				}
			}
			if bad > 0 {
				return fmt.Errorf("%d of %d bitstreams do not match", bad, len(paths))
			}
			return nil
		},
	}
	root.AddCommand(fetch, compare, file, bits, diffCmd(), clipCmd(), seedsCmd(), censusCmd())
	return root
}

// fetchStream downloads NAME.zip into dir/NAME and unpacks it, returning the
// bitstream's path. It is a no-op when the bitstream is already there.
func fetchStream(dir, name string) (string, error) {
	dst := filepath.Join(dir, name)
	if p, err := findBitstream(dst); err == nil {
		return p, nil
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.baseURL+name+".zip", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxZip+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxZip {
		return "", errors.New("zip larger than the bound")
	}
	if err := unzip(body, dst); err != nil {
		return "", err
	}
	return findBitstream(dst)
}

// unzip extracts a flat archive into dst, by base name (so no entry can leave
// dst), within a total size bound.
func unzip(body []byte, dst string) error {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return err
	}
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxExtract-total+1))
		rc.Close()
		if err != nil {
			return err
		}
		if total += int64(len(data)); total > maxExtract {
			return errors.New("archive expands past the bound")
		}
		if err := os.WriteFile(filepath.Join(dst, filepath.Base(f.Name)), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// findBitstream returns the H.264 bitstream in dir: the largest file with a
// bitstream extension.
func findBitstream(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	best, size := "", int64(-1)
	for _, e := range entries {
		if !spec.extension[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		if info, err := e.Info(); err == nil && info.Size() > size {
			best, size = filepath.Join(dir, e.Name()), info.Size()
		}
	}
	if best == "" {
		return "", fmt.Errorf("no bitstream in %s", dir)
	}
	return best, nil
}

// result is one row of the table.
type result struct {
	Name, Profile string
	Ours, Ref     int
	Match         bool
	Err           string
	Digest        string
	Seconds       float64 // decode time of our decoder, wall clock
}

func (r result) row() string {
	s := fmt.Sprintf("%s | %s | %d/%d | %v", r.Name, r.Profile, r.Ours, r.Ref, r.Match)
	if r.Err != "" {
		s += " | " + r.Err
	}
	return s
}

func compareStream(dir, manifests, name string) result {
	r := result{Name: name, Profile: "?"}
	path, err := fetchStream(dir, name)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	return compareBitstream(r, path, manifests)
}

// compareBitstream decodes the raw bitstream at path and compares it with ffmpeg.
func compareBitstream(r result, path, manifests string) result {
	data, err := os.ReadFile(path)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	r.Profile = spec.profile(data)
	ref, err := ffmpegMD5s(path, true)
	if err != nil {
		r.Err = "ffmpeg: " + err.Error()
		return r
	}
	r.Ref = len(ref)
	ours, size, whole, err := decodeAnnexBFull(data)
	r.Ours = len(ours)
	if err != nil {
		r.Err = err.Error()
	}
	r.Match = err == nil && equal(ours, ref)
	if err == nil && !r.Match {
		r.Err = fmt.Sprintf("first difference at picture %d", firstDiff(ours, ref))
	}
	// The JCT-VC suite ships one MD5 of the whole output beside the bitstream.
	if want := suiteMD5(path); want != "" {
		r.Err += fmt.Sprintf(" [suite md5 equal %v]", want == whole)
	}
	if y := refYUV(path); y != "" {
		if yref, yerr := yuvMD5s(y, size); yerr == nil {
			r.Err += fmt.Sprintf(" [conformance yuv: %d pictures, ours equal %v, ffmpeg equal %v]",
				len(yref), equal(ours, yref), equal(ref, yref))
		}
	}
	if r.Match && manifests != "" {
		if err := writeManifest(filepath.Join(manifests, r.Name+".md5"), ref); err != nil {
			r.Err = err.Error()
		}
	}
	return r
}

func compareFile(path string) (result, error) {
	r := result{Name: filepath.Base(path)}
	data, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	rd, err := container.NewReader(data)
	if err != nil {
		return r, err
	}
	var id uint32
	for _, t := range rd.File().Tracks {
		if t.Kind == container.Video {
			id = t.ID
			break
		}
	}
	start := time.Now()
	frames, err := decode.DecodeTrack(rd, id, decode.Config{MaxFrameMBs: 1 << 17})
	r.Seconds = time.Since(start).Seconds()
	for _, t := range rd.File().Tracks {
		if t.ID == id {
			r.Profile = fmt.Sprintf("%s %dx%d", t.Codec, t.Width, t.Height)
		}
	}
	ours := make([]string, len(frames))
	for i, f := range frames {
		ours[i] = frameMD5(f)
	}
	r.Ours = len(ours)
	if err != nil {
		return r, err
	}
	ref, err := ffmpegMD5s(path, false)
	if err != nil {
		return r, err
	}
	r.Ref = len(ref)
	r.Match = equal(ours, ref)
	sum := md5.Sum([]byte(strings.Join(ours, "\n")))
	r.Digest = hex.EncodeToString(sum[:])
	return r, nil
}

// firstDiff is the index of the first picture where a and b differ, or the
// length of the shorter when one is a prefix of the other.
func firstDiff(a, b []string) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func frameMD5(f decode.Frame) string {
	h := md5.New()
	h.Write(f.Y)
	h.Write(f.Cb)
	h.Write(f.Cr)
	return hex.EncodeToString(h.Sum(nil))
}

// decodeAnnexB decodes a whole raw stream, returning each picture's MD5 in
// display order. Pictures decoded before an error are kept.
func decodeAnnexB(data []byte) ([]string, error) {
	out, _, err := decodeAnnexBSize(data)
	return out, err
}

// decodeAnnexBSize also returns the byte size of one picture.
func decodeAnnexBSize(data []byte) (out []string, size int, err error) {
	out, size, _, err = decodeAnnexBFull(data)
	return out, size, err
}

// decodeAnnexBFull also returns the MD5 of every picture's planes, in display
// order, as one stream: what a conformance suite's own md5 file holds.
func decodeAnnexBFull(data []byte) (out []string, size int, whole string, err error) {
	d, err := spec.newRaw(decode.Config{MaxFrameMBs: 1 << 17}, 4)
	if err != nil {
		return nil, 0, "", err
	}
	defer d.Close()
	all := md5.New()
	add := func(fs []decode.Frame) {
		for _, f := range fs {
			out = append(out, frameMD5(f))
			size = len(f.Y) + len(f.Cb) + len(f.Cr)
			all.Write(f.Y)
			all.Write(f.Cb)
			all.Write(f.Cr)
		}
	}
	const chunk = 1 << 20
	for len(data) > 0 {
		n := min(chunk, len(data))
		fs, err := d.DecodeAnnexB(data[:n])
		add(fs)
		if err != nil {
			return out, size, hex.EncodeToString(all.Sum(nil)), err
		}
		data = data[n:]
	}
	fs, err := d.Flush()
	add(fs)
	return out, size, hex.EncodeToString(all.Sum(nil)), err
}

// suiteMD5 reads the MD5 of the whole decoded output that a JCT-VC conformance
// package carries beside its bitstream (a file ending in md5, named for the
// stream or its yuv), or "" when there is none. The file's first token is the
// hash.
func suiteMD5(bitstream string) string {
	if spec.name != "hevc" {
		return ""
	}
	// Some packages also ship the MD5 of the bitstream itself, "..._bit.md5";
	// the one of the decoded output is named for the yuv, or is the only one.
	yuv, _ := filepath.Glob(filepath.Join(filepath.Dir(bitstream), "*[yY][uU][vV]*[mM][dD]5*"))
	any, _ := filepath.Glob(filepath.Join(filepath.Dir(bitstream), "*[mM][dD]5*"))
	for _, p := range append(yuv, any...) {
		if strings.Contains(strings.ToLower(filepath.Base(p)), "_bit") {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil || len(b) > 4096 {
			continue
		}
		if f := strings.Fields(string(b)); len(f) > 0 && len(f[0]) == 32 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// yuvMD5s hashes a conformance suite's own reference output, a concatenation
// of size-byte pictures, so that ffmpeg is not the only witness.
func yuvMD5s(path string, size int) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil || size <= 0 {
		return nil, fmt.Errorf("no reference yuv (%v)", err)
	}
	var out []string
	for ; len(data) >= size; data = data[size:] {
		s := md5.Sum(data[:size])
		out = append(out, hex.EncodeToString(s[:]))
	}
	return out, nil
}

// refYUV finds the suite's reference output beside a bitstream.
func refYUV(bitstream string) string {
	m, _ := filepath.Glob(filepath.Join(filepath.Dir(bitstream), "*.yuv"))
	if len(m) == 1 {
		return m[0]
	}
	return ""
}

// ffmpegMD5s runs ffmpeg -f framemd5 and returns the MD5 column. raw names the
// codec's raw demuxer for bare bitstreams, whose extension may be foreign.
func ffmpegMD5s(path string, raw bool) ([]string, error) {
	args := []string{"-hide_banner", "-loglevel", "error", "-threads", "1"}
	if raw {
		args = append(args, "-f", spec.demuxer)
	}
	args = append(args, "-i", path, "-map", "0:v:0", "-fps_mode", "passthrough")
	if spec.pixFmt != "" {
		args = append(args, "-pix_fmt", spec.pixFmt)
	}
	args = append(args, "-f", "framemd5", "-")
	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// ffmpeg repeats one message per slice; the first line says it all.
		first, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		return nil, fmt.Errorf("%w: %.120s", err, first)
	}
	return parseFrameMD5(out)
}

// parseFrameMD5 reads ffmpeg's framemd5 text: comment lines start with #, the
// others are "stream, dts, pts, duration, size, md5".
func parseFrameMD5(out []byte) ([]string, error) {
	var md5s []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) != 6 {
			return nil, fmt.Errorf("framemd5 line %q has %d fields", line, len(f))
		}
		md5s = append(md5s, strings.TrimSpace(f[5]))
	}
	return md5s, sc.Err()
}

func writeManifest(path string, md5s []string) error {
	var b strings.Builder
	b.WriteString("# ffmpeg -f framemd5 md5 column, display order; frames=" + strconv.Itoa(len(md5s)) + "\n")
	for _, m := range md5s {
		b.WriteString(m + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// profileOf names the profile and level of the first SPS: profile_idc and
// constraint flags (H.264 clause 7.3.2.1, seq_parameter_set_data).
func profileOf(data []byte) string {
	for i := 0; i+6 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 && data[i+3]&0x1f == 7 {
			idc, flags, level := data[i+4], data[i+5], data[i+6]
			name := map[byte]string{66: "Baseline", 77: "Main", 88: "Extended", 100: "High", 110: "High10", 122: "High4:2:2", 244: "High4:4:4P"}[idc]
			if name == "" {
				name = "profile" + strconv.Itoa(int(idc))
			}
			if idc == 66 && flags&0x40 != 0 {
				name = "ConstrainedBaseline"
			}
			return fmt.Sprintf("%s L%d.%d", name, level/10, level%10)
		}
	}
	return "?"
}

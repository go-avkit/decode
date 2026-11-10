// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

// Command elstfix makes the edit list fixtures of the decode package with ffmpeg
// and records what ffmpeg decodes each one to, with the edit list applied (its
// default) and with it ignored (-ignore_editlist 1): the per-picture MD5 and
// presentation time of ffmpeg's framemd5 muxer. The fixtures and the recordings
// are small and committed, so the package's tests need no ffmpeg; this program
// is how they were made and how they are checked again:
//
//	elstfix --out ../testdata/elst --files ../testdata/files --work DIR
//	        write the fixtures and the recordings; DIR holds the encoded sources
//	elstfix ... --check    fail if ffmpeg now records other lines than the
//	                       committed ones
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-avkit/avkit/container"
	"github.com/spf13/cobra"
)

// A fixture is a file whose framemd5 is recorded. A fixture with no Args is an
// existing file under --files; one with Args is made by ffmpeg from the source
// Src, which is encoded under --work first.
type fixture struct {
	Name string
	Src  string
	Args []string // after the input, before the output name
	// Delay is -itsoffset, in seconds: the input is presented later, which the
	// MP4 muxer states with an empty edit.
	Delay string
	// Seek and To cut the input with -ss and -to, stream copied: the cut starts at
	// the keyframe before Seek and the edit list says where the presentation starts.
	Seek, To string
	// NoIgnore leaves out the recording with the edit list ignored.
	NoIgnore bool
	// Edits, in a movie timescale of 1000, replace the edit list of Src: ffmpeg
	// writes at most one edit and an optional empty one, so a list of several is
	// written with avkit's muxer, which writes every edit it is given.
	Edits []container.Edit
	// Fragment rewrites the fixture Src (a file of --out, made before) as a
	// fragmented MP4 with avkit's muxer, keeping its edit list: ffmpeg switches
	// its edit list support off for such a file.
	Fragment bool
}

// sources are encoded once, small: 64x64, 25 pictures per second, a keyframe
// every 10 pictures and two B-frames between references.
var sources = map[string][]string{
	"h264.mp4": {"-c:v", "libx264", "-preset", "veryfast", "-profile:v", "main", "-pix_fmt", "yuv420p", "-g", "10", "-bf", "2",
		"-x264-params", "scenecut=0:threads=1"},
	"hevc.mp4": {"-c:v", "libx265", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-tag:v", "hvc1",
		"-x265-params", "keyint=10:min-keyint=10:bframes=2:scenecut=0:frame-threads=1:pools=1:log-level=error"},
}

var fixtures = []fixture{
	{Name: "h264-delay.mp4", Src: "h264.mp4", Delay: "0.2"},
	{Name: "h264-trim.mp4", Src: "h264.mp4", Seek: "0.45", To: "1.15"},
	{Name: "h264-trim-delay.mp4", Src: "h264.mp4", Delay: "0.12", Seek: "0.45", To: "1.15"},
	{Name: "hevc-delay.mp4", Src: "hevc.mp4", Delay: "0.2"},
	{Name: "hevc-trim.mp4", Src: "hevc.mp4", Seek: "0.45", To: "1.15"},
	{Name: "hevc-trim-delay.mp4", Src: "hevc.mp4", Delay: "0.12", Seek: "0.45", To: "1.15"},
	// Source ticks are 1/12800 s and a picture lasts 512 of them: an empty edit of
	// 200 ms, then picture 11 of the track to 480 ms later (a cut in the middle of a
	// group of pictures), then the pictures from 160 ms for 360 ms, which the first
	// edit has already shown a part of.
	{Name: "h264-edits.mp4", Src: "h264.mp4", Edits: twoEdits},
	{Name: "hevc-edits.mp4", Src: "hevc.mp4", Edits: twoEdits},
	{Name: "h264-trim-frag.mp4", Src: "h264-trim.mp4", Fragment: true},
	{Name: "h264-delay-frag.mp4", Src: "h264-delay.mp4", Fragment: true},
	{Name: "hevc-trim-frag.mp4", Src: "hevc-trim.mp4", Fragment: true},
	{Name: "hevc-delay-frag.mp4", Src: "hevc-delay.mp4", Fragment: true},
	// Files committed before, whose edit lists are the reason for this program.
	{Name: "hevc-main-ipb.mp4"},
	{Name: "hevc-main10-ipb.mp4"},
	{Name: "bframes.mp4"},
	{Name: "bframes-delayed.mp4"},
	// Matroska has no edit list to ignore, and ffmpeg refuses the option there.
	{Name: "bframes.mkv", NoIgnore: true},
}

var twoEdits = []container.Edit{
	{SegmentDuration: 200, MediaTime: container.EmptyEdit, Rate: container.NormalRate},
	{SegmentDuration: 480, MediaTime: 11*512 + 1024, Rate: container.NormalRate},
	{SegmentDuration: 360, MediaTime: 2048 + 1024, Rate: container.NormalRate},
}

// trackWriter is what the progressive and the fragmented muxer share.
type trackWriter interface {
	AddTrack(container.TrackConfig) (uint32, error)
	WriteSample(uint32, container.Sample) error
	Close() error
}

// writeEdits copies the first track of src to dst with its edit list replaced by
// edits, or kept when edits is nil, in a progressive MP4 or, when fragment is set,
// a fragmented one.
func writeEdits(src, dst string, edits []container.Edit, fragment bool) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	r, err := container.NewReader(data)
	if err != nil {
		return err
	}
	id := r.TrackIDs()[0]
	cfg, err := r.TrackConfig(id)
	if err != nil {
		return err
	}
	samples, err := r.Samples(id)
	if err != nil {
		return err
	}
	if edits != nil {
		cfg.EditList = &container.EditList{MovieTimescale: 1000, Edits: edits}
	}
	var buf bytes.Buffer
	var m trackWriter = container.NewProgressiveMuxer(&buf)
	if fragment {
		m = container.NewMuxer(&buf)
	}
	tid, err := m.AddTrack(cfg)
	if err != nil {
		return err
	}
	for _, s := range samples {
		if err := m.WriteSample(tid, s); err != nil {
			return err
		}
	}
	if err := m.Close(); err != nil {
		return err
	}
	return os.WriteFile(dst, buf.Bytes(), 0o644)
}

func run(ffmpeg string, args []string) ([]byte, error) {
	var out, errb bytes.Buffer
	cmd := exec.Command(ffmpeg, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &out, &errb, nil
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg %s: %w: %s", strings.Join(args, " "), err, errb.String())
	}
	return out.Bytes(), nil
}

func (f fixture) makeArgs(src, path string) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if f.Delay != "" {
		args = append(args, "-itsoffset", f.Delay)
	}
	if f.Seek != "" {
		args = append(args, "-ss", f.Seek, "-to", f.To)
	}
	return append(args, "-i", src, "-map", "0:v:0", "-c", "copy", "-bitexact", path)
}

// recordArgs asks for the checksum of every picture ffmpeg returns, each with its
// time in the demuxer's own time base and untouched (-copyts: ffmpeg would
// otherwise take the start time of the file off, which is the delay an empty
// edit states).
func recordArgs(path string, ignore bool) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-copyts"}
	if ignore {
		args = append(args, "-ignore_editlist", "1")
	}
	return append(args, "-i", path, "-map", "0:v:0", "-fps_mode", "passthrough", "-enc_time_base:v", "demux", "-f", "framemd5", "-")
}

// RootCmd returns the command, so that a test can drive it.
func RootCmd() *cobra.Command {
	var out, files, work, ffmpeg string
	var check bool
	cmd := &cobra.Command{
		Use:           "elstfix",
		Short:         "make or check the edit list fixtures of the decode package",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" || files == "" || work == "" {
				return errors.New("--out, --files and --work are required")
			}
			for _, d := range []string{out, work} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					return err
				}
			}
			for name, enc := range sources {
				if check {
					break
				}
				args := append([]string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
					"testsrc2=size=64x64:rate=25:duration=1.6"}, enc...)
				if _, err := run(ffmpeg, append(args, "-bitexact", filepath.Join(work, name))); err != nil {
					return err
				}
			}
			var bad []string
			for _, f := range fixtures {
				path := filepath.Join(files, f.Name)
				if f.Src != "" {
					path = filepath.Join(out, f.Name)
					switch {
					case check:
					case f.Fragment:
						if err := writeEdits(filepath.Join(out, f.Src), path, nil, true); err != nil {
							return err
						}
					case f.Edits != nil:
						if err := writeEdits(filepath.Join(work, f.Src), path, f.Edits, false); err != nil {
							return err
						}
					default:
						if _, err := run(ffmpeg, f.makeArgs(filepath.Join(work, f.Src), path)); err != nil {
							return err
						}
					}
				}
				for _, ignore := range []bool{false, true} {
					if ignore && f.NoIgnore {
						continue
					}
					rec, err := run(ffmpeg, recordArgs(path, ignore))
					if err != nil {
						return err
					}
					ref := filepath.Join(out, f.Name+".applied.framemd5")
					if ignore {
						ref = filepath.Join(out, f.Name+".ignored.framemd5")
					}
					if check {
						want, err := os.ReadFile(ref)
						if err != nil {
							return err
						}
						if !bytes.Equal(rec, want) {
							bad = append(bad, filepath.Base(ref))
						}
						continue
					}
					if err := os.WriteFile(ref, rec, 0o644); err != nil {
						return err
					}
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %d bytes\n", filepath.Base(ref), len(rec))
				}
			}
			if len(bad) > 0 {
				return fmt.Errorf("ffmpeg now records other lines than the committed %s", strings.Join(bad, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "directory of the new fixtures and of the recordings")
	cmd.Flags().StringVar(&files, "files", "", "directory of the existing fixtures (testdata/files)")
	cmd.Flags().StringVar(&work, "work", "", "durable directory for the encoded sources")
	cmd.Flags().StringVar(&ffmpeg, "ffmpeg", "ffmpeg", "the ffmpeg to run")
	cmd.Flags().BoolVar(&check, "check", false, "compare with the committed recordings instead of writing them")
	return cmd
}

var osExit = os.Exit

func main() {
	cmd := RootCmd()
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "elstfix:", err)
		osExit(1)
	}
}

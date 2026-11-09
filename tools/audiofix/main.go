// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

// Command audiofix makes the audio fixtures of the decode package with ffmpeg and
// the reference PCM each one decodes to. The fixtures are small and committed, so
// the package's tests need no ffmpeg; this program is how they were made and how
// they are checked again:
//
//	audiofix --out ../testdata/audio            write fixtures and references
//	audiofix --out ../testdata/audio --check    fail if ffmpeg now decodes any
//	                                            fixture to other bytes than the
//	                                            committed reference
//	audiofix seeds --fixtures ../testdata/audio --out ../testdata/fuzz
//	                                            write the fuzz corpora
package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// A fixture is one file ffmpeg encodes from a synthetic signal, and the raw PCM
// ffmpeg decodes it to.
type fixture struct {
	// Name is the file name; the reference is Name + ".raw".
	Name string
	// Rate and Channels shape the signal.
	Rate, Channels int
	// Seconds is the signal's length.
	Seconds float64
	// Encode is what follows the input in ffmpeg's command line, up to the output
	// name.
	Encode []string
	// Decode is the raw sample format of the reference: s16le, s32le or f32le.
	Decode string
}

// fixtures is every file this program makes, by codec.
var fixtures = []fixture{
	{"flac_s16_stereo.flac", 44100, 2, 0.4, []string{"-c:a", "flac"}, "s16le"},
	{"flac_s24_mono.flac", 48000, 1, 0.3, []string{"-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "24"}, "s32le"},
	{"flac_s16_51.flac", 32000, 6, 0.2, []string{"-c:a", "flac"}, "s16le"},
	{"flac_s16_fixed.flac", 22050, 2, 0.3, []string{"-c:a", "flac", "-compression_level", "0"}, "s16le"},
	{"flac_s16_best.flac", 44100, 2, 0.3, []string{"-c:a", "flac", "-compression_level", "12"}, "s16le"},
	{"flac_s16_indep.flac", 44100, 2, 0.15, []string{"-c:a", "flac", "-ch_mode", "indep"}, "s16le"},
	{"flac_s16_leftside.flac", 44100, 2, 0.15, []string{"-c:a", "flac", "-ch_mode", "left_side"}, "s16le"},
	{"flac_s16_rightside.flac", 44100, 2, 0.15, []string{"-c:a", "flac", "-ch_mode", "right_side"}, "s16le"},
	{"flac_s16_midside.flac", 44100, 2, 0.15, []string{"-c:a", "flac", "-ch_mode", "mid_side"}, "s16le"},
	{"flac_s16_stereo.mka", 44100, 2, 0.3, []string{"-c:a", "flac"}, "s16le"},
	{"flac_s16_stereo.mp4", 44100, 2, 0.3, []string{"-c:a", "flac"}, "s16le"},
	{"flac_s32_stereo.flac", 48000, 2, 0.2, []string{"-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "32", "-strict", "experimental"}, "s32le"},
	{"flac_s16_8ch.flac", 48000, 8, 0.1, []string{"-c:a", "flac"}, "s16le"},
	{"flac_s16_voice8k.flac", 8000, 1, 0.5, []string{"-c:a", "flac", "-frame_size", "1000"}, "s16le"},
}

// signal is the ffmpeg lavfi source of a fixture: a tone, a chirp and a little
// noise per channel, so that the predictors have something to do.
func (f fixture) signal() string {
	exprs := make([]string, f.Channels)
	for i := range exprs {
		exprs[i] = fmt.Sprintf("0.4*sin(2*PI*%d*t)+0.3*sin(2*PI*(200*t+%d*t*t))*(0.5+0.5*sin(2*PI*3*t))+0.05*(random(%d)*2-1)",
			220*(i+2), 1500+300*i, i)
	}
	return fmt.Sprintf("aevalsrc=%s:s=%d:d=%g", strings.Join(exprs, "|"), f.Rate, f.Seconds)
}

func (f fixture) encodeArgs(path string) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", f.signal()}
	// A float signal would be encoded at 24 bits: a fixture that states s16le is
	// 16 bits wide.
	if f.Decode == "s16le" {
		args = append(args, "-sample_fmt", "s16")
	}
	args = append(args, f.Encode...)
	return append(args, "-bitexact", path)
}

func (f fixture) decodeArgs(path string) []string {
	return []string{"-hide_banner", "-loglevel", "error", "-i", path, "-f", f.Decode, "-"}
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

// RootCmd returns the command, so that a test can drive it.
func RootCmd() *cobra.Command {
	var out, ffmpeg string
	var check bool
	cmd := &cobra.Command{
		Use:           "audiofix",
		Short:         "make or check the audio fixtures of the decode package",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == "" {
				return errors.New("--out is required")
			}
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			var bad []string
			for _, f := range fixtures {
				path := filepath.Join(out, f.Name)
				if !check {
					if _, err := run(ffmpeg, f.encodeArgs(path)); err != nil {
						return err
					}
				}
				raw, err := run(ffmpeg, f.decodeArgs(path))
				if err != nil {
					return err
				}
				ref := path + ".raw"
				if check {
					want, err := os.ReadFile(ref)
					if err != nil {
						return err
					}
					if !bytes.Equal(raw, want) {
						bad = append(bad, f.Name)
					}
					continue
				}
				if err := os.WriteFile(ref, raw, 0o644); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %d bytes of %s\n", f.Name, len(raw), f.Decode)
			}
			if len(bad) > 0 {
				return fmt.Errorf("ffmpeg now decodes %s to other bytes than the references", strings.Join(bad, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "directory of the fixtures")
	cmd.Flags().StringVar(&ffmpeg, "ffmpeg", "ffmpeg", "the ffmpeg program")
	cmd.Flags().BoolVar(&check, "check", false, "compare ffmpeg's decoding with the references instead of writing")
	cmd.AddCommand(seedsCmd())
	return cmd
}

var osExit = os.Exit

func main() {
	if err := RootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "audiofix:", err)
		osExit(1)
	}
}

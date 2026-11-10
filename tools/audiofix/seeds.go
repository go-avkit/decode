// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-avkit/avkit/container"
	"github.com/spf13/cobra"
)

// maxSeedFile is the largest fixture written whole into a corpus.
const maxSeedFile = 40000

// seedTargets maps each fuzz target to the codec its seeds come from, by the
// sample entry the track has.
var seedTargets = []struct{ target, codec string }{
	{"FuzzFLACSample", "fLaC"},
	{"FuzzFLACConfig", "fLaC"},
	{"FuzzMP3Sample", "mp3"},
	{"FuzzOpusSample", "Opus"},
	{"FuzzOpusConfig", "Opus"},
}

// corpusEntry is the file format of go test fuzz v1 for one []byte argument.
func corpusEntry(b []byte) []byte {
	return []byte("go test fuzz v1\n[]byte(" + strconv.Quote(string(b)) + ")\n")
}

func writeSeed(dir, name string, b []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), corpusEntry(b), 0o644)
}

// seedsCmd writes the fuzz corpora of the audio targets from the fixtures.
func seedsCmd() *cobra.Command {
	var fixtures, out string
	cmd := &cobra.Command{
		Use:          "seeds",
		Short:        "write the fuzz corpus of the audio targets from the fixtures",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if fixtures == "" || out == "" {
				return fmt.Errorf("--fixtures and --out are required")
			}
			paths, err := filepath.Glob(filepath.Join(fixtures, "*"))
			if err != nil {
				return err
			}
			n := 0
			for _, p := range paths {
				if strings.HasSuffix(p, ".raw") {
					continue
				}
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				base := filepath.Base(p)
				if len(data) <= maxSeedFile {
					if err := writeSeed(filepath.Join(out, "FuzzAudioTrack"), "seed-"+base, data); err != nil {
						return err
					}
					n++
				}
				m, err := writeTrackSeeds(out, base, data)
				if err != nil {
					return err
				}
				n += m
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d seeds\n", n)
			return nil
		},
	}
	cmd.Flags().StringVar(&fixtures, "fixtures", "", "directory of the fixtures")
	cmd.Flags().StringVar(&out, "out", "", "the testdata/fuzz directory")
	return cmd
}

// writeTrackSeeds writes, for each codec target, the first coded samples and the
// codec configuration of the file's audio track. A file that is not readable as
// audio gives none.
func writeTrackSeeds(out, base string, data []byte) (int, error) {
	r, err := container.NewReader(data)
	if err != nil {
		return 0, nil
	}
	n := 0
	for _, tr := range r.File().Tracks {
		if tr.Kind != container.Audio {
			continue
		}
		tc, err := r.TrackConfig(tr.ID)
		if err != nil {
			continue
		}
		samples, err := r.Samples(tr.ID)
		if err != nil {
			continue
		}
		for _, st := range seedTargets {
			if st.codec != tc.Codec {
				continue
			}
			dir := filepath.Join(out, st.target)
			if strings.HasSuffix(st.target, "Config") {
				if err := writeSeed(dir, "seed-"+base, tc.CodecConfig); err != nil {
					return n, err
				}
				n++
				continue
			}
			for i, s := range samples[:min(len(samples), 2)] {
				if err := writeSeed(dir, fmt.Sprintf("seed-%s-%d", base, i), s.Data); err != nil {
					return n, err
				}
				n++
			}
		}
	}
	return n, nil
}

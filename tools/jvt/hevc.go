// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/spf13/cobra"
)

// profileOfHEVC names the profile of the first SPS of an Annex B stream, as
// general_profile_idc, the chroma format and bit depth, and the level. The
// fields are read by mp4ff v0.62.0 (hevc/sps.go, which cites ISO/IEC 23008-2
// clause 7.3.3 for profile_tier_level).
func profileOfHEVC(data []byte) string {
	for i := 0; i+4 < len(data); i++ {
		if data[i] != 0 || data[i+1] != 0 || data[i+2] != 1 || hevc.NaluType((data[i+3]>>1)&0x3f) != hevc.NALU_SPS {
			continue
		}
		end := len(data)
		if j := bytes.Index(data[i+3:], []byte{0, 0, 1}); j >= 0 {
			end = i + 3 + j
		}
		sps, err := hevc.ParseSPSNALUnit(bytes.TrimRight(data[i+3:end], "\x00"))
		if err != nil {
			return "?"
		}
		p := sps.ProfileTierLevel
		return fmt.Sprintf("profile_idc %d chroma %d %d-bit L%.1f", p.GeneralProfileIDC, sps.ChromaFormatIDC,
			sps.BitDepthLumaMinus8+8, float64(p.GeneralLevelIDC)/30)
	}
	return "?"
}

// streamNames is the streams named on the command line, or with all the whole
// suite; naming none and not asking for all is a mistake.
func streamNames(all bool, args []string) ([]string, error) {
	switch {
	case all && len(args) > 0:
		return nil, fmt.Errorf("--all and stream names are exclusive")
	case all:
		return suiteNames()
	case len(args) == 0:
		return nil, fmt.Errorf("name at least one stream, or pass --all")
	}
	return args, nil
}

// expandPaths replaces each @LIST argument by the paths a census list holds, the
// text before the first " | " of each line.
func expandPaths(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		list, ok := strings.CutPrefix(a, "@")
		if !ok {
			out = append(out, a)
			continue
		}
		b, err := os.ReadFile(list)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if p, _, ok := strings.Cut(line, " | "); ok {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

var zipLink = regexp.MustCompile(`(?i)href="[^"]*/([^"/]+)\.zip"`)

// suiteNames lists the streams of the codec's conformance suite from the
// directory index its base URL serves.
func suiteNames() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.baseURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, spec.baseURL)
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range zipLink.FindAllSubmatch(page, -1) {
		names = append(names, string(m[1]))
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no streams listed at %s", spec.baseURL)
	}
	return names, nil
}

// censusCmd lists the HEVC video files under a directory with what ffprobe says
// of their first video stream: the library census the claimed profiles rest on.
// It prints local paths, so its output is never committed.
func censusCmd() *cobra.Command {
	return &cobra.Command{
		Use: "census DIR", Short: "list HEVC files under DIR with profile, pixel format and size", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return filepath.WalkDir(args[0], func(p string, e fs.DirEntry, err error) error {
				if err != nil || e.IsDir() {
					return err
				}
				switch strings.ToLower(filepath.Ext(p)) {
				case ".mp4", ".mkv", ".mov", ".m4v":
				default:
					return nil
				}
				out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
					"stream=codec_name,profile,pix_fmt,width,height", "-of", "csv=p=0", p).Output()
				if err != nil {
					return nil // not a file ffprobe reads: not counted
				}
				if line := strings.TrimSpace(string(out)); strings.HasPrefix(line, "hevc,") {
					fmt.Fprintf(cmd.OutOrStdout(), "%s | %s\n", p, line)
				}
				return nil
			})
		},
	}
}

// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

// Command vpxfetch downloads libvpx's decoder test vectors and their per-frame md5 files:
//
//	vpxfetch --cc test_vectors.cc --dir ~/Documents/VCS/GIT/localhost/libvpx-test-vectors
//
// The names are read from libvpx's test/test_vectors.cc (the vp80, vp90, vp91, vp92 and vp93 files
// it lists); each NAME and NAME.md5 is fetched from the webmproject test-data bucket unless it is
// already in --dir. A file that does not download is reported and fails the run, never skipped.
package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// DefaultBase is where libvpx's test data lives (libvpx test/test_vectors.cc names the files).
const DefaultBase = "https://storage.googleapis.com/downloads.webmproject.org/test_data/libvpx/"

var osExit = os.Exit

var nameRE = regexp.MustCompile(`"(vp(?:80|90|91|92|93)-[A-Za-z0-9_.-]+\.(?:ivf|webm))"`)

// Names lists the vector file names a test_vectors.cc source holds, sorted, once each.
func Names(src string) []string {
	var out []string
	for _, m := range nameRE.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// fetch downloads base+name into dir/name unless it is there; the file is written whole or not at all.
func fetch(client *http.Client, base, dir, name string) error {
	dst := filepath.Join(dir, name)
	if st, err := os.Stat(dst); err == nil && st.Size() > 0 {
		return nil
	}
	resp, err := client.Get(base + name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", name, resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, name+".part*")
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("%s: %w", name, err)
	}
	return os.Rename(tmp.Name(), dst)
}

// RootCmd is the command line.
func RootCmd() *cobra.Command {
	var cc, dir, base string
	cmd := &cobra.Command{
		Use:           "vpxfetch --cc test_vectors.cc --dir DIR",
		Short:         "download libvpx's decoder test vectors and their md5 files",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cc == "" || dir == "" {
				return errors.New("--cc and --dir are required")
			}
			src, err := os.ReadFile(cc)
			if err != nil {
				return err
			}
			names := Names(string(src))
			if len(names) == 0 {
				return fmt.Errorf("%s lists no vp80/vp90/vp91/vp92/vp93 vectors", cc)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if !strings.HasSuffix(base, "/") {
				base += "/"
			}
			var failed []string
			for _, n := range names {
				for _, f := range []string{n, n + ".md5"} {
					if err := fetch(http.DefaultClient, base, dir, f); err != nil {
						failed = append(failed, err.Error())
					}
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d vectors in %s, %d failures\n", len(names), dir, len(failed))
			if len(failed) > 0 {
				return errors.New(strings.Join(failed, "; "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cc, "cc", "", "libvpx test/test_vectors.cc")
	cmd.Flags().StringVar(&dir, "dir", "", "directory to download into")
	cmd.Flags().StringVar(&base, "base", DefaultBase, "URL the files are under")
	return cmd
}

func main() {
	if err := RootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "vpxfetch:", err)
		osExit(1)
	}
}

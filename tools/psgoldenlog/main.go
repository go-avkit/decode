// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

// Command psgoldenlog re-records the golden output hashes of a route (testdata/aac/
// psgolden/<route>.txt) from the log of a CI lane that cannot be run by hand, so that no
// line of a log is retyped:
//
//	psgoldenlog --route amd64 --log lane.log --golden ../../testdata/aac/psgolden/amd64.txt
//
// TestPSOutputGolden prints, for a stream whose hash differs from the recorded one,
//
//	NAME (route R): output SHA-256 HASH, recorded "OLD"
//
// and, for a route with no file, PSGOLDEN R NAME HASH. The program reads both. The file
// keeps the streams it already holds in their order, each with the hash the log gives
// (a stream the log does not mention passed, so its hash stays), and the streams the log
// adds follow. A log that names no stream of the route is an error, not an empty file.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var osExit = os.Exit

// hashes reads the (name, hash) pairs of one route from a log, in order of appearance.
func hashes(r io.Reader, route string) (names []string, hash map[string]string, err error) {
	mismatch := regexp.MustCompile(`(\S+) \(route ` + regexp.QuoteMeta(route) + `\): output SHA-256 ([0-9a-f]{64})\b`)
	missing := regexp.MustCompile(`\bPSGOLDEN ` + regexp.QuoteMeta(route) + ` (\S+) ([0-9a-f]{64})\b`)
	hash = map[string]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		m := mismatch.FindStringSubmatch(line)
		if m == nil {
			m = missing.FindStringSubmatch(line)
		}
		if m == nil {
			continue
		}
		if _, dup := hash[m[1]]; !dup {
			names = append(names, m[1])
		}
		hash[m[1]] = m[2]
	}
	return names, hash, sc.Err()
}

// merge is the new content of a golden file: the old streams in order with the logged
// hashes, then the streams the log adds.
func merge(old string, names []string, logged map[string]string) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, ln := range strings.Split(strings.TrimSpace(old), "\n") {
		f := strings.Fields(ln)
		if len(f) != 2 {
			continue
		}
		h := f[1]
		if v, ok := logged[f[0]]; ok {
			h = v
		}
		seen[f[0]] = true
		fmt.Fprintf(&b, "%s %s\n", f[0], h)
	}
	for _, n := range names {
		if !seen[n] {
			fmt.Fprintf(&b, "%s %s\n", n, logged[n])
		}
	}
	return b.String()
}

// RootCmd is the command.
func RootCmd() *cobra.Command {
	var route, logPath, golden string
	cmd := &cobra.Command{
		Use:           "psgoldenlog --route R --log FILE --golden FILE",
		Short:         "re-record a PS golden file from a CI lane's log",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := os.Open(logPath)
			if err != nil {
				return err
			}
			defer f.Close()
			names, logged, err := hashes(f, route)
			if err != nil {
				return err
			}
			if len(names) == 0 {
				return fmt.Errorf("%s names no stream of route %s: not the log of that lane, or the lane passed", logPath, route)
			}
			var old []byte
			if o, err := os.ReadFile(golden); err == nil {
				old = o
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.WriteFile(golden, []byte(merge(string(old), names, logged)), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %d stream(s) taken from the log\n", golden, len(names))
			return nil
		},
	}
	cmd.Flags().StringVar(&route, "route", "", "the route (the lane's GOARCH and kernels), as TestPSOutputGolden names it")
	cmd.Flags().StringVar(&logPath, "log", "", "the lane's log")
	cmd.Flags().StringVar(&golden, "golden", "", "the golden file to write")
	for _, n := range []string{"route", "log", "golden"} {
		_ = cmd.MarkFlagRequired(n)
	}
	return cmd
}

func main() {
	cmd := RootCmd()
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "psgoldenlog:", err)
		osExit(1)
	}
}

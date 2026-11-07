// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

// Command covergate fails when a coverage profile has a statement that no test
// ran. It reads the profile itself: `go tool cover -func` prints percentages
// rounded to one decimal, and a function at 99.97% is printed as 100.0%.
//
//	go test -coverprofile=cover.out ./...
//	covergate cover.out
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var osExit = os.Exit

func main() {
	if err := RootCmd().Execute(); err != nil {
		osExit(1)
	}
}

// RootCmd builds the command.
func RootCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "covergate PROFILE",
		Short:        "fail unless every statement in a coverage profile ran",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			missed, total, err := Uncovered(f)
			if err != nil {
				return err
			}
			for _, m := range missed {
				fmt.Fprintln(cmd.ErrOrStderr(), "not run:", m)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%d of %d statements not run\n", len(missed), total)
			if len(missed) > 0 {
				return fmt.Errorf("%d statement blocks were never run", len(missed))
			}
			return nil
		},
	}
}

// Uncovered returns the blocks of a Go coverage profile (go help testflag,
// -coverprofile) that hold statements but ran zero times, and the statement
// count of the whole profile. A profile with no statements is an error, so a
// package that was not measured cannot pass.
func Uncovered(r io.Reader) (missed []string, total int, err error) {
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			if !strings.HasPrefix(line, "mode:") {
				return nil, 0, fmt.Errorf("not a coverage profile: %q", line)
			}
			continue
		}
		// file:startLine.startCol,endLine.endCol numStatements count
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, 0, fmt.Errorf("malformed profile line %q", line)
		}
		n, err1 := strconv.Atoi(fields[1])
		c, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			return nil, 0, fmt.Errorf("malformed profile line %q", line)
		}
		total += n
		if n > 0 && c == 0 {
			missed = append(missed, fields[0])
		}
	}
	if err := sc.Err(); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, fmt.Errorf("the profile holds no statements")
	}
	return missed, total, nil
}

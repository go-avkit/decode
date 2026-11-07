// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"fmt"
	"os/exec"
	"strconv"

	"github.com/spf13/cobra"
)

// clipCmd cuts the first N video packets of a real file, stream copy, into a
// new file. The first N packets in decoding order are self-contained (every
// reference of a picture precedes it), so ffmpeg and this decoder are asked for
// the same N pictures, and a two-hour film is not decoded to test a decoder.
func clipCmd() *cobra.Command {
	var n int
	c := &cobra.Command{
		Use: "clip SRC DST", Short: "stream-copy the first N video packets of SRC into DST", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-i", args[0],
				"-map", "0:v:0", "-c", "copy", "-frames:v", strconv.Itoa(n), "-an", "-sn", args[1]).CombinedOutput()
			if err != nil {
				return fmt.Errorf("ffmpeg: %w: %.200s", err, out)
			}
			return nil
		},
	}
	c.Flags().IntVar(&n, "frames", 900, "number of video packets to keep")
	return c
}

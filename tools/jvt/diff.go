// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"crypto/md5"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-avkit/decode"
	"github.com/spf13/cobra"
)

// diffCmd says where picture INDEX of a stream first goes wrong against the
// suite's own reference yuv: how many macroblocks differ and which come first.
// It is how a defect is localised before anything is explained.
func diffCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use: "diff NAME INDEX", Short: "locate the macroblocks of one picture that differ from the reference yuv", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var idx int
			if _, err := fmt.Sscan(args[1], &idx); err != nil {
				return err
			}
			path, err := fetchStream(dir, args[0])
			if err != nil {
				return err
			}
			return diffPicture(cmd.OutOrStdout(), path, idx)
		},
	}
	c.Flags().StringVar(&dir, "dir", "", "directory of downloaded streams")
	_ = c.MarkFlagRequired("dir")
	return c
}

func diffPicture(w io.Writer, path string, idx int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d, err := decode.New(decode.Config{MaxFrameMBs: 1 << 17}, 4)
	if err != nil {
		return err
	}
	defer d.Close()
	var frames []decode.Frame
	fs, err := d.DecodeAnnexB(data)
	frames = append(frames, fs...)
	if err == nil {
		fs, err = d.Flush()
		frames = append(frames, fs...)
	}
	if err != nil {
		return err
	}
	y := refYUV(path)
	if y == "" {
		return fmt.Errorf("no reference yuv beside %s", filepath.Base(path))
	}
	ref, err := os.ReadFile(y)
	if err != nil {
		return err
	}
	if len(frames) > 0 && idx < 0 {
		// INDEX -1: list every picture that differs.
		size := len(frames[0].Y) + len(frames[0].Cb) + len(frames[0].Cr)
		var bad []int
		for i, f := range frames {
			if (i+1)*size > len(ref) || frameMD5(f) != fmt.Sprintf("%x", md5.Sum(ref[i*size:(i+1)*size])) {
				bad = append(bad, i)
			}
		}
		fmt.Fprintf(w, "%d of %d pictures differ: %v\n", len(bad), len(frames), bad)
		// Pictures that are right but in the wrong place point at output order
		// (POC), not at decoding.
		where := map[string]int{}
		for i := 0; (i+1)*size <= len(ref); i++ {
			where[fmt.Sprintf("%x", md5.Sum(ref[i*size:(i+1)*size]))] = i
		}
		moved := 0
		var sample []string
		for _, i := range bad {
			if j, ok := where[frameMD5(frames[i])]; ok {
				moved++
				if len(sample) < 12 {
					sample = append(sample, fmt.Sprintf("%d->%d", i, j))
				}
			}
		}
		fmt.Fprintf(w, "%d of the differing pictures exist elsewhere in the reference (ours->reference): %v\n", moved, sample)
		return nil
	}
	if idx < 0 || idx >= len(frames) {
		return fmt.Errorf("picture %d of %d", idx, len(frames))
	}
	f := frames[idx]
	size := len(f.Y) + len(f.Cb) + len(f.Cr)
	if (idx+1)*size > len(ref) {
		return fmt.Errorf("reference yuv holds %d pictures", len(ref)/size)
	}
	ref = ref[idx*size : (idx+1)*size]
	mbw := (f.Width + 15) / 16
	bad := map[int]int{}
	var order []int
	for i := range f.Y {
		if f.Y[i] != ref[i] {
			mb := (i/f.Width/16)*mbw + (i%f.Width)/16
			if bad[mb] == 0 {
				order = append(order, mb)
			}
			bad[mb]++
		}
	}
	fmt.Fprintf(w, "%dx%d, %d macroblocks, %d differ in luma\n", f.Width, f.Height, mbw*((f.Height+15)/16), len(order))
	for i, mb := range order {
		if i == 12 {
			break
		}
		fmt.Fprintf(w, "  MB %d (x %d, y %d): %d samples\n", mb, mb%mbw, mb/mbw, bad[mb])
	}
	return nil
}

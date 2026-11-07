// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// seedsCmd writes the fuzz corpus the library's fuzz targets start from, in the
// "go test fuzz v1" file format, one file per stream and target. Real streams
// cut short are what a decoder meets when a file is truncated, and are a far
// better start than random bytes: the parameter sets and slice headers are
// already well formed, so mutation reaches the macroblock layer.
func seedsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "seeds STREAMS_DIR CORPUS_DIR", Short: "write the committed fuzz corpus from the test streams", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			streams, err := filepath.Glob(filepath.Join(args[0], "*.264"))
			if err != nil || len(streams) == 0 {
				return fmt.Errorf("no streams in %s", args[0])
			}
			for _, s := range streams {
				data, err := os.ReadFile(s)
				if err != nil {
					return err
				}
				name := strings.TrimSuffix(filepath.Base(s), ".264")
				nals := splitNALs(data)
				if err := writeSeed(args[1], "FuzzDecodeAnnexB", name, head(data, 1500)); err != nil {
					return err
				}
				if err := writeSeed(args[1], "FuzzDecodeSample", name, firstUnit(nals, 4)); err != nil {
					return err
				}
				if err := writeSeed(args[1], "FuzzParseAVCC", name, avccOf(nals)); err != nil {
					return err
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %d seeds per target\n", len(streams))
			return nil
		},
	}
}

func head(b []byte, n int) []byte { return b[:min(n, len(b))] }

func writeSeed(dir, target, name string, data []byte) error {
	d := filepath.Join(dir, target)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("go test fuzz v1\n[]byte(%q)\n", data)
	return os.WriteFile(filepath.Join(d, "seed-"+name), []byte(body), 0o644)
}

// splitNALs cuts an Annex B stream into NAL units without start codes.
func splitNALs(b []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				nals = append(nals, bytes.TrimRight(b[start:i], "\x00"))
			}
			start = i + 3
			i += 2
		}
	}
	if start >= 0 {
		nals = append(nals, b[start:])
	}
	return nals
}

// firstUnit is the stream's first sample as length-prefixed NAL units: every NAL
// before the second slice, each cut to 1500 bytes with its length corrected.
func firstUnit(nals [][]byte, size int) []byte {
	var out []byte
	slices := 0
	for _, n := range nals {
		if t := n[0] & 31; t == 1 || t == 5 {
			if slices++; slices > 1 {
				break
			}
		}
		n = head(n, 1500)
		out = binary.BigEndian.AppendUint32(out, uint32(len(n)))
		out = append(out, n...)
	}
	return out
}

// avccOf builds an avcC record from the stream's first SPS and PPS.
func avccOf(nals [][]byte) []byte {
	var sps, pps []byte
	for _, n := range nals {
		switch n[0] & 31 {
		case 7:
			if sps == nil {
				sps = n
			}
		case 8:
			if pps == nil {
				pps = n
			}
		}
	}
	if len(sps) < 4 || pps == nil {
		return nil
	}
	b := []byte{1, sps[1], sps[2], sps[3], 0xff, 0xe1}
	b = binary.BigEndian.AppendUint16(b, uint16(len(sps)))
	b = append(b, sps...)
	b = append(b, 1)
	b = binary.BigEndian.AppendUint16(b, uint16(len(pps)))
	return append(b, pps...)
}

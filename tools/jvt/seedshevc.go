// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// hevcSeeds writes the HEVC fuzz corpus from the .265 streams in dir, the same
// way the H.264 one is written: a stream cut short for the Annex B target, its
// first access unit length-prefixed for the sample target, and an hvcC record
// built from its parameter sets.
func hevcSeeds(cmd *cobra.Command, dir, corpus string) error {
	streams, err := filepath.Glob(filepath.Join(dir, "*.265"))
	if err != nil || len(streams) == 0 {
		return fmt.Errorf("no streams in %s", dir)
	}
	for _, s := range streams {
		data, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(s), ".265")
		nals := splitNALs(data)
		for target, seed := range map[string][]byte{
			"FuzzHEVCAnnexB": head(data, 1500),
			"FuzzHEVCSample": hevcFirstUnit(nals),
			"FuzzParseHVCC":  hvccOf(nals),
		} {
			if err := writeSeed(corpus, target, name, seed); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote %d seeds per target\n", len(streams))
	return nil
}

// hevcType is the nal_unit_type of an HEVC NAL unit (its first header byte).
func hevcType(n []byte) int { return int(n[0]>>1) & 0x3f }

// hevcFirstUnit is the stream's first access unit as length-prefixed NAL units:
// every NAL before the second picture's first slice, each cut to 1500 bytes.
func hevcFirstUnit(nals [][]byte) []byte {
	var out []byte
	pictures := 0
	for _, n := range nals {
		if len(n) < 3 {
			continue
		}
		if hevcType(n) < 32 && n[2]&0x80 != 0 {
			if pictures++; pictures > 1 {
				break
			}
		}
		n = head(n, 1500)
		out = binary.BigEndian.AppendUint32(out, uint32(len(n)))
		out = append(out, n...)
	}
	return out
}

// hvccOf builds an hvcC record from the stream's first VPS, SPS and PPS.
func hvccOf(nals [][]byte) []byte {
	b := make([]byte, 23)
	b[0], b[21] = 1, 0xff
	for _, typ := range []int{32, 33, 34} {
		for _, n := range nals {
			if len(n) > 2 && hevcType(n) == typ {
				b[22]++
				b = append(b, byte(0x80|typ), 0, 1)
				b = binary.BigEndian.AppendUint16(b, uint16(len(n)))
				b = append(b, n...)
				break
			}
		}
	}
	return b
}

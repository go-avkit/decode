// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// streams returns the committed bitstreams: testdata/streams/NAME.264, each
// with the NAME.md5 that ffmpeg -f framemd5 produced for it.
func streams(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "streams", "*.264"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no test streams: %v", err)
	}
	return paths
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// reference returns ffmpeg's per-picture MD5s for a stream.
func reference(t *testing.T, stream string) []string {
	t.Helper()
	var md5s []string
	for _, l := range strings.Split(string(readFile(t, strings.TrimSuffix(stream, ".264")+".md5")), "\n") {
		// A checkout with autocrlf (Windows) ends the lines with \r\n.
		l = strings.TrimRight(l, "\r")
		if l != "" && l[0] != '#' {
			md5s = append(md5s, l)
		}
	}
	return md5s
}

func sum(f Frame) string {
	h := md5.New()
	h.Write(f.Y)
	h.Write(f.Cb)
	h.Write(f.Cr)
	return hex.EncodeToString(h.Sum(nil))
}

func sums(fs []Frame) []string {
	var out []string
	for _, f := range fs {
		out = append(out, sum(f))
	}
	return out
}

// splitNALs cuts an Annex B stream (4-byte or 3-byte start codes) into NAL
// units without their start codes.
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
	return append(nals, b[start:])
}

// accessUnits groups NAL units into access units: a new one starts at an
// AUD/SPS/PPS/SEI that follows a slice, and at a slice with first_mb_in_slice
// 0 (its first RBSP bit is the ue(v) code '1').
func accessUnits(nals [][]byte) [][][]byte {
	var aus [][][]byte
	sawSlice := false
	for _, n := range nals {
		typ := n[0] & 31
		slice := typ == 1 || typ == 5
		if sawSlice && ((slice && n[1]&0x80 != 0) || (typ >= 6 && typ <= 9)) {
			sawSlice = false
			aus = append(aus, nil)
		}
		if len(aus) == 0 {
			aus = append(aus, nil)
		}
		aus[len(aus)-1] = append(aus[len(aus)-1], n)
		sawSlice = sawSlice || slice
	}
	return aus
}

// prefixed writes NAL units as one length-prefixed sample.
func prefixed(nals [][]byte, size int) []byte {
	var out []byte
	for _, n := range nals {
		switch size {
		case 1:
			out = append(out, byte(len(n)))
		case 2:
			out = binary.BigEndian.AppendUint16(out, uint16(len(n)))
		default:
			out = binary.BigEndian.AppendUint32(out, uint32(len(n)))
		}
		out = append(out, n...)
	}
	return out
}

// parameterSets splits an access unit list into SPS NALs, PPS NALs and the
// slice samples (every unit that is not a parameter set).
func parameterSets(aus [][][]byte) (sps, pps [][]byte, samples [][][]byte) {
	for _, au := range aus {
		var rest [][]byte
		for _, n := range au {
			switch n[0] & 31 {
			case 7:
				sps = append(sps, n)
			case 8:
				pps = append(pps, n)
			default:
				rest = append(rest, n)
			}
		}
		if len(rest) > 0 {
			samples = append(samples, rest)
		}
	}
	return
}

// avcC builds an AVCDecoderConfigurationRecord (ISO/IEC 14496-15 5.3.3.1.2).
func avcC(sps, pps [][]byte, lengthSize int, high bool) []byte {
	b := []byte{1, sps[0][1], sps[0][2], sps[0][3], 0xfc | byte(lengthSize-1), 0xe0 | byte(len(sps))}
	for _, s := range sps {
		b = binary.BigEndian.AppendUint16(b, uint16(len(s)))
		b = append(b, s...)
	}
	b = append(b, byte(len(pps)))
	for _, p := range pps {
		b = binary.BigEndian.AppendUint16(b, uint16(len(p)))
		b = append(b, p...)
	}
	if high {
		b = append(b, 0xfc|1, 0xf8, 0xf8, 0)
	}
	return b
}

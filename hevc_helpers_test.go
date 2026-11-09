// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"
)

// hevcStreams returns the committed HEVC bitstreams the decoder is expected to
// decode, testdata/hevc/streams/NAME.265, each with the NAME.md5 that ffmpeg
// -f framemd5 produced for it. yuv444_unsupported has none: it is the stream
// the decoder must refuse.
func hevcStreams(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "hevc", "streams", "*.265"))
	if err != nil || len(paths) < 6 {
		t.Fatalf("test streams: %v (%d)", err, len(paths))
	}
	var out []string
	for _, p := range paths {
		if !strings.Contains(p, "unsupported") {
			out = append(out, p)
		}
	}
	return out
}

// hevcReference returns ffmpeg's per-picture MD5s for a .265 stream.
func hevcReference(t *testing.T, stream string) []string {
	t.Helper()
	return reference(t, strings.TrimSuffix(stream, ".265")+".264")
}

func nalType(n []byte) int { return int(n[0]>>1) & 0x3f }

// hevcAccessUnits groups NAL units into access units: a new one starts at a
// parameter set, delimiter or prefix SEI that follows a slice, and at a slice
// whose first_slice_segment_in_pic_flag (the first bit after the two header
// bytes) is set.
func hevcAccessUnits(nals [][]byte) [][][]byte {
	var aus [][][]byte
	sawSlice := false
	for _, n := range nals {
		typ := nalType(n)
		slice := typ < 32
		if sawSlice && ((slice && n[2]&0x80 != 0) || (typ >= 32 && typ <= 40)) {
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

// hevcSets splits access units into VPS, SPS and PPS NAL units and the samples
// made of every other unit.
func hevcSets(aus [][][]byte) (vps, sps, pps [][]byte, samples [][][]byte) {
	for _, au := range aus {
		var rest [][]byte
		for _, n := range au {
			switch nalType(n) {
			case 32:
				vps = append(vps, n)
			case 33:
				sps = append(sps, n)
			case 34:
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

// hvcC builds an HEVCDecoderConfigurationRecord with the three arrays (layout as
// in mp4ff's hevc package).
func hvcC(vps, sps, pps [][]byte, lengthSize int) []byte {
	b := make([]byte, 23)
	b[0], b[21], b[22] = 1, 0xfc|byte(lengthSize-1), 3
	for i, set := range [][][]byte{vps, sps, pps} {
		b = append(b, byte(0x80|(32+i)))
		b = binary.BigEndian.AppendUint16(b, uint16(len(set)))
		for _, n := range set {
			b = binary.BigEndian.AppendUint16(b, uint16(len(n)))
			b = append(b, n...)
		}
	}
	return b
}

// paramBits writes the fields of a parameter set, for the streams a test must
// make up because no encoder will write them.
type paramBits struct {
	b []byte
	n int
}

func (w *paramBits) bit(x uint64) {
	if w.n%8 == 0 {
		w.b = append(w.b, 0)
	}
	w.b[len(w.b)-1] |= byte(x&1) << (7 - w.n%8)
	w.n++
}

func (w *paramBits) u(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> i)
	}
}

func (w *paramBits) ue(v uint64) {
	v++
	l := 0
	for x := v; x > 0; x >>= 1 {
		l++
	}
	w.u(0, l-1)
	w.u(v, l)
}

func (w *paramBits) trailing() []byte {
	w.bit(1)
	for w.n%8 != 0 {
		w.bit(0)
	}
	return w.b
}

// nalFrom makes a NAL unit of a type from an RBSP, with the emulation
// prevention bytes the payload needs.
func nalFrom(typ int, rbsp []byte) []byte {
	out := []byte{byte(typ << 1), 1}
	zeros := 0
	for _, c := range rbsp {
		if zeros >= 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// bigSPS is a sequence parameter set for a w x h Main profile 4:2:0 picture, and
// bigPPS the picture parameter set that goes with it.
func bigSPS(w, h uint64) []byte {
	var s paramBits
	s.u(0, 4) // vps id
	s.u(0, 3) // max_sub_layers_minus1
	s.u(1, 1) // temporal_id_nesting
	s.u(1, 8) // profile space, tier, profile_idc 1
	s.u(0x60000000, 32)
	s.u(0x90, 8) // progressive, non packed, frame only flags
	s.u(0, 40)   // the rest of the 48 bits of flags and reserved bits
	s.u(120, 8)  // level 4
	s.ue(0)      // sps id
	s.ue(1)      // chroma_format_idc
	s.ue(w)
	s.ue(h)
	s.u(0, 1) // conformance window
	s.ue(0)
	s.ue(0)
	s.ue(4) // log2_max_pic_order_cnt_lsb_minus4
	s.u(1, 1)
	s.ue(0)
	s.ue(0)
	s.ue(0)
	s.ue(0) // log2_min_luma_coding_block_size_minus3
	s.ue(3) // log2_diff_max_min
	s.ue(0)
	s.ue(3)
	s.ue(0)
	s.ue(0)
	s.u(0, 4) // scaling lists, amp, sao, pcm
	s.ue(0)   // short term ref pic sets
	s.u(0, 4) // long term, tmvp, strong intra, vui
	s.u(0, 1)
	return nalFrom(33, s.trailing())
}

func bigPPS() []byte {
	var p paramBits
	p.ue(0)
	p.ue(0)
	p.u(0, 1)
	p.u(0, 1)
	p.u(0, 3)
	p.u(0, 2)
	p.ue(0)
	p.ue(0)
	p.ue(0)   // init_qp_minus26 as se(0)
	p.u(0, 3) // constrained intra, transform skip, cu_qp_delta
	p.ue(0)   // cb and cr qp offsets
	p.ue(0)
	p.u(0, 10) // the ten flags from slice_chroma_qp_offsets_present to lists_modification
	p.ue(0)    // log2_parallel_merge_level_minus2
	p.u(0, 2)  // slice header extension, pps extension
	return nalFrom(34, p.trailing())
}

// bigIDR is an IDR slice: first_slice_segment_in_pic_flag 1,
// no_output_of_prior_pics_flag 0, slice_pic_parameter_set_id 0.
func bigIDR() []byte {
	return nalFrom(19, []byte{0xa0, 0x80})
}

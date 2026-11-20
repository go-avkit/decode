// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"crypto/md5"
	"encoding/hex"
	"hash"
	"os"
	"testing"
)

// rextSuite lists the JCT-VC conformance bitstreams whose format a Frame did not carry before
// (4:0:0, 4:2:2, 4:4:4, 9 and 11 to 16 bits, unequal luma and chroma depths), or that no manifest
// of testdata/hevc-conformance covers, with the MD5 of the whole decoded output that the
// stream's own package ships beside it (the .yuv or .gbr line of its .md5 or md5sum.txt file):
// every picture's planes in output order, cropped, each a sample after the other, little-endian
// words above 8 bits, luma only for 4:0:0. The hash, never the bitstream, whose redistribution
// terms are not stated, is what is committed; the bitstreams come from DECODE_HEVC_DIR, as for
// TestHEVCConformance. Where the suite's file and a Frame disagree about the layout (unequal
// depths), rextWordLayout says which one the suite uses.
var rextSuite = map[string]string{
	"ADJUST_IPRED_ANGLE_A_RExt_Mitsubishi_2":                               "b045776950c2e06d35348bd03268c07f",
	"Bitdepth_A_RExt_Sony_1":                                               "2b65fa2f384085926deb4f9d20fcd798",
	"Bitdepth_B_RExt_Sony_1":                                               "cf489cf45491fab54cdba936885836cb",
	"EXTPREC_HIGHTHROUGHPUT_444_16_INTRA_10BIT_RExt_Sony_1":                "ae359f92c74a42c6733e9adfa7ddbbe5",
	"EXTPREC_HIGHTHROUGHPUT_444_16_INTRA_12BIT_RExt_Sony_1":                "bcccdfb428885adb0a026d55df207e23",
	"EXTPREC_HIGHTHROUGHPUT_444_16_INTRA_16BIT_RExt_Sony_1":                "23d546b71e9bc7c8a636099ef9a3dfcd",
	"EXTPREC_HIGHTHROUGHPUT_444_16_INTRA_8BIT_RExt_Sony_1":                 "cd84e70999e4577f23eb2077c7a1d24f",
	"EXTPREC_MAIN_444_16_INTRA_10BIT_RExt_Sony_1":                          "6c45bc0e653c5687acbc4bb483b8a512",
	"EXTPREC_MAIN_444_16_INTRA_12BIT_RExt_Sony_1":                          "b10f17ae9968230f2904216c9551eb2b",
	"EXTPREC_MAIN_444_16_INTRA_16BIT_RExt_Sony_1":                          "68d8956971e5b327e90391b18c0dc0eb",
	"EXTPREC_MAIN_444_16_INTRA_8BIT_RExt_Sony_1":                           "7ef62c35074ed780790cb32d1b5da436",
	"ExplicitRdpcm_A_BBC_1":                                                "1156aca54db44114c2737e9679942656",
	"ExplicitRdpcm_B_BBC_2":                                                "866d75c3530d7b659b3632655c7eb974",
	"GENERAL_10b_420_RExt_Sony_1":                                          "d19c7e1764971983f0e827277db2f69a",
	"GENERAL_10b_422_RExt_Sony_1":                                          "ea5dfb01c95bffb169cd78e549bd39ab",
	"GENERAL_10b_444_RExt_Sony_2":                                          "079dd98d0cb2ab8fe26475a2e946fe53",
	"GENERAL_12b_400_RExt_Sony_1":                                          "549ff2b94ede8d83bfdc64a34440817d",
	"GENERAL_12b_420_RExt_Sony_1":                                          "27a2b09b136687f7a391bfdbc50ac964",
	"GENERAL_12b_422_RExt_Sony_1":                                          "d4f51ab5405659ad90e788083bcfc18d",
	"GENERAL_12b_444_RExt_Sony_2":                                          "0b81d1008ff671da0d930bd38ab3489c",
	"GENERAL_16b_400_RExt_Sony_1":                                          "eb432746ce6c66943613397da28526a7",
	"GENERAL_16b_444_RExt_Sony_2":                                          "41eb40070aae0bbedd584e1f73512840",
	"GENERAL_16b_444_highThroughput_RExt_Sony_2":                           "7422848e6a99897a8ade34814d396657",
	"GENERAL_8b_400_RExt_Sony_1":                                           "e5223be3da805fb96440dbf2bd170db0",
	"GENERAL_8b_420_RExt_Sony_1":                                           "99eb7a5106988759fd0eaecdb66e2cd2",
	"GENERAL_8b_444_RExt_Sony_2":                                           "3ed16fed34c4ed3ad8fe31962fb77aac",
	"IPCM_A_RExt_NEC_2":                                                    "8d4eb18812338f9f475035b8da27e74a",
	"IPCM_B_RExt_NEC":                                                      "eea32e6e1f88b7782515e53f8122be7d",
	"Main_422_10_A_RExt_Sony_2":                                            "66530b83be6647f87efbb2b9ae6e1e28",
	"Main_422_10_B_RExt_Sony_2":                                            "fd163bcbdc24792a23781ee7aace74eb",
	"PERSIST_RPARAM_A_RExt_Sony_3":                                         "d19f5cd87d5b971c4b4abbca22958031",
	"QMATRIX_A_RExt_Sony_1":                                                "e0fe38f19cf0ac875ce6674da989ea38",
	"SAO_A_RExt_MediaTek_1":                                                "126cd42a185b327d86640a9269044bc8",
	"TSCTX_10bit_I_RExt_SHARP_1":                                           "2230c35a91ddde5455a36c49fb868e86",
	"TSCTX_10bit_RExt_SHARP_1":                                             "a3015d8f4bd0a90645514bcf2e1fa8a7",
	"TSCTX_12bit_I_RExt_SHARP_1":                                           "acebd42c1edea6812f0c07a4e91d943f",
	"TSCTX_12bit_RExt_SHARP_1":                                             "f311f3e96d9fbda375bfce341b99cb99",
	"TSCTX_8bit_I_RExt_SHARP_1":                                            "5930ca9acf0bc741fb4dd827214f8db7",
	"TSCTX_8bit_RExt_SHARP_1":                                              "00fa6cd027d3a37cc0d10e5575d955d2",
	"TSUNEQBD_A_MAIN10_Technicolor_2":                                      "1bed23df7253170eeb8efc3fd1b740a3",
	"WAVETILES_RExt_Sony_2":                                                "d4809354218e6747c70aed951b80acc9",
	"WPP_AND_TILE_10Bit422Test_HIGH_TP_444_10BIT_RExt_Apple_2":             "32e6b91c9497e161fe66c1fbd3cec51e",
	"WPP_AND_TILE_AND_CABAC_BYPASS_ALIGN_0_HIGH_TP_444_14BIT_RExt_Apple_2": "e134676d233f3f5b49ab4c252d5d18be",
	"WPP_AND_TILE_AND_CABAC_BYPASS_ALIGN_1_HIGH_TP_444_14BIT_RExt_Apple_2": "5347f09153fd54b24a37427ca0bd9b30",
	"WPP_AND_TILE_AND_CABAC_EXT_PREC_1_HIGH_TP_444_14BIT_RExt_Apple_2":     "95f0019aed6c9c845fe5e2088aefdc39",
	"WPP_AND_TILE_HIGH_TP_444_8BIT_RExt_Apple_2":                           "e134676d233f3f5b49ab4c252d5d18be",
	"WPP_HIGH_TP_444_8BIT_RExt_Apple_2":                                    "3c94b5ebc0aed0abae4e619b9dcca9cc",
}

// rextWordLayout is the set of streams whose suite file stores every plane as 16-bit words
// because one of the two depths is above 8 (HM writes one file bit depth for the whole file).
// For every other stream the planes are packed at their own depth, which is the same thing
// whenever the two depths are both 8 or both above 8 (measured: Bitdepth_A and Bitdepth_B are
// the only two whose packed hash differs from the suite's; TSUNEQBD at 10 and 9 bits matches
// packed). A Frame carries each plane at its own width; the test widens the byte plane.
var rextWordLayout = map[string]bool{
	"Bitdepth_A_RExt_Sony_1": true, // 12 bit luma, 8 bit chroma
	"Bitdepth_B_RExt_Sony_1": true, // 8 bit luma, 12 bit chroma
}

// suiteSum is the MD5 of the output of frames in the suite's layout: planes one after another
// per picture; with words, a byte plane widened to little-endian words.
func suiteSum(frames []Frame, words bool) string {
	h := md5.New()
	for _, f := range frames {
		put(h, f.Y, f.SampleBytes(0), words)
		put(h, f.Cb, f.SampleBytes(1), words)
		put(h, f.Cr, f.SampleBytes(2), words)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func put(h hash.Hash, plane []byte, bytes int, words bool) {
	if !words || bytes != 1 {
		h.Write(plane)
		return
	}
	wide := make([]byte, 2*len(plane))
	for i, b := range plane {
		wide[2*i] = b
	}
	h.Write(wide)
}

// TestHEVCConformanceRExt decodes the JCT-VC range extension bitstreams (ITU-T H.265.1, the
// HEVC_v1 and RExt directories of the same site as TestHEVCConformance), whose formats a Frame
// carries since the design of docs/design/frame-chroma-formats.md, and compares the MD5 of the
// whole output with the one the suite ships. The picture hash SEI inside each stream is a second,
// independent oracle: h265 checks it.
func TestHEVCConformanceRExt(t *testing.T) {
	dir := os.Getenv("DECODE_HEVC_DIR")
	if dir == "" {
		t.Skip("DECODE_HEVC_DIR is not set")
	}
	for name, want := range rextSuite {
		t.Run(name, func(t *testing.T) {
			stream := conformanceStream(dir, name)
			if stream == "" {
				t.Skipf("%s is not in %s", name, dir)
			}
			d, err := NewHEVC(Config{MaxFrameMBs: 1 << 17}, 4)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			frames, err := d.DecodeAnnexB(readFile(t, stream))
			if err == nil {
				var rest []Frame
				rest, err = d.Flush()
				frames = append(frames, rest...)
			}
			if err != nil || len(frames) == 0 {
				t.Fatalf("%d pictures, %v", len(frames), err)
			}
			got, words := suiteSum(frames, false), suiteSum(frames, true)
			f := frames[0]
			t.Logf("%d pictures of %dx%d, chroma %d, %d/%d bits: packed %s, words %s, the suite's %s",
				len(frames), f.Width, f.Height, f.Chroma, f.BitDepth, f.BitDepthC, got, words, want)
			if rextWordLayout[name] {
				got = words
			}
			if got != want {
				t.Fatalf("output md5 %s (every plane as words: %s), the suite's is %s", got, words, want)
			}
		})
	}
}

func TestHEVCConformanceRExtTableIsWhole(t *testing.T) {
	if len(rextSuite) != 47 {
		t.Errorf("%d streams, want the 47 whose hash the suite ships and whose bitstream is named as its directory", len(rextSuite))
	}
	for name, sum := range rextSuite {
		if _, err := hex.DecodeString(sum); err != nil || len(sum) != 32 {
			t.Errorf("%s: %q is not an MD5", name, sum)
		}
	}
	for name := range suiteOracle {
		if _, ok := rextSuite[name]; ok {
			t.Errorf("%s is already an oracle of TestHEVCConformance", name)
		}
	}
	for name := range rextWordLayout {
		if _, ok := rextSuite[name]; !ok {
			t.Errorf("%s has a layout but no hash", name)
		}
	}
}

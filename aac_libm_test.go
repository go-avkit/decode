// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

// The sine windows of ffmpeg's AAC decoder are computed with the C library's
// single precision sine (libavcodec/sinewin_tablegen.h:63: sinf of the argument),
// which is not correctly rounded: on macOS (libm of macOS 26, arm64, the platform
// of the reference decodes in testdata/aac/ref) it differs from the correctly
// rounded float32 sine in these entries, by one ulp. Those are the entries of the
// rising half of the 2048-point and 256-point sine windows (index to float32
// bits), obtained by calling sinf on that platform with the same argument
// ((i+0.5)*pi/(2n), as a float): a measurement of the library, not of any decode.
//
// The decoder uses the correctly rounded value, which does not depend on the
// platform. TestAACOracle puts these entries in the tables to show that they, and
// nothing else, are what the decoder's output differs by from ffmpeg's.
var (
	aacLibmSineLong = map[int]uint32{
		57: 0x3db4684e, 223: 0x3eac1e29, 289: 0x3edbf8fd, 309: 0x3eea0d1c,
		311: 0x3eeb727a, 322: 0x3ef315c2, 356: 0x3f051f75, 375: 0x3f0b7046,
		441: 0x3f206c6b, 446: 0x3f21f2ec, 461: 0x3f2677b7, 462: 0x3f26c40a,
		481: 0x3f2c5ae8, 482: 0x3f2ca530,
	}
	aacLibmSineShort = map[int]uint32{
		15: 0x3e419b38, 19: 0x3e72b651, 24: 0x3e97a117,
	}
)

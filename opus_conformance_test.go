// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// The conformance gate for Opus is the one the specification defines. RFC 6716
// section 6: a decoder's output "MUST also be within the thresholds specified by
// the opus_compare.c tool ... when compared to the reference implementation for
// each of the test vectors provided and for each output sampling rate and channel
// count supported". RFC 8251 section 11 replaces the vectors: each has two
// reference outputs, with and without the CELT 180 degree phase shift, and "an
// implementation is compliant as long as it passes either set".
//
// The vectors are the 12 of opus_testvectors-rfc8251.tar.gz (74 MB, IETF Trust
// Simplified BSD licence), too large to commit: set OPUS_VECTORS_DIR to the
// directory holding testvector01.bit and its .dec files. The test is skipped
// without it; CI downloads the archive and checks its SHA-256. Every file is
// checked against the SHA-1 RFC 8251 section 11 lists before it is used.
//
// Not checked: the final range decoder state (RFC 6716 section 6), which pion/opus
// does not expose.
var opusVectorSHA1 = map[string]string{
	"testvector01.bit": "e49b2862ceec7324790ed8019eb9744596d5be01", "testvector02.bit": "b809795ae1bcd606049d76de4ad24236257135e0",
	"testvector03.bit": "e0c4ecaeab44d35a2f5b6575cd996848e5ee2acc", "testvector04.bit": "a0f870cbe14ebb71fa9066ef3ee96e59c9a75187",
	"testvector05.bit": "9b3d92b48b965dfe9edf7b8a85edd4309f8cf7c8", "testvector06.bit": "28e66769ab17e17f72875283c14b19690cbc4e57",
	"testvector07.bit": "bacf467be3215fc7ec288f29e2477de1192947a6", "testvector08.bit": "ddbe08b688bbf934071f3893cd0030ce48dba12f",
	"testvector09.bit": "3932d9d61944dab1201645b8eeaad595d5705ecb", "testvector10.bit": "521eb2a1e0cc9c31b8b740673307c2d3b10c1900",
	"testvector11.bit": "6bc8f3146fcb96450c901b16c3d464ccdf4d5d96", "testvector12.bit": "338c3f1b4b97226bc60bc41038becbc6de06b28f",
	"testvector01.dec": "f5ef93884da6a814d311027918e9afc6f2e5c2c8", "testvector02.dec": "48ac1ff1995250a756e1e17bd32acefa8cd2b820",
	"testvector03.dec": "d15567e919db2d0e818727092c0af8dd9df23c95", "testvector04.dec": "1249dd28f5bd1e39a66fd6d99449dca7a8316342",
	"testvector05.dec": "b85675d81deef84a112c466cdff3b7aaa1d2fc76", "testvector06.dec": "55f0b191e90bfa6f98b50d01a64b44255cb4813e",
	"testvector07.dec": "61e8b357ab090b1801eeb578a28a6ae935e25b7b", "testvector08.dec": "a58539ee5321453b2ddf4c0f2500e856b3966862",
	"testvector09.dec": "bb96aad2cde188555862b7bbb3af6133851ef8f4", "testvector10.dec": "1b6cdf0413ac9965b16184b1bea129b5c0b2a37a",
	"testvector11.dec": "b1fff72b74666e3027801b29dbc48b31f80dee0d", "testvector12.dec": "98e09bbafed329e341c3b4052e9c4ba5fc83f9b1",
	"testvector01m.dec": "1e7d984ea3fbb16ba998aea761f4893fbdb30157", "testvector02m.dec": "48ac1ff1995250a756e1e17bd32acefa8cd2b820",
	"testvector03m.dec": "d15567e919db2d0e818727092c0af8dd9df23c95", "testvector04m.dec": "1249dd28f5bd1e39a66fd6d99449dca7a8316342",
	"testvector05m.dec": "d70b0bad431e7d463bc3da49bd2d49f1c6d0a530", "testvector06m.dec": "6ac1648c3174c95fada565161a6c78bdbe59c77d",
	"testvector07m.dec": "fc5e2f709693738324fb4c8bdc0dad6dda04e713", "testvector08m.dec": "aad2ba397bf1b6a18e8e09b50e4b19627d479f00",
	"testvector09m.dec": "6feb7a7b9d7cdc1383baf8d5739e2a514bd0ba08", "testvector10m.dec": "1b6cdf0413ac9965b16184b1bea129b5c0b2a37a",
	"testvector11m.dec": "fd3d3a7b0dfbdab98d37ed9aa04b659b9fefbd18", "testvector12m.dec": "98e09bbafed329e341c3b4052e9c4ba5fc83f9b1",
}

func readVector(t testing.TB, dir, name string) []byte {
	t.Helper()
	b := readFile(t, filepath.Join(dir, name))
	if got := fmt.Sprintf("%x", sha1.Sum(b)); got != opusVectorSHA1[name] {
		t.Fatalf("%s: SHA-1 %s, RFC 8251 lists %s", name, got, opusVectorSHA1[name])
	}
	return b
}

func pcm16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}

// opusDemoPackets reads the .bit format of opus_demo: per packet a big-endian length
// and the encoder's final range, then the packet.
func opusDemoPackets(t testing.TB, b []byte) [][]byte {
	t.Helper()
	var out [][]byte
	for len(b) > 0 {
		if len(b) < 8 {
			t.Fatal("truncated packet header")
		}
		n := int(binary.BigEndian.Uint32(b))
		if n > len(b)-8 {
			t.Fatal("truncated packet")
		}
		out = append(out, b[8:8+n])
		b = b[8+n:]
	}
	return out
}

// opusDecodeVector decodes the packets to 16-bit PCM as opus_demo does: the float
// output scaled by 32768, rounded to nearest even and clamped.
func opusDecodeVector(t testing.TB, packets [][]byte, channels int) []int16 {
	t.Helper()
	d, err := NewAudioTrack(opusTrack(opusHead(channels, 0, 0, 0)), AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var out []int16
	for i, p := range packets {
		as, err := d.DecodeSample(container.Sample{Data: p})
		if err != nil {
			t.Fatalf("packet %d: %v", i, err)
		}
		for _, a := range as {
			for j := 0; j+4 <= len(a.Data); j += 4 {
				v := math.RoundToEven(float64(math.Float32frombits(binary.LittleEndian.Uint32(a.Data[j:]))) * 32768)
				out = append(out, int16(max(-32768, min(32767, v))))
			}
		}
	}
	return out
}

// TestOpusConformance: opus_compare must pass on every vector, for stereo and for
// mono output at 48 kHz, against either reference set. The scores are logged.
func TestOpusConformance(t *testing.T) {
	dir := os.Getenv("OPUS_VECTORS_DIR")
	if dir == "" {
		t.Skip("OPUS_VECTORS_DIR is not set")
	}
	for n := 1; n <= 12; n++ {
		t.Run(fmt.Sprintf("testvector%02d", n), func(t *testing.T) {
			t.Parallel()
			base := fmt.Sprintf("testvector%02d", n)
			packets := opusDemoPackets(t, readVector(t, dir, base+".bit"))
			refs := map[string][]int16{
				"phase shift":    pcm16(readVector(t, dir, base+".dec")),
				"no phase shift": pcm16(readVector(t, dir, base+"m.dec")),
			}
			for _, ch := range []int{2, 1} {
				got := opusDecodeVector(t, packets, ch)
				best, bestName := math.Inf(-1), ""
				for name, ref := range refs {
					q, err := opusCompare(ref, got, ch, 48000)
					if err != nil {
						t.Fatalf("%d channels against the %s reference: %v", ch, name, err)
					}
					t.Logf("%d channels against the %s reference: quality %.1f %%", ch, name, q)
					if q > best {
						best, bestName = q, name
					}
				}
				if best < 0 {
					t.Errorf("%d channels: opus_compare FAILS on both reference sets (best %.1f %%)", ch, best)
				} else {
					t.Logf("%d channels: PASSES, quality %.1f %% (%s)", ch, best, bestName)
				}
			}
		})
	}
}

func TestOpusVectorHashesAreHexSHA1(t *testing.T) {
	if len(opusVectorSHA1) != 36 {
		t.Fatalf("%d hashes, RFC 8251 lists 36", len(opusVectorSHA1))
	}
	for name, h := range opusVectorSHA1 {
		if b, err := hex.DecodeString(h); err != nil || len(b) != sha1.Size {
			t.Errorf("%s: %q is not a SHA-1", name, h)
		}
	}
}

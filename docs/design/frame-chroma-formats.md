# Frame: every chroma format and bit depth

Status: design, no code. For the owner's review. Written 2026-11-20 against decode `main` at
06eebc8 (h265 v0.23.0) and h265 `main` at 1eafca9; revised the same day for decode v0.14.0
(`Frames`, 8898999) and h265 v0.24.0 (`Options.RecyclePictures`, `DecodedPicture.Release`).
The `hevc.go` line numbers below are those of 06eebc8.

## Why

decode v0.13.0 refuses at the sequence parameter set every HEVC format a `Frame` is not said to
carry (`hevcFormat`, `hevc.go:227-240`; `hevcFrame` repeats the check per picture,
`hevc.go:264-270`): 4:0:0, 4:2:2, 4:4:4, any depth but 8 and 10, and unequal luma and chroma
depths. Of the 196 JCT-VC streams (HEVC_v1 147, RExt 49), 43 are refused that way (README,
"h265 v0.23.0 against v0.22.0"): 42 RExt streams and `TSUNEQBD_A_MAIN10_Technicolor_2`. h265
decodes all of them; six of them are also High Throughput 4:4:4 16 Intra streams that use
`cabac_bypass_alignment_enabled_flag`, which h265 v0.23.x decodes with every picture hash
matching.

The aim is a `Frame` that carries every format h265 decodes, with no change at all for the
4:2:0 8- and 10-bit frames callers get today, and the same `Frame` for H.264 once go-avkit/h264
decodes its High 10, High 4:2:2 and High 4:4:4 profiles.

## What the specifications allow

Read for this design (the published PDFs, not committed):

* **H.265 (V11, 01/2026), clause 6.2, Table 6-1.** `chroma_format_idc` 0 is monochrome (one
  array, nominally luma), 1 is 4:2:0 (SubWidthC = SubHeightC = 2), 2 is 4:2:2 (SubWidthC 2,
  SubHeightC 1), 3 is 4:4:4 (both 1), the last also with `separate_colour_plane_flag` 1. The
  same clause says each array is 8 to 16 bits and that luma and chroma may differ.
* **H.265 clause 7.4.3.2.1**, `bit_depth_luma_minus8` and `bit_depth_chroma_minus8` (7-4, 7-6):
  each 0 to 8, independently. The conformance window (same clause) is counted in units of
  SubWidthC and SubHeightC, and the chroma samples output are those at (x / SubWidthC,
  y / SubHeightC): a cropped luma size is therefore a whole number of chroma samples.
* **H.265 clause A.3.5, Table A.1** (format range extensions profiles): the profiles bound
  `bit_depth_luma_minus8` and `bit_depth_chroma_minus8` in one column, each to the same range
  (0 for Monochrome and Main 4:4:4; 0..2 for the "10" profiles; 0..4 for the "12" ones; 0..8 for
  Monochrome 16, Main 4:4:4 16 Intra and Main 4:4:4 16 Still Picture). Nothing requires them to
  be equal. The same clause requires `separate_colour_plane_flag` equal to 0 for every RExt
  profile.
* **H.264 (06/2026), clause 6.2, Table 6-1**: the same four formats; chroma arrays of a
  4:4:4 picture coded as separate colour planes are three monochrome pictures. Depths are 8 to
  14 and luma and chroma may differ. **Clause A.2.4** (High): `chroma_format_idc` 0 or 1, 8 bits.
  **A.2.6** (High 4:2:2): 0 to 2, each depth 8 to 10. **A.2.7** (High 4:4:4 Predictive): every
  format, each depth 8 to 14, independently. **7.4.2.1.1 (7-19 to 7-22)**: the crop unit is
  SubWidthC (1 for monochrome), so a cropped size is a whole number of chroma samples here too.

Measured in the conformance packages (ffmpeg 9.0.2, one thread, `-f md5` of the native pixel
format):

| Stream | Format | Suite `_rec.yuv` MD5 | ffmpeg |
|---|---|---|---|
| `GENERAL_8b_400_RExt_Sony_1` | 4:0:0, 8 | `e5223be3…` | `gray`: equal. As `yuv420p` (neutral chroma): `5ee22f2c…`, not equal |
| `GENERAL_12b_400_RExt_Sony_1` | 4:0:0, 12 | `549ff2b9…` | `gray12le`: equal |
| `GENERAL_12b_422_RExt_Sony_1` | 4:2:2, 12 | `d4f51ab5…` | `yuv422p12le`: equal |
| `GENERAL_16b_400_RExt_Sony_1`, `GENERAL_16b_444_RExt_Sony_2` | 16 bits | | refused: "8, 9, 10 and 12 bits" |
| `Bitdepth_A_RExt_Sony_1` | luma 12, chroma 8 | | refused: depths differ |
| `Bitdepth_B_RExt_Sony_1` | luma 8, chroma 12 | | refused: depths differ |

So the suite hashes planes packed one after another, little-endian words above 8 bits, and
**luma only for 4:0:0**. That is the `Frame` layout of today extended as below; no other choice
would let the suite's own MD5 be the oracle. Which layout the suite uses for *unequal* depths
(each plane at its own size, or every plane as words) is not known yet: ffmpeg cannot decode
those streams. Step 2 measures it (both are cheap to compute from a `Frame`); it does not decide
the `Frame` layout.

## The layout

A `Frame` keeps `Y, Cb, Cr []byte` and gets one field:

```go
// BitDepthC is the chroma bit depth: 8 to 16, and 0 for a Chroma400 frame.
BitDepthC int
```

Every plane is packed (stride = width × bytes per sample). The bytes per sample of a plane
follow **that plane's** depth: 1 when it is 8, 2 (a little-endian word) for 9 to 16.

| Chroma | Y | Cb, Cr (each) | YStride | CStride |
|---|---|---|---|---|
| `Chroma420` | W × H | ⌈W/2⌉ × ⌈H/2⌉ | W·bY | ⌈W/2⌉·bC |
| `Chroma422` | W × H | ⌈W/2⌉ × H | W·bY | ⌈W/2⌉·bC |
| `Chroma444` | W × H | W × H | W·bY | W·bC |
| `Chroma400` | W × H | nil | W·bY | 0 |

bY = 1 if `BitDepth` = 8 else 2; bC = 1 if `BitDepthC` = 8 else 2. The ceilings equal the exact
quotients for every conforming stream (the crop rules above); they are written as ceilings
because that is what `decode.go:305` and `hevc.go:271` compute today.

Decisions in that table, and the alternatives set aside:

* **Samples per plane, not per frame.** `Bitdepth_B` (luma 8, chroma 12) gets a byte luma plane
  and word chroma planes. That is h265's own rule (`NewDecodedPicture`, `recon.go:17-39`,
  allocates each plane at its component's depth; `Plane.Pix8` iff that depth is 8,
  `pixel.go:12-14`), so every byte plane stays a `memmove` and nothing is widened.
  *Rejected: words for every plane whenever either depth exceeds 8* — one sample size per frame
  is simpler to read, but it widens an 8-bit plane for nothing, and the suite's layout for
  unequal depths, the only argument for it, is unmeasured.
* **4:0:0 has no chroma planes**: `Cb`, `Cr` nil, `CStride` 0, `BitDepthC` 0. The doc comment
  already says so (`decode.go:134-136`), the suite hashes luma only, and ffmpeg's `gray*`
  framemd5 is luma only. *Rejected: neutral chroma (1 << (depth−1))* — it invents samples the
  stream does not have, costs half a picture of writes, and matches neither oracle (measured
  above). A caller that wants 4:2:0 out of a 4:0:0 frame fills it itself.
* **`[]byte` stays the sample type**, with little-endian words (ffmpeg's `*le` formats, and the
  suite's files). *Rejected: `Y16, Cb16, Cr16 []uint16` beside the byte fields* — the
  "exactly one non-nil" pattern of h265's `Plane` is right for a decoder's working planes, but a
  `Frame` is an output format; the bytes are what a writer, a hash or a GPU upload wants, and
  the copy into words is free on little-endian hosts (see Copy cost).
* **`separate_colour_plane_flag` 1** (H.265 4:4:4 outside the RExt profiles, H.264 High 4:4:4)
  would be `Chroma444` with three full planes: the output picture has three arrays either way.
  It stays refused with `ErrUnsupported` until h265 reconstructs it into three planes (it parses
  the flag, `sps.go:154-155`, and the slice's `colour_plane_id`; no witness has been decoded).
* **No accessor methods** are added (a `SampleBytes(plane)` or `ChromaSize()`): the strides
  already carry the bytes, and the table is three lines. Open question 2.

## Compatibility

Nothing is removed, renamed or retyped; every program that compiles against v0.13.0 compiles
against the next release.

Unchanged, byte for byte and field for field: every frame decode returns today, which is
4:2:0 with `BitDepth` = `BitDepthC` ∈ {8, 10} (HEVC) and 4:2:0 8-bit (H.264). For those frames
the new rule (Y by `BitDepth`, chroma by `BitDepthC`) reads exactly as the old one ("one byte
when `BitDepth` is 8"). The witnesses are the ones that gate `main` now: the 143 ffmpeg manifests
of `testdata/hevc-conformance`, the three `suiteOracle` MD5s, the H.264 manifests, and
`testdata/hevc-real-files.txt`, all required unchanged by every step.

Additive: the field `BitDepthC` (set on every frame, H.264 included, where it is 8).

`Frames` (decode v0.14.0, `avcc.go:226-256`) yields the same `Frame` values as `DecodeTrack`,
so it needs nothing of its own: every rule here applies to it unchanged.

**Behaviour change** — and the reason the release is a MINOR one (v0.15.0, after v0.14.0; below
v1 a break is a minor bump):

1. Streams refused with `ErrUnsupported` now return frames whose `Chroma` is not `Chroma420`,
   whose `BitDepth` is 9 or 11 to 16, or whose `BitDepthC` differs from `BitDepth`. A caller that
   took "a Frame is 4:2:0 at 8 or 10 bits" for granted, and did not check `Chroma`, will misread
   them. The field-by-field conversion to go-videodsp/yuv.Picture (`decode.go:129-132`) holds for
   `Chroma420` with both depths 8 only; the doc comment says so.
2. The sentence of `Frame` "a sample is one byte when BitDepth is 8" becomes per plane. A caller
   that sizes chroma from `BitDepth` is wrong only on unequal depths, which it never received.
3. `ErrUnsupported` no longer names a chroma format or a depth; it still covers separate colour
   planes, SCC and 3D.

Who is affected: GitHub code search (2026-11-20) finds no `go.mod` outside go-avkit/decode that
requires it; decode's own `tools/jvt` and tests are updated in the same step. An opt-in
(`Config` field that keeps the old refusal by default) would make the change strictly additive;
it is not proposed (open question 1).

## H.264

The same `Frame`, nothing more: H.264's formats (4:0:0 to 4:4:4, 8 to 14 bits, unequal depths
allowed by A.2.7) are a subset of H.265's. What changes is upstream and in `Decoder.frame`
(`decode.go:304-326`), which today hard-codes 4:2:0 and 8 bits:

* go-avkit/h264's public `decoder.Frame` (`decoder/decoder.go:33-41`) has `Y, Cb, Cr []byte` and
  one `StrideC`; its decoder refuses every chroma format but 4:2:0 and every depth but 8
  (`internal/decoder/decoder.go` `checkSupported`). When it decodes more it needs to say the
  format (`ChromaFormat`, the two depths) and hold deeper samples; decode then fills `Frame`
  with the same table and the same `packPlane`. The design does not prescribe h264's type; if
  it follows h265 (`Pix8`/`Pix16` per plane), decode's copy is shared code.
* High profile 4:0:0 (A.2.4) is the first one that needs nothing but h264's decoding: `Chroma400`,
  no chroma planes.
* Until then decode's H.264 frames set `BitDepthC` = 8 and nothing else moves. The format check
  stays h264's (`ErrUnsupported` from `checkSupported`).

## Copy cost

decode copies every picture out of h265's reference pictures (a `Frame` owns its planes,
README "A Frame owns its planes"). Per plane:

* **8-bit plane** (any plane at depth 8, luma of `Bitdepth_B` included): one `copy` per row, a
  `memmove`, as `packPlane` does now (`hevc.go:320-322`). When the source stride equals the width
  the whole plane is one `copy`.
* **16-bit plane**: today a loop packs four samples into a `uint64` and stores it little-endian
  (`hevc.go:324-333`). On a little-endian host the bytes of a `[]uint16` row already are that
  layout, so the row is a `memmove` of `2·W` bytes through an `unsafe.Slice` view of the source
  (decode already imports `unsafe`, `aac_sbr_hf.go`). Big-endian hosts keep the loop; the
  choice is made once (`binary.NativeEndian`), and the s390x lane of CI (`ci.yml:161`, the
  byte-order gate) runs the loop's tests on a big-endian machine.

**With h265's recycling** (v0.24.0, `recycle.go`; another decode PR turns it on): the copy
stays, since a `Frame` owns its planes. What it adds is an order: every plane of every format is
copied *before* `Release`, which empties the picture (`Planes` zeroed) and hands its memory to
the next picture. The pool already covers every format: its key is the number of planes, each
plane's size and whether it is a byte plane (`poolKey`, `shapeOf`), so a 4:0:0, 4:2:2, 4:4:4 or
unequal-depth stream recycles like a 4:2:0 one. Step 2 builds on that PR and tests one stream of
each layout row with recycling on, holding every `Frame` to the end and comparing each one's
MD5 with the run without recycling, so a plane read after `Release` (or a `Frame` that aliases
reused memory) shows as a mismatch. (h265's own poison switch is unexported and stays its test.)

Measured in step 3, single core, interleaved rounds, fractal (x86) and an M4: the per-frame copy
of a 1080p 4:2:0 10-bit and a 1080p 4:4:4 16-bit picture, the loop against the `memmove`, and
its share of a whole decode (h265's decode time is the denominator; ffmpeg decodes up to 12 bits
and is the reference wherever it decodes the stream). The `memmove` lands only if it is faster
on both machines; it also speeds up the Main 10 frames callers get today, with the same bytes.

## Security

Bounds, all in `int`, which is 32 bits on 386 (CI's `arch-386` lane, `ci.yml:182-191`) and
32-bit arm (no lane today):

* **Size.** h265 is given `MaxPixels = min(MaxFrameMBs·256, 2³¹−1)` luma samples
  (`hevc.go:22-25, 41`), so W·H fits an `int`. The bytes do not: a frame is
  W·H·bY + 2·⌈W/SubW⌉·⌈H/SubH⌉·bC, up to **6·W·H** (4:4:4, 16 bits). On 32 bits that wraps once
  W·H > 357 913 941. **Finding, present today:** `hevcFrame` computes `w*h*step`
  (`hevc.go:280`) in `int`; with `step` 2 it wraps once W·H > 2³⁰, which `maxFrameSamples` admits
  when a caller raises `MaxFrameMBs` above 2²² — a negative length, a panic on input. The
  default (36 864 MBs, 9.4 M samples, 57 MB at 4:4:4 16) is far below it. Fix, in step 2: the
  frame size is computed in `uint64` from the SPS (coded size, chroma format, both depths) and
  each plane and the total are checked against a platform-independent cap of 2³¹−1 bytes, so 32-
  and 64-bit hosts refuse the same streams, with `ErrOverLimit`, **at the SPS** before a picture
  is decoded, and again per picture in `hevcFrame` from the planes h265 hands over. Division
  before multiplication (`w > cap / h / b`), never a product compared after the fact.
* **Shape.** The picture's planes are checked against its format before any copy: `Components`
  is 1 for 4:0:0 and 3 otherwise; chroma dimensions equal ⌈W/SubW⌉ × ⌈H/SubH⌉; each depth is 8 to
  16 and matches the plane's kind (`Pix8` iff 8 — never index `Pix16` of a byte plane); every row
  lies in its slice (`planesFit`, `hevc.go:306-314`, generalised to `Components` planes, its
  `(H−1)·Stride + W` computed without overflow). A mismatch is `ErrBitstream`, as today.
* **Untrusted values** reach this code only through h265's SPS parse (chroma format 0 to 3, each
  depth minus 8 at most 8); decode re-checks the ranges rather than trusting them.

## Tests

* **Unchanged-output gate** (every step): the manifests and MD5s listed under Compatibility, as
  CI runs them now.
* **Conformance witnesses** (`TestHEVCConformance`, when `DECODE_HEVC_DIR` is set). For each
  newly decoded stream the whole output's MD5 is compared with the suite's `md5sum.txt` /
  `*.md5` (committed as `testdata/hevc-conformance-rext/NAME.suite` — the hash, never the
  bitstream, whose redistribution terms are not stated), and every picture with ffmpeg's
  framemd5 wherever ffmpeg decodes it (equal depths, at most 12 bits). The bypass-alignment
  16-bit streams have no ffmpeg oracle; the suite MD5 and h265's picture hash SEI are two
  independent ones. Witnesses per format, by stream name; step 2's first commit prints the
  census (format and depths of each of the 43) and the table is corrected from it:

  | Format | Witnesses |
  |---|---|
  | 4:0:0 8 / 12 / 16 | `GENERAL_8b_400`, `GENERAL_12b_400`, `GENERAL_16b_400` |
  | 4:2:0 12 | `GENERAL_12b_420` |
  | 4:2:0 unequal (10/9) | `TSUNEQBD_A_MAIN10_Technicolor_2` (leaves `suiteRefused`) |
  | 4:2:2 10 / 12 | `Main_422_10_A`, `Main_422_10_B`, `GENERAL_10b_422`, `GENERAL_12b_422` |
  | 4:4:4 8 / 10 / 12 / 16 | `GENERAL_8b_444`, `GENERAL_10b_444`, `GENERAL_12b_444`, `GENERAL_16b_444`, `EXTPREC_MAIN_444_16_INTRA_*` |
  | 4:4:4 16, bypass alignment | `GENERAL_16b_444_highThroughput`, `EXTPREC_HIGHTHROUGHPUT_444_16_INTRA_*`, `WAVETILES_RExt_Sony_2` |
  | 4:4:4 unequal (12/8, 8/12) | `Bitdepth_A_RExt_Sony_1`, `Bitdepth_B_RExt_Sony_1` |

  The gate counts: 196 streams, 0 refused for a format, and the manifests test pins the new
  counts by name (not a bare number).
* **Unequal depths' suite layout**: the test hashes both layouts (per plane, and all words) and
  states which one the suite's MD5 is; the doc then records it. If neither matches, the stream
  stays a witness of h265's picture hash only, and that is written down.
* **Unit tests** of `hevcFrame` (`hevc_frame_test.go`): one synthetic `h265.Output` per row of the
  layout table and per depth pair (8/8, 8/12, 12/8, 10/10, 16/16), every sample read back with
  `Plane.At` and compared with the bytes; the refusal cases of today's `"unequal depths"`,
  `"12 bits"`, `"16 bits"` rows become acceptance cases, and new refusals cover a wrong chroma
  size, a depth outside 8 to 16, a `Pix8` plane declared at 10 bits, and the size cap.
* **Fuzz** (corpus committed): `FuzzHEVCFrame` over a synthetic `Output` (W, H, chroma, two
  depths, strides, plane kinds, slice lengths): either an error or a frame whose lengths and
  strides equal the table and whose samples equal `At`; it is the fuzzer for the copy, the shape
  check and the size arithmetic. The SPS-level check is fuzzed over SPS units with every
  `chroma_format_idc` and depth (`fuzz_test.go` already sizes samples by `(BitDepth+7)/8`; it
  gets `BitDepthC`). The 16-bit `memmove` path and the loop are fuzzed against each other.
* **Bomb test**: an SPS declaring 4:4:4 16 bits just over the cap asserts `ErrOverLimit` before
  any allocation, run under `GOMEMLIMIT` and `ulimit -v`, and in the 386 lane.

## Steps (one PR each, to decode unless said)

1. **`BitDepthC`.** The field, set on every frame (H.264 8, HEVC equal to `BitDepth`); the doc
   comment's per-plane rule; nothing else. Additive; output unchanged (the gate above).
2. **Every HEVC format.** `hevcFormat` and `hevcFrame` accept 4:0:0, 4:2:2, 4:4:4, depths 8 to 16
   and unequal depths per the table; the size cap at the SPS and per picture (fixing the 32-bit
   wrap above); the shape checks; unit tests, fuzz, bomb test; the conformance witnesses and the
   unequal-depth layout measurement; README's format table and conformance paragraph;
   release v0.15.0 with the Behaviour change note. It is rebased on the h265 v0.24.0 PR with
   recycling on, and copies before `Release`.
3. **16-bit `memmove`.** The little-endian copy, measured as above; lands only if faster.
4. **Docs and landing.** The org landing page and the docs site say what decode now carries
   (one PR each in their repositories), checked on the published pages.
5. **H.264**, when go-avkit/h264 decodes a new format: decode's `frame` reads the format from
   h264's picture and fills the same `Frame`. A separate design in h264 for its picture type.

## Open questions for the owner

1. **Opt-in or not.** Accepting the new formats by default is the behaviour change of step 2.
   A `Config` flag keeping the refusal by default would make it purely additive, at the price of
   a permanent knob. Proposed: no flag, a MINOR release and a Behaviour change note.
2. **Accessors.** Add `(Frame).SampleBytes(plane int)` and `(Frame).ChromaSize()` now, or leave the
   table in the doc comment? Proposed: leave them out until a caller needs them.
3. **4:0:0 `BitDepthC`.** 0 (no chroma planes to describe) or the SPS's `bit_depth_chroma`, which
   the stream states even for monochrome? Proposed: 0.
4. **Separate colour planes.** Keep refusing until h265 has a witness, or ask h265 for it now
   (none of the 196 streams is known to need it; RExt profiles forbid it)?

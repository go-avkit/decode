# decode

[![ci](https://github.com/go-avkit/decode/actions/workflows/ci.yml/badge.svg)](https://github.com/go-avkit/decode/actions/workflows/ci.yml)

Video decoders for [avkit](https://github.com/go-avkit/avkit), pure Go, no cgo.
The first codec is **H.264** (ITU-T H.264 | ISO/IEC 14496-10), the second
**HEVC / H.265** (ITU-T H.265 | ISO/IEC 23008-2): Main and Main 10, 4:2:0.
Waves D1 and D2 of [go-avkit/avkit#89](https://github.com/go-avkit/avkit/issues/89).

```go
r, _ := container.NewReader(file)                   // MP4, Matroska, ...
frames, err := decode.DecodeTrack(r, trackID, decode.Config{})
for _, f := range frames {                          // display order
    use(f.Y, f.Cb, f.Cr, f.YStride, f.CStride, f.Width, f.Height, f.PTS)
}
```

One picture, for a poster or a thumbnail, without decoding the track:

```go
id, _ := decode.FirstVideoTrack(r)
f, err := decode.PosterFrame(r, id, 12.5, decode.PosterConfig{}) // the picture shown at 12.5 s
```

`PosterFrame` returns the picture with the greatest presentation time at or
before `t` (decode time plus composition offset, as `DecodeTrack` labels them), and
the very bytes `DecodeTrack` returns for it. A `t` past the end gives the last
picture, one before the first presentation time gives the first, a negative or NaN
`t` is `ErrTime`. Decoding starts at the last sync sample at or before the
picture's sample (the first sample if none is flagged) and stops as soon as the
picture is out. H.264 (`avc1`, `avc3`) and HEVC (`hvc1`, `hev1`); another codec is
`ErrUnsupported`.

Streaming: `decode.NewTrack(trackConfig, cfg)` or `decode.NewAVCC(avcC, cfg)`,
then `DecodeSample(sample.Data, pts)` per sample and `Flush()` at the end.
Raw `.264` files: `decode.New(cfg, 4)` and `DecodeAnnexB`.

`Frame` carries the planes, strides, size, `Chroma` format, `BitDepth` and
`PTS`. Its field names match `go-videodsp/yuv.Picture` for 8-bit 4:2:0, so
converting to RGBA is a field copy; `yuv.Picture` has no bit depth or time, which
is why it is not used as is.

HEVC takes the same calls: `DecodeTrack` and `NewTrack` choose HEVC for a track
whose sample entry is `hvc1` or `hev1`; `decode.NewHVCC(hvcC, cfg)` takes an
hvcC record (`ParseHVCC` reads one), `decode.NewHEVC(cfg, 4)` takes in-band
parameter sets and `DecodeAnnexB` a raw `.265` stream. Main 10 frames hold
little-endian 16-bit samples (ffmpeg's `yuv420p10le`). Each picture's `PTS` is
that of the sample holding its first slice, so it is exact whatever the
reordering. `Frame.Corrupt` marks a picture the decoder lost a slice of.
`Config.MaxFrameMBs` counts 256 luma samples for HEVC; `EnforceLevel` applies to
H.264 only.

## Audio

Audio decoders wrap pure Go libraries and are checked against ffmpeg, like the
video ones. Wave D3 of [go-avkit/avkit#89](https://github.com/go-avkit/avkit/issues/89),
one codec per pull request, **FLAC** first.

```go
id, _ := decode.FirstAudioTrack(r)
pcm, err := decode.DecodeAudioTrack(r, id, decode.AudioConfig{})
for _, a := range pcm {   // presentation order, trimmed as the container states
    use(a.Data, a.Format, a.SampleRate, a.Channels, a.Layout, a.PTS)
}
```

Streaming: `decode.NewAudioTrack(trackConfig, cfg)`, then `DecodeSample(sample)` per
`container.Sample` and `Flush()`. `Audio.Data` is interleaved little-endian PCM in
`Format` (`SampleS16`, `SampleS32`, `SampleF32`: ffmpeg's `s16le`, `s32le`,
`f32le`); `Layout` is a speaker mask in WAVE order; `PTS` counts samples of
`SampleRate` from the first presented sample. The start of the track is dropped as
its container states it (edit list, `StartDelay`, Opus pre-skip) and an edit list's
end cuts the last buffer, which is what ffmpeg does.

| Codec | Library | Licence | Output | Oracle |
|---|---|---|---|---|
| FLAC (`fLaC`) | [`mewkiz/flac`](https://github.com/mewkiz/flac) v1.0.14 | Unlicense (public domain) | `s16le` up to 16 bits, `s32le` (left-justified) up to 24 | bit-exact, 13 fixtures |

FLAC: 4 to 24 bits per sample, 1 to 8 channels, every channel mode, native `.flac`,
Matroska and MP4. A 32-bit stream is `ErrAudioUnsupported` (the library reads the
code RFC 9639 9.1.4 gives it as reserved). `mewkiz/flac` writes a line to the
standard logger the first time it meets 176.4 kHz or 24 kHz; this package cannot
silence it. The fixtures are in `testdata/audio`, made by `tools/audiofix` with
ffmpeg together with the PCM it decodes them to, so the tests need no ffmpeg.
Bounds: `AudioConfig` limits the channel count, the block size (checked from the
frame header, before the subframes are allocated), a coded sample and the PCM a
track may return.

## HEVC

The decoding is [`github.com/gen2brain/h265`](https://github.com/gen2brain/h265)
v0.2.4 (MIT, pure Go, assembly kernels with a `noasm` build tag), the `hevc`
package. It decodes more than is claimed here; only what was measured is
accepted:

| Profile | State |
| --- | --- |
| Main, Main Still Picture, 4:2:0 8-bit | bit-exact against ffmpeg |
| Main 10, 4:2:0 10-bit | bit-exact against ffmpeg |
| Other chroma formats, bit depths other than 8 and 10, unequal luma and chroma depths | refused with `ErrUnsupported` |

Measured, every picture's MD5 against `ffmpeg -f framemd5` (ffmpeg 9.0.2, one
thread, native pixel format):

* **JCT-VC conformance suite HEVC_v1** (ITU-T H.265.1): 147 bitstreams. 143 match
  ffmpeg on every picture (`testdata/hevc-conformance`), and wherever the package
  ships the MD5 of the whole output it equals ours too. Three are
  decided by the suite's own MD5, because ffmpeg disagrees with it and we agree:
  `SAODBLK_A_MainConcept_4`, `SAODBLK_B_MainConcept_4` (ffmpeg's output differs
  from picture 0, with or without its assembly) and `VPSSPSPPS_A_MainConcept_1`
  (ffmpeg reports missing parameter sets and outputs 2 pictures of the 6). One,
  `TSUNEQBD_A_MAIN10_Technicolor_2` (luma and chroma bit depths differ), is
  refused here and by ffmpeg. The bitstreams are not in the repository (their
  redistribution terms are not stated); `TestHEVCConformance` runs when
  `DECODE_HEVC_DIR` points at them (`go run ./tools/jvt --codec hevc fetch --all`).
* **5 streams in `testdata/hevc/streams`** (made here with x265 from ffmpeg's
  `testsrc2`; intra, B pyramid, slices, wavefronts, weighted prediction, cropping,
  Main and Main 10) run in CI against ffmpeg's MD5s, plus a 4:4:4 one that must be
  refused.
* **7 real files** (MP4 and Matroska, 540x960 to 3840x1606, Main and Main 10), 200
  pictures each: `testdata/hevc-real-files.txt`, hashes only.

Speed against ffmpeg is not measured: the machine's 1-minute load was 27 to 38
during this work. Tracked for decode#2.

Bibliography: ITU-T H.265 (V11, 01/2026), opened as the PDF; read and checked:
Annex B (B.2, B.3: byte stream and its NAL unit extraction) and C.5.2.2 to C.5.2.4
(output order DPB, bumping, cropping). ISO/IEC 14496-15 (hvcC) is paywalled and
**not opened**: the record layout is that of mp4ff v0.62.0
`hevc/hevcdecoderconfigurationrecord.go` (opened); it is fuzzed here. The JCT-VC
suite listing was opened.

## What is decoded (H.264)

The decoding is [`github.com/oops1/go.264`](https://github.com/oops1/go.264)
(BSD-2-Clause, pure Go with assembly kernels), pinned and forced onto its
software path. Where it is wrong it is fixed upstream by pull request.

| Profile | State |
| --- | --- |
| Constrained Baseline, Baseline without FMO/ASO | bit-exact |
| Main, progressive (CAVLC and CABAC, B pictures, weighted prediction, multiple references, long-term references, MMCO) | bit-exact |
| High, 4:2:0 8-bit, progressive (8x8 transform, scaling matrices) | bit-exact on the streams below; no JVT FRExt stream measured yet |
| Interlaced (PAFF, MBAFF), FMO/ASO, Extended (SP/SI, data partitioning) | refused with `ErrUnsupported` |
| High 10, 4:2:2, 4:4:4, lossless | refused with `ErrUnsupported` |

`Frame.BitDepth` and `Chroma` exist so the later profiles need no API change.

## Measured

Every picture's MD5 against `ffmpeg -f framemd5 -pix_fmt yuv420p`, single
thread, and against the reference yuv shipped in the suite where there is one.

* **ITU-T H.264.1 conformance suite**, AVCv1: 81 bitstreams tried, 73 match ffmpeg (and
  the suite's yuv where it ships one): 69 with go.264 v1.12.0 as pinned here, and
  4 more (MR2..MR5_TANDBERG) only with
  [oops1/go.264#2](https://github.com/oops1/go.264/pull/2) "decoder: handle MMCO 5
  and gaps in frame_num"; they are listed in `pendingUpstream` until it is
  released. The other 8 are not claimed: 5 interlaced, 3 use FMO/ASO (ffmpeg
  cannot decode two of them either). ffmpeg's MD5s are in `testdata/jvt`; the
  bitstreams are not in the repository (their redistribution terms are not
  stated), so `TestJVTConformance` runs when `DECODE_JVT_DIR` points at them
  (`go run ./tools/jvt fetch` downloads them).
* **29 streams in `testdata/streams`** (from go.264, BSD-2-Clause; Baseline,
  Main, High) run in CI against ffmpeg's MD5s in the `.md5` files.
* **5 real files** (MP4 and Matroska, 720x300 to 1920x1080, x264), 900 pictures
  each: `testdata/real-files.txt`, hashes only.

## Bounds

A stream is untrusted. `Config.MaxFrameMBs` (default 36864, a 4096x2304 picture)
and `MaxSampleBytes` (default 64 MiB) are checked before anything sized by the
stream is allocated; go.264 also refuses pictures past its level ceiling and
bounds its reference frames by `max_num_ref_frames`. `TestBombRefusedUnderMemoryLimit`
runs three bomb SPSs under a 256 MiB memory limit and asserts a refusal and under
8 MiB allocated. `FuzzParseAVCC`, `FuzzDecodeSample` and `FuzzDecodeAnnexB` have a
committed corpus (`testdata/fuzz`, from `go run ./tools/jvt seeds`) and run in CI;
so do `FuzzParseHVCC`, `FuzzHEVCSample` and `FuzzHEVCAnnexB`. For HEVC the picture
size is checked by the decoder at the first slice, from the SPS, before any plane is
allocated, and what an Annex B stream may hold waiting for its next start code is
bounded by `MaxSampleBytes`; `TestHEVCSizeBomb` declares pictures up to 1048576
square under a 256 MiB memory limit and asserts a refusal and under 32 MiB allocated.
A picture over the limit is refused with `ErrUnsupported`, not `ErrOverLimit`,
because gen2brain/h265 reports it with the error it uses for an unimplemented
tool.

`PosterFrame` adds two bounds of its own, in `PosterConfig`: `MaxSamples` (default
600) caps the samples decoded for one picture, counted from the keyframe it starts
at to the sample that outputs it (the decoder outputs a picture at least one
sample late); past it the error is `ErrPosterLimit`, raised before a decoder is
built when the picture's sample is already too far from its keyframe.
`MaxTrackSamples` (default 1<<20) refuses a longer sample list, checked once
`container.Reader.Samples` has listed it (a reader on a file left on disk
fetches every sample there, which this package cannot avoid). `FuzzPosterFrame`
reads an MP4 or Matroska file from bytes with any `t`, has a committed corpus
(`testdata/fuzz/FuzzPosterFrame`, rewritten with `DECODE_WRITE_SEEDS=1 go test -run
TestWritePosterSeeds`), and runs in CI under `GOMEMLIMIT` and `ulimit -v`.

## Bibliography

* ITU-T H.264 (V16, 06/2026, in force), opened as the PDF. Read and checked:
  7.3.2.1 (SPS syntax entry), 7.4.3 (frame_num semantics: a picture with
  memory_management_control_operation 5 is inferred to have had frame_num 0;
  Table 7-9), 8.2.1 (tempPicOrderCnt after operation 5; 8.2.1.1 to 8.2.1.3
  prevPicOrderCntMsb/Lsb and prevFrameNumOffset after it), 8.2.5.1 to 8.2.5.4
  (marking, gaps in frame_num, sliding window) and C.4.4 (output order and
  operation 5). The fix upstream (oops1/go.264#2) follows those clauses,
  including the rebased prevPicOrderCntLsb. ffmpeg (libavcodec h264_refs.c,
  h264_picture.c, h264_slice.c, h264_parse.c, opened) sets frame_num to 0 on
  MMCO_RESET and takes the frame order count as the Min of the two field
  counts; I found no rebase of prev_poc_lsb in those files. The spec text is
  the reference.
* ITU-T H.264.1, conformance specification and the bitstream list at
  <https://www.itu.int/wftp3/av-arch/jvt-site/draft_conformance/AVCv1/> (listing
  and each stream's readme opened).
* RFC 9639 (FLAC), opened as text: 9.1.3 channel layouts (5 channels are "back/surround"
  in the RFC, side in ffmpeg, whose layout the output follows), 9.1.4 bit depth
  code 0b111 is 32 bits, 9.1.6 block size 65536 forbidden.
* ISO/IEC 14496-15 (avcC): paywalled, **not opened**. The record layout is that
  of mp4ff v0.62.0 `avc/avcdecoderconfigurationrecord.go` (opened), which
  cites 14496-15 itself; it is fuzzed here.

## Performance

Not yet measured against ffmpeg: the machine's 1-minute load was 36 to 50 during
this work, above the limit for a measurement (8). Tracked in the issues.

## Development

```
go test -race ./...                       # library, 100% of statements
cd tools && go run ./covergate ../cover.out   # the gate (reads the profile)
cd tools && go test ./...                 # oracle harness
```

BSD-3-Clause. The streams in `testdata/streams` are from go.264 (BSD-2-Clause,
`testdata/streams/LICENSE.go264`).

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
before `t` (in presentation time, as `DecodeTrack` labels the pictures: see
[Edit lists](#edit-lists)), and the very bytes `DecodeTrack` returns for it. A `t`
past the end gives the last picture, one before the first presentation time gives
the first, a negative or NaN `t` is `ErrTime`. Decoding starts at the last sync
sample at or before the picture's sample (the first sample if none is flagged) and
stops as soon as the picture is out. H.264 (`avc1`, `avc3`) and HEVC (`hvc1`,
`hev1`); another codec is `ErrUnsupported`.

## Edit lists

An MP4 track's edit list (`elst`) says which stretch of the track is presented, and
when. `DecodeTrack` and `PosterFrame` **apply it by default, as ffmpeg does**
(libavformat/mov.c, `mov_fix_index`): a picture presented before the media time of
an edit, or at its end or after, is decoded (it may be a reference) but not
returned; presentation times are shifted so that the pictures of the edit start at
0, or at the length of the empty edit that delays the track; an edit list of
several edits lays them end to end, decoding again what an edit repeats; and
`PosterFrame`'s `t` is a presentation time. Matroska and WebM have no per-track
video edit list (RFC 9559 has `TrackOffset`, deprecated, and Ordered Chapters, a
timeline of the whole file; the container reader reads neither), so nothing changes
there.

```go
decode.DecodeTrack(r, id, decode.Config{IgnoreEditList: true}) // the track as it is
```

`Config.IgnoreEditList` (ffmpeg's `-ignore_editlist 1`) returns every picture,
labelled with track time: the decode time of its sample plus its composition offset,
which is what v0.3.0 returned. **The default is a change from v0.3.0** for a file
whose edit list trims or delays the track (every MP4 whose first B-frame is
delayed has one), so it ships as a minor release.

What the edit list says that this package does not apply is refused with
`ErrEditList`, naming the edit, and never guessed at: a media rate other than 1
(ffmpeg ignores it), an edit that lasts to the end of the media (duration 0, which
a fragmented file may say; ffmpeg presents nothing for it), an empty edit after the
first edit that shows media, a media time before the track, and times that do not
fit. `Config.IgnoreEditList` decodes such a file anyway. An edit list that would make
the decoder walk more than 16 times the track's samples is `ErrOverLimit`.

Measured against ffmpeg 9.0.2 (`-f framemd5 -fps_mode passthrough`, with and
without `-ignore_editlist 1`; `tools/elstfix` records them in `testdata/elst`, so the
tests need no ffmpeg): the picture MD5s and the times are equal on H.264 and HEVC
files with a delay (an empty edit), a cut start and end, both, and two edits after an
empty one, and on the B-frame fixtures. `hevc-main-ipb.mp4` and `hevc-main10-ipb.mp4`
(an edit of 96000, no `ctts`) give 10 of 12 pictures, the same ones; their times
differ, and not because of the edit list: they have a B-frame delay and no `ctts`,
so ffmpeg labels each picture with the packet it leaves with and repeats times,
where this package labels it with its own sample. A Matroska track states presentation times in its
blocks (RFC 9559 section 11.2), and the container reader hands them over delayed by
the track's reordering delay with no edit list; v0.3.0 returned them so
(`bframes.mkv`: first picture at 40, ffmpeg 0). The delay is taken out now, as the
edit list an MP4 muxer would write for it does, and `IgnoreEditList` still returns
the reader's times.

**Known divergence, fragmented MP4.** ffmpeg switches its edit list support off for
a fragmented file (mov.c, `advanced_editlist` off when the moov has no `stts`
entries) and keeps every picture, shifting times by the start only; this package
applies the edit list there as for a progressive file, because the container
reader does not say a file is fragmented
([avkit#115](https://github.com/go-avkit/avkit/issues/115), then
[#20](https://github.com/go-avkit/decode/issues/20)). Measured on fragmented files
written by avkit's muxer from the delay and trim fixtures: with a start delay
(empty edit) both agree, picture for picture and time for time; with a trimmed start
(media time above 0) ffmpeg returns 21 pictures where this package returns 19.

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
one codec per pull request: FLAC, MP3, Opus, and AAC-LC (in house, below).
**Vorbis is not decoded** (see below), **MP2 neither**.

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
its container states it (edit list, Opus pre-skip; a bare `StartDelay` is a timestamp shift in ffmpeg and drops nothing) and an edit list's
end cuts the last buffer, which is what ffmpeg does.

The sample count is what the container's edit list and gapless metadata define, in
the output rate. It equals ffmpeg's except where ffmpeg is known to differ, each case
with its issue:

* [#57](https://github.com/go-avkit/decode/issues/57): an implicitly signalled
  HE-AAC MP4 track whose timescale is the core rate (22050 Hz) and whose edit list
  starts after 0. 1000 ticks are 45.35 ms, 2000 samples at the 44100 Hz output; this
  package drops 2000, ffmpeg 9.0.2 drops 1000 and gives 1000 more (the ends agree).
  The fixture `testdata/aac/core22-he44-st.mp4` asserts 89520 samples where ffmpeg
  gives 90520 (to be reported to ffmpeg). None of 817 real HE-AAC files measured
  has this shape.
* [#20](https://github.com/go-avkit/decode/issues/20): a fragmented MP4, whose edit
  list ffmpeg does not apply.

| Codec | Library | Licence | Output | Oracle |
|---|---|---|---|---|
| FLAC (`fLaC`) | [`mewkiz/flac`](https://github.com/mewkiz/flac) v1.0.14 | Unlicense (public domain) | `s16le` up to 16 bits, `s32le` (left-justified) up to 24 | bit-exact, 13 fixtures |
| MP3 (`mp3`) | [`tphakala/go-mp3`](https://github.com/tphakala/go-mp3) v0.3.0 | MIT (a port of minimp3, CC0) | `f32le` | max error 1.07e-6, SNR at least 127.9 dB, 10 fixtures, sample counts equal |
| Opus (`Opus`) | [`pion/opus`](https://github.com/pion/opus) v0.1.0 | MIT | `f32le`, 48 kHz | opus_compare passes on all 12 RFC 8251 vectors, stereo and mono, quality 66.3 to 99.8 % |
| AAC-LC and HE-AAC v1 (`mp4a`) | in this package, from ISO/IEC 14496-3 | BSD-3-Clause | `f32le` | error against the exact result about half of ffmpeg's on 5 fixtures; HE-AAC within 3e-6 of ffmpeg on 4; 12 of 15 ISO conformance vectors, see below |

AAC-LC (mono to 7.1 by channel configuration 1 to 7, 8 kHz to 96 kHz; M/S and
intensity stereo, perceptual noise substitution, TNS, pulses, short windows): written
in Go from the standard and from the order of float32 operations of ffmpeg's decoder
(`docs/aac-design.md` lists what was read, `.provenance.yml` what each table is, and
`copycheck` in CI fails the build if code or tables resemble ffmpeg's, FAAD2's or
PacketVideo's beyond the declared normative values). **HE-AAC v1 (SBR over AAC-LC)
decodes at twice the core rate**, whether the AudioSpecificConfig signals it (object
type 5, or the backward compatible extension) or the stream only carries it (the
decoder switches at the first SBR data, as ffmpeg does): on the four HE-AAC fixtures
it is within 3e-6 of ffmpeg 9.0.2's output sample by sample, and within a float32
rounding plus 1e-6 of a float64 reference. Parametric Stereo (HE-AAC v2) is signalled
but not decoded: the output is the SBR mono channel, where ffmpeg gives stereo. **Refused
with `ErrAudioUnsupported`: Main, LTP, SSR, 960-sample frames, SBR in its downsampled
mode (output at the core rate), coupling channels, and channel configurations 11, 12
and 14.** The target is the exact result,
not ffmpeg's output: the inverse MDCT is computed in float64 and rounded to float32
once, the windows are fused multiply-adds (`math.FMA`, the same bits on every
architecture), the sine and TNS tables are the correctly rounded formulas.
`TestAACAccuracy` is the gate: against a float64 reference of the same bitstream
(every real-valued stage in float64, the transform by its definition), this decoder's
error is about half of ffmpeg's best build's, in RMS and in largest error, on every
fixture (st44: RMS 1.8e-8 against 3.5e-8; surround51: 8.6e-9 against 1.7e-8). So
it is not bit-equal to ffmpeg, whose builds do not agree with each other either (the
clang and gcc builds differ in fused multiply-adds and in `sinf`); `TestAACOracle`
reports the counts as information. ISO/IEC 14496-26 conformance
(`TestAACConformance`, 15 pinned vectors, a provisional 1 LSB bound): 12 pass at 0.002
to 0.006 LSB; `al03` (window sequences out of order) and `al18`, `al19` (noise
substitution: the generator is not normative) do not yet. A program_config_element
that names no layout decodes with its channels numbered in element order
(`Audio.Layout` is zero). Speed against ffmpeg is not measured yet.

Opus (RFC 6716: SILK, CELT and hybrid, mono and stereo, mapping family 0): the
pre-skip of the header is dropped, the output gain it states applied, and an Ogg
stream's end cut to its granule position, so the sample counts are ffmpeg's. A
stream of more channels (mapping family 1 or 255) is `ErrAudioUnsupported`. The
gate is Opus's own conformance test (RFC 6716 section 6, RFC 8251 section 11): the
12 official test vectors, decoded to stereo and to mono at 48 kHz, must score quality
0 or more in `opus_compare` (ported to Go in `opuscompare_test.go` from the libopus
source, BSD), against either reference set. All pass (`opus_conformance_test.go`; set
`OPUS_VECTORS_DIR`, CI downloads the archive and checks its SHA-256). Scores, best
reference set: 99.8, 90.6, 87.8, 86.1, 77.7, 68.6, 99.6, 93.7, 94.4, 87.7, 99.7 and
88.9 % for vectors 1 to 12 in stereo; mono 98.4, 92.1, 87.7, 85.9, 77.4, 68.3, 98.1,
96.2, 95.9, 96.1, 98.4 and 88.8 %. RFC 6716 recommends above 90 at 48 kHz; vectors 3
to 6, 10 and 12 are below it, which the RFC calls passing. Not checked: the final
range decoder state, which the library does not expose. The comparison with ffmpeg
(`opus_test.go`) is information: against libopus CELT streams agree to 2.2e-5 (90
dB), SILK and hybrid to 1.4e-2 (40.9 dB, libopus decoding SILK in fixed point), and
ffmpeg's native decoder resamples SILK with a filter of its own and is 15 to 22 dB from
libopus there. Differences from ffmpeg's output: in MP4, ffmpeg 9.0.2
delivers every packet less the pre-skip (19848 samples on the fixture) where the edit
list states 19200, and ffprobe reports 0.4 s; this decoder applies the edit list. In
Matroska the end is only known to the millisecond (72 samples too long on the
fixture).

Vorbis: the only pure Go decoder, `jfreymuth/vorbis` (MIT), was wrapped and failed
the oracle: the second channel of every ffmpeg-encoded stereo fixture decodes to
something else (SNR against the encoder's source 1.6 dB, against 24 dB for ffmpeg's
decoder) while the first is exact. It also allocates what its setup header states
(a codebook of 2^24 entries, or none, is gigabytes), which the wrapper bounded. The
work is on the branch `audio-vorbis-blocked`; no decoder was written.

MP3 (Layer III, MPEG-1, 2 and 2.5, mono and stereo): the LAME tag's priming and
padding reach the decoder as the edit list avkit states for it, and the count is
ffmpeg's. **MP2 (Layer II) is not decoded**: no pure Go decoder under a licence
compatible with this package exists (`hajimehoshi/go-mp3` derives from PDMP3, LGPL,
and decodes Layer III only; `tphakala/go-mp3` likewise), so `NewAudioTrack` refuses an
`mp2` track with `ErrAudioUnsupported`. In Matroska the encoder delay is the
CodecDelay and the end only the samples' millisecond durations: the start is exact,
the end within a millisecond of ffmpeg's (2 samples on the fixture). avkit's reader
does not read MPEG 2.5 files; the decoder does, tested from frames cut by hand.

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

Speed: slower than ffmpeg, see [Performance](#performance).

Bibliography: ITU-T H.265 (V11, 01/2026), opened as the PDF; read and checked:
Annex B (B.2, B.3: byte stream and its NAL unit extraction) and C.5.2.2 to C.5.2.4
(output order DPB, bumping, cropping). ISO/IEC 14496-15 (hvcC) is paywalled and
**not opened**: the record layout is that of mp4ff v0.62.0
`hevc/hevcdecoderconfigurationrecord.go` (opened); it is fuzzed here. The JCT-VC
suite listing was opened.

## What is decoded (H.264)

The decoding is [`github.com/go-avkit/h264/decoder`](https://github.com/go-avkit/h264)
(v0.8.0; pure Go with assembly kernels), the decoding layer of
[`oops1/go.264`](https://github.com/oops1/go.264) v1.12.0 (BSD-2-Clause) imported
into go-avkit/h264, which runs the same conformance streams in its own CI. It
has no hardware path. Fixes go into go-avkit/h264, and are offered upstream.

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
* RFC 6716 section 6 (conformance, opened as text) and RFC 8251 section 11 (new
  vectors, two reference sets, SHA-1 list, opened); `opus_compare.c` from the libopus
  tree (opened, ported); the 12 test vectors of `opus_testvectors-rfc8251.tar.gz`.
* RFC 7845 (Ogg Opus), opened as text: 5.1 the identification header (version
  accepted by its upper four bits, pre-skip at 48 kHz, output gain Q7.8 dB, mapping
  family 0 for mono and stereo). RFC 6716 was **not opened**: the TOC byte test in
  `opus_test.go` (configuration below 16 is SILK or hybrid) is the layout avkit's
  `opusPacketDuration` (`container/ogg.go`, opened) reads.
* MP3: ISO/IEC 11172-3 and 13818-3 are paywalled, **not opened**. The decoder is
  `tphakala/go-mp3`'s (its README and PROVENANCE.md opened: a port of minimp3,
  validated against it); the frame header fields the MPEG 2.5 test cuts frames by
  are checked by the decoding matching ffmpeg's. Priming is avkit's edit list
  (`container/audiocodecs.go`, `ogg.go`, `read.go` of v0.15.0, opened) and what
  ffmpeg 9.0.2 delivered on each fixture.
* ISO/IEC 14496-15 (avcC): paywalled, **not opened**. The record layout is that
  of mp4ff v0.62.0 `avc/avcdecoderconfigurationrecord.go` (opened), which
  cites 14496-15 itself; it is fuzzed here.

## Performance

**Slower than ffmpeg on every video input measured, an open defect
([#2](https://github.com/go-avkit/decode/issues/2)).** Measured on tag v0.3.0, single
core: Apple M4 Max, darwin/arm64, ffmpeg 9.0.2 with `-threads 1`, 5 interleaved
rounds, median, the harness refusing to start at a 1-minute load of 8 or more.

| Input | ours fps | ffmpeg fps | ours / ffmpeg |
| --- | --- | --- | --- |
| H.264 High 1080p (real clip, 1920x808, 600 frames) | 36.5 | 173.1 | 0.21 |
| HEVC Main 10 2160p (real clip, 3840x1606, 60 frames) | 10.3 | 27.9 | 0.37 |
| the committed H.264 and HEVC streams (each repeated 100 times) | | | 0.21 to 0.41 |

The profiles put the hot spots in the wrapped libraries, not in this package: in
go.264 the interpolation and CABAC residual decoding on a pure-Go build (no arm64
assembly kernels), in gen2brain/h265 the 10-bit motion compensation (its assembly is
taken at 8 bits only). The H.264 decoder is go-avkit/h264, whose README keeps the
mid-GOP table ([decode#10](https://github.com/go-avkit/decode/issues/10)): the
IDR and 45 pictures after it at 720p took 257 ms as imported from go.264 and take
179 ms in h264 v0.8.0, against about 58 ms for `ffmpeg -threads 1` once its
process start is taken off. The measurement is on arm64; an amd64 one is still needed.
Audio decoders are not measured yet. The numbers and the profiles are in the
comment on #2.

## Development

```
go test -race ./...                       # library, 100% of statements
cd tools && go run ./covergate ../cover.out   # the gate (reads the profile)
cd tools && go test ./...                 # oracle harness
```

BSD-3-Clause. The streams in `testdata/streams` are from go.264 (BSD-2-Clause,
`testdata/streams/LICENSE.go264`).

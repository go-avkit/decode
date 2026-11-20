# decode

[![ci](https://github.com/go-avkit/decode/actions/workflows/ci.yml/badge.svg)](https://github.com/go-avkit/decode/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-avkit/decode.svg)](https://pkg.go.dev/github.com/go-avkit/decode)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)

Part of [go-avkit](https://go-avkit.github.io): [documentation](https://go-avkit.github.io/docs/).

Video and audio decoders for [avkit](https://github.com/go-avkit/avkit), pure Go,
no cgo. Video: **H.264** (ITU-T H.264 | ISO/IEC 14496-10) and **HEVC / H.265**
(ITU-T H.265 | ISO/IEC 23008-2), Main and Main 10, 4:2:0. Audio: FLAC, MP3 and
Opus, AAC-LC and HE-AAC v1 (SBR) since v0.6.0, and HE-AAC v2 (Parametric Stereo)
since v0.7.0, with the full profile (procedure B, IPD/OPD) by default since v0.8.0; on one core it takes
about 1.6 times ffmpeg's CPU time on an x86 server core and about 1.1 times on an Apple M4,
indicative (see [Audio](#audio)). Waves D1 to D3 of
[go-avkit/avkit#89](https://github.com/go-avkit/avkit/issues/89); see
[Audio](#audio) for what each codec is checked against.

```go
r, _ := container.NewReader(file)                   // MP4, Matroska, ...
frames, err := decode.DecodeTrack(r, trackID, decode.Config{})
for _, f := range frames {                          // display order
    use(f.Y, f.Cb, f.Cr, f.YStride, f.CStride, f.Width, f.Height, f.PTS)
}
```

**New:** `decode.Frames` is `DecodeTrack` as an iterator (`iter.Seq2[*Frame, error]`).
It gives the same pictures, at the same times, H.264 and HEVC, edit list applied or
ignored as `Config` says, one at a time as the decoder outputs them, so a long file
is not held whole:

```go
for f, err := range decode.Frames(r, trackID, decode.Config{}) {
    if err != nil {
        return err // the last pair, after every picture decoded before it
    }
    use(f.Y, f.Cb, f.Cr, f.YStride, f.CStride, f.Width, f.Height, f.PTS)
}
```

Each `*Frame` owns its planes and nothing is reused between iterations, so it may be
kept, appended or sent to another goroutine; the price is one picture allocation per
frame, which the garbage collector takes back once the loop lets go of it. `break`
stops the decoding. An iterator and not a callback, because `break`, `return` and
`defer` then work in the loop body and the error is part of the loop. Measured
against `DecodeTrack`: see [Performance](#performance).

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
`hev1`); another codec (VP8, VP9, AV1, MPEG-4 Part 2, ...) is refused by name,
before any sample is read, with a `*CodecError` (`codec "vp09" is not supported`,
`errors.As` gives the fourcc, `errors.Is(err, ErrUnsupported)` holds) in
`DecodeTrack`, `NewTrack` and `PosterFrame`: a VP9 file is not reported as corrupt.

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
one codec per pull request: FLAC, MP3, Opus, and AAC-LC (in house, below), and
linear PCM (WAVE).
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

The sample count is meant to be what the container's edit list and gapless metadata
define, in the output rate. One open defect of this package falls short of that:
Matroska priming and trimming are applied in milliseconds
([#54](https://github.com/go-avkit/decode/issues/54)). An MP4's `iTunSMPB`, which
iTunes and `afconvert` write, is applied to AAC as ffmpeg does: the encoder delay
replaces the start the edit list states (the packets the edit list drops whole still
count against it), and the end padding cuts the track to its duration less the
padding when that is longer than both it and the original sample count; the numbers
are ticks of the track's timescale and double with an HE-AAC track at the core rate.
`DecodeAudioTrack` applies it, `NewAudioTrack` does not. Apart from the Matroska
defect the count equals ffmpeg's except where ffmpeg is known to differ, deliberate
divergences each with its issue:

* [#57](https://github.com/go-avkit/decode/issues/57): an HE-AAC MP4 track whose
  timescale is the core rate (22050 Hz) and whose edit list starts after 0, with no
  `iTunSMPB` delay to replace it. 1000 ticks are 45.35 ms, 2000 samples at the 44100 Hz
  output; this package drops 2000, ffmpeg 9.0.2 drops 1000 of an implicitly signalled
  track and gives 1000 more (the ends agree). The fixture
  `testdata/aac/core22-he44-st.mp4` asserts 89520 samples where ffmpeg gives 90520 (to
  be reported to ffmpeg). None of 817 real HE-AAC files measured has this shape.
* `iTunSMPB` on the wrong stream: ffmpeg attaches the item to the last stream parsed
  when it reads it, so in a file where another track follows the audio it trims no
  audio. This package trims the first audio track, as the item says
  (`TestGaplessItemOnTheWrongStream`: 9600 samples where ffmpeg gives 12288).
* `iTunSMPB` before any track: ffmpeg reads the item with no stream yet and ignores it
  (a `udta` ahead of the `trak`); this package applies it wherever it is
  (`TestGaplessItemBeforeTheTrack`: 9600 samples where ffmpeg gives 12288).
* [#20](https://github.com/go-avkit/decode/issues/20): a fragmented MP4, whose edit
  list ffmpeg does not apply.

Linear PCM, which is what a WAVE file's track is, goes through the same API
(reported from go-perceptual: it was refused, so WAV was unreachable). The width and
the byte order are the container's, from the `pcmC` record of an `ipcm` or `fpcm`
entry (`TrackConfig.CodecConfig`: ISO/IEC 23003-5 5.1), never inferred from a sample's
length or the track's duration; an entry without the record is `ErrAudioConfig`.
Output is ffmpeg's: `ipcm` 8 and 16 bits (signed) to `SampleS16`, 24 and 32 bits to
`SampleS32` (24 bits left-justified, shifted up by eight, so full scale stays full
scale), `fpcm` 32 bits to `SampleF32`, **`fpcm` 64 bits to `SampleF32`, rounded to
the nearest single: lossy**, and `raw ` (unsigned 8 bits, silence is 128) to
`SampleS16`. A sample that is not a whole number of frames is `ErrAudioSample`.

| Codec | Library | Licence | Output | Oracle |
|---|---|---|---|---|
| Linear PCM (`ipcm`, `fpcm`, `raw `) | in this package | BSD-3-Clause | as above | bit-exact against ffmpeg, 6 WAVE fixtures (s16, s24, s32, f32, f64, u8) |
| FLAC (`fLaC`) | [`mewkiz/flac`](https://github.com/mewkiz/flac) v1.0.14 | Unlicense (public domain) | `s16le` up to 16 bits, `s32le` (left-justified) up to 24 | bit-exact, 13 fixtures |
| MP3 (`mp3`) | [`tphakala/go-mp3`](https://github.com/tphakala/go-mp3) v0.3.0 | MIT (a port of minimp3, CC0) | `f32le` | max error 1.07e-6, SNR at least 127.9 dB, 10 fixtures, sample counts equal |
| Opus (`Opus`) | [`pion/opus`](https://github.com/pion/opus) v0.1.0 | MIT | `f32le`, 48 kHz | opus_compare passes on all 12 RFC 8251 vectors, stereo and mono, quality 66.3 to 99.8 % |
| AAC-LC, HE-AAC v1 and v2 (`mp4a`) | in this package, from ISO/IEC 14496-3 | BSD-3-Clause | `f32le` | error against the exact result about half of ffmpeg's on 5 fixtures; HE-AAC within 3e-6 of ffmpeg on 4; PS within 1.1e-5 of ffmpeg on 801 library files; 12 of 15 AAC-LC and 7 of 7 PS ISO conformance vectors (at 0.033 LSB), see below |

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
rounding plus 1e-6 of a float64 reference. **Parametric Stereo (HE-AAC v2) is applied
by default since v0.7.0** (`docs/aac-ps-design.md`, "PS4 as built"): **a mono core whose configuration
signals SBR (object type 5 or 29, or the backward compatible extension) and does not say
PS absent is announced as stereo from the first frame**, as ffmpeg's output is, because
`Audio.Channels` and the layout are those of the track and cannot change mid-stream; its
second channel is the right channel PS makes from the mono signal, and a copy of the first
channel while the stream sends no PS parameters. **The decoder applies the full PS
profile by default since v0.8.0** (`docs/aac-ps-design.md`, "PS5 as built"): procedure B for ICC modes 3 to 5 and
IPD/OPD with their smoothing and interpolation, as the `_ur` references of ISO's vectors
are (all 7 of 7 below); and 34-band streams keep 34 bands (a baseline decoder maps them to 20, so
`al_sbr_ps_06` matches `_ur` but not `_bl`, 2226 LSB off). v0.7.0 mixed with baseline
mixing (procedure A for every ICC mode, IPD/OPD ignored): the 801 library files use
neither feature and decode to the same samples. On 801 mono PS files of
the author's library the output has ffmpeg 9.0.2's length and channel count, and is
within 1.1e-5 of its samples (median 4.4e-7, 99th percentile 2.7e-6; the worst file's
error is the same on L and R, so it is in the mono signal before the mixer, in ffmpeg's
SIMD build: ours is 2.4e-6 from its C build there); on the written streams it is within
1e-6, on `he44-ps.aac` within 5e-5. A mono stream that signals
no SBR at all stays mono even if it carries SBR (ffmpeg turns stereo on at the first
frame that has it; none of the 832 mono files tried differs). **Refused
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
substitution: the generator is not normative) do not yet. The PS vectors `al_sbr_ps_00`
to `06` (`TestAACConformancePS`): all seven against ISO's `_ur` references (full
profile; 03 uses procedure B and 04 IPD/OPD), at 0.033 LSB. On the written streams
ffmpeg's output is matched to 1e-6 with procedure B (S18). With IPD/OPD, by default S16b
is 1.4e-2 to 1.9e-2 off ffmpeg, left channel only (S16c up to 4.8e-2), because ffmpeg deviates from the standard on the image
rows (Q5, confirmed): it negates only the start of the interpolation for them, and at 34
bands also negates rows 12 and 13, whose centre frequencies are positive. ISO's
`al_sbr_ps_04` follows the specification's rule (0.033 LSB), not ffmpeg's (0.76 LSB); S16
is matched to 1e-6 only under the test switch that gives ffmpeg's rule. S16d (phases
across a 20/34 layout switch) is not bounded (measured 6.0e-2). A program_config_element
that names no layout decodes with its channels numbered in element order
(`Audio.Layout` is zero).

**AAC is available since v0.6.0** (AAC-LC, HE-AAC v1, and the gapless handling
above: `iTunSMPB` and edit lists). Speed, measured for the release
([#35](https://github.com/go-avkit/decode/issues/35)): `BenchmarkAACDecodeReuse`
(`-cpu 1`, decode only, 3 allocations per op, all setup) against the median
`utime` of ffmpeg 9.0.2 `-threads 1 -benchmark`, **a whole process** that also
opens the file and demuxes, so it is not a like-for-like decode time. Go 1.27.4,
Apple M4 Max, single core, 10-minute inputs (a 60 s clip looped ten times by
stream copy), 5 interleaved rounds at 1-minute load 6.02 to 6.46 (two earlier
rounds started above load 8 and were discarded):

| input | decode median (min to max) | ffmpeg `utime` median | decode / ffmpeg |
|---|---|---|---|
| 5.1, 48 kHz | 793.4 ms (789.7 to 802.0) | 821 ms | 0.966 |
| stereo, 48 kHz | 309.0 ms (305.7 to 312.6) | 472 ms | 0.655 |

Mono was not measured (no input at hand). The measured binary is `a36c9fc`.

**HE-AAC v2 (PS applied) is slower than ffmpeg on one core: about 1.6 times its `utime` on
an x86 server core and about 1.1 times on an Apple M4**, an open defect
([#77](https://github.com/go-avkit/decode/issues/77); the SBR share is
[#47](https://github.com/go-avkit/decode/issues/47)); no parity is claimed. It was 1.74 on
the M4 before the speed work of v0.8.0 (load 7.37 to 7.81). What was measured for v0.8.0,
the same method (decode-only `BenchmarkAACDecodeReuse` against the `utime` of a whole
`ffmpeg -threads 1 -benchmark` process, Go 1.27.4, 5 to 11 interleaved rounds, the 1-minute
load recorded before each):

| machine, date | input (length) | decode / ffmpeg | load | ffmpeg |
|---|---|---|---|---|
| x86: AMD Ryzen 9 7950X (Zen 4), Ubuntu 24.04, `taskset -c 2`, 2026-11-15 | HE-AAC v2 (620 s) | 1.62 (692 ms against 427 ms; 1.60 to 1.65 in three runs of that day) | 0.13 to 0.93 | 6.1.1 |
| same | AAC-LC 5.1, 48 kHz (609 s) | 1.24 (715 ms against 578 ms; 1.24 to 1.27 in earlier runs) | 0.13 to 0.93 | 6.1.1 |
| same | AAC-LC stereo, 48 kHz (609 s) | 0.94 (283 ms against 301 ms; 0.90 to 0.95) | 0.13 to 0.93 | 6.1.1 |
| same | AAC-LC stereo, impulses every 50 ms (300 s, made with ffmpeg's encoder) | 1.04 (144 ms against 138 ms) | 0.13 to 0.93 | 6.1.1 |
| Apple M4 Max, 2026-11-15, **indicative** | HE-AAC v2 (620 s) | 1.11 (689 ms against 623 ms; main before it 1.12) | 7.0 to 7.7 | 9.0.2 |
| Apple M4 Max, 2026-11-15 (5 rounds, all under 8) | AAC-LC stereo, impulses every 50 ms (300 s) | 0.70 (142 ms against 203 ms; 154 ms, 0.76, before the short-window pair: -7.8 %) | 7.22 to 7.82 | 9.0.2 |
| same | AAC-LC stereo, 48 kHz (609 s) | 0.68 (319 ms against 471 ms; 318 ms before) | 7.22 to 7.82 | 9.0.2 |
| same | AAC-LC mono, 48 kHz (600 s) | 0.46 (153 ms against 333 ms; 152 ms before) | 7.22 to 7.82 | 9.0.2 |

The M4 HE-AAC v2 figure is at a load just under the gate of 8 and moves with it (1.37 to
1.47 earlier in the same work at a load of 1.6 and 6 to 7); it was not refreshed at a quieter
load. The three AAC-LC rows were measured at 7.2 to 7.8, under the gate. The
x86 rows are measured on a host with another user, loads under 1. The x86 ratios are
against ffmpeg 6.1.1 (Ubuntu's build), the M4 ones against 9.0.2, so they are not a
like-for-like comparison of the two machines. The short-block transform
(8 short windows as 4 pairs of the 128-point DCT-IV) is 27 % faster in its own benchmark on
the M4 (`BenchmarkAACIMDCTShort`: 1247 ns against 908 ns; load 36, **indicative**), and
7.8 % in the whole decode of the transient-heavy clip (the table); the other clips have few
short blocks, and x86 has no fused 128-point kernel. The changes that moved the HE-AAC v2 ratio
are the fused DCT-IV pairs and window sums of go-fft and go-simd, and the PS path run
slot by slot. The decorrelator and mixer, the second synthesis bank and the SBR
adjustment are the largest shares of what remains (on x86 the decode is instruction
bound: IPC 4.6).

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

The decoding is [`github.com/go-avkit/h265`](https://github.com/go-avkit/h265)
v0.22.0 (BSD-3-Clause, pure Go, written from the specification with assembly kernels
from go-videodsp/kernels and a portable path), behind the same API as before:
`NewHEVC`, `NewHVCC`, `NewTrack`, `DecodeSample`, `DecodeAnnexB`, `DecodeTrack` and
`PosterFrame` keep their signatures. **Since v0.10.0 it replaces
[`github.com/gen2brain/h265`](https://github.com/gen2brain/h265), which is no longer a
dependency**, with no fallback to it. It decodes more than a `Frame` can carry; only
what a `Frame` holds is returned:

| Profile | State |
| --- | --- |
| Main, Main Still Picture, 4:2:0 8-bit | bit-exact against ffmpeg |
| Main 10, 4:2:0 10-bit | bit-exact against ffmpeg |
| 4:0:0, 4:2:2, 4:4:4, bit depths other than 8 and 10, unequal luma and chroma depths | refused with `ErrUnsupported`, naming the format (the decoder reads them; a `Frame` cannot carry them, and the API is not widened here) |
| 3D and screen content coding extensions | refused with `ErrUnsupported` |
| `cabac_bypass_alignment_enabled_flag`, multilayer (MV-HEVC) streams | decoded (the base layer only) |

A `Frame` owns its planes: the pictures h265 returns are read-only views of its reference
pictures, and each is copied into the `Frame` (packed, one byte a sample, or a little-endian
word above eight bits), so what a caller does with a `Frame` never reaches the decoder.

Measured, every picture's MD5 against `ffmpeg -f framemd5` (ffmpeg 9.0.2, one
thread, native pixel format):

* **h265 v0.23.0 against v0.22.0, the whole JCT-VC suite** (HEVC_v1 and RExt, 196
  bitstreams, `NewHEVC` and `DecodeAnnexB` of this package built on each): the output of 152
  is **identical** (picture count and the MD5 of every plane, in output order), **0 are
  lost**, **1 more** is returned
  (`WPP_AND_TILE_AND_CABAC_BYPASS_ALIGN_1_HIGH_TP_444_14BIT`, 4:2:0 8 bits: h265 v0.22.0
  refused its `cabac_bypass_alignment_enabled_flag`; v0.23.0 decodes it with the aligned
  bypass decoding of 9.3.4.3.6, and its output MD5 is the `.yuv` MD5 the suite ships).
  The other 43 are refused by both, all for a format a `Frame` does not carry (4:0:0,
  4:2:2, 4:4:4, more than ten bits, `TSUNEQBD_A_MAIN10_Technicolor_2`'s unequal depths),
  which h265 itself decodes: six of them also use `cabac_bypass_alignment_enabled_flag`
  (the High Throughput 4:4:4 16 Intra streams, `GENERAL_16b_444_highThroughput` and
  `WAVETILES_RExt_Sony_2`), which h265 decodes with all their picture hashes matching.
  Earlier, from gen2brain/h265 to h265: 148 identical, 0 lost, 4 more returned.
* **The 9 MV-HEVC conformance streams** (`MVHEVCS_A` to `_I`, JCT-VC `MV-HEVC`, units of
  layers 1 to 5): a `Frame` is the base layer, and the other layers' units are skipped.
  All 9 decode with v0.23.0 (v0.22.0 refused every one at the PPS of layer 0), and the MD5
  of the output equals the base layer's `<name>_0.yuv` MD5 the suite ships.

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

Speed, `DecodeTrack` on the MP4 of four 1080p streams (x265 `medium` crf 23 from the h264
bench sources: Main 8-bit, a fade with explicit weighted prediction, Main 10, and a second
8-bit; commands and SHA-256s in go-avkit/h265's `tools/bench/README.md`), frames per second
of CPU time, median of 7 rounds, the decoders run in turn and the order reversed every other
round, against `ffmpeg 6.1.1 -threads 1` (x86) and 9.0.2 (M4). The picture MD5 of all four
equals ffmpeg's. "Before" is decode v0.9.0, over gen2brain/h265. The edit list drops 2 of the
240 frames (238, 118), which is why the counts differ from ffmpeg's.

| 1080p, one core | decode (h265) | decode before (gen2brain) | ffmpeg | decode / ffmpeg | decode / before |
| --- | --- | --- | --- | --- | --- |
| x86, Ryzen 9 7950X, `taskset -c 5`, load 0.3 to 1.1: Main (240 frames) | 95.8 | 87.2 | 396.0 | 0.24 | 1.10 |
| Main fade, weighted prediction (120) | 85.0 | 79.9 | 430.1 | 0.20 | 1.06 |
| Main 10 (240) | 75.1 | 29.9 | 295.2 | 0.25 | 2.51 |
| Main, second stream (240) | 108.7 | 95.8 | 438.0 | 0.25 | 1.14 |
| M4 Max, indicative (load 7 to 10): Main (240) | 94.5 | 66.0 | 392.2 | 0.24 | 1.43 |
| Main fade (120) | 102.0 | 64.1 | 446.1 | 0.23 | 1.59 |
| Main 10 (240) | 81.0 | 28.5 | 119.1 | 0.68 | 2.84 |
| Main, second stream (240) | 109.4 | 73.1 | 446.9 | 0.24 | 1.50 |

With h265 v0.22.0 every stream is faster than before on x86 too (8-bit 6 to 14 percent, Main 10 2.5
times), and 20 to 23 percent faster than v0.10.0 on the 8-bit streams (the earlier x86 slowdown,
issue 101, is closed): h265 v0.22.0 decodes an 8-bit picture into bytes end to end (`Plane.Pix8`),
so the `Frame` is filled by one memmove a row instead of a narrowing of 16-bit samples. The M4
rows are indicative: the 1-minute load was 7 to 10, which the harness refuses on x86; against
v0.10.0 on the same machine the 8-bit streams are 8 to 10 percent faster.

With h265 v0.24.0 the HEVC decoder reuses its pictures (`Options.RecyclePictures`): a picture
goes back to the decoder's pool as soon as it is copied into the `Frame`, which owns its planes,
so the decoder no longer allocates (and the kernel no longer faults in) a new picture for every
frame. Minor page faults on alien-main (238 frames), 8-bit 1080p: `Frames` 333 a frame before,
62 after (79293 to 14837); `DecodeTrack` 534 to 283 (127011 to 67343), which keeps every `Frame`
it returns and so still faults those in. The output is unchanged: the MD5 of the 196 conformance
streams through `NewHEVC` is identical to the previous release's (153 identical, 0 lost, 43 refused
by both for a format a `Frame` does not carry), and equals ffmpeg's where it did. The counts were
taken on an Apple M4 Max under a load of 47, where counts are exact and times are not, so no speed
figure is given here; the x86 time and page-fault figures for h265's own decoder are in go-avkit/h265's README.

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
size is checked by the decoder when the SPS arrives, before any plane is
allocated, and what an Annex B stream may hold waiting for its next start code is
bounded by `MaxSampleBytes`; `TestHEVCSizeBomb` declares pictures up to 1048576
square under a 256 MiB memory limit and asserts a refusal and under 32 MiB allocated.
A picture over the limit is refused with an error that matches both `ErrOverLimit`
(it is a limit) and `ErrUnsupported` (what callers of the previous decoder, which could
not tell a limit from an unimplemented tool, tested for).

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
assembly kernels); for HEVC see the table in [HEVC](#hevc), measured on v0.10.0. The H.264 decoder is go-avkit/h264, whose README keeps the
mid-GOP table ([decode#10](https://github.com/go-avkit/decode/issues/10)): the
IDR and 45 pictures after it at 720p took 257 ms as imported from go.264 and take
179 ms in h264 v0.8.0, against about 58 ms for `ffmpeg -threads 1` once its
process start is taken off. Since decode v0.12.0 (h264 v0.11.0), through `DecodeTrack`
on 1080p, one core, Apple M4 (indicative, load 9 to 11), alien takes 2.3 s instead of
5.7 to 5.9 s and fade 0.8 s instead of 2.0 s, 2.5 times faster; ffmpeg is still 2.3 to
2.8 times faster, and Zen 4 is not re-measured. The measurement is on arm64; an amd64 one is still needed.
`decode.Frames` against `DecodeTrack` on the same
files, Ryzen 9 7950X (Zen 4), one core, `taskset -c 9`, 7 alternating rounds, median, the
pictures dropped as they come, md5 of every plane equal: H.264 alien (1080p, 238
pictures) 2.63 s to 2.53 s, fade (120) 1.05 s to 1.00 s, HEVC alien-main (238) 2.66 s to
2.47 s; page faults 194 000 to 25 000, 100 000 to 18 000 and 515 000 to 339 000. The
control, `DecodeTrack` twice, differs by under 1 %. What is saved is the holding of
every picture: HEVC still allocates a picture per frame inside h265.
Only AAC's speed is measured (table in Audio above); FLAC, MP3 and Opus are not. The numbers and the profiles are in the
comment on #2.

## Development

```
go test -race ./...                       # library, 100% of statements
cd tools && go run ./covergate ../cover.out   # the gate (reads the profile)
cd tools && go test ./...                 # oracle harness
```

BSD-3-Clause. The streams in `testdata/streams` are from go.264 (BSD-2-Clause,
`testdata/streams/LICENSE.go264`).

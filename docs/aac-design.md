# AAC decoder: sources, gaps and plan

Status: design note, no code. Refs go-avkit/avkit#89. Written 2026-11-10.

The goal is a pure-Go AAC decoder in `decode` (BSD-3-Clause): AAC-LC first,
then SBR (HE-AAC v1), then Parametric Stereo (HE-AAC v2). The user decided on
2026-11-10:

- to work from **open sources** instead of buying ISO/IEC 14496-3;
- that PacketVideo's decoder is **studied** (syntax, tables, stage order,
  edge cases), **not ported line by line**;
- that the arithmetic is **floating point**, like ffmpeg's output, not
  PacketVideo's fixed point;
- that copyleft code (ffmpeg, FAAD2) **may be read to understand what it
  computes and in what order**, but never copied or translated, not even as a
  close structural paraphrase of a function, and its tables are never copied
  verbatim;
- that the read-don't-copy rule is enforced by a **gate** (`copycheck`, see
  "How the separation is kept"), not by this prose.

Patents are outside licence terms. This note only reports what the sources
themselves say about patents.

## Bibliography (what was opened, and on what terms)

A source is cited only if it was opened for this note.

| # | Source | What it holds | Terms (quoted or summarised from the source itself) |
|---|--------|---------------|------------------------------------------------------|
| S1 | 3GPP TS 26.401 V19.0.0 (2025-10), Enhanced aacPlus general description (`26401-j00.zip`, 3gpp.org) | Profile, tools, SBR LP/HQ rules, `frameLengthFlag` = 0 | "No part may be reproduced except as authorized by written permission. The copyright and the foregoing restriction extend to reproduction in all media. © 2025, 3GPP Organizational Partners" |
| S2 | 3GPP TS 26.410 V19.0.0, floating-point ANSI-C code (`26410-j00.zip`, containing `26410-i00-ANSI-C_source_code.zip`, tree `3GPP_enhanced_aacPlus_src_200907`); only the tree, the READMEs and file headers were opened | A complete float Enhanced aacPlus decoder (AAC-LC, SBR LP and HQ, PS) | The same 3GPP notice. C files carry **no licence header**; Makefiles say `# Copyright (c) Coding Technologies 2004`. No permission to reproduce or adapt is given anywhere. |
| S3 | 3GPP TS 26.411 V19.0.0 (fixed-point C code), `26411-j00.zip` (copyright line only) | — | The same 3GPP notice. |
| S4 | ISO/IEC 14496-26 2nd ed. conformance inserts, `standards.iso.org/iso-iec/14496/-26/ed-2/en/` (README_audio.txt, doc.zip, the md5 list) | Bitstreams: `compressedAdts.zip` (866 KB), `compressedAdif.zip`, `compressedMp4.zip` (1151 MB), `compressedAss.zip`; reference waveforms `referencesWav.zip` (9393 MB), `referencesMp4.zip` (226 MB). AAC-LC `alNN_<fs>`, SBR `al_sbr_*`, PS `al_sbr_ps_*`, LTP `ap*` | "You are permitted to use the electronic insert(s) available on this site in their original format without any modifications for the purposes specified in their respective ISO standard(s)" plus clauses 1, 7, 8, 9 of the ISO Customer Licence Agreement. **Use only: no redistribution, no modification.** |
| S5 | `standards.iso.org/ittf/PubliclyAvailableStandards/index.html` | The former list of free ISO/IEC standards | The site is closed and points to the ISO/IEC webstores; iso.org answered HTTP 403. 14496-3, 13818-7 and the 14496-26 **text** were not found free. |
| S6 | PacketVideo AAC decoder, AOSP `platform/frameworks/av` tag `android-4.1.1_r1`, `media/libstagefright/codecs/aacdec` (same code as `frameworks/base` `android-4.0.1_r1`, as verified by the coordinator) | Fixed-point AAC-LC, LTP, PNS, TNS, pulse, M/S, intensity, ADIF/ADTS/PCE, SBR, PS; `Android.mk:178` builds with `-DAAC_PLUS -DHQ_SBR -DPARAMETRICSTEREO` | **Apache-2.0** on every source file (`Copyright (C) 1998-2009 PacketVideo`), plus `MODULE_LICENSE_APACHE2` and `NOTICE`. **114 files also carry an MPEG reference-software notice** (below). |
| S7 | Symphonia `symphonia-codec-aac` (Rust): README and Cargo.toml | AAC-LC only; "ported and relicensed from the NihAV project with permission" | **MPL-2.0** |
| S8 | H. Purnhagen, "Low complexity parametric stereo coding in MPEG-4", DAFx'04, pp. 163-164 | PS paradigm, IID/ICC, mixing matrix H (eq. 4-8) | Conference paper; read only. |
| S9 | FFmpeg `n9.0.2` (LGPL-2.1+), the lines listed in "What was read in ffmpeg" | ffmpeg's float order of computation | **Read only**, never copied. |
| S10 | `decode`: `LICENSE` (BSD-3-Clause), `mp3_test.go:13-20` | Tolerance precedent: 1e-5 absolute and 100 dB SNR against ffmpeg | — |

Not opened, so not cited: Bosi & Goldberg; the AES/ICASSP SBR and PS papers
(Ekstrand, Dietz, Schuijers et al.); FAAD2; Android `external/aac` (fdk-aac).

Recorded from the coordinator, not re-verified here: FreeBSD ports carry only
faad (GPL), fdk-aac and faac (an encoder). There is no permissively licensed
AAC decoder in ports.

### The two notices on PacketVideo (S6)

114 files, among them all of `sbr_*` and `ps_*` and the Huffman, TNS, PNS, LTP
and bitstream readers, carry an MPEG notice by AT&T, Dolby, Fraunhofer IIS,
Nokia or Coding Technologies in addition to Apache-2.0. From
`huffdecode.cpp:152-171`:

> ISO/IEC gives users of the MPEG-2 NBC/MPEG-4 Audio standards free license to
> this software module or modifications thereof for use in hardware or software
> products claiming conformance to the MPEG-2 NBC/MPEG-4 Audio standards. Those
> intending to use this software module in hardware or software products are
> advised that this use may infringe existing patents. [...] Copyright is not
> released for non MPEG-2 NBC/MPEG-4 Audio conforming products. [...] This
> copyright notice must be included in all copies or derivative works.

`sbr_dec.cpp:48-73` (Coding Technologies, "Copyright (c) ISO/IEC 2002") has
the same terms. So PacketVideo is **not plain Apache-2.0**. A copy or
derivative of those 114 files carries a field-of-use condition that a BSD-3
module cannot pass on. We study them and make no copy or derivative of them.

### What each licence allows in `decode`

| Source | Read to understand | Copy tables | Port or translate code | Run as an oracle |
|--------|--------------------|-------------|------------------------|------------------|
| 3GPP C code (S2) | yes | **no** (no permission) | **no** | yes, built locally, never distributed |
| PacketVideo, Apache header only | yes | yes, with attribution | allowed by Apache-2.0, **not done** (user's direction) | yes |
| PacketVideo, Apache + MPEG notice | yes | **no** without a user decision (G4) | **no** | yes |
| ffmpeg, FAAD2 (LGPL/GPL) | yes: what is computed and in what order | **no** | **no** | yes (ffmpeg) |
| Symphonia (MPL-2.0) | yes, not needed | a copied table puts that Go file under MPL-2.0 | a translation is a Modification: those files stay **MPL-2.0**; `decode` would no longer be BSD-only. **Not done.** | yes (LC only) |
| ISO conformance inserts (S4) | — | — | — | yes: fetched at test time, **hashes only** in the repository |

**Apache-2.0 obligations when PacketVideo is studied (section 4):**

- *Algorithm re-expressed in our own float code:* Apache-2.0 covers copying
  the work, not ideas, so nothing is strictly required. The manifest still
  records PacketVideo as `studied`, so that provenance can be shown.
- *A table copied from a file with the Apache header only:* that Go file is a
  derivative. It needs (4a) the Apache licence text distributed with it, (4b)
  a statement that it was changed ("converted from PacketVideo `<file>` to
  Go"), (4c) the PacketVideo copyright notice kept. (4d) The AOSP `NOTICE`
  holds only the licence text, so no extra NOTICE content is owed; a
  `THIRD_PARTY.md` lists the copied tables anyway. Apache-2.0 is compatible
  with BSD-3: the module stays BSD-3 and those files are Apache-2.0.
- *A table from a file that also has the MPEG notice:* not copied (G4).
  In practice the tables we need are integers that we regenerate from their
  definition, or reals that PacketVideo holds only in Q15 (useless to a float
  decoder anyway).

## What was read in ffmpeg (S9, FFmpeg `n9.0.2`, LGPL; read, not copied)

| File:lines | What it tells us |
|------------|------------------|
| `libavcodec/aac/aacdec.c:1263-1296` | IMDCTs are `av_tx` float MDCTs with the scale `1/len/32768` folded into the transform; output therefore in ±1.0 float32 |
| `libavcodec/aac/aacdec.c:1353` | PNS random state seed `0x1f2e3d4c`, one state per decoder |
| `libavcodec/aac/aacdec_proc_template.c:39-41, 93, 104, 274` | PNS uses the LCG `x*1664525 + 1013904223`; dequantisation reads a precomputed `n^(4/3)` table (`ff_cbrt_tab`) |
| `libavcodec/aac/aacdec_dsp_template.c:41-79` | scalefactor gain from a float `2^(sf/4)` table (`ff_aac_pow2sf_tab`), one multiply per band |
| `libavcodec/aac/aacdec_dsp_template.c:325-387` | windowing: one `vector_fmul_window` per half-window; start/stop transitions are treated as short-to-short |
| `libavcodec/aac/aacdec_float.c:58-72`, `libavcodec/aactab.c:84-100` | KBD alpha 4 (long) and 6 (short); sine windows by formula; `pow2sf` built from `exp2` steps, not `pow` |
| `libavutil/tx_template.c:1223-1342, 2107-2129` | inverse MDCT = pre-twiddle, complex FFT of `len/2` (split-radix codelets, preshuffled), post-twiddle; twiddles computed in `double` by `cos/sin`, scaled by `sqrt(scale)`, then rounded to float |
| `libavcodec/aacsbr_template.c:1366-1461` | QMF analysis: reversed window multiply, `sum64x5`, pre-shuffle, a 64-point MDCT, post-shuffle; synthesis: two 64-point MDCTs (one in the down-sampled mode), de-interleave, then 10 `vector_fmul`/`vector_fmul_add` passes in a fixed order |

Read for the AAC-LC bitstream PR (plan item 4), also read only:

| File:lines | What it tells us | Used for |
|------------|------------------|----------|
| `libavcodec/aactab.c:38-91` | `pow2sf` is built as a float32 power of two times a float32 `2^(k/16)`, which is the correctly rounded float32 of `2^(i/4)` | `aacGain` is `float32(math.Exp2(...))` |
| `libavcodec/aactab.c:149-179` | sample-rate index to table row; band counts | row choice in `aacSwbLongIndex` and `aacSwbShortIndex` |
| `libavcodec/aactab.c:181-535, 1581-1960` | the bare codeword, length and band-offset values | values only, carried to `aac_tables.go` by a throwaway generator and declared in `.provenance.yml` |
| `libavcodec/aactab.c:543-1026` | the float vector tables of codebooks 1 to 11 (index to tuple, magnitude `q^(4/3)`) | **a check** of `aacTuples` (digit order, offsets, signed or unsigned books), run once and not committed; nothing copied |
| `libavcodec/aac/aacdec_proc_template.c:57-139` | spectral data is read group by group, band by band, window by window inside the group | the loop order of `spectral` |
| `libavcodec/aac/aacdec_dsp_template.c:36-84` | the gain of a band comes from the scalefactor less 100; noise and intensity bands carry no spectrum | `dequantise` |
| `libavcodec/aac/aacdec.c` (comment lines only: 816, 968, 1359, 1416, 1538, 1583, 1649, 1674, 1730, 1778, 1875) and FAAD2 `libfaad/syntax.c` (comment lines only) | which table of ISO/IEC 14496-3 describes each syntax element | the table numbers cited in the Go comments |

Read for the AAC-LC synthesis PR (plan item 5), also read only:

| File:lines | What it tells us | Used for |
|------------|------------------|----------|
| `libavutil/tx_template.c:540-586, 599-722, 1223-1342` and `libavutil/tx.c:125-154, 198-262`, `libavutil/tx_priv.h:87-110`, `tx_template.c:2107-2134` | the order of the float operations of the split-radix FFT (the base cases of 2 and 4 points, the combine step with its six temporaries), the input permutation, the twiddle tables and their rounding, the MDCT's pre- and post-rotation | `aac_mdct.go`: the arithmetic is the same, the code is ours (a recursion with one combine function instead of unrolled codelets) |
| `libavutil/float_dsp.c:27-125` | `vector_fmul_window`, `butterflies_float`, `vector_fmul_scalar`, the product sum | the order of the window overlap, M/S and noise energy in `aac_synth.go` |
| `libavcodec/aac/aacdec_dsp_template.c:81-219, 325-384` | M/S, intensity, TNS, the window overlap and buffer update cases | the stage order and the cases of `aac_synth.go` |
| `libavcodec/aac/aacdec_proc_template.c:57-139, 292-326`, `aacdec.c:1263-1296, 1592-1646, 1678-1727, 1785-1918, 2123-2170`, `aacdec_float.c:56-73, 91-113`, `lpc_functions.h:50-106`, `kbdwin.c:33-60`, `sinewin_tablegen.h:58-65`, `aactab.c:110-147, 1968-1997` | the noise generator and its energy rule, the scalefactor offsets and clips, TNS parsing and filtering, the stage order after the spectrum, the transform scale 1/N/32768, the KBD and sine windows, the TNS maximum bands | the same, and the constants of `aacTNSMaxBands` |

Read for the SBR parser PR (HE-AAC v1, `aac_sbr*.go`), also read only (user
decision 2026-11-12: G1 is settled for SBR by declaring bare normative values in
`.provenance.yml`; `copycheck` v0.102.0 reports 0 findings):

| File:lines | What it tells us | Used for |
|------------|------------------|----------|
| `libavcodec/aacsbr_template.c:188-247` | `sbr_header`: field order, the defaults when `bs_header_extra_1/2` are 0, the spectrum parameters whose change forces a table rebuild | `sbrState.header` |
| `libavcodec/aacsbr_template.c:249-491` | master frequency table: start/stop minima by rate, `sbr_offset`, the stop band widths, both `bs_freq_scale` cases, the two-region split and its warp, the checks | `sbrTables.makeMaster` (float64 for the log and the power; ffmpeg uses float32) |
| `libavcodec/aacsbr.c:67-83` | `make_bands`: band widths from rounded geometric steps | `sbrMakeBands` |
| `libavcodec/aacsbr_template.c:493-622` | patches and the derived tables: high/low/noise, `n_q`, patch loop with its guards | `sbrTables.makePatches`, `makeDerived` |
| `libavcodec/aacsbr_template.c:122-186` | limiter band table | `sbrTables.makeLim` |
| `libavcodec/aacsbr_template.c:624-1180` | `sbr_grid` for the four frame classes, `bs_pointer`, `l_A`, delta-coding flags, inverse filtering, envelope and noise floor decoding with the three frequency-resolution cases, coupled and uncoupled pairs, the extension loop, the byte accounting | `aac_sbr.go` |
| `libavcodec/aac/aacdec_tab.c:114-280` | the ten Huffman codes as (symbol, length) pairs, the symbol offsets, canonical code from lengths | `aacSBRHuffSyms`, `aacSBRHuffLens` (values only), `buildSBRHuff` |
| `libavcodec/aacsbrdata.h:35-42` | `sbr_offset` | `sbrOffset` |
| `libavcodec/sbr.h:63-117, 143-193` | which values live from one frame to the next, and the array sizes | `sbrChannel`, `sbrState` |
| `libavcodec/aac/aacdec.c:2024-2083` | SBR payload sits in a fill element of type 13 or 14 (with CRC), attached to the element before it | `aacParser.sbrFill` |

Read for the SBR QMF and high frequency generation PR (part 2: `aac_sbr_qmf.go`,
`aac_sbr_hf.go`, `aac_sbr_qmf_tables.go`), also read only. The decoder still refuses
HE-AAC: the envelope adjustment (part 3) is not written, so nothing calls this code
from `aacDecoder`.

| File:lines | What it tells us | Used for |
|------------|------------------|----------|
| `libavcodec/aacsbr_template.c:1363-1405` | analysis: 320 samples of history, 32 new per slot, reversed window multiply, `sum64x5`, a 64-point transform with a pre- and a post-shuffle | the form of the analysis and the stage order in `sbrAnalysis.process` |
| `libavcodec/aacsbr_template.c:1408-1463` | synthesis: history of 1280 moved back once per slot, two transforms (real and sign-alternated imaginary parts), a butterfly, ten window passes at offsets 0, 192, 256, 448, 512, 704, 768, 960, 1024, 1216 | the form of `sbrSynthesis.process` |
| `libavcodec/sbrdsp.c:46-95`, `sbrdsp_template.c:27-48` | the shuffles and the butterfly, bit by bit | worked out, with the transform definition of `aac_exact_test.go:81-101`, to the angle of the kernels (`(4n-1)` in the analysis, `(2n-255)` in the synthesis) and the factor 2; the check was a throwaway program, not committed |
| `libavcodec/aacsbr_template.c:85-107` | the transforms' scales: `1/(64*32768)` in synthesis, `-2*32768` in analysis | the subband domain is in 16-bit units |
| `libavcodec/aacsbr_template.c:1465-1560, 1690-1780` | the low band holds 8 slots of history, the high band is generated from `X_low[i+2-2..i+2]`, `X_high` over `2*t_env[0]` to `2*t_env[n]`, the stage order of one frame | `sbrChanDSP.analyse`, `generate`, the constants `sbrHFGen` and `sbrHFAdj` |
| `libavcodec/aacsbr.c:149-226`, `sbrdsp.c:101-184`, `sbr.h:90`, `aacsbr.h:37` | the covariance sums, the 1.000001 regularisation, the limit of 16 on the squared magnitude, the chirp table and its smoothing, the 38-sample window | `sbrPredictor`, `sbrChanDSP.chirp` (the estimate is derived from the least-squares problem, see the comment of `aac_sbr_hf.go`) |
| `libavcodec/aacsbrdata.h:45-206, 208-371` | the 320 and 640 window values; the first is every second value of the second (checked); two entries of each are written `-Q31(x)`, the sign outside the macro | the values of `sbrQMFWindow`, carried by a throwaway generator |

Envelope adjustment (part 3, piece 1: `aac_sbr_adj.go`, `aac_sbr_noise_table.go`).
Written from the algorithm as known, without reopening ffmpeg while writing; the
lines of ffmpeg that were read are `libavcodec/aacsbr.c:239-259` (the gain
formulas) and `296-347` (the smoothing filter and its use), which a search showed,
and `libavcodec/sbrdsp_template.c:112-388` and `aacsbr.c:298-304` for the values of the
noise table and the smoothing taps (the second is a raised cosine, computed).
Where the algorithm leaves a choice, the fixtures decided it by trying the
alternatives against ffmpeg's decoded output: the default `bs_smoothing_mode` 1 is no
smoothing, the noise index starts at 1 and steps once per subband and slot, the
sinusoid phase starts at 0 and steps once per slot, the limiter caps the noise level
with the gain and does not cap the sinusoid. An attack envelope (l_A or l_A_prev)
adds no noise and counts none in its boost (a first version misread this in the decoder
and the reference alike, which is why the reference's 1.8e-7 hid it; ffmpeg's output
showed it, and the committed streams are now asserted to 3e-6 of ffmpeg's). A
limiter band's gain cap is at most 1e5, and a header reset refills the smoothing
history with the first slot's gains: both from the rule, no fixture reaches them, and
crafted streams do. What the fixtures cannot decide (no
fixture has smoothing on, a coupled sinusoid or a second noise floor with ffmpeg
output of its own) is held by the written streams of `TestSBRAdjustWritten` against
the reference only, and is therefore the part to check first against the ISO
conformance streams (part 3, piece 3). The noise table could not be checked against a
second source (FAAD2 holds it in another form, PacketVideo in Q-format; not read):
`TestSBRNoiseTable` pins it against change, not against error.

Differences from ffmpeg, by design: every stage is float64 and the output is rounded
once; the transforms are two DCT-IV of 64 points (go-fft's plan; the sine part is a DST-IV, done as a DCT-IV of the reversed input), not a 64-point MDCT;
the low band keeps its history in all 32 bands where ffmpeg zeroes the bands between
`kx[0]` and `kx[1]`; frames of 1024 only (ffmpeg's autocorrelation has the 38 terms
of that frame length written in; its 960 mode was not examined). Not done: the downsampled
(32-band) synthesis used when the output keeps the core rate, and implicit signalling
(the first frames before the first SBR header are not at the double rate).

Behaviours kept from ffmpeg and worth knowing: a refused header turns SBR off
silently (pure upsampling, the core signal still decodes); a faulty payload turns
SBR off until the next header and the frame decodes without it (`aacParser.sbrErr`
says why); a degenerate header can give an empty limiter table or a repeated noise
border; the end of the limiter table can sit below the stop frequency when the last
patch was dropped. PS extension data is skipped (its presence is recorded in
`sbrState.psSeen`). One difference: scalefactors are range-checked as integers
(0 to 127, noise 0 to 30), ffmpeg checks a byte after wrap-around.

The syntax itself (`aac_syntax.go`) is written from the syntax tables of
ISO/IEC 14496-3 subpart 4 as known to the author; the reference decoders'
syntax functions were not opened for it.

### A measurement that shapes the target

ffmpeg 9.0.2 (Homebrew, arm64, NEON) decoding the same AAC-LC file to f32le,
2026-11-10:

| Decoder run | MD5 of the PCM |
|-------------|----------------|
| `aac`, default CPU flags (NEON) | `fa09813e5fbb8f743af04cdc06a501d3` |
| `aac`, `-cpuflags 0` (C code paths) | `5ce506d6483c7d92a615acb59b1012b9` |
| `aac_fixed` | `eeecc5e6482b0c1b215ec675b7762615` |

**ffmpeg does not agree with itself across CPU paths.** "Bit-exact with
ffmpeg" therefore has to name a path. The only portable one is
`-cpuflags 0` (the C code), and even that depends on whether the compiler
contracted `a*b+c` into FMA. Our exact target is **ffmpeg `-cpuflags 0`**;
the default path is compared within a tolerance.

## Coverage matrix

Confidence: **complete** = open sources give enough to write the stage;
**partial** = a detail or a float constant is missing; **missing** = none.
"vs ffmpeg C path" says whether exact float32 equality is a realistic target
once ffmpeg's order is known.

| Component | Sources | Confidence | What PacketVideo teaches | Our float form | vs ffmpeg C path |
|-----------|---------|-----------|--------------------------|----------------|------------------|
| ASC, ADTS, ADIF, PCE, SBR signalling | S6 `get_audio_specific_config`, `get_adts_header`, `get_prog_config` | complete | field order, explicit/implicit SBR | integers | exact |
| raw_data_block, SCE/CPE/LFE/DSE/FIL, ics_info, sections, scalefactors, pulse | S6 `getics`, `get_ics_info`, `hufffac`, `get_pulse_data`, `getfill` | complete | syntax, grouping, `max_sfb` limits, escapes | integers | exact |
| Huffman books 1-11 and scalefactor book | S6 `hcbtables_binary.cpp`, `huffcb.cpp` (MPEG notice) | complete as knowledge; tables regenerated (G4) | codes and lengths | integers | exact |
| Band offsets per rate | S6 `sfb.cpp` (MPEG notice) | as above | the tables | integers | exact |
| Inverse quantisation `|x|^(4/3) · 2^(sf/4)` | S6 `esc_iquant_scaling`; S9 | complete | formula, 8191 limit | float32 table of `n^(4/3)` and `2^(k/4)` built the way S9 builds them (round from double) | exact if both tables round identically: checked table against table |
| M/S, intensity | S6 `ms_synt`, `intensity_right` | complete | band rules, books 14/15 | float32 add/sub, scale | exact |
| PNS | S6 `pns_*`, `gen_rand_vector`; S9 LCG and seed | complete vs ffmpeg | energy rule, correlated PNS | the same LCG and seed, the same call order | exact vs ffmpeg; vs conformance only energy per band (G5) |
| TNS | S6 `tns_*`; S9 | complete | coefficient decoding, direction, order | float32 lattice filter | exact if the filter loop order matches (read in S9 before PR 4) |
| LTP | S6 `long_term_prediction` | complete in PV | — | **skipped** first | — |
| Main prediction | none in PV | missing | — | **not supported** | — |
| IMDCT 2048/256 | S6 `imdct_fxp`; S9 tx | complete | — | pre-twiddle, `len/2` split-radix complex FFT, post-twiddle, twiddles from double `cos/sin` rounded to float32 | exact only by reproducing the same split-radix factorisation and butterfly order; realistic but the hardest part. Fallback: ≤ a few float32 ulp |
| Windows, overlap-add | S6 `window_tables_fxp.cpp:50-71`; S9 alpha 4/6 | complete (G2 closed) | sine formula, KBD formula, window sequences | windows from formula in double, rounded to float32; one multiply-add per sample in the same order | exact |
| 960 framing | — | missing | — | **skipped** (S1 excludes it) | — |
| SBR bitstream and Huffman | S6 `sbr_get_*`, `sbr_decode_huff_cw` | complete | syntax, delta coding, coupling | integers | exact |
| SBR frequency tables, limiter, patches | S6 `sbr_update_freq_scale`, `sbr_create_limiter_bands`, … | complete | derivation rules | integers (+ `log2` in limiter) | exact |
| SBR QMF analysis/synthesis | S6 `calc_sbr_*filterbank`, `qmf_filterbank_coeff.cpp:148` (Q15, "10/9*table"); S9 order | **done (part 2): 64-band synthesis; the 32-band mode of a downsampled output is not**; prototype declared in `.provenance.yml` | modulation | float64 in every stage, one float32 rounding at the output, go-fft DCT-IV and DST-IV | within one float32 rounding of the float64 definition and of the 256-bit evaluation; low band of `he44-st.aac` against the exact path: 2.3e-8 largest error, ffmpeg's two builds 6.3e-5 (a stated bound, not equality) |
| HF generation | S6 `calc_auto_corr`, `sbr_generate_high_freq` | **done (part 2)** | covariance, chirp, patches | float64 | 4e-10 relative against the exact rational solution of the normal equations (float32 evaluation: 0.24 on tonal bands) |
| HF adjustment | S6 `calc_sbr_envelope`, `sbr_envelope_calc_tbl` | **done (part 3, piece 1)**; noise table declared `normative-values`, smoothing filter `derived` | operation order, limiter, smoothing | float64, noise table in float64 | within 1e-6 of the peak (1.8e-7 measured) of a by-definition float64 path; ffmpeg's builds 4.5e-7 off it at most, and this decoder within 2.5e-6 of ffmpeg's output sample by sample on every committed stream and channel (asserted at 3e-6) |
| SBR LP | S6 `sbr_aliasing_reduction` | partial | aliasing reduction | only if needed | — |
| PS bitstream | S6 `ps_read_data`, `ps_decode_bs_utils` | complete | syntax, dequantisation | integers | exact |
| PS hybrid filterbank | S6 `ps_hybrid_*` | partial (G1) | band split, filter lengths | float32; prototype coefficients needed in float | tolerance until G1 |
| PS decorrelator, transients, mixing | S6 `ps_decorrelate`, `ps_all_pass_*`, `ps_stereo_processing`; S8 eq. 4-8 | partial (G1) | all-pass structure, mixing matrix | phase factors by formula; decay constants needed in float | tolerance until G1 |
| CCE | S6 partly | partial | — | refused with a clear error at first | — |
| Error concealment | S1 points to TS 26.402 (not read) | missing | — | not needed: errors are reported | — |

## Gaps that open sources cannot close

- **G1. Real constants with no formula.** The SBR QMF prototype (640 taps),
  the PS hybrid prototype, the SBR noise table and smoothing filter, the PS
  all-pass decay values. PacketVideo holds them only in Q15/Q31; the 3GPP
  float code (S2) holds them in float without permission to reproduce;
  ffmpeg holds them under LGPL (not copied). Options for the user: (a) buy
  only the text that holds the tables; (b) ask 3GPP/ETSI for written
  permission to reuse the S2 tables; (c) widen PacketVideo's Q15 values to
  float: about 3e-5 error in the high band, so neither ffmpeg equality nor a
  16-bit conformance bound; (d) legal advice on whether a table of numeric
  constants from a standard is protected at all. **This decides SBR and PS.**
- **G2. KBD alpha:** closed. S9 gives alpha 4 (long) and 6 (short) as a
  parameter; the formula is in S6 `window_tables_fxp.cpp:59-71`; we check our
  float window against PacketVideo's Q15 table to within 1 LSB.
- **G3. The conformance criterion.** It is in the 14496-26 *text*, which is
  not free (S5). The inserts (S4) give streams and reference waveforms, not
  the criterion. Until that text is read, the conformance bound is labelled
  **provisional** in the code.
- **G4. Integer tables from files under the MPEG notice** (Huffman codes,
  band offsets). They are regenerated from their definition: the Huffman
  codes are checked by decoding the conformance streams exactly, and
  `copycheck` proves that no run of numbers was copied from a reference. If
  the user prefers to copy them from PacketVideo, they go into a separate
  file carrying both notices.
- **G5. PNS against conformance.** The ISO reference generator is not
  ours, so PNS conformance streams are checked by energy per band only.
- **G6. CCE** is rare and only partly covered: refused at first.

### What the black-box oracles can and cannot settle

- **Can:** the parse (a wrong parse is a wrong frame, not a small error); the
  sample count, priming and edit lists (exact, as in the MP3 test); whether
  ffmpeg runs SBR HQ or LP; implicit SBR rate doubling.
- **Cannot:** a float constant (G1): fitting a table to ffmpeg's output would
  be copying by other means and is not done; the conformance bound (G3).
- **PacketVideo and the S2 float code as oracles** (built locally, if they
  build) give the intermediates: scalefactors, sections, quantised spectra,
  TNS coefficients, SBR frequency tables and envelope indices. These are
  integers and must match **exactly**.

## Exactness and tolerance

| Stage | Comparison | Against |
|-------|-----------|---------|
| Parsing, scalefactors, Huffman, sections, pulse, TNS coefficients, SBR grid/envelope/noise indices, SBR frequency tables, PS indices | **exact** (integers) | PacketVideo and S2, frame by frame |
| AAC-LC PCM | **exact float32** is the target | ffmpeg `-cpuflags 0` |
| AAC-LC PCM | tolerance | ffmpeg default path (NEON/AVX), conformance waveforms |
| SBR/PS PCM | tolerance until G1; exact target after G1 | ffmpeg `-cpuflags 0`, then conformance |

Go may fuse `x*y+z` into an FMA on arm64. Every expression that must match
float32 order is written with explicit `float32(...)` conversions, which
forbid fusion, and is tested on amd64 and arm64.

### Tolerance policy

1. The conformance bound comes from the 14496-26 text (G3), not from a first
   run. Until it is read, the provisional bound follows from the reference
   waveforms' format: at 16-bit output, every sample within **1 LSB
   (2^-15 full scale)**, stated as provisional in the code.
2. Against ffmpeg's default path, the bound is first **measured as ffmpeg's
   own disagreement**: ffmpeg NEON versus ffmpeg `-cpuflags 0` on the same
   files (the table above shows they differ). Our decoder must be no further
   from either path than they are from each other. That bound is derived
   from ffmpeg itself, not from our first run.
3. A failure is a defect to explain, never a reason to widen the bound.

## How the separation is kept

The rule is enforced by `copycheck` (go-fleettools/fleettools, being built),
run in `decode`'s CI. It reads a provenance manifest, refuses copyleft
references in the roles `tables-from` and `ported-from`, and fingerprints
(winnowing) both numeric runs and C-to-Go-normalised code of every reference
against `decode`'s Go files, failing on a match.

`decode`'s manifest for AAC should hold exactly these references, each
pinned by URL, revision and content hash:

| Reference | Pin | SPDX | Role |
|-----------|-----|------|------|
| PacketVideo AAC decoder, AOSP `platform/frameworks/av` `media/libstagefright/codecs/aacdec` | tag `android-4.1.1_r1`, commit `2df63ad6bbf0ad0fd066592b44f2e15123513112` | `Apache-2.0` (+ MPEG notice on 114 files, recorded as a note) | `studied`, `oracle` |
| FFmpeg `libavcodec/aac/`, `libavcodec/aacsbr*`, `libavcodec/aacps*`, `libavcodec/sbrdsp*`, `libavutil/tx*`, `libavutil/float_dsp*` | tag `n9.0.2`, commit `946fcce07b6dcd0331c8cc609192aeff5e1924f8` | `LGPL-2.1-or-later` | `studied`, `oracle` (never `tables-from`/`ported-from`: refused by the gate) |
| FAAD2 | — | `GPL-2.0-or-later` | `oracle` only (not read) |
| fdk-aac | — | `LicenseRef-FDK-AAC` | `oracle` only (not read) |
| tphakala/go-aac | — | `LGPL` | `oracle` only (not read) |
| 3GPP TS 26.410 C code | `26410-i00-ANSI-C_source_code.zip` (inside `26410-j00.zip`) | `LicenseRef-3GPP-copyright` | `studied`, `oracle` (never copied: no permission) |
| 3GPP TS 26.401 | `26401-j00.zip` | `LicenseRef-3GPP-copyright` | `studied` |
| ISO/IEC 14496-26 ed. 2 inserts | `mpeg4audio-conformance2ndEd_20230125.md5` | `LicenseRef-ISO-electronic-insert` | `oracle` (fetched at test time, never committed) |
| Symphonia `symphonia-codec-aac` | pinned release | `MPL-2.0` | `oracle` (not read for code) |
| Purnhagen, DAFx'04 | DOI/URL | `LicenseRef-paper` | `studied` |

Rules that go with the manifest:

- No AAC table is copied; every table is generated by Go code from its
  definition (formula, rule, or decoding of the conformance streams), and the
  generator is committed. If the user later allows Apache-only PacketVideo
  tables, they are added with role `tables-from` and the obligations above.
- A PR that touches AAC code lists in its body which manifest entries were
  consulted for it.
- This note's "What was read in ffmpeg" table is the read log; it is extended
  (file:lines) by every PR that reads more.

## Plan (one PR each, green before the next)

**No AAC code PR opens before `copycheck` runs in `decode`'s CI with the
manifest above.**

1. **This note** (docs only).
2. **Gate:** the manifest and the `copycheck` CI job.
3. **Oracle harness:** `tools/aacconf` fetches the S4 inserts into a durable
   cache, checks them against the md5 list, keeps hashes in `testdata`;
   builds PacketVideo and the S2 float decoder locally for intermediates, if
   they build; hashes ffmpeg fixtures for both `-cpuflags 0` and the default
   path, and measures how far apart the two ffmpeg paths are (tolerance
   rule 2). Real files from `~/Movies`: hashes only.
4. **AAC-LC bitstream** (exact against PacketVideo/S2), fuzzed with a
   committed corpus, bomb tests for frame size and channel count.
5. **AAC-LC synthesis:** dequantisation, M/S, intensity, PNS, TNS, IMDCT,
   windows. Exact float32 against ffmpeg `-cpuflags 0` as the target;
   conformance `al*` streams within the provisional bound; speed against
   ffmpeg `-threads 1`.
6. **User decision on G1** (and G4 if tables are to be copied). Nothing in
   SBR starts before it.
7. **SBR (HQ):** bitstream and frequency tables (exact; part 1, decode#45), QMF
   banks and HF generation (part 2, this note: against the float64 reference,
   256-bit and exact-rational oracles, and ffmpeg's low band), adjustment (part 3,
   not written). Conformance `al_sbr_*` streams.
8. **PS:** bitstream (exact), hybrid filterbank, decorrelator, mixing.
   Conformance `al_sbr_ps_*` streams.
9. Later and separately: LTP (`ap*`), 960 framing, CCE.

## Go / no-go

- **AAC-LC: go.** Every stage is integer (exact against PacketVideo and S2)
  or follows from formulas computed in float. With ffmpeg's order known,
  **exact float32 equality with ffmpeg `-cpuflags 0`** is a realistic target;
  the IMDCT's split-radix order is the hardest part, with a fallback of a few
  ulp. ffmpeg's default (SIMD) path differs from its own C path, so against it
  only a tolerance makes sense. Conformance against the ISO waveforms is
  possible with the free inserts, under a provisional bound until G3.
- **SBR: conditional on G1.** The algorithm and ffmpeg's order are known; the
  640 QMF coefficients and the noise/smoothing constants have no float source
  we may copy. Go once the user decides G1; with PacketVideo's Q15 values the
  decoder works but meets neither ffmpeg equality nor a 16-bit bound.
- **PS: conditional on G1** as well (hybrid and all-pass constants), and it
  depends on SBR.

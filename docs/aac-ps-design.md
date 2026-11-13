# Parametric Stereo (HE-AAC v2): design

Status: design note, no code. Refs go-avkit/avkit#89. Written 2026-11-13.
It continues [aac-design.md](aac-design.md) (plan item 8) and follows its rules:
copyleft code is read to understand what is computed and in what order, never
copied, translated or paraphrased; bare normative values may be held when
declared in `.provenance.yml`, and a value that follows from a formula is
computed from the formula instead.

Why now: of the 822 HE-AAC files in the user's library, **801 signal PS**
(decode#52, `TestAACRealHE`). Until PS is decoded they come out as the SBR mono
channel while ffmpeg gives stereo, so they cannot even be compared.

## Sources (opened for this note)

| # | Source | What it gave |
|---|--------|--------------|
| P1 | H. Purnhagen, "Low complexity parametric stereo coding in MPEG-4", DAFx'04, pp. 163-168, <https://dafx.de/paper-archive/2004/P_163.PDF> | IID/ICC definitions (eq. 1-2), up-mix matrix H (eq. 4-6), β exact (eq. 7) and approximated (eq. 8), the hybrid filter bank (eq. 11-13, Fig. 4: M0=8, M1=M2=4 with outputs summed, 71 bands; 34-band variant M0=12, M1=8, M2..4=4), the decorrelator (§4.1: all-pass "for the first 23 QMF bands", a chain of three delay lines of 3, 4, 5 QMF samples, fractional delay as a phase rotation at the band's centre frequency), the transient reduction (§4.2, qualitative), linear interpolation of H between parameter positions (§3.3), PS placed just before the 64-band synthesis (Fig. 6b) |
| S9 | FFmpeg `n9.0.2` (LGPL-2.1+), the lines listed below; read only | what ffmpeg computes and in what order; the bare values it holds |
| — | `decode` at `e46043c` (main): `aac_sbr.go`, `aac_sbr_hf.go`, `aac_sbr_qmf.go`, `aac_sbr_adj.go`, `docs/aac-design.md`, `.provenance.yml`, `testdata/aac/README`, and decode#52's description | the interfaces PS plugs into |

Not opened for this note, so not cited for any fact: FAAD2 `ps_dec.c` (the
manifest lists FAAD2 as `oracle`; a PR that reads it changes the role to
`studied` first), PacketVideo's `ps_*` files (MPEG notice: study only, nothing
taken), the ISO/IEC 14496-3 text, the AES papers P1 cites ([4], [10]).

### What was read in ffmpeg (S9, `n9.0.2`, read, not copied)

| File:lines | What it tells us |
|------------|------------------|
| `libavcodec/aacps_common.c:28-110` | parameter counts per mode (10/20/34 IID-ICC, 5/11/17 IPD-OPD), envelope counts per frame class, time/frequency delta decoding of each parameter, the range checks (IID magnitude ≤ 7 or 15, ICC ≤ 7, IPD/OPD modulo 8) |
| `libavcodec/aacps_common.c:111-289` | `ps_data` order: header (IID mode, ICC mode, extension flag), frame class, envelope borders (fixed class: `e*32/num_env - 1`), IID, ICC, extension (IPD/OPD under extension id 0), the extra envelope added when the last border is not slot 31, the 34-band switch, the error path (PS off until the next header, parameters zeroed), the bit accounting |
| `libavcodec/aacps_common.c:291-306`, `aacpsdata.c:24-97` | ten Huffman codes as (symbol, length) pairs **in codeword order** (not sorted by length), symbol offsets -30, -14, -7, 0 |
| `libavcodec/aacpsdata.c:99-113` | the hybrid-band to parameter-band maps (71 and 91 entries; its comments cite Tables 8.48 and 8.49, not verified) |
| `libavcodec/aacps_tablegen.h:38-214` | the hybrid prototypes (7 taps of 13 each), their modulation, the IID dB steps as linear gains, the ICC values and their arc cosines (8 digits), mixing procedures A and B, IPD/OPD smoothing, the centre frequencies and fractional delays (0.39; 0.43, 0.75, 0.347) |
| `libavcodec/aacps.c:37-184` | the 2-band real prototype, the 20- and 34-band hybrid layouts and which outputs are summed; the look-ahead (below) |
| `libavcodec/aacps.c:186-397` | decay slope 0.05 from band 10/32, all-pass band counts 30/50, short-delay band 42/62, the 10/20/34 parameter re-mappings |
| `libavcodec/aacps.c:399-517` | transient detection (peak decay, smoothing 0.25, impact 1.5), the all-pass bands, the 14-slot and 1-slot delay bands, state reset on a 20/34 switch |
| `libavcodec/aacps.c:557-738` | mixing per envelope, matrices carried from the previous frame's last envelope, IPD/OPD phase smoothing and sign rule, linear interpolation from one border to the next; `ff_ps_apply` zeroes delay lines above the SBR top band `kx+m` |
| `libavcodec/aacps.h:32-104` | state sizes (5 envelopes + 1, 91 bands, delay 14, all-pass delay 5, 3 links) |
| `libavcodec/aacpsdsp_template.c:29-211` | the C kernels: sum of squares, real gain on complex, the 13-tap analysis, the all-pass link coefficients (0.65143905753106, 0.56471812200776, 0.48954165955695), the 2×2 interpolated mix with and without IPD/OPD |
| `libavcodec/aacsbr_template.c:965-989, 1526-1559, 1753-1780` | PS extension read only when PS is signalled; SBR output `X` is 38 slots, the last 6 holding the low band only; PS applied to channel 0's `X` before both syntheses; before the first PS header the mono `X` is copied to the second channel |

## Decisions and blockers before any PS code

These come first because they change the scope of the PRs below.

1. **Channel count can change mid-stream (decide in PS1).** With implicit
   signalling, PS can appear only in the frames, after a mono start. The
   output would then go from 1 channel to 2. decode's output API does not
   allow that: `Audio.Channels`, the channel layout and the slot map are fixed
   for a track. Options to choose from in PS1: always announce stereo for an
   implicit mono SBR stream (ffmpeg's choice), stay mono and drop the PS data,
   or refuse. Nothing past PS1 starts without this decision.
2. **decode#52's blocker applies to PS files.** The edit list is applied at
   the core rate, not the output rate. That affects the PS files too, so
   PS4's 801-file comparison against ffmpeg depends on its fix.
3. **The library census comes before PS2/PS3 are scoped.** Count 20 vs 34
   bands, IPD/OPD use, frame classes and signalling (explicit, backward
   compatible, implicit) over the 801 files (PS1 logs it). If the library uses
   only one configuration, the others are carried by written and conformance
   streams alone, and PS2/PS3 are sized and ordered accordingly.

## Where PS sits in decode

PS runs in the QMF domain between the SBR envelope adjustment and the 64-band
synthesis (P1 Fig. 6b), on a single channel element whose SBR has one channel.

Today (`aac_sbr_adj.go:317-333`, `sbrState.frame`), per channel: `analyse` →
`hfGenerate` → `adjust` → `assemble` into `d.x [32][64]complex128` (16-bit
units, `aac_sbr_qmf.go:52`) → `d.syn.process`. With PS:

```
ch 0:  analyse → hfGenerate → adjust → assemble(d.x)
PS:    ps.apply(&d.x, look, top) → (xL, xR) [32][64]complex128
       synthesis(xL) with ch 0's bank, synthesis(xR) with a second bank
```

Interfaces, as types to be written (names indicative):

| Item | Where | Notes |
|------|-------|-------|
| `psState` | new field of `sbrState`, made on first PS payload of an element | all PS state per decoder: parameters of the last frame, hybrid history, delay lines, transient state, the matrices of the last envelope. Never shared (decode#40). |
| parse hook | `sbrState.parsePayload` (`aac_sbr.go:295-312`) | today `psSeen` is set and the extension skipped. With PS signalled, extension id 2 goes to `psState.parse(r, bits)`, which must consume at most the extension's bits (ffmpeg's accounting, `aacps_common.c:275-288`); otherwise it stays skipped. |
| input | `d.x` of channel 0 after `assemble` | 32 slots × 64 bands, the full band (low band below `kx`, adjusted high band up to `kx+m`). |
| **look-ahead** | `d.low[k][34..39]` for `k < kx[1]`, zero otherwise | The hybrid filters are 13-tap and centred: slot `l` needs slots up to `l+6`. ffmpeg's `X` has 38 slots whose last 6 hold the low band only (`aacsbr_template.c:1547-1552`), i.e. the analysis slots that the SBR's own 6-slot latency (`sbrHFGen - sbrHFAdj`) has not yet released. Our low band array holds exactly those (`aac_sbr_hf.go:59`, `assemble` reads `low[k][l+sbrHFAdj]`). So PS adds **no delay** and needs a `[6][5]complex128` look-ahead, not a new buffer. Bands below 5 sit above `kx` only for a degenerate header: then the look-ahead is zero, as in ffmpeg. |
| `top` | `s.t.kx[1] + s.t.m[1]` | delay lines of bands at or above it are cleared each frame (ffmpeg `aacps.c:726-729`). |
| output | two `[32][64]complex128` | channel 0 overwritten in place (L), R in the second channel's `sbrChanDSP.x`, synthesised by `dsp[1].syn` — the second channel's bank already exists for a CPE. |
| before the first PS header, or after a PS error | — | R = L (ffmpeg copies `X`), output stereo. |
| channel count | `aac_config.go` (decode#52) | PS signalled (explicit or backward compatible) → 2 channels from the first frame. The policy for implicit signalling (ffmpeg treats any implicit mono SBR stream as possible PS and outputs stereo, `testdata/aac/README` on he48-mono) is decided in PS1 (decision 1 above), informed by the 801 files' signalling census. |

## The pipeline, stage by stage

All stages float64 (complex128), one float32 rounding at the PCM output, as in
SBR. "Hybrid domain" = 71 rows (20-band configuration, also used for 10 bands)
or 91 rows (34-band) × 32 slots.

| # | Stage | Input | Output | State carried to the next frame |
|---|-------|-------|--------|---------------------------------|
| 0 | **Parse** `ps_data` and its extension | bits of the SBR extension | per envelope `e < num_env (+1)`: border slot, IID index, ICC index, IPD/OPD index per parameter band; flags `iid_mode`, `icc_mode`, `enable_ext`, `enable_ipdopd`, 34-band | last envelope's indices (time-delta base), `num_env_old`, 34-band flag, `start` |
| 1 | **Hybrid analysis** (P1 eq. 11, 13; Fig. 4) | `d.x` rows 0..2 (20) or 0..4 (34) + look-ahead; rows 3..63 (5..63) unchanged | hybrid domain, mono `s[k][l]` | the last 12 slots of the split rows |
| 2 | **Transient detection** (P1 §4.2) | squared magnitude of `s`, summed per parameter band (k→i map) | gain `g[i][l] ∈ (0,1]` | peak, smoothed power, smoothed peak difference per parameter band |
| 3 | **Decorrelation** (P1 §4.1) | `s` | `d[k][l] = g[i(k)][l] · (H_k * s_k)[l]` | per band: pre-delay line, 3 link delay lines (all-pass bands), 14- or 1-slot delay lines (others) |
| 4 | **Mixing matrices** (P1 eq. 4-8; procedure B) | indices of each envelope, re-mapped to the 20 or 34 bands in use | `h11, h12, h21, h22` (+ imaginary parts with IPD/OPD) per envelope and parameter band | the last envelope's matrices (interpolation start of the next frame), IPD/OPD index history |
| 5 | **Mix with interpolation** (P1 §3.3) | `s`, `d`, matrices at borders `b[e]`, `b[e+1]` | `L[k][l]`, `R[k][l]`, matrix linear in `l` between borders | — |
| 6 | **Hybrid synthesis** (P1 eq. 12) | L, R in the hybrid domain | two `[32][64]` QMF-domain blocks: split rows summed back, others copied | — |
| 7 | QMF synthesis ×2 | existing `sbrSynthesis` | PCM L, R | the two banks' histories |

A 20↔34 band switch between frames re-maps the carried matrices and resets the
decorrelator and transient state (ffmpeg `aacps.c:419-425, 598-626`).

## Table-by-table provenance

"Derivable" = computed in Go from the cited formula and checked against the
values ffmpeg holds by a test that compares numbers only (a check, nothing
carried). "Normative value" = a bare value of the standard with no formula we
can cite, declared `kind: normative-values` in `.provenance.yml`. Numbers in
the third column were checked on 2026-11-13 by a throwaway Go program (not
committed) against ffmpeg's printed digits.

| Table | Status | Basis and check |
|-------|--------|-----------------|
| Huffman codes (10 codes, 242 (symbol, length) pairs) | **normative values** | as the SBR codes (`aacSBRHuffSyms`). ⚠ ffmpeg lists them in codeword order, not by increasing length, so the code is assigned in list order; `buildSBRHuff` assumes sorted lengths and must be generalised or a PS builder written, checked complete by the prefix-code test. |
| Symbol offsets, parameter counts (10/20/34, 5/11/17), envelope counts, fixed-class borders | normative (syntax, < 32 numbers) | syntax constants |
| IID dB steps: 15 (default) and 31 (fine) | **normative values** (integers: ±25, 18, 14, 10, 7, 4, 2, 0 and ±50, 45, 40, 35, 30, 25, 22, 19, 16, 13, 10, 8, 6, 4, 2, 0) | — |
| IID linear gain `c` | **derivable**: `c = 10^(IID/20)` (P1 eq. 5) | equals ffmpeg's 46 gains to 14 digits |
| ICC values (1, 0.937, 0.84118, 0.60092, 0.36764, 0, -0.589, -1) | **normative values** | no formula found |
| `acos(ICC)` | **derivable**: `α = arccos(ICC)/2` (P1 eq. 5) | ffmpeg's table is rounded to 8 digits (largest error 8.9e-8 at -0.589); we compute it exactly — a known, bounded difference from ffmpeg |
| Mixing procedure A (`icc_mode < 3`) | **derivable**: P1 eq. 5 with β from **eq. 8** (`β = α(cr − cl)/√2`), not eq. 7 | ffmpeg uses eq. 8; whether the standard does is checked by the conformance streams (the reference holds both and reports which one matches) |
| Mixing procedure B (`icc_mode ≥ 3`) | **derivable** (our derivation, not in P1): the principal axes of the 2×2 channel covariance with powers `c², 1` and cross term `ρc` (P1 eq. 1-2 inverted): `α = ½·atan2(2ρc, c²−1)`, eigenvalues `λ1,2`, `γ = atan √(λ2/λ1)`; algebraically identical to the closed form in ffmpeg (`aacps_tablegen.h:159-174`). Two conventions are **normative values**: ρ floored at 0.05, α < 0 shifted by π/2 | to be confirmed on conformance streams with `icc_mode ≥ 3` |
| IPD/OPD angles | **derivable**: `k·π/4`, k < 8 | — |
| IPD/OPD smoothing weights (0.25, 0.5, 1 over two previous frames and the current) | **normative values** | — |
| IPD/OPD sign rule on the first hybrid bands (k ≤ 1 at 20 bands, 9..13 at 34) | **unknown** | ffmpeg's own comment doubts it (`aacps.c:687`); decided by the conformance streams with IPD/OPD |
| Hybrid prototypes (13 taps, symmetric; at 20 bands M = 8 and the 2-band real form; at 34 bands M = 12, 8, 4) | **partly derivable**: length and symmetry (P1 eq. 11, `(Λ−1)/2 = 6`); centre tap `1/M` and zero taps at `6 ± jM` from perfect reconstruction (P1 eq. 12: the sum over m of the modulated filters must be a unit impulse) — 5 centre taps and 4 zeros, all checked to 14 digits; **the other 26 taps are normative values** (no window or formula matched) | values read in `aacps_tablegen.h:53-71`, `aacps.c:37-40` |
| Hybrid modulation (`exp(j2π(m+½)(λ−6)/M)`) | **derivable**: P1 eq. 11 | the sign (ffmpeg indexes its input oldest first) is checked in PS2 by the identity and the ICC = 1 streams, not assumed |
| Which hybrid outputs are summed | 20 bands: **derivable** from P1 Fig. 4 (q0,2+q0,5, q0,3+q0,4; for bands 1 and 2 the M = 4 outputs summed in pairs, which P1 notes gives real-valued filters: ffmpeg holds them as one 2-band real prototype); 34 bands: **normative** (P1 gives only the M values) | — |
| Hybrid band → parameter band maps (71, 91 entries) and the 10/20/34 re-mappings | **normative values** | held as data (source, weight) lists, not as code, so no function's shape is reproduced |
| Centre frequency of each hybrid row (for the fractional delays) | **derivable**: QMF band q → `q + ½`; hybrid output m of band k split in M_k (P1 Fig. 4: 8, 4, 4) → `k + 2(m+½)/M_k` wrapped into `[k − ½, k + 3/2)` (QMF band units; a complex QMF band is sampled at 2 band widths); a summed pair takes the member that lies inside `[k, k+1)` | for 20 bands this reproduces ffmpeg's 10 values (-3, -1, 1, 3, 5, 7, 10, 14, 18, 22)/8, worked by hand; the 34-band values are derived in PS3 and checked the same way |
| Fractional-delay rotations `exp(−iπ·d·f)` | **derivable**: P1 §4.1 (a delay of d samples at centre frequency f) | — |
| Fractional delays d (0.39 pre-delay; 0.43, 0.75, 0.347 per link) | **normative values** | — |
| Link delays 3, 4, 5 slots | **derivable**: P1 §4.1 | — |
| Pre-delay of 2 slots, the 14-slot delay, the 1-slot delay | **normative values** | — |
| All-pass band counts 30 / 50 | **derivable**: P1 §4.1's "first 23 QMF bands": 10 hybrid + QMF 3..22 = 30; 32 hybrid + QMF 5..22 = 50 | matches |
| Short-delay start 42 / 62 | **normative value** (QMF band 35 in both layouts, consistent) | — |
| Link decay `a_m` (3 values) | **normative values**. They equal `exp(−d_m/7)`, d = 3, 4, 5, to 14 digits, but that formula was recognised after the fact and no source gives 1/7, so it is not a derivation | — |
| Decay slope `clip(1 − 0.05(k − k0), 0, 1)` | k0 **derivable** (the first unsplit QMF band: row 10 / row 32); 0.05 **normative** | — |
| Transient peak decay | **normative value** (0.76592833836465). It equals `exp(−4/15)` to 14 digits, recognised after the fact; no source gives 4/15 | — |
| Transient smoothing 0.25, impact 1.5 | **normative values** | — |

Summary: derivable by formula — linear IID gains, `acos(ICC)`, both mixing
procedures, IPD/OPD angles, hybrid modulation and 20-band summing, 9 of 35
prototype taps, centre frequencies, phase rotations, link delays, all-pass band
counts, decay start.
Normative values only — the 10 Huffman codes, IID dB steps, the 8 ICC values,
26 prototype taps, band maps and re-mappings, 4 fractional delays, the pre-,
14- and 1-slot delays, the 3 link decays and the peak decay (their exponential
forms were recognised, not sourced), 0.05, 0.25, 1.5, 0.05 ICC floor, IPD/OPD
weights.
Unknown — the IPD/OPD sign rule; and whether eq. 7 or eq. 8 and the procedure-B
conventions are the standard's (decided by measurement on conformance streams).
This closes gap G1 of aac-design.md for PS: no float constant is missing.

## Oracles and accuracy

The SBR lesson (aac-design.md, envelope adjustment): two bugs hid because the
reference shared the decoder's table or reading; what caught them was an
absolute bound against ffmpeg's **output** on real streams. PS keeps both, and
adds a way to make each stage visible in ffmpeg's output.

1. **Properties (no reading involved).** Hybrid analysis then synthesis is the
   identity (no delay: the filters are centred) to 1e-14 of the peak, for both
   layouts; each all-pass `H_k` has `|H_k(e^{jω})| = 1` before the transient
   gain; procedure A keeps `cl² + cr² = 2` (P1 eq. 6); procedure B's columns
   reproduce the covariance it was derived from; on stationary noise with
   parameters held, the output's measured IID and ICC (P1 eq. 1-2) converge to
   the transmitted ones.
2. **Independent float64 reference** (test-only), written from the definitions,
   sharing no table with the decoder except the declared normative values:
   eq. 11 as a direct 13-tap sum with `math.Sincos` of the exact argument for
   **all** M outputs, then the summing of Fig. 4; the decorrelator as a
   convolution with its **closed-form impulse response** (a pre-delay, the
   rotation, and the product of three first-order all-pass sections in `z^-d`,
   expanded as series and truncated below 1e-20), not as the decoder's
   recursion; the matrices computed at run time from the dB value and the ICC
   value, both eq. 7 and eq. 8, procedure B by a numerical 2×2
   eigendecomposition. Decoder against reference: within one float32 rounding
   plus 1e-6 of the peak, as SBR.
   ⚠ The reference **shares every normative table** with the decoder: the
   band maps and re-mappings, the 26 prototype taps, the Huffman pairs, the
   ICC values, the delays and decay constants. It is independent in reading and
   arithmetic only. An error in one of those tables, whether a wrong value,
   a wrong sign or a wrong map entry, is invisible to it. Only the absolute
   bound against ffmpeg's output (item 3) and the conformance streams
   (item 4) can catch it.
3. **ffmpeg output**, both builds already used for SBR (macOS clang `-cpuflags 0`,
   linux gcc 14 `--disable-asm`, `testdata/aac/ref*/build-info.txt`):
   - `he44-ps.aac` (committed, Apple encoder): add `ref/he44-ps.f32` and
     `ref-linux/he44-ps.f32`.
   - **Written streams**: extend the SBR test writer (`aac_sbr_writer_test.go`)
     with a PS writer and choose parameters that isolate one stage in the PCM:
     IID 0 / ICC 1 gives L = R = the SBR mono output (hybrid round trip and the
     look-ahead timing); IID 0 / ICC 0 makes L − R proportional to the
     decorrelated signal (decorrelator and transient gain); IID ±max isolates
     the gains; then 10/34 bands, frame class 1 with free borders, `icc_mode ≥ 3`,
     IPD/OPD, a 20↔34 switch, a corrupt payload. These are synthetic, so ffmpeg's
     outputs on them are committed too. This puts ffmpeg's arithmetic behind the
     configurations no encoder in the library emits.
   - Bound: first measure the two builds' disagreement on these streams
     (tolerance rule 2 of aac-design.md), then assert every channel sample by
     sample at **3e-6** absolute (the SBR bound). Expected sources of difference:
     ffmpeg's float32 and its 8-digit arc cosines (≈1e-7 relative in α).
4. **ISO/IEC 14496-26 PS streams** (`al_sbr_ps_*`, names to be taken from the
   md5 list) via `tools/aacconf` (today it pins AAC-LC only): pins added,
   fetched at test time, hashes only; the provisional 1-LSB 16-bit bound until G3.
   These decide the unknowns above.
5. **Real files**: `TestAACRealHE` compares the 801 PS files with ffmpeg sample
   by sample at 3e-6 (numbers and hashes logged, never names) and logs a census
   of the PS configurations met (IID/ICC modes, frame classes, 34-band,
   extension and IPD/OPD use, implicit or explicit signalling), which says which
   paths the library actually exercises.

Integer stages (the parse) are exact: indices from the written streams
round-trip through the writer; `he44-ps.aac`'s indices are checked for range and
consistency (no second integer decoder is available yet).

## Performance

Measured as for SBR: `utime` of our decoder against `ffmpeg -threads 1` on the
same file, load below 8, interleaved rounds; slower is an open defect (decode#47
already records SBR at 1.5× ffmpeg's whole HE-AAC decode).

Cost per frame at 20 bands, ignoring the core: hybrid analysis 3 rows × 32 slots
(12 complex outputs of 13 taps, halved by symmetry), the decorrelator
30 bands × 32 slots × 3 links, transient detection 20 × 32, mixing
71 × 32 two-by-two products, hybrid synthesis sums. The **second 64-band QMF
synthesis** costs more than all of PS together and is already on go-fft's
DCT-IV; PS mostly doubles the synthesis.

| Hot spot | Plan |
|----------|------|
| Second QMF synthesis | existing `sbrSynthesis` (go-fft DCT-IV/DST-IV); any gain there serves SBR stereo too |
| Decorrelator | the recursion is serial in time but independent across bands: keep the state as separate real/imaginary planes `[link][band]` so each slot is a vector operation over 30/50 bands (complex rotation by a per-band constant, `a·x + y` with per-band real coefficients). These are generic kernels: look for them in go-simd/floats (`fleetverbs --match`), add them there if missing — one implementation in the lowest shared library |
| Mixing | per band, a 2×2 real matrix (4×2 with IPD/OPD) applied to complex pairs with a linear ramp across slots: vector over slots in planar layout; a generic "interpolated 2×2 mix" kernel is a go-simd candidate |
| Hybrid analysis | a 13-tap symmetric fold then an M-point odd-frequency DFT; for M ≤ 12 the direct folded form is expected to beat a transform; go-fft only if the measurement shows it |
| Transient detection | elementwise over 20/34 parameter bands per slot: vector over bands |

No allocation per frame (state sized at construction; `testing.AllocsPerRun`
asserts 0).

## Security

Untrusted input: PS bits are bounded by the SBR extension size; the parser
never reads past it (ffmpeg's accounting, refused otherwise, PS off until the
next header, the frame still decodes). Indices out of range after delta
decoding (|IID| > 7/15, ICC > 7), non-monotone borders, reserved modes → PS
off, not a panic. Huffman codes are up to 20 bits long. State is fixed-size
(≈ 91 × 46 + 50 × 3 × 37 complex values per decoder), nothing grows with the
input. `FuzzAACHE` gets PS payloads in its committed corpus; the written streams
seed it with every configuration. Concurrency: one `psState` per decoder,
covered by `TestAACConcurrentDecoders` under `-race`.

## PR split (each green, reviewed independently, before the next)

1. **PS1: bitstream and tables.** `aac_ps.go` (parse, state, fix-up envelope,
   error path), `aac_ps_tables.go` (Huffman pairs, band maps and re-mappings,
   dB steps, ICC values — declared `normative-values`), generic Huffman
   builder; the PS writer; parse tests, fuzz corpus, the real-file census
   (decision 3). It also records the channel-count policy (decision 1). The
   decoder's output is unchanged.
2. **PS2: hybrid banks and the hook behind a test switch.** Analysis and
   synthesis for both layouts with derived modulation and taps, the look-ahead
   from `d.low`, the identity mixing; property tests, the reference eq. 11, and
   the IID 0 / ICC 1 written streams against both ffmpeg builds at 3e-6 —
   this proves the timing before anything else depends on it.
3. **PS3: decorrelator, transients, mixing.** Procedures A and B, IPD/OPD,
   interpolation, 20↔34 switching; the impulse-response reference; the
   stage-isolating written streams and `he44-ps.aac` against both ffmpeg builds.
4. **PS4: on by default, conformance, real files, speed.** Requires the fix of
   decode#52's core-rate edit list (decision 2). It applies the channel count
   policy chosen in PS1, `al_sbr_ps_*` pins in `tools/aacconf`, the 801-file
   comparison, the speed table against ffmpeg; README, landing and docs site.

Each PR extends the read log of aac-design.md (file:lines) and `.provenance.yml`,
runs `copycheck` (0 findings), and lists in its body which references were
consulted.

## Risks

The three listed first under "Decisions and blockers" (channel count, the
core-rate edit list of decode#52, the census) come before these.

- **Shared normative tables.** The reference cannot see an error in a table
  it shares with the decoder (oracle item 2). Only ffmpeg's output and the
  conformance streams can.
- **Paraphrase.** The decorrelator recursion and the re-mapping functions are
  where a reviewer is most likely to find ffmpeg's shape (the grid and chirp
  precedent; copycheck missed both, fleettools#265). Mitigation: the
  decorrelator is written from its transfer function, the re-mappings and band
  maps are data, and the reference is structurally different by construction
  (convolution, not recursion). Review checks the shape, not only the gate.
- **Timing.** A wrong look-ahead or an extra delay is a large error, not a small
  one; PS2 settles it first with the ICC = 1 stream.
- **Frames without PS data after the first header.** ffmpeg keeps applying the
  last parameters (and their borders); whether the standard does is unknown
  until the conformance streams say.
- **Configurations not in the library.** If the census shows the Apple-encoded
  files use one configuration, 34 bands, procedure B and IPD/OPD rest on the
  written streams (ffmpeg's reading) and the conformance streams only.
- **ffmpeg is not baseline PS** (`PS_BASELINE 0`): it applies IPD/OPD. We follow
  ffmpeg, the oracle; whether the HE-AAC v2 profile requires only baseline PS
  is not verified here.
- **Speed.** PS adds a second synthesis to a path already 1.5× slower than
  ffmpeg (decode#47); the PS table is reported on its own so the two are not
  confused.
- **Unknowns above** (IPD/OPD sign rule, eq. 7 vs 8, procedure-B conventions)
  are measured on conformance streams, never fitted to ffmpeg's output.

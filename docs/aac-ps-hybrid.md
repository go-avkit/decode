# Parametric Stereo hybrid filter bank: specification

Status: clean-room specification for PS2 (hybrid analysis and synthesis), written
2026-11-13. Refs go-avkit/avkit#89. It states what is computed as prose, formulas
and tables of values. It contains no code and does not describe any
implementation's loops, variable names or order of operations. It continues
`docs/aac-ps-design.md` (stage 1 and stage 6 of its pipeline) and follows the style of
`docs/aac-ps-syntax.md`, both on the `ps1` branch (decode#60).

Scope. Section 3 covers the **20-band layout**, which is the only one the 801 PS files
of the library use: IID modes 1 and 4, ICC mode 1, so mixing procedure A. The
10-band IID/ICC modes (0 and 3) use the same hybrid layout. Section 4 covers the
**34-band layout**, which can follow later. Mixing, decorrelation and transient
handling are PS3's. They appear here only where an observation needs them.

## 1. Notation and the QMF domain

- `x_k[l]` is the complex QMF subband sample of band `k` (0 ≤ k < 64) at time slot `l`.
  This is the 64-band signal that the SBR tool hands to the 64-band synthesis: the low
  band below `kx` and the adjusted high band from `kx` up to `kx + M`. Slots `0..31`
  belong to the current frame.
- Frequencies are given in **QMF band units**: `f = 1` is the width of one band of the
  64-band synthesis bank, that is `fs_out / 128`. Band `k` covers `[k, k+1)`.
- **Subband frequency convention (to be verified, test T2).** decode's analysis kernel
  (`aac_sbr_qmf.go:21-23`) passes the input frequency `(k+½)·π/32` rad per core-rate
  sample in band `k`, and decimates by 32. A component at band-unit frequency `f` that
  appears in band `k` therefore has subband angular frequency
  `ω ≡ π·f (mod 2π)`.
  The consequence is that the centre of band `k` lies at `ω = +π/2` when `k` is even
  and at `ω = −π/2` when `k` is odd. On the band's own interval `[k, k+1)`, frequency
  increases with `ω` in both cases. The band's transition regions, `[k−½, k)` and
  `[k+1, k+3/2)`, fold onto the remaining half of the circle.
  Everything below about which output of a split band is "lower" follows from this
  convention.

## 2. The hybrid analysis (all layouts)

### 2.1 Defining formula

A split QMF band `k` with `M` sub-bands produces, for each modulation index
`m = 0..M−1`,

  `y_{k,m}[l] = Σ_{τ=−6}^{6} g[τ] · e^{−j·2π(m+½)τ/M} · x_k[l+τ]`

where `g` is a real prototype of 13 taps that is symmetric (`g[−τ] = g[τ]`).

This is Purnhagen DAFx'04 eq. 11 (filter length Λ = 13, phase centred at
`(Λ−1)/2 = 6`), advanced by 6 slots. Eq. 11 is causal, so its output at slot `n`
equals `y` at slot `n − 6`. The advance makes the split rows line up with the unsplit
bands without delaying them. That is eq. 13's compensation moved to the other side;
see section 2.4.

For an input `x_k[l] = e^{jωl}`, the output is `e^{jωl}·G(ω − ω_m)`, where
`G(θ) = Σ g[τ]e^{jθτ}` is the prototype's (real, even) frequency response and
`ω_m = 2π(m+½)/M`. Output `m` passes the subband frequency `ω_m`.

### 2.2 Combining outputs into real filters

When two outputs `m` and `m'` have `ω_{m'} ≡ −ω_m (mod 2π)`, that is `m + m' = M − 1`,
their sum is the **real** filter `2·g[τ]·cos(2π(m+½)τ/M)`. That filter passes `±ω_m`
alike. P1 (Fig. 4) uses such sums to reduce complexity. A two-band real split of band
`k` consists of:

- **low**: `h_low[τ] = g₂[τ]`, which passes `|ω| < π/2`;
- **high**: `h_high[τ] = (−1)^τ · g₂[τ]`, which passes `π/2 < |ω| ≤ π`.

These are exactly P1's `M = 4` bank with its outputs summed in the pairs `(0, 3)` (low)
and `(1, 2)` (high). Here `g₂[τ] = 2·g₄[τ]·cos(πτ/4)` for odd `τ`, `g₂[0] = 2·g₄[0]`, and
`g₂` is zero at even `τ ≠ 0` (because `cos(πτ/4)` vanishes at `τ = ±2, ±6`, and
`g₄[±4] = 0`, section 2.3). The `M = 4` prototype `g₄` that the 20-band layout implies
cannot be recovered at its even taps `±2, ±6`. Only `g₂` matters, and only `g₂` is
specified.

### 2.3 Perfect reconstruction, and what it fixes in the prototypes

The hybrid synthesis (section 2.5) sums all the outputs of a split band. The analysis
followed by the synthesis is the identity exactly when the sum of the filters is a unit
impulse:

  `Σ_m g[τ]·e^{−j2π(m+½)τ/M} = M·e^{−jπτ/M}·g[τ]` when `τ ≡ 0 (mod M)`, and 0 otherwise.

The identity therefore holds exactly, **for any values of the other taps**, when:

- the **centre tap** is `g[0] = 1/M`;
- `g[τ] = 0` for every `τ ≠ 0` in `[−6, 6]` that is a multiple of `M`. For `M = 4` that
  means `τ = ±4`. For the two-band real split (`h_low + h_high`) it means `τ = ±2, ±4, ±6`
  with `g₂[0] = ½`. For `M = 8` and `M = 12` no such tap exists.

These values are **derived**. The remaining taps are **normative values** (table 2.3).
They shape the pass and stop bands, and reconstruction does not constrain them.
⚠ A consequence for testing is that the identity cannot see an error in any
non-derived tap (section 5).

**Table 2.3: prototypes, `g[τ]` for τ = 0..6** (`g[−τ] = g[τ]`). The values were read
in ffmpeg n9.0.2 and FAAD2 `ps_dec.c`, and both hold the same digits. Standard table
numbers are not verified. **kind: normative-values**, except the entries marked *d*,
which are derived in 2.3.

| τ | 20-band, M = 8 (QMF 0) | 20-band, 2-band real `g₂` (QMF 1, 2) | 34-band, M = 12 (QMF 0) | 34-band, M = 8 (QMF 1) | 34-band, M = 4 (QMF 2, 3, 4) |
|---|---|---|---|---|---|
| 0 | 0.125 *d* | 0.5 *d* | 0.08333333333333 *d* | 0.125 *d* | 0.25 *d* |
| 1 | 0.11793710567217 | 0.30596630545168 | 0.08100347892914 | 0.12222452249753 | 0.23279856662996 |
| 2 | 0.09885108575264 | 0 *d* | 0.07428313801106 | 0.10307344158036 | 0.16486303567403 |
| 3 | 0.07266113929591 | −0.07293139167538 | 0.06399831151592 | 0.08417044116767 | 0.07778723915851 |
| 4 | 0.04546865930473 | 0 *d* | 0.05144908135699 | 0.05417891378782 | 0 *d* |
| 5 | 0.02270420949825 | 0.01899487526049 | 0.03812810994926 | 0.03752716391991 | −0.04871498374946 |
| 6 | 0.00746082949812 | 0 *d* | 0.04081179924692 | 0.01565675600122 | −0.05908211155639 |

The derived centre taps hold to all 14 printed digits (`1/12 = 0.08333333333333`).
`.provenance.yml` must declare the 26 normative taps. It must state that the centre taps
and zero taps are computed from section 2.3 and that a test checks the computed values
against the digits above.

### 2.4 Time alignment: look-ahead, history, no added delay

Output slot `l` needs `x_k[l−6 .. l+6]`.

- **Slots `l+τ < 0`** come from the previous frame: its slots `26..31` of the same QMF
  band, in that frame's 64-band signal, not from the hybrid rows. The history is
  therefore of QMF bands, so one history of QMF bands `0..4` serves both layouts, and
  a 20/34 switch needs no reset. At decoder start the history is zero.
- **Slots `l+τ ≥ 32`** are the 6 slots that follow the frame. For a band below the SBR
  crossover `kx`, these are the 6 most recent low-band analysis slots. They already
  exist because the SBR output lags the analysis by 6 slots: the HF generator's 8-slot
  margin minus the 2-slot adjustment offset. In decode they are the low-band samples
  that follow the 32 already assembled (aac-ps-design.md, "look-ahead"). For a split
  band at or above `kx`, nothing is available, and the look-ahead is **zero**, as in
  ffmpeg. This happens only with a degenerate SBR header.
- **Unsplit bands** (QMF 3..63 at 20 bands, 5..63 at 34 bands) pass through
  **unchanged and undelayed**. In P1's causal form (eq. 13) they would be delayed by 6
  slots instead. The two forms differ by a global delay of 6 slots (384 output
  samples). The centred form adds no latency to the decoder, and its PCM aligns with
  ffmpeg's. Whether the standard's decoder adds that latency is open question Q1.
- **Frames where PS is not applied** (before the first header, or after a parse fault)
  do not update the history, as in ffmpeg (aac-ps-design.md, where its frames copy the
  mono signal). **Unverified** (Q3). The first applied frame after such a gap filters
  its first 6 slots against stale or zero history.

### 2.5 Hybrid synthesis (all layouts)

For each output channel, QMF band `k` of the 64-band signal is the **sum of all hybrid
rows that came from band `k`**. A summed pair (2.2) counts as one row. An unsplit band's
row is copied. No filter, gain or delay is applied. With section 2.3 the cascade of
analysis and synthesis is the identity on every band, both layouts, at every slot,
including the frame edges, because the history and look-ahead are those of the same
signal.

## 3. 20-band layout (71 hybrid rows)

Split: QMF 0 with `M = 8` (outputs 2+5 and 3+4 summed, P1 Fig. 4); QMF 1 and 2 with the
two-band real split; QMF 3..63 unsplit. 6 + 2 + 2 + 61 = 71 rows.

**Row order: increasing centre frequency** (signed, so the two image rows of band 0
come first). The parameter band map of PS1 (`k_to_i` at 20 bands, normative values,
ffmpeg's comment cites Table 8.48, not verified) is indexed in this order.

**Centre frequency rule (derived).** Output `m` of a band split in `M` has
`f ≡ 2(m+½)/M (mod 2)`. Of the values congruent to it, the one taken is the one in
`[k−½, k+3/2)`, band `k`'s own interval widened by its two half-band transitions. A
summed pair or a real filter takes the member that lies in `[k, k+1)`. An unsplit band
has `f = k + ½`. Note: `aac-ps-design.md` (table-by-table provenance) writes this as
`k + 2(m+½)/M_k`. The `+k` is a slip. It gives the right set of 10 values but puts
the low and high outputs of QMF 1 in swapped rows.

**Table 3: the 20-band rows.** The output description and `f` are derived. The
parameter band is a normative value already declared in PS1.

| Row | QMF band | Output (2.1 / 2.2) | Passes `ω` | Centre `f` | Parameter band |
|---|---|---|---|---|---|
| 0 | 0 | m = 6 of 8 | −3π/8 | −3/8 | 1 |
| 1 | 0 | m = 7 of 8 | −π/8 | −1/8 | 0 |
| 2 | 0 | m = 0 of 8 | π/8 | 1/8 | 0 |
| 3 | 0 | m = 1 of 8 | 3π/8 | 3/8 | 1 |
| 4 | 0 | m = 2 + m = 5, real | ±5π/8 | 5/8 | 2 |
| 5 | 0 | m = 3 + m = 4, real | ±7π/8 | 7/8 | 3 |
| 6 | 1 | high, real | ±(π/2..π) | 5/4 | 4 |
| 7 | 1 | low, real | ±(0..π/2) | 7/4 | 5 |
| 8 | 2 | low, real | ±(0..π/2) | 9/4 | 6 |
| 9 | 2 | high, real | ±(π/2..π) | 11/4 | 7 |
| 10..70 | row − 7 | unsplit | — | row − 6.5 | below |

Rows 0 and 1 hold **negative frequencies**, the images of band 0's lower transition.
Each maps to the parameter band of its mirror (`|f|`: 3/8 → 1, 1/8 → 0). For band 1,
which is odd, the **low** real filter gives the **upper** half, because band 1's centre
is at `ω = −π/2` (section 1).

Parameter bands of the unsplit rows by QMF band: 3→8, 4→9, 5→10, 6→11, 7→12, 8→13,
9–10→14, 11–13→15, 14–17→16, 18–22→17, 23–34→18, 35–63→19.

A property that a test can check without any reference: the parameter band is a
**non-decreasing function of `|f|`** over all 71 rows.

Synthesis at 20 bands: QMF 0 is the sum of rows 0–5, QMF 1 of rows 6–7, QMF 2 of rows
8–9, and QMF `k ≥ 3` is row `k + 7`.

## 4. 34-band layout (91 hybrid rows), for a later PR

Split (P1 §3.2, "M₀ = 12, M₁ = 8, M_k = 4 for k = 2, 3, 4"): QMF 0 with `M = 12`, QMF 1
with `M = 8`, QMF 2, 3, 4 with `M = 4` each, **no outputs summed**. QMF 5..63 unsplit.
12 + 8 + 3·4 + 59 = 91 rows. The prototypes are the last three columns of table 2.3.
The `M = 4` prototype has its `τ = ±4` zero (derived, 2.3). One `M = 4` prototype serves
all three bands.

**Row order: by QMF band, then by modulation index `m`.** This is **not** frequency
order. The PS1 map at 34 bands (normative values, ffmpeg's comment cites Table 8.49, not
verified) is indexed in this order. Q6 discusses the risk.

**Table 4: centre frequencies (in 1/24 band units) and parameter bands** (rule of
section 3, derived; parameter band normative).

| QMF | Rows | `f`·24 for m = 0, 1, … | Parameter bands |
|---|---|---|---|
| 0 | 0–11 | 2, 6, 10, 14, 18, 22, 26, 30, 34, −10, −6, −2 | 0, 1, 2, 3, 4, 5, 6, 6, 7, 2, 1, 0 |
| 1 | 12–19 | 51, 57, 15, 21, 27, 33, 39, 45 | 10, 10, 4, 5, 6, 7, 8, 9 |
| 2 | 20–23 | 54, 66, 78, 42 | 10, 11, 12, 9 |
| 3 | 24–27 | 102, 66, 78, 90 | 14, 11, 12, 13 |
| 4 | 28–31 | 102, 114, 126, 90 | 14, 15, 16, 13 |
| `k` = 5..63 | `k + 27` | 24·(k + ½) | below |

Parameter bands of the unsplit rows by QMF band: 5→16, 6→17, 7→18, 8→19, 9→20, 10→21,
11–12→22, 13–14→23, 15–16→24, 17–18→25, 19–20→26, 21–23→27, 24–26→28, 27–29→29,
30–32→30, 33–36→31, 37–40→32, 41–63→33.

These 32 centre frequencies agree with the fractional-delay centre frequencies that
ffmpeg holds (aac-ps-design.md). Here they are computed, not held. As at 20 bands, the
parameter band is a non-decreasing function of `|f|`: the three negative rows of QMF 0
map like their mirrors, and the rows that lie in a neighbour's transition (QMF 1
`m = 0, 1` at 17/8 and 19/8, `m = 2` at 5/8; QMF 2 `m = 3`; QMF 3 `m = 0`; QMF 4 `m = 3`)
map to the neighbour's parameter band.

Synthesis at 34 bands: QMF 0 is the sum of rows 0–11, QMF 1 of rows 12–19, QMF 2 of
rows 20–23, QMF 3 of rows 24–27, QMF 4 of rows 28–31, and QMF `k ≥ 5` is row `k + 27`.
A 20↔34 switch between frames changes only which rows exist. The QMF history of 2.4
carries over (decorrelator resets are PS3's).

## 5. Accuracy: oracles and observable signals

The target has two parts. (a) The decoder agrees with an independent float64
reference to within a stated error bound. (b) Each stage is bounded in absolute terms
against ffmpeg's output on a stream chosen so that the stage is visible in the PCM.

**Observable signals.**

| Signal | In decode | In ffmpeg |
|---|---|---|
| hybrid rows `y_{k,m}[l]` (71 / 91 × 32) | test hook on the PS state | not printed; reachable only by instrumenting libavcodec (not proposed) |
| 64-band QMF blocks of L and R after hybrid synthesis | test hook | not printed |
| PCM L, R (and L+R, L−R) | output | output: the only observable |

The QMF synthesis is linear and is already bounded against ffmpeg at 3e-6 (SBR).
Because of that, a hybrid-stage discrepancy shows in the PCM with no extra
uncertainty of its own. Each stage is therefore compared through a stream that isolates
it.

**T1 Identity (no reference).** Analysis followed by synthesis returns the 64-band
input in both layouts, for random complex input, over frame edges, with history and
look-ahead. Bound: 4·2⁻⁵³ times the sum over the 13 taps of `|g|` times the window's
peak, per sample (roughly 1e-15 of the peak). This test sees the timing and the derived
taps. It **cannot** see the normative taps (2.3).

**T2 Frequency placement (no reference).** (a) A complex exponential at `ω = π·f_r` in
QMF band `k` lands with gain `G(0) ± 1e-12` in row `r` of tables 3/4 and with less
gain in every other row of that band. This checks the row order and the sign of the
modulation. (b) A real sinusoid at band-unit frequency `f` fed through decode's
analysis bank reaches the row whose centre frequency is nearest `f`. This checks the
convention of section 1.

**T3 Independent reference (exact up to the declared taps).** For each of the `M`
outputs, the reference evaluates eq. 2.1 as a direct 13-term sum. It does so in
`math/big` floats at 128 bits, with `e^{−jπp/M}` for the integer `p = (2m+1)τ mod 2M`
computed at that precision. Then it applies 2.2's sums and the layout of tables 3/4.
It shares with the decoder only the 26 normative taps and the two parameter maps.
Bound for the float64 decoder: `|y_dec − y_ref| ≤ 2⁻⁴⁸ · Σ|g| · max|x|` over the
window. This is asserted per sample, not as an average.

**T4 Against ffmpeg (PCM, both builds already used for SBR).** Written streams (the PS
writer of PS1):

| Stream | Parameters | What it isolates |
|---|---|---|
| S1 | 20 bands, IID index 0, ICC index 0 (ICC = 1) | With ICC = 1 the mixing angle is 0, so the decorrelated signal has weight exactly 0, and with IID 0 the gains are 1. L = R = the mono SBR output passed through the bank. This checks the **timing**: look-ahead, history and frame edges. A 1-slot error is a large error. It cannot see the taps. |
| S2 | 20 bands, ICC = 1, IID alternating between the extreme indices from one parameter band to the next | L and R weight each row by a different gain, so the identity no longer hides the taps. This checks the **normative taps, row order, summed pairs and the map**. It needs only the IID gains of procedure A at angle 0 (PS1's derived gains), not the decorrelator. |
| S3, S4 | as S1 and S2 at 34 bands | the 34-band layout (section 4) |
| S5 | S2 then S4 then S2 (layout switches) | QMF history carried across a switch |
| S6 | S1 with a corrupt payload mid-stream, then a header | stale history after PS off (Q3) |
| real | `he44-ps.aac` and the 801 files (PS4) | everything, through PS3's stages |

Bound: first measure the disagreement between the two ffmpeg builds on S1–S6, then
assert **3e-6 absolute** per PCM sample (the SBR bound). Expected budget: ffmpeg's
float32 13-tap analysis contributes about `13·2⁻²⁴` (≈ 8e-7) relative to the window's
magnitude per row. The float32 synthesis and output rounding take the rest.

⚠ **Shared tables.** T1–T3 cannot detect an error in a normative tap or map entry,
and T1 cannot detect any tap error. Only S2–S5 against ffmpeg and the ISO conformance
streams can. If ffmpeg and decode share a wrong value (both read from the same
digits), only the conformance streams can.

## 6. Open questions

- **Q1 Latency.** P1 (eq. 13) delays the unsplit bands by 6 slots. ffmpeg instead
  advances the split bands into SBR's look-ahead, which adds no delay. FAAD2 reads its
  hybrid input at an offset of 6 slots, marked "delay" (read through a summary, see the
  read log), so it is not settled whether it is causal. Whether the ISO reference
  decoder's output carries 384 extra samples decides how a conformance stream aligns.
  Decided by the `al_sbr_ps_*` streams.
- **Q2 Split band above `kx`.** The look-ahead is zero (ffmpeg). The exact centred
  filter would need high-band slots that do not yet exist. This only arises with
  degenerate headers. Follow ffmpeg and mark it unverified.
- **Q3 History across frames without PS.** This specification follows ffmpeg: the
  history is not updated while PS is off. The standard's behaviour is unknown, and S6
  measures the effect.
- **Q4 No formula for the 26 taps.** At M = 8 (20-band), the taps divided by the
  ideal low-pass `sin(πτ/8)/(πτ)` fall smoothly from 0.968 at τ = 1 to 0.199 at τ = 6,
  so a windowed design is likely, but no window was identified (nor by
  aac-ps-design.md). The M = 12 prototype rises again at τ = 6 (0.0408 > 0.0381), which
  no ordinary window does, so it was probably optimised numerically. The taps are kept
  as normative values, and the standard's table numbers are unverified.
- **Q5 The `M = 4` bank of P1 at 20 bands.** P1 says `M₁ = M₂ = 4` with summed pairs.
  The two-band real filter is consistent with that (2.2), but the even taps of the
  implied `M = 4` prototype are not determined. This is harmless, since only `g₂` acts.
- **Q6 Row order at 34 bands.** The order by modulation index comes from the reference
  map's indexing. The standard's own order is unverified. The order and the map are a
  consistent pair: if either changes, both must. The property tests (non-decreasing in
  `|f|`, T2) catch an inconsistent pair but cannot catch a consistent pair that is
  wrong.
- **Q7 Correction to aac-ps-design.md.** The centre frequency formula `k + 2(m+½)/M_k`
  should read `f ≡ 2(m+½)/M (mod 2)` in `[k−½, k+3/2)` (section 3). That change belongs
  in a PR to that note.
- **Q8 960-sample frames** (30 slots) are out of scope. The library has none.

## 7. Read log

Everything opened for this specification. Copyleft code was read to learn what is
computed; nothing was copied, and no structure is reproduced above.

| Source | Lines / pages | What it gave |
|---|---|---|
| H. Purnhagen, DAFx'04, <https://dafx.de/paper-archive/2004/P_163.PDF> | pp. 165–166: §3.1 eq. 9–10, §3.2 eq. 11–13, Fig. 4, §4.1 | the hybrid analysis formula, synthesis by summation, delay compensation, M values for both layouts, summed outputs at 20 bands |
| FFmpeg n9.0.2 `libavcodec/aacps.c` (LGPL) | 30–198, 419–424, 715–743 | 2-band real prototype; 20/34 split and which outputs are summed; synthesis sums; history kept for 5 QMF bands and never reset; band counts |
| FFmpeg `libavcodec/aacps_tablegen.h` | 30–217 | four complex prototypes and their modulation; centre frequencies held for the fractional delays (used as a check of tables 3/4) |
| FFmpeg `libavcodec/aacpsdsp_template.c` | 25–119 | the analysis kernel's arithmetic (confirms the sign of the modulation and the centring); copy-through of unsplit bands |
| FFmpeg `libavcodec/aacpsdata.c` | 99–113 | the 71- and 91-entry parameter band maps |
| FFmpeg `libavcodec/aacps.h`, `aacps_common.c` | search hits only: `aacps.h:68,74`; `aacps_common.c:193,202,261,268-269,284-287` | the history size; that the parser never resets it |
| FFmpeg `libavcodec/aacsbr_template.c` | 1526–1560, 1750–1767 | the 38-slot input with a 6-slot low-band look-ahead; PS applied only once started, otherwise the mono signal copied |
| FAAD2 `libfaad/ps_dec.c` (GPL), <https://raw.githubusercontent.com/knik0/faad2/master/libfaad/ps_dec.c> | read through a summarising fetch, not line by line (hybrid analysis about 514–589, synthesis about 591–615, prototype tables) | the same 5 prototypes to all digits; 12-slot history with input read at a 6-slot offset; pairs grouped at 20 bands only; synthesis by plain sums |
| decode `aac_sbr_qmf.go` (`ps1` worktree) | 1–80 | the analysis kernel, which fixes the subband frequency convention (section 1) |
| decode `docs/aac-ps-design.md`, `docs/aac-ps-syntax.md`, `.provenance.yml` (`origin/ps1`) | whole notes; provenance by search | pipeline, look-ahead decision, prototype provenance, the declared maps |

Not opened: the ISO/IEC 14496-3 text (table numbers above are as cited by ffmpeg's
comments, unverified), Schuijers et al. AES 2004, PacketVideo's PS sources.

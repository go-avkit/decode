# Parametric Stereo decorrelation and mixing: specification

Status: clean-room specification for PS3 (decorrelator, transient attenuation, stereo
mixing), written 2026-11-13. Refs go-avkit/avkit#89. It states what is computed as
prose, formulas and tables of values. It contains no code and does not describe any
implementation's loops, variable names or order of operations. Where a quantity is
defined by a recursion in time, the recursion is stated as an equation between values
at consecutive slots. That is the definition, not an evaluation order. It continues
`docs/aac-ps-design.md` (stages 2 to 6 of its pipeline), uses the hybrid rows of
`docs/aac-ps-hybrid.md` (PS2) and the parameters of `docs/aac-ps-syntax.md` (PS1), and
follows their style.

Scope. Sections 2 to 5 cover everything the 801 PS files of the library use: the
**20-band layout**, **mixing procedure A** (ICC mode 1), IID in both resolutions,
fixed-class envelopes, no IPD/OPD (PS1 census). The decorrelator, the transient
attenuation, the parameter remaps and the interpolation are specified for both
layouts, because they do not depend on the mixing procedure. Section 6 covers
**procedure B and IPD/OPD**, which no library file uses and which can be built in a
later PR.

Decisions of the main session that apply here: the decoder adds **no latency** (PCM
aligned with ffmpeg's), and the PS output is **stereo** under the PS1 channel policy.

## 1. Notation

- `s_k[n]` is the mono signal in hybrid row `k` at slot `n`. It is the output of the PS2
  hybrid analysis, so it is centred and undelayed. Rows are numbered as in PS2: 71 rows
  at 20 bands (table 3 of aac-ps-hybrid.md), 91 rows at 34 bands (its table 4). Slots
  `0..31` are the current frame. Negative slots are earlier frames, in the same row.
- `q(k)` is the QMF band that row `k` comes from. `f_k` is the row's **centre
  frequency** in QMF band units (PS2 section 3: derived, signed, negative for the image
  rows of QMF 0).
- `b(k)` is the **parameter band** of row `k`, given by the PS1 map (20 or 34 entries,
  normative values, declared in `.provenance.yml`).
- `d_k[n]` is the decorrelated signal, and `l_k[n]` and `r_k[n]` are the left and right
  hybrid outputs.
- `kx` and `M` are the SBR crossover band and the number of SBR bands of the frame. The
  **SBR top** is `t = kx + M`, a QMF band (`t ≤ 64`).
- A frame is **applied** when PS processing runs on it, which happens from the first
  header that parses correctly until a parse fault (PS1 section 9; "PS usable"). In a
  frame that is not applied the output is `L = R =` the mono signal, and **none** of
  the state of this document changes (ffmpeg; Q3 of PS2, Q9 below).
- Envelope borders `b_0 < … ≤ b_E` are the slot numbers of PS1 section 4, after the
  closing envelope of PS1 section 6. `b_0 = −1` and `b_E = 31`. `E` is the envelope
  count (1 to 5).

## 2. Signal flow and timing

For each applied frame, for each row `k` and slot `n`:

1. `d_k[n]` is computed from the current and past values of `s_k` only (section 3), and
   it is scaled by a transient gain that depends on the current and past slots of all
   rows (section 3.5).
2. `l_k[n] = g_Ls · s_k[n] + g_Ld · d_k[n]` and `r_k[n] = g_Rs · s_k[n] + g_Rd · d_k[n]`.
   The four real gains belong to parameter band `b(k)` at slot `n` (section 4). With
   IPD/OPD the gains become complex (section 6.2).
3. The `l` rows go through the PS2 hybrid synthesis into a 64-band QMF block, which the
   **channel 0** synthesis bank turns into the **left** PCM. The `r` rows go the same
   way through the **channel 1** bank (the second bank of a channel pair, which already
   exists) into the **right** PCM. No gain or delay is applied between the two
   syntheses.

**Latency.** The hybrid analysis is centred and its look-ahead comes from the SBR low
band (PS2 section 2.4). The decorrelator uses past slots only, and the mixing is local
to each slot. PS therefore adds no delay, and the left and right PCM of frame `j`
align sample for sample with the mono PCM that frame `j` would have produced without
PS. A delay of the decorrelated signal (2, 14 or 1 slots, section 3) is part of its
definition, and does not delay the output.

**Rows at or above the SBR top.** `s_k` is zero there by construction, so `l_k` and
`r_k` are zero once the decorrelator's past has drained. Section 3.6 states when that
past is cleared.

## 3. The decorrelator

### 3.1 Row classes

P1 §4.1 uses all-pass decorrelation "for the first 23 QMF bands" and a plain delay
above. The rows split into three classes by QMF band:

| Class | QMF bands | Rows at 20 bands | Rows at 34 bands | Filter |
|---|---|---|---|---|
| all-pass | 0..22 | 0..29 (30 rows: 10 split + QMF 3..22) | 0..49 (50 rows: 32 split + QMF 5..22) | section 3.2 |
| long delay | 23..34 | 30..41 | 50..61 | `z^−14` |
| short delay | 35..63 | 42..70 | 62..90 | `z^−1` |

The all-pass row counts (30, 50) are **derived** from P1's "first 23 QMF bands". The
boundary at QMF 35 and the two delays 14 and 1 are **normative values**. Both
references hold them as row numbers 42/62 and agree (FAAD2 as QMF band 35). The class
of a row depends only on its QMF band, so it is the same in both layouts.

For the long and short delay classes, the filtered signal is `s_k[n − 14]` or
`s_k[n − 1]`. There is no phase rotation.

### 3.2 All-pass rows: transfer function

For an all-pass row `k`, the filtered signal is `s_k` through

  `A_k(z) = z^−2 · φ_k · Π_{m=0}^{2} (Q_{k,m} · z^−D_m − a_m·γ_k) / (1 − a_m·γ_k · Q_{k,m} · z^−D_m)`

where:

- `z^−2` is a **pre-delay of 2 slots**. Its value is **normative**. Both references
  hold it.
- `φ_k = e^{−jπ·δ₀·f_k}` is a **fractional pre-delay** of `δ₀` slots, applied as a
  phase rotation at the row's centre frequency (P1 §4.1: "rotating the phase … by the
  angle that corresponds to the desired fraction of a sample at the center frequency").
  The formula is **derived**: a QMF slot is 64 output samples, and band-unit frequency
  `f` turns by `π·f` per slot (PS2 section 1), so a delay of `δ` slots is a factor
  `e^{−jπδf}`.
- `Q_{k,m} = e^{−jπ·δ_m·f_k}` is the fractional delay of link `m`. It is derived in the
  same way.
- `D_m = 3, 4, 5` are the link delays in slots. They are **derived**: P1 §4.1, "a chain
  of three delay lines with the length of 3, 4, and 5 QMF samples".
- `a_m` are the link coefficients. `γ_k` is the decay slope (section 3.4).

Since `a_m·γ_k` is real and `|Q_{k,m}| = |φ_k| = 1`, each factor is an all-pass:
`|A_k(e^{jω})| = 1` for every `ω`. That is a property a test can check without any
reference. Each link is causal and stable, with its poles at radius
`(a_m γ_k)^{1/D_m} < 1`. Its impulse response is

  `−a_m γ_k` at lag 0, then `(1 − a_m²γ_k²) · (a_m γ_k)^{i−1} · Q_{k,m}^i` at lag `i·D_m` for
  `i ≥ 1`, and 0 elsewhere.

Its ℓ1 norm is `1 + 2·a_m γ_k`, which is at most 2.303. When `γ_k = 0`, every link is a
pure delay `Q_{k,m} z^−D_m`. The row then reduces to a delay of `2 + 3 + 4 + 5 = 14`
slots with a phase, which is the long-delay class except for the phase. (Section 3.4
shows that `γ_k` never reaches 0 inside the all-pass class, so this is a limit, not a
case.)

**Initial state.** The decorrelator's past is zero at decoder start: `s_k[n] = 0` for
every slot before the first applied frame, and every link's past output is zero.

### 3.3 Constants of the all-pass rows

| Constant | Value | Kind | Basis |
|---|---|---|---|
| pre-delay | 2 slots | normative-values | ffmpeg and FAAD2 agree; table number not verified |
| `δ₀` | 0.39 | normative-values | ffmpeg and FAAD2 (FAAD2 holds the rotations, which give 0.39 back) |
| `δ_0, δ_1, δ_2` (links) | 0.43, 0.75, 0.347 | normative-values | as above |
| `D_m` | 3, 4, 5 | derived | P1 §4.1 |
| `a_m` | 0.65143905753106, 0.56471812200776, 0.48954165955695 | normative-values (PS1, `psLinkDecay`) | see below |
| rotation `e^{−jπδf}` | — | derived | P1 §4.1, PS2 section 1 |

**`a_m` and its formula.** `a_m = e^{−D_m/7}` reproduces the three held values to
4.4e-15 (throwaway check, 2026-11-13). PS1 declared the values normative because no
source gave the formula. FAAD2 `ps_dec.c:112` now gives one in a comment,
"a(m) = exp(-d_48kHz(m)/7)". That is a reference implementation's comment, not the
standard. This specification keeps the values **normative** and records the formula
as agreement (Q4). If the main session accepts a reference comment as the defining
source, the entry becomes derived (`e^{−D_m/7}`, agreement 4.4e-15). That is a
`.provenance.yml` change, not a numerical one.

**Centre frequencies.** `f_k` is the PS2 value (derived). The all-pass rows use:

- 20 bands, rows 0..9: `−3/8, −1/8, 1/8, 3/8, 5/8, 7/8, 5/4, 7/4, 9/4, 11/4` (PS2
  table 3). Rows 10..29: `q + ½` for QMF `q = 3..22`.
- 34 bands, rows 0..31: PS2 table 4 (`f·24` = 2, 6, 10, 14, 18, 22, 26, 30, 34, −10,
  −6, −2, 51, 57, 15, 21, 27, 33, 39, 45, 54, 66, 78, 42, 102, 66, 78, 90, 102, 114,
  126, 90). Rows 32..49: `q + ½` for `q = 5..22`.

Agreement: ffmpeg holds the 10 + 32 centre frequencies as data and the unsplit ones as
`row − 6.5` and `row − 26.5`. All of them equal the PS2 values. FAAD2 holds the
**rotations** instead: tables of `e^{+jπδf}` used through a multiplication by the
conjugate, which gives `e^{−jπδf}`. The angles of its entries give back `f` =
0.5, 1.5 (QMF 0, 1), 1/8, −3/8, 7/4, 5/4, 9/4 (hybrid rows), to the 8 digits those
tables hold (checked 2026-11-13 by a throwaway program). The two references therefore
agree, independently, on the sign of the rotation and on the centre frequencies,
including the negative ones.

### 3.4 Decay slope

`γ_k = 1` for every split row. For an unsplit row of QMF band `q`,

  `γ_k = clip(1 − σ·(q − q₁), 0, 1)`, with `σ = 0.05`,

where `q₁` is the first unsplit QMF band: 3 at 20 bands, 5 at 34 bands. `q₁` is
**derived** from the layout. `σ` is a **normative value**. Within the all-pass class
(`q ≤ 22`) the slope is never 0: its smallest value is `1 − 0.05·19 = 0.05` at 20 bands
and `1 − 0.05·17 = 0.15` at 34 bands. The clip is therefore inactive. Both references
agree: ffmpeg counts from row 10/32, FAAD2 from QMF 3/5.

### 3.5 Transient detection and attenuation

Per parameter band `β` (0..19 or 0..33) and slot `n`:

- **Power** `P_β[n] = Σ_{k : b(k) = β} |s_k[n]|²`, summed over **all** rows of the
  layout (71 or 91) whose parameter band is `β`, at the current slot.
- **Decaying peak** `P̂_β[n] = max(λ·P̂_β[n−1], P_β[n])`.
- **Smoothed power** `Ē_β[n] = Ē_β[n−1] + μ·(P_β[n] − Ē_β[n−1])`.
- **Smoothed peak excess** `D̄_β[n] = D̄_β[n−1] + μ·(P̂_β[n] − P_β[n] − D̄_β[n−1])`. It
  uses the peak of the **same** slot `n`.
- **Gain** `G_β[n] = 1` when `κ·D̄_β[n] ≤ Ē_β[n]`, else `Ē_β[n] / (κ·D̄_β[n])`.

The decorrelated signal of row `k` is the filtered signal of section 3.1/3.2 times
`G_{b(k)}[n]`:

  `d_k[n] = G_{b(k)}[n] · (A_k ∗ s_k)[n]` (all-pass rows), and
  `d_k[n] = G_{b(k)}[n] · s_k[n − 14]` or `G_{b(k)}[n] · s_k[n − 1]` (delay rows).

The gain is that of the **current** slot `n`, not of the slot the delayed sample came
from.

| Constant | Value | Kind |
|---|---|---|
| `λ` (peak decay per slot) | 0.76592833836465 | normative-values (PS1, `psPeakDecay`). Equals `e^{−4/15}` to 1.3e-15. FAAD2 holds a second value for low rates under a build option it does not enable, 0.58664621951003, which equals `e^{−8/15}` to 1.8e-15: twice the decay exponent for slots twice as long. That supports an exponential with a time constant of 5 ms at 48 kHz, but no source states it (Q4) |
| `μ` (smoothing) | 0.25 | normative-values |
| `κ` (transient impact) | 1.5 | normative-values |

**Initial state** `P̂ = Ē = D̄ = 0` for every band at decoder start.

**Properties** that a test can check without any reference:

- `0 < G ≤ 1` whenever `D̄ > 0`. When `Ē = D̄ = 0`, as in digital silence, `G = 1`.
- `G` is **invariant under scaling** of the input by any positive constant, because
  every state is homogeneous of degree 2. decode's QMF scale (16-bit units) and
  ffmpeg's therefore give the same gains, except where float32 underflows.
- `G` is a continuous function of the states. At `κD̄ = Ē` both branches give 1. A
  rounding difference between two implementations therefore changes `G` only a
  little, and never by a jump.
- With stationary input, `P̂ − P` and `D̄` stay small, so `G = 1`. A step up in power
  makes `P̂ = P`, and after it the peak decays faster than the power, so `D̄` grows and
  `G < 1`. This is the "duck after an onset" of P1 §4.2, Fig. 5.

### 3.6 State across frames, resets, clearing

The decorrelator's state is: for each row, its past input (up to 14 slots), each
all-pass link's past output (up to 5 slots), and the three transient values per
parameter band. It carries over from one applied frame to the next.

- **Layout switch.** When an applied frame's layout (20 or 34, PS1 section 3) differs
  from that of the previous **applied** frame, the whole decorrelator state is reset
  to zero before the frame is processed. This includes the transient values and every
  row's past. The matrices are remapped instead (section 4.3). Both references reset
  (ffmpeg, FAAD2 by reinitialising its state per layout).
  Known difference: ffmpeg detects the switch by comparing the layouts of the last two
  **parsed** frames. An applied frame with no PS data, after a parsed switch, therefore
  resets again, and remaps its carried matrix a second time (Q9). This specification
  resets once.
- **Clearing above the SBR top (ffmpeg).** At the start of every applied frame, the
  past of every row whose QMF band is `≥ t` (the frame's SBR top, section 1) is zeroed.
  Since `s_k` is zero there in the current frame, this changes the output only when `t`
  has **decreased** since an earlier frame: it removes what was still ringing or
  waiting in the delay lines of the bands that have just gone silent. FAAD2 does not
  clear. Because this specification aligns with ffmpeg, it clears, and the difference
  is marked unverified (Q10). A stream whose `t` drops (S17) shows the effect.
- **Frames not applied** change nothing (section 1).

## 4. Mixing, procedure A

### 4.1 From indices to the four gains

Per envelope and parameter band, the IID index `i` and the ICC index `c` (PS1, after
the remap of section 4.2) give:

- the IID in dB, `I`, from the dB step table (PS1, normative values) of the IID
  resolution of the last header (coarse for IID modes 0..2, fine for 3..5);
- the ratio `ĉ = 10^{I/20}` (P1 eq. 5, derived; PS1 `psIIDGain*`);
- the coherence `ρ`, from the 8 ICC values (PS1, normative values);
- `c_R = √2 / √(1 + ĉ²)` and `c_L = ĉ · c_R` (P1 eq. 5: `c_l = √2·c/√(1+c²)`,
  `c_r = √2/√(1+c²)`), so that `c_L² + c_R² = 2`;
- `α = ½·arccos ρ` (P1 eq. 5, derived);
- `β = α·(c_R − c_L)/√2` (P1 **eq. 8**, the approximation of eq. 7).

The gains are those of P1 eq. 4–5, `[L; R] = H·[M; D]`, with
`H = [[c_l cos(β+α), c_l sin(β+α)], [c_r cos(β−α), c_r sin(β−α)]]`:

| Gain | Value |
|---|---|
| `g_Ls` (mono to left) | `c_L · cos(β + α)` |
| `g_Ld` (decorrelated to left) | `c_L · sin(β + α)` |
| `g_Rs` (mono to right) | `c_R · cos(β − α)` |
| `g_Rd` (decorrelated to right) | `c_R · sin(β − α)` |

A positive IID index makes the left channel louder (`c_L > c_R`). This is the sign that
both references implement, and that eq. 1 defines (IID = 10 log10 of left over right).

**eq. 7 or eq. 8.** Both references use eq. 8. ffmpeg computes it, and FAAD2's
commented formula and its precomputed `β` tables both do. This specification uses eq. 8.
Whether the standard does is unverified. A conformance stream decides it (the
reference in section 7 evaluates both and reports which one matches). The difference
is zero when `c_L = c_R` and grows with |IID|.

**Anchor values** (derived, IID index 0, so `c_L = c_R = 1` and `β = 0`):

| ICC index | ρ | `α` | `g_Ls = g_Rs` | `g_Ld = −g_Rd` |
|---|---|---|---|---|
| 0 | 1 | 0 | 1 | 0 |
| 1 | 0.937 | 0.17842763716227 | 0.98412397592986 | 0.17748239349299 |
| 2 | 0.84118 | 0.28566733020309 | 0.95947381412939 | 0.28179779984947 |
| 3 | 0.60092 | 0.46307236069097 | 0.89468430186295 | 0.44669900380458 |
| 4 | 0.36764 | 0.59716315213822 | 0.82693409652765 | 0.56229885292431 |
| 5 | 0 | π/4 | √½ | √½ |
| 6 | −0.589 | 1.10030859436137 | 0.45332107826573 | 0.89134729482957 |
| 7 | −1 | π/2 | 0 | 1 |

Consequences that make the test streams of section 7 work:

- ICC index 0 (ρ = 1): `l = c_L·s` and `r = c_R·s`, with no decorrelated signal (PS2's
  S1 and S2).
- IID 0 and ICC index 7: `l = d` and `r = −d` **exactly**. The PCM shows the
  decorrelator and the transient gain alone.
- IID 0 and ICC index 5: `l − r = √2·d` and `l + r = √2·s`.

**Value to ffmpeg.** ffmpeg holds the arc cosines rounded to 8 digits. The largest
error is 8.9e-8 rad, at ρ = −0.589. It computes the gains in float32. This
specification computes the gains from the definitions (PS1 decision), a known, bounded
difference (section 7).

### 4.2 Parameter resolution to layout (10, 20, 34 → 20 or 34)

The layout is 34 bands when IID or ICC is enabled with 34 parameter bands, else 20
bands (PS1 section 3). Each of IID and ICC is carried at its own resolution, 10, 20 or
34 bands, and is mapped to the layout's band count independently, per envelope, before
section 4.1 applies. The maps are applied to **indices**. An average of indices is an
integer division **truncated toward zero**, as both references compute it. Whether
the standard rounds the same way is unverified (Q6). It matters only for an IID or ICC
sum that the divisor does not divide.

**kind: normative-values** for all four maps. ffmpeg's comment cites Table 8.46 for
10 → 20. The other table numbers are unknown and not verified. ffmpeg and FAAD2 hold
the same maps.

**10 → 20**: target band `j` takes source band `⌊j/2⌋` (each source band twice).

**34 → 20** (target ← sources):

| target | 0 | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 |
|---|---|---|---|---|---|---|---|---|---|---|
| from | (2·0 + 1)/3 | (1 + 2·2)/3 | (2·3 + 4)/3 | (4 + 2·5)/3 | (6+7)/2 | (8+9)/2 | 10 | 11 | (12+13)/2 | (14+15)/2 |

| target | 10 | 11 | 12 | 13 | 14 | 15 | 16 | 17 | 18 | 19 |
|---|---|---|---|---|---|---|---|---|---|---|
| from | 16 | 17 | 18 | 19 | (20+21)/2 | (22+23)/2 | (24+25)/2 | (26+27)/2 | (28+29+30+31)/4 | (32+33)/2 |

**20 → 34**:

| target | 0 | 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9 | 10 | 11 | 12 | 13 | 14 | 15 | 16 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| from | 0 | (0+1)/2 | 1 | 2 | (2+3)/2 | 3 | 4 | 4 | 5 | 5 | 6 | 7 | 8 | 8 | 9 | 9 | 10 |

| target | 17 | 18 | 19 | 20 | 21 | 22 | 23 | 24 | 25 | 26 | 27 | 28 | 29 | 30 | 31 | 32 | 33 |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| from | 11 | 12 | 13 | 14 | 14 | 15 | 15 | 16 | 16 | 17 | 17 | 18 | 18 | 18 | 18 | 19 | 19 |

**10 → 34** is **derived**: it is 20 → 34 applied after 10 → 20. Every average in
20 → 34 then combines two equal values, so it is exact. Written out, target bands
0–2 take source 0, 3–5 take 1, 6–9 take 2, 10–11 take 3, 12–15 take 4, 16–17 take 5,
18–19 take 6, 20–23 take 7, 24–27 take 8, and 28–33 take 9. That is ffmpeg's direct
table, entry for entry. A test can check the composition, not just the digits.

When a parameter is **disabled**, its indices are all 0 (PS1 section 5): IID 0 dB, and
ICC index 0, which is ρ = 1. The map leaves zeros as zeros.

### 4.3 Interpolation across envelopes and slots

P1 §3.3: "linear interpolation is applied to the elements h_ij of the matrix H for the
QMF samples located between two stereo parameter updates."

- The gains computed from envelope `e` (`e = 1..E`, in the order of PS1) are the
  matrix `H_e`. Its **parameter position** is the slot `b_e`, the envelope's last slot.
- `H_0` is the matrix in force at the end of the previous applied frame, that is, at
  its slot 31, which is slot `−1 = b_0` of this frame. On a 20 ↔ 34 switch it is first
  remapped by the **value** forms of the maps of 4.2 (same table shape, exact averages
  of the gain values, a 1/3–2/3 weighting where 4.2 has `(2a + b)/3`). For the
  20 → 34 direction these are ffmpeg's value maps. For 10-band sources no value map is
  needed, because `H_0` always has the layout of the previous frame.
- For `b_{e−1} < n ≤ b_e`, each of the four gains is

  `g[n] = g(H_{e−1}) + ((n − b_{e−1}) / (b_e − b_{e−1})) · (g(H_e) − g(H_{e−1}))`.

  At the parameter position (`n = b_e`) the gain equals `H_e`'s exactly.
- An envelope with `b_e = b_{e−1}` (allowed in class 1, PS1 section 4) covers no slot.
  Its matrix is still the starting point of envelope `e + 1`. The gains therefore jump
  from `H_{e−1}` to `H_e` with no slot in between. That is ffmpeg's behaviour. FAAD2
  instead moves equal borders apart (each at least one slot after the previous), which
  changes the slot grid (Q7).
- Interpolation is per gain (matrix element), not per parameter. An interpolated
  matrix is in general not one of the procedure-A family.
- **The very first applied frame** (no earlier matrix). ffmpeg starts from the
  **all-zero** matrix, so `l` and `r` rise linearly from silence over the first
  envelope (`(n + 1)/(b_1 + 1)` of their steady value at IID 0 and ICC 1). FAAD2 starts
  from `g_Ls = g_Rs = 1` and `g_Ld = g_Rd = 0`, the matrix of IID 0 and ICC 1, so there
  is no fade. This specification follows ffmpeg, because the PCM comparison is against
  ffmpeg. That choice is unverified against the standard (Q8). ⚠ The PS2 streams S1 to
  S6 are affected: their first applied frame is faded in ffmpeg, so "L = R = mono"
  holds from the end of the first envelope on, not from slot 0.

Library frames: fixed class only, so the borders are `{−1, 31}`, `{−1, 15, 31}` or
`{−1, 7, 15, 23, 31}`. A frame with no envelope gets the closing envelope `{−1, 31}`,
whose parameters repeat the last ones (PS1 section 6). That frame then interpolates
from `H_0` to a matrix equal to it, so its gains are constant.

### 4.4 From parameter bands to rows

Every row `k` of the layout uses the gains of parameter band `b(k)` (PS1 map; PS2
tables 3 and 4). The same real gain multiplies the real and the imaginary part of the
complex sample. The image rows (negative `f_k`) use the band of their mirror, as the
map gives. For procedure A that needs nothing more. For IPD/OPD the image rows need a
conjugation (section 6.2).

## 5. Output

- `l_k[n]` and `r_k[n]` for all rows and the 32 slots go through the PS2 synthesis
  (section 2.5 of aac-ps-hybrid.md: split rows summed back into their QMF band,
  unsplit rows copied) into two 64-band blocks, then through the existing QMF
  synthesis of channel 0 (left) and channel 1 (right).
- A frame that is not applied outputs the mono 64-band block through both banks.
  Both banks' histories advance in every frame, applied or not, so switching PS on or
  off introduces no gap and no shift.
- A frame that is applied writes nothing to the mono path. The mono hybrid rows `s`
  are consumed, so channel 0's block is `l`, not `s`.

## 6. Procedure B and IPD/OPD (a later PR; no library file uses them)

### 6.1 Procedure B (ICC mode 3..5)

The two mixed channels are given the covariance that the parameters describe: powers
in the ratio `ĉ²`, normalised so that their sum is 2, and correlation `ρ'`. The matrix
is the **principal-axis** decomposition of that 2×2 covariance (derived in
aac-ps-design.md; algebraically identical to ffmpeg's closed form):

- `ρ' = max(ρ, 0.05)`. The floor 0.05 is a **normative value**. A negative ICC
  therefore acts as 0.05.
- `α_B = ½·atan2(2ĉρ', ĉ² − 1)`. Since `2ĉρ' > 0`, `α_B ∈ (0, π/2)`. The references'
  "add π/2 when α < 0" is needed only with a one-argument arctangent (FAAD2), and has
  no effect with `atan2` (ffmpeg). It is not a separate convention.
- `ν = √(1 − 4ĉ²(1 − ρ'²)/(1 + ĉ²)²)` (the eigenvalues of the normalised covariance are
  `1 ± ν`), and `γ_B = arctan √((1 − ν)/(1 + ν))`.
- `g_Ls = √2 cos α_B cos γ_B`, `g_Ld = −√2 sin α_B sin γ_B`, `g_Rs = √2 sin α_B cos γ_B`,
  `g_Rd = √2 cos α_B sin γ_B`: a rotation by `α_B` of `(√2 cos γ_B · s, √2 sin γ_B · d)`.

Property: the output covariance (with `s` and `d` uncorrelated and of equal power)
equals the target exactly. A test can check this without a reference.
⚠ **The references disagree.** FAAD2's procedure-B gains put `−cos α` on `g_Ld` and
`sin α` on `g_Rd` (`ps_dec.c:1619-1622`), with its `cos α`/`sin α` read from tables
indexed in mirror order. Whether its tables compensate for that was not established
(Q11). This specification uses the rotation, which provably reproduces the covariance.
The remaps, the interpolation and the decorrelator are those of procedure A.

### 6.2 IPD/OPD

- **Angles.** An index `p` (0..7) stands for `p·π/4` (derived; PS1 `psPhase`).
- **Bands.** Only the first 11 (20 bands) or 17 (34 bands) parameter bands carry
  phases. The others keep real gains. Remap of the phase indices to the layout: the
  maps of 4.2 restricted to the phase band counts. 5 → 11 takes the first 10 target
  bands from 10 → 20, and target band 10 is 0. 17 → 11 takes the first 11 targets of
  34 → 20. 11 → 17 takes the first 17 targets of 20 → 34. 5 → 17 takes 10 → 34 for
  targets 0–15, and target 16 is 0. These are ffmpeg's maps (the "not full" forms).
  normative-values, not verified.
- **Smoothing** per phase band, over envelopes (not frames): the smoothed phase of
  envelope `e` is `arg(w₂·e^{jθ[e−2]} + w₁·e^{jθ[e−1]} + w₀·e^{jθ[e]})` with
  `(w₂, w₁, w₀) = (0.25, 0.5, 1)` (normative values). Envelopes are counted across
  frame boundaries. The sum never vanishes, because its modulus is at least
  `1 − 0.75`. The history before the first envelope, and after a 20 ↔ 34 switch, is
  angle 0 (ffmpeg). This is done for IPD and OPD separately, giving `θ_IPD` and
  `θ_OPD`.
- **Application.** `g_Ls` and `g_Ld` are multiplied by `e^{jθ_OPD}`, and `g_Rs` and
  `g_Rd` by `e^{j(θ_OPD − θ_IPD)}`, so the left channel carries the overall phase and
  the right channel lags it by the IPD. Both references agree. The gains are complex,
  and their real and imaginary parts are interpolated separately (4.3).
- **Image rows (derived rule).** A rotation `e^{jθ}` meant for a positive frequency
  acts on the image of that frequency as `e^{−jθ}`, so a row with `f_k < 0` uses the
  **conjugate** gains. Those rows are 0 and 1 at 20 bands, and 9, 10, 11 at 34 bands.
  FAAD2 does exactly this (the same rows, the conjugate applied to the whole
  interpolation). ⚠ ffmpeg differs in two ways. It flips rows 9 to 13 at 34 bands,
  which includes rows 12 and 13 (QMF 1, `f` = 51/24 and 57/24, positive). And it flips
  only the **start** of each envelope's interpolation, not its end, so the imaginary
  part of those rows runs from `−Im H_{e−1}` to `+Im H_e`. ffmpeg's own comment doubts
  the rule ("Is this necessary?"). This specification uses the derived rule. Streams
  with zero phase on the affected parameter bands (S16a) stay comparable with ffmpeg,
  and S16b measures the difference (Q5).
- The phase enable persists as in PS1 section 8. When it is off, the gains are real
  again from the next envelope on, and the imaginary parts are taken as zero at once
  (ffmpeg). Unverified.

## 7. Accuracy: oracles, streams, bounds

The target has two parts. (a) The decoder agrees with an **independent extended
precision reference** within a stated bound per sample. (b) The decoder's PCM agrees
with **ffmpeg's** (both builds already used for SBR) within **3e-6 absolute** per
sample. The bound is set after first measuring how far the two ffmpeg builds disagree
with each other.

### 7.1 Properties (no reference)

| Test | Checks |
|---|---|
| P1 all-pass | for each all-pass row of both layouts and `γ ∈ {1, 0.05, 0.15}`: `|A_k(e^{jω})| = 1 ± 1e-13` on a grid of 1024 `ω`; impulse response of the decoder's recursion equals the series of 3.2 to 1e-14 |
| P2 delay classes | an impulse in a long or short delay row comes out after exactly 14 or 1 slots with its phase unchanged; one in an all-pass row starts at slot +2 with `−a₀a₁a₂·φ_k` (at `γ = 1`) |
| P3 transient | scale invariance (input ×2^k changes no gain by more than 1 ulp); `G = 1` on stationary noise after the start; `G < 1` after a step, rising back toward 1 |
| P4 procedure A | `c_L² + c_R² = 2`; the anchor table of 4.1; output IID and ICC of a long stationary noise frame converge to the transmitted ones (P1 eq. 1–2) within 0.1 dB and 0.01 |
| P5 maps | 10 → 34 equals 20 → 34 after 10 → 20; each map leaves a constant input constant |
| P6 interpolation | the gain at `b_e` equals `H_e` exactly; constant matrices give constant gains; a zero-length envelope skips no matrix |
| P7 procedure B | output covariance equals the target to 1e-12 for all 46 × 8 index pairs |
| P8 timing | an impulse in `s` at slot `n` of frame `j` contributes to `l`, `r` at slot `n` (the `s` path) and from slot `n + 2` on (all-pass), with no other offset |

### 7.2 Independent reference (test only)

It shares with the decoder only the declared normative values: the PS1 tables, the
constants of 3.3 and 3.5, and the maps of 4.2. It is written from this document,
structured differently:

- **Decorrelator**: convolution of `s_k` with the **closed-form impulse response** of
  `A_k`, the product of the three link series of 3.2 multiplied out, truncated where the
  terms fall below `1e-20` of the first (`a^i < 1e-20` needs `i ≈ 108`, so about 540
  slots). This is evaluated in double-double arithmetic, with each rotation
  `e^{−jπδf}` computed from the exact argument. The input history comes from the
  reference's own previous frames.
- **Transient gain**: the recursions of 3.5 in `math/big` at 128 bits, from the
  reference's powers.
- **Matrices**: from the dB value and ρ at run time, both eq. 7 and eq. 8, in
  double-double (`arccos`, `cos`, `sin` to within 2⁻¹⁰⁰). Procedure B by a numerical 2×2
  eigendecomposition.
- **Interpolation**: the closed form of 4.3, in exact rationals times the gains.

Bound, decoder (float64) against the reference, per sample of `d`, `l`, `r`:
`2⁻⁴⁴ · max |s|` over the last 600 slots of the row's band. The reason is ℓ1 gains of
at most `2.303³ ≈ 12.2` through the links, about 30 roundings per slot, and a
transient gain that is Lipschitz wherever `D̄` is not below `2⁻⁹⁰⁰` of the power.
Asserted per sample, never as an average.
⚠ As in PS2, the reference cannot see an error in a table it shares (a wrong map
entry, a wrong `δ`, `a_m`, `λ`). Only ffmpeg's PCM and the conformance streams can,
and only conformance can when ffmpeg shares the value.

### 7.3 Written streams against ffmpeg

These use the PS writer of PS1, over the SBR writer's content. Each stream isolates
one stage in the PCM, as in PS2. S1 to S6 are those of aac-ps-hybrid.md section 5;
note the first-frame fade (4.3).

| Stream | Parameters | Isolates |
|---|---|---|
| S7 | 20 bands, IID 0, ICC index 7, stationary core content | `l = d`, `r = −d`: the all-pass rows, the 14- and 1-slot rows, the fractional delays, `a_m`, the decay slope; `G ≈ 1` |
| S8 | as S7, core with isolated onsets (loud frames after silence) | the transient gain: `λ`, `μ`, `κ`, the power sum over rows by `b(k)` |
| S9 | 20 bands, ICC 1..7 against IID over every index of both resolutions, one combination per parameter band and envelope | the procedure-A gains (all 46 × 8) and eq. 8 |
| S10 | fixed class, 1, 2 and 4 envelopes and 0 (closing), parameters changing every envelope | interpolation, parameter positions, the carried matrix |
| S11 | class 1, free borders including equal borders and a first border at 0 | zero-length envelopes (ffmpeg's jump, Q7) |
| S12 | IID mode 0/3 and ICC mode 0 (10 bands) | 10 → 20 |
| S13 | 34 bands, S7 and S9 content | the 34-band decorrelator rows and centre frequencies; 34-band gains |
| S14 | IID 34 with ICC 20, and IID 10 with ICC 34 | 20 → 34 and 10 → 34 index maps |
| S15 | S13 then S10 content, alternating layouts, ICC < 1 | the reset of 3.6 and the value remap of `H_0`, both directions (extends S5) |
| S16a / S16b | IPD/OPD over all indices, 20 and 34 bands; (a) phases 0 on the parameter bands of the image rows (20: bands 0, 1; 34: bands 0–2 and 10), (b) not | IPD/OPD smoothing and application; (b) measures the image-row rule difference (Q5) and is excluded from the 3e-6 bound |
| S17 | S7 with an SBR header that lowers `kx + M` mid-stream | clearing above the top (Q10) |
| S18 | `icc_mode` 3..5 over all index pairs | procedure B (Q11) |
| S19 | SBR frames without PS data after a header, and after a parsed layout switch | ffmpeg's behaviour of Q9; reported, not bounded |

S7, S8, S9, S10, S12 and S13 to S15 are expected to hold **3e-6**. S16b, S18 (until
Q11 is settled) and S19 are measured and their differences reported.

### 7.4 Library streams

- `he44-ps.aac` (committed): its first frames (the fade of 4.3) and the whole file,
  against `ref/he44-ps.f32` and `ref-linux/he44-ps.f32` (to be added). This settles
  PS1's open item: that file's IID/ICC values, seen through ffmpeg's stereo output.
- From the 801 files, behind `AAC_REAL_HE_DIR`: one file per census class of PS1 (IID
  off; IID mode 1 only; mode 4 only; both; frames with 2 envelopes; frames with 4).
  Each is chosen deterministically, as the first file of the class in order of content
  hash. Numbers and hashes are logged, never names. All 801 are PS4's.
- The comparison aligns as the SBR comparisons do. Files with a length difference
  (decode#52's edit-list blocker, aac-ps-design.md decision 2) are compared over the
  common span and logged.

### 7.5 Expected differences from ffmpeg (budget for the 3e-6 bound)

| Source | Size |
|---|---|
| ffmpeg's 8-digit `arccos` | ≤ 4.5e-8 rad in `α`, so ≤ 4.5e-8 relative on `g_·d` |
| float32 gains (`cosf`, `sinf`, `sqrtf`) | ~1e-7 relative |
| interpolation by repeated float32 addition of a step (ffmpeg) against the closed form | up to 32 roundings, ~2e-7 relative typically; worst case 32·2⁻²⁴·√2 ≈ 2.7e-6 relative, close to the bound: measure on S10 before asserting |
| float32 all-pass recursion | ~12.2 · 30 · 2⁻²⁴ ≈ 2e-5 **relative to the band's local magnitude**, but spread over 64 bands and synthesised; expected well under 3e-6 of full scale at PCM; measure on S7 |
| float32 transient gain near silence | large relative, negligible absolute (it multiplies a near-zero `d`) |
| first-frame fade, clearing above top | none: the same behaviour is specified |
| image-row phase sign (IPD/OPD only) | not small: excluded (S16b) |

## 8. Open questions

- **Q1 eq. 7 or eq. 8.** Both references use eq. 8, and so does this specification.
  The conformance streams with |IID| > 0 and ICC < 1 decide it. The reference holds
  both.
- **Q2 ICC disabled.** Disabled ICC means index 0, so ρ = 1 and `d` has no weight.
  The decorrelator still runs, and its state keeps evolving (ffmpeg). This is
  observable only when ICC is later enabled. Unverified.
- **Q3 IID resolution after a header that disables IID.** The gains use the last
  header's resolution, with all indices 0. Any resolution gives 0 dB, so it is
  harmless.
- **Q4 Formulas for normative constants.** `a_m = e^{−D_m/7}` (FAAD2's comment) and
  `λ = e^{−4/15}` (with FAAD2's low-rate `e^{−8/15}`) agree to about 1e-15. They stay
  normative until a standard text is opened. Promoting `a_m` to derived on the strength
  of FAAD2's comment is the main session's decision.
- **Q5 IPD/OPD on image rows.** The derived rule and FAAD2 conjugate the gains on the
  rows with negative frequency (20: 0, 1; 34: 9 to 11), over the whole envelope.
  ffmpeg flips rows 9 to 13 at 34 bands and only the interpolation start. The
  conformance streams with IPD/OPD decide.
- **Q6 Rounding of index averages.** Both references truncate toward zero. A standard
  that rounds would change the remapped index for an odd sum of negative IIDs.
- **Q7 Equal borders.** ffmpeg allows zero-length envelopes (a jump). FAAD2 forces
  each border at least one slot after the previous, and at most 32 minus the number
  of envelopes left. Decided by conformance streams with class 1.
- **Q8 First applied frame.** ffmpeg fades in from a zero matrix. FAAD2 starts from
  the identity-like matrix. This specification follows ffmpeg. Conformance decides.
  This also affects the PS2 streams' first frame.
- **Q9 Frames without PS data.** ffmpeg keeps applying the last parameters. Its carried
  matrix is then taken from the envelope count of the last **parsed** frame (not
  necessarily the last envelope), and a layout switch is re-detected in every such
  frame (repeated reset and remap). This specification carries the matrix in force at
  the end of the previous applied frame and resets once. No library file has an SBR
  frame without PS data (PS1 census). S19 measures the difference.
- **Q10 Clearing above the SBR top.** ffmpeg clears, FAAD2 does not. The standard is
  unknown. It is observable only when `kx + M` decreases.
- **Q11 Procedure B.** Section 6.1's rotation reproduces the covariance and matches
  ffmpeg. FAAD2's sign and placement differ, and its table layout was not traced.
  Conformance streams with ICC mode ≥ 3 decide.
- **Q12 Baseline PS.** ffmpeg is built as a full, not baseline, PS decoder and applies IPD/OPD and
  procedure B. Whether the HE-AAC v2 profile requires a decoder to do so is not
  verified (aac-ps-design.md risks).
- **Q13 Sample-rate dependence.** FAAD2 has a disabled build option with different
  constants below 32 kHz (all-pass delays 1, 2, 3; 45 all-pass bands; `λ = e^{−8/15}`;
  `μ = 0.6`). Both enabled references use one set for every rate, and so does this
  specification. The library's PS files are all at one SBR rate pair. A conformance
  stream at a low rate would show a rate-dependent standard.

## 9. Read log

Everything opened for this specification. Copyleft code was read to learn what is
computed; nothing was copied, and no structure is reproduced above.

| Source | Lines / pages | What it gave |
|---|---|---|
| H. Purnhagen, DAFx'04, <https://dafx.de/paper-archive/2004/P_163.PDF> | pp. 164–166: eq. 1–8, Fig. 3, §3.3, §4, §4.1, §4.2, Fig. 5 | IID/ICC definitions; `H` (eq. 4–5) with `c_l`, `c_r`, `α`; `β` exact (eq. 7) and approximate (eq. 8); interpolation of `h_ij` between parameter updates; all-pass for the first 23 QMF bands; delay lines 3, 4, 5 with fractional delays as phase rotations at the centre frequency; transient reduction (qualitative) |
| FFmpeg n9.0.2 `libavcodec/aacps.c` (LGPL), local clone at tag n9.0.2 | 186–198, 200–397, 399–517, 519–555, 557–717, 719–738 | row class boundaries, decay start, the four index maps and two value maps, transient recursion and constants, decorrelator classes and delays, state reset on layout switch, carried matrix, procedure selection, IPD/OPD smoothing and the image-row rule, interpolation from borders, clearing above the SBR top |
| FFmpeg `libavcodec/aacpsdsp_template.c` | 29–44, 104–211 | power sum, real gain on complex, the all-pass link arithmetic (pre-delay 2, link delays 3/4/5, `a_m`), the 2×2 interpolated mix with and without phases |
| FFmpeg `libavcodec/aacps_tablegen.h` | 38–51, 85–208 | IPD/OPD smoothing weights and normalisation, IID gains, ICC values and 8-digit arc cosines, procedures A and B, centre frequencies and fractional delays (0.39; 0.43, 0.75, 0.347) |
| FFmpeg `libavcodec/aacps.h` | 28–106 | state sizes (14-slot delay, 5-slot link history, 3 links, 5+1 matrices) |
| FFmpeg `libavcodec/aacps_common.c` | 255–289 (and search hits 82, 170–184, 224–261) | layout recomputed after parsing, previous layout updated per parsed frame, the "PS started" flag, fault path |
| FFmpeg `libavcodec/aacsbr_template.c` | 60–113, 1738–1780 | PS state zero at allocation (first-frame fade); PS applied only when started, else the mono block copied; two syntheses, channel 0 and 1 |
| FAAD2 `libfaad/ps_dec.c` (GPL), <https://raw.githubusercontent.com/knik0/faad2/master/libfaad/ps_dec.c>, master as fetched 2026-11-13 | 36–150, 735–959, 1038–1377, 1456–1886, 1896–2035 | the same constants and classes; the `a(m) = exp(-d/7)` comment; low-rate constants under a build option; image-row negation mask; transient recursion; identity start matrix; border adjustment; mixing procedures A and B; per-envelope phase smoothing |
| FAAD2 `libfaad/ps_tables.h` | 78–82, 147–158, 201–205, 270–274 | rotation tables (centre frequencies and sign recovered, section 3.3) |
| FAAD2 `libfaad/common.h` | 268–299 | its complex multiplication multiplies by the conjugate (sign of the rotations) |
| decode `docs/aac-ps-design.md`, `docs/aac-ps-syntax.md`, `docs/aac-ps-hybrid.md`, `aac_ps_params.go` 38–67, `.provenance.yml` PS entries (`origin/main` at `afb9007`) | whole notes; listed lines | pipeline, decisions, parameters in force, row layouts and centre frequencies, declared constants |
| Throwaway Go program (scratchpad, not committed), 2026-11-13 | — | `e^{−D/7}`, `e^{−4/15}`, `e^{−8/15}` against the held digits; FAAD2 rotation angles to `f`; the anchor gains of 4.1; ffmpeg's arc-cosine errors |

Not opened: the ISO/IEC 14496-3 text (no table number above is verified), Schuijers
et al. AES 2004, P1's reference [4], PacketVideo's PS sources.

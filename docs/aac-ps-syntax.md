# Parametric Stereo data: specification of what the parser must do

Status: specification for a clean-room rewrite of `aac_ps.go`, written for #60. It states
rules, not code. Sources: ISO/IEC 14496-3 subpart 8 as known to the author (clause and
table numbers not verified), what `docs/aac-ps-design.md` records of ffmpeg n9.0.2
`aacps_common.c` (read to learn what is parsed and what persists), and 240163 real
frames (`TestAACPSCensus`) that the present parser consumes exactly. Where the standard's
behaviour is not known, a rule says "as ffmpeg" and marks it **unverified**; those are
decided by conformance streams later. The tables (`aacPSHuffSyms`, `aacPSHuffLens`,
`psHuffOffset`) stay as they are.

## 1. Input and output

Input: the bits of one `ps_data`, `nbits` of them, and a persistent per-element state.
Output: the state updated, or a fault. The parser reads only inside `nbits` bits (a read
past them is a fault); it must leave the caller's reader exactly `nbits` further. After
the last field the bits that remain are fill and are ignored; for a well-formed stream
fewer than 8 remain (record how many, for tests).

## 2. Fields, in bit order

All integers are unsigned, most significant bit first.

| Field | Bits | Present when |
|---|---|---|
| header flag | 1 | always |
| IID enable | 1 | header flag set |
| IID mode | 3 | header flag and IID enable |
| ICC enable | 1 | header flag set |
| ICC mode | 3 | header flag and ICC enable |
| extension enable | 1 | header flag set |
| frame class | 1 | always |
| envelope code | 2 | always |
| border of each envelope | 5 each | frame class 1 only, one per envelope |
| per envelope: time-delta flag, then the IID indices | 1, then Huffman | IID enabled (the flag in force) |
| per envelope: time-delta flag, then the ICC indices | 1, then Huffman | ICC enabled |
| extension size | 4, plus 8 when the 4 bits are 15 | extension enabled (the flag in force) |
| extension items | see section 8 | extension enabled |

Order: the header fields, class, envelope code, borders, all IID envelopes, all ICC
envelopes, the extension. The enable flags, modes and extension flag **persist** from the
last header; a frame without a header uses them. Before any header they are all off
(IID and ICC disabled, extension off, band counts 0).

## 3. Header values

- IID mode and ICC mode 0 to 5 are valid; 6 and 7 are reserved: a fault.
- Parameter bands for IID and for ICC by mode 0..5: 10, 20, 34, 10, 20, 34.
- IID resolution: modes 0-2 coarse (indices -7..7, 15 steps); modes 3-5 fine (-15..15,
  31 steps). ICC has one resolution (0..7); ICC mode 3-5 only selects the mixing
  procedure later, not parsed differently.
- IPD and OPD band count: 5, 11, 17 for IID mode 0/3, 1/4, 2/5. It is set by the IID mode
  only, when a header enables IID. A header that disables IID leaves the previous count.
- The frame is in the 34-band layout when IID is enabled with 34 bands or ICC is enabled
  with 34 bands. This is recomputed after every successfully parsed frame in which IID or
  ICC is enabled; when neither is, the layout in force is kept. Keep also the layout of
  the frame before: it is updated by EVERY successfully parsed frame, recomputed or not.

## 4. Envelopes and borders

- Envelope count from the envelope code: class 0 maps 0,1,2,3 to 0,1,2,4 envelopes;
  class 1 maps 0,1,2,3 to 1,2,3,4. Keep the previous frame's final count (see section 7)
  as "old count" before replacing it.
- Border 0 is -1. Class 0: envelope e (1-based) of n ends at slot e*32/n - 1 (n is 1, 2
  or 4). Class 1: the borders are read; each must not be smaller than the one before
  (equal is allowed), else a fault.
- Slots are the 32 QMF time slots of the frame.

## 5. Indices

Each envelope of a parameter carries one index per parameter band of its resolution
(sections 3). Per envelope a flag says how it is coded: 0 frequency-delta, 1 time-delta.

Code choice (all Huffman, codes in the tables):

| Parameter | Frequency-delta code | Time-delta code |
|---|---|---|
| IID fine | table 0 | table 1 |
| IID coarse | table 2 | table 3 |
| ICC | table 4 | table 5 |
| IPD | table 6 | table 7 |
| OPD | table 8 | table 9 |

A decoded symbol plus `psHuffOffset[table]` is the delta.

- Frequency-delta: the index of band 0 is delta added to 0; each further band is the
  previous band's index plus its delta.
- Time-delta: the index of a band is the index of the same band in the reference envelope
  plus its delta. The reference is the previous envelope of this frame, or, for the first
  envelope, the last envelope of the previous frame as it stands after section 7. When the
  previous frame had no envelopes at all (old count 0 or 1), the reference is the stored
  first envelope (unverified: reference behaviour, not confirmed against the standard).
- IPD and OPD only: after each addition the value is reduced modulo 8 (the sum wraps; in
  frequency coding the running value is the wrapped one). They have no further range
  check.
- IID: every index must lie in -7..7 (coarse) or -15..15 (fine), ICC in 0..7: else a
  fault. The check follows each band.
- When IID (ICC) is disabled the whole stored IID (ICC) array is cleared to zero by the
  frame.

## 6. Closing envelope

After the extension, if the frame has no envelope, or its last border is below 31, one
envelope is added whose border is 31:

1. Its parameters are a copy of the last envelope of this frame; when the frame had none,
   of the last envelope of the previous frame (by old count). Copy IID if IID enabled, ICC
   if ICC enabled, IPD and OPD if IPD/OPD are enabled (section 8). When there is nothing
   to copy from (no envelope in this frame and old count 0) the envelope keeps whatever its
   slot holds (zeros at start). When the frame had no envelope and old count is 1, the
   envelope is its own source: nothing moves (unverified: reference behaviour, not
   confirmed against the standard). Known difference: the reference copies the whole
   parameter row; the parser copies only the bands of the resolution in force. It matters
   only to a time-delta read of bands past the count after a count change
   (`TestPSClosingCopiesBandsInForce`).
2. A copied IID index must still be in range for the **current** resolution (it may have
   been valid at the old one): else a fault. ICC needs no recheck.
3. The envelope count increases by one. At most 5 envelopes can result.

A frame whose last border is already 31 gets no closing envelope.

## 7. State kept between frames

Enables, modes, band counts, resolutions, extension flag, IPD/OPD enable, 34-band layout
and previous layout, "PS usable", the final envelope count (including a closing one, which
becomes the next frame's old count), and the parameters (IID, ICC, IPD, OPD, up to 5
envelopes of 34 entries; entries past the band count are left as they are). A header sets
"PS usable" after the frame parses; a fault clears it.

## 8. Extension

Present when extension is enabled. Read the size (section 2) in bytes: it is the number of
bits `8*size` the loop may use. Repeat while at least 8 bits remain of that budget: read a
2-bit id; subtract 2; if the id is 0, read IPD/OPD data and subtract the bits it took.
Ids other than 0 carry no data here (no length is known; the following bits are read as
further ids). If the budget ends negative: a fault. Then skip what remains of the budget.

IPD/OPD data (id 0): a 1-bit enable (this sets the persistent IPD/OPD enable, off or on);
when on, for each envelope of the frame (before any closing envelope): a time-delta flag
and the IPD indices, then a time-delta flag and the OPD indices, both with the IPD/OPD
band count and the codes of section 5; finally 1 reserved bit. The enable is **not**
reset by a frame that has no extension (as ffmpeg; **unverified**), so such a frame
keeps IPD/OPD in force with whatever they held. When the enable is off after the
frame, IPD and OPD arrays are cleared.

## 9. Faults

Any fault (reserved mode, falling borders, index out of range, truncated Huffman code,
extension overflow, reading past `nbits`, final read position above `nbits`, `nbits`
above the buffer): clear all four arrays, clear "PS usable", skip to the end of the data.
Counts and flags already updated in this frame stay as they were set. The frame still
decodes (caller's concern). A later header starts PS again.

## 10. Observable facts for tests

- Real library frames: fixed class, 0/1/2/4 envelopes, modes IID 1 or 4, ICC 1, no
  extension; the closing envelope is added in all files; a frame with 0 envelopes repeats
  the previous parameters (22 % of frames); fill under 8 bits.
- The tests in `aac_ps_test.go` and the writer `aac_ps_writer_test.go` encode the
  rules above; the fuzz target and its corpus bound the behaviour on arbitrary bits.
- Rules marked **unverified** must be re-decided against ISO conformance streams.

// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/cmplx"
	"math/rand"
	"testing"
)

// aacPSStageBound is the stated bound of the binary32 decorrelation and mixing stage
// against its float64 definition (psMixer.apply, itself held to 2^-44 of the row's peak by
// TestPSMixAgainstReference): per sample, over the rows of every scenario of
// psRefScenarios, |float32 stage - float64 stage| is at most 2^-17 of the largest magnitude
// the row had since the scenario began. 2^-20 is 5 times the measured worst case
// (1.87e-7, about 3 binary32 ulps: the all-pass recursion is stable, so the rounding does
// not accumulate), and 8 times tighter than the bound of the whole hybrid chain.
const aacPSStageBound = 0x1p-20

// TestPSStage32Bound runs every reference scenario through the binary32 stage (the input
// rounded to binary32 planes, transientSlot32, step32) and through the float64 stage on the
// same rows, and asserts the bound per sample, never as an average.
func TestPSStage32Bound(t *testing.T) {
	var worstAll float64
	for name, steps := range psRefScenarios() {
		t.Run(name, func(t *testing.T) {
			rng := rand.New(rand.NewSource(5))
			m64, m32 := new(psMixer), new(psMixer)
			var worst float64
			var last *psState
			var peak [psMaxRows]float64
			for fi, st := range steps {
				if st.fresh {
					if last != nil {
						st.p.numEnvOld = last.numEnv
					}
					last = st.p
				} else {
					st.p = last
				}
				dl := psDecLayouts[0]
				if st.p.is34 {
					dl = psDecLayouts[1]
				}
				h := new(psHybrid)
				h.rows = psNoiseRows(rng, dl.rows, 300, st.burst)
				// the binary32 stage gets the input rounded to binary32
				in := h.rows
				for n := range in {
					for k := range in[n] {
						in[n][k] = complex(float64(float32(real(in[n][k]))), float64(float32(imag(in[n][k]))))
					}
				}
				h.rows = in
				m64.apply(h, st.p, st.top)

				dl32 := m32.begin(st.p, st.top)
				var l, r psPlanes
				for n := 0; n < sbrQMFSlots; n++ {
					re, im := m32.st32.inSlot(m32.tick)
					for k := 0; k < dl32.rows; k++ {
						re[k], im[k] = float32(real(in[n][k])), float32(imag(in[n][k]))
					}
					m32.transientSlot32(n, re, im, dl32)
					m32.step32(n, dl32, &l, &r)
					for k := 0; k < dl32.rows; k++ {
						peak[k] = math.Max(peak[k], cmplx.Abs(in[n][k]))
						for side, pair := range [2]struct {
							ref  complex128
							l, r float32
						}{
							{h.rows[n][k], l[0][k], l[1][k]},
							{m64.rr[n][k], r[0][k], r[1][k]},
						} {
							got := complex(float64(pair.l), float64(pair.r))
							d := cmplx.Abs(got - pair.ref)
							worst = math.Max(worst, d/math.Max(peak[k], 1e-30))
							if bound := aacPSStageBound * peak[k]; d > bound {
								t.Fatalf("frame %d row %d slot %d side %d: |f32 - f64| = %g over %g", fi, k, n, side, d, bound)
							}
						}
					}
				}
			}
			worstAll = math.Max(worstAll, worst)
			t.Logf("largest difference %.3e of the row's peak (bound 2^-20 = %.3e)", worst, aacPSStageBound)
		})
	}
	t.Logf("largest over the scenarios %.3e (2^%.2f)", worstAll, math.Log2(worstAll))
}

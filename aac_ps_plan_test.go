// Copyright The go-avkit Authors. Use of this source code is governed by the license in LICENSE.

package decode

import "testing"

// TestPSAllPassPlanRefusalPanics pins the guard of allPassPlan: the layout tables and the
// mixer's arrays are fixed, so a plan the floats package refuses is a bug and panics rather
// than decorrelating from unproved memory.
func TestPSAllPassPlanRefusalPanics(t *testing.T) {
	dl := newPSDecLayout(false)
	dl.apRows.Q1 = dl.apRows.Q1[:dl.nAP-1] // a table one row short
	defer func() {
		if recover() == nil {
			t.Fatal("allPassPlan accepted a layout table that is one row short")
		}
	}()
	new(psMixer).allPassPlan(dl)
}

// TestPSAllPassPlanIsReused checks that a plan is kept for its layout and mixer and rebuilt
// for another layout.
func TestPSAllPassPlanIsReused(t *testing.T) {
	a, b := newPSDecLayout(false), newPSDecLayout(true)
	m := new(psMixer)
	p := m.allPassPlan(a)
	if m.allPassPlan(a) != p {
		t.Fatal("the plan was rebuilt for the same layout")
	}
	if m.allPassPlan(b) == p {
		t.Fatal("the plan was kept across a layout change")
	}
}

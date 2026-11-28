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

// TestPSAllPassPlan32Guards pins the guards of allPassPlan32: the plan is kept for its layout
// and stage, rebuilt for another layout and for a copy of the stage (a plan proves the memory
// of the stage it was built for, not of a copy's), and a refused table panics.
func TestPSAllPassPlan32Guards(t *testing.T) {
	a, b := newPSDecLayout(false), newPSDecLayout(true)
	s := new(psStage32)
	p := s.allPassPlan32(a)
	if s.allPassPlan32(a) != p {
		t.Fatal("the plan was rebuilt for the same layout")
	}
	if s.allPassPlan32(b) == s.allPassPlan32(a) {
		t.Fatal("the plan was kept across a layout change")
	}
	q := s.allPassPlan32(a)
	c := new(psStage32)
	*c = *s
	if c.allPassPlan32(a) == q {
		t.Fatal("the plan of the original stage served a copy")
	}
	bad := newPSDecLayout(false)
	bad.apRows32.Q1Re = bad.apRows32.Q1Re[:bad.nAP-1]
	defer func() {
		if recover() == nil {
			t.Fatal("allPassPlan32 accepted a layout table that is one row short")
		}
	}()
	new(psStage32).allPassPlan32(bad)
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

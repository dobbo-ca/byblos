package content

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mapEnv is a fake resource tree: one map of name to XObject per scope.
type mapEnv []map[string]XObject

func (e mapEnv) XObject(scope int, name string) (XObject, bool) {
	if scope < 0 || scope >= len(e) {
		return XObject{}, false
	}
	xo, ok := e[scope][name]
	return xo, ok
}

// mapEnv resolves no /ExtGState, so every state it reports is opaque. The
// documents that care carry a gsEnv instead.
func (e mapEnv) ExtGStateOpaque(scope int, name string) bool { return true }

// mapEnv resolves no fonts either; the tests that care carry a fontEnv.
func (e mapEnv) Font(scope int, name string) (int, bool) { return 0, false }

// gsEnv names the /ExtGState resources that introduce transparency. Everything
// else is opaque, which is also how a real document behaves: a graphics state
// that says nothing about /ca, /CA or /SMask leaves painting opaque.
type gsEnv struct {
	mapEnv
	transparent map[string]bool
}

func (e gsEnv) ExtGStateOpaque(scope int, name string) bool { return !e.transparent[name] }

func imageEnv(id int) mapEnv {
	return mapEnv{{"Im0": {Image: true, ID: id}}}
}

func boxEq(t *testing.T, got Box, llx, lly, urx, ury float64) {
	t.Helper()
	const eps = 1e-9
	if math.Abs(got.LLX-llx) > eps || math.Abs(got.LLY-lly) > eps ||
		math.Abs(got.URX-urx) > eps || math.Abs(got.URY-ury) > eps {
		t.Errorf("box = %+v; want {%g %g %g %g}", got, llx, lly, urx, ury)
	}
}

func TestMatrixMulIsApplyThenApply(t *testing.T) {
	scale := Matrix{2, 0, 0, 2, 0, 0}
	move := Matrix{1, 0, 0, 1, 5, 5}
	// move first, then scale: (1,1) -> (6,6) -> (12,12)
	got := move.Mul(scale)
	x, y := got.Apply(1, 1)
	if x != 12 || y != 12 {
		t.Errorf("move.Mul(scale).Apply(1,1) = (%g, %g); want (12, 12)", x, y)
	}
}

func TestMatrixUnitSquareBoxHandlesNegativeScale(t *testing.T) {
	// A y-flip: the unit square maps to y in [-1, 0].
	boxEq(t, Matrix{1, 0, 0, -1, 0, 0}.UnitSquareBox(), 0, -1, 1, 0)
}

func TestWalkSingleImagePlacement(t *testing.T) {
	s, err := Walk(context.Background(), []byte("q 612 0 0 792 0 0 cm /Im0 Do Q"), 0, imageEnv(7))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want exactly one", s.Images)
	}
	if s.Images[0].ID != 7 || s.Images[0].Name != "Im0" {
		t.Errorf("placement = %+v; want ID 7, name Im0", s.Images[0])
	}
	boxEq(t, s.Images[0].Box, 0, 0, 612, 792)
}

func TestWalkNestedCTM(t *testing.T) {
	s, err := Walk(context.Background(), []byte("q 2 0 0 2 10 10 cm q 1 0 0 1 5 5 cm /Im0 Do Q Q"), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want exactly one", s.Images)
	}
	// inner translate(5,5) composed under outer scale(2) translate(10,10)
	boxEq(t, s.Images[0].Box, 20, 20, 22, 22)
}

func TestWalkQRestoresTheCTM(t *testing.T) {
	src := "q 10 0 0 10 0 0 cm /Im0 Do Q /Im0 Do"
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 2 {
		t.Fatalf("Images = %+v; want two", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 10, 10)
	boxEq(t, s.Images[1].Box, 0, 0, 1, 1)
}

// An unbalanced Q must not panic or corrupt the CTM stack.
func TestWalkUnbalancedRestore(t *testing.T) {
	s, err := Walk(context.Background(), []byte("Q Q q 5 0 0 5 0 0 cm /Im0 Do Q Q Q"), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 5, 5)
}

func TestWalkCountsShownCharacters(t *testing.T) {
	src := "BT /F1 12 Tf (Hello) Tj [ (wor) -120 (ld) ] TJ ET"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.TextChars != 10 {
		t.Errorf("TextChars = %d; want 10", s.TextChars)
	}
	if s.TextOps != 2 {
		t.Errorf("TextOps = %d; want 2", s.TextOps)
	}
}

// The quote operators show text too, and the double-quote form takes two
// numeric operands before the string.
func TestWalkCountsQuoteOperators(t *testing.T) {
	s, err := Walk(context.Background(), []byte("BT (ab) ' 1 2 (cde) \" ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.TextChars != 5 || s.TextOps != 2 {
		t.Errorf("TextChars = %d, TextOps = %d; want 5, 2", s.TextChars, s.TextOps)
	}
}

func TestWalkRecursesIntoFormXObjects(t *testing.T) {
	env := mapEnv{
		{"Fm0": {Content: []byte("q 100 0 0 100 0 0 cm /Im0 Do Q"), Matrix: Matrix{2, 0, 0, 2, 0, 0}, Scope: 1}},
		{"Im0": {Image: true, ID: 42}},
	}
	s, err := Walk(context.Background(), []byte("q 0.5 0 0 0.5 0 0 cm /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 || s.Images[0].ID != 42 {
		t.Fatalf("Images = %+v; want one placement of ID 42", s.Images)
	}
	// form /Matrix scale(2) under page CTM scale(0.5) is the identity.
	boxEq(t, s.Images[0].Box, 0, 0, 100, 100)
}

// This is the regression the whole classification design rests on: text inside
// a form must be seen, because the page's own image count cannot see it.
func TestWalkSeesTextInsideAForm(t *testing.T) {
	env := mapEnv{
		{
			"Im0": {Image: true, ID: 1},
			"Fm0": {Content: []byte("BT (Scanned 2026-07-27) Tj ET"), Matrix: Identity, Scope: 1},
		},
		{},
	}
	s, err := Walk(context.Background(), []byte("q 612 0 0 792 0 0 cm /Im0 Do Q q /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Errorf("Images = %+v; want one", s.Images)
	}
	if s.TextChars != 18 || s.TextOps != 1 {
		t.Errorf("TextChars = %d, TextOps = %d; want 18, 1", s.TextChars, s.TextOps)
	}
}

// Images is z-ordered: index 0 is painted first and the last index is on top.
// Classification decides which layer is visible from that order alone, so the
// order has to survive a Form XObject, which is the one place a walk could
// plausibly reorder it.
func TestWalkImagesAreInPaintOrder(t *testing.T) {
	env := mapEnv{
		{
			"ImA": {Image: true, ID: 1},
			"Fm0": {Content: []byte("/ImB Do"), Matrix: Identity, Scope: 1},
			"ImC": {Image: true, ID: 3},
		},
		{"ImB": {Image: true, ID: 2}},
	}
	s, err := Walk(context.Background(), []byte("/ImA Do /Fm0 Do /ImC Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	var got []int
	for _, pl := range s.Images {
		got = append(got, pl.ID)
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("placement ids = %v; want [1 2 3], the order they are painted in", got)
	}
}

// Placements are opaque by default and stop being so under an /ExtGState that
// introduces transparency. Q restores the previous state, so the second
// placement here is opaque again.
func TestWalkRecordsGraphicsStateOpacity(t *testing.T) {
	env := gsEnv{mapEnv{{"Im0": {Image: true, ID: 1}}}, map[string]bool{"GSa": true}}
	s, err := Walk(context.Background(), []byte("q /GSa gs /Im0 Do Q /Im0 Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 2 {
		t.Fatalf("Images = %+v; want two", s.Images)
	}
	if s.Images[0].Opaque {
		t.Error("placement under /GSa reported Opaque; the state sets transparency")
	}
	if !s.Images[1].Opaque {
		t.Error("placement after Q reported not Opaque; Q restores the graphics state")
	}
}

// Transparency set outside a form applies to what the form paints.
func TestWalkCarriesOpacityIntoForms(t *testing.T) {
	env := gsEnv{mapEnv{
		{"Fm0": {Content: []byte("/Im0 Do"), Matrix: Identity, Scope: 1}},
		{"Im0": {Image: true, ID: 1}},
	}, map[string]bool{"GSa": true}}
	s, err := Walk(context.Background(), []byte("q /GSa gs /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	if s.Images[0].Opaque {
		t.Error("a placement inside a form reported Opaque; the enclosing state is transparent")
	}
}

// The canonical invisible OCR layer, verbatim from ia-DTIC_ADA134285.pdf p20.
// Rendering mode 3 paints no glyphs, so this text deposits no ink at all — and
// every scan pipeline ships a layer like it.
func TestWalkDoesNotCountInvisibleTextAsInked(t *testing.T) {
	src := "BT\n3 Tr /F10 1 Tf\n11.4 0 0 12 119 703.2 Tm (References)Tj\nET"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.TextOps != 1 {
		t.Errorf("TextOps = %d; want 1 (the operator is still there)", s.TextOps)
	}
	if s.InkedTextOps != 0 {
		t.Errorf("InkedTextOps = %d; want 0 (3 Tr paints no glyphs)", s.InkedTextOps)
	}
}

func TestWalkCountsVisibleTextAsInked(t *testing.T) {
	s, err := Walk(context.Background(), []byte("BT /F1 12 Tf (Hello) Tj [ (wor) -120 (ld) ] TJ ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.InkedTextOps != 2 {
		t.Errorf("InkedTextOps = %d; want 2 (mode 0 fills glyphs)", s.InkedTextOps)
	}
}

// ISO 32000-1 table 106. Modes 0-2 fill and/or stroke, 4-6 do the same and also
// clip, 3 paints nothing and 7 only clips. A mode outside the table is treated
// as inking, because diverting a page byblos does not understand is the safe
// direction.
func TestWalkInkPerRenderingMode(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		inked int
	}{
		{"0", 1}, {"1", 1}, {"2", 1}, {"3", 0},
		{"4", 1}, {"5", 1}, {"6", 1}, {"7", 0},
		{"9", 1}, {"-1", 1},
	} {
		t.Run("Tr "+tc.mode, func(t *testing.T) {
			s, err := Walk(context.Background(), []byte("BT "+tc.mode+" Tr (x) Tj ET"), 0, mapEnv{{}})
			if err != nil {
				t.Fatalf("Walk(context.Background(), ) error = %v", err)
			}
			if s.InkedTextOps != tc.inked {
				t.Errorf("InkedTextOps = %d; want %d", s.InkedTextOps, tc.inked)
			}
		})
	}
}

// The `3 Tr ... Tj ... 0 Tr` bracket idiom, measured on govdocs1/004513.pdf p1
// and documentcloud/dc-3331437.pdf p1. A per-page set of the Tr values a stream
// mentions would see {3, 0} here and call the text visible; only the mode in
// force at each showing operator answers the question.
func TestWalkRenderingModeIsPerOperatorNotPerPage(t *testing.T) {
	s, err := Walk(context.Background(), []byte("BT 3 Tr (hidden) Tj 0 Tr ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.TextOps != 1 || s.InkedTextOps != 0 {
		t.Errorf("TextOps = %d, InkedTextOps = %d; want 1, 0", s.TextOps, s.InkedTextOps)
	}

	s, err = Walk(context.Background(), []byte("BT 3 Tr (hidden) Tj 0 Tr (shown) Tj ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.TextOps != 2 || s.InkedTextOps != 1 {
		t.Errorf("TextOps = %d, InkedTextOps = %d; want 2, 1", s.TextOps, s.InkedTextOps)
	}
}

// Tr belongs to the text state, which is part of the graphics state, so Q
// restores it (ISO 32000-1 section 8.4.2 and table 104).
func TestWalkRestoresRenderingModeOnQ(t *testing.T) {
	s, err := Walk(context.Background(), []byte("q 3 Tr BT (a) Tj ET Q BT (b) Tj ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.TextOps != 2 || s.InkedTextOps != 1 {
		t.Errorf("TextOps = %d, InkedTextOps = %d; want 2, 1", s.TextOps, s.InkedTextOps)
	}
}

// BT/ET resets the text matrix, not the text state. A mode set in one text
// object is still in force in the next.
func TestWalkRenderingModeSurvivesBTET(t *testing.T) {
	s, err := Walk(context.Background(), []byte("BT 3 Tr ET BT (a) Tj ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.InkedTextOps != 0 {
		t.Errorf("InkedTextOps = %d; want 0 (ET does not reset Tr)", s.InkedTextOps)
	}
}

// The largest measured correction: 14 files and 207 pages of Tesseract
// /GlyphLessFont text living inside a Form XObject. A regex over the page
// stream sees no `3 Tr` there and scores the text visible.
func TestWalkSeesRenderingModeInsideAForm(t *testing.T) {
	env := mapEnv{
		{"Fm0": {Content: []byte("BT 3 Tr (References) Tj ET"), Matrix: Identity, Scope: 1}},
		{},
	}
	s, err := Walk(context.Background(), []byte("q /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.TextOps != 1 || s.InkedTextOps != 0 {
		t.Errorf("TextOps = %d, InkedTextOps = %d; want 1, 0", s.TextOps, s.InkedTextOps)
	}
}

// A form inherits the graphics state in force at its invocation, so a mode set
// on the page applies to text the form shows.
func TestWalkFormInheritsRenderingMode(t *testing.T) {
	env := mapEnv{
		{"Fm0": {Content: []byte("BT (References) Tj ET"), Matrix: Identity, Scope: 1}},
		{},
	}
	s, err := Walk(context.Background(), []byte("q 3 Tr /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.TextOps != 1 || s.InkedTextOps != 0 {
		t.Errorf("TextOps = %d, InkedTextOps = %d; want 1, 0", s.TextOps, s.InkedTextOps)
	}
}

// Do saves and restores the graphics state around a form (ISO 32000-1 section
// 8.10.1), so a mode the form sets must not leak back onto the page.
func TestWalkFormRenderingModeDoesNotLeak(t *testing.T) {
	env := mapEnv{
		{"Fm0": {Content: []byte("BT 3 Tr (hidden) Tj ET"), Matrix: Identity, Scope: 1}},
		{},
	}
	s, err := Walk(context.Background(), []byte("q /Fm0 Do Q BT (shown) Tj ET"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.TextOps != 2 || s.InkedTextOps != 1 {
		t.Errorf("TextOps = %d, InkedTextOps = %d; want 2, 1", s.TextOps, s.InkedTextOps)
	}
}

// The quote operators show text too and must be scored the same way.
func TestWalkQuoteOperatorsRespectRenderingMode(t *testing.T) {
	s, err := Walk(context.Background(), []byte("BT 3 Tr (ab) ' 1 2 (cde) \" ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.TextOps != 2 || s.InkedTextOps != 0 {
		t.Errorf("TextOps = %d, InkedTextOps = %d; want 2, 0", s.TextOps, s.InkedTextOps)
	}
}

func TestWalkRejectsUnboundedFormRecursion(t *testing.T) {
	env := mapEnv{{"Fm0": {Content: []byte("/Fm0 Do"), Matrix: Identity, Scope: 0}}}
	_, err := Walk(context.Background(), []byte("/Fm0 Do"), 0, env)
	if err == nil {
		t.Fatal("Walk(context.Background(), ) on a self-referencing form: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "nesting") {
		t.Errorf("error = %v; want it to mention nesting", err)
	}
}

func TestWalkCountsPaintingOperators(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want int
	}{
		{"stroke", "72 72 468 648 re S", 1},
		{"fill", "0 0 m 10 10 l f", 1},
		{"even-odd fill", "0 0 10 10 re f*", 1},
		{"close and stroke", "0 0 m 10 10 l b", 1},
		{"clip then no-op paint is not painting", "0 0 10 10 re W n", 0},
		{"construction alone is not painting", "0 0 m 10 10 l 20 20 l h", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Walk(context.Background(), []byte(tc.src), 0, mapEnv{{}})
			if err != nil {
				t.Fatalf("Walk(context.Background(), ) error = %v", err)
			}
			if len(s.Paints) != tc.want {
				t.Errorf("Paints = %+v; want %d", s.Paints, tc.want)
			}
		})
	}
}

// A paint operator with no current path marks nothing. Recording it would give
// it the empty box, which a containment test would then have to reason about.
func TestWalkIgnoresAPaintWithNoPath(t *testing.T) {
	s, err := Walk(context.Background(), []byte("f S B n"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 0 {
		t.Errorf("Paints = %+v; want none", s.Paints)
	}
}

// byb-b1.5 needs where a path landed, not how many there were: the decision is
// whether the raster covers it. Every path-construction operator takes its
// operands as coordinate pairs, and all of them have to reach the box.
func TestWalkRecordsThePathBoxInDeviceSpace(t *testing.T) {
	for _, tc := range []struct {
		name               string
		src                string
		llx, lly, urx, ury float64
	}{
		{"rectangle", "10 20 100 200 re f", 10, 20, 110, 220},
		{"lines", "10 10 m 50 80 l 30 5 l h f", 10, 5, 50, 80},
		{"cubic curve bounded by its control points", "0 0 m 10 90 20 90 30 0 c f", 0, 0, 30, 90},
		{"under a CTM", "q 2 0 0 3 5 7 cm 10 20 100 200 re f Q", 25, 67, 225, 667},
		// A second subpath extends the same path; both are painted by the one f.
		{"two subpaths", "0 0 10 10 re 100 100 10 10 re f", 0, 0, 110, 110},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Walk(context.Background(), []byte(tc.src), 0, mapEnv{{}})
			if err != nil {
				t.Fatalf("Walk(context.Background(), ) error = %v", err)
			}
			if len(s.Paints) != 1 {
				t.Fatalf("Paints = %+v; want exactly one", s.Paints)
			}
			boxEq(t, s.Paints[0].Box, tc.llx, tc.lly, tc.urx, tc.ury)
		})
	}
}

// The path is reset by the operator that paints or discards it, so a second
// paint reports only its own path.
func TestWalkResetsThePathAfterPainting(t *testing.T) {
	s, err := Walk(context.Background(), []byte("0 0 10 10 re f 100 100 10 10 re f"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 2 {
		t.Fatalf("Paints = %+v; want two", s.Paints)
	}
	boxEq(t, s.Paints[0].Box, 0, 0, 10, 10)
	boxEq(t, s.Paints[1].Box, 100, 100, 110, 110)
}

// n discards the path without painting it, and must clear it too.
func TestWalkResetsThePathAfterNoOp(t *testing.T) {
	s, err := Walk(context.Background(), []byte("0 0 500 500 re n 10 10 10 10 re f"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 1 {
		t.Fatalf("Paints = %+v; want one", s.Paints)
	}
	boxEq(t, s.Paints[0].Box, 10, 10, 20, 20)
}

// Stroked ink spreads half the line width to either side of the path, in device
// space, so the CTM scales the spread along with everything else.
func TestWalkInflatesAStrokeByHalfItsLineWidth(t *testing.T) {
	s, err := Walk(context.Background(), []byte("10 w 100 100 200 200 re S"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 1 {
		t.Fatalf("Paints = %+v; want one", s.Paints)
	}
	boxEq(t, s.Paints[0].Box, 95, 95, 305, 305)

	// Under a 2x CTM the same 10-point pen is 20 device points wide.
	s, err = Walk(context.Background(), []byte("q 2 0 0 2 0 0 cm 10 w 100 100 200 200 re S Q"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	boxEq(t, s.Paints[0].Box, 190, 190, 610, 610)

	// A fill is not inflated: ink stops at the path.
	s, err = Walk(context.Background(), []byte("10 w 100 100 200 200 re f"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	boxEq(t, s.Paints[0].Box, 100, 100, 300, 300)
}

// The default line width is 1.0 (ISO 32000-1 section 8.4.3.2), not zero, so a
// stroke that never sets one still spreads half a point.
func TestWalkStrokeUsesTheDefaultLineWidth(t *testing.T) {
	s, err := Walk(context.Background(), []byte("100 100 200 200 re S"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 1 {
		t.Fatalf("Paints = %+v; want one", s.Paints)
	}
	boxEq(t, s.Paints[0].Box, 99.5, 99.5, 300.5, 300.5)
}

// Index orders paints against image placements. Whether a wash is background or
// overlay is entirely this number.
func TestWalkOrdersPaintsAgainstPlacements(t *testing.T) {
	src := "0 0 10 10 re f q 612 0 0 792 0 0 cm /Im0 Do Q 20 20 10 10 re f"
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 2 || len(s.Images) != 1 {
		t.Fatalf("Paints = %+v, Images = %+v; want two paints and one image", s.Paints, s.Images)
	}
	if !(s.Paints[0].Index < s.Images[0].Index && s.Images[0].Index < s.Paints[1].Index) {
		t.Errorf("indices are paint %d, image %d, paint %d; want them strictly increasing",
			s.Paints[0].Index, s.Images[0].Index, s.Paints[1].Index)
	}
}

// A form's contents are painted where the Do appears, so a paint inside one
// must order against the page's own operators, not after all of them.
func TestWalkOrdersPaintsInsideAForm(t *testing.T) {
	env := mapEnv{
		{
			"Im0": {Image: true, ID: 1},
			"Fm0": {Content: []byte("0 0 10 10 re f"), Matrix: Identity, Scope: 1},
		},
		{},
	}
	s, err := Walk(context.Background(), []byte("/Fm0 Do q 612 0 0 792 0 0 cm /Im0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 1 || len(s.Images) != 1 {
		t.Fatalf("Paints = %+v, Images = %+v; want one of each", s.Paints, s.Images)
	}
	if s.Paints[0].Index >= s.Images[0].Index {
		t.Errorf("paint index %d, image index %d; the form's fill precedes the raster",
			s.Paints[0].Index, s.Images[0].Index)
	}
}

// The colour a paint was issued with is recorded but not resolved: `1 1 1 scn`
// is white in an ICCBased RGB space and nearly black in a three-ink DeviceN,
// and telling those apart means following /ColorSpace resources. classify does
// not read this yet; see the note on Color.
func TestWalkRecordsTheColourAPaintWasIssuedWith(t *testing.T) {
	src := "/Cs6 cs 1 1 1 scn 0 0 0 RG 4 w 0 0 10 10 re B 0.5 g 0 0 10 10 re f"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 2 {
		t.Fatalf("Paints = %+v; want two", s.Paints)
	}
	if got := s.Paints[0].Fill; got.Space != "Cs6" || len(got.Comps) != 3 || got.Comps[0] != 1 {
		t.Errorf("first fill = %+v; want space Cs6 with components 1 1 1", got)
	}
	if got := s.Paints[0].Stroke; got.Space != "DeviceRGB" || len(got.Comps) != 3 || got.Comps[0] != 0 {
		t.Errorf("first stroke = %+v; want DeviceRGB 0 0 0", got)
	}
	if got := s.Paints[1].Fill; got.Space != "DeviceGray" || len(got.Comps) != 1 || got.Comps[0] != 0.5 {
		t.Errorf("second fill = %+v; want DeviceGray 0.5", got)
	}
}

// q and Q save and restore the whole graphics state, not only the CTM. Line
// width is the one that changes a box, so it is the one asserted on.
func TestWalkQRestoresLineWidthAndColour(t *testing.T) {
	src := "2 w 0 g q 40 w 1 0 0 rg Q 100 100 200 200 re S"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 1 {
		t.Fatalf("Paints = %+v; want one", s.Paints)
	}
	boxEq(t, s.Paints[0].Box, 99, 99, 301, 301)
	if got := s.Paints[0].Fill; got.Space != "DeviceGray" {
		t.Errorf("fill = %+v; want the DeviceGray in force before the q", got)
	}
}

func TestWalkCountsShadingAndInlineImages(t *testing.T) {
	s, err := Walk(context.Background(), []byte("/Sh0 sh BI /W 1 /H 1 /BPC 8 /CS /G ID \x00 EI"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if s.ShadingOps != 1 {
		t.Errorf("ShadingOps = %d; want 1", s.ShadingOps)
	}
	if s.InlineImgs != 1 {
		t.Errorf("InlineImgs = %d; want 1", s.InlineImgs)
	}
}

func TestWalkRecordsUnresolvedXObjectNames(t *testing.T) {
	s, err := Walk(context.Background(), []byte("/Missing Do"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Unresolved) != 1 || s.Unresolved[0] != "Missing" {
		t.Errorf("Unresolved = %v; want [Missing]", s.Unresolved)
	}
}

func TestWalkPropagatesLexerErrors(t *testing.T) {
	if _, err := Walk(context.Background(), []byte("(unterminated"), 0, mapEnv{{}}); err == nil {
		t.Fatal("Walk(context.Background(), ) on an unterminated string: want an error, got nil")
	}
}

// --- byb-b1.12: clip paths (W, W*) narrow a placement's Box ----------------
//
// Walk currently ignores W/W* entirely (walk.go's "n" case comment: "W before
// it sets the clip from that path, which marks nothing either"), so every
// test below is RED against today's Walk: it reports the full, unclipped
// placement box. These tests do not touch content.Paint boxes -- see the
// no-clip-on-paint test at the end of this section for why.

// The canonical case from byb-b1.12's acceptance criterion: a clip path set
// with `re W n` narrows a placement that follows it to the intersection of
// the clip and the placement's own (unclipped) box.
func TestWalkClipPathNarrowsThePlacementBox(t *testing.T) {
	src := "q 0 0 100 100 re W n 612 0 0 792 0 0 cm /Im0 Do Q"
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 100, 100)
}

// W* (even-odd clip) sets the clip from the same current path as W (ISO
// 32000-1 8.5.4); the fill rule only matters for a self-intersecting path's
// interior, which a bounding box can never see. For bounding-box purposes it
// must narrow a placement exactly like W does.
func TestWalkEvenOddClipNarrowsThePlacementBox(t *testing.T) {
	src := "q 0 0 100 100 re W* n 612 0 0 792 0 0 cm /Im0 Do Q"
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 100, 100)
}

// Q restores the whole graphics state (ISO 32000-1 8.4.2), the clip included.
// A clip set inside a q...Q pair must not narrow a placement painted after
// the matching Q.
func TestWalkQRestoresTheClip(t *testing.T) {
	src := "q 0 0 50 50 re W n Q q 200 0 0 200 0 0 cm /Im0 Do Q"
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 200, 200)
}

// The mirror of TestWalkQRestoresTheClip: an OUTER clip must survive a nested
// q/Q that never touches it, not just the inner-clip-must-not-leak direction.
// q has to actually SAVE the clip on the stack, not push a copy with it
// nilled out (B-mutate.json PROBE 5) -- that slip is invisible to
// TestWalkQRestoresTheClip because that test's clip is set INSIDE the nested
// q/Q, not outside it.
func TestWalkQSavesTheOuterClipAcrossANestedPair(t *testing.T) {
	src := "0 0 100 100 re W n q Q 200 0 0 200 0 0 cm /Im0 Do"
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 100, 100)
}

// intersectClipPath and the form /BBox fold must allocate a new Box rather
// than mutating *clip in place through the shared pointer. Folding in place
// (B-mutate.json PROBE 8) is invisible to TestWalkClipInsideAFormDoesNotLeak
// because that test enters its form with gs.clip == nil, so the aliasing path
// is never taken; this test enters the form with an outer clip already set.
func TestWalkClipInsideAFormDoesNotCorruptTheOuterClip(t *testing.T) {
	env := mapEnv{
		{
			"Im0": {Image: true, ID: 1},
			"Fm0": {Content: []byte("0 0 50 50 re W n"), Matrix: Identity, Scope: 1},
		},
		{},
	}
	// Outer clip 0,0,200,200; the form's own inner clip (0,0,50,50) must not
	// retroactively narrow that outer clip once control returns to the page.
	src := "0 0 200 200 re W n q /Fm0 Do Q 300 0 0 300 0 0 cm /Im0 Do"
	s, err := Walk(context.Background(), []byte(src), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 200, 200)
}

// The clip a placement RECORDS (Placement.Clip, or the intersected
// Placement.Box) must not retroactively mutate once a later clip op folds
// through the same shared pointer (the other half of B-mutate.json PROBE 8,
// this time proven on an already-appended Placement rather than a live
// gs.clip).
func TestWalkClipDoesNotRetroactivelyCorruptAnAlreadyRecordedPlacement(t *testing.T) {
	src := "0 0 200 200 re W n q 300 0 0 300 0 0 cm /Im0 Do Q 0 0 50 50 re W n 300 0 0 300 0 0 cm /Im0 Do"
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 2 {
		t.Fatalf("Images = %+v; want two", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 200, 200)
	boxEq(t, s.Images[1].Box, 0, 0, 50, 50)
	// Box is a plain value, copied out at append time, so it cannot show an
	// in-place mutation of the pointer the two placements might share. Clip
	// is the pointer itself: if intersectClipPath ever mutates *clip rather
	// than allocating, the second clip op corrupts the first placement's
	// already-recorded Clip through it.
	if s.Images[0].Clip == nil {
		t.Fatal("Images[0].Clip = nil; want the first placement's clip recorded")
	}
	if got := *s.Images[0].Clip; got != (Box{LLX: 0, LLY: 0, URX: 200, URY: 200}) {
		t.Errorf("Images[0].Clip = %+v; want {0 0 200 200} -- the second clip op must not retroactively narrow it", got)
	}
}

// ISO 32000-1 7.9.5 permits a rectangle array's corners in either diagonal
// order. mapThrough must normalize over all four mapped corners, not just the
// two named ones (B-mutate.json PROBE 11) -- a swapped-corner /BBox is legal
// input, not malformed.
func TestWalkFormBBoxWithSwappedCornersIsNotCollapsed(t *testing.T) {
	env := mapEnv{
		{
			// [100 100 0 0] names the same rectangle as [0 0 100 100], corners
			// reversed.
			"Fm0": {Content: []byte("q 612 0 0 792 0 0 cm /Im0 Do Q"), Matrix: Identity, Scope: 1, BBox: &Box{LLX: 100, LLY: 100, URX: 0, URY: 0}},
		},
		{"Im0": {Image: true, ID: 1}},
	}
	s, err := Walk(context.Background(), []byte("/Fm0 Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 100, 100)
}

// A form /Matrix with a rotation must go through the same four-corner
// normalisation as a swapped-corner /BBox (B-mutate.json PROBE 11): mapping
// only the two named corners loses the box on a 90-degree turn.
func TestWalkFormBBoxWithARotatedMatrixIsNotCollapsed(t *testing.T) {
	env := mapEnv{
		{
			// A 90-degree rotation: [0 1 -1 0 0 0].
			"Fm0": {Content: []byte("q 612 0 0 792 0 0 cm /Im0 Do Q"), Matrix: Matrix{0, 1, -1, 0, 0, 0}, Scope: 1, BBox: &Box{LLX: 0, LLY: 0, URX: 100, URY: 100}},
		},
		{"Im0": {Image: true, ID: 1}},
	}
	s, err := Walk(context.Background(), []byte("/Fm0 Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	// The BBox's four corners (0,0), (100,0), (0,100), (100,100) rotated 90
	// degrees land at (0,0), (0,100), (-100,0), (-100,100); the bounding box
	// of all four, not just two, is (-100,0)-(0,100).
	boxEq(t, s.Images[0].Box, -100, 0, 0, 100)
}

// W with no current path marks nothing (ISO 32000-1 8.5.4): it must leave the
// running clip unchanged, not collapse it to an empty box (B-mutate.json
// PROBE 15).
func TestWalkClipOperatorWithNoCurrentPathDoesNotClipToNothing(t *testing.T) {
	for _, tc := range []struct {
		name, src string
	}{
		{"W with nothing painted first", "W n 300 0 0 300 0 0 cm /Im0 Do"},
		{"W immediately after n already cleared the path", "0 0 50 50 re n W n 300 0 0 300 0 0 cm /Im0 Do"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Walk(context.Background(), []byte(tc.src), 0, imageEnv(1))
			if err != nil {
				t.Fatalf("Walk(context.Background(), ) error = %v", err)
			}
			if len(s.Images) != 1 {
				t.Fatalf("Images = %+v; want one", s.Images)
			}
			boxEq(t, s.Images[0].Box, 0, 0, 300, 300)
		})
	}
}

// A form /BBox must INTERSECT the accumulated clip, not replace it
// (B-mutate.json PROBE 16): both a page clip narrower than the form's own
// /BBox, and a nested form whose /BBox is wider than its parent's, must still
// come out narrowed by the tighter of the two.
func TestWalkFormBBoxIntersectsRatherThanReplacesTheRunningClip(t *testing.T) {
	t.Run("page clip narrower than the form BBox", func(t *testing.T) {
		env := mapEnv{
			{
				"Fm0": {Content: []byte("q 1000 0 0 1000 0 0 cm /Im0 Do Q"), Matrix: Identity, Scope: 1, BBox: &Box{LLX: 0, LLY: 0, URX: 500, URY: 500}},
			},
			{"Im0": {Image: true, ID: 1}},
		}
		s, err := Walk(context.Background(), []byte("0 0 60 60 re W n /Fm0 Do"), 0, env)
		if err != nil {
			t.Fatalf("Walk(context.Background(), ) error = %v", err)
		}
		if len(s.Images) != 1 {
			t.Fatalf("Images = %+v; want one", s.Images)
		}
		boxEq(t, s.Images[0].Box, 0, 0, 60, 60)
	})

	t.Run("nested form BBox wider than the enclosing one", func(t *testing.T) {
		env := mapEnv{
			{
				"Fm0": {Content: []byte("/Fm1 Do"), Matrix: Identity, Scope: 1, BBox: &Box{LLX: 0, LLY: 0, URX: 200, URY: 200}},
			},
			{
				"Fm1": {Content: []byte("q 1000 0 0 1000 0 0 cm /Im0 Do Q"), Matrix: Identity, Scope: 2, BBox: &Box{LLX: 0, LLY: 0, URX: 500, URY: 500}},
			},
			{"Im0": {Image: true, ID: 1}},
		}
		s, err := Walk(context.Background(), []byte("/Fm0 Do"), 0, env)
		if err != nil {
			t.Fatalf("Walk(context.Background(), ) error = %v", err)
		}
		if len(s.Images) != 1 {
			t.Fatalf("Images = %+v; want one", s.Images)
		}
		boxEq(t, s.Images[0].Box, 0, 0, 200, 200)
	})
}

// Two successive `re W n` clips intersect cumulatively: the second clip
// narrows further, it does not replace the first (ISO 32000-1 8.5.4: "the new
// clipping path shall be the intersection of the current clipping path and
// the newly constructed path").
func TestWalkNestedClipsIntersect(t *testing.T) {
	src := "0 0 100 100 re W n 20 20 60 60 re W n q 200 0 0 200 0 0 cm /Im0 Do Q"
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	// intersect(0,0,100,100) with 20,20,60,60-re (corners 20,20 to 80,80) is
	// 20,20,80,80; intersecting that with the 0,0-200,200 image box leaves it
	// unchanged.
	boxEq(t, s.Images[0].Box, 20, 20, 80, 80)
}

// A clip set inside a Form XObject must not leak back out to the page that
// invoked it. doXObject already passes gstate by value into the form's own
// walk() call and that call keeps its own local q/Q stack, which is what
// makes this hold once a clip lives on gstate -- this test is the guard that
// keeps it holding.
func TestWalkClipInsideAFormDoesNotLeak(t *testing.T) {
	env := mapEnv{
		{
			"Im0": {Image: true, ID: 1},
			"Fm0": {Content: []byte("0 0 30 30 re W n"), Matrix: Identity, Scope: 1},
		},
		{},
	}
	src := "/Fm0 Do q 500 0 0 500 0 0 cm /Im0 Do Q"
	s, err := Walk(context.Background(), []byte(src), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 500, 500)
}

// A clip is fixed in DEVICE space at the moment W takes effect, not
// re-derived from its user-space path later. This is the discriminating case:
// the clip rect is set under one CTM and the image is painted under a second,
// larger one composed on top of it. A clip re-mapped through the CTM in force
// at Do time (wrong) would map the user-space rect 0,0,50,50 through the
// combined scale-300 CTM to 0,0,15000,15000 -- no effective clip at all. A
// clip fixed in device space at W time (correct, ISO 32000-1 8.5.4: "The new
// clipping path... shall be intersected with the running intersection of all
// pre-existing clipping paths") stays at 0,0,100,100 regardless.
func TestWalkClipIsFixedInDeviceSpaceNotUserSpace(t *testing.T) {
	src := "q 2 0 0 2 0 0 cm 0 0 50 50 re W n 150 0 0 150 0 0 cm /Im0 Do Q"
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 100, 100)
}

// ISO 32000-1 8.5.4: "W... shall not, in itself, have any effect on the
// clipping path. It merely sets a flag ... that shall be examined by the
// path-painting operator that follows immediately after". The clip takes
// effect after WHICHEVER path-painting operator follows -- n is only the
// no-op case -- so `re W f` both paints the path (unaffected by the byb-b1.5
// simplification this section deliberately does not touch, see below) AND
// sets the clip from it, exactly as `re W n` would.
func TestWalkClipTakesEffectAfterAnyPaintingOperatorNotJustN(t *testing.T) {
	src := "0 0 50 50 re W f q 200 0 0 200 0 0 cm /Im0 Do Q"
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 50, 50)
}

// A clip disjoint from a placement empties it out. This test PINS the chosen
// representation for "empty": the ordinary per-axis intersection
//
//	llx = max(a.LLX, b.LLX); urx = min(a.URX, b.URX)   (and the y axis likewise)
//
// inverts (urx < llx) exactly when the two rectangles do not overlap on that
// axis. Rather than introduce a second, inverted-box convention alongside
// every other Box this package produces -- pathBox, UnitSquareBox, and every
// clip test above all guarantee LLX<=URX and LLY<=URY -- an empty
// intersection clamps the collapsing edge to the near bound instead of
// letting it invert: `if urx < llx { urx = llx }`. That keeps Box's ordering
// invariant universal and turns "empty" into an ordinary zero-area box
// instead of a value every consumer of Box would need a special case for.
//
// Worked here: clip is 0,0,10,10; the image (unit square under a
// 500,0,0,500,500,500 CTM) lands at 500,500,1000,1000, disjoint on both axes.
// Per-axis: llx=max(0,500)=500, urx=min(10,1000)=10 -> clamped to 500;
// lly=max(0,500)=500, ury=min(10,1000)=10 -> clamped to 500. The pinned
// result is the single point 500,500,500,500.
func TestWalkDisjointClipReportsAZeroAreaBoxNotAnInvertedOne(t *testing.T) {
	src := "0 0 10 10 re W n q 500 0 0 500 500 500 cm /Im0 Do Q"
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 500, 500, 500, 500)
}

// A clip must NOT narrow Paint.Box. The record stays literal: Box is where the
// path went, Clip is what was cutting it, and the two are kept apart so a
// caller can ask for either. byb-7aq changed the reason this holds but not the
// rule -- the old reason was that an oversized Paint.Box "errs toward
// diverting", which stopped being true once byb-b1.12 narrowed Placement.Box
// and left this one alone. See TestWalkRecordsTheClipInForceOnAPaint and
// TestPaintInk.
func TestWalkClipDoesNotNarrowPaintBoxes(t *testing.T) {
	src := "0 0 10 10 re W n 0 0 500 500 re f"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 1 {
		t.Fatalf("Paints = %+v; want one", s.Paints)
	}
	boxEq(t, s.Paints[0].Box, 0, 0, 500, 500)
}

// The other half of that pair: Box is unnarrowed, and the clip that would have
// narrowed it is recorded beside it. Without this, a consumer comparing a path
// against a placement compares an unclipped rectangle against a clipped one,
// which is exactly what byb-7aq's four regressed pages were.
func TestWalkRecordsTheClipInForceOnAPaint(t *testing.T) {
	src := "0 0 10 10 re W n 0 0 500 500 re f"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 1 {
		t.Fatalf("Paints = %+v; want one", s.Paints)
	}
	if s.Paints[0].Clip == nil {
		t.Fatal("Paints[0].Clip = nil; want the 0 0 10 10 clip that was in force")
	}
	boxEq(t, *s.Paints[0].Clip, 0, 0, 10, 10)
}

// A path painted with nothing clipping it reports no clip, which is a different
// fact from a clip that happens to cover everything. Ink leaves such a path
// alone rather than intersecting it with a rectangle nobody wrote.
func TestWalkRecordsNoClipWhenNonePlaced(t *testing.T) {
	s, err := Walk(context.Background(), []byte("0 0 500 500 re f"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 1 {
		t.Fatalf("Paints = %+v; want one", s.Paints)
	}
	if s.Paints[0].Clip != nil {
		t.Errorf("Paints[0].Clip = %+v; want nil", *s.Paints[0].Clip)
	}
}

// ISO 32000-1 8.5.4: W/W* set the clip from the current path, and the new clip
// takes effect only AFTER the painting operator that terminates that path. So a
// path that paints and clips in one go is not clipped by its own W, and
// Paint.Clip must record the clip that was in force when the operator was
// issued, not the one it installs.
//
// The distinction is invisible on a fill -- the new clip is the old one
// intersected with the very path being filled, so the ink is the same either
// way -- and decisive on a stroke, because recordPaint spreads a stroke's box
// by half the pen while the clip comes from the un-spread path. Getting this
// backwards would shave the spread off and call a straddling stroke contained.
func TestWalkAPathIsNotClippedByItsOwnW(t *testing.T) {
	// A 20pt pen down the rectangle's own edge: the ink runs from 90 to 110 on
	// each side, while the path -- and so the clip it arms -- is 100 to 500.
	src := "20 w 100 100 400 600 re W S"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 1 {
		t.Fatalf("Paints = %+v; want one", s.Paints)
	}
	if c := s.Paints[0].Clip; c != nil {
		t.Errorf("Paints[0].Clip = %+v; want nil, the clip in force when S was issued", *c)
	}
	boxEq(t, s.Paints[0].Box, 90, 90, 510, 710)
	ink, marks := s.Paints[0].Ink()
	if !marks {
		t.Fatal("Ink() marks = false; a 20pt stroke marks the page")
	}
	boxEq(t, ink, 90, 90, 510, 710)
}

// The clip a painting operator arms still applies to everything AFTER it, which
// is the other half of 8.5.4 and the reason the pending flag exists at all.
func TestWalkAPathsOwnWClipsWhatFollows(t *testing.T) {
	src := "0 0 100 100 re W S\n200 200 300 300 re f"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 2 {
		t.Fatalf("Paints = %+v; want two", s.Paints)
	}
	if s.Paints[1].Clip == nil {
		t.Fatal("Paints[1].Clip = nil; the first operator's W clips what follows it")
	}
	boxEq(t, *s.Paints[1].Clip, 0, 0, 100, 100)
	if _, marks := s.Paints[1].Ink(); marks {
		t.Error("Ink() marks = true; the second fill is clipped entirely away")
	}
}

// A form's /BBox reaches Paint.Clip the same way it reaches Placement.Clip, and
// this is the case that made the field necessary rather than merely tidy:
// govdocs1/050104.pdf p2 clips its wash with the page's own clip and its raster
// with a form /BBox nested two deep, so the two differ by a fraction of a point
// and the wash is judged against a rectangle it was never bounded by.
func TestWalkRecordsAFormBBoxAsThePaintsClip(t *testing.T) {
	env := mapEnv{
		{"Fm0": {
			Content: []byte("0 0 500 500 re f"),
			Matrix:  Identity,
			Scope:   1,
			BBox:    &Box{LLX: 10, LLY: 10, URX: 100, URY: 100},
		}},
		{},
	}
	s, err := Walk(context.Background(), []byte("/Fm0 Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Paints) != 1 {
		t.Fatalf("Paints = %+v; want one", s.Paints)
	}
	if s.Paints[0].Clip == nil {
		t.Fatal("Paints[0].Clip = nil; the form's /BBox clips what is painted inside it")
	}
	boxEq(t, *s.Paints[0].Clip, 10, 10, 100, 100)
	boxEq(t, s.Paints[0].Box, 0, 0, 500, 500)
	ink, marks := s.Paints[0].Ink()
	if !marks {
		t.Fatal("Ink() marks = false; the fill covers the whole /BBox")
	}
	boxEq(t, ink, 10, 10, 100, 100)
}

func TestPaintInk(t *testing.T) {
	clip := &Box{LLX: 0, LLY: 0, URX: 10, URY: 10}
	for _, tc := range []struct {
		name  string
		paint Paint
		want  Box
		marks bool
	}{
		// The byb-7aq shape: a wash drawn past the page edge and clipped back to
		// it. What it can mark is the clip, not the path.
		{"a fill clipped back to the page",
			Paint{Op: "f", Box: Box{LLX: -9, LLY: -9, URX: 500, URY: 500}, Clip: clip},
			Box{LLX: 0, LLY: 0, URX: 10, URY: 10}, true},

		{"an unclipped fill is its own box",
			Paint{Op: "f", Box: Box{LLX: 1, LLY: 2, URX: 3, URY: 4}},
			Box{LLX: 1, LLY: 2, URX: 3, URY: 4}, true},

		// intersectBox clamps a disjoint pair to a degenerate box rather than
		// inverting it, so a path clipped entirely away bounds no area and marks
		// nothing.
		{"a fill clipped entirely away",
			Paint{Op: "f", Box: Box{LLX: 100, LLY: 100, URX: 200, URY: 200}, Clip: clip},
			Box{LLX: 100, LLY: 100, URX: 100, URY: 100}, false},

		// govdocs1/050104.pdf p2 opens with this exact operator sequence,
		// `0 842 m 0 842 l f` -- a fill of a path with no extent at all.
		{"a fill of a path with no extent",
			Paint{Op: "f", Box: Box{LLX: 0, LLY: 842, URX: 0, URY: 842}},
			Box{LLX: 0, LLY: 842, URX: 0, URY: 842}, false},

		// The exception. recordPaint has already spread a stroke by half the line
		// width, so a zero-area stroke box means a zero-width pen, and ISO
		// 32000-1 8.4.3.2 makes that a hairline -- the thinnest line the device
		// can render, which marks. Calling it invisible would extract a page
		// carrying a line the raster does not.
		{"a zero-width stroke is a hairline, not nothing",
			Paint{Op: "S", Box: Box{LLX: 0, LLY: 5, URX: 500, URY: 5}},
			Box{LLX: 0, LLY: 5, URX: 500, URY: 5}, true},

		// A clip disjoint from the path removes the path, and the hairline
		// exception does not reach that far: there is no line left to draw.
		{"a stroke clipped entirely away",
			Paint{Op: "S", Box: Box{LLX: 699, LLY: 699, URX: 1501, URY: 1501}, Clip: clip},
			Box{LLX: 699, LLY: 699, URX: 699, URY: 699}, false},

		// The hairline again, this time under a clip that contains it. The box is
		// degenerate and the clip is not disjoint from it, which is the only
		// combination the exception is for.
		{"a zero-width stroke inside its clip still marks",
			Paint{Op: "S", Box: Box{LLX: 0, LLY: 5, URX: 500, URY: 5}, Clip: clip},
			Box{LLX: 0, LLY: 5, URX: 10, URY: 5}, true},

		// Half the pen falls outside the clip: what is left still marks, and the
		// ink reported is the part the clip lets through.
		{"a stroke the clip only partly removes",
			Paint{Op: "S", Box: Box{LLX: -5, LLY: -5, URX: 5, URY: 5}, Clip: clip},
			Box{LLX: 0, LLY: 0, URX: 5, URY: 5}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, marks := tc.paint.Ink()
			if got != tc.want {
				t.Errorf("Ink() box = %+v; want %+v", got, tc.want)
			}
			if marks != tc.marks {
				t.Errorf("Ink() marks = %v; want %v", marks, tc.marks)
			}
		})
	}
}

// --- byb-b1.12: Form /BBox narrows a placement's Box ------------------------
//
// content.XObject has no BBox field today (walk.go's doc comment: "a Form
// XObject's /BBox clips its content... and Walk ignores it"), so every test
// below references a `BBox` field that does not exist yet. THIS IS A
// DELIBERATE RED-BY-COMPILE-ERROR STATE, not an oversight: unlike the clip
// tests above, there is no way to exercise "does Walk read and apply a form's
// /BBox" without first giving Env somewhere to put one, and content.XObject
// is that place (mirroring Matrix and Scope, the two other form-only fields
// already there). `go vet ./internal/content/...` at this state must fail
// with a compile error naming the missing BBox field, and that failure IS the
// expected RED for this section — see the test run transcript in the lane
// report for the verbatim message.
//
// The field is `*Box`: nil is how a form with no /BBox is told apart from one
// whose /BBox happens to be the zero rectangle, which
// TestWalkMissingFormBBoxDoesNotClip below depends on.

// A form's /BBox crops content painted inside it, including an oversized
// image (ISO 32000-1 8.10.2: "The result is to trim the form XObject... to
// the boundaries of that rectangle"). Both form Matrix and enclosing CTM are
// identity here, so BBox user-space coordinates equal device-space ones and
// this isolates the crop itself from the mapping tested next.
func TestWalkFormBBoxCropsThePlacement(t *testing.T) {
	env := mapEnv{
		{"Fm0": {
			Content: []byte("q 1000 0 0 1000 0 0 cm /Im0 Do Q"),
			Matrix:  Identity,
			Scope:   1,
			BBox:    &Box{LLX: 0, LLY: 0, URX: 50, URY: 50},
		}},
		{"Im0": {Image: true, ID: 1}},
	}
	s, err := Walk(context.Background(), []byte("/Fm0 Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 50, 50)
}

// The /BBox lives in the form's own coordinate system, which is /Matrix
// composed with the CTM in force at the Do that invoked it -- the exact
// composition doXObject already builds for its recursive walk() call
// (xo.Matrix.Mul(gs.ctm)). This test gives Matrix and the enclosing cm
// different, non-identity values so a wrong composition (only one of the two,
// or the wrong order) produces a different wrong answer than either alone.
//
// Matrix translate(5,5), enclosing CTM scale(3): combined is
// translate(5,5).Mul(scale(3)) = {3 0 0 3 15 15} (apply translate, then
// scale). BBox 0,0,10,10 maps through that to 15,15,45,45. The image inside
// the form is oversized well past it, so the reported box is the mapped BBox
// exactly.
func TestWalkFormBBoxIsMappedThroughMatrixAndCTM(t *testing.T) {
	env := mapEnv{
		{"Fm0": {
			Content: []byte("q 1000 0 0 1000 0 0 cm /Im0 Do Q"),
			Matrix:  Matrix{1, 0, 0, 1, 5, 5},
			Scope:   1,
			BBox:    &Box{LLX: 0, LLY: 0, URX: 10, URY: 10},
		}},
		{"Im0": {Image: true, ID: 1}},
	}
	s, err := Walk(context.Background(), []byte("q 3 0 0 3 0 0 cm /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 15, 15, 45, 45)
}

// /BBox is required by ISO 32000-1 table 95, but Byblos reads other malformed
// PDFs without erroring and a form missing it should be no different. Pinned
// choice: a nil BBox applies no BBox-derived clipping at all -- the form
// behaves as it did before byb-b1.12, not as though it clipped to nothing.
// Silently clipping to an empty box would be worse than the status quo bug:
// it would make every placement inside a malformed form disappear rather than
// merely overstate its box.
func TestWalkMissingFormBBoxDoesNotClip(t *testing.T) {
	env := mapEnv{
		{"Fm0": {
			Content: []byte("q 1000 0 0 1000 0 0 cm /Im0 Do Q"),
			Matrix:  Identity,
			Scope:   1,
			// BBox left nil: the form's /BBox was absent or unreadable.
		}},
		{"Im0": {Image: true, ID: 1}},
	}
	s, err := Walk(context.Background(), []byte("/Fm0 Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk(context.Background(), ) error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 1000, 1000)
}

func matrixEq(t *testing.T, got Matrix, want Matrix) {
	t.Helper()
	const eps = 1e-9
	for i := range got {
		if math.Abs(got[i]-want[i]) > eps {
			t.Errorf("matrix = %v; want %v", got, want)
			return
		}
	}
}

// showOrigin asserts where the shown string starts in device space: text-space
// (0,0) through Trm, rise excluded.
func showOrigin(t *testing.T, ts TextShow, x, y float64) {
	t.Helper()
	gx, gy := ts.Trm.Apply(0, 0)
	const eps = 1e-9
	if math.Abs(gx-x) > eps || math.Abs(gy-y) > eps {
		t.Errorf("origin(%q) = (%g, %g); want (%g, %g)", ts.Raw, gx, gy, x, y)
	}
}

// The bead's acceptance fixture (byb-lez.6): hand-computed Tm/Td/TJ origins,
// a BT reset between two text objects, and a " with non-zero Tw/Tc.
//
// Walk does not know glyph widths (fonts are byb-lez.5's business), so a
// string deposits no horizontal advance of its own; the origins here are
// computed under exactly that model, from the positioning operators alone.
func TestWalkTracksTextPositions(t *testing.T) {
	src := "BT /F1 12 Tf 50 700 Td (One) Tj [ (Two) -1000 (Three) ] TJ ET\n" +
		"BT /F1 10 Tf 2 0 0 2 100 500 Tm 14 TL (A) Tj 3 4 (Quote) \" ET"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.TextShows) != 5 {
		t.Fatalf("TextShows = %d entries; want 5", len(s.TextShows))
	}
	one, two, three, a, quote := s.TextShows[0], s.TextShows[1], s.TextShows[2], s.TextShows[3], s.TextShows[4]
	if string(one.Raw) != "One" || one.Font != "F1" || one.Size != 12 {
		t.Errorf("show 0 = %+v; want (One) in F1 at 12", one)
	}
	showOrigin(t, one, 50, 700)
	// A TJ number is subtracted from the displacement, in thousandths of text
	// space scaled by the font size: -1000 moves the next string RIGHT by
	// 12pt (ISO 32000-1 9.4.3).
	showOrigin(t, two, 50, 700)
	showOrigin(t, three, 62, 700)
	// BT reset the matrices; Tm then set both, and " advanced by the leading
	// (14, doubled by Tm's scale) before showing.
	if a.Size != 10 {
		t.Errorf("show 3 Size = %g; want 10", a.Size)
	}
	showOrigin(t, a, 100, 500)
	matrixEq(t, a.Trm, Matrix{2, 0, 0, 2, 100, 500})
	if string(quote.Raw) != "Quote" || quote.WordSpacing != 3 || quote.CharSpacing != 4 {
		t.Errorf("show 4 = %+v; want (Quote) with Tw 3 Tc 4", quote)
	}
	showOrigin(t, quote, 100, 472)
}

// Td, TD and T* differ only in what they do to the leading, and a walk that
// conflates them puts every later T* line in the wrong place: TD sets the
// leading to -ty, Td leaves it alone, and T* advances by whatever the leading
// is now.
func TestWalkTdVsTDVsTStar(t *testing.T) {
	src := "BT /F1 10 Tf 0 -20 TD (a) Tj T* (b) Tj 0 -5 Td (c) Tj T* (d) Tj ET"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.TextShows) != 4 {
		t.Fatalf("TextShows = %d entries; want 4", len(s.TextShows))
	}
	showOrigin(t, s.TextShows[0], 0, -20) // TD moved and set TL=20
	showOrigin(t, s.TextShows[1], 0, -40) // T* advanced by TL
	showOrigin(t, s.TextShows[2], 0, -45) // Td moved 5 but left TL=20
	showOrigin(t, s.TextShows[3], 0, -65) // T* still advances by 20, not 5
}

// ' advances to the next line BEFORE showing (ISO 32000-1 9.4.3); it is not
// just a Tj.
func TestWalkApostropheAdvancesBeforeShowing(t *testing.T) {
	src := "BT /F1 10 Tf 20 TL 0 -30 Td (x) ' ET"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.TextShows) != 1 {
		t.Fatalf("TextShows = %d entries; want 1", len(s.TextShows))
	}
	showOrigin(t, s.TextShows[0], 0, -50)
}

// Positions compose: Trm is Tm composed with the CTM in force, so nesting
// q/cm/Q around a text object multiplies through, Q restores the outer
// composition, and BT resets the text matrix without touching the CTM.
func TestWalkTextPositionsComposeUnderQAndBT(t *testing.T) {
	src := "q 2 0 0 2 10 10 cm " +
		"BT 1 0 0 1 5 5 Tm (a) Tj ET " +
		"q 1 0 0 1 100 0 cm BT 1 0 0 1 5 5 Tm (b) Tj ET Q " +
		"BT (c) Tj ET Q " +
		"BT (d) Tj ET"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.TextShows) != 4 {
		t.Fatalf("TextShows = %d entries; want 4", len(s.TextShows))
	}
	ctm1 := Matrix{2, 0, 0, 2, 10, 10}
	ctm2 := Matrix{1, 0, 0, 1, 100, 0}.Mul(ctm1)
	tm := Matrix{1, 0, 0, 1, 5, 5}
	matrixEq(t, s.TextShows[0].Trm, tm.Mul(ctm1))
	matrixEq(t, s.TextShows[1].Trm, tm.Mul(ctm2))
	matrixEq(t, s.TextShows[2].Trm, ctm1) // BT reset Tm; Q restored the CTM
	matrixEq(t, s.TextShows[3].Trm, Identity)
}

// The text state parameters live in the graphics state (ISO 32000-1 table
// 52), so Q restores them like everything else on gstate.
func TestWalkQRestoresTextState(t *testing.T) {
	src := "BT /F1 12 Tf 2 Tc 3 Tw 50 Tz 4 TL 5 Ts " +
		"q /F2 8 Tf 9 Tc 1 Tw 200 Tz 2 TL 0 Ts Q (a) Tj ET"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.TextShows) != 1 {
		t.Fatalf("TextShows = %d entries; want 1", len(s.TextShows))
	}
	ts := s.TextShows[0]
	if ts.Font != "F1" || ts.Size != 12 || ts.CharSpacing != 2 || ts.WordSpacing != 3 ||
		ts.Hscale != 0.5 || ts.Rise != 5 {
		t.Errorf("show = %+v; want F1 12 Tc 2 Tw 3 Tz 50 Ts 5 restored by Q", ts)
	}
}

// fontEnv resolves font names so a walk can attach an identity to Tf.
type fontEnv struct {
	mapEnv
	fonts map[string]int
}

func (e fontEnv) Font(scope int, name string) (int, bool) {
	id, ok := e.fonts[name]
	return id, ok
}

func TestWalkResolvesFontsThroughEnv(t *testing.T) {
	env := fontEnv{mapEnv: mapEnv{{}}, fonts: map[string]int{"F1": 7}}
	s, err := Walk(context.Background(), []byte("BT /F1 12 Tf (a) Tj /F9 9 Tf (b) Tj ET"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.TextShows) != 2 {
		t.Fatalf("TextShows = %d entries; want 2", len(s.TextShows))
	}
	if s.TextShows[0].FontID != 7 {
		t.Errorf("FontID = %d; want 7", s.TextShows[0].FontID)
	}
	// A name the env cannot resolve still records the show, with no identity:
	// byb-lez.5 needs to see "text I could not read", not silence.
	if s.TextShows[1].Font != "F9" || s.TextShows[1].FontID != 0 {
		t.Errorf("show 1 = %+v; want F9 with FontID 0", s.TextShows[1])
	}
}

// Text shows share the painting order with images and paths, so a renderer
// can interleave them (stage 4c reads occlusion off this order).
func TestWalkOrdersTextAgainstPlacements(t *testing.T) {
	src := "BT (a) Tj ET /Im0 Do BT (b) Tj ET"
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.TextShows) != 2 || len(s.Images) != 1 {
		t.Fatalf("TextShows = %d, Images = %d; want 2, 1", len(s.TextShows), len(s.Images))
	}
	if !(s.TextShows[0].Index < s.Images[0].Index && s.Images[0].Index < s.TextShows[1].Index) {
		t.Errorf("order = text %d, image %d, text %d; want strictly increasing",
			s.TextShows[0].Index, s.Images[0].Index, s.TextShows[1].Index)
	}
}

// A hostile many-Tj stream must not grow TextShows without bound: each entry
// retains its Raw bytes and text state, a ~30x retained-heap amplification of
// the stream. The counters keep counting past the cap.
func TestWalkCapsTextShows(t *testing.T) {
	const extra = 5
	src := "BT " + strings.Repeat("(a) Tj ", maxTextShows+extra) + "ET"
	s, err := Walk(context.Background(), []byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.TextShows) != maxTextShows {
		t.Errorf("TextShows = %d entries; want the cap, %d", len(s.TextShows), maxTextShows)
	}
	if s.TextOps != maxTextShows+extra || s.TextChars != maxTextShows+extra {
		t.Errorf("TextOps = %d, TextChars = %d; want both %d, counting past the cap",
			s.TextOps, s.TextChars, maxTextShows+extra)
	}
}

// TestWalkRefusesDeepGStateStack pins byb-9ml's first half. `q` is two bytes
// and saved a 328-byte gstate with no bound at all, so a content stream of
// nothing but `q ` -- which flate compresses about 1000:1, making an 8.6 KB
// PDF -- drove peak heap to 3.3 GB and returned a NIL error. The cap is
// measured, not guessed: 31,705 real page-walks put the maximum at 18.
func TestWalkRefusesDeepGStateStack(t *testing.T) {
	src := strings.Repeat("q ", maxGStateDepth+1)
	s, err := Walk(context.Background(), []byte(src), 0, imageEnv(1))
	if err == nil {
		t.Fatalf("Walk accepted %d nested q with no error", maxGStateDepth+1)
	}
	if !strings.Contains(err.Error(), "q nesting") {
		t.Errorf("error = %v; want it to name q nesting", err)
	}
	// The partial scan still comes back: inspectPage relies on that for a
	// damaged stream, and refusing must not become discarding (byb-3jq).
	if s == nil {
		t.Error("Walk returned a nil Scan alongside the error")
	}
	// One below the cap is still fine, so the bound is where it says it is.
	if _, err := Walk(context.Background(), []byte(strings.Repeat("q ", maxGStateDepth)), 0, imageEnv(1)); err != nil {
		t.Errorf("Walk refused %d nested q, which is within the cap: %v", maxGStateDepth, err)
	}
}

// TestWalkRefusesFormInvocationFanOut pins byb-9ml's second half, and it is
// the one maxFormDepth could not catch: DEPTH was bounded at 8 and WIDTH was
// not, so eight chained forms each invoking the next `fan` times cost fan^7
// walks. 505 bytes at fan 10 appended 10,000,000 Paints and reached 5.6 GB
// with a nil error; 3.7 KB at fan 40 is 1.6e11 walks, about sixteen hours.
//
// The fixture below is that shape in miniature -- 8 levels, fan 6, which is
// 6^7 = 279,936 walks uncapped. It is deliberately SMALL: it completes in
// 0.1s without the cap and simply reports a nil error, so this test states
// what it can prove. The 5.6 GB and the sixteen hours are on the bead, not
// here -- a test that reproduced them would be a test that OOMs CI.
func TestWalkRefusesFormInvocationFanOut(t *testing.T) {
	const levels, fan = 8, 6
	env := make(mapEnv, 1)
	env[0] = map[string]XObject{}
	for i := 1; i <= levels; i++ {
		body := "0 0 m f "
		if i < levels {
			body = strings.Repeat("/F"+itoa(i+1)+" Do ", fan)
		}
		env[0]["F"+itoa(i)] = XObject{Content: []byte(body), Scope: 0}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := Walk(ctx, []byte("/F1 Do"), 0, env)
	if err == nil {
		t.Fatal("Walk accepted a form fan-out of 6^7 invocations with no error")
	}
	if !strings.Contains(err.Error(), "form XObject invocations") {
		t.Errorf("error = %v; want it to name the invocation cap (a deadline here means the cap did not fire)", err)
	}
}

// A form invoked many times in a FLAT stream is legitimate -- a repeated
// letterhead or stamp -- so the cap must bound the tree without refusing that.
func TestWalkAllowsFlatFormRepetition(t *testing.T) {
	env := mapEnv{{"F1": {Content: []byte("0 0 m f "), Scope: 0}}}
	src := strings.Repeat("/F1 Do ", maxFormInvocations)
	if _, err := Walk(context.Background(), []byte(src), 0, env); err != nil {
		t.Errorf("Walk refused %d flat form invocations, which is within the cap: %v", maxFormInvocations, err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

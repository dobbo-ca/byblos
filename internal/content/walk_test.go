package content

import (
	"math"
	"strings"
	"testing"
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
	s, err := Walk([]byte("q 612 0 0 792 0 0 cm /Im0 Do Q"), 0, imageEnv(7))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("q 2 0 0 2 10 10 cm q 1 0 0 1 5 5 cm /Im0 Do Q Q"), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want exactly one", s.Images)
	}
	// inner translate(5,5) composed under outer scale(2) translate(10,10)
	boxEq(t, s.Images[0].Box, 20, 20, 22, 22)
}

func TestWalkQRestoresTheCTM(t *testing.T) {
	src := "q 10 0 0 10 0 0 cm /Im0 Do Q /Im0 Do"
	s, err := Walk([]byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Images) != 2 {
		t.Fatalf("Images = %+v; want two", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 10, 10)
	boxEq(t, s.Images[1].Box, 0, 0, 1, 1)
}

// An unbalanced Q must not panic or corrupt the CTM stack.
func TestWalkUnbalancedRestore(t *testing.T) {
	s, err := Walk([]byte("Q Q q 5 0 0 5 0 0 cm /Im0 Do Q Q Q"), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 5, 5)
}

func TestWalkCountsShownCharacters(t *testing.T) {
	src := "BT /F1 12 Tf (Hello) Tj [ (wor) -120 (ld) ] TJ ET"
	s, err := Walk([]byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("BT (ab) ' 1 2 (cde) \" ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("q 0.5 0 0 0.5 0 0 cm /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("q 612 0 0 792 0 0 cm /Im0 Do Q q /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("/ImA Do /Fm0 Do /ImC Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("q /GSa gs /Im0 Do Q /Im0 Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("q /GSa gs /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if s.TextOps != 1 {
		t.Errorf("TextOps = %d; want 1 (the operator is still there)", s.TextOps)
	}
	if s.InkedTextOps != 0 {
		t.Errorf("InkedTextOps = %d; want 0 (3 Tr paints no glyphs)", s.InkedTextOps)
	}
}

func TestWalkCountsVisibleTextAsInked(t *testing.T) {
	s, err := Walk([]byte("BT /F1 12 Tf (Hello) Tj [ (wor) -120 (ld) ] TJ ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
			s, err := Walk([]byte("BT "+tc.mode+" Tr (x) Tj ET"), 0, mapEnv{{}})
			if err != nil {
				t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("BT 3 Tr (hidden) Tj 0 Tr ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if s.TextOps != 1 || s.InkedTextOps != 0 {
		t.Errorf("TextOps = %d, InkedTextOps = %d; want 1, 0", s.TextOps, s.InkedTextOps)
	}

	s, err = Walk([]byte("BT 3 Tr (hidden) Tj 0 Tr (shown) Tj ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if s.TextOps != 2 || s.InkedTextOps != 1 {
		t.Errorf("TextOps = %d, InkedTextOps = %d; want 2, 1", s.TextOps, s.InkedTextOps)
	}
}

// Tr belongs to the text state, which is part of the graphics state, so Q
// restores it (ISO 32000-1 section 8.4.2 and table 104).
func TestWalkRestoresRenderingModeOnQ(t *testing.T) {
	s, err := Walk([]byte("q 3 Tr BT (a) Tj ET Q BT (b) Tj ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if s.TextOps != 2 || s.InkedTextOps != 1 {
		t.Errorf("TextOps = %d, InkedTextOps = %d; want 2, 1", s.TextOps, s.InkedTextOps)
	}
}

// BT/ET resets the text matrix, not the text state. A mode set in one text
// object is still in force in the next.
func TestWalkRenderingModeSurvivesBTET(t *testing.T) {
	s, err := Walk([]byte("BT 3 Tr ET BT (a) Tj ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("q /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("q 3 Tr /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("q /Fm0 Do Q BT (shown) Tj ET"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if s.TextOps != 2 || s.InkedTextOps != 1 {
		t.Errorf("TextOps = %d, InkedTextOps = %d; want 2, 1", s.TextOps, s.InkedTextOps)
	}
}

// The quote operators show text too and must be scored the same way.
func TestWalkQuoteOperatorsRespectRenderingMode(t *testing.T) {
	s, err := Walk([]byte("BT 3 Tr (ab) ' 1 2 (cde) \" ET"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if s.TextOps != 2 || s.InkedTextOps != 0 {
		t.Errorf("TextOps = %d, InkedTextOps = %d; want 2, 0", s.TextOps, s.InkedTextOps)
	}
}

func TestWalkRejectsUnboundedFormRecursion(t *testing.T) {
	env := mapEnv{{"Fm0": {Content: []byte("/Fm0 Do"), Matrix: Identity, Scope: 0}}}
	_, err := Walk([]byte("/Fm0 Do"), 0, env)
	if err == nil {
		t.Fatal("Walk() on a self-referencing form: want an error, got nil")
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
			s, err := Walk([]byte(tc.src), 0, mapEnv{{}})
			if err != nil {
				t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("f S B n"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
			s, err := Walk([]byte(tc.src), 0, mapEnv{{}})
			if err != nil {
				t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("0 0 10 10 re f 100 100 10 10 re f"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Paints) != 2 {
		t.Fatalf("Paints = %+v; want two", s.Paints)
	}
	boxEq(t, s.Paints[0].Box, 0, 0, 10, 10)
	boxEq(t, s.Paints[1].Box, 100, 100, 110, 110)
}

// n discards the path without painting it, and must clear it too.
func TestWalkResetsThePathAfterNoOp(t *testing.T) {
	s, err := Walk([]byte("0 0 500 500 re n 10 10 10 10 re f"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Paints) != 1 {
		t.Fatalf("Paints = %+v; want one", s.Paints)
	}
	boxEq(t, s.Paints[0].Box, 10, 10, 20, 20)
}

// Stroked ink spreads half the line width to either side of the path, in device
// space, so the CTM scales the spread along with everything else.
func TestWalkInflatesAStrokeByHalfItsLineWidth(t *testing.T) {
	s, err := Walk([]byte("10 w 100 100 200 200 re S"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Paints) != 1 {
		t.Fatalf("Paints = %+v; want one", s.Paints)
	}
	boxEq(t, s.Paints[0].Box, 95, 95, 305, 305)

	// Under a 2x CTM the same 10-point pen is 20 device points wide.
	s, err = Walk([]byte("q 2 0 0 2 0 0 cm 10 w 100 100 200 200 re S Q"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	boxEq(t, s.Paints[0].Box, 190, 190, 610, 610)

	// A fill is not inflated: ink stops at the path.
	s, err = Walk([]byte("10 w 100 100 200 200 re f"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	boxEq(t, s.Paints[0].Box, 100, 100, 300, 300)
}

// The default line width is 1.0 (ISO 32000-1 section 8.4.3.2), not zero, so a
// stroke that never sets one still spreads half a point.
func TestWalkStrokeUsesTheDefaultLineWidth(t *testing.T) {
	s, err := Walk([]byte("100 100 200 200 re S"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("/Fm0 Do q 612 0 0 792 0 0 cm /Im0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("/Sh0 sh BI /W 1 /H 1 /BPC 8 /CS /G ID \x00 EI"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if s.ShadingOps != 1 {
		t.Errorf("ShadingOps = %d; want 1", s.ShadingOps)
	}
	if s.InlineImgs != 1 {
		t.Errorf("InlineImgs = %d; want 1", s.InlineImgs)
	}
}

func TestWalkRecordsUnresolvedXObjectNames(t *testing.T) {
	s, err := Walk([]byte("/Missing Do"), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Unresolved) != 1 || s.Unresolved[0] != "Missing" {
		t.Errorf("Unresolved = %v; want [Missing]", s.Unresolved)
	}
}

func TestWalkPropagatesLexerErrors(t *testing.T) {
	if _, err := Walk([]byte("(unterminated"), 0, mapEnv{{}}); err == nil {
		t.Fatal("Walk() on an unterminated string: want an error, got nil")
	}
}

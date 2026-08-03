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
	s, err := Walk([]byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("/Fm0 Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("/Fm0 Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
			s, err := Walk([]byte(tc.src), 0, imageEnv(1))
			if err != nil {
				t.Fatalf("Walk() error = %v", err)
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
		s, err := Walk([]byte("0 0 60 60 re W n /Fm0 Do"), 0, env)
		if err != nil {
			t.Fatalf("Walk() error = %v", err)
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
		s, err := Walk([]byte("/Fm0 Do"), 0, env)
		if err != nil {
			t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte(src), 0, imageEnv(1))
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 500, 500, 500, 500)
}

// A clip must NOT narrow Paint.Box. Walk's doc comment is explicit that an
// oversized Paint.Box is the conservative direction -- it makes a raster less
// likely to be judged as containing (and hence hiding) the paint, so a
// clipped-away path errs toward diverting the page rather than silently
// dropping ink from consideration. Applying the new clip tracking to paints
// too would narrow Paint.Box and move divert decisions the unsafe way, so
// this pins the opposite: a path painted inside a tight clip still reports
// its own full, unclipped device-space box.
func TestWalkClipDoesNotNarrowPaintBoxes(t *testing.T) {
	src := "0 0 10 10 re W n 0 0 500 500 re f"
	s, err := Walk([]byte(src), 0, mapEnv{{}})
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Paints) != 1 {
		t.Fatalf("Paints = %+v; want one", s.Paints)
	}
	boxEq(t, s.Paints[0].Box, 0, 0, 500, 500)
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
	s, err := Walk([]byte("/Fm0 Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("q 3 0 0 3 0 0 cm /Fm0 Do Q"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
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
	s, err := Walk([]byte("/Fm0 Do"), 0, env)
	if err != nil {
		t.Fatalf("Walk() error = %v", err)
	}
	if len(s.Images) != 1 {
		t.Fatalf("Images = %+v; want one", s.Images)
	}
	boxEq(t, s.Images[0].Box, 0, 0, 1000, 1000)
}

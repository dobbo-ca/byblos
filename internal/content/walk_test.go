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
			if s.PaintOps != tc.want {
				t.Errorf("PaintOps = %d; want %d", s.PaintOps, tc.want)
			}
		})
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

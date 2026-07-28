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

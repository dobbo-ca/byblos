package content

import (
	"testing"
)

// straightenMatrix is the exact matrix byblos's own Straighten emits for
// StraightenSpec{Deg: 1.9} on a 612x792 page, rotating about the page centre.
// It is not invented -- byb-2mt's fixtures use the identical numbers so a
// primitive-level failure and a classify-level failure point at the same
// geometry.
var straightenMatrix = Matrix{611.6635, 20.2910, -26.2589, 791.5646, 13.2977, -9.9278}

func TestUnitSquareQuadAxisAlignedMatchesBox(t *testing.T) {
	m := Matrix{612, 0, 0, 792, 0, 0}
	box := m.UnitSquareBox()
	q := m.UnitSquareQuad()
	if !q.ContainsBox(box, 0) {
		t.Errorf("UnitSquareQuad() = %v does not contain its own UnitSquareBox() = %v", q, box)
	}
	// And the reverse: the box, read as a quad in the same ring order, must
	// contain the quad -- an axis-aligned quad IS its bounding box.
	boxQuad := Quad{box.LLX, box.LLY, box.URX, box.LLY, box.URX, box.URY, box.LLX, box.URY}
	if !boxQuad.ContainsQuad(q, 0) {
		t.Errorf("axis-aligned box-as-quad does not contain UnitSquareQuad() = %v", q)
	}
}

func TestQuadContainsBoxRotatedPage(t *testing.T) {
	q := straightenMatrix.UnitSquareQuad()
	page := Box{LLX: 0, LLY: 0, URX: 612, URY: 792}
	if q.ContainsBox(page, 1) {
		t.Errorf("ContainsBox(page) = true, want false: a 1.9deg rotated quad cannot contain the axis-aligned page it grew past")
	}
	// A box centred well inside the rotated quad, small enough that the
	// corner triangles rotation cuts off do not reach it.
	centred := Box{LLX: 200, LLY: 250, URX: 400, URY: 550}
	if !q.ContainsBox(centred, 1) {
		t.Errorf("ContainsBox(centred) = false, want true: %v should sit inside %v", centred, q)
	}
}

func TestQuadWindingBothMirrors(t *testing.T) {
	centred := Box{LLX: 200, LLY: 250, URX: 400, URY: 550}
	for name, m := range map[string]Matrix{
		"horizontal mirror": {-612, 0, 0, 792, 612, 0},
		"vertical mirror":   {612, 0, 0, -792, 0, 792},
	} {
		t.Run(name, func(t *testing.T) {
			q := m.UnitSquareQuad()
			if !q.ContainsBox(centred, 1) {
				t.Errorf("%s: ContainsBox(centred) = false, want true (quad %v)", name, q)
			}
		})
	}
}

func TestQuad90DegreeRotation(t *testing.T) {
	// a and d are zero, b and c are not: a clean quarter turn.
	m := Matrix{0, 792, -612, 0, 612, 0}
	q := m.UnitSquareQuad()
	page := Box{LLX: 0, LLY: 0, URX: 612, URY: 792}
	if !q.ContainsBox(page, 0) {
		t.Errorf("90deg ContainsBox(page) = false, want true: a quarter turn of a 612x792 unit square still exactly covers a 612x792 box (%v)", q)
	}
}

func TestQuadZeroDeterminant(t *testing.T) {
	m := Matrix{0, 0, 0, 0, 0, 0}
	q := m.UnitSquareQuad()
	if q.ContainsBox(Box{LLX: -1, LLY: -1, URX: 1, URY: 1}, 100) {
		t.Error("zero-determinant quad contains a box, want false: a collapsed quad has zero area and can contain nothing")
	}
}

func TestQuadZeroLengthEdge(t *testing.T) {
	// Two coincident vertices produce a zero-length edge; l==0 in
	// containsPoints must return false rather than divide by zero.
	q := Quad{0, 0, 0, 0, 10, 10, 0, 10}
	if q.ContainsBox(Box{LLX: 1, LLY: 1, URX: 2, URY: 2}, 0) {
		t.Error("quad with a zero-length edge contains a box, want false")
	}
}

func TestQuadToleranceAtTheBoundary(t *testing.T) {
	q := Matrix{612, 0, 0, 792, 0, 0}.UnitSquareQuad()
	// A point exactly 2 points outside the right edge: s*cross/l == -2, and
	// the algorithm's own comparison is the strict "< -tol", so tol==2 must
	// pass containment rather than reject it.
	outside := Box{LLX: 0, LLY: 0, URX: 614, URY: 792}
	if !q.ContainsBox(outside, 2) {
		t.Error("ContainsBox at tol exactly matching the shortfall = false, want true (boundary is inclusive: < -tol, not <=)")
	}
	if q.ContainsBox(outside, 1.999) {
		t.Error("ContainsBox at tol just short of the shortfall = true, want false")
	}
}

func TestQuadCollinearVerticesRejectsFarPoint(t *testing.T) {
	// Matrix{300,300,600,600,0,0} has determinant a*d-b*c = 300*600-300*600
	// == 0, but its four vertices are NOT coincident: they are four distinct
	// points on the line y=x. TestQuadZeroDeterminant's all-zero matrix
	// collapses every vertex to one point, which happens to ALSO trip the
	// l==0 zero-length-edge guard on every edge -- it never exercises the
	// twoA==0 guard on its own. This does: twoA==0 with four distinct,
	// collinear vertices, so only the shoelace-area guard is what stands
	// between this and treating a line as an unbounded strip.
	m := Matrix{300, 300, 600, 600, 0, 0}
	q := m.UnitSquareQuad()
	if q.shoelace() != 0 {
		t.Fatalf("test setup: shoelace() = %v, want 0", q.shoelace())
	}
	// Far from the line and from every vertex -- a point the guard-less
	// half-plane loop would still accept, because every edge lies along the
	// same direction and none of them bounds how far a point may sit along
	// that direction.
	far := Box{LLX: 5000, LLY: 5000, URX: 5001, URY: 5001}
	if q.ContainsBox(far, 100) {
		t.Error("collinear (zero-area, non-coincident) quad contains a far box, want false")
	}
}

func TestQuadZeroLengthEdgeRejectsPointInsideTriangle(t *testing.T) {
	// Quad{0,0, 0,0, 10,0, 5,10} repeats its first vertex, so edge 0 (v0->v1)
	// has zero length -- but the other three edges still bound a genuine
	// triangle (0,0)-(10,0)-(5,10), and (5,3) sits inside it. Every OTHER
	// edge's half-plane test passes this point, so it is the l==0 guard
	// alone that must reject it: TestQuadZeroLengthEdge's box sits outside
	// the triangle those three real edges bound, so a caller could remove
	// the guard and still watch that test pass on the real edges alone
	// (byb-2mt review finding F3). This point can't be rejected any other
	// way.
	q := Quad{0, 0, 0, 0, 10, 0, 5, 10}
	if q.shoelace() == 0 {
		t.Fatal("test setup: shoelace() = 0, want a genuine triangle area")
	}
	inside := Box{LLX: 5, LLY: 3, URX: 5, URY: 3}
	if q.ContainsBox(inside, 0) {
		t.Error("quad with a zero-length edge contains a point inside the triangle its other three edges bound, want false")
	}
}

func TestQuadContainsQuadItself(t *testing.T) {
	// tol is a hair above zero rather than exactly zero: every vertex sits on
	// its own boundary, cross == 0 in exact arithmetic, but the matrix
	// multiplications that built q leave float noise on the order of 1e-16
	// that a strict "< 0" comparison can catch as a false rejection. This is
	// the same reason the callers in extract.go always pass a real tolerance
	// (coverTolerancePt, paintTolerancePt, or paintFillTolerancePt), never 0.
	const tol = 1e-6
	q := straightenMatrix.UnitSquareQuad()
	if !q.ContainsQuad(q, tol) {
		t.Errorf("ContainsQuad(itself) = false, want true: every vertex sits (within float noise) on its own boundary")
	}
}

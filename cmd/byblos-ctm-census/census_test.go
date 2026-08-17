package main

import (
	"math"
	"testing"
)

// approxEqual matches to 1e-6, the precision byb-06n's pinned fixture
// (5.767889) was stated to.
func approxEqual(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: got %v, want %v (tol %v)", name, got, want, tol)
	}
}

// TestSkewShearPinnedCases pins byb-06n's four measured separations. Every
// one of them must hold for the shear test the bead is deciding whether to
// add to be trustworthy.
func TestSkewShearPinnedCases(t *testing.T) {
	deg5 := 5.0 * math.Pi / 180

	cases := []struct {
		name      string
		m         [6]float64
		wantShear float64
		wantSkew  float64
		shearTol  float64
	}{
		{
			name:      "rigid 5deg turn",
			m:         [6]float64{math.Cos(deg5), math.Sin(deg5), -math.Sin(deg5), math.Cos(deg5), 0, 0},
			wantShear: 0.0,
			wantSkew:  5.0,
			shearTol:  1e-9,
		},
		{
			name:      "symmetric shear",
			m:         [6]float64{1, math.Tan(deg5), math.Tan(deg5), 1, 0, 0},
			wantShear: 10.0,
			wantSkew:  5.0,
			shearTol:  1e-9,
		},
		{
			name:      "pinned shear fixture",
			m:         [6]float64{612, 0, 80, 792, 0, 0},
			wantShear: 5.767889,
			wantSkew:  5.767889,
			shearTol:  1e-6,
		},
		{
			// THE REFUTATION CASE. A rectangular rigid placement -- a plain
			// axis-aligned scale, |u|/|v| = 1.2642, no rotation and no shear
			// at all -- must report shear ~0. An equal-column-norms test
			// (|u| == |v|) would flag this placement as sheared purely
			// because its width and height differ, which is wrong: nothing
			// about a rectangle being non-square is a shear. This case is
			// what proves shearDegrees is not secretly an equal-norms test.
			name:      "rectangular rigid placement (refutes equal-norms test)",
			m:         [6]float64{1.2642, 0, 0, 1.0, 0, 0}, // |u|/|v| = 1.2642 exactly
			wantShear: 0.0,
			wantSkew:  0.0,
			shearTol:  1e-9,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotShear := shearDegrees(c.m)
			gotSkew := skewDegrees(c.m)
			approxEqual(t, c.name+" shear", gotShear, c.wantShear, c.shearTol)
			approxEqual(t, c.name+" skew", gotSkew, c.wantSkew, c.shearTol)
		})
	}
}

// TestRectangularRigidRatio pins the refutation case's own claim: |u|/|v| is
// really 1.2642, so the case is testing what it says it is.
func TestRectangularRigidRatio(t *testing.T) {
	m := [6]float64{1.2642, 0, 0, 1.0, 0, 0}
	u := math.Hypot(m[0], m[1])
	v := math.Hypot(m[2], m[3])
	ratio := u / v
	approxEqual(t, "ratio", ratio, 1.2642, 1e-9)
}

// TestShearDegenerate pins the documented behaviour for a zero-length
// column: NaN from shearDegrees, true from degenerate.
func TestShearDegenerate(t *testing.T) {
	cases := [][6]float64{
		{0, 0, 80, 792, 0, 0}, // first column zero
		{612, 0, 0, 0, 0, 0},  // second column zero
		{0, 0, 0, 0, 0, 0},    // both zero
	}
	for _, m := range cases {
		if !degenerate(m) {
			t.Errorf("degenerate(%v) = false, want true", m)
		}
		if !math.IsNaN(shearDegrees(m)) {
			t.Errorf("shearDegrees(%v) = %v, want NaN", m, shearDegrees(m))
		}
	}
}

// TestShearNonDegenerate checks a placement with two non-zero columns is
// never reported degenerate, including a perpendicular one.
func TestShearNonDegenerate(t *testing.T) {
	m := [6]float64{612, 0, 0, 792, 0, 0}
	if degenerate(m) {
		t.Errorf("degenerate(%v) = true, want false", m)
	}
	if math.IsNaN(shearDegrees(m)) {
		t.Errorf("shearDegrees(%v) = NaN, want a number", m)
	}
}

// TestShearNeverNaNOnAFiniteNonDegenerateCTM pins the reason shearDegrees is
// spelled with atan2 rather than asin(dot / (|u| |v|)).
//
// Both spellings agree to 2.8e-14 over every placement of the pinned sample, so
// no measurement distinguishes them. What distinguishes them is that the
// product |u| |v| underflows to zero, and overflows to +Inf, for CTMs whose
// columns are finite and whose lengths are not zero -- so degenerate() reports
// false, asin gets 0/0, and the row carries a NaN. json.Encode then fails, the
// sweep aborts, and the output file is left at zero bytes. Twenty nested
// `.0000000001 0 0 .0000000001 0 0 cm` reaches the underflow case, which is
// ordinary PDF syntax rather than a hostile input.
//
// Neither matrix occurs in the pinned sample (0 degenerate and 0 NaN over
// 1,490,723 placements), which is exactly why this needs a test rather than a
// corpus run.
func TestShearNeverNaNOnAFiniteNonDegenerateCTM(t *testing.T) {
	for _, c := range []struct {
		name string
		m    [6]float64
		want float64
	}{
		{"underflow", [6]float64{1e-200, 1e-200, -1e-200, 1e-200, 0, 0}, 90},
		{"overflow", [6]float64{1e200, 0, 1e200, 1e200, 0, 0}, 45},
	} {
		t.Run(c.name, func(t *testing.T) {
			if degenerate(c.m) {
				t.Fatalf("degenerate(%v) = true; the matrix has two non-zero columns", c.m)
			}
			got := shearDegrees(c.m)
			if math.IsNaN(got) {
				t.Fatalf("shearDegrees(%v) = NaN, want %v; a NaN here aborts the "+
					"whole sweep at json.Encode and leaves an empty file", c.m, c.want)
			}
			approxEqual(t, "shear "+c.name, got, c.want, 1e-9)
		})
	}
}

// TestPlacementDeg checks placementDeg against inspect.go's own definition:
// atan2(b,a) in degrees, signed, positive counter-clockwise.
func TestPlacementDeg(t *testing.T) {
	deg5 := 5.0 * math.Pi / 180
	m := [6]float64{math.Cos(deg5), math.Sin(deg5), -math.Sin(deg5), math.Cos(deg5), 0, 0}
	approxEqual(t, "placementDeg", placementDeg(m), 5.0, 1e-9)
}

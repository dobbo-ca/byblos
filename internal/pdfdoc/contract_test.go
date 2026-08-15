package pdfdoc

// Step 5 of the straighten design (docs/superpowers/specs/2026-08-14-straighten-design.md
// section 9): the shared fixture. testdata/straighten/contract.json is the
// contract itself -- neither side of the repository boundary owns it -- so
// these tests READ the file rather than restating its numbers as Go
// literals, and a checked-in contract.pdf pins the file byblos actually
// writes, not only the arithmetic.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"testing"
)

// contractCase is one entry of testdata/straighten/contract.json.
type contractCase struct {
	Name string `json:"name"`
	Page struct {
		CropBox [4]float64 `json:"cropbox"`
		Rotate  int        `json:"rotate"`
	} `json:"page"`
	Apply struct {
		Deg float64 `json:"deg"`
	} `json:"apply"`
	Centre [2]float64 `json:"centre"`
	Expect struct {
		CM [6]float64 `json:"cm"`
	} `json:"expect"`
}

type contractFixture struct {
	Convention string         `json:"convention"`
	Tol        float64        `json:"tol"`
	Cases      []contractCase `json:"cases"`
}

// loadContract reads testdata/straighten/contract.json -- the file IS the
// contract, so a test copying its numbers into a Go literal would no longer
// be testing the shared fixture, only a snapshot of it.
func loadContract(t *testing.T) contractFixture {
	t.Helper()
	data, err := os.ReadFile("../../testdata/straighten/contract.json")
	if err != nil {
		t.Fatalf("reading contract.json: %v", err)
	}
	var c contractFixture
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("parsing contract.json: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("contract.json has no cases")
	}
	return c
}

// blankPageDoc is a minimal one-page document carrying the given CropBox and
// /Rotate and nothing else -- no content stream, because the contract is
// about the wrapper matrix WrapContent produces, not about any existing
// content. TestPageWithoutContentsIsEmptyNotAnError already pins that a page
// with no /Contents is legal, so this is a case this package already
// tolerates on the read side.
//
// Written by hand, like inheritedCropBoxDoc (buildpages_test.go), because
// nothing in this package's own writer produces the arbitrary CropBox and
// Rotate combinations the contract's cases need as SOURCE material.
func blankPageDoc(cropbox [4]float64, rotate int) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [%g %g %g %g] /CropBox [%g %g %g %g] /Rotate %d >>",
			cropbox[0], cropbox[1], cropbox[2], cropbox[3],
			cropbox[0], cropbox[1], cropbox[2], cropbox[3], rotate),
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, start)
	return buf.Bytes()
}

// straightenedMatrix builds a one-page document from a contract case's page
// description, applies its straighten, and returns the wrapper cm.
func straightenedMatrix(t *testing.T, c contractCase) matrix {
	t.Helper()
	d, _ := build(t, []PageSource{{
		Source:     bytes.NewReader(blankPageDoc(c.Page.CropBox, c.Page.Rotate)),
		Page:       1,
		Rotate:     c.Page.Rotate,
		Straighten: &StraightenSpec{Deg: c.Apply.Deg},
	}})
	return wrapMatrix(t, d, 1)
}

// TestStraightenWritesTheContractsMatrix pins the emitted cm against
// contract.json term by term, so a sign flip reports as "cm[1] expected
// -0.0296662, got +0.0296662" rather than as a pixel diff (design spec
// section 9).
func TestStraightenWritesTheContractsMatrix(t *testing.T) {
	c := loadContract(t)
	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			m := straightenedMatrix(t, tc)
			got := [6]float64{m.a, m.b, m.c, m.d, m.e, m.f}
			names := [6]string{"a", "b", "c", "d", "e", "f"}
			for i, want := range tc.Expect.CM {
				if math.Abs(got[i]-want) > c.Tol {
					t.Errorf("cm[%s] = %v, want %v +/- %v", names[i], got[i], want, c.Tol)
				}
			}
		})
	}
}

// TestStraightenContractCentreIsAFixedPoint pins that mapping the contract's
// centre through cm returns the centre exactly (within tol). This catches a
// wrong rotation centre, which a copied constant does not: a translation
// derived for the wrong (cx, cy) can still match the contract's a/b/c/d terms
// while moving the centre.
func TestStraightenContractCentreIsAFixedPoint(t *testing.T) {
	c := loadContract(t)
	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			m := straightenedMatrix(t, tc)
			cx, cy := tc.Centre[0], tc.Centre[1]
			gotX := cx*m.a + cy*m.c + m.e
			gotY := cx*m.b + cy*m.d + m.f
			if math.Abs(gotX-cx) > c.Tol || math.Abs(gotY-cy) > c.Tol {
				t.Errorf("centre %v mapped to (%v, %v), want it fixed at (%v, %v)",
					tc.Centre, gotX, gotY, cx, cy)
			}
		})
	}
}

// TestStraightenContractAngleReadsBackFromAtan2 ties the fixture to the sign
// convention rather than restating it: atan2(b, a) is the exact computation
// ImageRef.PlacementDeg performs (design spec section 8), so this pins that
// reading the emitted matrix back through it recovers the case's "deg".
func TestStraightenContractAngleReadsBackFromAtan2(t *testing.T) {
	c := loadContract(t)
	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			m := straightenedMatrix(t, tc)
			got := math.Atan2(m.b, m.a) * 180 / math.Pi
			if math.Abs(got-tc.Apply.Deg) > c.Tol {
				t.Errorf("atan2(b, a) = %v degrees, want %v (contract's apply.deg)", got, tc.Apply.Deg)
			}
		})
	}
}

// creationDatePattern and idPattern mask the two spans of a pdfcpu-written
// file that are NOT reproducible from one build to the next: crypto.go's
// fileID hashes time.Now() into the trailer's /ID, and the writer stamps
// /CreationDate from the same clock. Both are fixed-width (a 14-digit
// timestamp, two 32-hex-digit IDs), so masking them with an equal-length
// placeholder leaves every other byte -- and every offset -- comparable.
var (
	creationDatePattern = regexp.MustCompile(`D:\d{14}[+-]\d{2}'\d{2}'`)
	idPattern           = regexp.MustCompile(`<[0-9A-Fa-f]{32}>`)
)

// maskVolatileFields replaces the clock-derived spans a pdfcpu build cannot
// reproduce with fixed placeholders, so two builds of the identical input
// compare equal on everything the straighten transform actually controls.
func maskVolatileFields(data []byte) []byte {
	data = creationDatePattern.ReplaceAll(data, []byte(`D:XXXXXXXXXXXXXX+XX'XX'`))
	data = idPattern.ReplaceAll(data, bytes.Repeat([]byte("X"), 34))
	return data
}

// TestContractPDFMatchesWhatByblosWrites regenerates contract.pdf from the
// contract's first case and compares it byte for byte against the checked-in
// file, so the fixture also pins the file byblos produces and not only the
// arithmetic (design spec section 9) -- and the checked-in copy cannot drift
// out from under contract.json unnoticed.
//
// RE-PINNED for byb-yul.6: BuildFromPages now writes its own bytes (884 ->
// 700) instead of pdfcpu's -- no /ID, no pdfcpu-authored /CreationDate or
// /ModDate (maskVolatileFields below is now a no-op on this file, kept for
// the day something else in the pipeline reintroduces either), and a
// /Producer naming byblos instead of pdfcpu. Deliberate, not a silent
// regeneration: verified stable across 20 regenerations of the identical
// input (byte-identical every time) before being checked in.
func TestContractPDFMatchesWhatByblosWrites(t *testing.T) {
	c := loadContract(t)
	tc := c.Cases[0]
	if tc.Name != "flat-page-1.7deg" {
		t.Fatalf("contract.json's first case is %q, want \"flat-page-1.7deg\"", tc.Name)
	}

	var buf bytes.Buffer
	err := BuildFromPages(&buf, []PageSource{{
		Source:     bytes.NewReader(blankPageDoc(tc.Page.CropBox, tc.Page.Rotate)),
		Page:       1,
		Rotate:     tc.Page.Rotate,
		Straighten: &StraightenSpec{Deg: tc.Apply.Deg},
	}})
	if err != nil {
		t.Fatalf("BuildFromPages: %v", err)
	}

	want, err := os.ReadFile("../../testdata/straighten/contract.pdf")
	if err != nil {
		t.Fatalf("reading contract.pdf: %v", err)
	}
	// Masked, not raw: pdfcpu's writer stamps /CreationDate and /ID from
	// time.Now() (crypto.go's fileID, info.go:119) on every build, so a raw
	// byte comparison would fail on every run including a no-op one. Masking
	// those two fixed-width spans still pins everything the straighten
	// transform controls -- the page tree, the wrapper streams, the cm.
	got, wantMasked := maskVolatileFields(buf.Bytes()), maskVolatileFields(want)
	if !bytes.Equal(got, wantMasked) {
		t.Errorf("regenerated contract.pdf differs from the checked-in file (%d bytes got, %d bytes want) -- "+
			"regenerate testdata/straighten/contract.pdf from this build if the change is intentional",
			buf.Len(), len(want))
	}
}

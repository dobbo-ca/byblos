package main

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
	"github.com/dobbo-ca/byblos/internal/sample"
)

// ownerPasswordOnlyFixture and userAndOwnerPasswordFixture are pre-built with
// api.Encrypt against the "mixed" corpus document (see
// internal/pdfdoc/encryptwritepaths_test.go for the live construction --
// pdfcpu itself may only be imported from internal/pdfdoc,
// TestOnlyPdfdocImportsPdfcpu enforces it, so this package reads the bytes
// from testdata instead of building them at run time).
const (
	ownerPasswordOnlyFixture    = "testdata/owner-password-only.pdf"     // empty user PW, owner PW "o"
	userAndOwnerPasswordFixture = "testdata/user-and-owner-password.pdf" // user PW "u", owner PW "o"
)

// TestClassifyOwnerPasswordOnlyIsBucket2 is the shape the bead is about: an
// empty user password, so pdfdoc.Open (and therefore this tool) reads the
// document with no password prompt at all.
func TestClassifyOwnerPasswordOnlyIsBucket2(t *testing.T) {
	d, cleanup := docFromFile(t, ownerPasswordOnlyFixture)
	defer cleanup()

	r := classify(d)
	if r.Bucket != 2 {
		t.Fatalf("bucket = %d, want 2 (err=%q)", r.Bucket, r.Err)
	}
	if r.P == nil || r.V == nil || r.R == nil {
		t.Fatal("bucket 2 row is missing /P, /V or /R")
	}
	if r.InspectOK == nil {
		t.Fatal("bucket 2 row is missing InspectOK")
	}
	if r.Pages == nil || *r.Pages != 2 {
		t.Fatalf("Pages = %v; want 2 (the mixed fixture is two pages)", derefInt(r.Pages))
	}
	// mixed is a born-digital page (no single raster: ErrNotSingleRaster)
	// followed by a scan (one raster), so exactly 1 of 2 pages should count.
	if r.Rasters == nil || *r.Rasters != 1 {
		t.Fatalf("Rasters = %v; want 1", derefInt(r.Rasters))
	}
}

// TestClassifyUserPasswordIsBucket3 covers the shape Open genuinely refuses:
// a real user password, empty candidate. This must land in bucket 3, carrying
// pdfcpu's own wrong-password text, and must NOT be confused with bucket 4.
func TestClassifyUserPasswordIsBucket3(t *testing.T) {
	d, cleanup := docFromFile(t, userAndOwnerPasswordFixture)
	defer cleanup()

	r := classify(d)
	if r.Bucket != 3 {
		t.Fatalf("bucket = %d, want 3 (err=%q)", r.Bucket, r.Err)
	}
	if !strings.Contains(r.Err, wrongPasswordText) {
		t.Errorf("error %q does not contain %q", r.Err, wrongPasswordText)
	}
}

// TestClassifyUsesErrorsIsNotStringMatching proves the bucket 3/4 split is
// classifying by SENTINEL IDENTITY (pdfdoc.IsWrongPassword, errors.Is against
// pkg/pdfcpu.ErrWrongPassword), not by the error's text: an error whose
// message happens to CONTAIN wrongPasswordText verbatim, but which does not
// wrap ErrWrongPassword, is not actually a wrong-password refusal.
func TestClassifyUsesErrorsIsNotStringMatching(t *testing.T) {
	d := sample.Doc{
		Path: "synthetic.pdf",
		Err:  fmt.Errorf("byblos/pdfdoc: read: some other pdfcpu error whose message happens to say %q", wrongPasswordText),
	}
	r := classify(d)
	if r.Bucket != 4 {
		t.Fatalf("bucket = %d, want 4 (err=%q) -- this error does not wrap pdfcpu.ErrWrongPassword, so it must not land in bucket 3 merely because its text matches", r.Bucket, r.Err)
	}
}

// TestClassifyPlainDocumentIsBucket1 is the control: no /Encrypt at all.
func TestClassifyPlainDocumentIsBucket1(t *testing.T) {
	src, ok := corpus.ByName("mixed")
	if !ok {
		t.Fatal("the corpus has no mixed document")
	}
	d, cleanup := docFromBytes(t, src)
	defer cleanup()

	r := classify(d)
	if r.Bucket != 1 {
		t.Fatalf("bucket = %d, want 1 (err=%q)", r.Bucket, r.Err)
	}
	if r.Pages == nil || *r.Pages != 2 {
		t.Fatalf("Pages = %v; want 2 (the mixed fixture is two pages) -- bucket 1 rows carry Pages too, not just bucket 2", derefInt(r.Pages))
	}
}

func derefInt(p *int) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprint(*p)
}

// docFromFile opens path the same way sample.Walk's visit does -- through
// pdfdoc.Open on a real *os.File -- so classify sees exactly the sample.Doc
// shape it sees in the real walk (an open File it can Seek and re-read, and
// Pages set from the same PageCount call visit uses).
func docFromFile(t *testing.T, path string) (d sample.Doc, cleanup func()) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening fixture %s: %v", path, err)
	}
	doc, err := pdfdoc.Open(f)
	d = sample.Doc{Path: path, File: f, Doc: doc, Err: err}
	if err == nil {
		d.Pages = doc.PageCount()
	}
	return d, func() { f.Close() }
}

// docFromBytes is docFromFile for in-memory bytes, for fixtures that need no
// pdfcpu of their own to build (the plain "mixed" corpus document).
func docFromBytes(t *testing.T, b []byte) (d sample.Doc, cleanup func()) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "fixture-*.pdf")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("seeking fixture: %v", err)
	}
	doc, err := pdfdoc.Open(f)
	d = sample.Doc{Path: f.Name(), File: f, Doc: doc, Err: err}
	if err == nil {
		d.Pages = doc.PageCount()
	}
	return d, func() { f.Close() }
}

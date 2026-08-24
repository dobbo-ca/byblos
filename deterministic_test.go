package byblos

// byb-c53: every write that funnels through internal/pdfdoc must be
// byte-deterministic BY DEFAULT. pdfcpu v0.13.0's writer injects two per-run
// stamps (wall-clock CreationDate/ModDate, a time-derived /ID) and emits
// referenced objects in Go map-iteration order, so without byblos holding the
// pen the same input processed twice yields two different files with two
// different hashes — which breaks deduplication and content-addressed storage
// for the archive byblos serves. See internal/pdfdoc/deterministic.go.
//
// These tests run each writing operation twice on identical input and require
// identical bytes; the map-order shuffle and the /ID both drifted on nearly
// every invocation, so a plain byte-compare catches a nondeterministic writer
// quickly. The pinned VALUES (dates, /ID element 0) are asserted in
// internal/pdfdoc/deterministic_values_test.go — they live inside a
// compressed object stream, so checking them takes a pdfcpu re-read, which
// this package must not import (TestOnlyPdfdocImportsPdfcpu).

import (
	"bytes"
	"image"
	"testing"
	"time"
)

// assertStillOpens runs Inspect over data, so a structural break in the
// deterministic writer's output fails here rather than in a downstream
// consumer.
func assertStillOpens(t *testing.T, data []byte) {
	t.Helper()
	if _, err := Inspect(bytes.NewReader(data)); err != nil {
		t.Fatalf("output no longer opens: %v", err)
	}
}

// runTwice runs op twice and requires byte-identical output, returning it.
func runTwice(t *testing.T, op func(w *bytes.Buffer) error) []byte {
	t.Helper()
	var a, b bytes.Buffer
	if err := op(&a); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := op(&b); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		i := 0
		for i < len(a.Bytes()) && i < len(b.Bytes()) && a.Bytes()[i] == b.Bytes()[i] {
			i++
		}
		t.Fatalf("two runs differ: %d vs %d bytes, first difference at offset %d", a.Len(), b.Len(), i)
	}
	return a.Bytes()
}

func TestStampTextLayerIsByteDeterministic(t *testing.T) {
	base := corpusDoc(t, "scan")
	tl := TextLayer{Pages: [][]PositionedWord{
		{{Text: "byb-c53", Bounds: image.Rect(72, 700, 140, 712)}},
	}}
	out := runTwice(t, func(w *bytes.Buffer) error {
		return StampTextLayer(w, bytes.NewReader(base), tl)
	})
	assertStillOpens(t, out)
}

func TestReplaceImagesIsByteDeterministic(t *testing.T) {
	base := corpusDoc(t, "scan")
	pages := inspect(t, "scan")
	objNr := pages[0].Images[0].ObjNr
	sub := map[int]EncodedImage{objNr: {
		Width: 3, Height: 2, BPC: 8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       flateEncode(t, []byte{0, 64, 128, 192, 255, 32}),
	}}
	out := runTwice(t, func(w *bytes.Buffer) error {
		return ReplaceImages(w, bytes.NewReader(base), sub)
	})
	assertStillOpens(t, out)
}

func TestWriteProvenanceIsByteDeterministic(t *testing.T) {
	base := corpusDoc(t, "born-digital")
	// A fixed record: WriteProvenance stores exactly what it is given, so a
	// caller-supplied ProcessedAt is not a source of drift.
	prov := Provenance{Version: Version, ProcessedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	out := runTwice(t, func(w *bytes.Buffer) error {
		return WriteProvenance(bytes.NewReader(base), w, prov)
	})
	assertStillOpens(t, out)
}

func TestOptimizeLinearizeIsByteDeterministic(t *testing.T) {
	// Give the input a provenance record first: on a document with none,
	// Optimize itself minting ProcessedAt=time.Now() into the record is
	// byblos-level drift that no writer pin can (or should) remove -- that
	// field exists to say when processing happened. With provenance already
	// present, Optimize's output is a pure function of its input, and this
	// test requires exactly that, through the deepest write path there is:
	// pdfcpu rewrite, provenance rewrite, then the Annex F linearizer.
	var dated bytes.Buffer
	prov := Provenance{Version: Version, ProcessedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	if err := WriteProvenance(bytes.NewReader(corpusDoc(t, nonPassThroughFixture)), &dated, prov); err != nil {
		t.Fatalf("WriteProvenance: %v", err)
	}
	base := dated.Bytes()
	out := runTwice(t, func(w *bytes.Buffer) error {
		return Optimize(w, bytes.NewReader(base), OptimizeOptions{Linearize: true})
	})
	assertStillOpens(t, out)
}

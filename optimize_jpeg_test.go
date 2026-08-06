package byblos

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// This file is the RED stage of byb-b3's JPEG recompression pass. It pins the
// behaviour design spec section 3 settled: OptimizeOptions.RecompressJPEG no
// longer returns *NotImplemented, the refusal at optimize.go's Optimize is
// gone, and recompression only touches eligible DCTDecode images.
//
// TestOptimizeRecompressJPEGRefused (optimize_test.go) asserts the OPPOSITE
// of what this file requires -- it is the byb-b5-era refusal test, and byb-b3
// deletes it as its own coupled edit. It is left in place here deliberately,
// as a live contradiction: while it still exists, no build can satisfy both
// it and TestOptimizeRecompressJPEGNoLongerRefused below. That is what makes
// this RED rather than merely "new and failing" -- the old test has to be
// removed as part of turning this suite green, exactly as design spec
// section 7's file:line change list requires.

// TestOptimizeRecompressJPEGNoLongerRefused is the direct negation of the
// removed byb-b5 refusal: RecompressJPEG:true must no longer produce a
// *NotImplemented naming "jpeg-recompress".
func TestOptimizeRecompressJPEGNoLongerRefused(t *testing.T) {
	in := corpus.ScanJPEG()
	var out bytes.Buffer
	err := Optimize(&out, bytes.NewReader(in), OptimizeOptions{RecompressJPEG: true, JPEGQuality: 50})
	if err != nil {
		t.Fatalf("Optimize with RecompressJPEG:true: want no error, got %v", err)
	}
	var ni *NotImplemented
	if errors.As(err, &ni) {
		t.Fatalf("Optimize with RecompressJPEG:true still returns *NotImplemented: %v", ni)
	}
}

// TestOptimizeRecompressJPEGShrinksOutput checks the eligible-image case
// end to end: a document whose shared JPEG XObject was encoded at quality
// 95 must come back smaller when recompressed at quality 50.
func TestOptimizeRecompressJPEGShrinksOutput(t *testing.T) {
	in := corpus.ScanJPEG()
	var out bytes.Buffer
	if err := Optimize(&out, bytes.NewReader(in), OptimizeOptions{RecompressJPEG: true, JPEGQuality: 50}); err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if out.Len() >= len(in) {
		t.Errorf("Optimize with RecompressJPEG:true produced %d bytes; want smaller than input's %d", out.Len(), len(in))
	}
}

// TestOptimizeRecompressJPEGRecordsApplied checks both pages painting the
// shared, deduped-by-ID JPEG image record "jpeg-recompress-50" in their
// PageProvenance.Applied -- corpus.ScanJPEG's two pages share a single
// image object, so a correct pass recompresses it once and marks both.
func TestOptimizeRecompressJPEGRecordsApplied(t *testing.T) {
	in := corpus.ScanJPEG()
	var out bytes.Buffer
	if err := Optimize(&out, bytes.NewReader(in), OptimizeOptions{RecompressJPEG: true, JPEGQuality: 50}); err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	prov, err := ReadProvenance(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("ReadProvenance: %v", err)
	}
	if prov == nil {
		t.Fatal("Optimize wrote no provenance")
	}
	if len(prov.Pages) != 2 {
		t.Fatalf("provenance has %d pages; want 2", len(prov.Pages))
	}
	for i, p := range prov.Pages {
		found := false
		for _, a := range p.Applied {
			if a == "jpeg-recompress-50" {
				found = true
			}
		}
		if !found {
			t.Errorf("page %d Applied = %v; want \"jpeg-recompress-50\"", i, p.Applied)
		}
	}
	found := false
	for _, c := range prov.Capabilities {
		if c == "jpeg-recompress" {
			found = true
		}
	}
	if !found {
		t.Errorf("Capabilities = %v; want \"jpeg-recompress\" once >=1 image was substituted", prov.Capabilities)
	}
}

// TestOptimizeRecompressJPEGNonDCTPageIsNoOp checks the non-eligible-document
// case: a document with no JPEG succeeds, is not lossily altered, and no
// page records a jpeg-recompress Applied entry.
//
// This exercises recompressJPEG directly rather than through Optimize:
// corpusDoc(t, "scan") is small enough that Optimize's "never larger than
// input" rule takes the pass-through branch regardless of what recompressJPEG
// does, which would make an assertion against Optimize's output vacuous (it
// never sees the recompressed candidate's provenance at all).
func TestOptimizeRecompressJPEGNonDCTPageIsNoOp(t *testing.T) {
	in := corpusDoc(t, "scan")
	out, applied, err := recompressJPEG(context.Background(), in, 50)
	if err != nil {
		t.Fatalf("recompressJPEG on a non-JPEG document: want no error, got %v", err)
	}
	if applied != nil {
		t.Errorf("recompressJPEG on a document with no JPEG recorded Applied entries: %v", applied)
	}
	if !bytes.Equal(out, in) {
		t.Errorf("recompressJPEG on a document with no JPEG altered the bytes")
	}
}

// TestOptimizeRecompressJPEGSkipsSMask checks an image carrying /SMask is
// left byte-for-byte alone: ReplaceImage refuses /SMask outright, so a
// recompression pass must not even attempt the substitution.
//
// This exercises recompressJPEG directly for the same reason as
// TestOptimizeRecompressJPEGNonDCTPageIsNoOp above: going through Optimize
// hits the pass-through branch, which discards the recompressed candidate's
// provenance and so cannot tell "skipped" from "never attempted".
func TestOptimizeRecompressJPEGSkipsSMask(t *testing.T) {
	in := corpus.ScanSMaskJPEG()
	out, applied, err := recompressJPEG(context.Background(), in, 10)
	if err != nil {
		t.Fatalf("recompressJPEG: %v", err)
	}
	if applied != nil {
		t.Errorf("recompressJPEG recorded Applied entries for an /SMask image, which must be skipped: %v", applied)
	}
	if !bytes.Equal(out, in) {
		t.Errorf("recompressJPEG altered the bytes of a document whose only image carries /SMask")
	}
}

// TestOptimizeRecompressJPEGQualityValidation checks JPEGQuality's 1..100
// range is enforced BEFORE any work, and that 0 is an error rather than a
// default -- design spec section 3's settled precedent (build.go's DPI).
func TestOptimizeRecompressJPEGQualityValidation(t *testing.T) {
	in := corpus.ScanJPEG()
	for _, q := range []int{0, -1, 101, 1000} {
		var out bytes.Buffer
		err := Optimize(&out, bytes.NewReader(in), OptimizeOptions{RecompressJPEG: true, JPEGQuality: q})
		if err == nil {
			t.Errorf("JPEGQuality %d: want error, got nil", q)
		}
		if out.Len() != 0 {
			t.Errorf("JPEGQuality %d: wrote %d bytes despite erroring", q, out.Len())
		}
	}
}

// TestOptimizeRecompressJPEGOutputReReads checks the output document is
// still a valid PDF that Inspect can read after recompression.
func TestOptimizeRecompressJPEGOutputReReads(t *testing.T) {
	in := corpus.ScanJPEG()
	var out bytes.Buffer
	if err := Optimize(&out, bytes.NewReader(in), OptimizeOptions{RecompressJPEG: true, JPEGQuality: 50}); err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	pages, err := Inspect(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("Inspect on recompressed output: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("Inspect found %d pages; want 2", len(pages))
	}
}

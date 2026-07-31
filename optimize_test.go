package byblos

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// This file is the RED stage of byb-b5 (Optimize). Optimize and
// OptimizeOptions do not exist yet, so nothing here compiles. That is the
// point: it fixes the API and its observable behaviour before any
// implementation exists.
//
// Size-policy background (decided on the bead, not reopened here): Optimize
// returns min(input, pdfcpu-optimized-output) and records which branch it
// took on Provenance.Optimized. Measured directly against this corpus
// (candidate = a document carrying a freshly-written Provenance, vs the raw
// input) exactly ONE of the 27 non-malformed corpus documents takes the
// non-pass-through branch once the provenance-record cost is accounted for:
// "dup-raster" (raw 2785 B, pdfcpu 1865 B, candidate 2143 B). Every other
// document, including "indirect-kids" (whose raw pdfcpu.Optimize alone
// shrinks it, 3042 -> 2895), flips back to pass-through once the record's own
// weight is added (3042 vs candidate 3174). "dup-raster" is therefore the
// only non-vacuous fixture available in this corpus for the non-pass-through
// branch, and is used for every non-pass-through assertion below.
//
// The candidate figures move by a few bytes with the record itself: the JSON
// is stored hex-encoded in the Info dictionary, so every byte of it costs two,
// and ProcessedAt is RFC3339Nano, whose length varies with how many trailing
// zeros the timestamp has. They are recorded here as measured, to localise a
// future change, not as constants any assertion depends on.
//
// Validation uses pdfdoc.ReadProperties, not a dedicated pdfdoc.Validate: it
// already calls pdfcpu's api.Properties, which runs ReadValidateAndOptimize
// internally (properties.go) and therefore returns an error for anything
// pdfcpu's validator rejects. That is the existing pdfdoc seam; no new
// pdfcpu-facing surface was added to write this test.

// nonPassThroughFixture is the one corpus document whose Optimize output is
// smaller than its raw input even after the provenance record Optimize must
// write is counted. See the package comment above for the measurement.
const nonPassThroughFixture = "dup-raster"

// passThroughFixture is a corpus document whose pdfcpu-optimized-plus-provenance
// candidate is larger than the raw input, so Optimize must take the
// pass-through branch. born-digital has no images to dedupe or restructure,
// so pdfcpu's rewrite (plus the provenance record's own weight) only grows it.
const passThroughFixture = "born-digital"

// TestOptimizeNeverLargerThanInput is the acceptance criterion from design
// spec section 8, literally: over every real (non-malformed) corpus
// document, Optimize's output is never larger than its input. This holds by
// construction once Optimize takes the smaller of the two candidates; an
// implementation that always returns pdfcpu's rewrite (ignoring the size
// policy) fails it on 26 of these 27 documents.
func TestOptimizeNeverLargerThanInput(t *testing.T) {
	for _, d := range corpus.All() {
		if d.Name == "malformed" {
			continue // covered separately: it must error, not size-compare.
		}
		d := d
		t.Run(d.Name, func(t *testing.T) {
			var out bytes.Buffer
			if err := Optimize(&out, bytes.NewReader(d.Data), OptimizeOptions{}); err != nil {
				t.Fatalf("Optimize: %v", err)
			}
			if out.Len() > len(d.Data) {
				t.Fatalf("Optimize grew %s: in=%d out=%d", d.Name, len(d.Data), out.Len())
			}
		})
	}
}

// TestOptimizeOutputValidates checks every corpus document's Optimize output
// still validates, via the pdfdoc seam described above. A broken
// implementation that emits a truncated or corrupt rewrite -- e.g. one that
// forgets to update /Length after copying the pdfcpu bytes, or one that
// hands back garbage on the pass-through branch -- fails this on the
// affected documents even though TestOptimizeNeverLargerThanInput would not
// catch it (a corrupt document can still be small).
func TestOptimizeOutputValidates(t *testing.T) {
	for _, d := range corpus.All() {
		if d.Name == "malformed" {
			continue
		}
		d := d
		t.Run(d.Name, func(t *testing.T) {
			var out bytes.Buffer
			if err := Optimize(&out, bytes.NewReader(d.Data), OptimizeOptions{}); err != nil {
				t.Fatalf("Optimize: %v", err)
			}
			if _, err := pdfdoc.ReadProperties(bytes.NewReader(out.Bytes())); err != nil {
				t.Fatalf("Optimize output for %s does not validate: %v", d.Name, err)
			}
		})
	}
}

// TestOptimizePassThroughBranch checks the branch taken, and recorded, when
// pdfcpu's rewrite (plus the provenance record) would be larger than the
// input: Optimize must hand back the input BYTE-FOR-BYTE VERBATIM (recon
// section 5 -- any in-band write, including just the branch marker, makes
// the output larger than the input and breaks the literal "never larger"
// criterion), and the branch marker must NOT read back as "rewritten". An
// implementation that always runs pdfcpu's rewrite (dropping the size
// policy) fails this: born-digital's pdfcpu-plus-provenance candidate (1168
// B) is 477 B larger than its 691 B input.
func TestOptimizePassThroughBranch(t *testing.T) {
	in := corpusDoc(t, passThroughFixture)

	var out bytes.Buffer
	if err := Optimize(&out, bytes.NewReader(in), OptimizeOptions{}); err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if !bytes.Equal(out.Bytes(), in) {
		t.Fatalf("pass-through branch must return the input verbatim: in=%d out=%d, bytes differ", len(in), out.Len())
	}

	prov, err := ReadProvenance(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("ReadProvenance: %v", err)
	}
	if prov != nil && prov.Optimized == "rewritten" {
		t.Fatalf("pass-through branch must not record Optimized=%q", "rewritten")
	}
}

// TestOptimizeNonPassThroughBranch checks the branch taken, and recorded,
// when pdfcpu's rewrite plus the provenance record is smaller than the
// input: Optimize must emit the smaller (pdfcpu-rewritten) bytes and record
// Optimized="rewritten" in the result's provenance. An implementation that
// always takes the pass-through branch (or never sets the field) fails
// this. See the package comment for why dup-raster is the only corpus
// document that reaches this branch once the provenance record's own cost
// is counted.
func TestOptimizeNonPassThroughBranch(t *testing.T) {
	in := corpusDoc(t, nonPassThroughFixture)

	var out bytes.Buffer
	if err := Optimize(&out, bytes.NewReader(in), OptimizeOptions{}); err != nil {
		t.Fatalf("Optimize: %v", err)
	}
	if out.Len() >= len(in) {
		t.Fatalf("expected the non-pass-through branch to shrink %s: in=%d out=%d", nonPassThroughFixture, len(in), out.Len())
	}

	prov, err := ReadProvenance(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("ReadProvenance: %v", err)
	}
	if prov == nil {
		t.Fatal("non-pass-through branch produced no provenance record")
	}
	if prov.Optimized != "rewritten" {
		t.Fatalf("Optimized = %q, want %q", prov.Optimized, "rewritten")
	}
}

// TestOptimizeProvenanceSurvivesPassThrough checks that an existing
// provenance record (as a prior Extract call would have written) is
// untouched when Optimize takes the pass-through branch. Trivially true if
// pass-through returns the input verbatim (as required above), but stated
// as its own assertion because "verbatim" and "provenance survives" are
// different claims a change to Optimize could break independently -- e.g. a
// pass-through implementation that re-serializes the input via pdfcpu
// "to be safe" would satisfy neither the byte-identity test above nor this
// one, but a change that broke ONLY provenance forwarding could otherwise
// slip past a size-only check.
func TestOptimizeProvenanceSurvivesPassThrough(t *testing.T) {
	want := Provenance{
		Version:      Version,
		Capabilities: Capabilities(),
		ProcessedAt:  time.Date(2026, 7, 31, 12, 34, 56, 0, time.UTC),
		Pages: []PageProvenance{
			{Applied: []string{"extract-raster"}},
		},
	}
	var withProv bytes.Buffer
	if err := WriteProvenance(bytes.NewReader(corpusDoc(t, passThroughFixture)), &withProv, want); err != nil {
		t.Fatalf("WriteProvenance: %v", err)
	}

	var out bytes.Buffer
	if err := Optimize(&out, bytes.NewReader(withProv.Bytes()), OptimizeOptions{}); err != nil {
		t.Fatalf("Optimize: %v", err)
	}

	got, err := ReadProvenance(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("ReadProvenance: %v", err)
	}
	if got == nil {
		t.Fatal("provenance did not survive the pass-through branch")
	}
	if got.Version != want.Version || !slices.Equal(got.Capabilities, want.Capabilities) ||
		!got.ProcessedAt.Equal(want.ProcessedAt) || len(got.Pages) != len(want.Pages) ||
		!slices.Equal(got.Pages[0].Applied, want.Pages[0].Applied) {
		t.Fatalf("provenance mutated across the pass-through branch: got %+v, want %+v", got, want)
	}
}

// TestOptimizeProvenanceSurvivesNonPassThrough is the bead's one explicitly
// untested claim: does a provenance record survive api.Optimize's rewrite on
// the branch where pdfcpu's bytes are actually used, not merely the
// pass-through branch where the bytes are untouched by construction? Real
// round trip: (a document with a provenance record already in its Info
// dictionary) -> Optimize -> ReadProvenance, comparing fields. If
// api.Optimize drops or rewrites the Info dictionary on this path, this is
// the test that catches it -- TestOptimizeNonPassThroughBranch above only
// checks the branch marker Optimize itself just wrote, not whether an
// EARLIER record survives Optimize's rewrite.
//
// The fixture is built with corpus.DupRasterWithInfo, not WriteProvenance,
// and that is load-bearing, not incidental: WriteProvenance is itself a full
// pdfcpu read-validate-optimize-write pass, so its output is already at
// pdfcpu's fixed point -- a SECOND Optimize call on WriteProvenance's own
// output has nothing left to shrink and always takes the pass-through
// branch (measured: every one of the 27 non-malformed corpus documents,
// pre-loaded with a provenance record via WriteProvenance, takes
// pass-through). DupRasterWithInfo sidesteps that by setting the Info entry
// directly in the trailer, so the input has never been through pdfcpu at
// all, and dup-raster's real image-deduplication saving is still there for
// Optimize's own pdfcpu pass to find.
func TestOptimizeProvenanceSurvivesNonPassThrough(t *testing.T) {
	want := Provenance{
		Version:      Version,
		Capabilities: Capabilities(),
		ProcessedAt:  time.Date(2026, 7, 31, 12, 34, 56, 0, time.UTC),
		Pages: []PageProvenance{
			{Applied: []string{"extract-raster"}},
			{Diverted: "not-single-raster", DroppedAnnots: 3},
		},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	in := corpus.DupRasterWithInfo(provenanceKey, string(data))

	var out bytes.Buffer
	if err := Optimize(&out, bytes.NewReader(in), OptimizeOptions{}); err != nil {
		t.Fatalf("Optimize: %v", err)
	}

	// The non-pass-through branch is the one under test: assert it was
	// actually taken, so a future change that makes Optimize regress to
	// pass-through here fails loudly instead of leaving this test vacuous
	// again.
	if bytes.Equal(out.Bytes(), in) {
		t.Fatalf("fixture no longer exercises the non-pass-through branch: Optimize returned the input verbatim (in=%d)", len(in))
	}
	if out.Len() > len(in) {
		t.Fatalf("Optimize grew the input: in=%d out=%d", len(in), out.Len())
	}

	got, err := ReadProvenance(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("ReadProvenance: %v", err)
	}
	if got == nil {
		t.Fatal("provenance did not survive Optimize")
	}
	if got.Optimized != "rewritten" {
		t.Fatalf("Optimized = %q, want %q (branch marker should confirm the rewritten path)", got.Optimized, "rewritten")
	}
	if got.Version != want.Version || !slices.Equal(got.Capabilities, want.Capabilities) ||
		!got.ProcessedAt.Equal(want.ProcessedAt) || len(got.Pages) != len(want.Pages) ||
		!slices.Equal(got.Pages[0].Applied, want.Pages[0].Applied) ||
		got.Pages[1].Diverted != want.Pages[1].Diverted || got.Pages[1].DroppedAnnots != want.Pages[1].DroppedAnnots {
		t.Fatalf("provenance mutated across Optimize: got %+v, want %+v", got, want)
	}
}

// --- degenerate inputs -------------------------------------------------

func TestOptimizeZeroByteInput(t *testing.T) {
	var out bytes.Buffer
	err := Optimize(&out, bytes.NewReader(nil), OptimizeOptions{})
	if err == nil {
		t.Fatal("Optimize on a zero-byte reader: want an error, got nil")
	}
}

func TestOptimizeNonPDFInput(t *testing.T) {
	var out bytes.Buffer
	err := Optimize(&out, bytes.NewReader([]byte("not a pdf at all, just text\n")), OptimizeOptions{})
	if err == nil {
		t.Fatal("Optimize on non-PDF input: want an error, got nil")
	}
}

// TestOptimizeMalformedInput uses the corpus's deliberately truncated
// document. Optimize must return an error, not panic: an implementation
// that calls straight into pdfcpu without going through pdfdoc's panic
// recovery (ErrMalformed, pdfdoc.go) can crash the process on exactly the
// damaged-file case this library exists to survive (see byb-avp).
//
// A panic here fails the test directly (the test binary crashes the
// process), so there is no separate assertion for "did not panic" beyond
// running Optimize and requiring a plain error return.
func TestOptimizeMalformedInput(t *testing.T) {
	in := corpusDoc(t, "malformed")

	var out bytes.Buffer
	err := Optimize(&out, bytes.NewReader(in), OptimizeOptions{})
	if err == nil {
		t.Fatal("Optimize on the malformed corpus document: want an error, got nil")
	}
}

// TestOptimizeFreshRecordClaimsNoCapabilities checks that a document with no
// prior provenance, once it takes the rewritten branch, does not come out
// with a record claiming Byblos capabilities it never actually applied:
// Optimize itself only rewrites structure, it does not extract or inspect
// anything, so the fresh record it writes must not claim Capabilities as if
// it had.
//
// This does not make UpgradeCandidates(fresh record, ...) report
// "extract-raster"/"inspect" as candidates -- that is a separate, pre-existing
// property of capabilityRules (upgrade.go): both are ruled "never" an
// upgrade, unconditionally, because re-running them cannot change this PDF's
// OUTPUT bytes (they are read-only passes; see the "Inspection and
// extraction do not alter output" comment there). That rule fires the same
// way whether Capabilities is empty or full -- only the special nil-p case
// (an entirely untouched document) bypasses it. Fixing THAT gap, if it is
// one, is a change to upgrade.go's own design, not to what Optimize writes,
// and is out of scope here. What Optimize must not do is misrepresent the
// document's history with a fabricated, false capability list, which is
// what this test guards.
func TestOptimizeFreshRecordClaimsNoCapabilities(t *testing.T) {
	in := corpus.DupRasterWithInfo("unrelated-key", "irrelevant")

	var out bytes.Buffer
	if err := Optimize(&out, bytes.NewReader(in), OptimizeOptions{}); err != nil {
		t.Fatalf("Optimize: %v", err)
	}

	got, err := ReadProvenance(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("ReadProvenance: %v", err)
	}
	if got == nil {
		t.Fatal("expected Optimize to write a fresh provenance record on the rewritten branch")
	}
	if len(got.Capabilities) != 0 {
		t.Fatalf("fresh record claims Capabilities %v, want none", got.Capabilities)
	}
}

// --- OptimizeOptions fields ---------------------------------------------

// TestOptimizeLinearizeRefused checks OptimizeOptions.Linearize's documented
// refusal: pdfcpu strips linearization rather than adding it, so Optimize
// must error rather than silently ignore the request (see the doc comment
// on Linearize).
func TestOptimizeLinearizeRefused(t *testing.T) {
	var out bytes.Buffer
	err := Optimize(&out, bytes.NewReader(corpusDoc(t, passThroughFixture)), OptimizeOptions{Linearize: true})
	if err == nil {
		t.Fatal("Optimize with Linearize:true: want an error, got nil")
	}
	if out.Len() != 0 {
		t.Fatalf("Optimize with Linearize:true wrote %d bytes despite erroring", out.Len())
	}
}

// TestOptimizeRecompressJPEGRefused checks OptimizeOptions.RecompressJPEG's
// documented refusal: this package has no recompression path yet, so
// Optimize must error rather than silently ignore the request, regardless
// of JPEGQuality's value.
func TestOptimizeRecompressJPEGRefused(t *testing.T) {
	var out bytes.Buffer
	err := Optimize(&out, bytes.NewReader(corpusDoc(t, passThroughFixture)), OptimizeOptions{RecompressJPEG: true, JPEGQuality: 50})
	if err == nil {
		t.Fatal("Optimize with RecompressJPEG:true: want an error, got nil")
	}
	if out.Len() != 0 {
		t.Fatalf("Optimize with RecompressJPEG:true wrote %d bytes despite erroring", out.Len())
	}
}

// TestOptimizeCorruptProvenanceFallsBack checks that a byblos-provenance
// Info value that is not valid JSON does not abort Optimize: the document
// is otherwise valid, and refusing to optimize it would give up a free,
// correct pass-through candidate over a key this call did not write. See
// errCorruptProvenance (provenance.go).
func TestOptimizeCorruptProvenanceFallsBack(t *testing.T) {
	in := corpus.DupRasterWithInfo(provenanceKey, "not valid json")

	var out bytes.Buffer
	if err := Optimize(&out, bytes.NewReader(in), OptimizeOptions{}); err != nil {
		t.Fatalf("Optimize on a document with corrupt provenance: want no error, got %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("Optimize on a document with corrupt provenance wrote no bytes")
	}
	if out.Len() > len(in) {
		t.Fatalf("Optimize grew the input: in=%d out=%d", len(in), out.Len())
	}
	if _, err := pdfdoc.ReadProperties(bytes.NewReader(out.Bytes())); err != nil {
		t.Fatalf("Optimize output does not validate: %v", err)
	}
}

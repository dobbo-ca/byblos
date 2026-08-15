package byblos

// BuildFromPages is design spec goal G4: materialise a document from a page
// sequence (byb-yul.4).
//
// It is the whole edit vocabulary in one call. Deleting a page is omitting it
// from the sequence, reordering is ordering the sequence, inserting is naming a
// different Source, and rotating is a field. Byblos never mutates a stored
// document: Kleio holds the sequence and an export builds a new document from
// it, which is what makes an export impossible to half-succeed.
//
// The object-graph migration is internal/pdfdoc's (buildpages.go). This file is
// the G3 half -- carrying each page's provenance record to its NEW index -- and
// it is not a formality. Provenance.Pages is positional with no page identity,
// so an export that left the slice alone would describe every page after the
// first edit wrongly.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// PageSource names one page of one document, and the rotation to give it.
// Aliased from the write seam for the reason EncodedImage is (build.go):
// internal packages are unreachable from Kleio, and a parallel type would be a
// second thing to keep in step.
type PageSource = pdfdoc.PageSource

// StraightenSpec is a lossless rotation of one page's content. Aliased from
// the write seam for the same reason PageSource is (byb-16j.4); see its own
// doc comment in internal/pdfdoc for the sign convention and the
// absolute-not-delta contract BuildFromPagesContext enforces below.
type StraightenSpec = pdfdoc.StraightenSpec

// divertedNotRecorded is the PageProvenance.Diverted reason for a page byblos
// did not process and has no record of -- typically a page imported from a
// document that never went through byblos at all.
//
// IT IS DELIBERATELY OUTSIDE THE EXTRACTION VOCABULARY. divertClass
// (extract.go) emits "not-single-raster" and the "unsupported-codec" family,
// and anyPageDiverted (upgrade.go) matches those by exact string -- so an
// unrecognised reason is inert and nominates nothing. That is the honest state:
// this page was not diverted BY extraction, it was never offered to it.
//
// The zero PageProvenance is not an option. provenance.go:337-339 says it "is
// indistinguishable to any reader from a page that was handled and had nothing
// applied", which is a claim about this page that byblos cannot make.
const divertedNotRecorded = "source-unrecorded"

// BuildFromPages writes a document whose page i is pages[i], and a provenance
// record that describes it.
//
// WHAT AN EXPORT KEEPS, AND WHAT IT DROPS. Each page arrives with its content
// stream, its resources, its annotations, its inherited attributes pushed down,
// and its provenance record moved to its new index. The document's catalog does
// NOT come with it: outlines, page labels, the structure tree, form fields and
// named destinations are dropped, because they describe the page SET or the page
// ORDER and an edit makes them silently wrong. Measured over the pinned sample,
// 61.9% of multi-page documents carry at least one such entry. See the design
// spec's 2026-08-13 amendment.
//
// OUTPUT IS NOT BYTE-STABLE and must not be treated as though it were. Two
// builds of one sequence differ in object numbering and in length. Content-
// address an export -- write it once, under a key derived from its bytes -- and
// never rewrite a key in place: a second write hands a client mid-download a
// torn object and invalidates any checksum taken over the first.
//
// OUTPUT IS NEVER LINEARIZED. The record says "rewritten-delinearized", which is
// what UpgradeCandidates reads to nominate the document for re-linearization.
// Compose with Optimize{Linearize: true} when a linearized export is wanted;
// asking for it here would mean re-opening the document from scratch.
//
// It cannot be cancelled. Use BuildFromPagesContext when the caller has a
// deadline.
func BuildFromPages(w io.Writer, pages []PageSource) error {
	return BuildFromPagesContext(context.Background(), w, pages)
}

// BuildFromPagesContext is BuildFromPages, cancellable at each source and at
// each page of the record (byb-xyn).
//
// CANCELLATION LATENCY: EFFECTIVELY THE WHOLE CALL, and for the same reason
// BuildPDFContext gives. The checked boundary brackets the migration walk and
// its own write, which is now the ONE pdfcpu WRITE pass this makes --
// pdfdoc's BuildFromPagesWithProperties folds the provenance record into the
// same build instead of a second read-validate-optimize-write pass afterwards
// (byb-yul.6, Correction 5; see its own doc comment for why that second pass
// is not just redundant but actively wrong). A second, READ-ONLY pass still
// runs below: pdfdoc.Validate, over the buffered output, restoring the gate
// the old second WRITE pass used to provide as a side effect of piping the
// bytes through api.AddProperties' own ReadValidateAndOptimize. Without it,
// a source pdfcpu's validator refuses -- an unsupported /PresSteps, a
// malformed date, a bad /ShowBookmarks -- built and wrote silently, with a
// nil error, once Correction 5 took the old validating pass away (found in
// review). Validate only reads; it does not reintroduce the dangling-
// reference bug a second WRITE pass caused. Budget for the whole build. A
// cancelled call writes nothing to w. See context.go.
func BuildFromPagesContext(ctx context.Context, w io.Writer, pages []PageSource) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if len(pages) == 0 {
		return fmt.Errorf("byblos: BuildFromPages: no pages")
	}

	// The record is assembled BEFORE the document is built, so that a source
	// whose provenance cannot be read fails the call rather than producing a
	// document with a record that silently describes the wrong pages. It also
	// puts the OLD Straightened.Deg in hand at exactly the moment the
	// enforced-absolute rule needs it (design spec section 2): deltas[i] is
	// the increment straighten actually has to apply to page i, having
	// already subtracted whatever that page's provenance recorded.
	record, deltas, err := buildRecord(ctx, pages)
	if err != nil {
		return err
	}

	// pdfdoc.BuildFromPages knows nothing of provenance and applies whatever
	// angle it is given; the absolute-vs-delta translation has to happen
	// here, the one place that has both the caller's absolute request and
	// the source's prior record. A copy, not a mutation of pages: the
	// caller's slice and its Straighten pointers are not this call's to
	// change.
	adjusted := make([]PageSource, len(pages))
	copy(adjusted, pages)
	for i, p := range pages {
		if p.Straighten == nil {
			continue
		}
		delta := *p.Straighten
		delta.Deg = deltas[i]
		adjusted[i].Straighten = &delta
	}

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("byblos: BuildFromPages: marshal provenance: %w", err)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	var built bytes.Buffer
	if err := pdfdoc.BuildFromPagesWithProperties(&built, adjusted, map[string]string{provenanceKey: string(data)}); err != nil {
		return fmt.Errorf("byblos: BuildFromPages: %w", err)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	// See BuildFromPagesContext's own doc comment: this is the gate Correction
	// 5's removal of the second WRITE pass silently dropped. Refuse here,
	// same as HEAD did, rather than hand the caller bytes pdfcpu's own
	// (relaxed) validator refuses.
	if err := pdfdoc.Validate(bytes.NewReader(built.Bytes())); err != nil {
		return fmt.Errorf("byblos: BuildFromPages: %w", err)
	}
	if _, err := w.Write(built.Bytes()); err != nil {
		return fmt.Errorf("byblos: BuildFromPages: %w", err)
	}
	return nil
}

// buildRecord assembles the exported document's provenance from its sources',
// and the per-page straighten INCREMENT the enforced-absolute rule requires
// (design spec section 2): deltas[i] is p.Straighten.Deg minus whatever page
// i's source provenance already recorded as Straightened.Deg, defaulting to
// zero for a source with no readable record -- the only safe default, being
// what every document byblos has never seen looks like.
func buildRecord(ctx context.Context, pages []PageSource) (rec Provenance, deltas []float64, err error) {
	sources := map[io.ReadSeeker]*Provenance{}
	for _, p := range pages {
		if err := checkContext(ctx); err != nil {
			return Provenance{}, nil, err
		}
		if p.Source == nil {
			// pdfdoc.BuildFromPages refuses this with the message that names
			// the page; do not pre-empt it with a worse one.
			continue
		}
		if _, done := sources[p.Source]; done {
			continue
		}
		// A record that is absent, or is present and unparseable, is "no
		// record" -- the same reading Optimize and RecordExtraction take. A
		// source that cannot be READ at all is a different thing, and
		// pdfdoc.BuildFromPages reports it in a moment with the page number.
		got, err := ReadProvenanceContext(ctx, p.Source)
		if err != nil {
			got = nil
		}
		sources[p.Source] = got
	}

	out := Provenance{
		Version:     Version,
		ProcessedAt: time.Now().UTC(),
		// Output is never linearized; see BuildFromPages.
		Optimized: "rewritten-delinearized",
	}
	out.Capabilities = commonCapabilities(pages, sources)

	deltas = make([]float64, len(pages))
	for i, p := range pages {
		src := sources[p.Source]
		// A source with no record, or one whose Pages slice is too short to
		// reach this page, cannot say what happened to it.
		var pp PageProvenance
		if src == nil || p.Page < 1 || p.Page > len(src.Pages) {
			pp = PageProvenance{Diverted: divertedNotRecorded}
		} else {
			pp = clonePageProvenance(src.Pages[p.Page-1])
		}

		if p.Straighten != nil {
			old := 0.0
			if pp.Straightened != nil {
				old = pp.Straightened.Deg
			}
			deltas[i] = p.Straighten.Deg - old
			pp.Applied = unionSorted(pp.Applied, []string{"straighten"})
			pp.Straightened = &PageStraighten{Deg: p.Straighten.Deg}
		}

		out.Pages = append(out.Pages, pp)
	}
	return out, deltas, nil
}

// commonCapabilities is what EVERY contributing source's build could do.
//
// The intersection, not the union, and the choice matters. Capabilities has no
// per-page form, so a document assembled from two builds can only honestly claim
// what both of them had. Claiming the union would suppress an upgrade
// nomination for a capability one contributing build lacked, and upgrade.go
// takes the opposite bias throughout: "reporting a wasted re-run is cheaper than
// hiding a real upgrade".
//
// One unrecorded source therefore empties the claim. That is the intended
// reading -- an export containing a page nothing is known about is a document
// worth re-examining, not one to vouch for.
func commonCapabilities(pages []PageSource, sources map[io.ReadSeeker]*Provenance) []string {
	var out []string
	first := true
	seen := map[io.ReadSeeker]bool{}
	for _, p := range pages {
		if p.Source == nil || seen[p.Source] {
			continue
		}
		seen[p.Source] = true

		rec := sources[p.Source]
		if rec == nil {
			return nil
		}
		if first {
			out = slices.Clone(rec.Capabilities)
			first = false
			continue
		}
		out = slices.DeleteFunc(out, func(c string) bool {
			return !slices.Contains(rec.Capabilities, c)
		})
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// clonePageProvenance deep-copies one page's record, so the exported document's
// record shares no slice with the source's -- a page named twice in a sequence
// would otherwise appear twice as the same backing array.
func clonePageProvenance(in PageProvenance) PageProvenance {
	out := in
	out.Applied = slices.Clone(in.Applied)
	out.Placement = slices.Clone(in.Placement)
	if in.Geometry != nil {
		g := *in.Geometry
		if in.Geometry.ClipBox != nil {
			clip := *in.Geometry.ClipBox
			g.ClipBox = &clip
		}
		out.Geometry = &g
	}
	if in.Straightened != nil {
		s := *in.Straightened
		out.Straightened = &s
	}
	return out
}

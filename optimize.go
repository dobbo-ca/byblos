package byblos

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// OptimizeOptions controls Optimize's behaviour (design spec section 4).
type OptimizeOptions struct {
	// Linearize requests a linearized ("fast web view") output.
	//
	// pdfcpu v0.13.0 cannot produce this and never will be asked to:
	// OptimizeContext frees linearization hint-table objects on write whenever
	// IsLinearizationObject is true (pdfcpu write.go, deleteRedundantObject),
	// and nothing in its write path ever emits a /Linearized dictionary.
	// Measured directly against pdfcpu's own linearized fixtures: a round trip
	// through pdfcpu's optimize pass STRIPS linearization rather than adding it
	// (bookletTest.pdf 50308 B Linearized=true -> 34531 B Linearized=false;
	// WaldenFull.pdf 3482146 B Linearized=true -> 1942597 B Linearized=false).
	// Byblos therefore owns the Annex F write path itself; see
	// internal/linearize and internal/pdfdoc/linearize.go (byb-1y7).
	//
	// Two consequences a caller has to know about:
	//
	//   - The output is larger than it would have been WITHOUT linearizing:
	//     measured over the 27 readable corpus documents, +649 to +1007 bytes
	//     against the same document rewritten and not linearized, with no
	//     exceptions. Linearization adds a second cross-reference section, a
	//     parameter dictionary and a hint stream, and it forbids the object
	//     streams pdfcpu's rewrite would otherwise use.
	//
	//     Against the INPUT it is usually but not always larger, because
	//     pdfcpu's rewrite can save more than linearization costs: the corpus
	//     spans +7 (dup-raster) to +1007, and 4 of the 49 PDFs in pdfcpu's own
	//     testdata come out smaller than they went in -- WaldenFull.pdf
	//     3482146 -> 2152320, VectorApple.pdf 1062861 -> 812959,
	//     testWithText.pdf 30008 -> 20859, read.go.pdf 254303 -> 254110.
	//     So "never larger than input" would not discard every linearized
	//     output; it would discard MOST of them, unpredictably, which is worse.
	//     The rule is therefore suspended for this branch and for this branch
	//     only -- a pass-through here would be a caller asking to linearize and
	//     silently not getting it, which is the exact failure
	//     Provenance.Optimized exists to make visible.
	//   - Linearization runs LAST. Writing provenance goes through pdfcpu's
	//     writer, which strips linearization, so nothing may re-serialize the
	//     document after this point.
	Linearize bool

	// RecompressJPEG and JPEGQuality name an image-recompression pass this
	// package does not yet implement: pdfcpu has no recompression API, and the
	// only substitution path (pdfdoc.ReplaceImage) needs an encoder this bead
	// did not build. Optimize refuses RecompressJPEG:true with a
	// *NotImplemented naming "jpeg-recompress", for the same reason it refuses
	// Linearize:true -- accepting the field and silently doing nothing would be
	// a promise it cannot keep.
	//
	// JPEGQuality is not validated, because it cannot be reached: every
	// RecompressJPEG:true call is refused before the value is read, and the
	// value is meaningless when RecompressJPEG is false. Whatever implements
	// the pass owns its range check.
	RecompressJPEG bool
	JPEGQuality    int
}

// Optimize writes a structurally-optimized copy of r's PDF to w.
//
// The size and linearization tradeoff (recorded on the bead byb-b5, not
// reopened here): Optimize returns min(input, pdfcpu-rewritten-output).
// Priority is size, and choosing the smaller of the two costs nothing in
// quality -- both candidates are lossless structural rewrites of the same
// document. The real tradeoff is size versus LINEARIZATION: on the documents
// where pdfcpu's rewrite is larger than the input, returning the input means
// a caller who asked to linearize did not get it, SILENTLY. That is why the
// branch taken is recorded on the result's Provenance.Optimized field rather
// than merely logged -- a log line is not visible to a caller inspecting the
// PDF later, and a field on the provenance record is.
//
// Because the pass-through branch returns the input's bytes byte-for-byte
// verbatim (required to satisfy "never larger than input" literally, since
// any in-band write would grow it), it cannot itself record which branch ran.
// Provenance.Optimized's zero value covers that: it means "not known to have
// been rewritten by Optimize", which is what both an unprocessed document and
// a pass-through result share.
//
// A document that reaches the rewritten branch with no prior provenance gets
// a fresh record with an EMPTY Capabilities: Optimize did not extract or
// inspect anything, so it must not claim those capabilities are done (see
// the note further down, and upgrade.go's UpgradeCandidates).
func Optimize(w io.Writer, r io.ReadSeeker, opts OptimizeOptions) error {
	if opts.RecompressJPEG {
		return &NotImplemented{
			Capability: "jpeg-recompress",
			Why:        "pdfcpu has no image recompression API; this needs image/jpeg plus the pdfdoc write seam",
			Issue:      "byb-b3",
		}
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("byblos: optimize: seek: %w", err)
	}
	in, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("byblos: optimize: read: %w", err)
	}

	var rewritten bytes.Buffer
	if err := pdfdoc.Optimize(bytes.NewReader(in), &rewritten); err != nil {
		return fmt.Errorf("byblos: optimize: %w", err)
	}

	// Read whatever provenance the input already carried (pdfcpu's optimize
	// pass preserves the Info dictionary; it only frees a *duplicate* Info
	// object), set the branch marker, and write it back onto the rewritten
	// bytes. This is one pdfcpu round trip in each direction -- optimize, then
	// write provenance -- not two: writing provenance is itself a full
	// pdfcpu read-validate-optimize-write pass, so optimizing twice would only
	// add noise, not change the result.
	prov, err := ReadProvenance(bytes.NewReader(rewritten.Bytes()))
	if err != nil {
		if !errors.Is(err, errCorruptProvenance) {
			return fmt.Errorf("byblos: optimize: %w", err)
		}
		// A pre-existing byblos-provenance value that is not valid JSON is
		// not this call's fault, and refusing to optimize an otherwise-valid
		// PDF over it would give up a free, correct pass-through candidate.
		// Treat it the same as no provenance at all.
		prov = nil
	}
	if prov == nil {
		// This document has no record of ever being processed by Byblos, so
		// it must not claim any Capabilities: Optimize itself only rewrites
		// structure, it does not extract or inspect anything, and a record
		// claiming otherwise is simply false. It also matters for
		// UpgradeCandidates (upgrade.go): today buildCapabilities only lists
		// "extract-raster" and "inspect", whose rules are unconditionally
		// "never" regardless of what Capabilities says, so this has no
		// visible effect yet -- but a fabricated full-capability record here
		// would silently suppress every FUTURE capability's Contains check
		// too, exactly the "reported as fully done" failure UpgradeCandidates'
		// own doc comment warns against.
		prov = &Provenance{
			Version:     Version,
			ProcessedAt: time.Now(),
		}
	}
	switch {
	case opts.Linearize:
		prov.Optimized = "rewritten-linearized"
	case isLinearized(in):
		// The rewrite has just thrown away the input's linearization. Say so:
		// this is the one case where taking the smaller candidate is not free.
		prov.Optimized = "rewritten-delinearized"
	default:
		prov.Optimized = "rewritten"
	}

	var candidate bytes.Buffer
	if err := WriteProvenance(bytes.NewReader(rewritten.Bytes()), &candidate, *prov); err != nil {
		return fmt.Errorf("byblos: optimize: %w", err)
	}

	if opts.Linearize {
		// Last, and nothing may run after it: WriteProvenance above goes
		// through pdfcpu's writer, and so would any further rewrite, which
		// strips linearization rather than preserving it.
		//
		// The never-larger-than-input rule is suspended here, and only here.
		// Measured on this implementation's own output, linearizing a corpus
		// document costs +649 to +1007 bytes over the same document rewritten
		// and not linearized, and +7 to +1007 over the input itself -- so all 27
		// exceed their input and under the rule every one of them would be
		// handed back to the caller un-linearized. Linearization is a
		// correctness requirement for the born-digital path, not a size
		// optimization, so there is nothing to trade.
		var out bytes.Buffer
		if err := pdfdoc.Linearize(bytes.NewReader(candidate.Bytes()), &out); err != nil {
			return fmt.Errorf("byblos: optimize: %w", err)
		}
		if _, err := w.Write(out.Bytes()); err != nil {
			return fmt.Errorf("byblos: optimize: write: %w", err)
		}
		return nil
	}

	// Optimize's acceptance criterion (design spec section 8) holds
	// literally, by construction: whichever candidate is not larger than the
	// input is the one returned, and the input itself is always a valid
	// fallback because it was already read successfully above.
	if candidate.Len() <= len(in) {
		_, err = w.Write(candidate.Bytes())
	} else {
		_, err = w.Write(in)
	}
	if err != nil {
		return fmt.Errorf("byblos: optimize: write: %w", err)
	}
	return nil
}

// linearizationWindow is how much of a file can hold the linearization
// parameter dictionary. ISO 32000-1:2008 Annex F.2.2 requires that dictionary
// to be the FIRST object in the file and to be contained entirely within the
// first 1024 bytes, which is what makes a prefix scan a complete test rather
// than a heuristic: a conforming linearized file cannot hide it further in,
// and a /Linearized key anywhere past this window does not make a file
// linearized.
const linearizationWindow = 1024

// isLinearized reports whether in carries a linearization parameter
// dictionary, i.e. whether the file claims "fast web view".
//
// This deliberately does not parse. The one fact Optimize needs is whether the
// input had the property that the pdfcpu rewrite is about to destroy, and
// Annex F.2.2's placement rule answers that from the first 1024 bytes with no
// xref walk and no dependency on the file being otherwise well-formed --
// which matters, because Optimize must also reach a verdict on documents
// pdfcpu itself refuses to read.
//
// It reports the CLAIM, not its validity: a file with a /Linearized dictionary
// whose hint tables are broken still counts. That is the right reading here.
// Byblos is not the arbiter of whether someone else's linearization was
// correct; it only needs to know whether it is about to take something away.
func isLinearized(in []byte) bool {
	return bytes.Contains(in[:min(len(in), linearizationWindow)], []byte("/Linearized"))
}

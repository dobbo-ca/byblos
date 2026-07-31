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
	// pdfcpu v0.13.0 cannot produce this: OptimizeContext frees linearization
	// hint-table objects on write whenever IsLinearizationObject is true
	// (pdfcpu write.go, deleteRedundantObject), and nothing in its write path
	// ever emits a /Linearized dictionary. Measured directly against pdfcpu's
	// own linearized fixtures: a round trip through pdfcpu's optimize pass
	// STRIPS linearization rather than adding it (bookletTest.pdf 50308 B
	// Linearized=true -> 34531 B Linearized=false; WaldenFull.pdf 3482146 B
	// Linearized=true -> 1942597 B Linearized=false).
	//
	// Optimize therefore refuses Linearize:true with an error rather than
	// silently ignoring it. Silently ignoring it would be exactly the failure
	// the branch-recording on Provenance.Optimized exists to avoid elsewhere
	// in this function: a caller asking for something and not getting it,
	// with nothing to show for the gap.
	Linearize bool

	// RecompressJPEG and JPEGQuality name an image-recompression pass this
	// package does not yet implement: pdfcpu has no recompression API, and
	// the only substitution path (pdfdoc.ReplaceImage) needs an encoder this
	// bead did not build. Optimize refuses RecompressJPEG:true with an error,
	// for the same reason it refuses Linearize:true -- accepting the field
	// and silently doing nothing would be a promise it cannot keep.
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
	if opts.Linearize {
		return errors.New("byblos: optimize: linearize is not supported by pdfcpu v0.13.0 (it strips linearization on write rather than adding it)")
	}
	if opts.RecompressJPEG {
		return errors.New("byblos: optimize: recompress jpeg is not yet implemented")
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
	prov.Optimized = "rewritten"

	var candidate bytes.Buffer
	if err := WriteProvenance(bytes.NewReader(rewritten.Bytes()), &candidate, *prov); err != nil {
		return fmt.Errorf("byblos: optimize: %w", err)
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

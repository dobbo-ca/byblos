package byblos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"slices"
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
	//     measured over the 35 readable corpus documents, +649 to +2071 bytes
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

	// RecompressJPEG re-encodes every DCTDecode image XObject at JPEGQuality
	// and substitutes it through pdfdoc.ReplaceImage. It is lossy and it is
	// the only lossy thing Optimize does.
	//
	// It touches only images pdfcpu renders as file type "jpg" -- DCTDecode
	// with at most three components -- whose /ColorSpace is the name
	// /DeviceGray or /DeviceRGB and which carry no /SMask, /Mask or
	// /ImageMask. Everything else is left byte-for-byte alone, with no error:
	// a page with no JPEG on it is not an ineligible document, it is a page
	// with no JPEG on it. Which pages were actually re-encoded is recorded as
	// "jpeg-recompress-<quality>" in each page's PageProvenance.Applied.
	//
	// A re-encoded stream is substituted only when it is strictly smaller than
	// the one it replaces, so this pass cannot grow a document and Optimize's
	// "never larger than input" rule needs no suspension for it (unlike
	// Linearize above).
	//
	// JPEGQuality must be 1..100 and Optimize validates it. image/jpeg would
	// silently clamp instead, and a caller who passed 150 would get 100 and no
	// way to know. Zero is an error rather than a default, for the same reason
	// BuildPage.DPI's zero is: guessing how much of someone's image to throw
	// away is not a default anyone can audit.
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
// It cannot be cancelled. Use OptimizeContext when the caller has a deadline.
func Optimize(w io.Writer, r io.ReadSeeker, opts OptimizeOptions) error {
	return OptimizeContext(context.Background(), w, r, opts)
}

// OptimizeContext is Optimize, cancellable between its stages and inside the
// JPEG recompression pass (byb-xyn).
//
// CANCELLATION LATENCY: A WHOLE PDFCPU ROUND TRIP, and on the default options
// that is essentially the whole call. Optimize's work is four pdfcpu passes --
// pdfdoc.Optimize, ReadProvenance, WriteProvenance, and optionally Linearize --
// none of which is interruptible. What makes the default path cancellable at
// all is that the checks sit BETWEEN those passes, so the latency is one pass
// and not the whole call; with RecompressJPEG false there is no finer boundary
// than that. With RecompressJPEG true the recompression pass adds per-page and
// per-image boundaries that ARE checked, so a context cancelled during
// recompression is honoured within one image's re-encode.
//
// A caller that needs Optimize to stop promptly does not have that option
// today; it must budget for the document's full rewrite. A cancelled call
// writes nothing to w. See context.go.
func OptimizeContext(ctx context.Context, w io.Writer, r io.ReadSeeker, opts OptimizeOptions) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if opts.RecompressJPEG && (opts.JPEGQuality < 1 || opts.JPEGQuality > 100) {
		return fmt.Errorf("byblos: optimize: JPEGQuality %d is outside 1..100 (75 is a reasonable default)", opts.JPEGQuality)
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("byblos: optimize: seek: %w", err)
	}
	origIn, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("byblos: optimize: read: %w", err)
	}
	in := origIn

	// applied maps a 0-based page index to the "jpeg-recompress-<quality>"
	// entries it earned. It is built from the ORIGINAL bytes, but discarded
	// along with the recompressed candidate if the final "never larger than
	// input" comparison below falls back to origIn -- see that branch.
	var applied map[int][]string
	if opts.RecompressJPEG {
		in, applied, err = recompressJPEG(ctx, in, opts.JPEGQuality)
		if err != nil {
			return fmt.Errorf("byblos: optimize: %w", err)
		}
	}

	// Checked HERE, before the rewrite, and deliberately not again after it.
	// This boundary is the one that pays: recompressJPEG above can run for a
	// long time on an image-heavy document, and without this a context that
	// expired during it would still launch a whole pdfcpu rewrite pass before
	// anything noticed. A second check after pdfdoc.Optimize would be dead
	// code -- ReadProvenanceContext below opens with the identical check, and
	// deleting a post-rewrite check was measured to change no test at all.
	if err := checkContext(ctx); err != nil {
		return err
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
	prov, err := ReadProvenanceContext(ctx, bytes.NewReader(rewritten.Bytes()))
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
	if len(applied) > 0 {
		for idx, entries := range applied {
			for len(prov.Pages) <= idx {
				prov.Pages = append(prov.Pages, PageProvenance{})
			}
			prov.Pages[idx].Applied = append(prov.Pages[idx].Applied, entries...)
		}
		if !slices.Contains(prov.Capabilities, "jpeg-recompress") {
			prov.Capabilities = append(prov.Capabilities, "jpeg-recompress")
			slices.Sort(prov.Capabilities)
		}
	}
	switch {
	case opts.Linearize:
		prov.Optimized = "rewritten-linearized"
	case isLinearized(origIn):
		// The rewrite has just thrown away the input's linearization. Say so:
		// this is the one case where taking the smaller candidate is not free.
		prov.Optimized = "rewritten-delinearized"
	default:
		prov.Optimized = "rewritten"
	}

	var candidate bytes.Buffer
	if err := WriteProvenanceContext(ctx, bytes.NewReader(rewritten.Bytes()), &candidate, *prov); err != nil {
		return fmt.Errorf("byblos: optimize: %w", err)
	}

	if opts.Linearize {
		// Last, and nothing may run after it: WriteProvenance above goes
		// through pdfcpu's writer, and so would any further rewrite, which
		// strips linearization rather than preserving it.
		//
		// The never-larger-than-input rule is suspended here, and only here.
		// Measured on this implementation's own output, linearizing a corpus
		// document costs +649 to +2071 bytes over the same document rewritten
		// and not linearized, and +7 to +1007 over the input itself -- so all
		// 35 readable corpus documents exceed their input, and under the rule
		// every one of them would be handed back to the caller
		// un-linearized. Linearization is a
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
	// ORIGINAL input is the one returned, and the input itself is always a
	// valid fallback because it was already read successfully above. This
	// compares against origIn, not the (possibly recompressed) in: on the
	// rare document where recompression's own structural noise pushes the
	// candidate over the original file size, the fallback below returns that
	// original file verbatim, and the jpeg-recompress Applied entries --
	// which live only in the discarded candidate's provenance -- are
	// correctly lost along with it.
	if candidate.Len() <= len(origIn) {
		_, err = w.Write(candidate.Bytes())
	} else {
		_, err = w.Write(origIn)
	}
	if err != nil {
		return fmt.Errorf("byblos: optimize: write: %w", err)
	}
	return nil
}

// recompressJPEG re-encodes every eligible DCTDecode image XObject in in at
// quality, once per distinct object even when several pages share it, and
// returns the resulting document alongside which 0-based page indices earned
// a "jpeg-recompress-<quality>" Applied entry.
//
// When nothing in the document was actually substituted -- no JPEG at all, or
// every candidate found was not eligible or not smaller -- in is returned
// unchanged and applied is nil, without running it through a pdfcpu
// Open/Write round trip it does not need.
func recompressJPEG(ctx context.Context, in []byte, quality int) ([]byte, map[int][]string, error) {
	d, err := pdfdoc.Open(bytes.NewReader(in))
	if err != nil {
		return nil, nil, fmt.Errorf("byblos: optimize: recompress: open: %w", err)
	}

	n := d.PageCount()
	pageImageIDs := make([][]int, n)
	for i := 1; i <= n; i++ {
		if err := checkContext(ctx); err != nil {
			return nil, nil, err
		}
		_, scan, err := inspectPage(d, i)
		if err != nil {
			return nil, nil, fmt.Errorf("byblos: optimize: recompress: page %d: %w", i, err)
		}
		seen := map[int]bool{}
		for _, pl := range scan.Images {
			if seen[pl.ID] {
				continue
			}
			seen[pl.ID] = true
			pageImageIDs[i-1] = append(pageImageIDs[i-1], pl.ID)
		}
	}

	entry := fmt.Sprintf("jpeg-recompress-%d", quality)
	substituted := map[int]bool{} // object id -> was it actually replaced
	applied := map[int][]string{}
	any := false
	for i := 0; i < n; i++ {
		pageApplied := false
		for _, id := range pageImageIDs[i] {
			if err := checkContext(ctx); err != nil {
				return nil, nil, err
			}
			did, seen := substituted[id]
			if !seen {
				did, err = recompressOneImage(d, id, quality)
				if err != nil {
					return nil, nil, err
				}
				substituted[id] = did
			}
			if did && !pageApplied {
				applied[i] = append(applied[i], entry)
				pageApplied = true
				any = true
			}
		}
	}
	if !any {
		return in, nil, nil
	}

	var out bytes.Buffer
	if err := d.Write(&out); err != nil {
		return nil, nil, fmt.Errorf("byblos: optimize: recompress: write: %w", err)
	}
	return out.Bytes(), applied, nil
}

// recompressOneImage re-encodes the image XObject id at quality and
// substitutes it via pdfdoc.ReplaceImage, reporting whether it did.
//
// Eligibility (design spec byb-b3 section 3): pdfcpu must render it as file
// type "jpg" (DCTDecode, at most three components), its /ColorSpace must be
// the device name /DeviceGray or /DeviceRGB, and it must carry no /SMask,
// /Mask, /ImageMask or /Decode -- ReplaceImage refuses the first three
// outright, and jpeg.Decode ignores /Decode entirely while ReplaceImage drops
// it from the substituted stream, which would invert a legal
// /Decode [1 0 ...] image. Everything ineligible is reported as "not
// substituted", not an error: a page that does not want recompression is not
// a broken page.
func recompressOneImage(d pdfdoc.Doc, id int, quality int) (bool, error) {
	info, ok := d.ImageInfo(id)
	if !ok {
		return false, nil
	}
	if info.SMask || info.Mask || info.ImageMask || info.Decode {
		return false, nil
	}
	switch info.ColorSpace {
	case "DeviceGray", "DeviceRGB":
	default:
		return false, nil
	}

	data, fileType, err := d.RawImage(id)
	if err != nil {
		if errors.Is(err, pdfdoc.ErrUnsupportedCodec) {
			return false, nil
		}
		return false, fmt.Errorf("byblos: optimize: recompress: image %d: %w", id, err)
	}
	if fileType != "jpg" {
		return false, nil
	}

	// A JPEG image/jpeg cannot decode -- arithmetic coding, 12-bit precision,
	// lossless JPEG, a truncated scan -- is ineligible, not broken. gs and
	// poppler tolerate encodings Go's decoder does not, and one such image
	// must not fail the whole document (see RecompressJPEG's doc comment:
	// "Everything else is left byte-for-byte alone, with no error").
	src, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return false, nil
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: quality}); err != nil {
		return false, fmt.Errorf("byblos: optimize: recompress: encode image %d: %w", id, err)
	}
	// The whole reason this rule is not suspended for the document-level
	// "never larger than input" guarantee (see Optimize): a candidate that
	// did not shrink is simply not applied.
	if buf.Len() >= len(data) {
		return false, nil
	}

	cs := "DeviceRGB"
	if _, ok := src.(*image.Gray); ok {
		cs = "DeviceGray"
	}
	if err := d.ReplaceImage(id, pdfdoc.EncodedImage{
		Width:      info.Width,
		Height:     info.Height,
		BPC:        8,
		ColorSpace: pdfdoc.ColorSpace{Name: cs},
		Filter:     "DCTDecode",
		Data:       buf.Bytes(),
	}); err != nil {
		return false, fmt.Errorf("byblos: optimize: recompress: replace image %d: %w", id, err)
	}
	return true, nil
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

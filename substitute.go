package byblos

// ReplaceImages is the write seam byb-fp6 asks for: the route by which bytes
// one of Byblos' encoders produced get back into an EXISTING PDF. Without it
// Downsample, QuantizeIndexed and EncodeJBIG2Generic have nowhere to put what
// they produce -- internal/pdfdoc has done the substitution since byb-0he, but
// on an unexported receiver, so nothing outside the module could reach it.
//
// It is a general primitive rather than per-capability OptimizeOptions fields
// (decided on byb-fp6, option P). The caller chooses which images to re-encode
// and with what; the preset ladder that decides is orchestration policy and
// lives above this library. Three measurements drove that: a single
// DownsampleDPI field cannot express a ladder that carries a separate mono DPI,
// the ordering of a substitution against OCR is a cross-stage decision only the
// caller can sequence, and one XObject shared by N pages has no single "source
// DPI" for Byblos to pick.

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// ReplaceImages copies r's PDF to w with each named image XObject's stream and
// dictionary replaced by the encoded image given for it. Keys are
// ImageRef.ObjNr, from Inspect; an object number this document has no image
// for is an error, not a silently skipped substitution.
//
// It is per-OBJECT, which is what ImageRef.ObjNr's doc comment warns about
// from the other side: one XObject painted on several pages is substituted
// once and changes all of them.
//
// Placement is untouched. A page positions an image with a CTM on the unit
// square, so a raster with different pixel dimensions lands in exactly the same
// rectangle at a different resolution; that is what makes downsampling a
// substitution rather than a re-layout.
//
// What it refuses, inherited from the seam and not re-derived here
// (internal/pdfdoc/write.go): an image carrying /SMask or /Mask, whose
// transparency is keyed to the samples being replaced; an /ImageMask stencil,
// whose dictionary is a different shape; an image stream that is a direct
// object, which has no cross-reference entry to write back to. A stale /Decode
// array is dropped, because a leftover [1 0] would silently invert the new
// samples. The first failure aborts the whole call and nothing is written --
// a partly substituted document would open cleanly and be wrong.
//
// What it does NOT do, all of it deliberate:
//
//   - It does not check that Data decodes to Width x Height samples. That is
//     the encoder's contract (EncodedImage), and this seam carries JBIG2 and
//     JPX bytes Byblos itself cannot decode.
//   - It does not compare sizes. A substitution that grows the document is
//     written, because whether that trade is worth making depends on what the
//     caller was buying -- Optimize's "never larger than input" rule is
//     Optimize's, and applying it here would silently discard, for instance, a
//     bitonal re-encode a caller asked for on purpose.
//   - It writes no provenance, the same as StampTextLayer and BuildPDF. Under
//     option P it CANNOT: the Applied vocabulary names the capability that ran
//     ("downsample-150", "jbig2-generic"), and the same call substitutes bytes
//     from any encoder or none. The caller records what it applied, through
//     RecordExtraction and WriteProvenance.
//
// That last point has a sharp edge worth stating: a provenance record the
// document already carried survives this call unchanged, and a substitution can
// make it stale. Replacing a raster with JBIG2 turns a page that used to
// extract into one ExtractPageRaster reports ErrUnsupportedImageCodec for,
// while the old record still says "extract-raster". Re-run RecordExtraction
// after substituting if the record has to stay true. Page GEOMETRY does not go
// stale that way -- it is measured in points, and the placement did not move.
// It cannot be cancelled. Use ReplaceImagesContext when the caller has a
// deadline.
func ReplaceImages(w io.Writer, r io.ReadSeeker, subs map[int]EncodedImage) error {
	return ReplaceImagesContext(context.Background(), w, r, subs)
}

// ReplaceImagesContext is ReplaceImages, cancellable at each page boundary of
// the resolving walk and at each substitution (byb-xyn).
//
// CANCELLATION LATENCY: one page's walk during the walk phase, or one image's
// substitution during the substitution phase -- but the final d.Write is a
// single uninterruptible pdfcpu round trip, so a context cancelled once the
// write has begun is not noticed until the document is fully serialized. A
// cancelled call writes nothing to w. See context.go.
func ReplaceImagesContext(ctx context.Context, w io.Writer, r io.ReadSeeker, subs map[int]EncodedImage) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if len(subs) == 0 {
		return fmt.Errorf("byblos: replace images: no substitutions")
	}
	d, err := pdfdoc.Open(r)
	if err != nil {
		return fmt.Errorf("byblos: replace images: %w", err)
	}
	// An image is only substitutable once the document has resolved it, and
	// they are resolved by walking a page's content stream. Every page is
	// walked, not just the ones a caller is substituting on: nothing else says
	// which page paints a given object, and one object may be painted on
	// several. A page that cannot be read at all fails the call, matching
	// RecordExtraction -- there is no way to substitute into half a document
	// and report that honestly.
	for n := 1; n <= d.PageCount(); n++ {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if _, _, err := inspectPage(d, n); err != nil {
			return fmt.Errorf("byblos: replace images: %w", err)
		}
	}
	// Sorted, so a call with two unsubstitutable images names the same one
	// every time.
	for _, objNr := range slices.Sorted(maps.Keys(subs)) {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if err := d.ReplaceImage(objNr, subs[objNr]); err != nil {
			return fmt.Errorf("byblos: replace images: %w", err)
		}
	}
	if err := d.Write(w); err != nil {
		return fmt.Errorf("byblos: replace images: %w", err)
	}
	return nil
}

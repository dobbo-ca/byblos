package byblos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"strings"

	_ "image/jpeg"
	_ "image/png"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// ErrNotSingleRaster reports a page that is not one visible image: tiled
// rasters, visible vector content, or image-plus-overlay. The wrapped message
// names the specific reason.
//
// How much of the page that one image covers is not part of the test, because
// nothing in the content stream can mark the rest of it (byb-b1.3).
// PageRaster.CoversPage is what tells the caller. Callers divert such documents for review;
// design spec section 2 explains why detecting rather than rendering is the
// whole reason this project is tractable.
//
// "Visible" is doing work in that sentence. A path painted before the raster
// and inside its placement box marks nothing anyone can see, and byb-b1.5
// measured 126 scan-shaped pages diverting on exactly that. See paintsHidden.
var ErrNotSingleRaster = errors.New("byblos: page is not a single raster")

// ErrUnsupportedImageCodec reports a page raster stored in a codec Byblos
// cannot decode: JBIG2, JPEG 2000, or CMYK images pdfcpu re-renders as TIFF.
//
// This error exists because of a specific correctness trap: pdfcpu does not
// error on JBIG2Decode or JPXDecode, it returns the raw opaque bytes. Handing
// those to an image decoder would either fail obscurely or, worse, appear to
// work. Byblos names the case instead.
var ErrUnsupportedImageCodec = errors.New("byblos: page raster uses an image codec byblos cannot decode")

// coverTolerancePt is how far a placement may fall short of the page box and
// still count as page-covering. One point at 300 DPI is about four pixels.
//
// This constant is an engineering choice made against a synthetic corpus. If
// the divert-rate instrumentation shows "not-page-covering" is a common reason
// on real scans, revisit it here before revisiting the design.
const coverTolerancePt = 1.0

// maxSkewDeg is how far a placement's axes may lie from the page's before the
// image is treated as rotated or sheared.
//
// It is an angle rather than a matrix entry for a reason recorded in byb-b1.2:
// the tolerance used to be 1e-6 compared against the off-diagonal terms, which
// are in points. At a page scale of 560 that is an exact-zero test — about 1e-7
// degrees — so it rejected all 147 sub-degree scanner deskews in the
// measurement. Two degrees clears the widest of them (1.09) with room to spare
// and stays nowhere near a quarter turn.
const maxSkewDeg = 2.0

// skewDegrees returns how far the placement's axes lie from the page's axes, in
// degrees, taking the larger of the two: zero for an axis-aligned placement, the
// rotation angle for a rotation, and the worse of the two for a shear.
//
// Each axis is measured against its own scale term, so the answer does not
// depend on the size of the raster or on the two axes being scaled alike. Signs
// are dropped, which keeps a mirror at zero: a mirrored placement is square to
// the page and classify catches it by the sign of the scale terms instead.
func skewDegrees(m content.Matrix) float64 {
	x := math.Atan2(math.Abs(m[1]), math.Abs(m[0]))
	y := math.Atan2(math.Abs(m[2]), math.Abs(m[3]))
	return max(x, y) * 180 / math.Pi
}

// mrcPatchAreaFrac is how much of the page a non-bitonal placement must cover
// before it counts as an MRC patch rather than a stamp or a logo.
//
// It is not clear of noise, and it is load-bearing. Measured over the pinned
// sample at ~/work/dobbo-ca/.byblos-sample -- 5,672 sample files and
// 169,376 sample pages -- by walking every page and recording each non-bitonal
// placement's area fraction on the 742 pages that carry a qualifying bitonal
// base. The page counts it yields are reproducible with the shipped tool:
//
//	byblos-divert -json ~/work/dobbo-ca/.byblos-sample/anchors  # mrc-layers: 176
//	byblos-divert -json ~/work/dobbo-ca/.byblos-sample/dc       # mrc-layers:  42
//
// govdocs1 and ia contribute nothing: no page in either has an MRC base. So
// 218 pages across 5 files reach mrc-layers. Their patches run 0.020194 to
// 1.000000 of the page box, median 0.2481 -- so "about 84%" describes the
// dominant patch, not the distribution, and the low end sits 0.97% above this
// floor. That single page is ia-municipaldocume00masgoog.pdf p99: a 146x156
// 8-bit raster placed at 70.08 x 74.88 pt, an inset region under the same JXi0
// resource name as the page-sized patches. Raise the floor and it stops being
// mrc-layers.
//
// Below the floor is not empty either. The same file's p101 (0.017130), p389
// (0.015151, 0.006809) and p397 (0.009054) carry JXi0/JXi1 insets over the
// same J2i0 bitonal base (94.61-94.78%) and classify multiple-images today,
// which is the mislabel byb-b1.6 is about. The floor cuts through a continuous
// run of real MRC insets; the nearest thing that is genuinely a stamp is 6.6x
// further down -- dc-28519909.pdf's greyscale form stamps, 0.003016 and below.
//
// It stays at 0.02 anyway. Moving it would relabel 3 of 169,376 sample pages,
// all in one file, and every one of them diverts either way -- and both sides
// of the gap rest on a single document each, which is not enough to place a
// threshold with. Whether those pages need both layers is a pixel question (is
// the base blank?), not a geometry one. TestClassifyMRCPatchFloorBracket pins
// both ends.
const mrcPatchAreaFrac = 0.02

// mrcBaseAreaFrac is how much of the page a bitonal placement must cover to be
// the base layer of an MRC page.
//
// It is an area fraction and not covers(), because the measured bases are not
// page-covering by that test: Google Books places the base at its own
// resolution, so it falls about ten points short on every edge and covers 94.7%
// of the page. A covers()-based test recognises only an idealised base and is
// dead code on the file this guard exists for. 90% is the threshold the
// prevalence measurement used.
const mrcBaseAreaFrac = 0.90

// PageRaster is a page's raster and where it sits on the page.
//
// It exists because the raster is not always the whole page. byb-b1.3 measured
// 132 pages across 17 files whose raster is placed at its own resolution on a
// nominal Letter box — 2384x3321 pixels at 302 DPI is 568.37 x 791.76 points on
// a 612x792 MediaBox — and those pages extract. Returning a bare image.Image
// for them would quietly hand a caller 91.74% of a page as if it were the page.
//
// Bounds and Page are both in PDF default user space: points, origin
// lower-left, y increasing upward, the same convention as PageInfo.Bounds.
// image.Rectangle is used only as a convenient integer rectangle — do not read
// it as screen coordinates.
//
// The residual affine of a deskewed placement (byb-b1.2) is not here; it is on
// ImageRef.Placement, reached through Inspect. byb-b5.1 designs the stored form
// of both.
//
// Since byb-b1.12, Bounds is where the raster is VISIBLE — the placement's own
// extent intersected with any W/W* clip path or form /BBox in effect — and can
// be smaller than the placement's own unclipped extent. Image is always the
// full, uncropped raster as stored (Byblos never crops pixel data): a clip
// narrows where Bounds says the raster's visible mark falls, not what Image
// contains. A caller that assumed Bounds was simply Image's own placement box
// must not, now that a clipped page reports the narrower, honest rectangle
// instead. ImageRef.Bounds carries the identical relationship to Placement.
//
// Bounds is never EMPTY beside a returned Image (byb-62t). A placement narrower
// than a point on an axis is reported as the smallest integer rectangle that
// contains it rather than collapsing to nothing, so an empty Bounds and returned
// bytes cannot occur together. The cost is that such a Bounds overstates the
// extent by up to a point per edge, which is one more reason not to derive a
// scale or DPI from it.
type PageRaster struct {
	Image  image.Image
	Bounds image.Rectangle // where the raster lands
	Page   image.Rectangle // the page's CropBox

	// ObjNr is the PDF object number of the image XObject this raster came
	// from -- the same handle ImageRef.ObjNr carries from Inspect, and exactly
	// what ReplaceImages keys its substitution map on (substitute.go:28-35,
	// :127-131). Without it, a caller wanting to write back an encoding of
	// this raster had no exported way to name which object to replace and had
	// to guess.
	//
	// The guess is wrong on real documents. classify picks the LAST placement
	// in paint order as the page's one visible raster (extract.go: top :=
	// len(s.Images) - 1) because a stacked page can hide an earlier image behind
	// an opaque one painted over it -- 16,241 measured Internet Archive pages
	// do exactly this, two page-covering images at the identical CTM, the
	// second occluding the first. On those pages the first placement's object
	// number is the HIDDEN under-layer: substituting on it writes a document
	// that opens cleanly, looks unchanged, and is wrong, with no error raised
	// anywhere. ObjNr is the object classify actually chose, which fixes that
	// case; it is negative for an image XObject that is a direct object rather
	// than an indirect reference, exactly as ImageRef.ObjNr is (inspect.go:55-58),
	// since ReplaceImages has no cross-reference entry to write such a
	// substitution back to.
	//
	// ObjNr identifies the XObject, not the placement: one image can be painted
	// on several pages, and ReplaceImages substitutes it once, changing every
	// page that shares it (optimize.go:317 memoizes per object id for exactly
	// this reason).
	ObjNr int
	// DroppedAnnots counts the annotations on this page that paint and are not
	// in Image.
	//
	// Annotations live beside the content stream, not in it, so classify never
	// sees them and no raster ever contains them: a stamp, a signature or a
	// form field on a scanned page is shown by a viewer and absent here. That
	// is true of every extracted page and always has been — byb-b1.3 did not
	// introduce it, it only removed the coverage gate that had been catching
	// some of it by accident.
	//
	// Non-zero means the caller is holding an image that is missing ink a
	// reader would see. What to do about it is the caller's policy, which is
	// why this is reported rather than diverted: byb-b1.11 measured 6 such
	// pages in 18,610 extracted, and refusing 6 real pages to avoid 6
	// incomplete ones is the worse trade for an archive. Run byblos-annots for
	// the breakdown by subtype.
	//
	// Rendering the appearance streams into the raster would be a renderer,
	// which design spec section 2 puts out of scope.
	DroppedAnnots int

	// Bitonal reports that the source XObject DECLARED one bit per component,
	// or was an image mask. It is the same predicate Inspect surfaces as
	// ImageRef.Bitonal, carried to where the pixels are.
	//
	// Image is an *image.RGBA either way: decoding widens the samples, and
	// Bitonal is the only surviving record of what they were widened from.
	// Callers feeding DownsampleDeclaredBPC map true to declaredBPC 1.
	//
	// It is a DECLARATION, never a measurement. Do not replace it with a pixel
	// test: an 8-bpc source whose pixels happen to be pure black and white --
	// a bitonal TIFF widened to 8 bpc, common -- is indistinguishable from a
	// genuine 1-bpc source by pixel data alone, but Ghostscript keys
	// /MonoImageDownsampleType off the declared depth and downsamples it
	// bicubically. byb-plj measured a first attempt at that sniff 13 dB under
	// byblos's own oracle.
	//
	// Bitonal is chosen over an int BPC because an /ImageMask carries no
	// /BitsPerComponent at all and would have to report 0; see byb-xcx.
	Bitonal bool
}

// CoversPage reports whether the raster fills the page box. When it is false
// the caller is holding the scanned area, not the MediaBox, and the difference
// between Bounds and Page is the part of the page the scan does not cover.
//
// Byblos does not pad the raster out to the page. Synthesising pixels that were
// never scanned is the same kind of lie as resampling a deskewed raster to
// straighten it, and on a bilevel JBIG2 scan it would mean decoding and
// re-encoding the very pages the lossless promise exists for.
// A zero Page is not covered by anything. image.Rectangle.In answers true for
// an empty receiver, so without this the zero PageRaster — what every error
// return hands back — would report itself as a full-page scan, and a caller
// that read CoversPage before err would never notice.
func (p PageRaster) CoversPage() bool {
	if p.Page.Empty() {
		return false
	}
	return p.Page.In(p.Bounds)
}

// ExtractPageRaster returns the single raster of the given 1-based page.
//
// A page may reach that through more than one placement. Scanner pipelines
// routinely paint two page-covering images at the same matrix, the second
// hiding the first; that page has one visible raster and this returns it. See
// classify for the conditions, which are stricter than they look — a layer that
// is not provably an opaque cover diverts.
//
// It returns an error wrapping ErrNotSingleRaster when the page is anything
// else, and ErrUnsupportedImageCodec when the raster's codec is not decodable.
//
// Note on rotation: a page's /Rotate is a display attribute and does not affect
// content space, so a rotated page still extracts cleanly. The returned image
// is the raster as stored; applying /Rotate is the caller's business.
//
// Orientation within content space is a narrower promise: the returned raster
// is the page as it reads, to within maxSkewDeg. Scanner deskew lives inside
// that tolerance — a bulk scanner writes a fraction of a degree into the
// placement matrix and leaves the pixels raw — so those pages extract as stored,
// with the residual affine recorded in ImageRef.Placement and, at write time, in
// PageProvenance.Placement. Byblos does not straighten them: resampling a
// bilevel raster to take out a tenth of a degree would break the lossless
// promise on exactly the pages this library exists for.
//
// Vector paint is judged the same way: by what the content stream proves, not
// by rendering it. A path painted before the raster and landing inside its
// placement box is behind an opaque image and cannot be seen, so the page is
// still one page-covering raster and still extracts. Paint the raster does not
// hide diverts. Nothing is filled, scan-converted or composited to decide this
// — only painting order and a bounding box.
//
// Coverage is not a condition at all when the page has one raster and nothing
// else. Every other arm of classify has already established that no text, path,
// shading, inline image or unresolved XObject marks the page, so nothing can
// put ink outside the placement and that raster IS the page, at any coverage.
// byb-b1.3 measured 132 such pages; on every one of them the region outside the
// placement held zero content operators. What the caller gets told is the
// geometry — PageRaster.CoversPage — not a divert.
//
// Past the tolerance the page diverts, and byb-b1.2 settled that this includes
// the two cases a caller could correct exactly, a quarter turn and a mirror.
// Recording the affine does weaken the older argument that the caller has no
// signal to correct them by. What it does not weaken is the reason: the returned
// image.Image is consumed — OCR, thumbnails, human review — by code that never
// reads provenance, and a sideways or mirrored raster is wrong there in a way a
// fraction of a degree is not.
// It cannot be cancelled. Use ExtractPageRasterContext when the caller has a
// deadline.
func ExtractPageRaster(r io.ReadSeeker, page int) (*PageRaster, error) {
	return ExtractPageRasterContext(context.Background(), r, page)
}

// ExtractPageRasterContext is ExtractPageRaster, cancellable at the two
// boundaries this primitive has (byb-xyn).
//
// CANCELLATION LATENCY: THE WHOLE OF ONE PAGE'S EXTRACTION. This is the
// weakest guarantee of the nine and the honest statement of it matters more
// than the check does. Extracting one page is open, walk, classify, decode,
// and none of those is interrupted: pdfcpu is not context-aware, and byblos'
// own content walk (internal/content.Walk) DOES check now, since byb-fem: it
// consults ctx at every token, so a content stream with millions of operators
// costs one token rather than the whole walk. Before that bead the walk was
// 95.4% of this call on such a stream and would not stop at all.
//
// What stays uninterruptible is pdfcpu. A caller needing a tighter bound than
// "one page" must still budget for one page's worst case in the DECODE, which
// is bounded by byb-riy's resource budget rather than by this context.
// Measured over a 120-page document of ordinary scans, the longest stretch
// between two context checks was 55% of the call; that figure predates
// byb-fem and is unchanged by it, because on those documents the walk was
// never the dominant unit.
//
// The checks are placed to keep a cancelled call OUT of the extraction
// telemetry: a call abandoned because the caller's deadline expired is not a
// failed extraction, and counting it as one would pollute the divert rate that
// design spec section 2's premise rests on with what are really worker
// timeouts. That is why the pdfdoc.Open error branch re-checks before
// counting -- a caller that closes the reader on cancel makes Open fail, and
// without the re-check every timed-out document would land in the counters as
// Attempted+Failed and be reported to the caller as a pdfcpu error rather than
// a cancellation.
func ExtractPageRasterContext(ctx context.Context, r io.ReadSeeker, page int) (*PageRaster, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	d, err := pdfdoc.Open(r)
	if err != nil {
		if cerr := checkContext(ctx); cerr != nil {
			return nil, cerr
		}
		countAttempt()
		countFailure()
		return nil, err
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	pr, _, err := extractPage(ctx, d, page)
	return pr, err
}

// extractPage is ExtractPageRaster's body once the document is open, plus the
// record of what it did. Splitting it out lets RecordExtraction (provenance.go)
// walk a whole document with one pdfdoc.Open and still get exactly the outcome
// ExtractPageRaster reports -- and it is the only scope where the UNROUNDED
// floats exist: PageRaster.Bounds/.Page have already been through round()
// (inspect.go:92-100), and 568.3708 is not 568.
func extractPage(ctx context.Context, d pdfdoc.Doc, page int) (*PageRaster, PageProvenance, error) {
	countAttempt()

	p, err := d.Page(page)
	if err != nil {
		countFailure()
		return nil, PageProvenance{}, err
	}
	_, scan, err := inspectPage(ctx, d, page)
	if err != nil {
		countFailure()
		return nil, PageProvenance{}, err
	}

	idx, reason := classify(p.CropBox, scan, d.ImageInfo)
	if reason != "" {
		countDivert(reason)
		return nil, PageProvenance{Diverted: divertClass(reason)}, fmt.Errorf("%w: %s", ErrNotSingleRaster, reason)
	}

	placement := scan.Images[idx]
	data, fileType, err := d.RawImage(placement.ID)
	if err != nil {
		// pdfcpu declines to render some filters by returning a nil reader;
		// pdfdoc turns that into ErrUnsupportedCodec. That is a divert (the page
		// is understood, its codec is not), never a read failure.
		//
		// pdfcpu gives up before naming a file type here (RawImage's ErrUnsupportedCodec
		// comment), so unlike the two divert sites below, there is no codec name
		// to carry. This stays the coarse legacy reason; divertClass maps it to
		// the class that still nominates every decode-* rule (byb-z8j).
		//
		// NO *NotImplemented HERE, AND THE MISSING CODEC NAME IS THE WHOLE
		// REASON (byb-bjh). NotImplemented.Capability is a string from the
		// register's vocabulary rather than free text, precisely so a caller can
		// hand it back to UpgradeCandidates; with no file type there is no
		// string to put in it, and the register has no name for "some codec
		// pdfcpu declined to identify". Naming a missing capability this site
		// cannot actually identify would be worse than naming none.
		if errors.Is(err, pdfdoc.ErrUnsupportedCodec) {
			countDivert("unsupported-codec")
			return nil, PageProvenance{Diverted: divertClass("unsupported-codec")}, fmt.Errorf("%w: %v", ErrUnsupportedImageCodec, err)
		}
		countFailure()
		return nil, PageProvenance{}, err
	}
	var img image.Image
	switch fileType {
	case "jbig2":
		// pdfcpu returns JBIG2 as opaque bytes rather than erroring, so nothing
		// upstream of here has looked at them; image.Decode would not recognise
		// them either. byblos decodes them itself (jbig2.go): the generic-region
		// subset EncodeJBIG2Generic writes, so a byblos-compressed page is
		// re-extractable by byblos (byb-riy), and since byb-9v0 the arithmetic
		// symbol mode most archive scanners emit.
		//
		// What still diverts is the rest -- Huffman symbol coding, refinement,
		// halftones, MMR, the three generic templates byblos does not code --
		// under the SAME reason string as before. The taxonomy does not gain a
		// term for it: a page that needs a fuller JBIG2 decoder and a page that
		// needs any JBIG2 decoder nominate the same capability, decode-jbig2,
		// and splitting them is byb-z8j's to decide, not this site's.
		//
		// The symbol dictionary a text region places is very often not in this
		// stream at all: a bulk scanner writes one per DOCUMENT and points every
		// page's image dictionary at it through /DecodeParms /JBIG2Globals
		// (byb-9v0). Without it such a page decodes to a blank raster rather
		// than failing, so the globals are fetched here, and a failure to read
		// an entry that IS there is a diverted page rather than a silent one.
		//
		// NO *NotImplemented ON THAT FAILURE (byb-bjh): a /JBIG2Globals entry
		// byblos cannot resolve is a property of THIS document, not of this
		// build. Every byblos ever written would fail on it, so no future
		// capability recovers the page and nominating one would send a caller
		// back over an archive for nothing.
		globals, gerr := d.RawImageGlobals(placement.ID)
		if gerr != nil {
			countDivert("unsupported-codec-jbig2")
			return nil, PageProvenance{Diverted: divertClass("unsupported-codec-jbig2")},
				fmt.Errorf("%w: jbig2: %v", ErrUnsupportedImageCodec, gerr)
		}
		img, err = decodeJBIG2Placement(data, globals, d.ImageInfo, placement.ID)
		if err != nil {
			// No fileType interpolation here, unlike the sibling sites: every
			// error this branch can produce already opens with "jbig2:", so
			// adding it again would read "jbig2: jbig2: ...".
			countDivert("unsupported-codec-jbig2")
			prov := PageProvenance{Diverted: divertClass("unsupported-codec-jbig2")}
			// THE DIVERT REASON CANNOT SAY THIS AND THE ERROR CAN. Both arms
			// record "unsupported-codec-jbig2" -- byb-z8j owns the reason
			// vocabulary and this site does not add to it -- but a coding mode
			// byblos has not built is a property of the BUILD, and damaged bytes
			// are a property of the DOCUMENT. The first is recovered by a future
			// byblos for every document at once; the second is recovered by
			// nothing. A caller that re-processes on the wrong one wastes a pass
			// over the archive or never re-processes at all.
			//
			// Measured over the pinned sample at 23a3470: 37 pages reach here,
			// 36 for a coding mode and 1 for damage.
			if errors.Is(err, ErrUnsupportedJBIG2Feature) {
				return nil, prov, fmt.Errorf("%w: %v (%w)", ErrUnsupportedImageCodec, err, &NotImplemented{
					Capability: "decode-jbig2",
					Why:        "the stream uses a JBIG2 coding mode this build does not decode",
					Issue:      capabilityIssue["decode-jbig2"],
				})
			}
			return nil, prov, fmt.Errorf("%w: %v", ErrUnsupportedImageCodec, err)
		}
	case "jpx":
		// As above for the opaque bytes, and unlike jbig2 there is no byblos
		// JPEG2000 code in either direction and x/image ships no JPX decoder, so
		// every one of these diverts.
		//
		// The reason carries fileType so divertClass (extract.go) can emit a
		// codec-specific class and capabilityRules (upgrade.go) can nominate
		// decode-jpx without also nominating decode-jbig2 for the same page
		// (byb-z8j).
		//
		// THIS IS THE UNCONDITIONAL CASE and the only one in the switch. The
		// jbig2 arm has to ask which of two things went wrong, because byblos
		// decodes some JBIG2; here it decodes none, so there is no document this
		// arm could be reporting a property of. Every jpx page that reaches this
		// line does so for the same reason and a future build recovers all of
		// them together, which is exactly what *NotImplemented means.
		countDivert("unsupported-codec-jpx")
		return nil, PageProvenance{Diverted: divertClass("unsupported-codec-jpx")},
			fmt.Errorf("%w: jpx (%w)", ErrUnsupportedImageCodec, &NotImplemented{
				Capability: "decode-jpx",
				Why:        "byblos has no JPEG2000 code in either direction and x/image ships no decoder",
				Issue:      capabilityIssue["decode-jpx"],
			})
	default:
		img, _, err = image.Decode(bytes.NewReader(data))
	}
	if err != nil {
		// As above, the reason carries fileType so the divert nominates one
		// decoder rather than all three (byb-z8j).
		//
		// Do NOT read this as "CMYK rasters divert". They do not. pdfcpu renders
		// a CMYK raster to TIFF (writeImage.go:385, :705 — note it returns the
		// type as "tif", not "tiff"; divertClass maps that), and TIFF is ALREADY
		// decodable here: github.com/hhrutter/tiff calls image.RegisterFormat
		// for both endiannesses (reader.go:882-883) and is linked into every
		// binary containing this package, transitively through
		// internal/pdfdoc -> pdfcpu. Measured 2026-07-31: image.DecodeConfig on
		// TIFF magic returns format "tiff", against "image: unknown format" for
		// a control.
		//
		// So this site is reached for a TIFF only when that registered decoder
		// REJECTS it — an unsupported compression, not a colour space. That is a
		// much narrower set than the stale comment here used to claim, and it is
		// why decode-tiff is rare in practice rather than dead.
		//
		// NO *NotImplemented HERE EITHER (byb-bjh), and this is the one omission
		// a later change could reverse. An unsupported TIFF compression IS a
		// missing capability and decode-tiff IS its name — but image.Decode
		// returns the same shape of error for a compression it does not
		// implement and for a TIFF that is simply corrupt, so this site cannot
		// tell which it is holding. The jbig2 arm above can only make that call
		// because internal/jbig2 draws the line itself and says so with
		// ErrUnsupportedFeature. Give a decoder here the same sentinel (byb-2bx)
		// and this arm can name its capability the same way.
		countDivert("unsupported-codec-" + fileType)
		return nil, PageProvenance{Diverted: divertClass("unsupported-codec-" + fileType)}, fmt.Errorf("%w: %s: %v", ErrUnsupportedImageCodec, fileType, err)
	}
	out := &PageRaster{
		Image:  img,
		Bounds: boxRect(placement.Box),
		Page:   rectOf(p.CropBox),
	}
	// The declared depth, from the same dictionary fact classify already took
	// and inspect.go turns into ImageRef.Bitonal. Keep the two predicates
	// identical: a page's PageRaster.Bitonal and its ImageRef.Bitonal must
	// never disagree.
	//
	// A miss leaves it false. That is the safe direction: false routes a
	// caller to the contone resampler, which is what happened before this
	// field existed, whereas a wrongly-true would subsample a contone scan.
	//
	// ObjNr is resolved the same way inspect.go turns a placement into
	// ImageRef.ObjNr (inspect.go:215-220): the two must never disagree, since
	// ObjNr's whole purpose is to be the handle a caller already holds from
	// Inspect.
	if info, ok := d.ImageInfo(placement.ID); ok {
		out.Bitonal = info.BPC == 1 || info.ImageMask
		out.ObjNr = info.ObjNr
	}
	// Only on the success path. A divert has already told the caller it is
	// getting no raster, and 97% of a real archive diverts, so reading and
	// dereferencing every annotation there would be work spent on an answer
	// nobody receives.
	//
	// An unreadable /Annots is not a reason to fail a page whose raster is
	// fine. It leaves the count at zero, which understates the loss, so the
	// error is dropped here and byblos-annots reports the read failures
	// separately.
	if annots, err := d.Annots(page); err == nil {
		for _, a := range annots {
			if a.Paints() {
				out.DroppedAnnots++
			}
		}
	}
	geom := &PageGeometry{
		RasterBox: normalizedBox(placement.Box.LLX, placement.Box.LLY, placement.Box.URX, placement.Box.URY),
		PageBox:   normalizedBox(p.CropBox.LLX, p.CropBox.LLY, p.CropBox.URX, p.CropBox.URY),
	}
	// ClipBox is recorded only when a clip actually narrowed this placement
	// below its own unclipped extent (PageGeometry's doc comment) -- not
	// whenever a clip happened to be in effect, and not using placement.Box
	// itself, which after byb-b1.12 IS the narrowed box already.
	if clipNarrowed(placement) {
		cb := normalizedBox(placement.Clip.LLX, placement.Clip.LLY, placement.Clip.URX, placement.Clip.URY)
		geom.ClipBox = &cb
	}
	rec := PageProvenance{
		Applied:       []string{"extract-raster"},
		DroppedAnnots: out.DroppedAnnots,
		Geometry:      geom,
	}
	// Empty for an axis-aligned placement, which is almost every page
	// (PageProvenance's doc comment). The off-diagonal terms are exactly what
	// skewDegrees reads, and classify has already rejected anything past
	// maxSkewDeg, so what survives here is a scanner deskew.
	if m := placement.CTM; m[1] != 0 || m[2] != 0 {
		rec.Placement = m[:]
	}
	countExtracted(out.CoversPage())
	return out, rec, nil
}

// normalizedBox returns [llx lly urx ury] with each pair of corners put in
// canonical (min, max) order.
//
// ISO 32000-1 7.9.5 permits a rectangle array's corners in EITHER diagonal
// order and requires a consumer to normalize; pdfcpu's RectForArray does not
// (it stores whatever four numbers it read), so a page whose /CropBox names
// its corners UR-then-LL round-trips into PageGeometry inverted -- e.g. page_box
// [612 792 0 0] -- unless normalized here. PageRaster.Page, by contrast, is
// already canonical because image.Rect swaps a reversed pair; without this a
// full-page scan could be recorded as PageGeometry.CoversPage()==false while
// PageRaster.CoversPage()==true for the very same page.
func normalizedBox(llx, lly, urx, ury float64) [4]float64 {
	if llx > urx {
		llx, urx = urx, llx
	}
	if lly > ury {
		lly, ury = ury, lly
	}
	return [4]float64{llx, lly, urx, ury}
}

// classify returns the index of the placement that is the page's raster, or the
// reason the page cannot be treated as one. imageInfo resolves an image's
// dictionary facts by placement ID; pdfdoc.Doc's ImageInfo method is the real
// one.
//
// The order is deliberate: the first matching reason is the one reported, and
// it should be the most informative. A born-digital page has both no image and
// text; "no-image" says more.
//
// The returned strings are the keys of the divert counters, so changing one
// changes an operational metric. Do not rename them casually.
func classify(page pdfdoc.Rect, s *content.Scan, imageInfo func(int) (pdfdoc.ImageInfo, bool)) (int, string) {
	switch {
	case len(s.Images) == 0:
		return 0, "no-image"
	// Only text that deposits ink is a reason to divert. Rendering mode 3 paints
	// no glyphs, and an invisible OCR layer is what nearly every scan pipeline
	// ships; keying on TextOps diverted 100% of a real ScanSnap corpus, and
	// 98.7% of the pages it diverted over a page-covering raster carried no ink
	// at all.
	//
	// Known simplification: this ignores z-order. Text painted BEFORE an opaque
	// page-covering image is hidden by it and contributes nothing either, which
	// was 36 pages of the measurement against 210 for the rendering-mode cases.
	// Those pages divert, which is the safe direction; fixing them means
	// tracking paint order, not pretending operator order does not matter.
	case s.InkedTextOps > 0:
		return 0, "has-text"
	case s.InlineImgs > 0:
		return 0, "inline-image"
	// Not every painted path is content. A background wash laid down before the
	// raster and then covered by it is invisible, and byb-b1.5 measured 126
	// scan-shaped pages diverting on exactly that: 117 of them set a background
	// fill colour before the first Do. Only paint the raster does not hide
	// diverts a page.
	//
	// Every placement is a candidate cover, not just the top one: a path is
	// hidden by anything opaque painted over it (byb-7aq). Testing here rather
	// than after the geometry checks keeps the reported reason the most
	// informative one: a page carrying visible vector content says so, whatever
	// else is also wrong with it.
	case !paintsHidden(s.Images, s.Paints, imageInfo):
		return 0, "vector-paint"
	case s.ShadingOps > 0:
		return 0, "shading"
	case len(s.Unresolved) > 0:
		return 0, "unresolved-xobject"
	}
	if reason := mrcLayers(page, s.Images, imageInfo); reason != "" {
		return 0, reason
	}

	// Scan.Images is in paint order, so the last placement is the one on top,
	// and it is the only candidate: an earlier raster is not what the page
	// shows, and a raster painted after the candidate would be lost by
	// returning it.
	top := len(s.Images) - 1
	// A clip (W/W* n, or a form /BBox) can narrow the top placement's Box to
	// zero area -- e.g. a clip rectangle disjoint from the image's own extent
	// clamps to a degenerate box at the near corner (intersectBox,
	// internal/content/walk.go). Nothing is visible there: extracting would
	// hand back the full raster bytes against a raster_box that claims an
	// empty rectangle, often outside the page box entirely, which is not a
	// record any reader of PageGeometry can trust. Diverting instead keeps
	// the record coherent: no bytes are returned that disagree with a
	// geometry nobody measured a visible mark in.
	//
	// The test is on the FLOAT extent, and the distinction is the whole of
	// byb-62t. It used to test the ROUNDED rectangle, on the reasoning that
	// Bounds is what the caller sees, and it named a 0.4pt sliver as the case
	// that justified it. That sliver is not invisible: poppler 26.06.0 inks 792
	// pixels of `q .4 0 0 792 10 0 cm /Im0 Do Q` at 72 DPI and 9,900 at 300, a
	// black stripe the height of the page, and pdfimages -list reports the image
	// present at 720 ppi. Diverting it loses a page poppler renders, which is
	// the trade byb-3jq settled against.
	//
	// So the two questions are separated. "Can this deposit ink" is a float
	// question and marks() answers it here; "what rectangle do I report" is a
	// projection and boxRect answers it, widening a sub-point extent outward
	// rather than collapsing it (inspect.go). Nothing downstream now depends on
	// that collapse, which is what makes the widening safe.
	//
	// The condition is "a clip actually NARROWED this placement", not "a clip
	// was in effect", and it is deliberately the same test the ClipBox
	// recording site above uses -- the two must agree about what counts as
	// clip-caused or the divert reason and the stored geometry tell different
	// stories. A page-sized `re W n`, the commonest clip in real PDFs, narrows
	// nothing: under a bare Clip != nil test it would steal "flipped-placement"
	// and "clipped-away" would name a cause that did not apply. placementReason
	// below owns the zero-area causes a clip did not create.
	if clipNarrowed(s.Images[top]) && !marks(s.Images[top].Box) {
		if top > 0 {
			return 0, "multiple-images"
		}
		return 0, "clipped-away"
	}
	if reason := placementReason(s.Images[top]); reason != "" {
		// On a layered page the informative fact is that the layers did not
		// reduce to one raster, not which geometry test the top layer failed.
		// This is also what keeps genuine tiling reported as tiling.
		if top > 0 {
			return 0, "multiple-images"
		}
		return 0, reason
	}

	// A lone raster is not asked to cover the page, and byb-b1.3 is why.
	// 132 measured pages across 17 files place the raster at its own resolution
	// on a nominal box — 302, 300, 400 DPI, a round number in every dominant
	// file — and fall short of it. ia-DTIC_ADA383635.pdf p40 covers 91.74% and
	// leaves a 43.6 point blank strip. Raising coverTolerancePt does not reach
	// them and the bead measured that too: 1.0 -> 2.0pt buys 7 pages, and
	// swallowing 90% of the bucket needs about 56pt, which is not a tolerance.
	//
	// What reaches them is not a tolerance at all. Every arm above has already
	// established there is no inked text, no path, no shading, no inline image
	// and no unresolved XObject anywhere in the stream, so nothing in the
	// CONTENT STREAM can mark the page outside the placement and that raster IS
	// the page, whatever fraction of the box it occupies. On all 132 measured
	// pages the region outside the placement held zero content operators. The
	// caller learns the geometry from PageRaster.CoversPage rather than losing
	// the page to a divert.
	//
	// "Content stream" is the exact width of the claim, and two things sit
	// outside it. Annotations are not in it, so an appearance stream in the
	// uncovered strip is not something any arm above can see (byb-b1.11). And
	// Walk still ignores text clipping modes (Tr 4-7, ISO 32000-1 9.3.6): a
	// glyph outline that clips without painting is not folded into gs.clip, so
	// Bounds can overstate what is visible in that residual case (Walk's doc
	// comment). Form /BBox and every W/W* clip path, by contrast, ARE tracked
	// as of byb-b1.12 and narrow Bounds accordingly. Neither gap is new here;
	// both were masked for these pages by the gate this replaced, which is why
	// they are named rather than assumed away.
	//
	// byb-b1.11 has since been measured rather than left as a caveat. Over
	// 151,077 pages, 18,610 of them extracted: 6 carry an annotation that
	// paints, exactly ONE of those had a raster short of the page box — the
	// case this branch admits — and ZERO had ink landing outside the raster.
	// So removing the gate widened the loss by one page in 151,077. It is
	// reported instead of diverted, on PageRaster.DroppedAnnots, because
	// refusing six real pages to avoid six incomplete ones is the worse trade
	// for an archive. Note the count is non-zero on covered pages too: the loss
	// was never particular to this branch, and 5 of the 6 predate it.
	if top > 0 {
		// That argument does not extend to a stack: an under-layer reaching past
		// the top one is exactly the ink it says cannot exist. A layered page
		// still has to be covered.
		if !covers(s.Images[top].Box, page) {
			return 0, "multiple-images"
		}
		// 16,241 measured Internet Archive pages paint two page-covering images
		// at the identical CTM, the second hiding the first. That reduces to one
		// raster only while the top layer really is an opaque cover over
		// everything below it.
		if !opaqueCover(s.Images[top], imageInfo) {
			return 0, "transparent-overlay"
		}
		for _, under := range s.Images[:top] {
			if !contains(s.Images[top].Box, under.Box) {
				return 0, "multiple-images"
			}
		}
	}
	return top, ""
}

// clipNarrowed reports whether a clip (W/W* n, or a form /BBox) actually cut
// this placement below its own unclipped extent, as opposed to merely being in
// effect when it was painted.
//
// The distinction is load-bearing and has exactly one definition, shared by the
// two callers that need it: classify's clipped-away guard and the ClipBox
// recording site in extractPage. A page-sized `re W n` is the commonest clip in
// real documents and narrows nothing, so a bare Clip != nil test would both
// record a ClipBox for a clip that did not bite and blame that clip for a
// zero-area box some other cause produced. If these two ever disagree, the
// divert reason and the stored geometry start telling different stories about
// the same page.
//
// Comparing against CTM.UnitSquareBox() rather than to a saved copy is
// deliberate: an image XObject always occupies the unit square in its own space
// (ISO 32000-1 8.9.5.2), so the unclipped extent is always recoverable from the
// CTM and needs no extra field on Placement.
func clipNarrowed(p content.Placement) bool {
	return p.Clip != nil && p.Box != p.CTM.UnitSquareBox()
}

// placementReason reports why a placement cannot stand as the page's raster, or
// "" when it can.
//
// Everything it asks is about orientation, and nothing about size. Coverage
// used to be the third test here and byb-b1.3 removed it; classify explains
// where that went and why a stack still needs it.
func placementReason(p content.Placement) string {
	m := p.CTM
	// A sub-degree rotation here is scanner deskew, and the raster underneath it
	// is the raw skewed scan: the page is one page-covering raster and extracts,
	// with the matrix recorded in ImageRef.Placement (byb-b1.2).
	if skewDegrees(m) > maxSkewDeg {
		return "rotated-placement"
	}
	// The off-diagonal terms do not pin orientation. A negative scale mirrors
	// the raster without any skew, and Box cannot see it either: UnitSquareBox
	// takes min/max over the four mapped corners, so `612 0 0 -792 0 792` still
	// reports {0 0 612 792} and covers() still says yes. Without this check a
	// mirrored or upside-down page extracts silently as if it were clean.
	// Requiring both scales strictly positive also rules out a degenerate
	// zero-scale placement, leaving a > 0 and d > 0 for everything downstream.
	if m[0] <= 0 || m[3] <= 0 {
		return "flipped-placement"
	}
	return ""
}

// opaqueCover reports whether p can be said to hide whatever is painted under
// it. Three of the four ways it cannot are dictionary facts — /ImageMask,
// /SMask, /Mask — and the fourth is the graphics state at the moment of
// painting, which the walk recorded.
//
// An image whose dictionary could not be read is not opaque. Guessing the other
// way is the one error here that returns a wrong page instead of a divert.
func opaqueCover(p content.Placement, imageInfo func(int) (pdfdoc.ImageInfo, bool)) bool {
	if !p.Opaque {
		return false
	}
	info, ok := imageInfo(p.ID)
	return ok && !info.ImageMask && !info.SMask && !info.Mask
}

// mrcLayers reports the two-tier MRC shape Google Books emits: a bitonal
// page-covering base plus a smaller non-bitonal patch.
//
// It runs before the take-the-top rule and overrides it. On 153 measured pages
// of one Internet Archive file the bitonal base is BLANK and the patch carries
// every word on the page, and only decoding the pixels tells those apart from
// the pages where the base carries the text. Either layer can be the document,
// so neither is returned.
//
// Compositing the layers is a renderer's job, and byblos-divert established it
// is not worth building for 0.78% of one corpus.
func mrcLayers(page pdfdoc.Rect, imgs []content.Placement, imageInfo func(int) (pdfdoc.ImageInfo, bool)) string {
	pageArea := area(content.Box{LLX: page.LLX, LLY: page.LLY, URX: page.URX, URY: page.URY})
	if pageArea <= 0 {
		return ""
	}
	var base, patch bool
	for i, p := range imgs {
		info, ok := imageInfo(p.ID)
		if !ok {
			continue
		}
		if info.BPC == 1 || info.ImageMask {
			// A base is painted under something. A bitonal layer on top is a
			// stencil over the raster below, which is a transparency question,
			// not this one.
			base = base || (i < len(imgs)-1 && area(p.Box)/pageArea >= mrcBaseAreaFrac)
			continue
		}
		patch = patch || area(p.Box)/pageArea > mrcPatchAreaFrac
	}
	if base && patch {
		return "mrc-layers"
	}
	return ""
}

// contains reports whether outer covers inner, within tolerance.
func contains(outer, inner content.Box) bool {
	return outer.LLX <= inner.LLX+coverTolerancePt &&
		outer.LLY <= inner.LLY+coverTolerancePt &&
		outer.URX >= inner.URX-coverTolerancePt &&
		outer.URY >= inner.URY-coverTolerancePt
}

// paintTolerancePt is how far a STROKE may stray outside the raster and still
// count as hidden by it. A thousandth of a point is a two-hundredth of a pixel
// at 300 DPI, so it forgives arithmetic and nothing else.
//
// It is deliberately not coverTolerancePt. That one point is an allowance for
// where a page's own edge is, on the reasoning that a raster falling a point
// short still covers the page. Reusing it for a stroke would exactly cancel the
// spread recordPaint adds for a 2-point pen, and a page carrying a visible band
// of ink the raster does not would extract as if it were clean.
const paintTolerancePt = 1e-3

// paintFillTolerancePt is the same allowance for a FILL, and the stroke
// argument above is the whole reason the two differ.
//
// A fill's Box is its path. Nothing inflates it, so there is no spread for a
// tolerance to cancel and the objection that keeps paintTolerancePt at 1e-3
// cannot arise. byb-e04 measured what the strict number was costing: of the 471
// pages that divert "vector-paint" and would otherwise extract, the operator
// that escapes is `f` or `f*` on every single one a tolerance can reach. Not one
// is a stroke.
//
// One point is deliberately coverTolerancePt's value, for coverTolerancePt's
// reason — it is the width of the disagreement two producers have about where a
// page's edge is, and a wash laid on the page while the raster is laid on a form
// /BBox trimmed a fraction of a point inside it is exactly that disagreement.
// govdocs1/050104.pdf p2 is the case: its /Fm5 /BBox maps to a raster 0.28pt
// inside the page on the left and 0.30pt on the right, and the wash covers the
// page. The trim is real and byblos computes it correctly; a third of a point of
// unpainted page is not content.
//
// POPPLER PICKED THIS NUMBER, not the distribution. Every one of the 191 pages
// a 1pt fill tolerance releases was rendered at 72 and 300 DPI and the band
// outside the returned raster counted: zero of them carry a mark. The first page
// that does need 1.6831pt — govdocs1/600666.pdf p1, whose escaping wash is
// DeviceRGB 0.0471 grey and paints a visibly dark border, which is why the
// number is not raised to meet it. Going to 2pt would take that page and lose
// its border.
//
// The sample would tolerate anything below 1.6831 and the number is 1.0 anyway.
// Fitting a constant to the largest value one corpus happens to allow leaves
// nothing to defend it with when the next corpus disagrees.
//
// IT MOVES ONE COUNTER, and classify's doc comment asks that such a move be
// deliberate rather than quiet. Four pages — govdocs1/300512.pdf 16, 17, 18 and
// 23, whose wash needs 0.0603pt — reported "vector-paint" and now report
// "multiple-images". They diverted before and divert now; what changed is that
// the reason names the layered stack that actually stops the page reducing to
// one raster, instead of a wash that was never visible. They are the only four
// reason changes across 169,376 sample pages. See
// TestClassifyPaintOcclusionAcrossPlacements.
const paintFillTolerancePt = 1.0

// paintsHidden reports whether the page's rasters hide every path-painting
// operator on it: each path's visible ink landed inside an opaque placement
// painted after it.
//
// This is a graphics-state test, not a renderer (design spec section 2). It
// asks only what the content stream and the image dictionaries prove — the
// order operators were issued in, where their paths landed, what was clipping
// them, and whether a raster is see-through — and never what any pixel ends up
// being.
//
// The opacity check is what makes dropping a wash safe. A stencil /ImageMask
// paints only through its 1 bits, so a wash beneath one stays visible: of the
// 126 pages byb-b1.5 measured, 27 fill the PowerPoint slide colour rather than
// white, and on those the wash is the page background. opaqueCover rejects a
// mask, an /SMask, a /Mask and a lowered /ca or /CA alike, so none of them can
// hide anything.
//
// byb-7aq changed the two halves of the question this asks, because byb-b1.12
// regressed four govdocs1 pages that used to extract and each half accounts for
// part of them.
//
// The PATH side is Paint.Ink rather than Paint.Box: a path is judged on the
// part of it the clip lets through. byb-b1.12 narrowed Placement.Box to the
// visible rectangle and left Paint.Box unnarrowed, so this compared a clipped
// rectangle against an unclipped one. On 050667 p1 and 350795 p1 the SAME two
// clip paths bound the wash and the raster, and the wash then failed a test the
// raster defines — by 0.06 of a point on the first and by nine points on the
// second.
//
// The RASTER side is every placement rather than only the top one. A path is
// hidden by anything opaque painted over it, and byb-b1.12 made that
// distinguishable: on 050101 p49 the wash is unclipped and covers [0 0 1024 768],
// /Im1 covers the identical rectangle and is painted after it, and only then is
// the top raster placed under a clip that falls 0.573pt short at the foot of the
// page. classify already calls that wash rectangle contained when an IMAGE
// paints it — the under-layer loop above accepts /Im1 beneath the top placement
// — so testing the top placement alone was the odd rule out.
//
// Widening it costs nothing in safety. An opaque raster over a path hides that
// path whether or not it is the raster this page would extract, and whether the
// page extracts at all is still the layered-stack question the arms below
// decide.
//
// It does move one counter, and classify's doc comment asks that such a move be
// deliberate rather than quiet. A page whose wash is hidden by an opaque layer
// UNDER a transparent top layer used to report "vector-paint" and now reports
// "transparent-overlay". The page diverted before and diverts now; what changed
// is that the reason names the layer that actually stops the page reducing to
// one raster, instead of a wash that was never visible. See
// TestClassifyPaintOcclusionAcrossPlacements.
func paintsHidden(imgs []content.Placement, paints []content.Paint, imageInfo func(int) (pdfdoc.ImageInfo, bool)) bool {
	for _, p := range paints {
		ink, marks := p.Ink()
		if !marks {
			continue
		}
		tol := paintFillTolerancePt
		if p.Strokes() {
			tol = paintTolerancePt
		}
		if !inkHidden(ink, p.Index, tol, imgs, imageInfo) {
			return false
		}
	}
	return true
}

// inkHidden reports whether some opaque placement painted after this ink
// contains it. order is the ink's position in the shared painting order, and tol
// is how far the ink may stray outside a placement and still count as hidden —
// paintFillTolerancePt or paintTolerancePt, which the caller picks by operator.
func inkHidden(ink content.Box, order int, tol float64, imgs []content.Placement, imageInfo func(int) (pdfdoc.ImageInfo, bool)) bool {
	for _, img := range imgs {
		if img.Index < order || !opaqueCover(img, imageInfo) {
			continue
		}
		if ink.LLX >= img.Box.LLX-tol &&
			ink.LLY >= img.Box.LLY-tol &&
			ink.URX <= img.Box.URX+tol &&
			ink.URY <= img.Box.URY+tol {
			return true
		}
	}
	return false
}

// covers reports whether box contains the page box, within tolerance. An image
// larger than the page is fine: it is simply cropped on display.
func covers(b content.Box, page pdfdoc.Rect) bool {
	return contains(b, content.Box{LLX: page.LLX, LLY: page.LLY, URX: page.URX, URY: page.URY})
}

func area(b content.Box) float64 { return (b.URX - b.LLX) * (b.URY - b.LLY) }

// marks reports whether a box has the extent to deposit ink at all: positive on
// both axes. A raster squeezed to nothing on one axis paints nothing, however
// wide it is on the other.
//
// content.Paint.Ink asks the identical question of a path
// (internal/content/walk.go) and the two must not drift, because a placement and
// a path bounding the same rectangle either both mark or both do not. Ink has
// one exception this does not: a zero-width STROKE still marks, since ISO
// 32000-1 8.4.3.2 makes a zero-width line the thinnest the device can render.
// An image XObject has no such rule.
//
// Testing the extent per axis rather than area(b) > 0 is deliberate. It states
// the claim exactly, it does not underflow on a box small on both axes, and it
// is the answer that stays safe if a Box ever arrives inverted -- boxRect would
// swap such a pair into a non-empty rectangle, and this would not.
func marks(b content.Box) bool { return b.URX > b.LLX && b.URY > b.LLY }

// divertClass maps a fine-grained classify (or codec) reason to the class
// stored in PageProvenance.Diverted.
//
// Two vocabularies exist on purpose. The counters want detail, because their
// whole job is to say *why* the divert rate is what it is. The stored record
// wants only enough to answer "would re-processing help, and with what
// capability" — which is what capabilityRules in upgrade.go matches on, and a
// record written today has to stay meaningful when a later release renames a
// counter key.
//
// byb-z8j: for the codec case that answer now names the codec.
// "unsupported-codec-jbig2"/"-jpx" pass straight through, and
// "unsupported-codec-tif" — the file type pdfcpu v0.13.0 actually returns for
// a rendered TIFF (writeImage.go renderDeviceCMYKToTIFF and
// renderIndexedCMYKToTIFF both return "tif", never "tiff") — normalizes to
// "unsupported-codec-tiff" so the stored class matches the readable rule name
// decode-tiff (upgrade.go) keys on. decode-jbig2/-jpx/-tiff (upgrade.go) can
// each nominate only the pages that want that specific decoder. Anything else
// that starts with "unsupported-codec" — the coarse legacy string a
// pre-byb-z8j build wrote, where nothing can say after the fact which codec
// it carried, and any codec name this build does not yet special-case —
// collapses to the coarse "unsupported-codec" class, which every decode-*
// rule still matches (TestDivertClassCoversEveryReason and the compatibility
// test in upgrade_test.go pin this). A codec problem never falls through to
// "not-single-raster": that class nominates a renderer, and byb-97q is why a
// renderer is never the answer to an undecodable codec.
//
// B5 writes the record; this is the single place that decides the mapping. A
// reason belonging to neither vocabulary falls back to the class that makes a
// renderer a candidate, because reporting a wasted re-run is cheaper than
// hiding a real upgrade — the same bias UpgradeCandidates takes for a
// capability with no rule.
func divertClass(reason string) string {
	switch reason {
	case "unsupported-codec-jbig2", "unsupported-codec-jpx":
		return reason
	case "unsupported-codec-tif":
		return "unsupported-codec-tiff"
	}
	if strings.HasPrefix(reason, "unsupported-codec") {
		return "unsupported-codec"
	}
	return "not-single-raster"
}

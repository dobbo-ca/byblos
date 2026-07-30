package byblos

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"math"

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
// before it counts as an MRC patch rather than a stamp or a logo. Google Books
// patches cover about 84% of the page, so the floor only has to be clear of
// noise; it is not a tuned number.
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
type PageRaster struct {
	Image  image.Image
	Bounds image.Rectangle // where the raster lands
	Page   image.Rectangle // the page's CropBox
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
func ExtractPageRaster(r io.ReadSeeker, page int) (*PageRaster, error) {
	countAttempt()

	d, err := pdfdoc.Open(r)
	if err != nil {
		countFailure()
		return nil, err
	}
	p, err := d.Page(page)
	if err != nil {
		countFailure()
		return nil, err
	}
	_, scan, err := inspectPage(d, page)
	if err != nil {
		countFailure()
		return nil, err
	}

	idx, reason := classify(p.CropBox, scan, d.ImageInfo)
	if reason != "" {
		countDivert(reason)
		return nil, fmt.Errorf("%w: %s", ErrNotSingleRaster, reason)
	}

	placement := scan.Images[idx]
	data, fileType, err := d.RawImage(placement.ID)
	if err != nil {
		// pdfcpu declines to render some filters by returning a nil reader;
		// pdfdoc turns that into ErrUnsupportedCodec. That is a divert (the page
		// is understood, its codec is not), never a read failure.
		if errors.Is(err, pdfdoc.ErrUnsupportedCodec) {
			countDivert("unsupported-codec")
			return nil, fmt.Errorf("%w: %v", ErrUnsupportedImageCodec, err)
		}
		countFailure()
		return nil, err
	}
	switch fileType {
	case "jbig2", "jpx":
		// pdfcpu returns these as opaque bytes rather than erroring, so the
		// check has to happen here or the bytes look like a valid image.
		countDivert("unsupported-codec")
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedImageCodec, fileType)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// TIFF is what pdfcpu emits for CMYK rasters; golang.org/x/image/tiff
		// support arrives with B3.
		countDivert("unsupported-codec")
		return nil, fmt.Errorf("%w: %s: %v", ErrUnsupportedImageCodec, fileType, err)
	}
	out := &PageRaster{
		Image:  img,
		Bounds: boxRect(placement.Box),
		Page:   rectOf(p.CropBox),
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
	countExtracted(out.CoversPage())
	return out, nil
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
	// The candidate is the top placement, for the reason given below where top
	// is taken. Testing here rather than after the geometry checks keeps the
	// reported reason the most informative one: a page carrying visible vector
	// content says so, whatever else is also wrong with it.
	case !paintsHidden(s.Images[len(s.Images)-1], s.Paints, imageInfo):
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
	// Walk ignores a form /BBox and every clip path, so Bounds can overstate
	// what is visible (byb-b1.12). Neither is new here; both were masked for
	// these pages by the gate this replaced, which is why they are named rather
	// than assumed away.
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

// paintTolerancePt is how far a path may stray outside the raster and still
// count as hidden by it. A thousandth of a point is a two-hundredth of a pixel
// at 300 DPI, so it forgives arithmetic and nothing else.
//
// It is deliberately not coverTolerancePt. That one point is an allowance for
// where a page's own edge is, on the reasoning that a raster falling a point
// short still covers the page. Reusing it here would allow a point of ink to
// fall outside the raster and still be called invisible, which is the opposite
// direction — and it would exactly cancel the stroke spread that recordPaint
// adds for a 2-point pen.
const paintTolerancePt = 1e-3

// paintsHidden reports whether the raster hides every path-painting operator on
// the page: the raster is an opaque cover, and each path was painted before it
// and landed inside its placement box.
//
// This is a graphics-state test, not a renderer (design spec section 2). It
// asks only what the content stream and the image dictionaries prove — the
// order operators were issued in, where their paths landed, and whether the
// raster is see-through — and never what any pixel ends up being.
//
// The opacity check is what makes dropping a wash safe. A stencil /ImageMask
// paints only through its 1 bits, so a wash beneath one stays visible: of the
// 126 pages byb-b1.5 measured, 27 fill the PowerPoint slide colour rather than
// white, and on those the wash is the page background. opaqueCover rejects a
// mask, an /SMask, a /Mask and a lowered /ca or /CA alike, so none of them
// reaches the geometry test.
func paintsHidden(raster content.Placement, paints []content.Paint, imageInfo func(int) (pdfdoc.ImageInfo, bool)) bool {
	if len(paints) == 0 {
		return true
	}
	if !opaqueCover(raster, imageInfo) {
		return false
	}
	for _, p := range paints {
		if p.Index > raster.Index {
			return false
		}
		if p.Box.LLX < raster.Box.LLX-paintTolerancePt ||
			p.Box.LLY < raster.Box.LLY-paintTolerancePt ||
			p.Box.URX > raster.Box.URX+paintTolerancePt ||
			p.Box.URY > raster.Box.URY+paintTolerancePt {
			return false
		}
	}
	return true
}

// covers reports whether box contains the page box, within tolerance. An image
// larger than the page is fine: it is simply cropped on display.
func covers(b content.Box, page pdfdoc.Rect) bool {
	return contains(b, content.Box{LLX: page.LLX, LLY: page.LLY, URX: page.URX, URY: page.URY})
}

func area(b content.Box) float64 { return (b.URX - b.LLX) * (b.URY - b.LLY) }

// divertClass maps a fine-grained classify reason to the coarse class stored in
// PageProvenance.Diverted.
//
// Two vocabularies exist on purpose. The counters want detail, because their
// whole job is to say *why* the divert rate is what it is. The stored record
// wants only enough to answer "would re-processing help?", which is what
// capabilityRules in upgrade.go matches on — and a record written today has to
// stay meaningful when a later release renames a counter key.
//
// B5 writes the record; this is the single place that decides the mapping. An
// unrecognised reason falls back to the class that makes a renderer a candidate,
// because reporting a wasted re-run is cheaper than hiding a real upgrade —
// the same bias UpgradeCandidates takes for a capability with no rule.
func divertClass(reason string) string {
	if reason == "unsupported-codec" {
		return "unsupported-codec"
	}
	return "not-single-raster"
}

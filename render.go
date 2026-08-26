package byblos

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // RenderPage decodes image XObjects through image.Decode
	_ "image/png"
	"io"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
	"github.com/dobbo-ca/byblos/internal/render"
)

// RenderPage rasterises one page to an RGBA image whose long edge is
// longEdgePx pixels, and is the exported entry point to the renderer byb-8b9
// built (byb-547). page is 1-based.
//
// WHAT FIDELITY THIS IS. Thumbnail fidelity, the target design spec §2's
// 2026-08-03 amendment chose: recognisable rather than faithful. There is no
// colour management, no transparency group, no shading and no JBIG2 or JPX
// image XObject — a page element this stage cannot draw is skipped, and the
// rest of the page still renders. Measured against pdftoppm at 400px over 283
// comparable documents of a 299-document no-embedded-font population, the
// median disagreement is 6.88% of pixels (byb-8b9.7). Do not store the result
// as a rendition of the page; it is for looking at.
//
// THIS IS NOT THE `render` CAPABILITY, AND SHIPPING IT DOES NOT CLAIM ONE.
// That capability string (upgrade.go's capabilityRules, FUTURE.md's "A PDF
// renderer") names the ARCHIVAL renderer, whose job is to rescue the pages
// ExtractPageRaster refuses with ErrNotSingleRaster and the pages whose
// provenance records DroppedAnnots > 0. This function rescues neither:
// ExtractPageRaster's answer on those pages is unchanged. Adding "render" to
// buildCapabilities would make UpgradeCandidates skip exactly the documents
// that still want the archival renderer, which is the failure mode
// capability_register_test.go's "never both" arm exists to prevent.
//
// It cannot be cancelled. Use RenderPageContext when the caller has a deadline.
func RenderPage(r io.ReadSeeker, page, longEdgePx int) (*image.RGBA, error) {
	return RenderPageContext(context.Background(), r, page, longEdgePx)
}

// RenderPageContext is RenderPage, cancellable.
//
// CANCELLATION LATENCY: the renderer checks ctx as it walks the content
// stream, so a cancel lands within one operator of the check rather than at
// the end of the page.
func RenderPageContext(ctx context.Context, r io.ReadSeeker, page, longEdgePx int) (*image.RGBA, error) {
	if longEdgePx <= 0 {
		return nil, fmt.Errorf("byblos: RenderPage: long edge %d is not a positive number of pixels", longEdgePx)
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	d, err := pdfdoc.Open(r)
	if err != nil {
		return nil, fmt.Errorf("byblos: RenderPage: %w", err)
	}
	p, err := d.Page(page)
	if err != nil {
		return nil, fmt.Errorf("byblos: RenderPage: %w", err)
	}
	// The CROP box, not the media box: it is what a viewer shows, and it is
	// what the pdftoppm -cropbox oracle the fidelity above was measured
	// against renders.
	box := content.Box{LLX: p.CropBox.LLX, LLY: p.CropBox.LLY, URX: p.CropBox.URX, URY: p.CropBox.URY}
	long := box.URX - box.LLX
	if h := box.URY - box.LLY; h > long {
		long = h
	}
	if !(long > 0) {
		return nil, fmt.Errorf("byblos: RenderPage: page %d has a degenerate crop box %gx%g",
			page, box.URX-box.LLX, box.URY-box.LLY)
	}
	// /Rotate turns the raster but does not change which edge is longer, so
	// the scale is taken from the unrotated box.
	img, err := render.Page(ctx, p.Content, box, p.Rotate, float64(longEdgePx)/long,
		renderImages(d, p), renderFonts(d, p))
	if err != nil {
		return nil, fmt.Errorf("byblos: RenderPage: page %d: %w", page, err)
	}
	return img, nil
}

// renderImages resolves a Do operand to a decoded image XObject, through the
// same seam the extract path uses: pdfdoc.RawImage, then image.Decode.
//
// ok=false skips the draw and leaves the rest of the page renderable, which is
// what makes a codec byblos does not decode a missing element rather than a
// failed page. JBIG2 and JPX are refused here by name: RawImage hands both
// back as opaque bytes with a file type and no error, so image.Decode would be
// asked to guess at them.
func renderImages(d pdfdoc.Doc, p *pdfdoc.Page) render.ImageFor {
	return func(name string) (render.Image, bool) {
		xo, ok := d.XObject(p.Scope, name)
		if !ok || !xo.Image {
			return render.Image{}, false
		}
		data, fileType, err := d.RawImage(xo.ID)
		if err != nil || fileType == "jbig2" || fileType == "jpx" {
			return render.Image{}, false
		}
		im, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return render.Image{}, false
		}
		return render.Image{Data: im}, true
	}
}

// renderFonts resolves a Tf operand through pdfdoc.RenderFont. ok=false leaves
// the glyphs undrawn without failing the page, the same degradation
// renderImages gives an undecodable raster.
func renderFonts(d pdfdoc.Doc, p *pdfdoc.Page) render.FontFor {
	return func(name string) (render.Font, bool) {
		rf, ok := d.RenderFont(p.Scope, name)
		if !ok {
			return render.Font{}, false
		}
		return render.Font{
			Program: rf.Program, BaseFont: rf.BaseFont, Flags: rf.Flags,
			FirstChar: rf.FirstChar, Widths: rf.Widths,
			Type0: rf.Type0, W: rf.W, DW: rf.DW,
		}, true
	}
}

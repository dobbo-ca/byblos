package byblos

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/dobbo-ca/byblos/internal/limits"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// Document is a PDF parsed once. Info and PageRaster read from that one parse,
// so N pages cost one pdfcpu parse rather than N+1; the one-shot InspectDocument
// is OpenDocument, Info, Close. Its object table and one stream decode are
// reserved from the memory budget (SetMemoryLimit) until Close.
//
// The reader is read in OpenDocument only. A Document is not safe for
// concurrent use. Per-page state (decoded streams, the walk's records) is reset
// by each page's walk and a returned raster is the caller's, so a page's memory
// is collectable once the caller drops it.
type Document struct {
	d       pdfdoc.Doc
	held    *limits.Held
	release func()
}

// OpenDocument parses r under lim, checked before anything is allocated. The
// parse is not interruptible (see InspectContext); a ctx ended before or during
// it is noticed after. Close the result.
func OpenDocument(ctx context.Context, r io.ReadSeeker, lim Limits) (*Document, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	d, held, release, err := pdfdoc.OpenBudgetedHeld(ctx, r, lim)
	if err != nil {
		if pdfdoc.IsWrongPassword(err) {
			return nil, fmt.Errorf("%w: %w", ErrPassword, err)
		}
		return nil, err
	}
	return &Document{d: d, held: held, release: release}, nil
}

// Close gives back the memory reservation and drops the parse. It is safe to
// call twice.
func (doc *Document) Close() {
	if doc.release != nil {
		doc.release()
	}
	doc.d, doc.release = nil, nil
}

// PageCount is the number of pages.
func (doc *Document) PageCount() int { return doc.d.PageCount() }

// PageRaster is ExtractPageRasterLimits for one page of the open document: the
// same extraction, the same caps, no parse.
func (doc *Document) PageRaster(ctx context.Context, page int) (*PageRaster, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	pr, _, err := extractPage(limits.WithHeld(ctx, doc.held), doc.d, page)
	return pr, err
}

// Info is InspectDocument over the document OpenDocument parsed: the same loop,
// without a second parse.
func (doc *Document) Info(ctx context.Context) (*DocumentInfo, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	ctx = limits.WithHeld(ctx, doc.held)
	d := doc.d
	cat := d.Catalog()
	di := &DocumentInfo{
		Pages:          make([]PageInfo, 0, d.PageCount()),
		Encrypted:      d.EncryptInfo().Encrypted,
		HasSignature:   cat.HasSignature,
		NeedsRendering: cat.NeedsRendering,
	}
	images := 0
	dl := d.Limits()
	for n := 1; n <= d.PageCount(); n++ {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		pi, _, err := inspectPage(ctx, d, n)
		// A cap or budget refusal is the document's, not a page byblos worked
		// around: it fails the call, whichever layer noticed it.
		if derr := d.Err(); derr != nil {
			err = derr
		}
		// The walk caps at their defaults stay page diagnostics, as before
		// they were LimitErrors; a caller who set one asked for the failure.
		if (errors.Is(err, ErrLimit) && !defaultWalkCap(err, dl)) || errors.Is(err, ErrMemoryLimit) {
			return nil, err
		}
		if err != nil {
			// Cancellation is not a defect in the document. Reporting it as a
			// page diagnostic would hand back a document that was never read.
			if cerr := checkContext(ctx); cerr != nil {
				return nil, cerr
			}
			// Everything else is a page byblos worked around. inspectPage
			// returns whatever it did establish, which is nothing at all only
			// when the page dictionary itself would not resolve.
			if pi == nil {
				pi = &PageInfo{Index: n}
			}
			pi.Diagnostics = append(pi.Diagnostics, Diagnostic{
				Severity: SeverityError,
				Message:  err.Error(),
			})
		}
		// The per-page caps do not bound what the pages keep together: pages
		// can share one content stream. The same caps hold for the document.
		images += len(pi.Images)
		if err := limits.Check("images", int64(images), dl.Images()+dl.InlineImages()); err != nil {
			return nil, err
		}
		di.Pages = append(di.Pages, *pi)
	}
	return di, nil
}

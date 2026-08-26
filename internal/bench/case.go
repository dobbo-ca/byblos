package bench

import (
	"bytes"
	"errors"
	"fmt"
	"image"

	"github.com/dobbo-ca/byblos"
)

// ErrIneligible reports that a document cannot exercise a capability -- a
// born-digital page has no raster to extract, a page with no JPEG has nothing
// to recompress. It is a skip, not a failure: the harness records no sample and
// moves on.
var ErrIneligible = errors.New("bench: document is ineligible for this capability")

// JPEGQuality is the quality the jpeg-recompress case always uses.
//
// It is a constant rather than a parameter because it governs how much of the
// image is discarded. A candidate that lowered it would post a large size win
// for strictly worse output, which is the one way this harness could be made to
// reward damage (spec section 6, rule 3).
const JPEGQuality = 75

// Case is how one capability is exercised.
//
// Prepare builds the input and is NOT measured. Run does the work that is
// measured and returns the bytes it produced, or nil for a capability that
// produces no encoded output.
type Case struct {
	Prepare func(doc []byte) (any, error)
	Run     func(in any) ([]byte, error)
}

// CaseFor returns the case for a capability string.
func CaseFor(capability string) (Case, bool) {
	c, ok := cases[capability]
	return c, ok
}

// rasterOf extracts page 1's raster, translating the two "this document is not
// eligible" outcomes into ErrIneligible.
func rasterOf(doc []byte) (image.Image, error) {
	pr, err := byblos.ExtractPageRaster(bytes.NewReader(doc), 1)
	switch {
	case errors.Is(err, byblos.ErrNotSingleRaster):
		return nil, ErrIneligible
	case err != nil:
		return nil, fmt.Errorf("extract: %w", err)
	}
	return pr.Image, nil
}

// passthrough is Prepare for a case that measures a call taking the raw PDF.
func passthrough(doc []byte) (any, error) { return doc, nil }

var cases = map[string]Case{
	"inspect": {
		Prepare: passthrough,
		Run: func(in any) ([]byte, error) {
			_, err := byblos.Inspect(bytes.NewReader(in.([]byte)))
			return nil, err
		},
	},

	"extract-raster": {
		Prepare: passthrough,
		Run: func(in any) ([]byte, error) {
			_, err := byblos.ExtractPageRaster(bytes.NewReader(in.([]byte)), 1)
			if errors.Is(err, byblos.ErrNotSingleRaster) {
				return nil, ErrIneligible
			}
			return nil, err
		},
	},

	"jbig2-generic": {
		// Extraction and binarisation are setup. Only the encode is measured.
		Prepare: func(doc []byte) (any, error) {
			img, err := rasterOf(doc)
			if err != nil {
				return nil, err
			}
			return byblos.Sauvola(img)
		},
		Run: func(in any) ([]byte, error) {
			return byblos.EncodeJBIG2Generic(in.(*byblos.Bitmap))
		},
	},

	"quantize-png": {
		Prepare: func(doc []byte) (any, error) { return rasterOf(doc) },
		Run: func(in any) ([]byte, error) {
			return byblos.QuantizePNG(in.(image.Image), 256)
		},
	},

	"downsample": {
		Prepare: func(doc []byte) (any, error) { return rasterOf(doc) },
		Run: func(in any) ([]byte, error) {
			_, err := byblos.Downsample(in.(image.Image), 300, 150)
			return nil, err
		},
	},

	"build-pdf": {
		Prepare: func(doc []byte) (any, error) {
			img, err := rasterOf(doc)
			if err != nil {
				return nil, err
			}
			enc, err := byblos.QuantizeIndexed(img, 256)
			if err != nil {
				return nil, err
			}
			return []byblos.BuildPage{{Image: enc, DPI: 300}}, nil
		},
		Run: func(in any) ([]byte, error) {
			var buf bytes.Buffer
			if err := byblos.BuildPDF(&buf, in.([]byblos.BuildPage)); err != nil {
				return nil, err
			}
			return buf.Bytes(), nil
		},
	},

	"text-layer": {
		Prepare: func(doc []byte) (any, error) {
			pages, err := byblos.Inspect(bytes.NewReader(doc))
			if err != nil {
				return nil, err
			}
			if len(pages) == 0 {
				return nil, ErrIneligible
			}
			return stampInput{doc: doc, layer: syntheticLayer(len(pages))}, nil
		},
		Run: func(in any) ([]byte, error) {
			si := in.(stampInput)
			var buf bytes.Buffer
			if err := byblos.StampTextLayer(&buf, bytes.NewReader(si.doc), si.layer); err != nil {
				return nil, err
			}
			return buf.Bytes(), nil
		},
	},

	"jpeg-recompress": {
		Prepare: passthrough,
		Run: func(in any) ([]byte, error) {
			var buf bytes.Buffer
			opts := byblos.OptimizeOptions{RecompressJPEG: true, JPEGQuality: JPEGQuality}
			if err := byblos.Optimize(&buf, bytes.NewReader(in.([]byte)), opts); err != nil {
				return nil, err
			}
			return buf.Bytes(), nil
		},
	},

	"linearize": {
		Prepare: passthrough,
		Run: func(in any) ([]byte, error) {
			var buf bytes.Buffer
			opts := byblos.OptimizeOptions{Linearize: true}
			if err := byblos.Optimize(&buf, bytes.NewReader(in.([]byte)), opts); err != nil {
				return nil, err
			}
			return buf.Bytes(), nil
		},
	},
}

// stampInput carries both halves StampTextLayer needs through the any.
type stampInput struct {
	doc   []byte
	layer byblos.TextLayer
}

// syntheticLayer builds a fixed text layer of printable ASCII.
//
// It is synthetic rather than real OCR output because the measurement is of
// StampTextLayer's cost per word, and a fixed word count makes that comparable
// across documents. Every rune is inside the glyphless font's coverage, so
// ErrUnstampableRune cannot fire and turn a measurement into a failure.
func syntheticLayer(pages int) byblos.TextLayer {
	const wordsPerPage = 400
	tl := byblos.TextLayer{Pages: make([][]byblos.PositionedWord, pages)}
	for p := range tl.Pages {
		words := make([]byblos.PositionedWord, wordsPerPage)
		for i := range words {
			col, row := i%10, i/10
			x, y := 40+col*50, 40+row*18
			words[i] = byblos.PositionedWord{
				Text:   "benchmark",
				Bounds: image.Rect(x, y, x+45, y+12),
			}
		}
		tl.Pages[p] = words
	}
	return tl
}

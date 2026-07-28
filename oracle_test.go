package byblos

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

type oracleImage struct {
	Page   int `json:"page"`
	Width  int `json:"width"`
	Height int `json:"height"`
	BPC    int `json:"bpc"`
}

type oracleRaster struct {
	Page   int    `json:"page"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Pixels string `json:"pixels_sha256"`
}

type oracleDoc struct {
	Pages      int            `json:"pages"`
	PageWidth  float64        `json:"page_width_pt"`
	PageHeight float64        `json:"page_height_pt"`
	Images     []oracleImage  `json:"images"`
	Rasters    []oracleRaster `json:"rasters"`
	HasText    bool           `json:"has_text"`
	Error      string         `json:"error,omitempty"`
}

type oracleFile struct {
	Tools     string               `json:"tools"`
	Documents map[string]oracleDoc `json:"documents"`
}

func loadOracle(t *testing.T) oracleFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "oracle", "poppler.json"))
	if err != nil {
		t.Skipf("poppler golden not present (run make oracle): %v", err)
	}
	var o oracleFile
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("parsing the poppler golden: %v", err)
	}
	if o.Tools == "" {
		t.Fatal("the golden records no tool versions; regenerate with make oracle")
	}
	return o
}

// pixelHash must stay byte-for-byte identical to the one in
// testdata/oracle/gen.go. See the note there for why it is duplicated.
func pixelHash(im image.Image) string {
	b := im.Bounds()
	h := sha256.New()
	fmt.Fprintf(h, "%dx%d;", b.Dx(), b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := im.At(x, y).RGBA()
			h.Write([]byte{byte(r >> 8), byte(g >> 8), byte(bl >> 8), byte(a >> 8)})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Inspect must agree with poppler on page count, page size, and every image's
// pixel dimensions. Those are facts about the file, not judgements, so the
// comparison is exact.
func TestInspectAgreesWithPoppler(t *testing.T) {
	o := loadOracle(t)
	t.Logf("golden generated with %s", o.Tools)

	for _, d := range corpus.All() {
		want, ok := o.Documents[d.Name]
		if !ok {
			t.Errorf("golden has no entry for %q; regenerate with make oracle", d.Name)
			continue
		}
		t.Run(d.Name, func(t *testing.T) {
			got, err := Inspect(bytes.NewReader(d.Data))
			if want.Error != "" || want.Pages == 0 {
				if err == nil {
					t.Fatalf("poppler rejected %q but Inspect succeeded", d.Name)
				}
				return
			}
			if err != nil {
				t.Fatalf("Inspect() error = %v, but poppler read %d pages", err, want.Pages)
			}
			if len(got) != want.Pages {
				t.Fatalf("page count = %d; poppler says %d", len(got), want.Pages)
			}
			for _, pi := range got {
				if w := float64(pi.Bounds.Dx()); math.Abs(w-want.PageWidth) > 1 {
					t.Errorf("page %d width = %v pt; poppler says %v", pi.Index, w, want.PageWidth)
				}
				if h := float64(pi.Bounds.Dy()); math.Abs(h-want.PageHeight) > 1 {
					t.Errorf("page %d height = %v pt; poppler says %v", pi.Index, h, want.PageHeight)
				}
			}

			// Compare image pixel dimensions as multisets: poppler lists stored
			// image objects, Byblos lists paintings of them, and the two orders
			// need not agree.
			gotDims := map[[2]int]int{}
			for _, pi := range got {
				for _, im := range pi.Images {
					gotDims[[2]int{im.Width, im.Height}]++
				}
			}
			wantDims := map[[2]int]int{}
			for _, im := range want.Images {
				wantDims[[2]int{im.Width, im.Height}]++
			}
			for k, n := range wantDims {
				if gotDims[k] != n {
					t.Errorf("images %dx%d: Inspect found %d, pdfimages found %d",
						k[0], k[1], gotDims[k], n)
				}
			}
			for k, n := range gotDims {
				if wantDims[k] == 0 {
					t.Errorf("Inspect reported %d images of %dx%d that pdfimages did not list",
						n, k[0], k[1])
				}
			}

			// TextChars is a born-digital signal, so the oracle assertion is the
			// bi-conditional, not a character count: pdftotext normalises
			// whitespace and reading order, and matching its exact length would
			// assert poppler's formatting rather than our extraction.
			var chars int
			for _, pi := range got {
				chars += pi.TextChars
			}
			if (chars > 0) != want.HasText {
				t.Errorf("TextChars total = %d (text present: %v); pdftotext says text present: %v",
					chars, chars > 0, want.HasText)
			}
		})
	}
}

// ExtractPageRaster must return the same pixels poppler does. Dimensions alone
// would not catch a faithless re-render.
//
// A page Byblos diverts is skipped, not failed: poppler has no notion of "single
// page-covering raster", so `pdfimages -png` writing a file for overlay-text
// says nothing about whether that page should have been extracted.
func TestExtractedRasterMatchesPdfimages(t *testing.T) {
	o := loadOracle(t)
	compared := 0
	for _, d := range corpus.All() {
		want, ok := o.Documents[d.Name]
		if !ok {
			t.Errorf("golden has no entry for %q; regenerate with make oracle", d.Name)
			continue
		}
		for _, rr := range want.Rasters {
			pr, err := ExtractPageRaster(bytes.NewReader(d.Data), rr.Page)
			if errors.Is(err, ErrNotSingleRaster) || errors.Is(err, ErrUnsupportedImageCodec) {
				t.Logf("%s page %d: diverted, no disagreement with poppler (%v)", d.Name, rr.Page, err)
				continue
			}
			if err != nil {
				t.Errorf("%s page %d: ExtractPageRaster error = %v, but pdfimages wrote a %dx%d PNG",
					d.Name, rr.Page, err, rr.Width, rr.Height)
				continue
			}
			if b := pr.Image.Bounds(); b.Dx() != rr.Width || b.Dy() != rr.Height {
				t.Errorf("%s page %d: raster %dx%d; pdfimages %dx%d",
					d.Name, rr.Page, b.Dx(), b.Dy(), rr.Width, rr.Height)
				continue
			}
			if got := pixelHash(pr.Image); got != rr.Pixels {
				t.Errorf("%s page %d: pixels %s; pdfimages %s", d.Name, rr.Page, got, rr.Pixels)
			}
			compared++
		}
	}
	// Without this, a regression that diverted every page would pass silently.
	if compared == 0 {
		t.Error("no page was compared against pdfimages; the oracle is vacuous")
	}
	t.Logf("compared %d pages against pdfimages", compared)
}

// Bitonal detection has no poppler equivalent worth parsing beyond the bpc
// column, so assert against that directly, in both directions: the corpus scan
// is 8-bit grey and must not be reported bitonal, the jbig2 document is 1-bit
// and must be.
func TestInspectBitonalFlagMatchesBitsPerComponent(t *testing.T) {
	o := loadOracle(t)
	for _, name := range []string{"scan", "jbig2"} {
		t.Run(name, func(t *testing.T) {
			want := o.Documents[name]
			// A golden with the wrong shape is a bug in the generator, not a
			// reason to skip: skipping here is how an empty image table goes
			// unnoticed.
			if len(want.Images) != 1 {
				t.Fatalf("golden for %s has %d image rows; want exactly 1 — regenerate with make oracle",
					name, len(want.Images))
			}
			pages, err := Inspect(bytes.NewReader(corpusDoc(t, name)))
			if err != nil {
				t.Fatalf("Inspect() error = %v", err)
			}
			if len(pages) != 1 || len(pages[0].Images) != 1 {
				t.Fatalf("Inspect() = %+v; want one page with one image", pages)
			}
			if got := pages[0].Images[0].Bitonal; got != (want.Images[0].BPC == 1) {
				t.Errorf("Bitonal = %v; pdfimages reports bpc %d", got, want.Images[0].BPC)
			}
		})
	}
}

package byblos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	imagepng "image/png"
	"math/rand"
	"reflect"
	"runtime"
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/dobbo-ca/byblos/internal/limits"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// errStr keeps error text comparable across nil and non-nil.
func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// The open-once render and inspect equal the one-shot ones for every corpus
// document and page, error text included, at each budget and with none.
func TestOpenDocumentMatchesOneShot(t *testing.T) {
	ctx := context.Background()
	for _, budget := range []int64{0, 64 << 20, 300 << 20, 1024 << 20} {
		prev := SetMemoryLimit(budget)
		for _, c := range corpus.All() {
			t.Run(fmt.Sprintf("%d/%s", budget>>20, c.Name), func(t *testing.T) {
				want, werr := InspectDocument(ctx, bytes.NewReader(c.Data), Limits{})
				doc, err := OpenDocument(ctx, bytes.NewReader(c.Data), Limits{})
				if err != nil {
					if werr == nil || errStr(err) != errStr(werr) {
						t.Fatalf("open err = %v; one-shot err = %v", err, werr)
					}
					return
				}
				defer doc.Close()
				got, gerr := doc.Info(ctx)
				if errStr(gerr) != errStr(werr) || !reflect.DeepEqual(got, want) {
					t.Fatalf("Info = %+v, %v; want %+v, %v", got, gerr, want, werr)
				}
				for n := 1; n <= doc.PageCount(); n++ {
					w, werr := ExtractPageRasterLimits(ctx, bytes.NewReader(c.Data), n, Limits{})
					g, gerr := doc.PageRaster(ctx, n)
					if errStr(gerr) != errStr(werr) || !reflect.DeepEqual(g, w) {
						t.Errorf("page %d: %+v, %v; want %+v, %v", n, g, gerr, w, werr)
					}
				}
			})
		}
		SetMemoryLimit(prev)
	}
}

// While a Document is open, a one-shot call on another document does not wait
// behind it: the Document holds its parse, not a decode slot.
func TestOpenDocumentDoesNotStarveOthers(t *testing.T) {
	for _, budget := range []int64{64 << 20, 300 << 20} {
		prev := SetMemoryLimit(budget)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		doc, err := OpenDocument(ctx, bytes.NewReader(corpusDoc(t, "booklet")), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := doc.PageRaster(ctx, 1); err != nil {
			t.Fatalf("budget %d: page: %v", budget, err)
		}
		if _, err := ExtractPageRasterLimits(ctx, bytes.NewReader(corpusDoc(t, "scan")), 1, Limits{}); err != nil {
			t.Errorf("budget %d: one-shot while a Document is open: %v", budget, err)
		}
		doc.Close()
		cancel()
		SetMemoryLimit(prev)
	}
}

// A page's peak reservation (the open parse plus the page's own) covers what the
// page allocates while it runs, and a page leaves nothing reserved behind.
func TestOpenDocumentPageReservationCoversLiveBytes(t *testing.T) {
	prev := SetMemoryLimit(300 << 20)
	defer SetMemoryLimit(prev)
	oldGC := debug.SetGCPercent(1)
	defer debug.SetGCPercent(oldGC)
	ctx := context.Background()
	var buf bytes.Buffer
	if err := BuildPDF(&buf, []BuildPage{{Image: flateGrayImage(t, 1000, 1000, 1), WidthPt: 612, HeightPt: 792, DPI: 100}}); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"flate": buf.Bytes(), "jpeg": corpus.ScanJPEG()} {
		doc, err := OpenDocument(ctx, bytes.NewReader(data), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		base := int64(m.HeapAlloc)
		limits.ResetPeak()
		var peak atomic.Int64
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			var s runtime.MemStats
			for {
				runtime.ReadMemStats(&s)
				for old := peak.Load(); int64(s.HeapAlloc) > old && !peak.CompareAndSwap(old, int64(s.HeapAlloc)); old = peak.Load() {
				}
				select {
				case <-stop:
					return
				default:
				}
			}
		}()
		if _, err := doc.PageRaster(ctx, 1); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		close(stop)
		<-done
		live, reserved := peak.Load()-base, limits.PeakReserved()
		t.Logf("%s: live %d, peak reserved %d", name, live, reserved)
		if live > reserved {
			t.Errorf("%s: page held %d bytes, reserved only %d", name, live, reserved)
		}
		doc.Close()
	}
}

// N pages cost one parse; the one-shot path costs one per page.
func TestOpenDocumentParsesOnce(t *testing.T) {
	ctx := context.Background()
	data := corpusDoc(t, "booklet")
	before := pdfdoc.Parses.Load()
	doc, err := OpenDocument(ctx, bytes.NewReader(data), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Info(ctx); err != nil {
		t.Fatal(err)
	}
	n := doc.PageCount()
	for p := 1; p <= n; p++ {
		if _, err := doc.PageRaster(ctx, p); err != nil {
			t.Fatalf("page %d: %v", p, err)
		}
	}
	doc.Close()
	if got := pdfdoc.Parses.Load() - before; got != 1 {
		t.Errorf("open-once: %d parses for %d pages; want 1", got, n)
	}
	before = pdfdoc.Parses.Load()
	for p := 1; p <= n; p++ {
		ExtractPageRasterLimits(ctx, bytes.NewReader(data), p, Limits{})
	}
	if got := pdfdoc.Parses.Load() - before; got != int64(n) {
		t.Errorf("one-shot: %d parses for %d pages; want %d", got, n, n)
	}
}

// Limits are checked at open and again as the pages are read; ctx is honoured
// between pages.
func TestOpenDocumentLimitsAndCancel(t *testing.T) {
	ctx := context.Background()
	data := corpusDoc(t, "dup-raster")
	var le *LimitError
	if _, err := OpenDocument(ctx, bytes.NewReader(data), Limits{MaxObjects: 1}); !errors.As(err, &le) {
		t.Errorf("open under MaxObjects 1: err = %v; want *LimitError", err)
	}
	// Eight one-image pages against a document cap of two: it trips on the
	// third page, mid-document.
	doc, err := OpenDocument(ctx, bytes.NewReader(corpusDoc(t, "booklet")), Limits{MaxImages: 1, MaxInlineImages: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	if _, err := doc.Info(ctx); !errors.As(err, &le) || le.Name != "images" {
		t.Errorf("Info under an image cap of 2: err = %v; want *LimitError images", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	if _, err := doc.PageRaster(cctx, 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := doc.PageRaster(cctx, 2); !errors.Is(err, context.Canceled) {
		t.Errorf("PageRaster after cancel: err = %v; want context.Canceled", err)
	}
	if _, err := doc.Info(cctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Info after cancel: err = %v; want context.Canceled", err)
	}
}

// An open Document reserves nothing: the whole budget is reservable while it is
// open, and Close leaves no goroutine behind.
func TestOpenDocumentCloseReleases(t *testing.T) {
	const budget = 300 << 20
	prev := SetMemoryLimit(budget)
	defer SetMemoryLimit(prev)
	ctx := context.Background()
	data := corpusDoc(t, "booklet")
	runtime.GC()
	g0 := runtime.NumGoroutine()

	doc, err := OpenDocument(ctx, bytes.NewReader(data), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Info(ctx); err != nil {
		t.Fatal(err)
	}
	tctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	rel, err := limits.Reserve(tctx, budget)
	cancel()
	if err != nil {
		t.Errorf("whole budget not reservable while the document is open: %v", err)
	} else {
		rel()
	}
	doc.Close()
	doc.Close()

	for i := 0; i < 50 && runtime.NumGoroutine() > g0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if g := runtime.NumGoroutine(); g > g0 {
		t.Errorf("goroutines %d after Close; %d before", g, g0)
	}
}

// A page whose own need fits the budget renders open-once whenever it renders
// one-shot, even when the budget is under the need plus the document's parse:
// the Document must not wait on bytes only its own Close would give back.
func TestOpenDocumentPageFitsBudgetWithoutParseHeadroom(t *testing.T) {
	var buf bytes.Buffer
	if err := BuildPDF(&buf, []BuildPage{{Image: flateGrayImage(t, 1000, 1000, 1), WidthPt: 612, HeightPt: 792, DPI: 100}}); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	prev := SetMemoryLimit(1 << 30)
	defer SetMemoryLimit(prev)
	limits.ResetPeak()
	if _, err := ExtractPageRasterLimits(context.Background(), bytes.NewReader(data), 1, Limits{}); err != nil {
		t.Fatal(err)
	}
	need := limits.PeakReserved()
	SetMemoryLimit(need + 16)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	want, werr := ExtractPageRasterLimits(ctx, bytes.NewReader(data), 1, Limits{})
	doc, err := OpenDocument(ctx, bytes.NewReader(data), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	got, gerr := doc.PageRaster(ctx, 1)
	if werr != nil || gerr != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("budget %d: open-once err=%v; one-shot err=%v; equal=%v", need+16, gerr, werr, reflect.DeepEqual(got, want))
	}
	// And a one-shot call on another document is not starved meanwhile.
	if _, err := ExtractPageRasterLimits(ctx, bytes.NewReader(corpus.ScanJPEG()), 1, Limits{}); err != nil {
		t.Errorf("one-shot beside the open Document: %v", err)
	}
}

// openOnceFitsBudget asserts the page renders open-once under need+16, where need is
// what the one-shot render of it reserved at its peak: every lazily loaded stream the
// page reads is reserved once, inside the reservation the one-shot path takes.
func openOnceFitsBudget(t *testing.T, data []byte) {
	t.Helper()
	prev := SetMemoryLimit(1 << 30)
	defer SetMemoryLimit(prev)
	limits.ResetPeak()
	if _, err := ExtractPageRasterLimits(context.Background(), bytes.NewReader(data), 1, Limits{}); err != nil {
		t.Fatal(err)
	}
	need := limits.PeakReserved()
	SetMemoryLimit(need + 16)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	want, werr := ExtractPageRasterLimits(ctx, bytes.NewReader(data), 1, Limits{})
	doc, err := OpenDocument(ctx, bytes.NewReader(data), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	got, gerr := doc.PageRaster(ctx, 1)
	if werr != nil || gerr != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("budget %d: open-once err=%v; one-shot err=%v; equal=%v", need+16, gerr, werr, reflect.DeepEqual(got, want))
	}
}

// Masked, codec and globals pages fit the same budget open-once as one-shot.
func TestOpenDocumentFitsBudgetMasksCodecsAndGlobals(t *testing.T) {
	const side = 256
	px := make([]byte, side*side)
	rand.New(rand.NewSource(2)).Read(px)
	dctSMask := func(t *testing.T) []byte {
		enc, _, err := EmbedJPEG(dcOnlyJPEG(side, side, 1, 1))
		if err != nil {
			t.Fatal(err)
		}
		enc.SMask = &EncodedImage{Width: side, Height: side, BPC: 8, ColorSpace: ColorSpace{Name: "DeviceGray"}, Filter: "FlateDecode", Data: flateEncode(t, px)}
		var buf bytes.Buffer
		if err := BuildPDF(&buf, []BuildPage{{Image: enc, WidthPt: 612, HeightPt: 792}}); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	for _, c := range []struct {
		name string
		pdf  func(*testing.T) []byte
	}{
		{"flate-smask", func(t *testing.T) []byte { return flateImagePDF(t, 600, 600, 3, true) }},
		{"dct-flate-smask", dctSMask},
		{"jbig2-globals", func(t *testing.T) []byte {
			return jbig2GlobalsPDF(symbolFixture(t, "symbol-page.jb2"), symbolFixture(t, "symbol-globals.jb2"), 640, 480)
		}},
		{"cmyk-dct", func(t *testing.T) []byte { return dctPDF(t, img2pdfFile(t, "cmyk.jpg"), 16, 12) }},
	} {
		t.Run(c.name, func(t *testing.T) { openOnceFitsBudget(t, c.pdf(t)) })
	}
}

// A page whose JPEG decode reserves memory must not queue behind the document's
// own reservation: under a budget the one-shot render and the open-once render
// of it agree, and neither waits (a held ctx would end the test by its deadline).
func TestOpenDocumentJPEGPageUnderBudget(t *testing.T) {
	data := corpus.ScanJPEG()
	for _, budget := range []int64{64 << 20, 300 << 20, 1024 << 20} {
		prev := SetMemoryLimit(budget)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		want, werr := ExtractPageRasterLimits(ctx, bytes.NewReader(data), 1, Limits{})
		doc, err := OpenDocument(ctx, bytes.NewReader(data), Limits{})
		if err != nil {
			t.Fatalf("budget %d: open: %v", budget, err)
		}
		got, gerr := doc.PageRaster(ctx, 1)
		doc.Close()
		cancel()
		SetMemoryLimit(prev)
		if werr != nil || gerr != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("budget %d: open-once err=%v; one-shot err=%v; equal=%v", budget, gerr, werr, reflect.DeepEqual(got, want))
		}
	}
}

// An open Document holds the file's parse, not its image bytes, and a page's
// images are gone when the page is: live heap after OpenDocument, and after
// every page has run, stays well under the file's size.
func TestOpenDocumentHoldsNoImageBytes(t *testing.T) {
	const pages, w, h = 24, 512, 256
	rng := rand.New(rand.NewSource(1))
	bp := make([]BuildPage, pages)
	for i := range bp {
		px := make([]byte, w*h)
		rng.Read(px)
		bp[i] = BuildPage{Image: EncodedImage{Width: w, Height: h, BPC: 8, ColorSpace: ColorSpace{Name: "DeviceGray"}, Filter: "FlateDecode", Data: flateEncode(t, px)}, WidthPt: 612, HeightPt: 792, DPI: 100}
	}
	var buf bytes.Buffer
	if err := BuildPDF(&buf, bp); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	live := func() uint64 {
		var m runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	ctx := context.Background()
	base := live()
	doc, err := OpenDocument(ctx, bytes.NewReader(data), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	if grew := live() - base; grew > uint64(len(data))/4 {
		t.Errorf("after open, live heap grew %d bytes for a %d-byte file", grew, len(data))
	}
	for p := 1; p <= pages; p++ {
		if _, err := doc.PageRaster(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if grew := live() - base; grew > uint64(len(data))/4 {
		t.Errorf("after %d pages, live heap grew %d bytes for a %d-byte file", pages, grew, len(data))
	}
}

// A Flate image with a Flate /SMask (what TranscodePNG writes for alpha) renders
// from an open Document as the one-shot path renders it: the mask is an image
// stream whose bytes the lazy parse left in the file.
func TestOpenDocumentFlateSoftMask(t *testing.T) {
	const w, h = 64, 32
	src := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range src.Pix {
		src.Pix[i] = byte(i * 7)
	}
	var png bytes.Buffer
	if err := imagepng.Encode(&png, src); err != nil {
		t.Fatal(err)
	}
	enc, _, err := TranscodePNG(context.Background(), &png, Limits{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := BuildPDF(&buf, []BuildPage{{Image: enc, WidthPt: 612, HeightPt: 792}}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	want, werr := ExtractPageRasterLimits(ctx, bytes.NewReader(buf.Bytes()), 1, Limits{})
	doc, err := OpenDocument(ctx, bytes.NewReader(buf.Bytes()), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	got, gerr := doc.PageRaster(ctx, 1)
	if errStr(gerr) != errStr(werr) || !reflect.DeepEqual(got, want) {
		t.Fatalf("doc: %v; one-shot: %v (nil: %v / %v)", gerr, werr, got == nil, want == nil)
	}
	if want == nil {
		t.Fatal("one-shot rendered nothing")
	}
}

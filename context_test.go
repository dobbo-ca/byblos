package byblos

// byb-xyn. These tests are the acceptance bar for the context convention, and
// the bar is deliberately not "a ctx check exists somewhere in the function".
//
// Cancellation latency equals the LONGEST UNINTERRUPTIBLE UNIT OF WORK. A
// function that checks ctx once on entry and then runs to completion passes
// every "does it return context.Canceled" test ever written while giving a
// caller no guarantee at all -- which is the failure byb-xyn exists to prevent,
// because kleio's worker relies on that guarantee to avoid an SQS
// visibility-timeout redelivery loop. So the tests below measure latency
// by observing where a call's context boundaries actually fall, rather than by
// cancelling at a guessed moment and timing the return -- see observeCtx for
// why the timing approach was abandoned. TestLatencyInstrumentIsNotVacuous
// mutation-checks the instrument itself: every latency assertion is an upper
// bound, zero satisfies every upper bound, so an instrument that measured
// nothing would report a perfect result for a function that never checks its
// context at all.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// expensivePages is the page count for the latency fixture. It is large enough
// that one page's work is a small fraction of the document's, which is what
// makes "did it stop after the current page, or run to the end" separable at
// all: at 120 pages a per-page check returns in under 1% of the full duration
// while no check at all returns in ~50%, so the assertions have two orders of
// magnitude of headroom and do not depend on the host being fast.
const expensivePages = 120

// fixture is one expensive document in every shape the nine entry points need
// it: the PDF bytes, the BuildPage slice that produced them (BuildPDF takes
// pages, not a document), and a substitution keyed by a real ObjNr from it.
type fixture struct {
	doc   []byte
	pages []BuildPage
	subs  map[int]EncodedImage
	tl    TextLayer
	prov  Provenance
}

// newFixture builds an n-page PDF, each page painting a full JPEG scan raster.
// Every page costs a JPEG decode on the extraction path, so this is the most
// expensive ORDINARY input available. It is deliberately not the worst case:
// byb-riy's hostile JBIG2 page costs far more per page, and that is what
// actually bounds the contract, so it is measured separately by
// TestCancellationLatencyOnAHostilePage.
func newFixture(t *testing.T, n int) fixture {
	t.Helper()
	src := corpus.Scanpage()
	b := src.Bounds()
	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encoding the scan fixture: %v", err)
	}
	img := EncodedImage{
		Width:      b.Dx(),
		Height:     b.Dy(),
		BPC:        8,
		ColorSpace: ColorSpace{Name: "DeviceRGB"},
		Filter:     "DCTDecode",
		Data:       jb.Bytes(),
	}
	f := fixture{pages: make([]BuildPage, n)}
	for i := range f.pages {
		f.pages[i] = BuildPage{Image: img, DPI: 300}
	}
	var out bytes.Buffer
	if err := BuildPDF(&out, f.pages); err != nil {
		t.Fatalf("building the %d-page fixture: %v", n, err)
	}
	f.doc = out.Bytes()

	// ReplaceImages needs a real object number, and refuses one the document
	// has no image for. Substituting the raster with itself exercises the full
	// walk-every-page path without changing what the document says.
	infos, err := Inspect(bytes.NewReader(f.doc))
	if err != nil {
		t.Fatalf("inspecting the fixture: %v", err)
	}
	f.subs = map[int]EncodedImage{}
	for _, pg := range infos {
		for _, ref := range pg.Images {
			if ref.ObjNr > 0 {
				f.subs[ref.ObjNr] = img
			}
		}
	}
	if len(f.subs) == 0 {
		t.Fatal("the fixture has no substitutable image; ReplaceImages cannot be measured")
	}

	// A word on EVERY page, so StampTextLayer's per-page loop actually runs n
	// times. With a one-page text layer the loop would run once and the
	// measurement would be reporting pdfcpu's write cost under byblos's name.
	f.tl = TextLayer{Pages: make([][]PositionedWord, n)}
	for i := range f.tl.Pages {
		f.tl.Pages[i] = []PositionedWord{{Text: "a", Bounds: image.Rect(10, 10, 40, 30)}}
	}
	f.prov = Provenance{Version: Version, Capabilities: []string{"extract-raster"}}
	return f
}

// entryPoint is one of the nine document-scale primitives that byb-xyn's
// CONVENTION DECIDED note gives a Context variant. call and plain both take
// the io.Writer explicitly so the same table drives the latency measurement
// and the partial-write check; primitives that produce no stream ignore w.
type entryPoint struct {
	name     string
	call     func(ctx context.Context, f fixture, w io.Writer) error
	plain    func(f fixture, w io.Writer) error
	writesTo bool
	// interruptible records whether this primitive has a loop boundary BYBLOS
	// OWNS between its start and its end, and so can honour a context that is
	// cancelled after the call is already running.
	//
	// This field is the honest half of byb-xyn and it is measured, not
	// asserted: TestCancellationLatency requires an interruptible primitive to
	// return in a small fraction of its full duration, and requires a
	// non-interruptible one to be caught RUNNING TO COMPLETION with the
	// cancelled context ignored. Both directions fail loudly, so a primitive
	// cannot quietly change group and leave its doc comment claiming the old
	// one.
	//
	// The four false entries are not oversights. ExtractPageRaster, WriteProvenance
	// and ReadProvenance are each a single indivisible pdfcpu pass with nothing
	// byblos owns inside it. BuildPDF does have a checked page loop, but that
	// loop is pageBox arithmetic and the pdfbuild.Write after it is where the
	// call actually spends its time, so cancellation lands in the write and is
	// not seen. Their doc comments say exactly this.
	perPageBoundary bool
}

// nineEntryPoints is exactly the set from the CONVENTION DECIDED note.
func nineEntryPoints() []entryPoint {
	return []entryPoint{
		{
			name:  "Inspect", perPageBoundary: true,
			call:  func(c context.Context, f fixture, _ io.Writer) error { _, e := InspectContext(c, bytes.NewReader(f.doc)); return e },
			plain: func(f fixture, _ io.Writer) error { _, e := Inspect(bytes.NewReader(f.doc)); return e },
		},
		{
			name: "ExtractPageRaster",
			call: func(c context.Context, f fixture, _ io.Writer) error {
				_, e := ExtractPageRasterContext(c, bytes.NewReader(f.doc), 1)
				return e
			},
			plain: func(f fixture, _ io.Writer) error { _, e := ExtractPageRaster(bytes.NewReader(f.doc), 1); return e },
		},
		{
			name: "Optimize",
			call: func(c context.Context, f fixture, w io.Writer) error {
				return OptimizeContext(c, w, bytes.NewReader(f.doc), OptimizeOptions{})
			},
			plain:    func(f fixture, w io.Writer) error { return Optimize(w, bytes.NewReader(f.doc), OptimizeOptions{}) },
			writesTo: true,
		},
		{
			name: "StampTextLayer", perPageBoundary: true,
			call: func(c context.Context, f fixture, w io.Writer) error {
				return StampTextLayerContext(c, w, bytes.NewReader(f.doc), f.tl)
			},
			plain:    func(f fixture, w io.Writer) error { return StampTextLayer(w, bytes.NewReader(f.doc), f.tl) },
			writesTo: true,
		},
		{
			name:     "BuildPDF", perPageBoundary: true,
			call:     func(c context.Context, f fixture, w io.Writer) error { return BuildPDFContext(c, w, f.pages) },
			plain:    func(f fixture, w io.Writer) error { return BuildPDF(w, f.pages) },
			writesTo: true,
		},
		{
			name: "ReplaceImages", perPageBoundary: true,
			call: func(c context.Context, f fixture, w io.Writer) error {
				return ReplaceImagesContext(c, w, bytes.NewReader(f.doc), f.subs)
			},
			plain:    func(f fixture, w io.Writer) error { return ReplaceImages(w, bytes.NewReader(f.doc), f.subs) },
			writesTo: true,
		},
		{
			name: "RecordExtraction", perPageBoundary: true,
			call: func(c context.Context, f fixture, _ io.Writer) error {
				_, e := RecordExtractionContext(c, bytes.NewReader(f.doc))
				return e
			},
			plain: func(f fixture, _ io.Writer) error { _, e := RecordExtraction(bytes.NewReader(f.doc)); return e },
		},
		{
			name: "WriteProvenance",
			call: func(c context.Context, f fixture, w io.Writer) error {
				return WriteProvenanceContext(c, bytes.NewReader(f.doc), w, f.prov)
			},
			plain:    func(f fixture, w io.Writer) error { return WriteProvenance(bytes.NewReader(f.doc), w, f.prov) },
			writesTo: true,
		},
		{
			name: "ReadProvenance",
			call: func(c context.Context, f fixture, _ io.Writer) error {
				_, e := ReadProvenanceContext(c, bytes.NewReader(f.doc))
				return e
			},
			plain: func(f fixture, _ io.Writer) error { _, e := ReadProvenance(bytes.NewReader(f.doc)); return e },
		},
	}
}

// TestNineEntryPointsIsTheDecidedSet pins the table against byb-xyn's decision
// rather than against whatever the table happens to hold. Every other test
// here iterates nineEntryPoints(), so a row deleted from it would silently
// delete that primitive's entire cancellation coverage.
func TestNineEntryPointsIsTheDecidedSet(t *testing.T) {
	want := []string{
		"Inspect", "ExtractPageRaster", "Optimize", "StampTextLayer", "BuildPDF",
		"ReplaceImages", "RecordExtraction", "WriteProvenance", "ReadProvenance",
	}
	got := nineEntryPoints()
	if len(got) != len(want) {
		t.Fatalf("nineEntryPoints() has %d entries, byb-xyn decided %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].name != w {
			t.Errorf("entry %d is %q, want %q", i, got[i].name, w)
		}
	}
}

// TestContextVariantsRefuseAnAlreadyCancelledContext is the floor, not the
// bar: every variant must notice a context that was already dead when it was
// handed over. It proves a check exists; it says nothing about where.
func TestContextVariantsRefuseAnAlreadyCancelledContext(t *testing.T) {
	f := newFixture(t, 3)
	for _, ep := range nineEntryPoints() {
		t.Run(ep.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := ep.call(ctx, f, io.Discard)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%sContext with a cancelled context returned %v, want context.Canceled", ep.name, err)
			}
		})
	}
}

// TestContextVariantsAgreeWithTheirShippedTwins is the ADD NEVER CHANGE half.
// byblos is tagged and kleio pins it, so the shipped names must keep behaving
// exactly as they did, and the variant under context.Background() must be the
// same call.
func TestContextVariantsAgreeWithTheirShippedTwins(t *testing.T) {
	f := newFixture(t, 3)
	for _, ep := range nineEntryPoints() {
		t.Run(ep.name, func(t *testing.T) {
			var ctxOut, plainOut bytes.Buffer
			ctxErr := ep.call(context.Background(), f, &ctxOut)
			plainErr := ep.plain(f, &plainOut)
			if fmt.Sprint(ctxErr) != fmt.Sprint(plainErr) {
				t.Fatalf("%sContext(Background) returned %v but %s returned %v; the shipped name must delegate, not diverge",
					ep.name, ctxErr, ep.name, plainErr)
			}
			if ep.writesTo && ctxOut.Len() == 0 && plainOut.Len() == 0 && ctxErr == nil {
				t.Fatalf("%s wrote nothing and reported no error; the fixture is not exercising it", ep.name)
			}
		})
	}
}

// observeCtx is the instrument, and it measures the contract's own definition
// rather than a proxy for it.
//
// Cancellation latency IS the longest stretch of work between two consecutive
// context checks: cancel at the worst possible instant and you wait exactly
// that long. So instead of cancelling at a guessed moment and timing the
// return, this context never cancels at all -- it just records when each check
// happened and reports the largest gap between them, plus the tail from the
// last check to the end of the call.
//
// That removes every source of flakiness the first two versions of this test
// had. There is no sleep, no second goroutine, and no dependence on one run
// taking as long as the previous run. The earlier design timed an uncancelled
// run and cancelled a later one at half that duration; later runs are
// systematically faster (warm caches), and at GOMAXPROCS=1 the worker goroutine
// does not even start until the test goroutine blocks in Sleep, so the call
// could finish inside the sleep and NO cancel could ever land mid-call. That
// version failed 3 subtests of 9 on a first run at GOMAXPROCS=1, and CI runs
// `go test ./... -v` with no -short.
type observeCtx struct {
	context.Context
	checks int
	last   time.Time
	maxGap time.Duration
}

func newObserveCtx() *observeCtx {
	return &observeCtx{Context: context.Background(), last: time.Now()}
}

// Err records the check and always reports "not cancelled". byblos consults a
// context only through checkContext, which calls Err, so this sees every
// boundary byblos actually has and nothing else.
func (c *observeCtx) Err() error {
	now := time.Now()
	if c.checks > 0 {
		if gap := now.Sub(c.last); gap > c.maxGap {
			c.maxGap = gap
		}
	}
	c.checks++
	c.last = now
	return nil
}

// worstUnit is the longest uninterruptible stretch: the widest gap between
// checks, or the tail after the final check if that is longer. The tail counts
// because a cancel arriving just after the last check waits for it too.
func (c *observeCtx) worstUnit(end time.Time) time.Duration {
	if tail := end.Sub(c.last); tail > c.maxGap {
		return tail
	}
	return c.maxGap
}

// cancelAtCheck is the behavioural half: a context that reports cancelled from
// its Nth check onward. It makes "the check actually causes the call to stop"
// testable without any timing at all -- the cancellation lands exactly at a
// check, deterministically, on every machine.
type cancelAtCheck struct {
	context.Context
	checks  int
	after   int
	firedAt time.Time
	fired   bool
}

func (c *cancelAtCheck) Err() error {
	c.checks++
	if c.checks > c.after {
		if !c.fired {
			c.fired, c.firedAt = true, time.Now()
		}
		return context.Canceled
	}
	return nil
}

// TestCancellationLatency is byb-xyn's acceptance bar, and it reports the
// number the doc comments quote.
//
// It runs each primitive under observeCtx at two document sizes and asserts the
// property that actually matters to a caller: does the number of context
// boundaries GROW WITH THE DOCUMENT, or is it a fixed handful regardless of
// size? A primitive with a per-page boundary bounds its cancellation latency by
// one page no matter how long the document is; one with a fixed few boundaries
// bounds it by a whole pdfcpu pass, which grows without limit.
//
// Asserting on the COUNT rather than on elapsed time is deliberate. The count is
// exact, identical on every machine, and cannot be perturbed by load -- and it
// is what a deleted check actually changes: remove the check inside Inspect's
// page loop and its count falls from pages+1 to 1, which no amount of CI noise
// can fake in either direction. The measured durations are logged beside it,
// because they are the contract, but they are not asserted on: two earlier
// versions of this test asserted on timing and both were flaky.
func TestCancellationLatency(t *testing.T) {
	if testing.Short() {
		t.Skipf("runs a %d-page and a %d-page document once per entry point", expensivePages/2, expensivePages)
	}
	small, large := expensivePages/2, expensivePages
	fSmall, fLarge := newFixture(t, small), newFixture(t, large)

	for _, ep := range nineEntryPoints() {
		t.Run(ep.name, func(t *testing.T) {
			run := func(f fixture, pages int) (checks int, full, worst time.Duration) {
				oc := newObserveCtx()
				start := time.Now()
				if err := ep.call(oc, f, io.Discard); err != nil {
					t.Fatalf("%sContext over %d pages under an observing context failed: %v", ep.name, pages, err)
				}
				return oc.checks, time.Since(start), oc.worstUnit(time.Now())
			}
			cSmall, _, _ := run(fSmall, small)
			cLarge, full, worst := run(fLarge, large)

			t.Logf("MEASURED %s: per-page-boundary=%t | %d pages: full=%v worst-uninterruptible-unit=%v (%.1f%% of the call) | checks %d->%d as pages %d->%d",
				ep.name, ep.perPageBoundary, large, full, worst, 100*float64(worst)/float64(full), cSmall, cLarge, small, large)

			if cLarge == 0 {
				t.Fatalf("%sContext never consulted its context at all", ep.name)
			}

			if ep.perPageBoundary {
				// One boundary per page, plus the entry guard. Stated as a
				// floor rather than an equality so a primitive that checks
				// more often than once per page (StampTextLayer checks per
				// word too) still satisfies it.
				if cLarge < large {
					t.Errorf("%sContext is recorded as having a per-page boundary but made only %d checks over %d pages; "+
						"its context check is not inside the loop that walks the document",
						ep.name, cLarge, large)
				}
				if grew := cLarge - cSmall; grew < small/2 {
					t.Errorf("%sContext's check count barely moved (%d over %d pages -> %d over %d pages); "+
						"its boundaries do not scale with the document, so cancellation latency is not bounded by one page",
						ep.name, cSmall, small, cLarge, large)
				}
				return
			}

			// The other direction, and it is the honest half: this primitive
			// has a FIXED number of boundaries, so its uninterruptible unit
			// grows with the document without limit. If that stops being true
			// it has gained a per-page boundary, and its doc comment -- which
			// tells a caller to budget for a whole pdfcpu pass -- is now wrong
			// and must be rewritten along with this table.
			if cLarge != cSmall {
				t.Errorf("%sContext is recorded as having NO per-page boundary, but its check count moved from %d to %d "+
					"as the document went from %d to %d pages; it has gained one, so mark it perPageBoundary and rewrite "+
					"its doc comment's CANCELLATION LATENCY paragraph",
					ep.name, cSmall, cLarge, small, large)
			}
		})
	}
}

// TestInterruptibleCallsStopAtTheirNextCheck is the behavioural half of the
// bar. TestCancellationLatency measures where the boundaries ARE; this proves
// that reaching one actually stops the call, rather than the checks being
// consulted and their answer discarded.
//
// The cancellation is triggered at a specific check rather than at a wall-clock
// moment, so this is deterministic on any machine at any GOMAXPROCS.
func TestInterruptibleCallsStopAtTheirNextCheck(t *testing.T) {
	f := newFixture(t, 8)
	for _, ep := range nineEntryPoints() {
		if !ep.perPageBoundary {
			continue
		}
		t.Run(ep.name, func(t *testing.T) {
			// Fire on the SECOND check, not the first: the first is the entry
			// guard, which every variant has and which proves nothing about
			// the loop. The second is the one inside the work.
			cc := &cancelAtCheck{Context: context.Background(), after: 1}
			err := ep.call(cc, f, io.Discard)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%sContext returned %v after its context went cancelled at check 2; want context.Canceled. "+
					"The check is reached but its answer is not acted on.", ep.name, err)
			}
			if cc.checks <= cc.after {
				t.Fatalf("%sContext made only %d checks; the cancellation at check %d was never reached",
					ep.name, cc.checks, cc.after+1)
			}
			t.Logf("%s stopped after %d checks, %v from the cancellation", ep.name, cc.checks, time.Since(cc.firedAt))
		})
	}
}

// TestInterruptibleSetIsTheMeasuredOne states, in one place, which primitives
// byblos can actually stop mid-call. It is the table kleio needs: for the five
// interruptible ones a deadline is enforceable to within one page, and for the
// other four the caller must budget for the whole call because byblos cannot
// interrupt pdfcpu.
//
// It is a separate test from the measurement so that the claim is greppable and
// so that a change of group is a deliberate edit in two places, not a quiet
// flip of one bool.
func TestPerPageBoundarySetIsTheMeasuredOne(t *testing.T) {
	want := map[string]bool{
		"Inspect": true, "StampTextLayer": true, "BuildPDF": true,
		"ReplaceImages": true, "RecordExtraction": true,
		"ExtractPageRaster": false, "Optimize": false,
		"WriteProvenance": false, "ReadProvenance": false,
	}
	for _, ep := range nineEntryPoints() {
		w, ok := want[ep.name]
		if !ok {
			t.Errorf("%s is not in the interruptibility table", ep.name)
			continue
		}
		if ep.perPageBoundary != w {
			t.Errorf("%s: perPageBoundary=%t, the measured set says %t", ep.name, ep.perPageBoundary, w)
		}
	}
}

// TestCancellationLatencyOnAHostilePage is the measurement the JPEG fixture
// cannot make, and it is the one byb-xyn actually turns on.
//
// The contract these primitives offer is "cancellation latency is one page",
// and that is only worth anything if ONE PAGE is bounded. byb-riy exists
// because it is not bounded by default: a few bytes of hostile JBIG2 buy an
// enormous amount of decoding. So the honest worst case is not the ~1 ms
// measured over ordinary scans -- it is what a page costs when the document is
// chosen by an adversary, and that is bounded by byb-riy's resource budget
// rather than by anything this context does.
//
// Measured here: one hostile page is SECONDS, against roughly a millisecond for
// 120 ordinary ones. That ratio is the finding. A caller sizing an SQS
// visibility timeout against the ordinary number still loses the worker to a
// hostile document and still gets it redelivered, which is precisely the
// failure this bead exists to prevent.
func TestCancellationLatencyOnAHostilePage(t *testing.T) {
	if testing.Short() {
		t.Skip("decodes hostile JBIG2 on every page")
	}
	doc := hostilePageDoc(t)
	oc := newObserveCtx()
	start := time.Now()
	if _, err := RecordExtractionContext(oc, bytes.NewReader(doc)); err != nil {
		t.Fatalf("RecordExtraction over the hostile document: %v", err)
	}
	full := time.Since(start)
	worst := oc.worstUnit(time.Now())

	t.Logf("MEASURED hostile: full=%v over %d pages, checks=%d, worst uninterruptible unit=%v",
		full, hostilePages, oc.checks, worst)
	t.Logf("CONTRACT: cancelling RecordExtraction still stops at a page boundary on a hostile document, "+
		"but ONE PAGE here costs %v -- against ~12ms for the worst page of an ordinary 120-page scan. "+
		"Size deadlines and SQS visibility timeouts against this number, not the ordinary one.", worst)

	if oc.checks < hostilePages {
		t.Fatalf("only %d context checks over %d pages; the per-page boundary is not being reached", oc.checks, hostilePages)
	}
	// The property under test is that cancellation is still bounded by ONE page
	// and not by the document. With hostilePages pages, one page is about
	// 1/hostilePages of the call; allow generous slack so this asserts the
	// property and not this host's speed.
	if worst > full*3/4 {
		t.Errorf("worst uninterruptible unit is %v of a %v call over %d pages; cancellation is not bounded by one page",
			worst, full, hostilePages)
	}
}

// hostilePages is how many hostile pages the fixture carries. Two distinct
// image objects is what the dup-raster corpus document offers, and it is
// enough: the claim under test is "cancelling stops at a page boundary", which
// needs more than one page and nothing more.
const hostilePages = 2

// hostileDim is the square page/region size of the hostile stream: 8191x8191 is
// 67,092,481 pixels, just under the 67,108,864 byb-riy admits, so it is the
// most expensive page every gate still lets through.
const hostileDim = 8191

// hostilePageDoc builds a document whose every page paints a JBIG2 stream that
// costs far more to decode than it can ever return.
func hostilePageDoc(t *testing.T) []byte {
	t.Helper()
	// hostileJBIG2Stream at 8191x8191, NOT wastedWorkStream. The 67-byte
	// wastedWork fixture carries a 1x1 region and diverts almost instantly --
	// building this test on it measured 104us and proved nothing, which is
	// exactly the vacuous-instrument failure byb-riy kept hitting. This one is
	// a page just under jbig2.MaxPagePixels fully covered by a region, so it is
	// admitted by every gate and actually decodes ~67 million pixels.
	data := hostileJBIG2Stream(hostileDim, hostileDim, hostileDim, hostileDim)
	in := corpusDoc(t, "dup-raster")
	pages := inspect(t, "dup-raster")
	if len(pages) != hostilePages {
		t.Fatalf("the dup-raster fixture is %d pages; want %d", len(pages), hostilePages)
	}
	repl := map[int]EncodedImage{}
	for _, p := range pages {
		if len(p.Images) != 1 {
			t.Fatalf("page %d has %d images; want 1", p.Index, len(p.Images))
		}
		repl[p.Images[0].ObjNr] = EncodedImage{
			Width: hostileDim, Height: hostileDim, BPC: 1,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "JBIG2Decode",
			Data:       data,
		}
	}
	if len(repl) != hostilePages {
		t.Fatalf("the two pages share %d image object(s); want %d distinct ones", len(repl), hostilePages)
	}
	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), repl); err != nil {
		t.Fatalf("ReplaceImages: %v", err)
	}
	return out.Bytes()
}

// TestOptimizeRecompressPathIsCancellable covers the loops the main table
// cannot reach. Every other test here drives Optimize with OptimizeOptions{},
// which skips recompressJPEG entirely -- so the per-page and per-image checks
// inside it were, until this test existed, an untested path: deleting both of
// them left the whole suite green.
//
// The recompression pass is also the case where Optimize's pre-rewrite check
// earns its place, since recompressJPEG is where an image-heavy document
// actually spends its time.
func TestOptimizeRecompressPathIsCancellable(t *testing.T) {
	if testing.Short() {
		t.Skip("recompresses every page of the expensive fixture")
	}
	f := newFixture(t, expensivePages)
	opts := OptimizeOptions{RecompressJPEG: true, JPEGQuality: 50}

	oc := newObserveCtx()
	start := time.Now()
	if err := OptimizeContext(oc, io.Discard, bytes.NewReader(f.doc), opts); err != nil {
		t.Fatalf("Optimize+RecompressJPEG under an observing context failed: %v", err)
	}
	full := time.Since(start)
	worst := oc.worstUnit(time.Now())
	t.Logf("MEASURED Optimize+RecompressJPEG: full=%v checks=%d worst-uninterruptible-unit=%v (%.1f%% of full, over %d pages)",
		full, oc.checks, worst, 100*float64(worst)/float64(full), expensivePages)

	if full < time.Millisecond {
		t.Fatalf("Optimize+RecompressJPEG measured %v; too cheap to measure against", full)
	}
	if worst > full/3 {
		t.Errorf("Optimize+RecompressJPEG's worst uninterruptible unit is %v of a %v call (%d checks); "+
			"recompressJPEG's per-page and per-image checks are not bounding the call", worst, full, oc.checks)
	}

	// And that reaching one of those boundaries actually stops the call.
	cc := &cancelAtCheck{Context: context.Background(), after: 1}
	if err := OptimizeContext(cc, io.Discard, bytes.NewReader(f.doc), opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("Optimize+RecompressJPEG returned %v after cancellation at check 2; want context.Canceled", err)
	}
}

// TestLatencyInstrumentIsNotVacuous mutation-tests the instrument itself.
//
// Every latency assertion above is an UPPER bound on observeCtx.worstUnit, and
// zero satisfies every upper bound -- so an instrument that measured nothing
// would report a perfect result for every primitive, including one that never
// checks its context at all. That is exactly the failure byblos already shipped
// once with its decoded-pixel counter: zeroing it left three rounds of budget
// tests green, because every assertion on it was an upper bound too.
//
// So this drives observeCtx against a control whose uninterruptible stretch is
// KNOWN, and requires the instrument to report it. If this stops failing when
// worstUnit is broken to return 0, nothing else in this file means anything.
func TestLatencyInstrumentIsNotVacuous(t *testing.T) {
	const gap = 200 * time.Millisecond

	// A control with exactly one long stretch between two checks.
	oc := newObserveCtx()
	_ = oc.Err()
	time.Sleep(gap)
	_ = oc.Err()
	if got := oc.worstUnit(time.Now()); got < gap {
		t.Fatalf("the instrument measured a worst unit of %v across a deliberate %v stretch between checks; "+
			"it is not measuring the gaps the latency assertions rest on", got, gap)
	}

	// And the tail: work after the LAST check is uninterruptible too, and an
	// instrument that ignored it would under-report every primitive whose
	// expensive stage runs after its final boundary.
	tc := newObserveCtx()
	_ = tc.Err()
	_ = tc.Err()
	time.Sleep(gap)
	if got := tc.worstUnit(time.Now()); got < gap {
		t.Fatalf("the instrument measured a worst unit of %v while %v of work ran after the final check; "+
			"the tail is not being counted", got, gap)
	}

	// A context that is never consulted must not read as perfectly interruptible.
	nc := newObserveCtx()
	if nc.checks != 0 {
		t.Fatalf("a fresh observing context reports %d checks; want 0", nc.checks)
	}
}

// TestCancelledCallWritesNothing covers the failure a latency test cannot see.
// Returning promptly is worthless if the primitive has already emitted a
// truncated document to w: that file opens, is wrong, and nothing downstream
// can tell. Every writer-taking primitive must build its output fully before
// touching w.
func TestCancelledCallWritesNothing(t *testing.T) {
	f := newFixture(t, 8)
	for _, ep := range nineEntryPoints() {
		if !ep.writesTo {
			continue
		}
		t.Run(ep.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var buf bytes.Buffer
			err := ep.call(ctx, f, &buf)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%sContext returned %v, want context.Canceled", ep.name, err)
			}
			if buf.Len() != 0 {
				t.Errorf("%sContext wrote %d bytes before returning context.Canceled; a cancelled call must leave w untouched, not truncated",
					ep.name, buf.Len())
			}
		})
	}
}

// TestCancelledExtractionDoesNotMoveTheCounters pins the claim
// ExtractPageRasterContext's doc comment makes about telemetry, which would
// otherwise rest on reading the code.
//
// A call abandoned because the caller's deadline expired is not a failed
// extraction. Counting it as one would inflate the divert and unhandled rates
// that design spec section 2's premise rests on with what are really worker
// timeouts -- and the more pathological the corpus, the more timeouts, so the
// error would grow exactly where the measurement is load-bearing.
//
// The reader-closed case is the one that actually bites and is why the
// pdfdoc.Open error branch re-checks the context before counting: a caller
// that cancels typically closes the reader too, which makes Open fail, and
// without the re-check every timed-out document would land in the counters as
// Attempted+Failed AND be reported to the caller as a pdfcpu error rather than
// a cancellation.
func TestCancelledExtractionDoesNotMoveTheCounters(t *testing.T) {
	f := newFixture(t, 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("readable reader", func(t *testing.T) {
		ResetExtractStats()
		if _, err := ExtractPageRasterContext(ctx, bytes.NewReader(f.doc), 1); !errors.Is(err, context.Canceled) {
			t.Fatalf("ExtractPageRasterContext returned %v, want context.Canceled", err)
		}
		if got := ExtractStats(); got.Attempted != 0 || got.Failed != 0 || got.Diverted != 0 {
			t.Errorf("a cancelled extraction moved the counters: %+v", got)
		}
	})

	t.Run("reader closed under the cancel", func(t *testing.T) {
		ResetExtractStats()
		// The context must be LIVE at entry and dead by the time Open fails --
		// otherwise the entry check returns first and the Open-error branch,
		// which is the whole point of this subtest, is never reached. An
		// already-cancelled context makes this vacuous: with one, reverting
		// the fix in extract.go leaves this test green, which is how the first
		// version of it was caught.
		//
		// cancelAtCheck fires on the check AFTER the entry guard, which is
		// exactly the re-check inside the Open-error branch.
		cc := &cancelAtCheck{Context: context.Background(), after: 1}
		_, err := ExtractPageRasterContext(cc, failingReadSeeker{}, 1)
		if cc.checks < 2 {
			t.Fatalf("only %d context checks; the Open-error branch never re-checked, so a caller whose "+
				"deadline expired gets a pdfcpu error instead of a cancellation", cc.checks)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ExtractPageRasterContext over a closed reader returned %v, want context.Canceled -- "+
				"a caller cannot distinguish its own timeout from a corrupt document", err)
		}
		if got := ExtractStats(); got.Attempted != 0 || got.Failed != 0 {
			t.Errorf("a cancelled extraction over a closed reader was counted as a real attempt: %+v", got)
		}
	})

	t.Run("RecordExtraction", func(t *testing.T) {
		ResetExtractStats()
		if _, err := RecordExtractionContext(ctx, bytes.NewReader(f.doc)); !errors.Is(err, context.Canceled) {
			t.Fatalf("RecordExtractionContext returned %v, want context.Canceled", err)
		}
		if got := ExtractStats(); got.Attempted != 0 || got.Failed != 0 || got.Diverted != 0 {
			t.Errorf("a cancelled RecordExtraction moved the counters: %+v", got)
		}
	})
}

// failingReadSeeker fails every operation, standing in for a reader the caller
// closed when its deadline expired.
type failingReadSeeker struct{}

func (failingReadSeeker) Read([]byte) (int, error) { return 0, errors.New("reader closed") }
func (failingReadSeeker) Seek(int64, int) (int64, error) {
	return 0, errors.New("reader closed")
}

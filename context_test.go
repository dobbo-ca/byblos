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
// against the same call's uncancelled duration rather than asserting a tuned
// constant, and TestCancellationLatencyHarnessIsNotVacuous mutation-checks the
// stopwatch itself: every latency assertion is an upper bound, zero satisfies
// every upper bound, so a harness that measured nothing would report a perfect
// result for a function that ignores cancellation entirely.

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
// expensive LEGITIMATE input available -- byb-riy's hostile JBIG2 page is the
// separate worst case and is measured by TestCancellationLatencyOnAHostilePage.
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
	interruptible bool
}

// nineEntryPoints is exactly the set from the CONVENTION DECIDED note.
func nineEntryPoints() []entryPoint {
	return []entryPoint{
		{
			name:  "Inspect", interruptible: true,
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
			name: "Optimize", interruptible: true,
			call: func(c context.Context, f fixture, w io.Writer) error {
				return OptimizeContext(c, w, bytes.NewReader(f.doc), OptimizeOptions{})
			},
			plain:    func(f fixture, w io.Writer) error { return Optimize(w, bytes.NewReader(f.doc), OptimizeOptions{}) },
			writesTo: true,
		},
		{
			name: "StampTextLayer", interruptible: true,
			call: func(c context.Context, f fixture, w io.Writer) error {
				return StampTextLayerContext(c, w, bytes.NewReader(f.doc), f.tl)
			},
			plain:    func(f fixture, w io.Writer) error { return StampTextLayer(w, bytes.NewReader(f.doc), f.tl) },
			writesTo: true,
		},
		{
			name:     "BuildPDF",
			call:     func(c context.Context, f fixture, w io.Writer) error { return BuildPDFContext(c, w, f.pages) },
			plain:    func(f fixture, w io.Writer) error { return BuildPDF(w, f.pages) },
			writesTo: true,
		},
		{
			name: "ReplaceImages", interruptible: true,
			call: func(c context.Context, f fixture, w io.Writer) error {
				return ReplaceImagesContext(c, w, bytes.NewReader(f.doc), f.subs)
			},
			plain:    func(f fixture, w io.Writer) error { return ReplaceImages(w, bytes.NewReader(f.doc), f.subs) },
			writesTo: true,
		},
		{
			name: "RecordExtraction", interruptible: true,
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

// latency runs ep against f, cancels the context after `at`, and returns how
// long the call took to return AFTER the cancel landed. That subtraction is
// the whole measurement: elapsed-until-return measured from the cancel, not
// from the start of the call.
func latency(t *testing.T, ep entryPoint, f fixture, at time.Duration) (time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ep.call(ctx, f, io.Discard) }()
	time.Sleep(at)
	cancelledAt := time.Now()
	cancel()
	select {
	case err := <-done:
		return time.Since(cancelledAt), err
	case <-time.After(120 * time.Second):
		t.Fatalf("%sContext did not return within 120s of cancellation", ep.name)
		return 0, nil
	}
}

// TestCancellationLatency is byb-xyn's acceptance bar. For each of the nine it
// measures the uncancelled duration, cancels a second run halfway through, and
// requires the call to return in a small fraction of what remained.
//
// The ratio is the point. An absolute millisecond budget would be a constant
// tuned to this host and would pass on a slower one for the wrong reason; a
// function that ignores its context returns at ~half the full duration here,
// which no ratio below 1/2 can accommodate.
func TestCancellationLatency(t *testing.T) {
	if testing.Short() {
		t.Skipf("the latency measurement runs a %d-page document twice per entry point", expensivePages)
	}
	f := newFixture(t, expensivePages)
	for _, ep := range nineEntryPoints() {
		t.Run(ep.name, func(t *testing.T) {
			start := time.Now()
			if err := ep.call(context.Background(), f, io.Discard); err != nil {
				t.Fatalf("uncancelled %sContext failed: %v", ep.name, err)
			}
			full := time.Since(start)

			got, err := latency(t, ep, f, full/2)
			t.Logf("MEASURED %s: interruptible=%t full=%v after-cancel=%v (%.2f%% of full, over %d pages) err=%v",
				ep.name, ep.interruptible, full, got, 100*float64(got)/float64(full), expensivePages, err)

			if !ep.interruptible {
				// The claim being pinned is the DEFICIENCY, in the direction
				// that can catch a doc comment going stale: this primitive has
				// no loop boundary byblos owns, so a context cancelled after
				// the call started is ignored and the work runs to completion.
				// If that ever stops being true the primitive has become
				// cancellable and its doc comment -- which currently promises a
				// caller it must budget for the whole call -- is now wrong and
				// has to be rewritten along with this table.
				if err != nil {
					t.Errorf("%sContext is recorded as not interruptible, but a mid-call cancellation returned %v "+
						"instead of running to completion; it has become cancellable, so move it to interruptible "+
						"and rewrite its doc comment's CANCELLATION LATENCY paragraph",
						ep.name, err)
				}
				return
			}

			// A zeroed or broken stopwatch reports 0 for everything, and 0
			// satisfies every upper bound below. Requiring the uncancelled call
			// to be measurably long is what gives the ratio assertion a
			// direction it can fail in.
			if full < time.Millisecond {
				t.Fatalf("uncancelled %sContext measured %v over %d pages; the fixture is not expensive enough to measure a latency against",
					ep.name, full, expensivePages)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%sContext cancelled mid-call returned %v, want context.Canceled", ep.name, err)
			}
			if got > full/3 {
				t.Errorf("%sContext took %v to return after cancellation, against a full duration of %v; "+
					"the context was checked but not acted on inside the loop that does the work",
					ep.name, got, full)
			}
		})
	}
}

// TestInterruptibleSetIsTheMeasuredOne states, in one place, which primitives
// byblos can actually stop mid-call. It is the table kleio needs: for the five
// interruptible ones a deadline is enforceable, and for the other four the
// caller must budget for the whole call because byblos cannot interrupt pdfcpu.
//
// It is a separate test from the measurement so that the claim is greppable and
// so that a change of group is a deliberate edit in two places, not a quiet
// flip of one bool.
func TestInterruptibleSetIsTheMeasuredOne(t *testing.T) {
	want := map[string]bool{
		"Inspect": true, "Optimize": true, "StampTextLayer": true,
		"ReplaceImages": true, "RecordExtraction": true,
		"ExtractPageRaster": false, "BuildPDF": false,
		"WriteProvenance": false, "ReadProvenance": false,
	}
	for _, ep := range nineEntryPoints() {
		w, ok := want[ep.name]
		if !ok {
			t.Errorf("%s is not in the interruptibility table", ep.name)
			continue
		}
		if ep.interruptible != w {
			t.Errorf("%s: interruptible=%t, the measured set says %t", ep.name, ep.interruptible, w)
		}
	}
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
	ep := entryPoint{
		name:          "Optimize+RecompressJPEG",
		interruptible: true,
		call: func(c context.Context, f fixture, w io.Writer) error {
			return OptimizeContext(c, w, bytes.NewReader(f.doc), opts)
		},
	}

	start := time.Now()
	if err := ep.call(context.Background(), f, io.Discard); err != nil {
		t.Fatalf("uncancelled %s failed: %v", ep.name, err)
	}
	full := time.Since(start)
	if full < time.Millisecond {
		t.Fatalf("%s measured %v; too cheap to measure a cancellation against", ep.name, full)
	}

	got, err := latency(t, ep, f, full/2)
	t.Logf("MEASURED %s: full=%v after-cancel=%v (%.2f%% of full, over %d pages)",
		ep.name, full, got, 100*float64(got)/float64(full), expensivePages)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%s cancelled mid-call returned %v, want context.Canceled", ep.name, err)
	}
	if got > full/3 {
		t.Errorf("%s took %v to return after cancellation, against a full duration of %v; "+
			"recompressJPEG's per-page and per-image checks are not bounding the call",
			ep.name, got, full)
	}
}

// TestCancellationLatencyHarnessIsNotVacuous mutation-tests the stopwatch. The
// latency assertions are all upper bounds and zero satisfies an upper bound,
// so a harness that measured nothing would report a perfect result. This drives
// `latency` against a function that deliberately ignores its context and
// requires the harness to CATCH it: if this stops failing on an uncancellable
// call, every measurement above is meaningless.
func TestCancellationLatencyHarnessIsNotVacuous(t *testing.T) {
	const work = 300 * time.Millisecond
	uncancellable := entryPoint{
		name: "uncancellable",
		call: func(ctx context.Context, _ fixture, _ io.Writer) error {
			time.Sleep(work)
			return nil
		},
	}
	got, err := latency(t, uncancellable, fixture{}, work/3)
	if err != nil {
		t.Fatalf("the control function returned %v, want nil", err)
	}
	// It ignored the cancel, so it must still have been running for most of
	// `work` after the cancel landed. A harness reporting ~0 here is broken and
	// would have silently passed every entry point above.
	if got < work/3 {
		t.Fatalf("the harness measured %v of latency on a function that ignores cancellation entirely; "+
			"it should have measured most of the remaining %v, so the stopwatch is not measuring what the latency tests assume",
			got, work)
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

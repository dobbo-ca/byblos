package byblos

// byb-xyn: the context convention, decided once for the whole exported surface
// rather than per function.
//
// WHY IT EXISTS. Kleio's pipeline runs poppler as a subprocess through
// exec.CommandContext, so a per-document deadline actually KILLS a runaway
// tool -- the operating system provides that guarantee for free. Replacing a
// subprocess with an in-process byblos call silently gives it up: a document
// that sends pdfcpu somewhere slow holds the worker until the SQS visibility
// timeout expires, and the message is then REDELIVERED onto the same document,
// so one pathological file can occupy a worker in a loop. That is a real cost
// of the whole G1 project, and it is byblos' to pay, not the caller's: wrapping
// a call in a goroutine and selecting on ctx.Done() leaks the goroutine and the
// work continues unobservably, so there is no caller-side fix.
//
// NAMING IS FORCED, NOT CHOSEN. byblos is tagged and kleio pins it, so ADD
// NEVER CHANGE applies: each shipped primitive gains an XxxContext(ctx, ...)
// twin and the shipped name delegates with context.Background(). The shipped
// signatures do not move. This is forced only for what already shipped --
// anything NEW takes ctx as a plain first parameter with no Context twin.
//
// WHICH FUNCTIONS. The nine document-scale primitives, whose cost scales with
// the document: Inspect, ExtractPageRaster, Optimize, StampTextLayer, BuildPDF,
// ReplaceImages, RecordExtraction, WriteProvenance, ReadProvenance.
//
// The six image-scale pure-compute functions -- Sauvola, QuantizePNG,
// QuantizeIndexed, Downsample, DownsampleDeclaredBPC, EncodeJBIG2Generic --
// deliberately get NO context variant, and the omission is a decision rather
// than an oversight. Their cost is bounded by one already-in-memory image the
// caller chose to pass, so the caller can bound them without a context; adding
// ctx there would be API surface carrying no guarantee. DecodeJBIG2Generic is
// the same shape and additionally carries its own resource budget (byb-riy).
// The five trivial ones -- NewBitmap, ExtractStats, ResetExtractStats,
// Capabilities, UpgradeCandidates -- do no document-scale work at all.
//
// WHAT CANCELLATION MEANS, AND WHAT IT DOES NOT. byblos checks ctx at every
// PAGE- AND DOCUMENT-LEVEL boundary it controls. It does NOT check inside a
// page: pdfcpu is not context-aware and will not become so, and byblos' own
// content walk (internal/content.Walk) drives a per-operator loop that takes
// no context either. So CANCELLATION LATENCY EQUALS THE LONGEST
// UNINTERRUPTIBLE UNIT OF WORK, and that unit is one page at best -- for four
// of the nine it is a whole pdfcpu round trip. This is the part that would
// otherwise ship as a comfortable fiction, so each primitive's doc comment
// states its MEASURED worst case rather than claiming "we check between
// pages". Pushing a check down into content.Walk is byb-fem.
//
// WHAT WAS MEASURED, and it is less flattering than "nine cancellable
// primitives" sounds. Each entry point was run under a context that records
// where its boundaries fall, over a 120-page document of 300-dpi scans; the
// figure is the longest stretch of work between two consecutive checks, as a
// fraction of the whole call:
//
//	RecordExtraction     3%    one page, and the page loop is the whole call
//	StampTextLayer      22%
//	Optimize            35%    4 checks, no per-page boundary by default
//	ReplaceImages       46%
//	ExtractPageRaster   55%    2 checks, however long the document
//	Inspect             69%    pdfdoc.Open dominates the page loop
//	BuildPDF            94%    pdfbuild.Write dominates the page loop
//	ReadProvenance     100%    1 check
//	WriteProvenance    100%    1 check
//
// So RECORDEXTRACTION IS THE ONE THAT WORKS. It is also the one that matters,
// being the most expensive entry point and the one kleio would run per
// document. For the rest, the context lets a caller decline to START the work
// and bounds an inner loop that is not where the time goes; a caller must
// still budget for a whole pdfcpu pass. Five of the nine have a per-page
// boundary and four do not, and TestCancellationLatency pins that split by
// CHECK COUNT rather than by elapsed time -- deleting a loop check changes the
// count exactly, whereas timing assertions on this proved flaky twice.
//
// AND THE PAGE ITSELF IS NOT SMALL. RecordExtraction's per-page unit is ~12 ms
// over ordinary scans but SECONDS over a JBIG2 page that byb-riy's budget still
// admits, because one such page decodes 67 million pixels. A caller sizing a
// deadline or an SQS visibility timeout must use the hostile number: sized
// against the ordinary one, a single hostile document still holds a worker past
// its timeout and is redelivered onto the same file, which is the failure this
// convention exists to prevent. See TestCancellationLatencyOnAHostilePage.
//
// A cancelled call writes nothing -- but NOT because the output is buffered.
// Only Optimize builds its result in memory first; StampTextLayer,
// ReplaceImages, BuildPDF and WriteProvenance all stream straight into the
// caller's io.Writer. What makes truncation impossible is that no context
// check runs once a write has begun: every check sits strictly before the
// write. That is a real constraint on future edits, not an accident --
// ADDING a check around one of those writes would make truncation possible,
// and a truncated PDF opens cleanly and is wrong. TestCancelledCallWritesNothing
// pins the property; this paragraph is why it holds.

import "context"

// checkContext is the single place a loop boundary consults ctx. It exists so
// that "where does byblos check cancellation" is one grep, and so the check
// cannot drift into ctx.Done() selects that behave differently under a context
// that is cancelled and drained.
func checkContext(ctx context.Context) error {
	return ctx.Err()
}

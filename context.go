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
// loop boundary it controls. It cannot check inside pdfcpu, which is not
// context-aware and will not become so, so CANCELLATION LATENCY EQUALS THE
// LONGEST UNINTERRUPTIBLE UNIT OF WORK -- and for several of these primitives
// that unit is a whole pdfcpu round trip, not a page. This is the part that
// would otherwise ship as a comfortable fiction, so each primitive's doc
// comment states its MEASURED worst case rather than claiming "we check
// between pages". See TestCancellationLatency for the measurements and
// docs/superpowers/specs for the table.
//
// A cancelled call writes nothing. Every primitive here that takes an
// io.Writer builds its output completely before touching it, so cancellation
// can never leave a truncated document behind -- one that would open cleanly
// and be wrong. TestCancelledCallWritesNothing pins that.

import "context"

// checkContext is the single place a loop boundary consults ctx. It exists so
// that "where does byblos check cancellation" is one grep, and so the check
// cannot drift into ctx.Done() selects that behave differently under a context
// that is cancelled and drained.
func checkContext(ctx context.Context) error {
	return ctx.Err()
}

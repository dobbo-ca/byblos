package byblos

// Measurement probe for byb-16j.2: how many pages of the pinned sample carry an
// unbalanced q/Q content stream, and would therefore be REFUSED by
// pdfdoc.WrapContent (internal/pdfdoc/text.go:399-449) before straightening
// writes a single byte.
//
// Spec section 5 names this refusal explicitly: "This refusal is unpriced.
// Nothing in the repository can currently detect an unbalanced content stream,
// and nobody has counted them." This probe is that count.
//
// # Why the q/Q counter is copied here instead of called
//
// contentDepth and contentQDepth (internal/pdfdoc/text.go) are unexported, on
// purpose -- the spec: "contentDepth stays unexported -- it is this package's
// own guard, not a question a caller asks." pdfdoc cannot be extended with a
// test-only export for this probe without moving that decision, and this
// probe cannot live inside package pdfdoc itself: internal/sample already
// imports internal/pdfdoc to open documents, so the reverse import would
// cycle.
//
// The counting rule itself is small and is pdfdoc's own doc comment
// (text.go:451-468), reproduced exactly here: walk the content with
// internal/content's lexer, "q" increments a depth counter, "Q" decrements it
// UNLESS depth is already zero, in which case it is a surplus Q noted
// separately (a Q immediately followed by a q can wash the running count back
// to zero without ever un-happening). Both directions -- a surplus Q, or a net
// nonzero depth at end of stream -- are what WrapContent refuses via
// ErrUnbalancedContent.
//
// # It decodes content streams and NOTHING else
//
// pdfdoc.Doc.Page(n) decodes and concatenates /Contents (pdfdoc.go:184,
// pdfdoc.go:420-441) but never touches an image XObject. This probe reads
// Page.Content and nothing more, so it is far cheaper than the byb-16j.1 skew
// census, which had to decode every raster too.
//
//	BYBLOS_QQ_CORPUS=<dir> BYBLOS_QQ_OUT=<file> BYBLOS_JOBS=6 \
//	  go test -run TestQQBalanceCensus -v -count=1 -timeout 60m .

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/sample"
)

// contentQDepthProbe reproduces internal/pdfdoc's contentQDepth exactly
// (text.go:469-493): a "q" token increments depth, a "Q" token decrements it
// unless depth is already zero, in which case it is counted as a surplus Q
// instead of driving depth negative. depth at EOF is the net nesting;
// sawSurplusQ is set the instant a Q pops state this stream never pushed, even
// if a later q washes the running count back to zero.
func contentQDepthProbe(src []byte) (depth int, sawSurplusQ bool, lexErr error) {
	l := content.NewLexer(src)
	for {
		tok, err := l.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return depth, sawSurplusQ, nil
			}
			return depth, sawSurplusQ, err
		}
		if tok.Kind != content.KindKeyword {
			continue
		}
		switch string(tok.Text) {
		case "q":
			depth++
		case "Q":
			if depth == 0 {
				sawSurplusQ = true
				continue
			}
			depth--
		}
	}
}

// qqRow is one page: its net q/Q depth and whether WrapContent would refuse it.
type qqRow struct {
	docIndex int
	rel      string
	page     int

	hasContent bool
	depth      int
	surplusQ   bool // a Q popped state this page's own stream never pushed
	lexErr     string
	refused    bool
}

func (r qqRow) line() string {
	reason := "balanced"
	switch {
	case !r.hasContent:
		reason = "no-content"
	case r.lexErr != "":
		reason = "lex-error"
	case r.surplusQ && r.depth > 0:
		reason = "surplus-Q-and-net-q" // both directions in one stream
	case r.surplusQ:
		reason = "surplus-Q"
	case r.depth > 0:
		reason = "surplus-q"
	case r.depth < 0:
		reason = "net-negative" // unreachable given the counting rule; named for safety
	}
	return strings.Join([]string{
		strconv.Itoa(r.docIndex), r.rel, strconv.Itoa(r.page),
		fbool(r.hasContent), strconv.Itoa(r.depth), fbool(r.surplusQ),
		fbool(r.refused), reason,
		strings.ReplaceAll(r.lexErr, "\t", " "),
	}, "\t")
}

// qqCensusDoc measures every page of one already-open document. It never calls
// content.Walk and never decodes an image: Page.Content is already the decoded,
// concatenated content stream (pdfdoc.go:184), so this is the cheapest pass
// over the sample in the tree.
func qqCensusDoc(doc sample.Doc) []qqRow {
	d := doc.Doc
	rows := make([]qqRow, 0, d.PageCount())
	for n := 1; n <= d.PageCount(); n++ {
		r := qqRow{docIndex: doc.Index, rel: doc.Rel, page: n}
		p, err := d.Page(n)
		if err != nil {
			r.lexErr = err.Error()
			r.refused = true // an unreadable page is not something WrapContent
			// could act on either -- pdfdoc.WrapContent calls PageDict/PageContent
			// itself and would fail the same way, so it is refused for a
			// different reason but refused all the same.
			rows = append(rows, r)
			continue
		}
		if len(p.Content) == 0 {
			// No /Contents, or /Contents decodes to zero bytes: contentDepth
			// returns (0, nil) for exactly this case (text.go:401-402,
			// 421-423, 427-431) without invoking the lexer at all.
			rows = append(rows, r)
			continue
		}
		r.hasContent = true
		depth, surplusQ, lexErr := contentQDepthProbe(p.Content)
		r.depth, r.surplusQ = depth, surplusQ
		if lexErr != nil {
			r.lexErr = lexErr.Error()
			// contentDepth refuses when the lexer cannot tokenize the whole
			// stream (text.go:436-444): a partial read cannot be proven
			// balanced, so it is refused rather than trusted on a prefix.
			r.refused = true
		} else {
			r.refused = surplusQ || depth != 0
		}
		rows = append(rows, r)
	}
	return rows
}

// fbool is skew_probe_test.go's helper, same package.

func TestQQBalanceCensus(t *testing.T) {
	root := os.Getenv("BYBLOS_QQ_CORPUS")
	if root == "" {
		t.Skip("set BYBLOS_QQ_CORPUS to a directory of PDFs to run the byb-16j.2 q/Q census")
	}
	outPath := os.Getenv("BYBLOS_QQ_OUT")
	if outPath == "" {
		t.Fatal("set BYBLOS_QQ_OUT to the TSV to write")
	}
	workers := 6
	if v, err := strconv.Atoi(os.Getenv("BYBLOS_JOBS")); err == nil && v > 0 {
		workers = v
	}

	out, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create %s: %v", outPath, err)
	}
	defer out.Close()
	w := bufio.NewWriterSize(out, 1<<20)
	// One mutex over the writer, same rationale as TestSkewCensus: the work is
	// milliseconds per document and a goroutine-plus-channel writer would need
	// its own shutdown path to guarantee the last flush.
	var mu sync.Mutex
	var pages, refused int
	flushEvery := 500
	sinceFlush := 0

	start := time.Now()
	pop, err := sample.Walk(root, workers, func(d sample.Doc) {
		if d.Err != nil {
			return
		}
		rows := qqCensusDoc(d)
		mu.Lock()
		defer mu.Unlock()
		for _, r := range rows {
			fmt.Fprintln(w, r.line())
			pages++
			if r.refused {
				refused++
			}
		}
		sinceFlush++
		if sinceFlush >= flushEvery {
			_ = w.Flush()
			sinceFlush = 0
			t.Logf("%s  docs-flushed pages=%d refused=%d",
				time.Since(start).Round(time.Second), pages, refused)
		}
	})
	mu.Lock()
	_ = w.Flush()
	mu.Unlock()
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	// The denominator is internal/sample's, never this probe's -- byb-wj2's
	// lesson, restated for a second probe over the same corpus.
	t.Logf("files=%d unopenable=%d documents=%d pages=%d",
		pop.Files, pop.Unopenable, pop.Documents, pop.Pages)
	t.Logf("rows=%d refused=%d elapsed=%s", pages, refused, time.Since(start).Round(time.Second))
	if pages != pop.Pages {
		t.Logf("NOTE: emitted %d rows for a population of %d pages", pages, pop.Pages)
	}
}

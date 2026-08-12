package byblos

// Measurement probe for byb-9v0: count the pages byblos understands completely
// and still cannot return, because their raster is a JBIG2 stream coded in a
// mode byblos does not decode -- and split that count by WHICH mode.
//
// The bead exists because extract.go:373-379 asserts the shape of this
// population ("symbol mode above all, which is what most archive scanners
// emit") without anyone ever counting it. A decoder is a large piece of work and
// the split decides whether byb-9v0 is P1 or P2, so it is measured first.
//
// IT ASKS THE SHIPPED DECODER RATHER THAN PARSING SEGMENT HEADERS AGAIN. The
// classification this probe reports is exactly the one extractPage acts on:
// classify must return "" (the page is a single page-covering raster by every
// other arm), the placement's RawImage must come back as fileType "jbig2", and
// then decodeJBIG2Placement's verdict is the answer. A second header walk
// written for the probe could disagree with the decoder, and then the number
// would describe the probe.
//
// IT ASKS THE CODING-MODE QUESTION SEPARATELY FROM THE GATE QUESTION, and the
// first run of this probe is why. decodeJBIG2Placement refuses on DICTIONARY
// FACTS before it decodes anything -- a /Decode array, then a pixel count, then
// a page-size disagreement -- so a page stopped by one of those was never asked
// what coding mode it uses. Reporting only the first refusal said
// "unsupported-feature=0, malformed=6483" over a corpus whose own extract.go
// says symbol mode is common, which is the shape of an instrument answering a
// different question rather than a corpus with no symbol mode in it.
//
// So every row carries TWO verdicts: the gate that stopped extraction, and what
// DecodeEmbeddedStream says about the bytes regardless of that gate. Only the
// second prices byb-9v0; the difference between them prices byb-e7n.
//
// THE SENTINEL IS THE INTERNAL ONE, DELIBERATELY. errors.Is against the root
// package's ErrUnsupportedJBIG2Feature reports false here, because
// decodeJBIG2Placement returns jbig2.DecodeEmbeddedStream's error unwrapped and
// only the exported DecodeJBIG2Generic attaches that sentinel (jbig2.go). That
// is worth knowing beyond this probe: extractPage maps every one of these to a
// single divert reason, so on the extract path a caller cannot tell "byblos is
// not enough" from "the bytes are damaged" -- the distinction jbig2.go:82-91
// says the sentinel exists to draw.
//
// THE SEGMENT TYPE COMES OUT OF THE ERROR TEXT, which is a real coupling and is
// why it is stated here. segment_decode.go's default branch formats
// "%w: segment type %d (segment %d)", and this reads that %d back. If that
// wording changes, seg_type goes to -1 and the summary line says so rather than
// silently reporting every stream as one bucket.
//
// TWO LIMITS ON THE SPLIT, both structural:
//
//   - The dispatch returns on the FIRST segment it cannot handle, so a stream
//     carrying a symbol dictionary (type 0) AND a text region (types 4, 6, 7)
//     is counted once, under whichever came first. "Pages needing symbol mode"
//     is therefore the union of those buckets, not the sum of anything.
//   - A stream whose HEADERS do not parse fails before the dispatch and carries
//     no segment type at all. Those are damage rather than a missing feature --
//     the distinction ErrUnsupportedJBIG2Feature exists to draw (jbig2.go:82-91)
//     -- and they are reported separately, because a decoder does not recover
//     them and counting them here would overstate byb-9v0's payoff.
//
// It skips unless BYBLOS_JBIG2_CORPUS is set, so `go test ./...` is unaffected.
//
//	BYBLOS_JBIG2_CORPUS=<dir> BYBLOS_JBIG2_OUT=<file> BYBLOS_JOBS=8 \
//	  go test -run TestJBIG2CodingModeCensus -v -count=1 -timeout 60m .

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/jbig2"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// segTypeNames are T.88's segment type numbers. Only the ones byblos might meet
// are named; anything else is reported by number, which is enough to look up.
var segTypeNames = map[int]string{
	0:  "symbol-dictionary",
	4:  "intermediate-text-region",
	6:  "immediate-text-region",
	7:  "immediate-lossless-text-region",
	16: "pattern-dictionary",
	20: "intermediate-halftone-region",
	22: "immediate-halftone-region",
	23: "immediate-lossless-halftone-region",
	36: "intermediate-generic-region",
	40: "intermediate-refinement-region",
	42: "immediate-refinement-region",
	43: "immediate-lossless-refinement-region",
}

// symbolMode is the set byb-9v0 is about: a symbol dictionary and the text
// regions that consume one. They are counted as a union because the dispatch
// stops at the first of them it meets.
var symbolMode = map[int]bool{0: true, 4: true, 6: true, 7: true}

var segTypeRe = regexp.MustCompile(`segment type (\d+)`)

type jbig2Row struct {
	path string
	page int
	w, h int
	// gate is what actually stopped extractPage: "none" when the page returns a
	// raster today, otherwise the first refusal decodeJBIG2Placement made.
	gate string
	// coding is what the BYTES are, asked independently of gate: "decodes",
	// "unsupported-feature" or "malformed". This is the column byb-9v0 is
	// priced on.
	coding  string
	segType int // -1 when the message carried none
	msg     string
}

func (r jbig2Row) line() string {
	name := segTypeNames[r.segType]
	if r.segType < 0 {
		name = "-"
	} else if name == "" {
		name = "unnamed"
	}
	return strings.Join([]string{
		r.path, strconv.Itoa(r.page),
		fmt.Sprintf("%dx%d", r.w, r.h),
		r.gate, r.coding, strconv.Itoa(r.segType), name,
		strings.ReplaceAll(r.msg, "\t", " "),
	}, "\t")
}

// gateOf names the first dictionary fact decodeJBIG2Placement refuses on,
// mirroring its order exactly (jbig2.go). It is a classifier for the same
// refusals, not a second implementation of them: the row's msg still comes from
// the real call, and TestJBIG2CensusGateMatchesTheRealRefusal pins the two
// together.
func gateOf(msg string) string {
	switch {
	case msg == "":
		return "none"
	case strings.Contains(msg, "/Decode array"):
		return "decode-array"
	case strings.Contains(msg, "byblos renders a bilevel page"):
		return "pixel-limit"
	case strings.Contains(msg, "but image") && strings.Contains(msg, "dictionary says"):
		return "size-disagreement"
	case strings.Contains(msg, "no dictionary to check"):
		return "no-dictionary"
	default:
		return "coding-mode"
	}
}

// censusFile reports one row per page whose returned raster is a JBIG2 stream.
func censusFile(path string) (rows []jbig2Row, pages int) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	d, err := pdfdoc.Open(f)
	if err != nil {
		return nil, 0
	}
	ctx := context.Background()
	for n := 1; n <= d.PageCount(); n++ {
		pages++
		p, err := d.Page(n)
		if err != nil {
			continue
		}
		s, walkErr := content.Walk(ctx, p.Content, p.Scope, d)
		if walkErr != nil || s == nil {
			continue
		}
		// Every arm of classify must already be satisfied. A page that diverts
		// for any other reason is not one a JBIG2 decoder would rescue, and
		// counting it would price byb-9v0 against work it does not do.
		idx, reason := classify(p.CropBox, s, d.ImageInfo)
		if reason != "" {
			continue
		}
		placement := s.Images[idx]
		data, fileType, err := d.RawImage(placement.ID)
		if err != nil || fileType != "jbig2" {
			continue
		}
		r := jbig2Row{path: path, page: n, segType: -1}
		if info, ok := d.ImageInfo(placement.ID); ok {
			r.w, r.h = info.Width, info.Height
		}
		// What stops extraction today.
		if _, err := decodeJBIG2Placement(data, d.ImageInfo, placement.ID); err != nil {
			r.msg = err.Error()
		}
		r.gate = gateOf(r.msg)

		// What the BYTES are, asked whatever the gate said. A page refused on a
		// dictionary fact never reached this question, and 4,371 of them in the
		// first run of this probe are exactly why it is asked separately.
		switch _, err := jbig2.DecodeEmbeddedStream(data); {
		case err == nil:
			r.coding = "decodes"
		case errors.Is(err, jbig2.ErrUnsupportedFeature):
			r.coding = "unsupported-feature"
			if m := segTypeRe.FindStringSubmatch(err.Error()); m != nil {
				r.segType, _ = strconv.Atoi(m[1])
			}
		default:
			r.coding = "malformed"
			if r.msg == "" {
				r.msg = err.Error()
			}
		}
		rows = append(rows, r)
	}
	return rows, pages
}

func TestJBIG2CodingModeCensus(t *testing.T) {
	root := os.Getenv("BYBLOS_JBIG2_CORPUS")
	if root == "" {
		t.Skip("set BYBLOS_JBIG2_CORPUS to a directory of PDFs to run the byb-9v0 census")
	}
	outPath := os.Getenv("BYBLOS_JBIG2_OUT")
	if outPath == "" {
		t.Fatal("set BYBLOS_JBIG2_OUT to the TSV to write")
	}
	var paths []string
	if err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() && strings.EqualFold(filepath.Ext(p), ".pdf") {
			paths = append(paths, p)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatalf("no PDFs under %s", root)
	}
	workers := 8
	if v, err := strconv.Atoi(os.Getenv("BYBLOS_JOBS")); err == nil && v > 0 {
		workers = v
	}

	// Slot-indexed like the clause probe, so the output is in lexical path
	// order whatever the worker count and two runs diff cleanly.
	per := make([][]jbig2Row, len(paths))
	pages := make([]int, len(paths))
	start := time.Now()
	work := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				per[i], pages[i] = censusFile(paths[i])
			}
		}()
	}
	for i := range paths {
		work <- i
	}
	close(work)
	wg.Wait()
	elapsed := time.Since(start)

	out, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create %s: %v", outPath, err)
	}
	defer out.Close()
	var total, symbol, unparsed, gateOnly int
	byType := map[int]int{}
	byGate := map[string]int{}
	byCoding := map[string]int{}
	for i := range paths {
		for _, r := range per[i] {
			total++
			byGate[r.gate]++
			byCoding[r.coding]++
			// The pages a /Decode fix alone would release: a dictionary fact
			// stopped them and the bytes underneath decode cleanly. This is the
			// number that separates byb-e7n's payoff from byb-9v0's.
			if r.gate != "none" && r.coding == "decodes" {
				gateOnly++
			}
			if r.coding == "unsupported-feature" {
				byType[r.segType]++
				if r.segType < 0 {
					unparsed++
				}
				if symbolMode[r.segType] {
					symbol++
				}
			}
			if _, err := fmt.Fprintln(out, r.line()); err != nil {
				t.Fatalf("write %s: %v", outPath, err)
			}
		}
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close %s: %v", outPath, err)
	}
	var sum int
	for _, n := range pages {
		sum += n
	}
	t.Logf("files=%d pages=%d jbig2-rasters=%d extract-today=%d symbol-mode=%d seg-type-unparsed=%d gate-only=%d elapsed=%v",
		len(paths), sum, total, byGate["none"], symbol, unparsed, gateOnly, elapsed)
	for _, g := range []string{"none", "decode-array", "pixel-limit", "size-disagreement", "no-dictionary", "coding-mode"} {
		t.Logf("  gate   %-18s %d pages", g, byGate[g])
	}
	for _, c := range []string{"decodes", "unsupported-feature", "malformed"} {
		t.Logf("  coding %-18s %d pages", c, byCoding[c])
	}
	types := make([]int, 0, len(byType))
	for k := range byType {
		types = append(types, k)
	}
	sort.Ints(types)
	for _, k := range types {
		name := segTypeNames[k]
		if k < 0 {
			name = "(no segment type in the message)"
		} else if name == "" {
			name = "unnamed"
		}
		t.Logf("  seg type %3d %-36s %d pages", k, name, byType[k])
	}
}

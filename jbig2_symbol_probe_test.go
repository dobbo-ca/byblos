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
// second prices byb-9v0; the difference between them priced byb-e7n.
//
// byb-e7n then added three DICTIONARY columns -- decode, cs and bpc -- because
// pricing the /Decode gate said how many pages it holds and not what it would
// take to release them, and those are different questions. The first run to
// carry them is the acceptance run for the fix: applying the array must empty
// the decode-array gate, and any page left in it is one whose array byblos
// still declines, named by its own columns rather than by inference.
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
// byb-9v0 THEN ADDED THREE MORE COLUMNS, and the third is a different KIND of
// column from every other one here:
//
//   - globals, the size of the /DecodeParms /JBIG2Globals stream, because a
//     symbol dictionary usually lives in one and a page with a text region and
//     no dictionary is a different finding from a page with both.
//   - features, every segment's coding variant from jbig2.ProfileStream. The
//     coding column names the FIRST refusal and nothing else; this names all of
//     them, which is what prices the round after this one. A page carrying a
//     Huffman dictionary and a refinement text region needs both implemented,
//     and counting it under whichever came first would say otherwise.
//   - oracle, jbig2dec's verdict, which is the ONLY column that can fail a
//     decoder rather than describe one. Everything else asks byblos about
//     byblos: "coding=decodes" means the decoder returned a bitmap, not that
//     the bitmap is right, and a symbol-mode decoder that has misread one field
//     returns a full page of glyph-shaped noise without erroring. Until
//     byb-9v0 the gap did not exist, because the only thing byblos decoded was
//     the thing byblos also encoded and the round trip pinned it.
//
// It skips unless BYBLOS_JBIG2_CORPUS is set, so `go test ./...` is unaffected.
// The adjudication is opt-in because it forks jbig2dec once per decoded page.
//
//	BYBLOS_JBIG2_CORPUS=<dir> BYBLOS_JBIG2_OUT=<file> BYBLOS_JBIG2_ORACLE=1 \
//	  BYBLOS_JOBS=8 go test -run TestJBIG2CodingModeCensus -v -count=1 -timeout 60m .

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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

// symbolModeType is the set byb-9v0 was about, as SEGMENT TYPES scraped from the
// refusal message.
//
// IT READS ZERO NOW, AND THAT IS THE INSTRUMENT AND NOT THE CORPUS. Before
// byb-9v0 a symbol-mode stream was refused by the TYPE dispatch, whose message
// carries "segment type 0"; since byb-9v0 those types are decoded, and what is
// left is refused by a FEATURE check whose message names SDREFAGG and no type at
// all. So the regex finds nothing, segType stays -1, and a counter reading "the
// corpus contains no symbol mode" describes a corpus that is 99% symbol mode.
//
// The honest counter is symbolModeFeature below, which reads the header parse
// rather than the error string. This one is kept because the per-type histogram
// is still worth having for the types that ARE dispatched on -- pattern
// dictionaries and halftones -- and deleting it would take that with it.
var symbolModeType = map[int]bool{0: true, 4: true, 6: true, 7: true}

// symbolModeFeature reports whether a page's profile carries a symbol dictionary
// or a text region, decoded or not. It reads jbig2.ProfileStream's tokens, which
// come from the decoder's own header parse, so it says what the stream IS rather
// than what stopped it.
func symbolModeFeature(features string) bool {
	for _, f := range strings.Split(features, "|") {
		if strings.HasPrefix(f, "sd ") || strings.HasPrefix(f, "tr ") {
			return true
		}
	}
	return false
}

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
	// decode, cs and bpc are the dictionary facts that decide what a /Decode
	// array MEANS, and byb-e7n added them. The array alone is not enough: the
	// same [1 0] inverts a /DeviceGray sample and indexes a palette backwards
	// in an /Indexed space, so the colour space and the bit depth travel with
	// it. cs is "-" when /ColorSpace is an array or an indirect reference,
	// which is exactly the case ImageInfo declines to resolve.
	decode string
	cs     string
	bpc    int
	// globals is the size of the /DecodeParms /JBIG2Globals stream this page's
	// image names, or -1 when it names none. byb-9v0 added it: a text region's
	// symbol dictionary very often lives there, so a page with a text region
	// and no globals and a page with both are two different findings that the
	// coding column alone cannot separate.
	globals int
	// features is every segment's coding variant, from the decoder's own header
	// parse (jbig2.ProfileStream), joined by "|". The coding column names the
	// FIRST refusal; this names all of them, which is what prices whatever is
	// left after this round.
	features string
	// oracle is jbig2dec's verdict on the pages byblos now decodes: "agree",
	// "differ", "" when the comparison was not run.
	//
	// IT IS THE ONLY COLUMN THAT CAN FAIL A DECODER THAT RETURNS PLAUSIBLE
	// NOISE. Every other column here asks byblos about byblos. The MQ decoder
	// yields a decision for any input, so a symbol-mode decoder that has
	// misread one field does not error -- it returns a full-size page of
	// glyph-shaped rubbish, and "coding=decodes" counts it as a win. Before
	// byb-9v0 that gap did not exist, because the only thing byblos decoded was
	// the thing byblos also encoded and the round trip pinned it.
	oracle string
	msg    string
}

// decodeColumn formats an image's /Decode array for the census: "-" when the
// entry is absent, "?" when it is present in a shape pdfdoc could not read as
// numbers, and the numbers otherwise.
func decodeColumn(info pdfdoc.ImageInfo) string {
	switch {
	case !info.Decode:
		return "-"
	case info.DecodeArray == nil:
		return "?"
	}
	parts := make([]string, len(info.DecodeArray))
	for i, v := range info.DecodeArray {
		parts[i] = strconv.FormatFloat(v, 'g', -1, 64)
	}
	return "[" + strings.Join(parts, " ") + "]"
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
		r.decode, r.cs, strconv.Itoa(r.bpc),
		strconv.Itoa(r.globals), r.features, r.oracle,
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
		r := jbig2Row{path: path, page: n, segType: -1, decode: "-", cs: "-", globals: -1}
		globals, gerr := d.RawImageGlobals(placement.ID)
		if gerr != nil {
			// An entry that is there and does not resolve. Counted as a gate of
			// its own below rather than folded into the coding column: the
			// bytes are not the problem.
			r.gate, r.coding, r.msg = "globals-unreadable", "malformed", gerr.Error()
			rows = append(rows, r)
			continue
		}
		if globals != nil {
			r.globals = len(globals)
		}
		if info, ok := d.ImageInfo(placement.ID); ok {
			r.w, r.h = info.Width, info.Height
			r.decode, r.bpc = decodeColumn(info), info.BPC
			if info.ColorSpace != "" {
				r.cs = info.ColorSpace
			}
		}
		// What stops extraction today.
		if _, err := decodeJBIG2Placement(data, globals, d.ImageInfo, placement.ID); err != nil {
			r.msg = err.Error()
		}
		r.gate = gateOf(r.msg)

		// Every segment's coding variant, whatever the gate and whatever the
		// coding column say. This is the column that prices the NEXT round.
		if profile, err := jbig2.ProfileStream(globals, data); err == nil {
			parts := make([]string, 0, len(profile))
			for _, sp := range profile {
				switch {
				case sp.Err != "":
					parts = append(parts, fmt.Sprintf("t%d:parse-error", sp.Type))
				case sp.Feature != "":
					parts = append(parts, sp.Feature)
				default:
					parts = append(parts, fmt.Sprintf("t%d", sp.Type))
				}
			}
			r.features = strings.Join(parts, "|")
		} else {
			r.features = "framing-error"
		}

		// What the BYTES are, asked whatever the gate said. A page refused on a
		// dictionary fact never reached this question, and 4,371 of them in the
		// first run of this probe are exactly why it is asked separately.
		switch b, err := jbig2.DecodeEmbeddedStreamWithGlobals(globals, data); {
		case err == nil:
			r.coding = "decodes"
			r.oracle = adjudicateJBIG2(b, globals, data)
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

// oracleEnabled is set once from the environment: asking jbig2dec about every
// page that decodes costs a fork per page, which is a minute over the corpus and
// nothing against the run's 17.
var oracleEnabled = os.Getenv("BYBLOS_JBIG2_ORACLE") != ""

// adjudicate asks jbig2dec what the same bytes decode to and reports whether
// byblos agrees.
//
// It compares the RASTER, not a hash of the file: jbig2dec writes a PBM whose
// rows are padded to a byte exactly as this package's Bitmap is, so the pixel
// buffers are directly comparable and a disagreement can be counted rather than
// merely detected.
func adjudicateJBIG2(got *jbig2.Bitmap, globals, data []byte) string {
	if !oracleEnabled {
		return ""
	}
	dir, err := os.MkdirTemp("", "jbig2-oracle")
	if err != nil {
		return "oracle-error"
	}
	defer os.RemoveAll(dir)
	page := filepath.Join(dir, "page.jb2")
	out := filepath.Join(dir, "out.pbm")
	if err := os.WriteFile(page, data, 0o600); err != nil {
		return "oracle-error"
	}
	args := []string{"-e", "-q", "-t", "pbm", "-o", out}
	if len(globals) > 0 {
		g := filepath.Join(dir, "globals.jb2")
		if err := os.WriteFile(g, globals, 0o600); err != nil {
			return "oracle-error"
		}
		args = append(args, g)
	}
	args = append(args, page)
	if err := exec.Command("jbig2dec", args...).Run(); err != nil {
		// jbig2dec refusing a stream byblos read is a finding of its own, and
		// not necessarily byblos's: the two decoders have different limits.
		return "oracle-refused"
	}
	ref, err := os.ReadFile(out)
	if err != nil {
		return "oracle-error"
	}
	w, h, body, ok := parsePBMHeader(ref)
	if !ok {
		return "oracle-error"
	}
	if w != got.W || h != got.H {
		return "differ-size"
	}
	if len(body) < len(got.Pix) || !bytes.Equal(body[:len(got.Pix)], got.Pix) {
		return "differ"
	}
	return "agree"
}

// parsePBMHeader reads a binary PBM (P4) header and returns the raster bytes.
func parsePBMHeader(b []byte) (w, h int, body []byte, ok bool) {
	fields := make([]string, 0, 3)
	i := 0
	for len(fields) < 3 {
		for i < len(b) && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t' || b[i] == '\r') {
			i++
		}
		if i < len(b) && b[i] == '#' {
			for i < len(b) && b[i] != '\n' {
				i++
			}
			continue
		}
		start := i
		for i < len(b) && b[i] != ' ' && b[i] != '\n' && b[i] != '\t' && b[i] != '\r' {
			i++
		}
		if start == i {
			return 0, 0, nil, false
		}
		fields = append(fields, string(b[start:i]))
	}
	i++
	if fields[0] != "P4" || i > len(b) {
		return 0, 0, nil, false
	}
	w, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, nil, false
	}
	h, err = strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, nil, false
	}
	return w, h, b[i:], true
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
	var total, symbol, symbolPages, unparsed, gateOnly int
	byType := map[int]int{}
	byGate := map[string]int{}
	byCoding := map[string]int{}
	// byDecode keys on the three dictionary facts together, because no one of
	// them decides anything on its own (see jbig2Row.decode). Only rows that
	// declare a /Decode array are counted; the rest are the "-" column and are
	// already the difference between this total and byGate.
	byDecode := map[string]int{}
	// byFeature is the histogram byb-9v0's successor is priced from: every
	// coding variant present on a page that still does not extract, counted
	// once per page per distinct variant. A page carrying a Huffman dictionary
	// AND a refinement text region is in both buckets, because implementing
	// either alone would not release it.
	byFeature := map[string]int{}
	byOracle := map[string]int{}
	withGlobals, textNeedingGlobals := 0, 0
	for i := range paths {
		for _, r := range per[i] {
			total++
			byGate[r.gate]++
			byCoding[r.coding]++
			if r.oracle != "" {
				byOracle[r.oracle]++
			}
			if symbolModeFeature(r.features) {
				symbolPages++
			}
			if r.globals >= 0 {
				withGlobals++
				if strings.Contains(r.features, "tr ") {
					textNeedingGlobals++
				}
			}
			if r.gate != "none" {
				seen := map[string]bool{}
				for _, f := range strings.Split(r.features, "|") {
					if f == "" || seen[f] {
						continue
					}
					seen[f] = true
					byFeature[f]++
				}
			}
			if r.decode != "-" {
				byDecode[fmt.Sprintf("%s cs=%s bpc=%d coding=%s", r.decode, r.cs, r.bpc, r.coding)]++
			}
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
				if symbolModeType[r.segType] {
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
	t.Logf("files=%d pages=%d jbig2-rasters=%d extract-today=%d symbol-mode=%d symbol-mode-refused=%d "+
		"seg-type-unparsed=%d gate-only=%d elapsed=%v",
		len(paths), sum, total, byGate["none"], symbolPages, symbol, unparsed, gateOnly, elapsed)
	t.Logf("  symbol-mode counts pages whose HEADERS carry a symbol dictionary or text region; " +
		"symbol-mode-refused counts the far smaller set whose refusal message still names one of " +
		"those segment types. See symbolModeType for why the second reads zero after byb-9v0.")
	t.Logf("  globals: %d of %d rasters name a /JBIG2Globals stream; %d of those carry a text region",
		withGlobals, total, textNeedingGlobals)
	for _, g := range []string{"none", "decode-array", "pixel-limit", "size-disagreement", "no-dictionary",
		"globals-unreadable", "coding-mode"} {
		t.Logf("  gate   %-18s %d pages", g, byGate[g])
	}
	for _, c := range []string{"decodes", "unsupported-feature", "malformed"} {
		t.Logf("  coding %-18s %d pages", c, byCoding[c])
	}
	keys := make([]string, 0, len(byDecode))
	for k := range byDecode {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return byDecode[keys[i]] > byDecode[keys[j]] })
	for _, k := range keys {
		t.Logf("  decode %-52s %d pages", k, byDecode[k])
	}
	for _, k := range []string{"agree", "differ", "differ-size", "oracle-refused", "oracle-error"} {
		if byOracle[k] != 0 {
			t.Logf("  oracle %-18s %d pages", k, byOracle[k])
		}
	}
	// The adjudication is only meaningful where there was something to
	// adjudicate. A corpus subset with no JBIG2 raster in it is not a failure,
	// and an earlier version of this check said it was.
	if oracleEnabled && byCoding["decodes"] > 0 && byOracle["agree"] == 0 {
		t.Errorf("%d pages decoded, the jbig2dec adjudication was enabled, and not one of them agreed; "+
			"either every decode is wrong or the comparison is measuring itself", byCoding["decodes"])
	}

	feats := make([]string, 0, len(byFeature))
	for k := range byFeature {
		feats = append(feats, k)
	}
	sort.Slice(feats, func(i, j int) bool { return byFeature[feats[i]] > byFeature[feats[j]] })
	for i, k := range feats {
		if i == 40 {
			t.Logf("  feature (%d more variants below %d pages)", len(feats)-40, byFeature[k])
			break
		}
		t.Logf("  feature %-64s %d pages", k, byFeature[k])
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

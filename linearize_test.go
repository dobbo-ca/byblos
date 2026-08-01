package byblos

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// The acceptance suite for byb-1y7: ISO 32000-1:2008 Annex F linearization.
//
// ------------------------------------------------------------------------
// THE ORACLE TRAP, AND WHY NOTHING HERE ASSERTS ON EXIT STATUS
// ------------------------------------------------------------------------
//
// `qpdf --check-linearization` exits 0 for a file that is not linearized at
// all. Measured with qpdf 12.3.2:
//
//	genuinely linearized  -> stdout "<path>: no linearization errors"   exit 0
//	not linearized        -> stdout "<path> is not linearized"          exit 0
//	damaged hint tables   -> stdout EMPTY, warnings on stderr           exit 3
//
// So `cmd.Run() == nil` is true both for a correct linearizer and for one that
// does nothing whatsoever, and an exit-status assertion is a test that passes
// against a total failure. Every qpdf-backed assertion in this file therefore
// requires the literal string "no linearization errors" on STDOUT, and
// TestQpdfOracleRejectsAnUnlinearizedFile keeps that requirement honest by
// running the identical helper over a document nobody linearized and demanding
// it come back false.
//
// Use Output(), not CombinedOutput(): qpdf writes warnings to stderr and echoes
// the file path in them, so a Contains check over merged streams matches text
// that came from a failure.
//
// ------------------------------------------------------------------------
// WHAT THE ORACLE CANNOT SEE
// ------------------------------------------------------------------------
//
// qpdf checks the hint tables against the file's own offsets. It does NOT
// check the first-page property: a file whose page 2 is physically before page
// 1 earns "no linearization errors" as long as the hint tables agree with that
// wrong layout. And qpdf is not installed everywhere, so a check that only it
// can perform is a check that skips. Four tests here need no external tool:
//
//	TestLinearizedOutputLengthMatchesL   -- the /L defect qpdf downgrades to
//	                                        "is not linearized", exit 0
//	TestFirstPagePartition               -- the layout property, by re-scanning
//	                                        the output bytes independently
//	TestPrimaryHintStreamDescribesTheFileItIsIn
//	                                     -- the hint tables, decoded from the
//	                                        output and checked against the
//	                                        offsets the output actually has
//	TestLinearizedOutputHasNoOrphanObjects
//	                                     -- every object written is an object
//	                                        the finished file refers to
//
// ------------------------------------------------------------------------
// WHY THERE ARE FIXTURES IN THIS FILE AND NOT IN internal/corpus
// ------------------------------------------------------------------------
//
// The corpus does not reach the shapes that make the hint tables and the
// partition do any work. Measured on every readable corpus document, both under
// `qpdf --linearize` and on byblos's own output, via
// `qpdf --show-linearization`:
//
//   - nshared_objects is 0 on every page of every one of them. (nshared_total
//     is NOT 0 -- it is 3, 4 or 5, because the first page's own objects are all
//     shared-table members by construction. What is empty is the per-page
//     identifier columns.) qpdf only cross-checks a page's shared list when its
//     computed list is non-empty (QPDF_linearization.cc:957-965), so a wrong
//     identifier column is invisible on the corpus alone. sharedObjectsFixture
//     is the answer: three pages sharing one font and one image XObject, which
//     both linearizers turn into nshared_objects 2 on pages 2 and 3.
//   - first_shared_obj and first_shared_offset are 0 in all 29 of them, because
//     part 8 -- objects shared between LATER pages that the first page does not
//     use -- is empty in every one. laterPageSharedFixture is the answer.
//   - no corpus catalog has a key beyond /Pages, so nothing exercises the split
//     Annex F.3.5 and F.3.10 make between document-level material a reader needs
//     before it can display anything and material it does not.
//     documentLevelFixture is the answer.
//   - no corpus document, and no pdfcpu rewrite of one, has an indirect stream
//     /Length -- the input shape that made the linearizer emit orphan objects
//     and qpdf refuse four real documents. indirectLengthFixture is the answer.
//
// These live here rather than in internal/corpus because corpus.All() is
// enumerated by the committed poppler golden (testdata/oracle/poppler.json);
// adding a document there fails TestInspectAgreesWithPoppler until `make
// oracle` regenerates it, which would mix an unrelated failure into this
// stage. Promoting them is a mechanical move plus a golden regeneration.

// --- the document set under test -------------------------------------------

type linCase struct {
	name string
	data []byte
	// skip is set for a case whose fixture is not on this machine.
	skip string
}

// linearizeCases is the sweep every structural test runs over. It is
// deliberately not just corpus.All(): 24 of the 28 corpus documents have a
// single page, where part 7 is empty, every per-page hint column is zero-width
// and the first-page partition is trivially satisfied by ANY layout. A
// linearizer that is correct only on one-page documents passes a corpus-only
// sweep completely.
func linearizeCases(t *testing.T) []linCase {
	t.Helper()
	var out []linCase
	for _, d := range corpus.All() {
		if d.Name == "malformed" {
			continue // covered by TestOptimizeLinearizeMalformedInput: it must error.
		}
		out = append(out, linCase{name: d.Name, data: d.Data})
	}
	out = append(out,
		linCase{name: "shared-objects", data: sharedObjectsFixture(false)},
		linCase{name: "shared-objects-outlines", data: sharedObjectsFixture(true)},
		linCase{name: "later-page-shared", data: laterPageSharedFixture()},
		linCase{name: "document-level", data: documentLevelFixture()},
		linCase{name: "indirect-length", data: indirectLengthFixture()},
	)
	// bookletTest.pdf is 64 pages and stores its objects in object streams, so
	// it is the only input here that exercises "unpack the container objects
	// and do not carry them into part 9". It is an oracle-style fixture: it
	// comes from the pdfcpu module cache, so it skips rather than fails when
	// the cache is absent.
	if b, ok := moduleCacheFixture("bookletTest.pdf"); ok {
		out = append(out, linCase{name: "bookletTest", data: b})
	} else {
		out = append(out, linCase{name: "bookletTest",
			skip: "pdfcpu testdata not in the module cache"})
	}
	return out
}

func moduleCacheFixture(name string) ([]byte, bool) {
	matches, err := filepath.Glob(filepath.Join(
		os.Getenv("HOME"), "go", "pkg", "mod", "github.com", "pdfcpu",
		"pdfcpu@*", "pkg", "testdata", name))
	if err != nil || len(matches) == 0 {
		// GOPATH may not be $HOME/go; ask the toolchain.
		goBin, lookErr := exec.LookPath("go")
		if lookErr != nil {
			return nil, false
		}
		outB, runErr := exec.Command(goBin, "env", "GOMODCACHE").Output()
		if runErr != nil {
			return nil, false
		}
		matches, err = filepath.Glob(filepath.Join(
			strings.TrimSpace(string(outB)), "github.com", "pdfcpu",
			"pdfcpu@*", "pkg", "testdata", name))
		if err != nil || len(matches) == 0 {
			return nil, false
		}
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		return nil, false
	}
	return b, true
}

// linearized runs the capability under test and returns the output bytes.
func linearized(t *testing.T, in []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Optimize(&out, bytes.NewReader(in), OptimizeOptions{Linearize: true}); err != nil {
		t.Fatalf("Optimize(Linearize:true): %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("Optimize(Linearize:true) wrote no bytes and returned no error")
	}
	return out.Bytes()
}

func writeTemp(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name+".pdf")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
	return p
}

// --- T1 / T2: the qpdf oracle ----------------------------------------------

const qpdfClean = "no linearization errors"

// qpdfSaysLinearizationIsClean is the ONLY place this file talks to qpdf, so
// there is exactly one place the stdout rule can be got wrong. It returns the
// verdict and a diagnostic; it never fails the test itself, which is what lets
// TestQpdfOracleRejectsAnUnlinearizedFile assert the negative on the same code
// path the positive tests use.
func qpdfSaysLinearizationIsClean(t *testing.T, path string) (bool, string) {
	t.Helper()
	qpdf, err := exec.LookPath("qpdf")
	if err != nil {
		t.Skipf("qpdf not installed; this is the byb-1y7 acceptance gate: %v", err)
	}
	cmd := exec.Command(qpdf, "--check-linearization", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, runErr := cmd.Output()
	// runErr is deliberately not consulted for the verdict. qpdf exits 0 on a
	// file that is not linearized at all and 3 on one whose hint tables are
	// wrong, so the exit status separates neither case from success.
	diag := fmt.Sprintf("exit=%v\nstdout: %s\nstderr: %s",
		runErr, strings.TrimSpace(string(stdout)), strings.TrimSpace(stderr.String()))
	return bytes.Contains(stdout, []byte(qpdfClean)), diag
}

// TestLinearizedOutputPassesQpdf is the acceptance gate. It catches every
// defect qpdf's checker can see, measured: a wrong /E, /O, /T or /N; a /H
// offset or length off by one; a page-offset entry with the wrong object count
// or page length; a truncated hint stream; a /H that does not point at a
// stream; a shared-object group length that disagrees with the file; and, on
// the outlines fixture, a missing outline hint table (verified by patching /O
// out of a qpdf-linearized copy of that exact fixture: "WARNING: incorrect
// object count in outline hint table", empty stdout, exit 3).
//
// It cannot catch a wrong object ORDER: measured, a file whose page 2 sits
// inside the first-page section earns "no linearization errors" as long as the
// hint tables describe that layout accurately. TestFirstPagePartition rejects it.
//
// It DOES catch a wrong /L -- not as an error, but because qpdf then prints
// "is not linearized", which fails the stdout rule above.
// TestLinearizedOutputLengthMatchesL still earns its place: it is the /L check
// that survives qpdf being absent.
//
// And it IS absent, on any machine that has not installed it, where
// exec.LookPath turns this whole gate into a silent skip. Measured, a build
// emitting no hint stream at all used to pass everything else in this file under
// PATH=/usr/bin:/bin. TestPrimaryHintStreamDescribesTheFileItIsIn is the same
// coverage with no external tool, and .github/workflows/ci.yml installs qpdf so
// that CI is not one runner-image change away from losing the oracle.
func TestLinearizedOutputPassesQpdf(t *testing.T) {
	if _, err := exec.LookPath("qpdf"); err != nil {
		t.Skipf("qpdf not installed; this is the byb-1y7 acceptance gate: %v", err)
	}
	checked, multiPage := 0, 0
	for _, c := range linearizeCases(t) {
		t.Run(c.name, func(t *testing.T) {
			if c.skip != "" {
				t.Skip(c.skip)
			}
			out := linearized(t, c.data)
			ok, diag := qpdfSaysLinearizationIsClean(t, writeTemp(t, c.name, out))
			if !ok {
				t.Fatalf("qpdf --check-linearization did not print %q\n%s", qpdfClean, diag)
			}
			checked++
			if p, err := parseLinDict(out); err == nil && p["N"] > 1 {
				multiPage++
			}
		})
	}
	// Both guards exist because a sweep that quietly stopped covering anything
	// is the failure mode this repo has shipped before.
	if checked == 0 {
		t.Error("no document reached the oracle; this test is vacuous")
	}
	if multiPage == 0 {
		t.Error("every document checked had a single page: part 7 was empty in all of " +
			"them, so the page-offset hint table's per-page columns and the first-page " +
			"partition were never exercised")
	}
	t.Logf("qpdf accepted %d linearized documents, %d of them multi-page", checked, multiPage)
}

// TestQpdfOracleRejectsAnUnlinearizedFile is the negative control for the test
// above, and it is not optional. If qpdf were upgraded, renamed a flag, or the
// helper mistakenly matched on exit status, TestLinearizedOutputPassesQpdf
// would go green over any output at all -- including a byte-for-byte copy of
// the input. This runs the identical helper over documents that were never
// linearized and requires the answer to be no.
func TestQpdfOracleRejectsAnUnlinearizedFile(t *testing.T) {
	for _, name := range []string{"born-digital", "mixed", "dup-raster"} {
		in := corpusDoc(t, name)
		ok, diag := qpdfSaysLinearizationIsClean(t, writeTemp(t, name, in))
		if ok {
			t.Fatalf("the oracle reported %q for %s, which nobody linearized; "+
				"the check is not testing what it claims\n%s", qpdfClean, name, diag)
		}
	}
	// And the same for a linearized file whose /L was corrupted: qpdf reports
	// "is not linearized" and exits 0 for that, which is why /L needs its own
	// test rather than relying on this one.
	t.Log("oracle correctly refuses to certify a document that was not linearized")
}

// --- T3: /L, the defect qpdf silently downgrades ----------------------------

var linDictRe = regexp.MustCompile(`(?s)/Linearized\s+1(.*?)>>`)

// parseLinDict reads the linearization parameter dictionary out of a file's
// own first bytes. ISO 32000-1 Annex F.2.2 requires that dictionary to be the
// first object and to fit entirely within the first 1024 bytes, so scanning the
// prefix is a complete test rather than a heuristic -- the same reasoning
// isLinearized (optimize.go) is built on.
func parseLinDict(b []byte) (map[string]int, error) {
	m := linDictRe.FindSubmatch(b[:min(len(b), linearizationWindow)])
	if m == nil {
		return nil, fmt.Errorf("no linearization parameter dictionary in the first %d bytes",
			linearizationWindow)
	}
	// Everything AFTER "/Linearized 1", so that "/L" cannot match the "/L" of
	// "/Linearized" itself.
	body := m[1]
	out := map[string]int{}
	for _, k := range []string{"L", "O", "E", "N", "T"} {
		km := regexp.MustCompile(`/` + k + `\s+(\d+)\b`).FindSubmatch(body)
		if km == nil {
			return nil, fmt.Errorf("linearization dictionary has no /%s: %s", k, body)
		}
		v, err := strconv.Atoi(string(km[1]))
		if err != nil {
			return nil, fmt.Errorf("/%s: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
}

// TestLinearizedOutputLengthMatchesL closes the hole in the oracle. Measured:
// a linearized file whose /L is one byte off makes qpdf print
// "<path> is not linearized" and exit 0 -- indistinguishable, by exit status
// AND by our stdout rule, from success on a file nobody touched. qpdf's own
// reader treats /L as the test for whether a file counts as linearized at all
// (QPDF_linearization.cc:424-428), so it can never report a bad /L as an error.
//
// This needs no external tool: /L is defined by Table F.1 as the length of the
// entire file, so the assertion is arithmetic on the output.
func TestLinearizedOutputLengthMatchesL(t *testing.T) {
	for _, c := range linearizeCases(t) {
		t.Run(c.name, func(t *testing.T) {
			if c.skip != "" {
				t.Skip(c.skip)
			}
			out := linearized(t, c.data)
			p, err := parseLinDict(out)
			if err != nil {
				t.Fatalf("output is not linearized: %v", err)
			}
			if p["L"] != len(out) {
				t.Errorf("/L = %d but the file is %d bytes; poppler reports this as "+
					"Optimized: no and qpdf reports it as \"is not linearized\", exit 0",
					p["L"], len(out))
			}
			if p["N"] < 1 {
				t.Errorf("/N = %d; a document has at least one page", p["N"])
			}
			if p["O"] < 1 {
				t.Errorf("/O = %d; it must name the first page's object", p["O"])
			}
			if p["E"] <= 0 || p["E"] > len(out) {
				t.Errorf("/E = %d, outside a %d-byte file", p["E"], len(out))
			}
		})
	}
}

// TestPdfinfoReportsOptimized is a second, independent witness for the same
// property, from a different codebase. poppler's "Optimized" line is exactly
// the "/Linearized dictionary exists and /L equals the file size" test --
// measured across a full defect matrix, it is the ONLY defect poppler catches
// and it is precisely the one qpdf misses. It is worth its own gated assertion
// because agreement between two implementations on /L is stronger evidence than
// our own arithmetic agreeing with itself.
func TestPdfinfoReportsOptimized(t *testing.T) {
	bin, err := exec.LookPath("pdfinfo")
	if err != nil {
		t.Skipf("pdfinfo not installed (poppler): %v", err)
	}
	for _, c := range linearizeCases(t) {
		t.Run(c.name, func(t *testing.T) {
			if c.skip != "" {
				t.Skip(c.skip)
			}
			out := linearized(t, c.data)
			raw, err := exec.Command(bin, writeTemp(t, c.name, out)).Output()
			if err != nil {
				t.Fatalf("pdfinfo: %v", err)
			}
			var line string
			for _, l := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(l, "Optimized:") {
					line = l
				}
			}
			if line == "" {
				t.Fatalf("pdfinfo printed no Optimized line:\n%s", raw)
			}
			if !strings.Contains(line, "yes") {
				t.Errorf("pdfinfo says %q; poppler only says yes when a /Linearized "+
					"dictionary is present AND /L equals the file size", strings.TrimSpace(line))
			}
		})
	}
}

// --- T4: the first-page partition, with no external tool --------------------

var (
	objStartRe  = regexp.MustCompile(`(?m)^(\d+)\s+(\d+)\s+obj\b`)
	refRe       = regexp.MustCompile(`(\d+)\s+(\d+)\s+R\b`)
	pageUpRe    = regexp.MustCompile(`/(Parent|Thumb)\s+\d+\s+\d+\s+R`)
	pageTypeRe  = regexp.MustCompile(`/Type\s*/Page\b`)
	pagesTypeRe = regexp.MustCompile(`/Type\s*/Pages\b`)
	rootRe      = regexp.MustCompile(`/Root\s+(\d+)\s+\d+\s+R`)
	infoRe      = regexp.MustCompile(`/Info\s+(\d+)\s+\d+\s+R`)
)

type objSpan struct {
	start, end int
	// extent is start through the newline after "endobj", i.e. the number of
	// bytes the object occupies. It is what every LENGTH in a hint table is
	// summed from, so it has to match the writer's own accounting exactly.
	extent int
	body   []byte
}

// scanObjects re-parses the output with a regex over `N G obj` rather than
// reusing whatever object table the writer built. That is deliberate: a check
// fed by the writer's own bookkeeping confirms the writer agrees with itself,
// which is the one thing that is true even of a broken layout.
func scanObjects(b []byte) map[int]objSpan {
	locs := objStartRe.FindAllSubmatchIndex(b, -1)
	out := make(map[int]objSpan, len(locs))
	for i, loc := range locs {
		num, err := strconv.Atoi(string(b[loc[2]:loc[3]]))
		if err != nil {
			continue
		}
		start := loc[0]
		end := len(b)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := b[start:end]
		objEnd := end
		if k := bytes.LastIndex(body, []byte("endobj")); k >= 0 {
			objEnd = start + k + len("endobj")
		}
		extent := objEnd - start
		if objEnd < len(b) && b[objEnd] == '\n' {
			extent++
		}
		out[num] = objSpan{start: start, end: objEnd, extent: extent, body: body}
	}
	return out
}

// stripStream removes a stream's payload before references are scanned. Without
// it, deflated bytes that happen to spell "12 0 R" invent references and the
// closure below grows for no reason. The dictionary, which is where real
// references live, is kept.
func stripStream(body []byte) []byte {
	s := bytes.Index(body, []byte("\nstream"))
	e := bytes.Index(body, []byte("endstream"))
	if s >= 0 && e > s {
		return append(append([]byte{}, body[:s]...), body[e:]...)
	}
	return body
}

// outgoingRefs lists the objects body names. A PAGE dictionary's /Parent and
// /Thumb are dropped, and only a page dictionary's: /Parent points back at the
// page tree, which Annex F.3.10 files in part 9, and /Thumb at a thumbnail no
// reader needs in order to show the page. internal/pdfdoc drops exactly these
// two, from exactly page dictionaries, when it builds its graph -- a checker
// that dropped /Parent everywhere would silently stop following an outline
// item's parent link or an AcroForm field's, and one that followed it from a
// page would demand part-9 material before /E.
func outgoingRefs(body []byte) []int {
	b := stripStream(body)
	if pageTypeRe.Match(b) {
		b = pageUpRe.ReplaceAll(b, nil)
	}
	var out []int
	for _, m := range refRe.FindAllSubmatch(b, -1) {
		if n, err := strconv.Atoi(string(m[1])); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// reachable walks from start. barrier holds every page object and every
// page-tree node: the walk does not enter one it did not start from, because
// an /Outlines destination or a /Next link legitimately names a page that lives
// past /E. Verified against the reference implementation: without that rule
// qpdf's OWN linearized output for a document with outlines fails check (A)
// below, on nothing worse than the outline item's /Dest naming page 2.
func reachable(objs map[int]objSpan, start int, barrier map[int]bool) map[int]bool {
	seen := map[int]bool{}
	stack := []int{start}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[n] {
			continue
		}
		o, ok := objs[n]
		if !ok {
			continue
		}
		if n != start && barrier[n] {
			continue
		}
		seen[n] = true
		stack = append(stack, outgoingRefs(o.body)...)
	}
	return seen
}

// pageSets splits the scanned objects into the leaf pages and the closure
// barrier, which is the leaf pages plus every /Pages node.
func pageSets(objs map[int]objSpan) (pages, barrier map[int]bool) {
	pages, barrier = map[int]bool{}, map[int]bool{}
	for n, o := range objs {
		b := stripStream(o.body)
		if pageTypeRe.Match(b) {
			pages[n], barrier[n] = true, true
		}
		if pagesTypeRe.Match(b) {
			barrier[n] = true
		}
	}
	return pages, barrier
}

// annexFPart4Keys are the catalog entries a reader consults before it can
// display anything. Annex F.3.5 puts what they reach in part 4, which is before
// /E; every OTHER catalog key -- /Metadata, /Names, /StructTreeRoot,
// /PageLabels -- names document-level material F.3.10 files in part 9, which is
// legitimately after it. The list is the same one internal/pdfdoc partitions on.
//
// Getting this distinction wrong in the CHECKER rather than the writer is the
// quiet failure: demanding the whole catalog closure before /E makes half (A)
// fire on a correctly linearized file that qpdf certifies, and every real
// Acrobat or Word PDF has a catalog /Metadata.
var annexFPart4Keys = []string{"ViewerPreferences", "PageMode", "Threads", "OpenAction", "AcroForm"}

// firstPageRequired is the set of objects Annex F requires before /E: the
// catalog, the closure of the open-document catalog keys, the closure of the
// first page, and -- only when /PageMode is /UseOutlines, per F.3.8 -- the
// outline tree.
func firstPageRequired(objs map[int]objSpan, root, first int, barrier map[int]bool) map[int]bool {
	req := reachable(objs, first, barrier)
	req[root] = true

	cat := stripStream(objs[root].body)
	var seeds []int
	for _, k := range annexFPart4Keys {
		if v, ok := dictEntry(cat, k); ok {
			seeds = append(seeds, refsIn(v)...)
		}
	}
	if v, ok := dictEntry(cat, "PageMode"); ok && bytes.Contains(v, []byte("UseOutlines")) {
		if o, ok := dictEntry(cat, "Outlines"); ok {
			seeds = append(seeds, refsIn(o)...)
		}
	}
	for _, s := range seeds {
		if barrier[s] {
			// A seed that is itself a page or a page-tree node is not entered,
			// which is how internal/pdfdoc treats one: an /OpenAction naming
			// page 2 must not drag page 2's objects into the first-page set.
			continue
		}
		for n := range reachable(objs, s, barrier) {
			req[n] = true
		}
	}
	return req
}

// --- a dictionary-entry scanner, so the checker can reason key by key --------
//
// A regex cannot do this: a catalog entry's value is routinely a nested
// dictionary (/Names) or an array (/OpenAction), and Annex F files
// document-level material by WHICH KEY names it. The checker has to be able to
// say "everything /OpenAction reaches" without also saying "everything
// /Metadata reaches".

func isPDFSpace(c byte) bool {
	return c == 0 || c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == ' '
}

func isPDFDelim(c byte) bool {
	return isPDFSpace(c) || bytes.IndexByte([]byte("()<>[]{}/%"), c) >= 0
}

func skipPDFSpace(b []byte, i int) int {
	for i < len(b) && isPDFSpace(b[i]) {
		i++
	}
	return i
}

// tokenEnd returns the index just past the token at i, which may be a name.
func tokenEnd(b []byte, i int) int {
	if i < len(b) && b[i] == '/' {
		i++
	}
	for i < len(b) && !isPDFDelim(b[i]) {
		i++
	}
	return i
}

func stringEnd(b []byte, i int) int {
	depth := 0
	for i < len(b) {
		switch b[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}
	return len(b)
}

// valueEnd returns the index just past the object value that starts at i.
func valueEnd(b []byte, i int) int {
	if i >= len(b) {
		return len(b)
	}
	switch {
	case bytes.HasPrefix(b[i:], []byte("<<")):
		depth, j := 0, i
		for j < len(b) {
			switch {
			case bytes.HasPrefix(b[j:], []byte("<<")):
				depth++
				j += 2
			case bytes.HasPrefix(b[j:], []byte(">>")):
				depth--
				j += 2
				if depth == 0 {
					return j
				}
			case b[j] == '(':
				j = stringEnd(b, j)
			default:
				j++
			}
		}
		return j
	case b[i] == '[':
		depth, j := 0, i
		for j < len(b) {
			switch b[j] {
			case '[':
				depth++
				j++
			case ']':
				depth--
				j++
				if depth == 0 {
					return j
				}
			case '(':
				j = stringEnd(b, j)
			default:
				j++
			}
		}
		return j
	case b[i] == '(':
		return stringEnd(b, i)
	case b[i] == '<':
		if k := bytes.IndexByte(b[i:], '>'); k >= 0 {
			return i + k + 1
		}
		return len(b)
	}
	// A bare token: a name, a number, a boolean, null -- or the three tokens of
	// an indirect reference, which have to be taken together or the "0 R" would
	// be read as the next entry's key.
	j := tokenEnd(b, i)
	if allDigits(b[i:j]) {
		k := skipPDFSpace(b, j)
		g := tokenEnd(b, k)
		if g > k && allDigits(b[k:g]) {
			r := skipPDFSpace(b, g)
			if r < len(b) && b[r] == 'R' && tokenEnd(b, r) == r+1 {
				return r + 1
			}
		}
	}
	return j
}

func allDigits(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// dictEntry returns the raw bytes of key's value in the dictionary body opens
// with, looking only at that dictionary's own entries and not at nested ones.
func dictEntry(body []byte, key string) ([]byte, bool) {
	i := bytes.Index(body, []byte("<<"))
	if i < 0 {
		return nil, false
	}
	for i += 2; i < len(body); {
		switch {
		case bytes.HasPrefix(body[i:], []byte(">>")):
			return nil, false
		case body[i] == '/':
			j := tokenEnd(body, i)
			name := string(body[i+1 : j])
			j = skipPDFSpace(body, j)
			end := valueEnd(body, j)
			if name == key {
				return body[j:end], true
			}
			i = end
		default:
			i++
		}
	}
	return nil, false
}

func refsIn(b []byte) []int {
	var out []int
	for _, m := range refRe.FindAllSubmatch(b, -1) {
		if n, err := strconv.Atoi(string(m[1])); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// firstPageReport is the two-sided check the oracle cannot perform.
type firstPageReport struct {
	missing  []int // (A) required in the first-page section, but ends after /E
	leak     []int // (B) private to a later page, but starts before /E
	pageLeak []int // (B), restricted to page dictionaries: the readable sub-case
	npages   int
}

func checkFirstPage(b []byte, p map[string]int) (firstPageReport, error) {
	objs := scanObjects(b)
	if len(objs) == 0 {
		return firstPageReport{}, fmt.Errorf("no `N G obj` headers found in %d bytes", len(b))
	}
	rm := rootRe.FindSubmatch(b)
	if rm == nil {
		return firstPageReport{}, fmt.Errorf("no /Root in the trailer")
	}
	root, err := strconv.Atoi(string(rm[1]))
	if err != nil {
		return firstPageReport{}, fmt.Errorf("/Root: %w", err)
	}

	pageObjs, barrier := pageSets(objs)
	if _, ok := objs[root]; !ok {
		return firstPageReport{}, fmt.Errorf("/Root names object %d, which the file does not hold", root)
	}
	first := firstPageRequired(objs, root, p["O"], barrier)

	rep := firstPageReport{npages: p["N"]}
	E := p["E"]
	for n := range first {
		if objs[n].end > E {
			rep.missing = append(rep.missing, n)
		}
	}
	var others []int
	for n := range pageObjs {
		if n != p["O"] {
			others = append(others, n)
		}
	}
	slices.Sort(others)
	private := map[int]bool{}
	for _, o := range others {
		for n := range reachable(objs, o, barrier) {
			if !first[n] {
				private[n] = true
			}
		}
	}
	for n := range private {
		if objs[n].start < E {
			rep.leak = append(rep.leak, n)
		}
	}
	for _, o := range others {
		if objs[o].start < E {
			rep.pageLeak = append(rep.pageLeak, o)
		}
	}
	slices.Sort(rep.missing)
	slices.Sort(rep.leak)
	slices.Sort(rep.pageLeak)
	return rep, nil
}

// TestFirstPagePartition is the test qpdf cannot be a substitute for. A
// deliberately scrambled file -- page 2's objects physically before page 1's,
// with hint tables that describe that layout accurately -- earns
// "no linearization errors" and exit 0. This rejects it.
//
// (A) alone is not enough: the scrambled file satisfies (A). (B) is the
// discriminator, and (B) is vacuous on a one-page document, which is why the
// sweep must contain multi-page inputs and why this test counts them.
func TestFirstPagePartition(t *testing.T) {
	multiPage := 0
	for _, c := range linearizeCases(t) {
		t.Run(c.name, func(t *testing.T) {
			if c.skip != "" {
				t.Skip(c.skip)
			}
			out := linearized(t, c.data)
			// The scanner below cannot see inside an object stream, so an
			// output that used them would make this test blind rather than
			// failing. Annex F.3.1 forbids a compressed page dictionary
			// anyway, and byblos's design emits classic cross-reference tables
			// only.
			if bytes.Contains(out, []byte("/ObjStm")) {
				t.Fatal("output contains an object stream; the first-page scanner cannot " +
					"see objects inside one, so this check would silently stop testing anything")
			}
			p, err := parseLinDict(out)
			if err != nil {
				t.Fatalf("output is not linearized: %v", err)
			}
			rep, err := checkFirstPage(out, p)
			if err != nil {
				t.Fatalf("re-scanning the output: %v", err)
			}
			if len(rep.missing) > 0 {
				t.Errorf("(A) objects %v are needed to render page 1 but end after /E = %d; "+
					"a progressive viewer would stall on them", rep.missing, p["E"])
			}
			if len(rep.leak) > 0 {
				t.Errorf("(B) objects %v belong to pages 2..%d but start before /E = %d; "+
					"they are downloaded before the first page can be shown",
					rep.leak, p["N"], p["E"])
			}
			if len(rep.pageLeak) > 0 {
				t.Errorf("page dictionaries %v other than /O = %d start before /E = %d",
					rep.pageLeak, p["O"], p["E"])
			}
			if rep.npages > 1 {
				multiPage++
			}
		})
	}
	if multiPage == 0 {
		t.Error("no multi-page document reached this check, so half (B) -- the only half " +
			"that rejects a scrambled layout -- never ran")
	}
	t.Logf("first-page partition verified on %d multi-page documents", multiPage)
}

// TestFirstPagePartitionCheckerRejectsAScrambledLayout proves the checker
// above is not a function that returns "no problems" unconditionally. It builds
// two byte-identical sets of objects laid out two ways and requires opposite
// verdicts.
//
// This runs with no implementation and no external tool, so it stays green
// through the RED stage: it tests the TEST. Without it, a checker with an
// inverted comparison or an empty closure would make TestFirstPagePartition
// pass on any output at all, which is the exact failure this file's oracle
// discipline exists to prevent.
func TestFirstPagePartitionCheckerRejectsAScrambledLayout(t *testing.T) {
	clean := buildPartitionFixture(false)
	pc, err := parseLinDict(clean)
	if err != nil {
		t.Fatalf("clean fixture: %v", err)
	}
	rep, err := checkFirstPage(clean, pc)
	if err != nil {
		t.Fatalf("clean fixture: %v", err)
	}
	if len(rep.missing) != 0 || len(rep.leak) != 0 || len(rep.pageLeak) != 0 {
		t.Fatalf("the checker rejects a correctly partitioned file: %+v", rep)
	}

	scrambled := buildPartitionFixture(true)
	ps, err := parseLinDict(scrambled)
	if err != nil {
		t.Fatalf("scrambled fixture: %v", err)
	}
	rep, err = checkFirstPage(scrambled, ps)
	if err != nil {
		t.Fatalf("scrambled fixture: %v", err)
	}
	if len(rep.leak) == 0 {
		t.Error("the checker accepts a file whose page 2 sits inside the first-page " +
			"section; half (B) is not doing anything")
	}
	if len(rep.pageLeak) == 0 {
		t.Error("the checker did not notice a second page dictionary before /E")
	}
	// Half (A) must NOT fire here: the scrambled file still has everything page
	// 1 needs before /E. Asserting that keeps the two halves from being
	// conflated into one check that happens to fire.
	if len(rep.missing) != 0 {
		t.Errorf("half (A) fired on the scrambled fixture (%v); it is meant to be "+
			"satisfied there, which is exactly why half (B) has to exist", rep.missing)
	}
}

// buildPartitionFixture emits a minimal file that is linearized only in the
// sense that checkFirstPage cares about: a parameter dictionary with a truthful
// /E and /O, a trailer with /Root, and objects at known offsets. It is never
// opened by a PDF reader, so it needs no valid cross-reference table.
//
// /E is written with a fixed seven-digit width so that patching it afterwards
// cannot move any of the offsets it describes.
func buildPartitionFixture(scramble bool) []byte {
	const (
		catalog   = 2
		tree      = 3
		page1     = 4
		page1Cont = 5
		page2     = 6
		page2Cont = 7
	)
	bodies := map[int]string{
		catalog:   fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", tree),
		tree:      fmt.Sprintf("<< /Type /Pages /Count 2 /Kids [ %d 0 R %d 0 R ] >>", page1, page2),
		page1:     fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Contents %d 0 R >>", tree, page1Cont),
		page1Cont: "<< /Length 8 >>\nstream\npage one\nendstream",
		page2:     fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Contents %d 0 R >>", tree, page2Cont),
		page2Cont: "<< /Length 8 >>\nstream\npage two\nendstream",
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	// Part 2, fixed width so the patch below is offset-neutral.
	fmt.Fprintf(&buf, "1 0 obj\n<< /Linearized 1 /L 0000000 /H [ 0 0 ] /O %d /E 0000000 /N 2 /T 0000000 >>\nendobj\n", page1)
	emit := func(n int) {
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, bodies[n])
	}
	emit(catalog)
	var e int
	if scramble {
		// Page 2 first: still self-consistent, still passes half (A).
		emit(page2)
		emit(page2Cont)
		emit(page1)
		emit(page1Cont)
		e = buf.Len()
	} else {
		emit(page1)
		emit(page1Cont)
		e = buf.Len()
		emit(page2)
		emit(page2Cont)
	}
	emit(tree)
	fmt.Fprintf(&buf, "trailer\n<< /Size 8 /Root %d 0 R >>\n%%%%EOF\n", catalog)

	out := buf.Bytes()
	out = bytes.Replace(out, []byte("/E 0000000"), fmt.Appendf(nil, "/E %07d", e), 1)
	out = bytes.Replace(out, []byte("/L 0000000"), fmt.Appendf(nil, "/L %07d", len(out)), 1)
	return out
}

// --- T5: the hint tables, with no external tool ------------------------------
//
// Measured, a build that emits NO PRIMARY HINT STREAM AT ALL passes every other
// test in this file on a machine without qpdf, and so does one whose shared
// object group lengths, per-page object counts or per-page shared identifiers
// are wrong. The oracle catches all four -- and the oracle is behind
// exec.LookPath, so on a runner without it the skip is silent. This section is
// the same check, computed from the output's own bytes.
//
// It is not a round trip through our own encoder. Nothing here calls
// internal/linearize: the hint stream is inflated out of the finished file and
// decoded against ISO 32000-1 Tables F.3 and F.5, and every value it yields is
// compared with an offset, an extent or a reference closure re-derived from the
// bytes of the emitted PDF.

// hintTables is a decoded primary hint stream.
type hintTables struct {
	minObjects        int
	firstPageOffset   int
	nObjects          []int // per page
	pageLength        []int // per page
	contentLength     []int // per page
	sharedIDs         [][]int
	firstSharedObj    int
	firstSharedOffset int
	nSharedFirstPage  int
	nSharedTotal      int
	groupLength       []int
	outline           []int // FirstObject, FirstObjectOffset, NObjects, GroupLength
}

// bitReader reads the fixed-width big-endian header fields and the MSB-first
// bit-packed columns Annex F.4 defines. align() is the counterpart of the
// encoder's per-COLUMN flush: F.4.4 puts every column on a byte boundary, so a
// reader that only aligned per table would drift.
type bitReader struct {
	b       []byte
	pos     int
	bit     uint
	overrun bool
}

func (r *bitReader) read(n int) int {
	v := 0
	for i := 0; i < n; i++ {
		if r.pos >= len(r.b) {
			r.overrun = true
			return v
		}
		v = v<<1 | int((r.b[r.pos]>>(7-r.bit))&1)
		r.bit++
		if r.bit == 8 {
			r.bit, r.pos = 0, r.pos+1
		}
	}
	return v
}

func (r *bitReader) align() {
	if r.bit != 0 {
		r.bit, r.pos = 0, r.pos+1
	}
}

func (r *bitReader) fixed(n int) int {
	r.align()
	v := 0
	for i := 0; i < n; i++ {
		if r.pos >= len(r.b) {
			r.overrun = true
			return v
		}
		v = v<<8 | int(r.b[r.pos])
		r.pos++
	}
	return v
}

func decodeHints(payload []byte, s, npages int) (hintTables, error) {
	var h hintTables
	if npages < 1 {
		return h, fmt.Errorf("a document has at least one page, /N = %d", npages)
	}
	if s <= 0 || s > len(payload) {
		return h, fmt.Errorf("/S = %d, outside a %d-byte hint stream", s, len(payload))
	}

	// Table F.3, items 1-13: 36 bytes of header, then seven byte-aligned
	// columns.
	r := &bitReader{b: payload[:s]}
	h.minObjects = r.fixed(4)
	h.firstPageOffset = r.fixed(4)
	nbObjects := r.fixed(2)
	minLength := r.fixed(4)
	nbLength := r.fixed(2)
	r.fixed(4) // 6  least content stream offset
	nbContentOff := r.fixed(2)
	minContent := r.fixed(4)
	nbContentLen := r.fixed(2)
	nbShared := r.fixed(2)
	nbIdent := r.fixed(2)
	nbNumer := r.fixed(2)
	if denom := r.fixed(2); denom == 0 {
		return h, fmt.Errorf("item 13, the shared object denominator, is 0")
	}

	h.nObjects = make([]int, npages)
	for i := range h.nObjects {
		h.nObjects[i] = h.minObjects + r.read(nbObjects)
	}
	r.align()
	h.pageLength = make([]int, npages)
	for i := range h.pageLength {
		h.pageLength[i] = minLength + r.read(nbLength)
	}
	r.align()
	counts := make([]int, npages)
	for i := range counts {
		counts[i] = r.read(nbShared)
	}
	r.align()
	h.sharedIDs = make([][]int, npages)
	for i, c := range counts {
		for j := 0; j < c; j++ {
			h.sharedIDs[i] = append(h.sharedIDs[i], r.read(nbIdent))
		}
	}
	r.align()
	for _, c := range counts {
		for j := 0; j < c; j++ {
			r.read(nbNumer)
		}
	}
	r.align()
	for range npages {
		r.read(nbContentOff)
	}
	r.align()
	h.contentLength = make([]int, npages)
	for i := range h.contentLength {
		h.contentLength[i] = minContent + r.read(nbContentLen)
	}
	r.align()
	if r.overrun {
		return h, fmt.Errorf("the page offset hint table is shorter than the %d pages it describes", npages)
	}

	// Table F.5, items 1-7: 24 bytes of header, then three byte-aligned columns.
	rs := &bitReader{b: payload[s:]}
	h.firstSharedObj = rs.fixed(4)
	h.firstSharedOffset = rs.fixed(4)
	h.nSharedFirstPage = rs.fixed(4)
	h.nSharedTotal = rs.fixed(4)
	nbGroupObjects := rs.fixed(2)
	minGroup := rs.fixed(4)
	nbGroup := rs.fixed(2)
	if h.nSharedTotal < 0 || h.nSharedTotal > len(payload)*8 {
		return h, fmt.Errorf("the shared object table claims %d entries", h.nSharedTotal)
	}
	h.groupLength = make([]int, h.nSharedTotal)
	for i := range h.groupLength {
		h.groupLength[i] = minGroup + rs.read(nbGroup)
	}
	rs.align()
	for range h.groupLength {
		rs.read(1) // signature present
	}
	rs.align()
	for range h.groupLength {
		rs.read(nbGroupObjects)
	}
	rs.align()
	if rs.overrun {
		return h, fmt.Errorf("the shared object hint table is shorter than the %d entries it declares",
			h.nSharedTotal)
	}
	return h, nil
}

var (
	hintExtentRe = regexp.MustCompile(`/H\s*\[\s*(\d+)\s+(\d+)\s*\]`)
	hintSRe      = regexp.MustCompile(`/S\s+(\d+)`)
	hintORe      = regexp.MustCompile(`/O\s+(\d+)`)
)

// hintObject is the primary hint stream as the finished file holds it.
type hintObject struct {
	num     int    // its object number
	off     int    // /H[0]
	length  int    // /H[1]
	s       int    // /S: where the shared object table starts in payload
	o       int    // /O: where the outline table starts, or 0 for none
	payload []byte // inflated
}

// hintStream locates the primary hint stream from /H and checks that an object
// really is there before inflating it.
func hintStream(b []byte, objs map[int]objSpan) (hintObject, error) {
	var h hintObject
	m := hintExtentRe.FindSubmatch(b[:min(len(b), linearizationWindow)])
	if m == nil {
		return h, fmt.Errorf("the linearization dictionary has no /H")
	}
	h.off, _ = strconv.Atoi(string(m[1]))
	h.length, _ = strconv.Atoi(string(m[2]))
	if h.length == 0 {
		return h, fmt.Errorf("/H declares a zero-length hint stream")
	}
	for n, o := range objs {
		if o.start == h.off {
			h.num = n
			break
		}
	}
	if h.num == 0 {
		return h, fmt.Errorf("/H names offset %d, where no object begins", h.off)
	}

	body := objs[h.num].body
	dict := stripStream(body)
	sm := hintSRe.FindSubmatch(dict)
	if sm == nil {
		return h, fmt.Errorf("the hint stream dictionary has no /S")
	}
	h.s, _ = strconv.Atoi(string(sm[1]))
	if om := hintORe.FindSubmatch(dict); om != nil {
		h.o, _ = strconv.Atoi(string(om[1]))
	}

	i := bytes.Index(body, []byte("stream\n"))
	j := bytes.LastIndex(body, []byte("\nendstream"))
	if i < 0 || j <= i {
		return h, fmt.Errorf("the object at /H is not a stream")
	}
	zr, err := zlib.NewReader(bytes.NewReader(body[i+len("stream\n") : j]))
	if err != nil {
		return h, fmt.Errorf("inflating the hint stream: %w", err)
	}
	if h.payload, err = io.ReadAll(zr); err != nil {
		return h, fmt.Errorf("inflating the hint stream: %w", err)
	}
	return h, nil
}

// TestPrimaryHintStreamDescribesTheFileItIsIn decodes the hint tables and holds
// every value in them against the file they are supposed to describe.
//
// The one convention it relies on is Annex F.4's: every offset STORED in a hint
// table is measured as if the hint stream were not in the file, so an offset
// past the splice point is the file offset minus the hint stream's length.
func TestPrimaryHintStreamDescribesTheFileItIsIn(t *testing.T) {
	multiPage, withPart8, withSharedIDs, withOutline := 0, 0, 0, 0
	for _, c := range linearizeCases(t) {
		t.Run(c.name, func(t *testing.T) {
			if c.skip != "" {
				t.Skip(c.skip)
			}
			out := linearized(t, c.data)
			p, err := parseLinDict(out)
			if err != nil {
				t.Fatalf("output is not linearized: %v", err)
			}
			objs := scanObjects(out)
			hs, err := hintStream(out, objs)
			if err != nil {
				t.Fatalf("primary hint stream: %v", err)
			}
			if got := objs[hs.num].extent; got != hs.length {
				t.Errorf("/H says the hint stream is %d bytes; object %d occupies %d",
					hs.length, hs.num, got)
			}
			h, err := decodeHints(hs.payload, hs.s, p["N"])
			if err != nil {
				t.Fatalf("decoding the hint stream: %v", err)
			}

			// The pre-splice offset of any object at or past the splice point is
			// its file offset minus the hint stream's length.
			preSplice := func(n int) int {
				if objs[n].start < hs.off {
					return objs[n].start
				}
				return objs[n].start - hs.length
			}

			O, E := p["O"], p["E"]
			if _, ok := objs[O]; !ok {
				t.Fatalf("/O = %d, and no such object is in the file", O)
			}
			if h.firstPageOffset != preSplice(O) {
				t.Errorf("the page offset table puts the first page's first object at %d; "+
					"object %d is at %d, i.e. %d with the hint stream absent",
					h.firstPageOffset, O, objs[O].start, preSplice(O))
			}

			// Part 6 is one ascending run of object numbers starting at /O and
			// ending at /E, which is what lets the front cross-reference section
			// be a single subsection.
			n6 := 0
			for {
				o, ok := objs[O+n6]
				if !ok || o.start >= E {
					break
				}
				n6++
			}
			if h.nObjects[0] != n6 {
				t.Errorf("the page offset table gives page 1 %d objects; %d objects lie "+
					"between /O = %d and /E = %d", h.nObjects[0], n6, O, E)
			}
			if h.pageLength[0] != E-objs[O].start {
				t.Errorf("the page offset table gives page 1 a length of %d; the first-page "+
					"section runs from %d to /E = %d, i.e. %d bytes",
					h.pageLength[0], objs[O].start, E, E-objs[O].start)
			}
			if len(h.sharedIDs[0]) != 0 {
				t.Errorf("page 1 carries %d shared identifiers; Table F.4 defines the first "+
					"page's shared objects implicitly and qpdf warns about a file that lists them",
					len(h.sharedIDs[0]))
			}
			if h.nSharedFirstPage != n6 {
				t.Errorf("the shared object table declares %d first-page entries; the "+
					"first-page section holds %d objects", h.nSharedFirstPage, n6)
			}

			// Parts 7, 8 and 9 are numbered 1..k in emission order (F.3.1), so
			// page i's group starts where page i-1's ended and part 8 starts
			// where part 7 ended. Both are re-derived here, not read back.
			_, barrier := pageSets(objs)
			pageDicts := []int{O}
			obj := 1
			for i := 1; i < p["N"]; i++ {
				first := obj
				o, ok := objs[first]
				if !ok || !pageTypeRe.Match(stripStream(o.body)) {
					t.Fatalf("the object counts in the page offset table put page %d's group "+
						"at object %d, which is not a page dictionary", i+1, first)
				}
				pageDicts = append(pageDicts, first)
				length := 0
				for k := 0; k < h.nObjects[i]; k++ {
					sp, ok := objs[first+k]
					if !ok {
						t.Fatalf("page %d's group claims object %d, which the file does not hold",
							i+1, first+k)
					}
					length += sp.extent
				}
				if h.pageLength[i] != length {
					t.Errorf("the page offset table gives page %d a length of %d; its %d "+
						"objects occupy %d bytes", i+1, h.pageLength[i], h.nObjects[i], length)
				}
				if objs[first].start < E {
					t.Errorf("page %d's dictionary is object %d at %d, before /E = %d",
						i+1, first, objs[first].start, E)
				}
				obj += h.nObjects[i]
			}

			// The shared object table is part 6 followed by part 8.
			part8 := h.nSharedTotal - h.nSharedFirstPage
			if part8 < 0 {
				t.Fatalf("the shared object table declares %d entries in total and %d for the "+
					"first page", h.nSharedTotal, h.nSharedFirstPage)
			}
			sharedObj := func(k int) int {
				if k < h.nSharedFirstPage {
					return O + k
				}
				return h.firstSharedObj + (k - h.nSharedFirstPage)
			}
			switch {
			case part8 == 0:
				if h.firstSharedObj != 0 || h.firstSharedOffset != 0 {
					t.Errorf("no object is shared between later pages only, but the table "+
						"names object %d at %d as the first one",
						h.firstSharedObj, h.firstSharedOffset)
				}
			default:
				if h.firstSharedObj != obj {
					t.Errorf("the shared object table starts part 8 at object %d; part 7 ends "+
						"at object %d and F.3.1 numbers part 8 straight after it",
						h.firstSharedObj, obj-1)
				}
				if _, ok := objs[h.firstSharedObj]; !ok {
					t.Fatalf("the shared object table names object %d, which is not in the file",
						h.firstSharedObj)
				}
				if h.firstSharedOffset != preSplice(h.firstSharedObj) {
					t.Errorf("the shared object table puts object %d at %d; it is at %d, i.e. "+
						"%d with the hint stream absent", h.firstSharedObj,
						h.firstSharedOffset, objs[h.firstSharedObj].start,
						preSplice(h.firstSharedObj))
				}
				withPart8++
			}
			for k := range h.nSharedTotal {
				n := sharedObj(k)
				sp, ok := objs[n]
				if !ok {
					t.Fatalf("shared object entry %d names object %d, which is not in the file", k, n)
				}
				if h.groupLength[k] != sp.extent {
					t.Errorf("shared object entry %d gives object %d a length of %d; it occupies %d",
						k, n, h.groupLength[k], sp.extent)
				}
			}

			// A page's shared identifiers are the indices of the shared objects
			// it actually reaches, re-derived from the emitted references.
			for i := 1; i < p["N"]; i++ {
				used := reachable(objs, pageDicts[i], barrier)
				var want []int
				for k := range h.nSharedTotal {
					if used[sharedObj(k)] {
						want = append(want, k)
					}
				}
				got := slices.Clone(h.sharedIDs[i])
				slices.Sort(got)
				if !slices.Equal(got, want) {
					t.Errorf("page %d lists shared objects %v; it reaches %v", i+1, got, want)
				}
				if len(want) > 0 {
					withSharedIDs++
				}
			}

			// Items 8 and 9: a conforming reader may treat the whole page as one
			// content unit, which is what both this writer and the reference
			// implementation do.
			for i := range h.contentLength {
				if h.contentLength[i] != h.pageLength[i] {
					t.Errorf("page %d has content length %d and page length %d; they are "+
						"written as one unit", i+1, h.contentLength[i], h.pageLength[i])
				}
			}

			if hs.o != 0 {
				if hs.o < 0 || hs.o+16 > len(hs.payload) {
					t.Fatalf("/O = %d, outside a %d-byte hint stream", hs.o, len(hs.payload))
				}
				ro := &bitReader{b: hs.payload[hs.o:]}
				h.outline = []int{ro.fixed(4), ro.fixed(4), ro.fixed(4), ro.fixed(4)}
				first, offset, count, glen := h.outline[0], h.outline[1], h.outline[2], h.outline[3]
				if _, ok := objs[first]; !ok {
					t.Fatalf("the outline hint table names object %d, which is not in the file", first)
				}
				if offset != preSplice(first) {
					t.Errorf("the outline hint table puts object %d at %d; it is at %d, i.e. %d "+
						"with the hint stream absent", first, offset, objs[first].start,
						preSplice(first))
				}
				sum := 0
				for k := 0; k < count; k++ {
					sp, ok := objs[first+k]
					if !ok {
						t.Fatalf("the outline group claims object %d, which the file does not hold",
							first+k)
					}
					sum += sp.extent
				}
				if glen != sum {
					t.Errorf("the outline hint table gives the group a length of %d; its %d "+
						"objects occupy %d bytes", glen, count, sum)
				}
				withOutline++
			}

			if p["N"] > 1 {
				multiPage++
			}
		})
	}
	// Four guards, because each of these columns is zero-width or absent on a
	// document that does not have the shape, and a sweep that lost the shape
	// would go on passing.
	if multiPage == 0 {
		t.Error("every document was one page: the per-page columns of the page offset " +
			"hint table were never more than one row deep")
	}
	if withPart8 == 0 {
		t.Error("no document put anything in part 8, so first_shared_obj and " +
			"first_shared_offset were 0 everywhere and could hold anything at all")
	}
	if withSharedIDs == 0 {
		t.Error("no page listed a shared object, so the shared identifier column was " +
			"zero-width in every document and its contents were never checked")
	}
	if withOutline == 0 {
		t.Error("no document carried an outline hint table")
	}
	t.Logf("hint tables verified: %d multi-page, %d with a non-empty part 8, %d with an outline table",
		multiPage, withPart8, withOutline)
}

// --- T6: no object is written that the file does not refer to ----------------

// TestLinearizedOutputHasNoOrphanObjects is the check that caught the defect
// this suite shipped with: internal/pdfdoc followed a stream dictionary's
// INDIRECT /Length when it built the object graph, and then streamBody replaced
// that /Length with a direct integer -- so the integer object it had named was
// written, numbered, given an xref entry and counted in the hint tables, while
// nothing in the finished file referred to it any more.
//
// qpdf refuses such a file ("found unknown object while calculating length for
// linearization data"), but only qpdf did, and only on inputs no fixture had.
// This states the property directly and needs no external tool: the only two
// objects a linearized file legitimately leaves unreferenced are the
// linearization parameter dictionary and the primary hint stream, both of which
// are found by position rather than by reference.
func TestLinearizedOutputHasNoOrphanObjects(t *testing.T) {
	for _, c := range linearizeCases(t) {
		t.Run(c.name, func(t *testing.T) {
			if c.skip != "" {
				t.Skip(c.skip)
			}
			out := linearized(t, c.data)
			if orphans := orphanObjects(t, out); len(orphans) > 0 {
				t.Errorf("objects %v are written but nothing refers to them; they take an "+
					"object number, an xref entry and a slot in the hint tables, and qpdf "+
					"refuses a file that has them", orphans)
			}
		})
	}
}

// orphanObjects returns every object in b that nothing refers to, other than the
// linearization parameter dictionary and the primary hint stream.
func orphanObjects(t *testing.T, b []byte) []int {
	t.Helper()
	objs := scanObjects(b)
	if len(objs) == 0 {
		t.Fatalf("no `N G obj` headers found in %d bytes", len(b))
	}
	referenced := map[int]bool{}
	for _, o := range objs {
		for _, n := range refsIn(stripStream(o.body)) {
			referenced[n] = true
		}
	}
	// The trailer is where the catalog and the info dictionary are named.
	for _, re := range []*regexp.Regexp{rootRe, infoRe} {
		for _, m := range re.FindAllSubmatch(b, -1) {
			if n, err := strconv.Atoi(string(m[1])); err == nil {
				referenced[n] = true
			}
		}
	}
	exempt := map[int]bool{}
	for n, o := range objs {
		if bytes.Contains(o.body, []byte("/Linearized")) {
			exempt[n] = true
		}
	}
	if hs, err := hintStream(b, objs); err == nil {
		exempt[hs.num] = true
	}
	var out []int
	for n := range objs {
		if !referenced[n] && !exempt[n] {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// TestLinearizeDoesNotOrphanAnIndirectStreamLength drives internal/pdfdoc
// directly, so that the input the linearizer sees is known to have the shape
// under test.
//
// It has to: the sweep reaches the linearizer through Optimize, which first
// rewrites the document with pdfcpu, and whether pdfcpu's writer keeps a stream
// /Length indirect is pdfcpu's business, not this test's. Asserting the shape on
// the fixture and then handing the fixture straight to Linearize removes that
// dependency. Both halves matter -- the assertion below is what stops the
// fixture quietly becoming a document with no indirect /Length at all.
func TestLinearizeDoesNotOrphanAnIndirectStreamLength(t *testing.T) {
	in := indirectLengthFixture()
	indirect := regexp.MustCompile(`/Length\s+\d+\s+0\s+R`)
	if n := len(indirect.FindAll(in, -1)); n != 2 {
		t.Fatalf("the fixture has %d indirect stream lengths; it exists to have 2", n)
	}

	var out bytes.Buffer
	if err := pdfdoc.Linearize(bytes.NewReader(in), &out); err != nil {
		t.Fatalf("pdfdoc.Linearize: %v", err)
	}
	if orphans := orphanObjects(t, out.Bytes()); len(orphans) > 0 {
		t.Errorf("objects %v are orphaned; an indirect /Length is rewritten direct, so the "+
			"integer object it named must not be carried into the output", orphans)
	}
	if indirect.Match(out.Bytes()) {
		t.Error("the output still has an indirect stream /Length; internal/pdfdoc emits the " +
			"length it measured off the bytes it wrote")
	}
	if ok, diag := qpdfSaysLinearizationIsClean(t, writeTemp(t, "indirect-length", out.Bytes())); !ok {
		t.Errorf("qpdf --check-linearization did not print %q\n%s", qpdfClean, diag)
	}
}

// --- T7: the document must survive ------------------------------------------

// TestLinearizeRoundTrip is what catches the failure mode a structural check
// cannot: a perfectly laid-out file that lost content on the way through.
// Serializing every object by hand means every dictionary entry, every stream
// /Length and every inherited page attribute is re-emitted, and pdfcpu's
// Dict.PDFString silently DROPS any value whose concrete Go type is outside its
// switch (types/dict.go:517 -- the default arm is guarded by a logger that is
// off by default). A dropped /MediaBox or /Contents produces a file that
// linearizes cleanly and renders nothing.
func TestLinearizeRoundTrip(t *testing.T) {
	for _, c := range linearizeCases(t) {
		t.Run(c.name, func(t *testing.T) {
			if c.skip != "" {
				t.Skip(c.skip)
			}
			out := linearized(t, c.data)

			// pdfcpu's validator, through the existing pdfdoc seam. This is
			// what caught a dropped /Root in the design probe.
			if _, err := pdfdoc.ReadProperties(bytes.NewReader(out)); err != nil {
				t.Fatalf("the linearized output does not validate: %v", err)
			}

			before := pageCount(t, c.data)
			after := pageCount(t, out)
			if before != after {
				t.Errorf("page count changed: %d in, %d out", before, after)
			}
			if p, err := parseLinDict(out); err == nil && p["N"] != after {
				t.Errorf("/N = %d but the document has %d pages", p["N"], after)
			}

			if bin, err := exec.LookPath("pdftotext"); err == nil {
				in, outText := pdftotextHash(t, bin, c.name+"-in", c.data),
					pdftotextHash(t, bin, c.name+"-out", out)
				if in != outText {
					t.Errorf("pdftotext output changed: %s in, %s out", in, outText)
				}
			}
			if bin, err := exec.LookPath("pdfimages"); err == nil {
				in, outN := pdfimagesRows(t, bin, c.name+"-in", c.data),
					pdfimagesRows(t, bin, c.name+"-out", out)
				if in != outN {
					t.Errorf("pdfimages listed %d images before and %d after", in, outN)
				}
			}
		})
	}
}

func pageCount(t *testing.T, b []byte) int {
	t.Helper()
	d, err := pdfdoc.Open(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("pdfdoc.Open: %v", err)
	}
	return d.PageCount()
}

func pdftotextHash(t *testing.T, bin, name string, b []byte) string {
	t.Helper()
	out, err := exec.Command(bin, writeTemp(t, name, b), "-").Output()
	if err != nil {
		t.Fatalf("pdftotext %s: %v", name, err)
	}
	sum := sha256.Sum256(out)
	return hex.EncodeToString(sum[:8])
}

// pdfimagesRows counts listed images. pdfimages -list prints two header lines
// before the rows.
func pdfimagesRows(t *testing.T, bin, name string, b []byte) int {
	t.Helper()
	out, err := exec.Command(bin, "-list", writeTemp(t, name, b)).Output()
	if err != nil {
		// A document poppler declines to list is not this test's subject; the
		// comparison only means anything when both sides ran.
		return -1
	}
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(l)
		if len(f) > 0 {
			if _, err := strconv.Atoi(f[0]); err == nil {
				n++
			}
		}
	}
	return n
}

// --- T8: Optimize's contract ------------------------------------------------

// TestOptimizeLinearizeSuspendsTheNeverLargerRule pins the trap that the
// existing size policy sets for this feature. Optimize returns
// min(input, rewritten) today (optimize.go). Linearization makes every document
// in this corpus BIGGER -- measured on byblos's own output, +7 to +1007 bytes
// against the input over the 27 readable documents (e.g. mixed 1965 -> 2949,
// born-digital 691 -> 1698, scan 1512 -> 2511; the +7 is dup-raster, the one
// document pdfcpu's rewrite shrinks enough to nearly pay for the linearization).
// Against the un-linearized candidate the cost is +649 to +1007 with no
// exceptions. Under the unmodified rule a
// fully working linearizer would hand back the un-linearized input every single
// time, and every other test in this file would then fail loudly -- but a
// careless fix (say, keeping the rule and quietly dropping the linearize
// request) would make them fail quietly instead. This states the requirement
// directly.
//
// The rule must stay in force for Linearize:false. TestOptimizeNeverLargerThanInput
// (optimize_test.go) is unchanged and covers that; if this suspension is
// implemented too broadly, that test fails.
func TestOptimizeLinearizeSuspendsTheNeverLargerRule(t *testing.T) {
	grew := 0
	for _, d := range corpus.All() {
		if d.Name == "malformed" {
			continue
		}
		t.Run(d.Name, func(t *testing.T) {
			out := linearized(t, d.Data)
			if bytes.Equal(out, d.Data) {
				t.Fatal("Optimize returned the input verbatim: the pass-through branch " +
					"ran and the caller did not get the linearization it asked for")
			}
			p, err := parseLinDict(out)
			if err != nil {
				t.Fatalf("output carries no linearization dictionary: %v", err)
			}
			if p["L"] != len(out) {
				t.Fatalf("/L = %d for a %d-byte output", p["L"], len(out))
			}
			if len(out) > len(d.Data) {
				grew++
			}
		})
	}
	if grew == 0 {
		t.Error("no document grew, so nothing here proves the never-larger-than-input " +
			"rule was actually suspended; re-check against the measured +7..+1007 bytes")
	}
	t.Logf("%d documents were returned linearized despite being larger than their input", grew)
}

// TestOptimizeLinearizeMalformedInput: a document pdfcpu cannot read must
// produce an error, not a pass-through of the damaged bytes. Measured, the
// corpus's truncated document fails at dereferenceObject; the linearize path
// must surface that rather than emitting something.
func TestOptimizeLinearizeMalformedInput(t *testing.T) {
	var out bytes.Buffer
	err := Optimize(&out, bytes.NewReader(corpusDoc(t, "malformed")), OptimizeOptions{Linearize: true})
	if err == nil {
		t.Fatal("Optimize(Linearize:true) on the malformed corpus document: want an error, got nil")
	}
	if out.Len() != 0 {
		t.Errorf("wrote %d bytes despite erroring", out.Len())
	}
}

// TestOptimizeRecordsLinearization: the provenance record has to say that this
// document was linearized, and it has to survive the process.
//
// Order is load-bearing and this test is what pins it. WriteProvenance goes
// through pdfcpu's writer (provenance.go -> pdfdoc.WriteProperties ->
// api.AddProperties), which STRIPS linearization -- measured, bookletTest.pdf
// 50308 B linearized in, 34531 B not-linearized out. So linearization must be
// the last thing that touches the bytes. An implementation that linearizes and
// then writes provenance produces a file with a correct-looking record and no
// linearization; one that never writes the record produces a linearized file
// that claims nothing. This fails on both.
func TestOptimizeRecordsLinearization(t *testing.T) {
	for _, name := range []string{"born-digital", "mixed", "dup-raster"} {
		t.Run(name, func(t *testing.T) {
			out := linearized(t, corpusDoc(t, name))
			if !isLinearized(out) {
				t.Fatal("the output is not linearized; provenance is beside the point")
			}
			p, err := ReadProvenance(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("ReadProvenance: %v", err)
			}
			if p == nil {
				t.Fatal("the linearized output carries no provenance record")
			}
			if p.Optimized != "rewritten-linearized" {
				t.Errorf("Optimized = %q; want %q -- a caller cannot otherwise tell a "+
					"linearized result from one that merely got rewritten",
					p.Optimized, "rewritten-linearized")
			}
		})
	}
}

// TestLinearizeIsARegisteredCapability. Provenance and UpgradeCandidates share
// one capability vocabulary; a capability that exists in code and not in that
// vocabulary cannot be recorded, and a document processed by an older build
// can never be nominated for it. capabilityRules["linearize"] is "never" today,
// which was correct while the capability did not exist and is wrong the moment
// it does: a document whose record does not show linearization WOULD come out
// different if it were reprocessed.
func TestLinearizeIsARegisteredCapability(t *testing.T) {
	if !slices.Contains(Capabilities(), "linearize") {
		t.Errorf("Capabilities() = %v; it must list %q now that this build can do it",
			Capabilities(), "linearize")
	}
	rule, ok := capabilityRules["linearize"]
	if !ok {
		t.Fatal(`capabilityRules has no "linearize" entry`)
	}
	if !rule(&Provenance{Optimized: "rewritten"}) {
		t.Error("a document that was rewritten WITHOUT linearization is not nominated " +
			"for the linearize capability; reprocessing it would change its output, so it must be")
	}
	if rule(&Provenance{Optimized: "rewritten-linearized"}) {
		t.Error("a document that is already linearized is nominated for re-linearization; " +
			"that is a wasted pass over the archive")
	}
}

// --- fixtures ----------------------------------------------------------------

// sharedObjectsFixture builds a three-page document whose pages share one font
// object and one image XObject, plus per-page private content streams of
// different lengths.
//
// Every part of that shape is load-bearing:
//
//   - THREE pages, so part 7 has more than one group and the per-page columns
//     of the page-offset hint table have more than one row to disagree about.
//   - A SHARED font and a SHARED image, so objects land in the shared-object
//     hint table. Verified against the reference implementation: qpdf's own
//     linearization of these exact bytes reports nshared_total 4 and two shared
//     identifiers on each of pages 1 and 2, where every corpus document reports
//     zero. Without this fixture an all-zero shared-object table passes.
//   - DIFFERENT content lengths per page, so nbits_delta_page_length is not 0
//     and the delta columns are not a run of zeroes that any bit order encodes
//     identically.
//
// With outlines, the catalog also gets an /Outlines tree and /PageMode
// /UseOutlines, which is what puts the outline objects in part 6 and requires
// the /O outline hint table. Verified non-vacuous: patching /O out of a
// qpdf-linearized copy of this fixture (a same-length /O -> /Q substitution, so
// no offset moves) turns "no linearization errors" into
// "WARNING: incorrect object count in outline hint table", empty stdout, exit 3.
//
// The outline item's /Dest names page 2, as real outlines do. That is the case
// which showed the naive first-page check to be wrong -- see reachable().
func sharedObjectsFixture(outlines bool) []byte {
	const imgW, imgH = 64, 64
	w := newFixtureWriter()
	catalog := w.reserve()
	tree := w.reserve()
	font := w.reserve()
	img := w.reserve()
	pages := []int{w.reserve(), w.reserve(), w.reserve()}
	contents := []int{w.reserve(), w.reserve(), w.reserve()}
	var outlineRoot, outlineItem int
	if outlines {
		outlineRoot = w.reserve()
		outlineItem = w.reserve()
	}

	w.fill(font, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	w.fillStream(img, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceGray /BitsPerComponent 8", imgW, imgH), fixtureGray(imgW*imgH))

	var kids strings.Builder
	for i := range pages {
		fmt.Fprintf(&kids, "%d 0 R ", pages[i])
		var body strings.Builder
		body.WriteString("q 200 0 0 200 60 500 cm /Im0 Do Q\n")
		for j := 0; j <= i*4; j++ {
			fmt.Fprintf(&body, "BT /F1 12 Tf 1 0 0 1 72 %d Tm (page %d line %d) Tj ET\n",
				400-14*j, i+1, j)
		}
		w.fillStream(contents[i], "", []byte(body.String()))
		w.fill(pages[i], fmt.Sprintf(
			"<< /Type /Page /Parent %d 0 R /MediaBox [ 0 0 612 792 ]"+
				" /Resources << /Font << /F1 %d 0 R >> /XObject << /Im0 %d 0 R >> >>"+
				" /Contents %d 0 R >>", tree, font, img, contents[i]))
	}
	w.fill(tree, fmt.Sprintf("<< /Type /Pages /Count %d /Kids [ %s] >>", len(pages), kids.String()))
	if outlines {
		w.fill(outlineItem, fmt.Sprintf(
			"<< /Title (Second page) /Parent %d 0 R /Dest [ %d 0 R /Fit ] >>",
			outlineRoot, pages[1]))
		w.fill(outlineRoot, fmt.Sprintf(
			"<< /Type /Outlines /Count 1 /First %d 0 R /Last %d 0 R >>", outlineItem, outlineItem))
		w.fill(catalog, fmt.Sprintf(
			"<< /Type /Catalog /Pages %d 0 R /Outlines %d 0 R /PageMode /UseOutlines >>",
			tree, outlineRoot))
	} else {
		w.fill(catalog, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", tree))
	}
	return w.finish(catalog)
}

// TestSharedObjectsFixtureIsUsable checks the fixtures are real PDFs before
// anything asserts against what a linearizer did to them. It needs no new API,
// so it stays green through the RED stage: if it ever fails, the failures
// elsewhere in this file say nothing about the linearizer.
func TestSharedObjectsFixtureIsUsable(t *testing.T) {
	for _, outlines := range []bool{false, true} {
		name := "shared-objects"
		if outlines {
			name += "-outlines"
		}
		t.Run(name, func(t *testing.T) {
			b := sharedObjectsFixture(outlines)
			if got := pageCount(t, b); got != 3 {
				t.Fatalf("PageCount() = %d; want 3", got)
			}
			if _, err := pdfdoc.ReadProperties(bytes.NewReader(b)); err != nil {
				t.Fatalf("the fixture does not validate: %v", err)
			}
			if isLinearized(b) {
				t.Fatal("the fixture is already linearized; it cannot test linearizing")
			}
			// The whole point of the fixture: one font object and one image
			// object, named by all three pages. Counting the `obj` headers is
			// an independent statement of that, so a future edit that gives
			// each page its own font silently disarms the shared-object hint
			// table without disarming this assertion.
			if n := bytes.Count(b, []byte("/BaseFont /Helvetica")); n != 1 {
				t.Errorf("the fixture holds %d font objects; the shared-object hint "+
					"table needs exactly 1 shared between the pages", n)
			}
			if n := bytes.Count(b, []byte("/Subtype /Image")); n != 1 {
				t.Errorf("the fixture holds %d image objects; want exactly 1, shared", n)
			}
			if n := bytes.Count(b, []byte("/Type /Page /Parent")); n != 3 {
				t.Errorf("the fixture holds %d page dictionaries; want 3", n)
			}
		})
	}
}

// laterPageSharedFixture builds a three-page document in which pages 2 and 3
// share a font and an image that page 1 does not use, and page 1 has a font of
// its own.
//
// That is the only shape that puts anything in Annex F PART 8 -- "objects shared
// between later pages but not used by the first page" -- and part 8 is what
// Table F.5 items 1 and 2, first_shared_obj and first_shared_offset, describe.
// Measured with `qpdf --show-linearization` over byblos's own output for all 29
// other documents in this sweep: first_shared_obj 0, first_shared_offset 0, and
// nshared_total equal to nshared_first_page in every single one. Both fields
// could therefore be filled with any value at all and nothing would notice.
func laterPageSharedFixture() []byte {
	const imgW, imgH = 48, 48
	w := newFixtureWriter()
	catalog := w.reserve()
	tree := w.reserve()
	fontA := w.reserve() // page 1 only
	fontB := w.reserve() // pages 2 and 3, and NOT page 1
	img := w.reserve()   // pages 2 and 3, and NOT page 1
	pages := []int{w.reserve(), w.reserve(), w.reserve()}
	contents := []int{w.reserve(), w.reserve(), w.reserve()}

	w.fill(fontA, "<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>")
	w.fill(fontB, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	w.fillStream(img, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceGray /BitsPerComponent 8", imgW, imgH), fixtureGray(imgW*imgH))

	var kids strings.Builder
	for i := range pages {
		fmt.Fprintf(&kids, "%d 0 R ", pages[i])
		var body strings.Builder
		res := fmt.Sprintf("<< /Font << /F1 %d 0 R >> >>", fontA)
		if i > 0 {
			body.WriteString("q 200 0 0 200 60 500 cm /Im0 Do Q\n")
			res = fmt.Sprintf("<< /Font << /F1 %d 0 R >> /XObject << /Im0 %d 0 R >> >>", fontB, img)
		}
		for j := 0; j <= i*3; j++ {
			fmt.Fprintf(&body, "BT /F1 12 Tf 1 0 0 1 72 %d Tm (page %d line %d) Tj ET\n",
				400-14*j, i+1, j)
		}
		w.fillStream(contents[i], "", []byte(body.String()))
		w.fill(pages[i], fmt.Sprintf(
			"<< /Type /Page /Parent %d 0 R /MediaBox [ 0 0 612 792 ] /Resources %s"+
				" /Contents %d 0 R >>", tree, res, contents[i]))
	}
	w.fill(tree, fmt.Sprintf("<< /Type /Pages /Count %d /Kids [ %s] >>", len(pages), kids.String()))
	w.fill(catalog, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", tree))
	return w.finish(catalog)
}

// documentLevelFixture builds a two-page document whose catalog carries both
// kinds of document-level key Annex F distinguishes.
//
// /ViewerPreferences and /OpenAction are consulted before anything can be
// displayed, so F.3.5 puts what they reach in part 4, before /E. /Metadata,
// /Names and /PageLabels are not, so F.3.10 files them in part 9, after it --
// and so is an outline tree when /PageMode is NOT /UseOutlines, which is the
// case F.3.8 distinguishes and which this fixture takes (there is no /PageMode
// here at all).
//
// No corpus document has any catalog key beyond /Pages, so without this the
// difference between the two groups is never exercised in either direction.
func documentLevelFixture() []byte {
	w := newFixtureWriter()
	catalog := w.reserve()
	tree := w.reserve()
	font := w.reserve()
	viewerPrefs := w.reserve()
	metadata := w.reserve()
	dests := w.reserve()
	pageLabels := w.reserve()
	outlineRoot := w.reserve()
	outlineItem := w.reserve()
	pages := []int{w.reserve(), w.reserve()}
	contents := []int{w.reserve(), w.reserve()}

	w.fill(font, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	w.fill(viewerPrefs, "<< /HideToolbar true /FitWindow true >>")
	w.fillStream(metadata, "/Type /Metadata /Subtype /XML", []byte(
		`<?xpacket begin="" id="W5M0MpCehiHzreSzNTczkc9d"?>`+
			`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF `+
			`xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`+
			`</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`))
	w.fill(pageLabels, "<< /Nums [ 0 << /S /D >> ] >>")

	var kids strings.Builder
	for i := range pages {
		fmt.Fprintf(&kids, "%d 0 R ", pages[i])
		w.fillStream(contents[i], "", fmt.Appendf(nil,
			"BT /F1 12 Tf 1 0 0 1 72 700 Tm (page %d) Tj ET\n", i+1))
		w.fill(pages[i], fmt.Sprintf(
			"<< /Type /Page /Parent %d 0 R /MediaBox [ 0 0 612 792 ]"+
				" /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			tree, font, contents[i]))
	}
	w.fill(dests, fmt.Sprintf("<< /Names [ (second) [ %d 0 R /Fit ] ] >>", pages[1]))
	w.fill(outlineItem, fmt.Sprintf(
		"<< /Title (Second page) /Parent %d 0 R /Dest [ %d 0 R /Fit ] >>", outlineRoot, pages[1]))
	w.fill(outlineRoot, fmt.Sprintf(
		"<< /Type /Outlines /Count 1 /First %d 0 R /Last %d 0 R >>", outlineItem, outlineItem))
	w.fill(tree, fmt.Sprintf("<< /Type /Pages /Count %d /Kids [ %s] >>", len(pages), kids.String()))
	w.fill(catalog, fmt.Sprintf(
		"<< /Type /Catalog /Pages %d 0 R /ViewerPreferences %d 0 R"+
			" /OpenAction [ %d 0 R /Fit ] /Metadata %d 0 R /Names << /Dests %d 0 R >>"+
			" /PageLabels %d 0 R /Outlines %d 0 R >>",
		tree, viewerPrefs, pages[0], metadata, dests, pageLabels, outlineRoot))
	return w.finish(catalog)
}

// indirectLengthFixture builds a two-page document whose content streams state
// their /Length indirectly, which ISO 32000-1 7.3.8.2 explicitly allows and
// which real producers do when they cannot know the length until the stream is
// finished.
//
// It is the shape that broke the linearizer: the graph builder followed the
// /Length reference, so the integer object joined the write set, and the
// serializer then replaced /Length with a direct integer, leaving that object
// referred to by nothing. Nothing in the corpus has it, and no pdfcpu rewrite of
// a corpus document produces it.
func indirectLengthFixture() []byte {
	w := newFixtureWriter()
	catalog := w.reserve()
	tree := w.reserve()
	font := w.reserve()
	pages := []int{w.reserve(), w.reserve()}
	contents := []int{w.reserve(), w.reserve()}
	lengths := []int{w.reserve(), w.reserve()}

	w.fill(font, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	var kids strings.Builder
	for i := range pages {
		fmt.Fprintf(&kids, "%d 0 R ", pages[i])
		var body strings.Builder
		for j := 0; j <= i*5; j++ {
			fmt.Fprintf(&body, "BT /F1 12 Tf 1 0 0 1 72 %d Tm (page %d line %d) Tj ET\n",
				700-14*j, i+1, j)
		}
		w.fillStreamIndirectLength(contents[i], "", []byte(body.String()), lengths[i])
		w.fill(pages[i], fmt.Sprintf(
			"<< /Type /Page /Parent %d 0 R /MediaBox [ 0 0 612 792 ]"+
				" /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			tree, font, contents[i]))
	}
	w.fill(tree, fmt.Sprintf("<< /Type /Pages /Count %d /Kids [ %s] >>", len(pages), kids.String()))
	w.fill(catalog, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", tree))
	return w.finish(catalog)
}

// TestDocumentLevelMaterialIsPartitionedByKey is the two-sided statement of
// F.3.5 against F.3.10: it is not enough that a linearized file put SOMETHING
// before /E.
//
// The first half is what a first-page check cannot do on its own -- material a
// reader does not need before it can display page 1 must be OUT of the
// first-page section, or linearization has bought nothing. The second half is
// the guard against overcorrecting: an implementation that pushed everything
// document-level into part 9 would strand /ViewerPreferences and /OpenAction
// past /E, where a reader has to wait for them.
func TestDocumentLevelMaterialIsPartitionedByKey(t *testing.T) {
	out := linearized(t, documentLevelFixture())
	p, err := parseLinDict(out)
	if err != nil {
		t.Fatalf("output is not linearized: %v", err)
	}
	objs := scanObjects(out)
	rm := rootRe.FindSubmatch(out)
	if rm == nil {
		t.Fatal("no /Root in the trailer")
	}
	root, _ := strconv.Atoi(string(rm[1]))
	cat := stripStream(objs[root].body)
	E := p["E"]

	// Reading the object numbers back out of the emitted catalog is what makes
	// this independent of the renumbering.
	after := []string{"Metadata", "Names", "PageLabels", "Outlines"}
	before := []string{"ViewerPreferences", "OpenAction"}
	for _, key := range after {
		v, ok := dictEntry(cat, key)
		if !ok {
			t.Fatalf("the emitted catalog has no /%s; the fixture exists to carry one", key)
		}
		refs := refsIn(v)
		if len(refs) == 0 {
			t.Fatalf("/%s = %q names no object", key, v)
		}
		for _, n := range refs {
			sp, ok := objs[n]
			if !ok {
				t.Fatalf("/%s names object %d, which is not in the file", key, n)
			}
			if sp.start < E {
				t.Errorf("/%s reaches object %d at %d, inside the first-page section "+
					"(/E = %d); F.3.10 files it in part 9 and a reader does not need it "+
					"to show page 1", key, n, sp.start, E)
			}
		}
	}
	for _, key := range before {
		v, ok := dictEntry(cat, key)
		if !ok {
			t.Fatalf("the emitted catalog has no /%s; the fixture exists to carry one", key)
		}
		for _, n := range refsIn(v) {
			sp, ok := objs[n]
			if !ok {
				t.Fatalf("/%s names object %d, which is not in the file", key, n)
			}
			if pageTypeRe.Match(stripStream(sp.body)) {
				continue // /OpenAction names page 1, which /O already covers.
			}
			if sp.end > E {
				t.Errorf("/%s reaches object %d, which ends at %d, past /E = %d; F.3.5 "+
					"puts it in part 4 because a reader consults it before it can display "+
					"anything", key, n, sp.end, E)
			}
		}
	}
}

// fixtureWriter is the same minimal, hand-rolled PDF writer internal/corpus
// uses (corpus.go, "the minimal PDF writer"), reproduced here because that
// one is unexported and these fixtures deliberately do not live in the corpus.
// If they are ever promoted, this goes away with them.
type fixtureWriter struct {
	buf     bytes.Buffer
	offsets []int
}

func newFixtureWriter() *fixtureWriter {
	w := &fixtureWriter{}
	w.buf.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	return w
}

func (w *fixtureWriter) reserve() int {
	w.offsets = append(w.offsets, -1)
	return len(w.offsets)
}

func (w *fixtureWriter) fill(n int, body string) {
	w.offsets[n-1] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n%s\nendobj\n", n, body)
}

func (w *fixtureWriter) fillStream(n int, dict string, payload []byte) {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(payload); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	w.offsets[n-1] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n<< %s /Filter /FlateDecode /Length %d >>\nstream\n", n, dict, z.Len())
	w.buf.Write(z.Bytes())
	w.buf.WriteString("\nendstream\nendobj\n")
}

// fillStreamIndirectLength is fillStream with the /Length stated as a reference
// to lenObj, which it also fills.
func (w *fixtureWriter) fillStreamIndirectLength(n int, dict string, payload []byte, lenObj int) {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(payload); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	w.offsets[n-1] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n<< %s /Filter /FlateDecode /Length %d 0 R >>\nstream\n",
		n, dict, lenObj)
	w.buf.Write(z.Bytes())
	w.buf.WriteString("\nendstream\nendobj\n")
	w.fill(lenObj, strconv.Itoa(z.Len()))
}

// finish writes the cross-reference table and trailer, 20 bytes per entry as
// ISO 32000-1 section 7.5.4 requires.
func (w *fixtureWriter) finish(root int) []byte {
	start := w.buf.Len()
	fmt.Fprintf(&w.buf, "xref\n0 %d\n0000000000 65535 f \n", len(w.offsets)+1)
	for i, off := range w.offsets {
		if off < 0 {
			panic(fmt.Sprintf("fixture: object %d was reserved but never filled", i+1))
		}
		fmt.Fprintf(&w.buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&w.buf, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(w.offsets)+1, root, start)
	return w.buf.Bytes()
}

// fixtureGray is deterministic 8-bit grey that does not compress to nothing, so
// the shared image object has a length worth putting in a hint table.
func fixtureGray(n int) []byte {
	px := make([]byte, n)
	for i := range px {
		px[i] = byte((i*7 + 93) % 251)
	}
	return px
}

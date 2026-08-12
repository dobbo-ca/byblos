package byblos

// The pinned sample's population, pinned twice: once against Inspect, so the
// DEFINITION cannot drift, and once against the tree, so the NUMBERS cannot.
//
// byb-wj2: two verified lanes measured the same pinned sample as 169,376 pages
// and as 169,034 pages. Neither had miscounted. One counted through pdfdoc.Open
// and PageCount, the other through byblos.Inspect, and at the time Inspect
// returned an error for the whole document on the first page it could not walk
// -- so 17 openable documents holding 342 pages left one lane's denominator and
// not the other's. Every JBIG2 percentage published from either lane inherited
// the disagreement.
//
// byb-3jq has since made those two derivations agree, which is exactly why this
// file exists: they agree by behaviour, not by construction, and nothing else
// would notice them parting again.

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/dobbo-ca/byblos/internal/sample"
)

// writeSampleFixtures lays the generated corpus out on disk together with the
// documents that exist to fail in a particular way, which All() deliberately
// does not carry. All three of the package sample's readability predicates have
// a document here, and the last two are what give the comparison below its kill
// power: a corpus of clean documents would compare two derivations that cannot
// disagree.
func writeSampleFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".pdf"), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for _, d := range corpus.All() {
		write(d.Name, d.Data)
	}
	write("mixed-page-two-unreadable", corpus.MixedPageTwoUnreadable())
	write("page-two-stops-mid-stream", corpus.PageTwoStopsMidStream())
	write("corrupt-content-stream", corpus.CorruptContentStream())
	write("corrupt-content-stream-in-array", corpus.CorruptContentStreamInArray())
	return dir
}

// TestPopulationAgreesWithInspect is the test byb-wj2 asks for, and the one that
// would have caught the 342 pages the day they appeared.
//
// It derives the same population twice and by different routes. sample.Walk goes
// through pdfdoc.Open and PageCount and never looks at a page's content; Inspect
// walks every page's content stream and is the entry point the losing lane used.
// The two must report the same number of pages and the same number of documents
// that could not be read at all.
//
// THE VACUITY GUARD IS THE POINT OF THE FIXTURE LIST. Over documents byblos can
// read cleanly the two derivations cannot disagree, so a corpus with no damaged
// page would pass this test no matter what either side did. It therefore fails
// unless the walk actually met a document that opens and holds a page byblos
// cannot finish -- the exact shape of the 17 govdocs1 documents.
func TestPopulationAgreesWithInspect(t *testing.T) {
	dir := writeSampleFixtures(t)

	p, err := sample.Walk(dir, 4, nil)
	if err != nil {
		t.Fatalf("sample.Walk: %v", err)
	}

	paths, err := sample.Paths(dir)
	if err != nil {
		t.Fatalf("sample.Paths: %v", err)
	}
	var pages, refused, partialPages, partialDocs int
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		infos, err := Inspect(bytes.NewReader(data))
		if err != nil {
			refused++
			continue
		}
		pages += len(infos)
		before := partialPages
		for _, pi := range infos {
			for _, d := range pi.Diagnostics {
				if d.Severity == SeverityError {
					partialPages++
					break
				}
			}
		}
		if partialPages > before {
			partialDocs++
		}
	}

	if pages != p.Pages {
		t.Errorf("Inspect reads %d pages across this corpus; sample.Walk counts %d. "+
			"These are the two derivations byb-wj2 found 342 pages apart. If Inspect has "+
			"gone back to refusing a whole document for one page it cannot walk, the "+
			"population is not the thing that changed.", pages, p.Pages)
	}
	if refused != p.Unopenable {
		t.Errorf("Inspect refuses %d documents outright; sample.Walk calls %d unopenable. "+
			"An openable document that Inspect will not read is the 342-page bug, not a "+
			"new unopenable document.", refused, p.Unopenable)
	}
	if partialPages == 0 || partialDocs == 0 {
		t.Fatalf("no document in this corpus opened and then failed on a page "+
			"(%d partial pages in %d documents), so the two derivations were compared over "+
			"documents that cannot make them disagree and this test proved nothing",
			partialPages, partialDocs)
	}
	t.Logf("%d files, %d documents, %d pages, %d partially read pages in %d documents",
		p.Files, p.Documents, p.Pages, partialPages, partialDocs)
}

// --- the pinned sample's figures ---------------------------------------------

// sampleClaims is the set of phrasings a figure about the pinned sample has to
// be written in, and what each one must equal. It is the convention
// corpusCountClaims established for internal/corpus, applied to the other
// population the tree quotes.
//
// THE WORDING CARRIES THE PREDICATE. "sample files" counts the one document that
// will not open and "sample documents" does not, so the two differ by one and a
// claim cannot be pinned to the wrong number by being written loosely. That is
// worth the reword it costs, because writing "5,672 documents" when the walk
// dropped one of them is how a denominator goes wrong quietly.
//
// Numbers are matched with or without thousands separators because the tree
// already contains both, and consistency of formatting is not what this pins.
//
// The leading \b is load-bearing and was put there by a false positive rather
// than by foresight. internal/pdfdoc/pdfdoc.go writes "4,840 govdocs1 sample
// files"; the corpus name ends in a digit, so without the \b the pattern took
// that trailing digit as the whole figure and reported the file as claiming
// there is one such file.
//
// Note what this comment must not do, for the same reason: a doc comment here
// that spells a number next to one of these phrases is itself a claim, and the
// scanner is right to read it as one.
//
// "N-page sample" IS NOT ONE OF THESE, deliberately, and the reason is worth
// keeping. upgrade.go and tools/sample/README.md both quote a 166,423-page run
// measured 2026-07-30 over three of the four sets. That figure is not stale and
// it is not this population; a pattern that claimed it would fail until someone
// "fixed" it to 169,376, which would falsify a dated measurement. The honest
// cost of leaving the pattern out is that a figure written that way is invisible
// here -- the same trade corpusCountClaims documents.
var sampleClaims = []struct {
	re   *regexp.Regexp
	want int
	what string
}{
	{regexp.MustCompile(`\b(\d[\d,]*) sample files`), sample.Files,
		"*.pdf paths under the pinned sample (sample.Files)"},
	{regexp.MustCompile(`\b(\d[\d,]*) sample documents`), sample.Documents,
		"of those that pdfdoc.Open accepts (sample.Documents)"},
	{regexp.MustCompile(`\b(\d[\d,]*) sample pages`), sample.Pages,
		"pages in the pinned sample (sample.Pages)"},
}

// TestSampleClaimsMatchThePinnedPopulation pins every figure in the tree about
// the size of the pinned sample to internal/sample's constants.
//
// Nothing here pins prose. It pins numbers that are already written down, to the
// population they are already about -- the same job
// TestCorpusCountClaimsMatchTheCorpus does for internal/corpus, and for the same
// reason: that figure read 27 in five places through three documentation
// reconciliations because nothing connected the prose to the code. This figure
// is worse to get wrong. The corpus count describes test fixtures; the sample
// count is the denominator under every rate byblos has published.
func TestSampleClaimsMatchThePinnedPopulation(t *testing.T) {
	// docs/superpowers/plans holds dated implementation records that quote the
	// population as it stood on the day they were written. Those are history and
	// must not be rewritten -- the same exclusion corpusCountClaims makes.
	skipDir := map[string]bool{".git": true, "plans": true}

	claims := 0
	files := map[string]bool{}
	perPattern := make([]int, len(sampleClaims))
	walkErr := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] || path == "testdata/corpus" {
				return fs.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".go" && ext != ".md" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for ci, c := range sampleClaims {
				for _, m := range c.re.FindAllStringSubmatch(line, -1) {
					claims++
					perPattern[ci]++
					files[path] = true
					got, err := strconv.Atoi(strings.ReplaceAll(strings.TrimRight(m[1], ","), ",", ""))
					if err != nil {
						t.Errorf("%s:%d: %q: %v", path, i+1, m[0], err)
						continue
					}
					if got != c.want {
						t.Errorf("%s:%d says %q; there are %d %s. Update the figure here, "+
							"not the constant in internal/sample.",
							path, i+1, m[0], c.want, c.what)
					}
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walking the tree: %v", walkErr)
	}

	// Without these, a scanner whose patterns were quietly reworded out of
	// existence reports a clean pass over zero claims, which is the shape of the
	// drift it is here to catch.
	goFiles := 0
	for f := range files {
		if strings.HasSuffix(f, ".go") {
			goFiles++
		}
	}
	if goFiles < 3 {
		t.Errorf("sample figures were found in only %d Go files; extract.go, jbig2.go and "+
			"extract_test.go all quote one, and a fix that reaches only the docs is the "+
			"half-fix byb-a20 had to come back for", goFiles)
	}
	var missing []string
	for i, c := range sampleClaims {
		if perPattern[i] == 0 && c.want != sample.Documents {
			missing = append(missing, c.re.String())
		}
	}
	if len(missing) > 0 {
		t.Errorf("no claim in the tree matches %v, so those patterns pin nothing. Either "+
			"the prose was reworded or the pattern was; both are the drift this catches.",
			missing)
	}
	t.Logf("%d sample figures pinned across %d files", claims, len(files))
}

// TestPinnedSampleFiguresAreSelfConsistent is the arithmetic of the constants
// themselves, which no walk of the real sample is needed to check.
func TestPinnedSampleFiguresAreSelfConsistent(t *testing.T) {
	if sample.Documents+1 != sample.Files {
		t.Errorf("sample.Files %d - sample.Documents %d is %d; the pinned sample holds "+
			"exactly one document that will not open (govdocs1/pdfs/700620.pdf). If a "+
			"second one appeared, say which, here.",
			sample.Files, sample.Documents, sample.Files-sample.Documents)
	}
	if sample.PartiallyReadPages >= sample.Pages {
		t.Errorf("sample.PartiallyReadPages %d is not a subset of sample.Pages %d",
			sample.PartiallyReadPages, sample.Pages)
	}
	if sample.Pages < sample.Documents {
		t.Errorf("sample.Pages %d is below sample.Documents %d; every document that opens "+
			"has at least one page", sample.Pages, sample.Documents)
	}
}

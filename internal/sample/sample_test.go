package sample

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// writeFixtures lays the generated corpus out on disk, plus the documents that
// exist to fail in a particular way and are therefore kept out of All(). The
// three predicates in the package comment all have a document here:
// "malformed" will not open, and mixed-page-two-unreadable and
// page-two-stops-mid-stream each open and hold a page byblos cannot walk.
func writeFixtures(t *testing.T) (dir string, files int) {
	t.Helper()
	dir = t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".pdf"), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		files++
	}
	for _, d := range corpus.All() {
		write(d.Name, d.Data)
	}
	write("mixed-page-two-unreadable", corpus.MixedPageTwoUnreadable())
	write("page-two-stops-mid-stream", corpus.PageTwoStopsMidStream())
	write("corrupt-content-stream", corpus.CorruptContentStream())
	write("corrupt-content-stream-in-array", corpus.CorruptContentStreamInArray())
	return dir, files
}

// TestWalkPartitionsEveryFileItFinds is the arithmetic the package comment
// promises: Documents and Unopenable partition Files, and Pages is the sum of
// what the callback was told each document contributes.
//
// The last check is not a tautology. Walk tallies the population from its own
// per-document return values and the callback sees a separate Doc struct; a
// change that made those two disagree -- a callback fired for a document the
// tally skipped, or a Pages field filled in from something other than what was
// counted -- would show up here and nowhere else.
func TestWalkPartitionsEveryFileItFinds(t *testing.T) {
	dir, files := writeFixtures(t)

	var seen, sum int
	var mu sync.Mutex
	p, err := Walk(dir, 3, func(d Doc) {
		mu.Lock()
		defer mu.Unlock()
		seen++
		sum += d.Pages
		if (d.Doc == nil) != (d.Err != nil) {
			t.Errorf("%s: Doc nil is %v but Err is %v; the package comment says they "+
				"are the same condition", d.Rel, d.Doc == nil, d.Err)
		}
		if d.Err != nil && d.Pages != 0 {
			t.Errorf("%s: unopenable and yet contributes %d pages", d.Rel, d.Pages)
		}
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if p.Files != files {
		t.Errorf("Walk found %d files; %d were written", p.Files, files)
	}
	if seen != files {
		t.Errorf("the callback saw %d documents; %d were written", seen, files)
	}
	if p.Documents+p.Unopenable != p.Files {
		t.Errorf("Documents %d + Unopenable %d != Files %d",
			p.Documents, p.Unopenable, p.Files)
	}
	if sum != p.Pages {
		t.Errorf("the callback was told about %d pages; the population says %d",
			sum, p.Pages)
	}
}

// TestOnlyAnUnopenableDocumentIsOutOfTheCount is the predicate test, and it is
// the one that would have caught byb-wj2's 342 pages.
//
// Exactly one document of this corpus is out of the count, "malformed", because
// it will not open. The other two failure fixtures open and MUST contribute
// every page of their page tree even though byblos cannot walk one of those
// pages: that is the second predicate in the package comment, and dropping it is
// what cost the pinned sample 342 pages over 17 documents.
//
// The identity is asserted and not just the number. A second document that
// stopped opening by accident would otherwise be absorbed by a compensating edit
// here, which is the same trade TestCorpusReadableCountIsWhatTheCorpusDeclares
// makes for internal/corpus.
func TestOnlyAnUnopenableDocumentIsOutOfTheCount(t *testing.T) {
	dir, _ := writeFixtures(t)

	pages := map[string]int{}
	var unopenable []string
	var mu sync.Mutex
	p, err := Walk(dir, 1, func(d Doc) {
		mu.Lock()
		defer mu.Unlock()
		if d.Err != nil {
			unopenable = append(unopenable, d.Rel)
			return
		}
		pages[d.Rel] = d.Pages
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if want := []string{"malformed.pdf"}; len(unopenable) != 1 || unopenable[0] != want[0] {
		t.Errorf("the documents that do not open are %v; this corpus carries exactly one "+
			"deliberately unopenable document, %v", unopenable, want)
	}
	if p.Unopenable != 1 {
		t.Errorf("Population.Unopenable is %d; %v did not open", p.Unopenable, unopenable)
	}

	// Two pages each, and the second page of each is the one byblos cannot walk
	// to the end of. A reader that gave up on the document would report 0; one
	// that dropped the bad page would report 1.
	for _, name := range []string{
		"mixed-page-two-unreadable.pdf",
		"page-two-stops-mid-stream.pdf",
		"corrupt-content-stream.pdf",
		"corrupt-content-stream-in-array.pdf",
	} {
		if got, ok := pages[name]; !ok {
			t.Errorf("%s is not in the count at all; a document holding a page byblos "+
				"cannot walk is still an openable document", name)
		} else if got != 2 {
			t.Errorf("%s contributes %d pages; it has 2, and the page byblos cannot walk "+
				"is one of them. byb-wj2: dropping it cost the pinned sample 342 pages.",
				name, got)
		}
	}
}

// TestPathsAreLexicalAndPDFOnly pins the enumeration rule, which is the other
// half of "the same population": two walks that disagree about which files are
// candidates disagree about Files before they ever open anything.
func TestPathsAreLexicalAndPDFOnly(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.pdf", "a.PDF", "c.pdf", "notes.txt", "d.pdfx"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("%PDF-1.4\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(dir, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "e.pdf"), []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths, err := Paths(dir)
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	var got []string
	for _, p := range paths {
		r, _ := filepath.Rel(dir, p)
		got = append(got, r)
	}
	want := []string{"a.PDF", "b.pdf", "c.pdf", filepath.Join("nested", "e.pdf")}
	if len(got) != len(want) {
		t.Fatalf("Paths returned %v; want %v (case-insensitive .pdf, recursive, "+
			"lexical, and .pdfx is not .pdf)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Paths[%d] is %q; want %q -- order is part of the contract",
				i, got[i], want[i])
		}
	}
}

// TestWalkIsIndependentOfWorkerCount is the concurrency contract. The callback
// runs on a worker goroutine, so a population that moved with -j would be a
// population nobody could reproduce.
func TestWalkIsIndependentOfWorkerCount(t *testing.T) {
	dir, _ := writeFixtures(t)
	var first Population
	for _, workers := range []int{1, 2, 8, 64} {
		p, err := Walk(dir, workers, nil)
		if err != nil {
			t.Fatalf("Walk(workers=%d): %v", workers, err)
		}
		if workers == 1 {
			first = p
			continue
		}
		if p != first {
			t.Errorf("workers=%d reports %+v; workers=1 reports %+v", workers, p, first)
		}
	}
	if first.Pages == 0 {
		t.Fatal("the corpus counted zero pages, so this test compared nothing")
	}
}

// TestWalkReportsIndexInLexicalOrder pins the handle a concurrent callback uses
// to emit its own output in corpus order.
func TestWalkReportsIndexInLexicalOrder(t *testing.T) {
	dir, files := writeFixtures(t)
	paths, err := Paths(dir)
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	seen := make([]string, files)
	var mu sync.Mutex
	if _, err := Walk(dir, 4, func(d Doc) {
		mu.Lock()
		defer mu.Unlock()
		if d.Index < 0 || d.Index >= len(seen) {
			t.Errorf("%s: Index %d is out of range", d.Rel, d.Index)
			return
		}
		seen[d.Index] = d.Path
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	for i := range paths {
		if seen[i] != paths[i] {
			t.Errorf("Index %d was given to %q; Paths puts %q there",
				i, seen[i], paths[i])
		}
	}
}

// TestPinnedSamplePopulation re-derives the constants in pinned.go from the real
// directory. It is the only check that the pinned numbers describe anything, and
// it can only run where the sample is, so everything else in this package is
// written to have kill power without it.
//
//	BYBLOS_SAMPLE=~/work/dobbo-ca/.byblos-sample go test ./internal/sample/
func TestPinnedSamplePopulation(t *testing.T) {
	root := os.Getenv("BYBLOS_SAMPLE")
	if root == "" {
		t.Skip("set BYBLOS_SAMPLE to the pinned sample to re-derive the pinned population")
	}
	workers := DefaultJobs
	if n, err := strconv.Atoi(os.Getenv("BYBLOS_JOBS")); err == nil && n > 0 {
		workers = n
	}
	p, err := Walk(root, workers, nil)
	if err != nil {
		t.Fatalf("Walk %s: %v", root, err)
	}
	if p.Files != Files || p.Documents != Documents || p.Pages != Pages {
		t.Errorf("%s walks to %+v; pinned.go declares Files %d, Documents %d, Pages %d. "+
			"If the sample is unchanged, byblos's reader moved and every rate divided by "+
			"the old figure is stale.", root, p, Files, Documents, Pages)
	}
}

// TestPathsRefusesADirectoryItCannotRead is the defect an adversarial review of
// byb-wj2's own probes turned up in this package: the walk pattern every other
// sweep in the tree uses discards WalkDir's error, so a subdirectory the process
// cannot open makes the population smaller and the walk still succeeds.
//
// Silently returning a short list is the exact failure this package exists to
// prevent, arriving through the enumeration instead of through the predicate.
func TestPathsRefusesADirectoryItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 directory regardless")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.pdf"), []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	closed := filepath.Join(dir, "closed")
	if err := os.MkdirAll(closed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(closed, "b.pdf"), []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })

	if paths, err := Paths(dir); err == nil {
		t.Errorf("Paths returned %v and no error over a directory it cannot read; a "+
			"population that quietly loses a subtree is the bug this package is for", paths)
	}
	if _, err := Walk(dir, 2, nil); err == nil {
		t.Error("Walk reported a population over a directory it cannot read")
	}
}

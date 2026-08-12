package sample

// The pinned sample's population, measured under the definition in this
// package's comment.
//
// THESE ARE THE SINGLE SOURCE OF THE THREE NUMBERS. Every figure quoted in the
// tree about the size of the pinned sample must equal one of them, and
// TestSampleClaimsMatchThePinnedPopulation (root package, designspec_pin_test.go)
// fails until they do. The corpus counts in internal/corpus went stale in five
// places through three successive documentation reconciliations because nothing
// connected the prose to the code (byb-a20); these numbers are quoted in as many
// places and had already produced a worse failure than staleness, two lanes
// publishing rates over two different denominators (byb-wj2).
//
// THE WORDING CARRIES THE PREDICATE, so a claim cannot be pinned to the wrong
// number by writing it loosely. "sample files" is Files and counts the
// unopenable document; "sample documents" is Documents and does not. They differ
// by one and the difference is the point.
//
// MEASURED at 5375a34 over ~/work/dobbo-ca/.byblos-sample, whose contents are
// described in tools/sample/README.md. The split, which is not pinned because no
// prose in the tree quotes it, and which is recorded here because it is the
// evidence that reconciled byb-wj2:
//
//	corpus     files   pages   partially read pages
//	govdocs1   4,840  136,135                     7
//	dc           520   15,346                     0
//	ia           299   14,943                     0
//	anchors       13    2,952                     0
//
// Re-derive them with TestPinnedSamplePopulation (this package), which walks the
// real directory and is skipped unless BYBLOS_SAMPLE points at it.
const (
	// Files is every *.pdf path under the sample root.
	Files = 5672
	// Documents is the subset pdfdoc.Open accepts. One govdocs1 file
	// (pdfs/700620.pdf, "xrefsection: missing trailer dict") is not among them.
	Documents = 5671
	// Pages is the sum of PageCount over Documents, and is the denominator every
	// rate measured over this sample divides by.
	Pages = 169376
)

// PartiallyReadPages is how many of Pages byblos opens, finds in the page tree,
// and cannot walk to the end of -- the second predicate in the package comment.
// It is here to be quoted, not to be a denominator: it is IN Pages, and it moves
// whenever byblos's reader improves, which Pages does not.
//
// 7 pages across 3 documents at 5375a34: govdocs1/pdfs/050734.pdf (3 of 22),
// 150277.pdf (3 of 138), 500865.pdf (1 of 23). byb-3jq is why they are 7 pages
// and not 3 documents' worth of 183.
const PartiallyReadPages = 7

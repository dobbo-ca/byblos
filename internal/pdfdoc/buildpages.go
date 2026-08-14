package pdfdoc

// BuildFromPages: materialise a document from a page sequence (byb-yul.4).
//
// This is the object-graph migration walk, and byblos owns it because pdfcpu
// does not offer one. api.InsertPages inserts BLANK pages; Merge and MergeRaw
// append WHOLE documents with no position argument, and MergeRaw additionally
// cannot exceed 101 pages because it nests the page tree one level per appended
// file against a depth-100 limit. pdfcpu's own migrateIndRef and migratePageDict
// are unexported (migrate.go:24, :140), and vendoring them would be a
// maintenance liability across the v0.14 bump tracked as byb-3iw.
//
// IT IS THE SAME WALK EVEN FROM ONE SOURCE. pdfcpu's own single-source path
// proves it: api.Collect calls ExtractPages, which calls AddPages, which calls
// migrateIndRef. There is no cheaper single-source route that is not in-place
// splicing, and splicing cannot serve a sequence spanning two documents.
//
// WHY A FRESH CATALOG AND NOT A GRAFT ONTO THE FIRST SOURCE'S. Measured: pdfcpu's
// writer walks the catalog, so an object that falls out of the page tree is
// never written -- splicing a page out of /Kids and calling api.WriteContext
// drops exactly that page's three objects and leaves no orphan. That makes
// grafting tempting, because it would keep the catalog, the outline tree and the
// information dictionary for free. It is wrong. Those entries describe the page
// SET or the page ORDER, and an edit invalidates them silently: over the pinned
// sample, 61.9% of the 5,063 multi-page documents carry at least one --
// /PageLabels 35.5%, /StructTreeRoot 23.7%, /Outlines 21.7%, /OpenAction 12.4%,
// /AcroForm 11.7%, /Names 9.1%. Dropping the entry is honest; carrying a stale
// one is not. See the design spec's 2026-08-13 amendment.
//
// MEASURED OVER THE PINNED SAMPLE, 9,798 builds: every multi-page document
// twice, once fully reversed and once with its middle page dropped. 4,899 of the
// 5,063 multi-page documents built each way; the 164 refused per sequence are
// 159 encrypted documents (3.1%, see openSources) plus 5 others. Page count was
// right on all 9,798, every output re-opened, and output/input size ran p10
// 0.490, median 0.856, p90 0.982, max 1.001 -- an export is smaller than its
// input, because it drops the catalog and pdfcpu's writer rewrites the rest.
//
// TWO KNOWN DEFECTS CAME OUT OF THAT RUN. Both are named here rather than left
// to be rediscovered:
//
//   - A DANGLING REFERENCE ON 11 OF 4,899 DOCUMENTS (0.22%), and the mechanism
//     generalises. pdfcpu's writer traverses only the entries it KNOWS, so an
//     entry this walk migrated but the writer does not follow leaves a reference
//     naming an object the file never defines. Measured, exactly 2 per page, in
//     vendor-private page-dictionary keys -- /CREO_Tools and /HDAG_Tools
//     (8 documents), /AAPL:PPK and /AAPL:PPKHash (1) -- and in
//     /Resources/Properties/MC0 and /MC1 (1), which is NOT private but a
//     standard marked-content property list. ISO 32000-1 7.3.10 makes such a
//     reference the null object, so a reader sees an absent entry rather than an
//     error, which is why nothing else caught it. The real fix is for byblos to
//     serialize the output itself instead of letting pdfcpu's traversal decide
//     the write set, the way internal/linearize already does; that is byb-yul.6.
//
//   - AN OUTPUT api.Validate REFUSES, ON 28 OF 4,899 (0.57%). This one is
//     CARRIED, not introduced: of the 12 documents checked, 12 had an input that
//     already fails the same validator. Open deliberately skips validation (see
//     its comment), so byblos accepts documents pdfcpu's validator refuses --
//     an unsupported page transition, a /Named action pdfcpu does not know, a
//     /Resources missing the ColorSpace subdict its content names, a date of
//     "D:20010031211931" -- and the export carries the offending object through.
//     Nothing here makes a valid document invalid.
//
// OUTPUT IS NOT BYTE-STABLE. Object numbers fall out of the order objects are
// first reached, and pdfcpu's writer is not deterministic either: measured over
// 90 documents, the same selection written twice in one process gave 0
// byte-identical outputs and 73 differing in length, first divergence at byte
// 60. A caller must content-address an export rather than rewrite a key.

import (
	"fmt"
	"io"
	"math"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// PageSource names one page of one document, and the rotation to give it.
type PageSource struct {
	// Source is the document to take the page from. Two entries naming the
	// SAME io.ReadSeeker are one logical source, opened once and shared.
	Source io.ReadSeeker
	// Page is 1-based, in Source.
	Page int
	// Rotate is the ABSOLUTE /Rotate to give the page: 0, 90, 180 or 270. It
	// is not added to whatever the source page declares.
	Rotate int
	// Straighten is a lossless rotation of the page's content, nil for none.
	// See StraightenSpec's doc comment for the sign convention and the
	// absolute-not-delta contract (byb-16j.4).
	Straighten *StraightenSpec
}

// BuildFromPages writes a document whose page i is pages[i].
//
// Deleting a page is omitting it from the sequence, reordering is ordering the
// sequence, inserting is naming a different Source, and rotating is a field. The
// output is a new document every time; no Source is modified.
//
// It writes nothing to w unless the whole document was built.
func BuildFromPages(w io.Writer, pages []PageSource) (err error) {
	defer catchPanic("build from pages", &err)

	if err := validate(pages); err != nil {
		return err
	}
	sources, err := openSources(pages)
	if err != nil {
		return err
	}
	for i, p := range pages {
		n := sources[p.Source].ctx.PageCount
		if p.Page < 1 || p.Page > n {
			return fmt.Errorf("byblos/pdfdoc: build from pages: page %d takes source page %d, "+
				"out of range 1..%d", i+1, p.Page, n)
		}
	}
	ctx, err := buildContext(pages, sources)
	if err != nil {
		return err
	}
	if err := applyStraighten(ctx, pages); err != nil {
		return err
	}
	if err := api.WriteContext(ctx, w); err != nil {
		return fmt.Errorf("byblos/pdfdoc: build from pages: write: %w", err)
	}
	return nil
}

// validate rejects what cannot be resolved without opening a document.
//
// The rotation check is not defensive tidiness. pdfcpu writes /Rotate 45 and
// /Rotate -90 with a nil error, and the 45 case produces a file pdfcpu itself
// then refuses on re-read -- so an unchecked rotation is a document that fails
// at the next reader rather than at this call. 360 is rejected with the rest:
// it is a legal quarter-turn multiple and it is not one of the four values this
// takes, and silently folding it to 0 would accept 720 too.
//
// PageInfo.Rotate is NOT a safe way to produce this field. It normalizes into
// [0,360), so a declared -90 reads back as 270, and it does not guarantee a
// multiple of 90 -- a declared 45 reads back as 45. A caller round-tripping
// through it must still be told when the value it got cannot be written.
func validate(pages []PageSource) error {
	if len(pages) == 0 {
		return fmt.Errorf("byblos/pdfdoc: build from pages: no pages")
	}
	for i, p := range pages {
		if p.Source == nil {
			return fmt.Errorf("byblos/pdfdoc: build from pages: page %d has no source", i+1)
		}
		switch p.Rotate {
		case 0, 90, 180, 270:
		default:
			return fmt.Errorf("byblos/pdfdoc: build from pages: page %d asks for rotation %d, "+
				"which is not one of 0, 90, 180, 270", i+1, p.Rotate)
		}
		if p.Straighten != nil {
			// A non-finite Deg produces a `cm` of non-finite numbers, which
			// pdfcpu writes and api.Validate then refuses -- the same shape of
			// failure as /Rotate 45, caught here at the call rather than at
			// the next reader. An angle outside (-180, 180] is NOT refused:
			// the arithmetic takes it modulo 360 by construction (design spec
			// section 2).
			if math.IsNaN(p.Straighten.Deg) || math.IsInf(p.Straighten.Deg, 0) {
				return fmt.Errorf("byblos/pdfdoc: build from pages: page %d straighten angle %v "+
					"is not finite", i+1, p.Straighten.Deg)
			}
			// Crop is declared in the contract and not implemented in this
			// version (design spec section 6); refusing it is what lets the
			// field exist now without a caller silently getting a page that
			// ignored it.
			if p.Straighten.Crop != nil {
				return fmt.Errorf("byblos/pdfdoc: build from pages: page %d straighten crop "+
					"is not implemented", i+1)
			}
		}
	}
	return nil
}

// openSources opens each distinct Source exactly once.
//
// Once, for two reasons. A document opened twice is parsed twice and held in
// memory twice, and export memory is already the binding constraint. And every
// object migrated out of it would be migrated twice, so a font or a raster
// shared by two exported pages would be written to the output twice.
//
// Sources are keyed by the interface value, so the caller decides what "the
// same document" means by handing back the same reader. A dynamic type that is
// not comparable panics on the map insert; catchPanic in BuildFromPages turns
// that into ErrMalformed rather than taking the process down.
func openSources(pages []PageSource) (map[io.ReadSeeker]*doc, error) {
	out := map[io.ReadSeeker]*doc{}
	for i, p := range pages {
		if _, done := out[p.Source]; done {
			continue
		}
		opened, err := Open(p.Source)
		if err != nil {
			return nil, fmt.Errorf("byblos/pdfdoc: build from pages: page %d source: %w", i+1, err)
		}
		d, ok := opened.(*doc)
		if !ok {
			return nil, fmt.Errorf("byblos/pdfdoc: build from pages: page %d source: "+
				"unexpected document type %T", i+1, opened)
		}
		if d.ctx.XRefTable.Encrypt != nil {
			// The same refusal Linearize makes, for the same reason
			// (linearize.go:75-81). pdfcpu decrypts strings and streams on
			// READ, so migrating them into an output with no /Encrypt
			// dictionary silently STRIPS the encryption from a document
			// somebody chose to encrypt -- and re-emitting them under the
			// source's /Encrypt would produce a file that opens as garbage.
			//
			// A document with a user password never reaches here: Open fails
			// on it, because pdfcpu cannot read it without the password. The
			// case this catches is owner-password-only, which opens with no
			// password at all and is the common one. Measured before this
			// existed: an owner-encrypted document exported to 892 bytes of
			// plaintext with a nil error.
			//
			// ReplaceImages, Optimize and StampTextLayer still have no opinion
			// here, which is byb-cvx. This fixes only its own path.
			return nil, fmt.Errorf("byblos/pdfdoc: build from pages: page %d source: "+
				"the document is encrypted", i+1)
		}
		out[p.Source] = d
	}
	return out, nil
}

// srcObj identifies one object of one source document. It is the migration's
// memo key, so an image or a font two exported pages share is migrated once.
type srcObj struct {
	d *doc
	n int
}

// migration carries the state of one build.
type migration struct {
	dest  *model.XRefTable
	pages types.IndirectRef // the output's /Pages node, for every /Parent
	done  map[srcObj]int    // source object -> output object number
	leafs map[*doc][]int    // each source's page leaves, in page order
	depth int

	// exported maps a source page leaf to the output page it became, and tree
	// holds every object of every source's page tree -- leaves and /Pages nodes
	// alike. Together they stop the walk from following a reference INTO a page.
	// See reference.
	exported map[srcObj]types.IndirectRef
	tree     map[srcObj]bool
}

// maxMigrationDepth bounds the recursion. A malformed document can hold a
// reference cycle that the memo below does not break -- the memo is written
// before the copy recurses, so a cycle terminates -- but a legal document can
// still nest resource dictionaries deeply, and an unbounded recursion on a
// hostile file is a stack overflow the process cannot recover from.
const maxMigrationDepth = 256

func buildContext(pages []PageSource, sources map[io.ReadSeeker]*doc) (*model.Context, error) {
	xt, err := pdfcpu.CreateXRefTableWithRootDict()
	if err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: build from pages: new catalog: %w", err)
	}
	// The /Pages node is reserved before anything is migrated, because every
	// migrated page's /Parent has to name it.
	pagesNr, err := xt.InsertObject(nil)
	if err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: build from pages: reserve page tree: %w", err)
	}
	m := &migration{
		dest:     xt,
		pages:    refTo(pagesNr),
		done:     map[srcObj]int{},
		leafs:    map[*doc][]int{},
		exported: map[srcObj]types.IndirectRef{},
		tree:     map[srcObj]bool{},
	}

	// Every output page is given its object number BEFORE any page is migrated,
	// because a link annotation on page 1 may name page 9 and has to resolve to
	// the page 9 this build will produce, not to the one it came from.
	kids := make(types.Array, 0, len(pages))
	for i, p := range pages {
		leaves, err := m.pageLeaves(sources[p.Source])
		if err != nil {
			return nil, fmt.Errorf("byblos/pdfdoc: build from pages: page %d: %w", i+1, err)
		}
		nr, err := xt.InsertObject(nil)
		if err != nil {
			return nil, fmt.Errorf("byblos/pdfdoc: build from pages: reserve page %d: %w", i+1, err)
		}
		kids = append(kids, refTo(nr))
		// FIRST occurrence wins. A source page named twice becomes two output
		// pages, and a destination that names it lands on the earlier one --
		// which is what a reader jumping to "that page" expects.
		key := srcObj{d: sources[p.Source], n: leaves[p.Page-1]}
		if _, seen := m.exported[key]; !seen {
			m.exported[key] = refTo(nr)
		}
	}
	for i, p := range pages {
		out, ok := kids[i].(types.IndirectRef)
		if !ok {
			return nil, fmt.Errorf("byblos/pdfdoc: build from pages: page %d lost its reservation", i+1)
		}
		if err := m.migratePage(sources[p.Source], p, out.ObjectNumber.Value()); err != nil {
			return nil, fmt.Errorf("byblos/pdfdoc: build from pages: page %d: %w", i+1, err)
		}
	}

	pagesDict := types.Dict{
		"Type":  types.Name("Pages"),
		"Kids":  kids,
		"Count": types.Integer(len(kids)),
	}
	if err := m.set(pagesNr, pagesDict); err != nil {
		return nil, err
	}
	cat, err := xt.Catalog()
	if err != nil || cat == nil {
		return nil, fmt.Errorf("byblos/pdfdoc: build from pages: new catalog: %w", err)
	}
	cat["Pages"] = m.pages
	xt.PageCount = len(kids)

	if err := m.carryInfo(sources); err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: build from pages: %w", err)
	}
	return pdfcpu.CreateContext(xt, defaultConfig()), nil
}

// infoCarried are the information-dictionary entries an export inherits.
//
// IT IS AN ALLOWLIST, not a filter, and that is the point. Every entry here
// describes the document's CONTENT and survives a page being deleted, reordered
// or rotated. Everything else is excluded by saying nothing:
//
//   - /Producer and the dates name what WROTE the bytes, which is now byblos.
//     Carrying them would misattribute the file. pdfcpu writes its own.
//   - byblos's own provenance record is under a key of its own and is
//     POSITIONAL -- Provenance.Pages index i describes page i+1 -- so an export
//     that reorders or omits a page carries a record onto the wrong page.
//     Re-indexing it is the caller's job; inheriting it silently is worse than
//     losing it. See the design spec's G3 obligation.
//   - A custom key means whatever the tool that wrote it meant. Guessing that
//     it survives a page edit is not something this can do.
var infoCarried = []string{"Title", "Author", "Subject", "Keywords", "Creator"}

// carryInfo gives the output an information dictionary, when there is one
// defensible answer.
//
// ONE SOURCE ONLY. With two, "whose title is this" has no answer that does not
// depend on the page order -- and picking the first page's source would mean
// that moving an imported page to the front silently changes what the document
// claims to be. An export that mixes sources carries nothing, which is a state a
// reader can see.
func (m *migration) carryInfo(sources map[io.ReadSeeker]*doc) error {
	if len(sources) != 1 {
		return nil
	}
	var d *doc
	for _, only := range sources {
		d = only
	}
	xt := d.ctx.XRefTable
	if xt.Info == nil {
		return nil
	}
	src, err := xt.DereferenceDict(*xt.Info)
	if err != nil || src == nil {
		// An /Info that does not resolve is the source's problem and not
		// something to fail an export over; the export simply carries none.
		return nil
	}
	out := types.Dict{}
	for _, key := range infoCarried {
		v, found := src.Find(key)
		if !found {
			continue
		}
		// A text string is written either way (ISO 32000-1 7.9.2.2), and a
		// UTF-16BE title is normally the hex form.
		switch s := d.deref(v).(type) {
		case types.StringLiteral:
			out[key] = s
		case types.HexLiteral:
			out[key] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	nr, err := m.dest.InsertObject(nil)
	if err != nil {
		return err
	}
	if err := m.set(nr, out); err != nil {
		return err
	}
	ref := refTo(nr)
	m.dest.Info = &ref
	return nil
}

// migratePage copies one source page into the output as a page of its own.
//
// The page dictionary is NOT memoized, though everything it reaches is. Two
// entries naming the same source page at two rotations are two pages of the
// output and need two dictionaries; sharing one would give both the second
// rotation. They still share the content stream, the raster and the fonts
// underneath, because those go through migrate.
func (m *migration) migratePage(d *doc, p PageSource, nr int) error {
	leaves, err := m.pageLeaves(d)
	if err != nil {
		return err
	}
	src := leaves[p.Page-1]
	xt := d.ctx.XRefTable
	o, err := xt.Dereference(refTo(src))
	if err != nil {
		return fmt.Errorf("source page object %d: %w", src, err)
	}
	pd, ok := o.(types.Dict)
	if !ok {
		return fmt.Errorf("source page object %d is a %T, not a dictionary", src, o)
	}

	out := types.Dict{}
	for key, val := range pd {
		// /Parent names the SOURCE page tree, and the output's is assigned
		// below. reference would return null for it in any case; skipping it
		// here says why rather than leaving a null to be explained.
		if key == "Parent" {
			continue
		}
		cp, err := m.copy(d, val)
		if err != nil {
			return fmt.Errorf("source page object %d /%s: %w", src, key, err)
		}
		out[key] = cp
	}
	out["Type"] = types.Name("Page")
	out["Parent"] = m.pages
	// ABSOLUTE, not additive: the caller states the rotation the exported page
	// must have, and the source page's own /Rotate is replaced rather than
	// combined. Kleio redelivers a job at least once, so a rotation that
	// accumulated on each delivery would turn a retry into a defect.
	out["Rotate"] = types.Integer(p.Rotate)
	if _, ok := out.Find("MediaBox"); !ok {
		// The source declared no /MediaBox anywhere in this page's inheritance
		// chain. byblos reads such a page as US Letter and says so through
		// Page.MediaBoxDefaulted (byb-8ly, nine govdocs1 files); writing no box
		// at all would emit a document that byblos accepted on the way in and
		// that every validator refuses on the way out.
		out["MediaBox"] = types.Array{
			types.Integer(0), types.Integer(0),
			types.Integer(defaultPageWidthPt), types.Integer(defaultPageHeightPt),
		}
	}

	return m.set(nr, out)
}

// pageLeaves returns d's page leaves in page order, having pushed every
// inherited attribute down onto them.
//
// The push-down is required, not an optimization. A /Pages node may legally hold
// /Resources, /MediaBox, /CropBox and /Rotate on behalf of its descendants (ISO
// 32000-1 table 30), and the output's page tree is a new node that holds none of
// them. MEASURED on a document whose /Pages node carries /CropBox [100 100 300
// 400]: byblos reads page 1 as (100,100)-(300,400), and pdfcpu's own api.Collect
// reports (0,0)-(612,792) after extracting it, because addPages bakes Resources,
// Parent, MediaBox and conditionally Rotate onto the migrated leaf and never
// CropBox.
//
// walkPageTree does the push-down in place on the SOURCE context, so it runs at
// most once per source.
func (m *migration) pageLeaves(d *doc) ([]int, error) {
	if leaves, ok := m.leafs[d]; ok {
		return leaves, nil
	}
	xt := d.ctx.XRefTable
	root, err := xt.Pages()
	if err != nil || root == nil {
		return nil, fmt.Errorf("source page tree: %w", err)
	}
	leaves, nodes, err := walkPageTree(xt, *root, types.Dict{}, map[int]bool{})
	if err != nil {
		return nil, err
	}
	if len(leaves) != d.ctx.PageCount {
		return nil, fmt.Errorf("source page tree walks %d pages, and the document reports %d",
			len(leaves), d.ctx.PageCount)
	}
	m.leafs[d] = leaves
	for _, n := range leaves {
		m.tree[srcObj{d: d, n: n}] = true
	}
	for _, n := range nodes {
		m.tree[srcObj{d: d, n: n}] = true
	}
	return leaves, nil
}

// reference resolves one indirect reference of a source document.
//
// IT STOPS AT A PAGE, and that is the whole reason this is not just migrate.
// /Parent is not the only way a page is reachable: a /Link annotation's /Dest
// names a page, a /GoTo action's /D names a page, and an article thread's beads
// name pages. Following one migrates the destination page's dictionary, its
// content stream and its resources into a document with no room for them -- an
// invisible copy of the page the caller deleted.
//
// A destination page that IS in this build resolves to the output page it
// became, so a link between two exported pages still works. A page that is not
// becomes null. ISO 32000-1 7.3.10 makes a reference to an undefined object the
// null object, so this loses only what was already unreachable, whereas leaving
// the reference would name an object number the output never defines. Measured
// before this existed: a two-page document exported down to page 1 wrote
// "/Dest [(5 0 R) /Fit]" with no object 5 anywhere in the file, and pdfcpu's own
// validator accepted it.
func (m *migration) reference(d *doc, n int) (types.Object, error) {
	key := srcObj{d: d, n: n}
	if ref, ok := m.exported[key]; ok {
		return ref, nil
	}
	if m.tree[key] {
		return nil, nil
	}
	return m.migrate(d, n)
}

// migrate copies the source object src.n of document src.d into the output, once.
func (m *migration) migrate(d *doc, n int) (types.IndirectRef, error) {
	key := srcObj{d: d, n: n}
	if nw, ok := m.done[key]; ok {
		return refTo(nw), nil
	}
	o, err := d.ctx.XRefTable.Dereference(refTo(n))
	if err != nil {
		return types.IndirectRef{}, fmt.Errorf("object %d: %w", n, err)
	}
	nr, err := m.dest.InsertObject(nil)
	if err != nil {
		return types.IndirectRef{}, err
	}
	// Memoized BEFORE the copy recurses, so a reference cycle terminates here
	// rather than running the stack out.
	m.done[key] = nr

	cp, err := m.copy(d, o)
	if err != nil {
		return types.IndirectRef{}, fmt.Errorf("object %d: %w", n, err)
	}
	if err := m.set(nr, cp); err != nil {
		return types.IndirectRef{}, err
	}
	return refTo(nr), nil
}

// copy deep-copies one object, migrating every indirect reference it holds.
//
// Every reference it emits is the VALUE form. types.NewIndirectRef returns a
// pointer, and both XRefTable.Dereference and Dict.PDFString match only the
// value type -- a *IndirectRef is dereferenced to itself with a nil error, and
// silently dropped by the serializer.
func (m *migration) copy(d *doc, o types.Object) (types.Object, error) {
	if m.depth++; m.depth > maxMigrationDepth {
		m.depth--
		return nil, fmt.Errorf("object graph nests deeper than %d", maxMigrationDepth)
	}
	defer func() { m.depth-- }()

	switch v := o.(type) {
	case types.IndirectRef:
		return m.reference(d, v.ObjectNumber.Value())
	case *types.IndirectRef:
		return m.reference(d, v.ObjectNumber.Value())
	case types.Dict:
		return m.copyDict(d, v)
	case types.Array:
		out := make(types.Array, len(v))
		for i, e := range v {
			cp, err := m.copy(d, e)
			if err != nil {
				return nil, err
			}
			out[i] = cp
		}
		return out, nil
	case types.StreamDict:
		return m.copyStream(d, v)
	case *types.StreamDict:
		return m.copyStream(d, *v)
	}
	return o, nil
}

func (m *migration) copyDict(d *doc, in types.Dict) (types.Dict, error) {
	out := make(types.Dict, len(in))
	for k, v := range in {
		cp, err := m.copy(d, v)
		if err != nil {
			return nil, fmt.Errorf("/%s: %w", k, err)
		}
		out[k] = cp
	}
	return out, nil
}

// copyStream copies a stream object, payload verbatim.
//
// The encoded bytes are carried across untouched: nothing here re-encodes, and
// a JBIG2 or JPX payload pdfcpu cannot decode migrates as well as a Flate one.
//
// /Length is forced DIRECT and recomputed from the payload. An indirect /Length
// would otherwise migrate an integer object of its own, and pdfcpu's writeStream
// checks len(Raw) against StreamLength and fails the write when they disagree --
// the dictionary entry and the field are written from different places and both
// have to say the same thing.
func (m *migration) copyStream(d *doc, in types.StreamDict) (types.StreamDict, error) {
	dict, err := m.copyDict(d, in.Dict)
	if err != nil {
		return types.StreamDict{}, err
	}
	out := in
	out.Dict = dict
	n := int64(len(in.Raw))
	out.Dict["Length"] = types.Integer(n)
	out.StreamLength = &n
	out.StreamLengthObjNr = nil
	return out, nil
}

// set fills a reserved output object, refusing anything pdfcpu's serializer
// would drop on the floor.
//
// auditSerializable is the same guard internal/pdfdoc/linearize.go uses and it
// is not optional here either: Dict.PDFString and Array.PDFString discard any
// value outside their switch behind a logger that is off by default, so an
// unmigrated type produces a document that parses and has silently lost an
// entry, or an array one element short.
func (m *migration) set(nr int, o types.Object) error {
	if err := auditSerializable(nr, o); err != nil {
		return err
	}
	e, ok := m.dest.FindTableEntry(nr, 0)
	if !ok || e == nil {
		return fmt.Errorf("output object %d has no cross-reference entry", nr)
	}
	e.Object = o
	return nil
}

func refTo(n int) types.IndirectRef {
	return types.IndirectRef{ObjectNumber: types.Integer(n), GenerationNumber: types.Integer(0)}
}

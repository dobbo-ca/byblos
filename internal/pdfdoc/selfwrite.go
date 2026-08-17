package pdfdoc

// writeDocument (byb-yul.6) is byblos's own serializer for a context this
// package built itself -- BuildFromPages's replacement for api.WriteContext.
//
// WHY NOT api.WriteContext. Its writer decides what gets written by
// TRAVERSING the catalog: writePageDict follows a hardcoded key list and
// nothing else, writeDeepDict skips any dict carrying /Type /Page unless
// entry.Valid (never true for a context this package built, because it is
// never run through pdfcpu's validator), and anything under a key literally
// named D or Dest has its element 0 treated as an already-resolved page
// reference and dropped. Three different ways for an object this package's
// migration walk reserved an output number for -- and wrote, into the
// in-memory table -- to never reach the file. The reference survives
// (ISO 32000-1 7.3.10 makes an undefined reference the null object), so
// nothing refuses the result; a reader just gets nothing where a resource,
// a property list or a unit dictionary should be. See buildpages.go's
// package comment for the measured extent (11 of 4,899 real documents).
//
// This package does not have that problem, because it does not traverse
// anything: xt.Table already holds exactly the write set -- every object
// buildContext and applyStraighten reserved, they also filled -- so writing
// the document is walking the table once, not walking the graph a second
// time and hoping the two walks agree.
//
// IT STREAMS. serializeAll (linearize.go) materialises every object's body
// into a map before anything is written, which is fine for Annex F
// linearization, where the whole point is a layout pass that needs random
// access to every body before deciding where the first one goes. It is not
// fine here: measured writing to an *os.File, a 133 MiB / 211-page document
// held pdfcpu's own writer to 188.9 MiB resident and a serializeAll-based
// self-writer to 804.1 MiB (4.26x) -- worse than the write-twice design this
// bead's own spec rejected on memory grounds. This writer instead serialises
// and emits ONE OBJECT AT A TIME through a bufio.Writer, recording its
// offset as it goes, so the resident cost is the source graph plus one
// object body rather than the whole output a second time over.
//
// buildpages.go:102 promises BuildFromPages "writes nothing to w unless the
// whole document was built" -- so the audit that can still refuse the whole
// write (an object reserved but never filled) runs to completion BEFORE the
// first byte reaches w. What it cannot promise, and never could: an I/O
// error partway through CAN still leave a partial write on w, the same as
// api.WriteContext's own streaming writer today.
import (
	"bufio"
	"fmt"
	"io"
	"sort"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// binaryMarker is ISO 32000-1 section 7.5.2's recommended comment line: four
// bytes above 128, right after the header, telling a byte-oriented tool the
// file holds binary data.
const binaryMarker = "%\xE2\xE3\xCF\xD3\n"

// producer is this package's own /Producer stamp (Correction 6). pdfcpu's
// ensureInfoDict writes "pdfcpu " + its own version whenever it holds the pen;
// now that this package writes the bytes, the document should say what wrote
// it. No version number: internal/pdfdoc cannot import the root byblos
// package's Version constant without a cycle, and a stale one would be worse
// than none.
const producer = "byblos"

// writeDocument audits xt's whole table, then streams it to w as a classic
// (non-cross-reference-stream) PDF: header, one object per source object
// number in numeric order, a single "0 N" xref subsection carrying a proper
// free list, and a trailer with /Size, /Root and /Info.
//
// NO /ID. /ID is optional (ISO 32000-1 14.4) and pdfcpu's writer computed it
// from a digest that folded in a clock reading -- so two builds of the exact
// same page sequence would carry different /ID pairs for no reason connected
// to their content, which is the same non-determinism buildpages.go's own
// package comment already documents and tells a caller to work around by
// content-addressing the bytes instead of trusting any single field. Adding
// one here would cost a hash over the whole output before the trailer can be
// written, for a value this package's own callers do not read.
func writeDocument(w io.Writer, xt *model.XRefTable) error {
	filled, freeNrs, max, err := auditTable(xt)
	if err != nil {
		return err
	}

	bw := bufio.NewWriter(w)
	var pos int64
	var werr error
	writeStr := func(s string) {
		if werr != nil {
			return
		}
		n, err := bw.WriteString(s)
		pos += int64(n)
		werr = err
	}
	writeBytes := func(b []byte) {
		if werr != nil {
			return
		}
		n, err := bw.Write(b)
		pos += int64(n)
		werr = err
	}

	version := xt.VersionString()
	if version == "" {
		version = "1.7"
	}
	writeStr(fmt.Sprintf("%%PDF-%s\n", version))
	writeStr(binaryMarker)

	offsets := make([]int64, max+1)
	for n := 1; n <= max; n++ {
		o, ok := filled[n]
		if !ok {
			continue // a free entry; its xref line is built from freeNrs below.
		}
		body, err := serialize(o)
		if err != nil {
			return fmt.Errorf("byblos/pdfdoc: write: object %d: %w", n, err)
		}
		if pos >= 1e10 {
			// The xref entry's offset field is a fixed 10 ASCII digits (ISO
			// 32000-1 7.5.4), so an offset at or past 10^10 would overflow
			// it and misalign every entry after this one. Refusing here is
			// cheap and keeps the guarantee that a corrupt table never
			// reaches w; producing a document this size at all is not
			// something a caller of this package does today.
			return fmt.Errorf("byblos/pdfdoc: write: object %d starts at offset %d, "+
				"past the 10-digit xref offset field's limit", n, pos)
		}
		offsets[n] = pos
		writeStr(fmt.Sprintf("%d 0 obj\n", n))
		writeBytes(body)
		writeStr("\nendobj\n")
		if werr != nil {
			return fmt.Errorf("byblos/pdfdoc: write: object %d: %w", n, werr)
		}
	}

	start := pos
	writeStr(fmt.Sprintf("xref\n0 %d\n", max+1))
	next := 0
	if len(freeNrs) > 0 {
		next = freeNrs[0]
	}
	// Object 0 is per definition the free-list head, generation 65535
	// (types.FreeHeadGeneration): its "offset" field names the first free
	// object, or itself (0) when nothing is free -- which is every build
	// this package's own migration produces, since it never deletes an
	// object it reserved. See auditTable's comment for when this is not
	// empty.
	writeStr(fmt.Sprintf("%010d 65535 f \n", next))
	for n := 1; n <= max; n++ {
		if _, ok := filled[n]; ok {
			writeStr(fmt.Sprintf("%010d 00000 n \n", offsets[n]))
			continue
		}
		idx := sort.SearchInts(freeNrs, n)
		next := 0
		if idx+1 < len(freeNrs) {
			next = freeNrs[idx+1]
		}
		writeStr(fmt.Sprintf("%010d 00000 f \n", next))
	}

	trailer := fmt.Sprintf("trailer\n<< /Size %d /Root %d 0 R",
		max+1, xt.Root.ObjectNumber.Value())
	if xt.Info != nil {
		trailer += fmt.Sprintf(" /Info %d 0 R", xt.Info.ObjectNumber.Value())
	}
	trailer += fmt.Sprintf(" >>\nstartxref\n%d\n%%%%EOF\n", start)
	writeStr(trailer)
	if werr != nil {
		return fmt.Errorf("byblos/pdfdoc: write: %w", werr)
	}
	return bw.Flush()
}

// auditTable walks xt.Table once, before writeDocument commits to writing
// anything, and reports:
//
//   - filled: every in-use object, by number, ready for serialize.
//   - freeNrs: every genuinely free object number above 0, sorted --
//     PRACTICALLY ALWAYS EMPTY for a table this package builds. Object
//     numbers here come from two calls: InsertObject (buildpages.go, always
//     appends -- xreftable.go:InsertNew) and IndRefForNewObject
//     (applyStraighten, text.go:73,98,112,242,357 -- InsertAndUseRecycled).
//     The second name suggests object reuse, but it only recycles when the
//     free-list head's offset is nonzero, and nothing on this package's
//     write path ever frees an object it reserved -- so today it always
//     takes InsertAndUseRecycled's "nothing to recycle" branch, which is
//     InsertNew under another name. This is NOT assumed, only measured: the
//     table is walked as it actually is, in case that ever stops holding.
//   - max: the highest object number in the table, so writeDocument knows
//     how far the single xref subsection runs.
//
// AN OBJECT WHOSE ENTRY IS FILLED BUT WHOSE Object FIELD IS nil is a hard
// error, not a written null. That field is a nil interface in exactly two
// cases this package cannot tell apart from the table alone: a slot
// buildContext or applyStraighten reserved and a bug left unfilled (m.set is
// always called on every reservation buildpages.go makes -- see its own
// comment -- so this would mean that promise broke), and a slot migration
// legitimately filled with the PDF null value, because the SOURCE document
// named an object number it never defines (ISO 32000-1 7.3.10; Open
// deliberately skips validation, so such a source reaches here). Measured
// over the whole pinned sample this never happens either way
// (refusedSelfOnly = 0 across 9,798 builds) -- it is synthetic-only today.
// Given the choice between silently writing null for what might be this
// package's OWN bug, and refusing a build that might be a legal-but-unusual
// source, this refuses: an internal invariant breaking must never reach a
// reader as a quietly wrong file.
func auditTable(xt *model.XRefTable) (filled map[int]types.Object, freeNrs []int, max int, err error) {
	filled = map[int]types.Object{}
	for n, e := range xt.Table {
		if n == 0 || e == nil {
			continue
		}
		if n > max {
			max = n
		}
		if e.Free {
			freeNrs = append(freeNrs, n)
			continue
		}
		switch e.Object.(type) {
		case types.ObjectStreamDict, *types.ObjectStreamDict, types.XRefStreamDict, *types.XRefStreamDict:
			// An object stream or a cross-reference stream describes how the
			// INPUT was STORED, not a logical object in its graph: nothing
			// this package's readers or writer produce ever holds an
			// IndirectRef naming one by object number (the compressed
			// objects inside one surface as ordinary entries elsewhere in
			// this same table once dereferenced -- see linearize.go's
			// liveObjects for the identical reasoning). writeDocument's only
			// caller today is BuildFromPages' own fresh context, which never
			// creates either type, so this branch is unreached in practice --
			// it is a safety net, not load-bearing, for the day a context
			// built from an arbitrary read document reaches this writer
			// (WriteProperties does not: it still goes through
			// api.AddProperties, untouched by this bead). Treating the slot
			// as free, rather than writing a container object no reader will
			// ever look up by number again, is what pdfcpu's own writer
			// effectively does too: it never traverses to one either.
			freeNrs = append(freeNrs, n)
			continue
		}
		if e.Object == nil {
			return nil, nil, 0, fmt.Errorf(
				"byblos/pdfdoc: write: object %d was reserved but never filled", n)
		}
		if err := auditSerializable(n, e.Object); err != nil {
			return nil, nil, 0, err
		}
		filled[n] = e.Object
	}
	sort.Ints(freeNrs)
	// Every object number from 1 to max must be accounted for as either
	// filled or free -- a classic xref table has no third state (ISO 32000-1
	// 7.5.4: the subsection covers every number in its range). InsertNew
	// (pdfcpu's xreftable.go) always appends at the table's current Size, so
	// this package's own migration and applyStraighten never leave a gap --
	// but that is a fact about today's callers, not a guarantee this
	// function can take on faith. Left unchecked, a gap would fall through
	// writeDocument's free-list loop and be chained as though it were a
	// genuinely free entry it was never recorded as, corrupting the chain
	// for every free object numbered after it.
	for n := 1; n <= max; n++ {
		if _, ok := filled[n]; ok {
			continue
		}
		if idx := sort.SearchInts(freeNrs, n); idx < len(freeNrs) && freeNrs[idx] == n {
			continue
		}
		return nil, nil, 0, fmt.Errorf(
			"byblos/pdfdoc: write: object %d has no cross-reference entry", n)
	}
	return filled, freeNrs, max, nil
}

package pdfdoc

// Deterministic pdfcpu output (byb-c53).
//
// pdfcpu v0.15.0's writer is nondeterministic three ways, and only the first
// two are stamps that could be patched after the fact: ensureInfoDict
// (info.go) writes CreationDate/ModDate from time.Now(), fileID (crypto.go)
// hashes time.Now() nanoseconds into the trailer's /ID -- and writeDeepDict
// (writeObjects.go) emits referenced objects in Go MAP ITERATION order, so
// object order, xref offsets and object-stream interiors shuffle from run to
// run with no knob to stop it. Measured on this repo's corpus: the same
// StampTextLayer call produced 2 distinct outputs in 6 runs. That breaks
// deduplication, content-addressed storage and any integrity check that
// re-derives a document and compares (byblos is a library for an archive that
// stores what it produces).
//
// The only fix that covers all three is to not let pdfcpu's traversal hold
// the pen: writeDeterministic serializes the context itself, with the same
// primitives the Annex F linearizer already uses on arbitrary read documents
// (liveObjects, serialize, auditSerializable -- linearize.go). Objects are
// renumbered contiguously in ascending source-number order; non-stream
// objects are packed into one object stream and the trailer is a
// cross-reference stream, mirroring the compressed layout pdfcpu would have
// used (see deterministicBytes for the measured size cost of not doing so).
// The per-run stamps become pure functions of the input:
//
//   - CreationDate/ModDate are the input document's own dates, normalized to
//     the fixed 23-byte D:YYYYMMDDHHMMSS±HH'MM' form (preserving their UTC
//     offset), or the constant pinnedDate when it had none.
//
//   - /ID keeps the input's first element when the input carried an /ID (ISO
//     32000-1 14.4 makes it the document's permanent identity) and derives
//     the second from an md5 of the emitted body -- still a fingerprint of
//     the file, but a deterministic one.
//
// Determinism is the DEFAULT, not an option: nondeterminism here is never a
// feature anyone asks for, and an opt-in flag would leave the archive's
// default behaviour broken.
//
// FAIL OPEN. Anything this writer cannot hold safely -- an /Encrypt
// dictionary (pdfcpu re-encrypts strings and streams on write from the same
// context, which encryptwritepaths_test.go proves 198 real documents depend
// on; this writer cannot), a catalog-less table, an object pdfcpu's
// serializer would silently mangle -- falls back to api.WriteContext:
// nondeterministic bytes, but never a corrupted document.

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// pinnedDate is the CreationDate/ModDate written when the input document had
// none of its own: one documented constant instead of the wall clock. The
// value is arbitrary but load-bearing — changing it changes every produced
// document's bytes and hence its content hash.
const pinnedDate = "D:20000101000000+00'00'"

// writeDeterministic serializes ctx to w with deterministic bytes, falling
// back to pdfcpu's own (nondeterministic) writer when it cannot hold the pen
// safely -- see the package comment's fail-open list.
func writeDeterministic(ctx *model.Context, w io.Writer) error {
	if b, ok := deterministicBytes(ctx); ok {
		_, err := w.Write(b)
		return err
	}
	return api.WriteContext(ctx, w)
}

// infoDates returns ctx's Info-dictionary CreationDate and ModDate as raw
// date strings, "" for absent or unreadable.
func infoDates(ctx *model.Context) (creation, mod string) {
	if ctx.Info == nil {
		return "", ""
	}
	d, err := ctx.DereferenceDict(*ctx.Info)
	if err != nil || d == nil {
		return "", ""
	}
	return rawDate(ctx, d["CreationDate"]), rawDate(ctx, d["ModDate"])
}

func rawDate(ctx *model.Context, o types.Object) string {
	o, err := ctx.Dereference(o)
	if err != nil {
		return ""
	}
	switch v := o.(type) {
	case types.StringLiteral:
		if s, err := types.StringLiteralToString(v); err == nil {
			return s
		}
	case types.HexLiteral:
		if s, err := types.HexLiteralToString(v); err == nil {
			return s
		}
	}
	return ""
}

// pinDate normalizes raw to pdfcpu's fixed 23-byte D:YYYYMMDDHHMMSS±HH'MM'
// form (any valid PDF date has one, and it preserves the date's own UTC
// offset), falling back to pinnedDate when raw is absent, unparseable, or —
// out-of-range years — would not render at that length.
func pinDate(raw string) string {
	if raw != "" {
		if t, ok := types.DateTime(raw, true); ok {
			if s := types.DateString(t); len(s) == len(pinnedDate) {
				return s
			}
		}
	}
	return pinnedDate
}

// pinInfo rewrites ctx's Info dictionary in place to its deterministic form —
// dates pinned, /Producer naming what actually writes the bytes (pdfcpu's
// ensureInfoDict would have clobbered all three with per-run/per-version
// values) — creating the Info object when the document has none, exactly as
// pdfcpu's own writer would have. Idempotent: pinned dates normalize to
// themselves, so a second Write emits the same bytes.
func pinInfo(ctx *model.Context) error {
	creation, mod := infoDates(ctx)
	var d types.Dict
	if ctx.Info != nil {
		var err error
		d, err = ctx.DereferenceDict(*ctx.Info)
		if err != nil || d == nil {
			return fmt.Errorf("info dictionary: %v", err)
		}
	} else {
		d = types.NewDict()
		ir, err := ctx.XRefTable.IndRefForNewObject(d)
		if err != nil {
			return err
		}
		ctx.Info = ir
	}
	d["CreationDate"] = types.StringLiteral(pinDate(creation))
	d["ModDate"] = types.StringLiteral(pinDate(mod))
	d["Producer"] = types.StringLiteral(producer)
	return nil
}

// deterministicBytes renders ctx as a complete PDF whose bytes are a pure
// function of the context's content, or ok=false when pdfcpu's own writer
// must hold the pen instead (see the fail-open list in writeDeterministic's
// comment).
//
// The layout matches what pdfcpu's writer would have used in spirit — every
// non-stream object packed into one object stream, a cross-reference stream
// as the trailer — because dropping that compression costs real size
// (measured on pdfcpu's bookletTest.pdf: a classic-xref rendering was 51,019
// bytes where pdfcpu wrote 34,533, which flipped byblos.Optimize's
// is-it-smaller policy into pass-through). Only the ORDER is different:
// objects are renumbered contiguously in ascending source-number order, so
// nothing depends on Go map iteration.
//
// ponytail: the whole output is built in memory, like the writePinned design
// it replaces; stream one object at a time (selfwrite.go's shape) if a write
// path ever outgrows the callers that already hold input and output in memory.
func deterministicBytes(ctx *model.Context) ([]byte, bool) {
	xt := ctx.XRefTable
	if xt.Encrypt != nil || xt.Root == nil {
		return nil, false
	}
	if err := pinInfo(ctx); err != nil {
		return nil, false
	}
	live, err := liveObjects(xt)
	if err != nil {
		return nil, false
	}
	rootNr := xt.Root.ObjectNumber.Value()
	infoNr := xt.Info.ObjectNumber.Value()
	keep := reachable(live, rootNr, infoNr)
	if !keep[rootNr] || !keep[infoNr] {
		return nil, false
	}
	olds := make([]int, 0, len(keep))
	for n := range keep {
		olds = append(olds, n)
	}
	slices.Sort(olds)
	renumber := make(map[int]int, len(olds))
	for i, old := range olds {
		renumber[old] = i + 1
	}

	// Serialize every object, split by where it can live: a stream object
	// must be a top-level object, everything else goes into the object
	// stream (ISO 32000-1 7.5.7).
	type topObj struct {
		nr     int
		body   []byte
		offset int
	}
	var tops []topObj
	var packedNrs []int
	var packed [][]byte
	for i, old := range olds {
		if err := auditSerializable(old, live[old]); err != nil {
			return nil, false
		}
		body, err := serialize(renumberObject(live[old], renumber))
		if err != nil {
			return nil, false
		}
		switch live[old].(type) {
		case types.StreamDict, *types.StreamDict:
			tops = append(tops, topObj{nr: i + 1, body: body})
		default:
			packedNrs = append(packedNrs, i+1)
			packed = append(packed, body)
		}
	}
	if len(packed) > 0xFFFF {
		return nil, false // the xref stream's object-stream index field is 2 bytes
	}
	objStmNr := len(olds) + 1
	xrefNr := objStmNr + 1

	var buf bytes.Buffer
	version := xt.VersionString()
	// Cross-reference and object streams are PDF 1.5 features; a 1.0-1.4
	// input's content is untouched, but the file that now carries it must
	// declare a version a reader will accept them under.
	if version == "" || version < "1.5" {
		version = "1.5"
	}
	fmt.Fprintf(&buf, "%%PDF-%s\n%s", version, binaryMarker)

	// The object stream: "nr offset" pairs, then the bodies (7.5.7). Never
	// empty: the catalog and the Info dictionary are always packable.
	objStmOffset := buf.Len()
	var header, bodies bytes.Buffer
	for i, nr := range packedNrs {
		fmt.Fprintf(&header, "%d %d ", nr, bodies.Len())
		bodies.Write(packed[i])
		bodies.WriteByte('\n')
	}
	d := types.Dict{
		"Type":   types.Name("ObjStm"),
		"N":      types.Integer(len(packed)),
		"First":  types.Integer(header.Len()),
		"Filter": types.Name("FlateDecode"),
	}
	raw, err := flateRaw(append(header.Bytes(), bodies.Bytes()...))
	if err != nil {
		return nil, false
	}
	body, err := streamBody(types.StreamDict{Dict: d, Raw: raw})
	if err != nil {
		return nil, false
	}
	fmt.Fprintf(&buf, "%d 0 obj\n", objStmNr)
	buf.Write(body)
	buf.WriteString("\nendobj\n")
	for i := range tops {
		tops[i].offset = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n", tops[i].nr)
		buf.Write(tops[i].body)
		buf.WriteString("\nendobj\n")
	}
	start := buf.Len()
	if start > 0xFFFFFFFF {
		return nil, false // the xref stream's offset field below is 4 bytes
	}

	// /ID: a deterministic fingerprint. Element two is the md5 of everything
	// emitted so far; element one is the input's own first element when it
	// carried an /ID (unchanged identity), else the same digest.
	sum := md5.Sum(buf.Bytes())
	fresh := strings.ToUpper(hex.EncodeToString(sum[:]))
	el0 := fresh
	if xt.ID != nil {
		if b := trailerID(xt)[0]; b != nil {
			el0 = strings.ToUpper(hex.EncodeToString(b))
		}
	}

	// The cross-reference stream (7.5.8): W [1 4 2], one contiguous
	// subsection covering object 0 through the xref stream itself.
	size := xrefNr + 1
	entries := make([]byte, 0, 7*size)
	add := func(kind byte, mid int, low uint16) {
		entries = append(entries, kind,
			byte(mid>>24), byte(mid>>16), byte(mid>>8), byte(mid),
			byte(low>>8), byte(low))
	}
	add(0, 0, 0xFFFF) // object 0: head of an empty free list
	stmIdx := map[int]int{}
	for i, nr := range packedNrs {
		stmIdx[nr] = i
	}
	topOff := map[int]int{}
	for _, o := range tops {
		topOff[o.nr] = o.offset
	}
	for nr := 1; nr <= len(olds); nr++ {
		if i, ok := stmIdx[nr]; ok {
			add(2, objStmNr, uint16(i))
		} else {
			add(1, topOff[nr], 0)
		}
	}
	add(1, objStmOffset, 0) // the object stream
	add(1, start, 0)        // the xref stream itself
	xraw, err := flateRaw(entries)
	if err != nil {
		return nil, false
	}
	xd := types.Dict{
		"Type":   types.Name("XRef"),
		"Size":   types.Integer(size),
		"W":      types.Array{types.Integer(1), types.Integer(4), types.Integer(2)},
		"Index":  types.Array{types.Integer(0), types.Integer(size)},
		"Filter": types.Name("FlateDecode"),
		"Root": types.IndirectRef{ObjectNumber: types.Integer(renumber[rootNr]),
			GenerationNumber: types.Integer(0)},
		"Info": types.IndirectRef{ObjectNumber: types.Integer(renumber[infoNr]),
			GenerationNumber: types.Integer(0)},
		"ID": types.Array{types.HexLiteral(el0), types.HexLiteral(fresh)},
	}
	xbody, err := streamBody(types.StreamDict{Dict: xd, Raw: xraw})
	if err != nil {
		return nil, false
	}
	fmt.Fprintf(&buf, "%d 0 obj\n", xrefNr)
	buf.Write(xbody)
	fmt.Fprintf(&buf, "\nendobj\nstartxref\n%d\n%%%%EOF\n", start)
	return buf.Bytes(), true
}

// flateRaw is FlateDecode's encode side: zlib, default level — deterministic
// for a given input.
func flateRaw(b []byte) ([]byte, error) {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(b); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return z.Bytes(), nil
}

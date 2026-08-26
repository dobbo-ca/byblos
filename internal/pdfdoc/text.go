package pdfdoc

// The invisible-text write seam (byb-b4 part B).
//
// StampTextLayer (the root package) builds operator bytes and a TrueTypeFont
// value; everything that actually touches pdfcpu types -- allocating the font
// objects, binding them into a page's /Font resources, and splicing a new
// content stream into /Contents -- happens here, for the same reason
// write.go's image half does: only this package may import pdfcpu
// (arch_test.go).
//
// AddFontResource and AppendContent are on Doc next to ReplaceImage and Write:
// both need the context Open normalized (normalizePageTree), so there is no
// way to reach them from a document Byblos did not read.

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// TrueTypeFont describes a simple, single-byte-encoded TrueType font to embed.
//
// It is deliberately narrow: exactly the fields a simple (non-Type0)
// /FontDescriptor and /Font dictionary need. Widths is in 1000ths of text
// space (ISO 32000-1 9.2.4), indexed from FirstChar, one entry per character
// code up to LastChar (== FirstChar+len(Widths)-1).
type TrueTypeFont struct {
	BaseFont    string // /BaseFont and /FontDescriptor /FontName
	Program     []byte // the sfnt bytes; becomes /FontFile2 with /Length1 = len(Program)
	FirstChar   int
	Widths      []int
	Flags       int
	FontBBox    [4]int
	ItalicAngle int
	Ascent      int
	Descent     int
	CapHeight   int
	StemV       int
}

// AddFontResource embeds f as an indirect font object the first time it is
// called for a given f.BaseFont on this document, then binds it into page n's
// /Font resource dictionary under a name no other font on that page uses, and
// returns that name.
//
// Binding into an inherited /Resources dictionary mutates it for every page
// that inherits it. That is harmless here -- the same font object under a
// unique name -- but it is a fact worth stating rather than discovering.
func (d *doc) AddFontResource(n int, f TrueTypeFont) (name string, err error) {
	defer catchPanic(fmt.Sprintf("add font resource on page %d", n), &err)
	xt := d.ctx.XRefTable

	ref, ok := d.fontRefs[f.BaseFont]
	if !ok {
		fsd, err := xt.NewStreamDictForBuf(f.Program)
		if err != nil {
			return "", fmt.Errorf("byblos/pdfdoc: font %s: %w", f.BaseFont, err)
		}
		// /Length1 is the length of the UNCOMPRESSED font program and is
		// required by ISO 32000-1 table 126: pdfcpu's own validator rejects a
		// FontFile2 stream without it. It must be set before Encode(), which
		// compresses Content into Raw and leaves Length1 alone.
		fsd.InsertInt("Length1", len(f.Program))
		if err := fsd.Encode(); err != nil {
			return "", fmt.Errorf("byblos/pdfdoc: font %s: encode font file: %w", f.BaseFont, err)
		}
		fontFileRef, err := xt.IndRefForNewObject(*fsd)
		if err != nil {
			return "", fmt.Errorf("byblos/pdfdoc: font %s: %w", f.BaseFont, err)
		}

		widths := make(types.Array, len(f.Widths))
		for i, wv := range f.Widths {
			widths[i] = types.Integer(wv)
		}
		bbox := make(types.Array, len(f.FontBBox))
		for i, v := range f.FontBBox {
			bbox[i] = types.Integer(v)
		}
		descr := types.Dict{
			"Type":        types.Name("FontDescriptor"),
			"FontName":    types.Name(f.BaseFont),
			"Flags":       types.Integer(f.Flags),
			"FontBBox":    bbox,
			"ItalicAngle": types.Integer(f.ItalicAngle),
			"Ascent":      types.Integer(f.Ascent),
			"Descent":     types.Integer(f.Descent),
			"CapHeight":   types.Integer(f.CapHeight),
			"StemV":       types.Integer(f.StemV),
			"FontFile2":   *fontFileRef,
		}
		descrRef, err := xt.IndRefForNewObject(descr)
		if err != nil {
			return "", fmt.Errorf("byblos/pdfdoc: font %s: %w", f.BaseFont, err)
		}
		fontDict := types.Dict{
			"Type":           types.Name("Font"),
			"Subtype":        types.Name("TrueType"),
			"BaseFont":       types.Name(f.BaseFont),
			"FirstChar":      types.Integer(f.FirstChar),
			"LastChar":       types.Integer(f.FirstChar + len(f.Widths) - 1),
			"Widths":         widths,
			"Encoding":       types.Name("WinAnsiEncoding"),
			"FontDescriptor": *descrRef,
		}
		fontRef, err := xt.IndRefForNewObject(fontDict)
		if err != nil {
			return "", fmt.Errorf("byblos/pdfdoc: font %s: %w", f.BaseFont, err)
		}
		ref = *fontRef
		if d.fontRefs == nil {
			d.fontRefs = map[string]types.IndirectRef{}
		}
		d.fontRefs[f.BaseFont] = ref
	}

	// consolidateRes=false: true runs pdfcpu's resource consolidation, which
	// prunes the returned Resources dict down to only what the page's OWN
	// content stream names -- it never walks a Form XObject's or an
	// annotation appearance stream's content (pdfcpu's xreftable.go says so
	// itself, in a TODO on consolidateResourcesWithContent). A page with no
	// own /Resources that inherits one, whose content reaches a resource
	// only indirectly through a Do, would silently lose entries other
	// content still needs. false returns the inherited dict as pdfcpu found
	// it, unpruned.
	pd, _, inh, err := xt.PageDict(n, false)
	if err != nil {
		return "", fmt.Errorf("byblos/pdfdoc: page %d dict: %w", n, err)
	}
	if pd == nil {
		return "", fmt.Errorf("byblos/pdfdoc: page %d has no dictionary", n)
	}

	// A page's own /Resources, if it has one; otherwise adopt the inherited
	// one rather than creating an empty own dictionary, which would discard
	// whatever the page inherits.
	res, _ := xt.DereferenceDict(pd["Resources"])
	if res == nil {
		if inh != nil && inh.Resources != nil {
			res = inh.Resources
		} else {
			res = types.NewDict()
		}
		pd["Resources"] = res
	}
	fonts, _ := xt.DereferenceDict(res["Font"])
	if fonts == nil {
		fonts = types.NewDict()
		res["Font"] = fonts
	}
	for i := 0; ; i++ {
		name = fmt.Sprintf("BbGl%d", i)
		if _, found := fonts.Find(name); !found {
			break
		}
	}
	fonts[name] = ref
	return name, nil
}

// AppendContent appends ops to page n's content, first countering whatever
// net transform the existing content left in effect, so ops execute in PDF
// default user space regardless of what came before them.
//
// "Net transform" is not just the graphics-state stack depth: a `cm` issued
// outside any q/Q pair (Chrome/Skia's print-to-PDF output starts with one, a
// y-flip) changes the CTM permanently, with a balanced stack and qDepth==0,
// and no number of trailing Q operators undoes it. netCTM walks the whole
// stream tracking q/Q/cm the way a PDF interpreter would and returns the
// matrix actually in effect at the end; AppendContent wraps ops in a q/cm/Q
// block that applies that matrix's inverse first, then restores the state
// for whatever runs after (nothing does, since this is always the last
// content on the page, but the wrap costs nothing and keeps the invariant
// obviously true rather than true by accident of being last).
//
// It appends a new stream to /Contents rather than rewriting an existing one:
// rewriting means decoding and re-encoding bytes that are not this call's
// business, for no reason, and fails outright on a filter pdfcpu cannot
// decode. Appending is byte-preserving for everything already there.
func (d *doc) AppendContent(n int, ops []byte) (err error) {
	defer catchPanic(fmt.Sprintf("append content on page %d", n), &err)
	xt := d.ctx.XRefTable

	pd, _, _, err := xt.PageDict(n, false)
	if err != nil {
		return fmt.Errorf("byblos/pdfdoc: page %d dict: %w", n, err)
	}
	if pd == nil {
		return fmt.Errorf("byblos/pdfdoc: page %d has no dictionary", n)
	}

	// A page whose /Contents decodes to zero bytes -- absent, or an array of
	// empty streams -- leaves the CTM at identity, and identity is not a guess:
	// it is the only CTM zero bytes can leave. pageContents distinguishes that
	// from a page whose streams failed to decode, which is refused; pdfcpu
	// reports the two with the identical sentinel and the identical zero bytes,
	// which is why byblos decodes them itself (byb-3iw).
	//
	// Recovered content is used AS content here, deliberately. A page carrying a
	// bad Adler-32 has a real net CTM and stamping identity onto it would place
	// the text layer wrong; pdfcpu v0.13 used these bytes too, silently. Page
	// states the recovery through ContentRecovered.
	ctm := identityMatrix
	cur, _, err := d.pageContents(pd)
	if err != nil {
		return fmt.Errorf("byblos/pdfdoc: page %d content: %w", n, err)
	}
	if len(cur) > 0 {
		ctm = netCTM(cur)
	}

	var buf bytes.Buffer
	buf.WriteString("q\n")
	if inv, ok := ctm.invert(); ok && inv != identityMatrix {
		fmt.Fprintf(&buf, "%.10f %.10f %.10f %.10f %.10f %.10f cm\n",
			inv.a, inv.b, inv.c, inv.d, inv.e, inv.f)
	}
	// A singular net CTM (determinant 0, e.g. a degenerate "0 0 0 0 0 0 cm")
	// cannot be inverted; there is no matrix that undoes a collapse to a
	// point or a line. Falling through without a correcting cm at least
	// matches this call's own behaviour before any transform tracking
	// existed, rather than failing a stamp over page content already this
	// broken.
	buf.Write(ops)
	buf.WriteString("\nQ\n")

	sd, err := xt.NewStreamDictForBuf(buf.Bytes())
	if err != nil {
		return fmt.Errorf("byblos/pdfdoc: page %d: new content stream: %w", n, err)
	}
	if err := sd.Encode(); err != nil {
		return fmt.Errorf("byblos/pdfdoc: page %d: encode content stream: %w", n, err)
	}
	ref, err := xt.IndRefForNewObject(*sd)
	if err != nil {
		return fmt.Errorf("byblos/pdfdoc: page %d: %w", n, err)
	}

	switch c := pd["Contents"].(type) {
	case types.IndirectRef:
		if err := appendToIndirectContents(xt, pd, c, *ref); err != nil {
			return fmt.Errorf("byblos/pdfdoc: page %d: %w", n, err)
		}
	case *types.IndirectRef:
		if err := appendToIndirectContents(xt, pd, *c, *ref); err != nil {
			return fmt.Errorf("byblos/pdfdoc: page %d: %w", n, err)
		}
	case types.Array:
		pd["Contents"] = append(c, *ref)
	case nil:
		pd["Contents"] = types.Array{*ref}
	default:
		// ISO 32000-1 7.3.8.1: every stream shall be an indirect object, so a
		// direct types.StreamDict in /Contents is malformed. Matches
		// ReplaceImage's posture on a direct image stream (write.go).
		return fmt.Errorf("byblos/pdfdoc: page %d has a direct /Contents stream, which is malformed", n)
	}
	return nil
}

// ErrUnbalancedContent reports a content stream whose q/Q nesting does not
// come back to zero, in either direction. A surplus Q pops a state
// WrapContent's own "before" wrapper pushed, so everything painted after
// that point in the stream is silently NOT rotated -- a page that comes out
// half-corrected, with no error. A surplus q is merely malformed. Both are
// refused, because a wrapper that only half-applies is worse than a refusal.
var ErrUnbalancedContent = errors.New("byblos/pdfdoc: unbalanced q/Q in content stream")

// WrapContent brackets page n's whole content with before and after, as two
// new streams. No existing stream is decoded or rewritten: s1 s2 s3 stay
// byte for byte what they were, and the new streams are appended (not
// merged) at either end of /Contents.
//
// Unlike AppendContent, this needs no netCTM inverse: a prepend is the
// outermost transform by construction, so before and after run exactly as
// given, and the trailing "Q" a caller writes into after restores whatever
// state before's own "q" pushed -- it works even when the existing content
// leaves a stray cm outside any q/Q pair, because before/after never look at
// the existing content's net transform at all.
//
// contentDepth refuses first (ErrUnbalancedContent): an unbalanced q/Q in
// the existing content would pop or leave open the state before's own q
// pushed, so the wrap would only half-apply.
func (d *doc) WrapContent(n int, before, after []byte) (err error) {
	defer catchPanic(fmt.Sprintf("wrap content on page %d", n), &err)
	xt := d.ctx.XRefTable

	pd, _, _, err := xt.PageDict(n, false)
	if err != nil {
		return fmt.Errorf("byblos/pdfdoc: page %d dict: %w", n, err)
	}
	if pd == nil {
		return fmt.Errorf("byblos/pdfdoc: page %d has no dictionary", n)
	}

	if _, err := d.contentDepth(n); err != nil {
		return fmt.Errorf("byblos/pdfdoc: page %d: %w", n, err)
	}

	beforeRef, err := d.newContentStream(before)
	if err != nil {
		return fmt.Errorf("byblos/pdfdoc: page %d: before stream: %w", n, err)
	}
	afterRef, err := d.newContentStream(after)
	if err != nil {
		return fmt.Errorf("byblos/pdfdoc: page %d: after stream: %w", n, err)
	}

	switch c := pd["Contents"].(type) {
	case types.IndirectRef:
		if err := wrapIndirectContents(xt, pd, c, *beforeRef, *afterRef); err != nil {
			return fmt.Errorf("byblos/pdfdoc: page %d: %w", n, err)
		}
	case *types.IndirectRef:
		if err := wrapIndirectContents(xt, pd, *c, *beforeRef, *afterRef); err != nil {
			return fmt.Errorf("byblos/pdfdoc: page %d: %w", n, err)
		}
	case types.Array:
		wrapped := make(types.Array, 0, len(c)+2)
		wrapped = append(wrapped, *beforeRef)
		wrapped = append(wrapped, c...)
		wrapped = append(wrapped, *afterRef)
		pd["Contents"] = wrapped
	case nil:
		// No content to rotate, but the wrapper is still written so the
		// record and the file agree: a caller that asked for a straighten
		// gets one, even on an empty page.
		pd["Contents"] = types.Array{*beforeRef, *afterRef}
	default:
		// ISO 32000-1 7.3.8.1: every stream shall be an indirect object, so a
		// direct types.StreamDict in /Contents is malformed. Matches
		// AppendContent and ReplaceImage's posture on a direct image stream.
		return fmt.Errorf("byblos/pdfdoc: page %d has a direct /Contents stream, which is malformed", n)
	}
	return nil
}

// newContentStream writes ops as a new, encoded content stream object and
// returns its indirect reference.
func (d *doc) newContentStream(ops []byte) (*types.IndirectRef, error) {
	xt := d.ctx.XRefTable
	sd, err := xt.NewStreamDictForBuf(ops)
	if err != nil {
		return nil, fmt.Errorf("new content stream: %w", err)
	}
	if err := sd.Encode(); err != nil {
		return nil, fmt.Errorf("encode content stream: %w", err)
	}
	return xt.IndRefForNewObject(*sd)
}

// wrapIndirectContents handles /Contents as an indirect reference, which ISO
// 32000-1 table 30 allows to resolve to either a stream or an array of
// streams. Those are not interchangeable -- see appendToIndirectContents,
// which names the identical trap for AppendContent's single-ended case: an
// indirect reference to an ARRAY must be extended in place, through
// xt.FindTableEntryForIndRef, because xt.Dereference hands back the table
// entry's own Object and assigning through a local variable after append
// (which may reallocate) does not reach back into the map.
//
// Mutating the array object in place is safe only because
// migration.copyContentsField (buildpages.go) guarantees this array is not
// shared with any other output page: two output page dicts naming the same
// source page would otherwise resolve /Contents to the identical migrated
// array object, and this function's in-place extension would apply both
// pages' wrappers to that one shared array.
func wrapIndirectContents(xt *model.XRefTable, pd types.Dict, ref types.IndirectRef, before, after types.IndirectRef) error {
	target, err := xt.Dereference(ref)
	if err != nil {
		return fmt.Errorf("dereference /Contents: %w", err)
	}
	switch t := target.(type) {
	case types.Array:
		entry, found := xt.FindTableEntryForIndRef(&ref)
		if !found || entry == nil {
			return fmt.Errorf("/Contents array has no cross-reference entry")
		}
		wrapped := make(types.Array, 0, len(t)+2)
		wrapped = append(wrapped, before)
		wrapped = append(wrapped, t...)
		wrapped = append(wrapped, after)
		entry.Object = wrapped
	case types.StreamDict:
		pd["Contents"] = types.Array{before, ref, after}
	default:
		return fmt.Errorf("/Contents indirect reference resolves to %T, not a stream or array", target)
	}
	return nil
}

// contentDepth returns the net q/Q nesting page n's content leaves behind.
//
// A page with no /Contents, or one that decodes to zero bytes (see
// verifyEmptyContent), leaves no nesting and returns (0, nil).
//
// Any imbalance -- a final depth that is not zero, OR a Q ever seen with no
// q left open to match it, even if later q's bring the running count back to
// zero -- returns ErrUnbalancedContent. The second case is why this cannot
// just check the final count: "Q q" ends net-zero but its leading Q pops
// whatever state was in effect before this stream ran, which is exactly the
// state WrapContent's own "before" wrapper would have just pushed.
func (d *doc) contentDepth(n int) (depth int, err error) {
	defer catchPanic(fmt.Sprintf("content depth on page %d", n), &err)
	xt := d.ctx.XRefTable

	pd, _, _, err := xt.PageDict(n, false)
	if err != nil {
		return 0, fmt.Errorf("byblos/pdfdoc: page %d dict: %w", n, err)
	}
	if pd == nil {
		return 0, fmt.Errorf("byblos/pdfdoc: page %d has no dictionary", n)
	}
	cur, _, err := d.pageContents(pd)
	if err != nil {
		return 0, fmt.Errorf("byblos/pdfdoc: page %d content: %w", n, err)
	}
	if len(cur) == 0 {
		return 0, nil
	}

	depth, unbalanced, lexErr := contentQDepth(cur)
	if lexErr != nil {
		// A stream this lexer cannot fully tokenize cannot be proven
		// balanced either -- the "unbalanced" reading it would otherwise
		// report is only the prefix read before the error, and a later
		// surplus Q past that point would pass silently. Refuse rather than
		// guess; a wrapper that only half-applies is worse than a refusal.
		return 0, fmt.Errorf("byblos/pdfdoc: page %d: content: %w", n, lexErr)
	}
	if unbalanced || depth != 0 {
		return depth, fmt.Errorf("byblos/pdfdoc: page %d: %w", n, ErrUnbalancedContent)
	}
	return depth, nil
}

// contentQDepth walks src the way netCTM does -- lexing with internal/content
// rather than splitting on whitespace, for the same reason netCTM gives: a
// naive split miscounts anything that looks like an operator inside a
// literal string or an inline image's binary payload. It answers a different
// question than netCTM (nesting, not the resulting matrix), so it is a
// second pass over the same lexer rather than a second lexer -- two lexers
// that disagree about what counts as a q or a Q is the bug this guards
// against.
//
// sawSurplusQ reports a Q seen while depth was already zero: that Q pops
// state this stream never pushed, and it is possible for depth to still
// read zero at the end (a Q followed by a q washes the count back out),
// which is why the caller must check both return values.
//
// A non-nil error means the lexer could not tokenize the whole stream (a
// stray ')' or '>', an unterminated string, ...); depth and sawSurplusQ then
// describe only the prefix read before the error, not the whole stream, and
// the caller must not treat that prefix as though it were the end.
func contentQDepth(src []byte) (depth int, sawSurplusQ bool, err error) {
	l := content.NewLexer(src)
	for {
		tok, lerr := l.Next()
		if lerr != nil {
			if errors.Is(lerr, io.EOF) {
				return depth, sawSurplusQ, nil
			}
			return depth, sawSurplusQ, lerr
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

// appendToIndirectContents handles /Contents as an indirect reference. ISO
// 32000-1 table 30 allows that reference to resolve to either a stream (the
// common case) or, just as legally, an array of streams -- and those are not
// interchangeable: wrapping an indirect reference to an ARRAY in a new outer
// array produces /Contents pointing at an array containing that array, which
// no reader accepts as page content, silently discarding everything on the
// page (original content and the new stamp both).
//
// Appending into the dereferenced array requires writing the extended array
// back into the xref table entry explicitly, not just returning it: xt.Dereference
// hands back the table entry's own Object, but assigning through a local
// variable after append (which may reallocate) does not reach back into the
// map the way *StreamDict field mutations do for a stream (see write.go's
// package comment on the identical trap for DereferenceStreamDict).
func appendToIndirectContents(xt *model.XRefTable, pd types.Dict, ref types.IndirectRef, add types.IndirectRef) error {
	target, err := xt.Dereference(ref)
	if err != nil {
		return fmt.Errorf("dereference /Contents: %w", err)
	}
	switch t := target.(type) {
	case types.Array:
		entry, found := xt.FindTableEntryForIndRef(&ref)
		if !found || entry == nil {
			return fmt.Errorf("/Contents array has no cross-reference entry")
		}
		entry.Object = append(t, add)
	case types.StreamDict:
		pd["Contents"] = types.Array{ref, add}
	default:
		return fmt.Errorf("/Contents indirect reference resolves to %T, not a stream or array", target)
	}
	return nil
}

// matrix is a PDF-style affine transform [a b c d e f]: [x' y' 1] = [x y 1] * M.
type matrix struct{ a, b, c, d, e, f float64 }

var identityMatrix = matrix{1, 0, 0, 1, 0, 0}

// concat returns the matrix that applies m first, then n (ISO 32000-1
// 8.3.4's concatenation formula: CTM_new = M x CTM_old, in that order).
func (m matrix) concat(n matrix) matrix {
	return matrix{
		a: m.a*n.a + m.b*n.c,
		b: m.a*n.b + m.b*n.d,
		c: m.c*n.a + m.d*n.c,
		d: m.c*n.b + m.d*n.d,
		e: m.e*n.a + m.f*n.c + n.e,
		f: m.e*n.b + m.f*n.d + n.f,
	}
}

// invert returns m's inverse, or ok=false if m is singular (determinant 0 --
// a degenerate scale-to-zero, which no matrix undoes).
func (m matrix) invert() (matrix, bool) {
	det := m.a*m.d - m.b*m.c
	if det == 0 {
		return matrix{}, false
	}
	inv := matrix{
		a: m.d / det,
		b: -m.b / det,
		c: -m.c / det,
		d: m.a / det,
	}
	inv.e = -(m.e*inv.a + m.f*inv.c)
	inv.f = -(m.e*inv.b + m.f*inv.d)
	return inv, true
}

// netCTM walks src the way a PDF interpreter would -- tracking q (push), Q
// (pop) and cm (concatenate) -- and returns the CTM in effect at the end,
// starting from identity. Operators other than those three never change the
// CTM (Do applies a form's Matrix only within the form's own execution; gs
// changes alpha/blend state, not the transform) and are ignored.
//
// It lexes with internal/content rather than splitting on whitespace, for
// the same reason the former qDepth did: a naive split miscounts anything
// that looks like an operator inside a literal string or an inline image's
// binary payload, which content.KindString / content.KindInlineImage already
// swallow whole.
func netCTM(src []byte) matrix {
	l := content.NewLexer(src)
	m := identityMatrix
	var stack []matrix
	var nums []float64
	for {
		tok, err := l.Next()
		if err != nil {
			return m
		}
		switch tok.Kind {
		case content.KindNumber:
			nums = append(nums, tok.Num)
		case content.KindKeyword:
			switch string(tok.Text) {
			case "cm":
				if len(nums) >= 6 {
					a := nums[len(nums)-6:]
					m = matrix{a[0], a[1], a[2], a[3], a[4], a[5]}.concat(m)
				}
			case "q":
				stack = append(stack, m)
			case "Q":
				if len(stack) > 0 {
					m = stack[len(stack)-1]
					stack = stack[:len(stack)-1]
				}
			}
			nums = nums[:0]
		default:
			nums = nums[:0]
		}
	}
}

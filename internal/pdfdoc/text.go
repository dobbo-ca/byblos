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

	ctm := identityMatrix
	if _, ok := pd.Find("Contents"); ok {
		cur, err := xt.PageContent(pd, n)
		switch {
		case errors.Is(err, model.ErrNoContent):
			// /Contents is present but decodes to zero bytes -- e.g. an
			// array of empty streams. That is legitimately empty content,
			// not a failure: identity is not a guess, it is the only CTM
			// zero bytes can leave. But pdfcpu reports a corrupt content
			// stream with the identical sentinel and the identical zero
			// bytes (see verifyEmptyContent), so confirm this page is
			// actually blank before trusting identity as its CTM.
			if err := d.verifyEmptyContent(pd["Contents"]); err != nil {
				return fmt.Errorf("byblos/pdfdoc: page %d content: %w", n, err)
			}
		case err != nil:
			return fmt.Errorf("byblos/pdfdoc: page %d content: %w", n, err)
		default:
			ctm = netCTM(cur)
		}
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

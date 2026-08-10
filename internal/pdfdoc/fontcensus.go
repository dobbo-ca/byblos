package pdfdoc

// Font-census reads. This file exists so cmd/byblos-fonts can classify font
// dicts without importing pdfcpu: design spec section 3 keeps pdfcpu behind
// this package's own types so that replacing it later is a swap rather than a
// rewrite, and TestOnlyPdfdocImportsPdfcpu enforces it.
//
// The split is deliberate. Everything here is a FACT about a font dictionary --
// what it says, and whether the file could be read at all. Nothing here decides
// what those facts MEAN; the class letters of the pdftotext scope doc's section
// 4.1 are policy and live in the command.

import (
	"fmt"
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// FontEncoding is what a font dict says about mapping character codes to
// glyphs. The three cases are distinguished because they need different amounts
// of work from a decoder, not because the PDF spec groups them this way.
type FontEncoding int

const (
	// EncodingAbsent is no /Encoding at all, or an encoding dict carrying
	// neither /Differences nor /BaseEncoding. Both need the font program's own
	// built-in encoding, so a dict with neither is no better than none.
	EncodingAbsent FontEncoding = iota
	// EncodingNamed is a bare name such as /WinAnsiEncoding, or an encoding
	// dict with a /BaseEncoding. Either way it is a table lookup.
	EncodingNamed
	// EncodingDifferences is an encoding dict carrying /Differences.
	EncodingDifferences
)

// FontFacts is what one /Type /Font dictionary says about itself.
//
// Subtype is reported verbatim, INCLUDING CIDFontType0 and CIDFontType2. Those
// descendants are never shown directly -- their Type0 parent is -- but whether
// to exclude them is a counting decision, so it belongs to the caller and not
// here.
type FontFacts struct {
	Subtype   string
	ToUnicode bool
	Encoding  FontEncoding
	// Symbolic is FontDescriptor /Flags bit 3 set AND bit 6 clear (ISO 32000-1
	// table 123; the spec numbers bits from 1, so those are 1<<2 and 1<<5). A
	// font claiming both is contradictory and is not reported as symbolic.
	Symbolic bool
}

const (
	flagSymbolic    = 1 << 2
	flagNonsymbolic = 1 << 5
)

// ErrFontCensusUnreadable reports a file whose font dicts could not be read
// trustworthily. It is deliberately distinct from "a file with no fonts":
// counting the first as the second is silent, because the totals still look
// plausible and only the per-file denominator goes wrong.
var ErrFontCensusUnreadable = fmt.Errorf("pdfdoc: font dicts are not readable")

// FontDicts returns the facts for every /Type /Font dict in the file's xref
// table, or ErrFontCensusUnreadable.
//
// It takes a path rather than an io.ReadSeeker, unlike the rest of this
// package, because the two reads it needs are api.ReadContextFile and
// api.ReadContext and only the first accepts a path. That asymmetry is the
// whole point of the function -- see readBothForFonts.
func FontDicts(path string) ([]FontFacts, error) {
	ctx, err := readBothForFonts(path)
	if err != nil {
		return nil, err
	}
	var out []FontFacts
	forEachFontDict(ctx, func(d types.Dict) {
		out = append(out, factsOf(ctx, d))
	})
	return out, nil
}

// readBothForFonts reads the file twice and keeps the better result.
//
// api.ReadContextFile expands object streams; api.ReadContext does NOT, so
// every font dict inside an ObjStm is invisible to it. Neither read is a
// superset of the other -- either can fail on a file the other handles -- so
// both are tried and the larger surviving font count wins. That single choice
// moved the scope doc's dc column from 1,981 dicts / 374 files to 5,408 / 516
// of 520.
func readBothForFonts(path string) (*model.Context, error) {
	var best *model.Context
	var bestN int
	var firstErr error

	if ctx, err := api.ReadContextFile(path); err == nil {
		if err := usableForFonts(ctx); err == nil {
			best, bestN = ctx, countFontDicts(ctx)
		} else {
			firstErr = err
		}
	} else {
		firstErr = err
	}

	expanded := best != nil // only the ObjStm-expanding read yields a trustworthy zero

	if f, err := os.Open(path); err == nil {
		defer f.Close()
		if ctx, err := api.ReadContext(f, model.NewDefaultConfiguration()); err == nil {
			if err := usableForFonts(ctx); err != nil {
				if firstErr == nil {
					firstErr = err
				}
			} else if n := countFontDicts(ctx); best == nil || n > bestN {
				best, bestN = ctx, n
			}
		} else if firstErr == nil {
			firstErr = err
		}
	}

	// When the expanding read failed, a zero from the fallback cannot be told
	// apart from "every font dict is inside an ObjStm the fallback cannot see"
	// -- but ONLY if the file actually uses object streams.
	//
	// Both halves are measured. Four dc files carry a non-standard /PageLayout
	// (PDLayoutDontCare) that fails ReadContextFile's validation; the fallback
	// reads them, resolves a page tree, and finds zero fonts, while poppler
	// finds 5, 9, 8 and 4. Drop the object-stream condition and the rule
	// over-fires the other way: govdocs1/350939.pdf uses no object streams at
	// all, poppler agrees it has zero fonts, and calling it unreadable files a
	// correctly-read file as a failure.
	if !expanded && best != nil && bestN == 0 && usesObjectStreams(best) {
		return nil, fmt.Errorf("%w: object streams are unreadable, so a zero-font result is not trustworthy: %v",
			ErrFontCensusUnreadable, firstErr)
	}

	if best == nil {
		if firstErr == nil {
			firstErr = fmt.Errorf("no read succeeded")
		}
		return nil, fmt.Errorf("%w: %v", ErrFontCensusUnreadable, firstErr)
	}
	return best, nil
}

// usableForFonts rejects a read that returned no error and no document.
//
// EnsurePageCount, not a bare ctx.PageCount test: PageCount is not populated by
// a plain read, so testing it directly rejects good files -- measured, it
// dropped 3 readable dc files and 3 of their fonts. Resolving the page tree is
// the actual question, namely whether this context can be walked at all.
func usableForFonts(ctx *model.Context) error {
	if ctx == nil || ctx.XRefTable == nil {
		return fmt.Errorf("read returned no xref table")
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return fmt.Errorf("page tree does not resolve: %w", err)
	}
	if ctx.PageCount < 1 {
		return fmt.Errorf("read returned a context with 0 pages")
	}
	return nil
}

// usesObjectStreams reads pdfcpu's own record rather than inferring one.
//
// Walking the xref for a /Type /ObjStm dict does NOT work and silently returns
// false every time: the non-expanding read never materialises those objects,
// which is the same invisibility this whole file exists to correct.
func usesObjectStreams(ctx *model.Context) bool {
	return ctx != nil && ctx.Read != nil && ctx.Read.UsingObjectStreams
}

// countFontDicts is the tie-break between the two reads: larger in the quantity
// actually being reported, not in some other measure of the file.
func countFontDicts(ctx *model.Context) int {
	n := 0
	forEachFontDict(ctx, func(types.Dict) { n++ })
	return n
}

func forEachFontDict(ctx *model.Context, fn func(types.Dict)) {
	if ctx == nil || ctx.XRefTable == nil {
		return
	}
	for i := range ctx.XRefTable.Table {
		e := ctx.XRefTable.Table[i]
		if e == nil || e.Object == nil {
			continue
		}
		d, ok := e.Object.(types.Dict)
		if !ok {
			continue
		}
		if n := d.NameEntry("Type"); n == nil || *n != "Font" {
			continue
		}
		fn(d)
	}
}

func factsOf(ctx *model.Context, d types.Dict) FontFacts {
	f := FontFacts{Encoding: EncodingAbsent}
	if n := d.NameEntry("Subtype"); n != nil {
		f.Subtype = string(*n)
	}
	_, f.ToUnicode = d["ToUnicode"]

	if encObj, ok := d["Encoding"]; ok {
		if ed, err := ctx.DereferenceDict(encObj); err == nil && ed != nil {
			switch {
			case hasKey(ed, "Differences"):
				f.Encoding = EncodingDifferences
			case hasKey(ed, "BaseEncoding"):
				f.Encoding = EncodingNamed
			}
		} else if o, err := ctx.Dereference(encObj); err == nil {
			if _, isName := o.(types.Name); isName {
				f.Encoding = EncodingNamed
			}
		}
	}

	if fd, err := ctx.DereferenceDict(d["FontDescriptor"]); err == nil && fd != nil {
		if flags := fd.IntEntry("Flags"); flags != nil {
			f.Symbolic = *flags&flagSymbolic != 0 && *flags&flagNonsymbolic == 0
		}
	}
	return f
}

func hasKey(d types.Dict, k string) bool {
	_, ok := d[k]
	return ok
}

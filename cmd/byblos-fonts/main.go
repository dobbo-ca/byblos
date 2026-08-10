// Command byblos-fonts classifies every /Type /Font dict in a corpus by what a
// code-to-Unicode decoder would have to do with it.
//
// It makes the pdftotext scope doc's section 4.1 table reproducible. Those
// numbers came from a throwaway program that no longer exists, and section 4.1a
// is what that cost: the dc column was wrong and could not be reconciled,
// because the probe was gone. byb-lez.8 makes it permanent.
//
// THE DEFECT THAT MADE 4.1 WRONG, encoded here so it cannot come back:
// pdfcpu's api.ReadContext builds the xref table but does NOT decompress object
// streams, so every font dict inside an ObjStm is invisible to it. This tool
// reads each file TWICE -- api.ReadContextFile, which expands object streams,
// and api.ReadContext, which does not -- and keeps whichever read succeeded,
// preferring the larger count when both did. Neither read is a superset of the
// other, so this is the correction, not belt-and-braces. That single choice
// moved dc from 1,981 dicts / 374 files to 5,408 / 516 of 520.
//
// WHY govdocs1 VOUCHED FOR A BROKEN dc COLUMN: govdocs1 is classic-xref
// Distiller output almost end to end, so it moved only 137 dicts (0.27%) when
// the read path was fixed. The corpus that looked right hid the corpus that was
// wrong. dc is the load-bearing half of any check of this tool.
//
// A file no reader could open is REPORTED, never counted as a file with zero
// fonts. That silent zero is the whole of section 4.1a's defect 2.
//
//	byblos-fonts [-jsonl out.jsonl] [-pdffonts] <dir>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// The seven classes of section 4.1, in the table's order. The letters are the
// scope doc's and are quoted in prose all over byb-lez; do not renumber them.
const (
	classA = "A" // Type0 with /ToUnicode
	classB = "B" // simple font with /ToUnicode
	classC = "C" // simple, /Differences, no /ToUnicode
	classD = "D" // simple, named or absent encoding, no /ToUnicode, non-symbolic
	classE = "E" // simple, symbolic, no /ToUnicode
	classF = "F" // Type0, no /ToUnicode
	classG = "G" // Type3
)

var classOrder = []string{classA, classB, classC, classD, classE, classF, classG}

// FontDescriptor /Flags bits (ISO 32000-1 table 123). Bit numbering in the spec
// is 1-based, so bit 3 is 1<<2 and bit 6 is 1<<5.
const (
	flagSymbolic    = 1 << 2
	flagNonsymbolic = 1 << 5
)

type fileRow struct {
	File     string         `json:"file"`
	Fonts    int            `json:"fonts"`
	Classes  map[string]int `json:"classes"`
	DNamed   int            `json:"d_named"`
	DAbsent  int            `json:"d_absent"`
	ReadErr  string         `json:"read_err,omitempty"`
	PdfFonts int            `json:"pdffonts,omitempty"`
}

type summary struct {
	Files       int
	FilesWith   int // files with at least one shown font dict
	Unreadable  int // files NO read could open; never counted as zero fonts
	Class       map[string]int
	DNamed      int
	DAbsent     int
	PdfFontsSum int // only when -pdffonts ran
	CensusOnCmp int // census total over the same files pdffonts could read
	CmpFiles    int
}

func main() {
	jsonl := flag.String("jsonl", "", "write a per-file JSON line to this path")
	cmp := flag.Bool("pdffonts", false, "also run pdffonts per file and report the differential")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: byblos-fonts [-jsonl out.jsonl] [-pdffonts] <dir>")
		os.Exit(2)
	}
	root := flag.Arg(0)

	var enc *json.Encoder
	if *jsonl != "" {
		f, err := os.Create(*jsonl)
		if err != nil {
			fmt.Fprintln(os.Stderr, "byblos-fonts:", err)
			os.Exit(1)
		}
		defer f.Close()
		enc = json.NewEncoder(f)
	}

	s := summary{Class: map[string]int{}}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".pdf") {
			return nil
		}
		scanFile(path, root, &s, enc, *cmp)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "byblos-fonts:", err)
		os.Exit(1)
	}
	report(s, *cmp)
}

// readBoth is the correction of section 4.1a. api.ReadContextFile expands
// object streams; api.ReadContext does not. Either can fail on a file the other
// reads, so both are tried and the larger surviving count wins.
//
// It returns a nil context ONLY when both reads failed, which is what makes
// "unreadable" distinguishable from "zero fonts".
func readBoth(path string) (*model.Context, error) {
	var best *model.Context
	var bestN int
	var firstErr error

	if ctx, err := api.ReadContextFile(path); err == nil {
		if err := usable(ctx); err == nil {
			best, bestN = ctx, countFontDicts(ctx)
		} else {
			firstErr = err
		}
	} else {
		firstErr = err
	}

	expanded := best != nil // the ObjStm-expanding read is the only trustworthy zero

	if f, err := os.Open(path); err == nil {
		defer f.Close()
		if ctx, err := api.ReadContext(f, model.NewDefaultConfiguration()); err == nil {
			if err := usable(ctx); err != nil {
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

	// CLAUSE (b). api.ReadContext does not decompress object streams, so when
	// the expanding read failed, a zero from the fallback cannot be told apart
	// from "every font dict is inside an ObjStm the fallback cannot see".
	//
	// Measured on dc: four files carry a non-standard /PageLayout
	// (PDLayoutDontCare) that fails api.ReadContextFile's validation. The
	// fallback reads them, resolves a page tree, and finds ZERO fonts --
	// while poppler finds 5, 9, 8 and 4. Counting them as zero-font files is
	// section 4.1a's defect 2, and it is silent: the class totals stay right,
	// only the per-file denominator goes wrong.
	//
	// A zero from a file the expanding read HANDLED is a real zero and is kept.
	if !expanded && best != nil && bestN == 0 && hasObjectStreams(best) {
		if firstErr == nil {
			firstErr = fmt.Errorf("no read succeeded")
		}
		return nil, fmt.Errorf("object streams unreadable, so a zero-font result is not trustworthy: %w", firstErr)
	}

	if best == nil {
		if firstErr == nil {
			firstErr = fmt.Errorf("no read succeeded")
		}
		return nil, firstErr
	}
	return best, nil
}

// hasObjectStreams reports whether the file stores objects inside an ObjStm.
//
// It is the missing half of clause (b), and leaving it out over-flags. A zero
// from the non-expanding read is only untrustworthy when there is somewhere for
// font dicts to hide. Measured, with poppler as the independent check:
//
//	dc's four /PageLayout files  ObjStm present, pdffonts 5/9/8/4 -> unreadable
//	govdocs1 350939.pdf          NO ObjStm,      pdffonts 0       -> a real zero
//
// Without this condition 350939.pdf is reported unreadable, which is the same
// error as defect 2 pointing the other way: a file byblos read correctly, filed
// as one it could not read.
// It reads pdfcpu's own record rather than inferring one. Walking the xref for
// a /Type /ObjStm dict does NOT work: the non-expanding read never materialises
// those objects, so the scan finds nothing and the condition is always false.
// ReadContext sets UsingObjectStreams while parsing the xref, which is the fact
// itself rather than a proxy for it.
func hasObjectStreams(ctx *model.Context) bool {
	return ctx != nil && ctx.Read != nil && ctx.Read.UsingObjectStreams
}

// usable rejects a read that returned no error and no document.
//
// THIS IS CLAUSE (b) AND IT IS NOT DEFENSIVE PADDING. Four dc files carry a
// non-standard /PageLayout value (PDLayoutDontCare): api.ReadContextFile fails
// validation outright, and api.ReadContext returns err == nil with an xref of
// 55-87 objects, ZERO pages, and no reachable font dict. Without this check the
// census reports those files as read successfully with zero fonts, while
// poppler reads all four and finds 5, 9, 8 and 4 fonts. That is section 4.1a's
// defect 2 exactly: a silent zero that leaves the totals looking plausible.
//
// Zero pages is the pin. A PDF always has at least one page, so a context
// claiming none did not parse -- it is a failed read wearing a nil error.
func usable(ctx *model.Context) error {
	if ctx == nil || ctx.XRefTable == nil {
		return fmt.Errorf("read returned no xref table")
	}
	// PageCount is NOT populated by a bare read, so testing it directly rejects
	// good files -- measured: it dropped 3 readable dc files and 3 class-A
	// fonts. EnsurePageCount resolves the page tree and is the actual question:
	// can this context be walked at all?
	if err := ctx.EnsurePageCount(); err != nil {
		return fmt.Errorf("page tree does not resolve: %w", err)
	}
	if ctx.PageCount < 1 {
		return fmt.Errorf("read returned a context with 0 pages")
	}
	return nil
}

// countFontDicts is the tie-break between the two reads. It counts what
// classify would count, so "prefer the larger count" means larger in the
// quantity this tool reports, not in some other measure of the file.
func countFontDicts(ctx *model.Context) int {
	n := 0
	forEachFont(ctx, func(types.Dict) { n++ })
	return n
}

// forEachFont visits every /Type /Font dict in the xref table that a decoder
// would actually be shown.
//
// CIDFontType0 and CIDFontType2 descendants are EXCLUDED: they are never shown
// directly, their Type0 parent is, and counting both would double-count every
// composite font. Section 4.1 states this exclusion and the table depends on it.
func forEachFont(ctx *model.Context, fn func(types.Dict)) {
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
		if d.NameEntry("Type") == nil || *d.NameEntry("Type") != "Font" {
			continue
		}
		sub := ""
		if n := d.NameEntry("Subtype"); n != nil {
			sub = string(*n)
		}
		if sub == "CIDFontType0" || sub == "CIDFontType2" {
			continue
		}
		fn(d)
	}
}

// classify implements section 4.1's precedence exactly, and the order is
// load-bearing: a symbolic font that ALSO carries /Differences counts as C, not
// E, because the /Differences test runs first.
func classify(ctx *model.Context, d types.Dict) (class string, dNamed bool) {
	sub := ""
	if n := d.NameEntry("Subtype"); n != nil {
		sub = string(*n)
	}
	if sub == "Type3" {
		return classG, false
	}
	_, hasToUni := d["ToUnicode"]
	if sub == "Type0" {
		if hasToUni {
			return classA, false
		}
		return classF, false
	}
	// Simple font from here down.
	if hasToUni {
		return classB, false
	}
	encObj, hasEnc := d["Encoding"]
	if hasEnc {
		if ed, err := ctx.DereferenceDict(encObj); err == nil && ed != nil {
			if _, hasDiff := ed["Differences"]; hasDiff {
				return classC, false
			}
			// An encoding dict with a /BaseEncoding is still a table lookup, so
			// it counts as "named" for the D split.
			if _, hasBase := ed["BaseEncoding"]; hasBase {
				return classD, true
			}
			// A dict with neither is no better than no encoding at all.
			return dOrE(ctx, d, false)
		}
		// A bare name: /WinAnsiEncoding and friends.
		if o, err := ctx.Dereference(encObj); err == nil {
			if _, isName := o.(types.Name); isName {
				return dOrE(ctx, d, true)
			}
		}
	}
	return dOrE(ctx, d, false)
}

// dOrE applies the last precedence step: symbolic goes to E, everything else to
// D. Symbolic means FontDescriptor /Flags bit 3 set AND bit 6 clear -- a font
// claiming both is contradictory and section 4.1 treats it as non-symbolic.
func dOrE(ctx *model.Context, d types.Dict, named bool) (string, bool) {
	fd, err := ctx.DereferenceDict(d["FontDescriptor"])
	if err == nil && fd != nil {
		if f := fd.IntEntry("Flags"); f != nil {
			if *f&flagSymbolic != 0 && *f&flagNonsymbolic == 0 {
				return classE, false
			}
		}
	}
	return classD, named
}

func scanFile(path, root string, s *summary, enc *json.Encoder, cmp bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	s.Files++

	row := fileRow{File: rel, Classes: map[string]int{}}
	ctx, err := readBoth(path)
	if err != nil {
		// REPORTED, never counted as a file with zero fonts. Adding it to the
		// class tallies as a zero is section 4.1a's defect 2, and it is silent:
		// the totals still look plausible.
		s.Unreadable++
		row.ReadErr = err.Error()
		// pdffonts still runs here, and it must. A file the census could not
		// read is exactly where poppler's count matters most -- dc's four
		// /PageLayout files carry 26 fonts between them, and leaving them out
		// of the denominator reports the gap as +3.01% instead of the +2.50%
		// section 4.1 states. The differential is census-vs-poppler over the
		// WHOLE corpus, not over the subset the census happened to manage.
		if cmp {
			if n, ok := pdffontsCount(path); ok {
				row.PdfFonts = n
				s.PdfFontsSum += n
				s.CmpFiles++
			}
		}
		emit(enc, row)
		return
	}

	forEachFont(ctx, func(d types.Dict) {
		c, named := classify(ctx, d)
		row.Fonts++
		row.Classes[c]++
		s.Class[c]++
		if c == classD {
			if named {
				row.DNamed++
				s.DNamed++
			} else {
				row.DAbsent++
				s.DAbsent++
			}
		}
	})
	if row.Fonts > 0 {
		s.FilesWith++
	}

	if cmp {
		if n, ok := pdffontsCount(path); ok {
			row.PdfFonts = n
			s.PdfFontsSum += n
			s.CensusOnCmp += row.Fonts
			s.CmpFiles++
		}
	}
	emit(enc, row)
}

// pdffontsCount is the independent definition: poppler's page-resource walk,
// which is what caught the object-stream defect in the first place.
//
// It counts a DIFFERENT thing on purpose, and the gap has an expected
// direction. pdffonts lists fonts REACHABLE from a page's resources; this tool
// counts every font dict in the xref table. A font dict that no page references
// is real and unreferenced, so the census is a SUPERSET -- measured 2.50% high
// on dc and 5.39% high on govdocs1. A census total BELOW pdffonts means the
// read path is losing dicts again, which is the regression this mode exists to
// catch.
func pdffontsCount(path string) (int, bool) {
	bin, err := exec.LookPath("pdffonts")
	if err != nil {
		return 0, false
	}
	out, err := exec.Command(bin, path).Output()
	if err != nil {
		return 0, false
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 2 {
		return 0, true // header only: a file with no fonts
	}
	n := 0
	for _, l := range lines[2:] {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n, true
}

// pdffontsVersion reads the banner. CombinedOutput because every poppler tool
// prints -v to STDERR, so Output() would return an empty string -- the same
// trap testdata/oracle/gen.go records.
func pdffontsVersion() string {
	bin, err := exec.LookPath("pdffonts")
	if err != nil {
		return "absent"
	}
	out, _ := exec.Command(bin, "-v").CombinedOutput()
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "pdffonts version") {
			return strings.TrimSpace(strings.TrimPrefix(l, "pdffonts version"))
		}
	}
	return "unknown"
}

func emit(enc *json.Encoder, row fileRow) {
	if enc != nil {
		_ = enc.Encode(row)
	}
}

func report(s summary, cmp bool) {
	total := 0
	for _, c := range classOrder {
		total += s.Class[c]
	}
	labels := map[string]string{
		classA: "A Type0 with /ToUnicode",
		classB: "B simple font with /ToUnicode",
		classC: "C simple, /Differences, no /ToUnicode",
		classD: "D simple, named or absent encoding, no /ToUnicode, non-symbolic",
		classE: "E simple, symbolic, no /ToUnicode",
		classF: "F Type0, no /ToUnicode",
		classG: "G Type3",
	}
	for _, c := range classOrder {
		fmt.Printf("%-64s %8d\n", labels[c], s.Class[c])
	}
	fmt.Printf("%-64s %8d\n", "shown font dicts", total)
	fmt.Printf("%-64s %8d\n", "files in corpus", s.Files)
	fmt.Printf("%-64s %8d\n", "files with >=1 shown font", s.FilesWith)
	fmt.Printf("%-64s %8d\n", "files no reader could open", s.Unreadable)

	// Class D is two populations and Tier 1 can only serve one. A named
	// encoding is a table lookup; an absent one needs the font program's own
	// built-in encoding, which the scope doc files under "research projects".
	fmt.Println()
	fmt.Printf("%-64s %8d\n", "D named (table lookup)", s.DNamed)
	fmt.Printf("%-64s %8d\n", "D absent (needs the font program's own encoding)", s.DAbsent)
	if total > 0 {
		fmt.Printf("%-64s %7.2f%%\n", "D absent, as % of shown font dicts", 100*float64(s.DAbsent)/float64(total))
		tier1 := s.Class[classA] + s.Class[classB] + s.Class[classC] + s.Class[classD]
		fmt.Printf("%-64s %7.2f%%\n", "Tier 1 ceiling (A+B+C+D)", 100*float64(tier1)/float64(total))
		fmt.Printf("%-64s %7.2f%%\n", "Tier 1 less D-absent", 100*float64(tier1-s.DAbsent)/float64(total))
	}

	if cmp {
		fmt.Println()
		if s.CmpFiles == 0 {
			fmt.Println("pdffonts differential: pdffonts not on PATH or read nothing")
			return
		}
		// Stamp the tool, as testdata/oracle/gen.go does. The gap is a
		// diagnostic, not an invariant: it is a comparison against a
		// third-party binary, so its exact value moves with that binary's
		// version. Measured with poppler 26.06.0 the gap is +2.50% on dc --
		// section 4.1's figure exactly -- and +5.35% on govdocs1 against the
		// 5.39% recorded there, a difference of 16 font rows in 48,582
		// (0.03%). What clause (c) actually pins is the DIRECTION.
		fmt.Printf("%-64s %8s\n", "pdffonts version", pdffontsVersion())
		fmt.Printf("%-64s %8d\n", "pdffonts total, whole corpus", s.PdfFontsSum)
		fmt.Printf("%-64s %8d\n", "census total, whole corpus", s.CensusOnCmp)
		if s.PdfFontsSum > 0 {
			gap := 100 * float64(s.CensusOnCmp-s.PdfFontsSum) / float64(s.PdfFontsSum)
			fmt.Printf("%-64s %+7.2f%%\n", "census minus pdffonts (expected POSITIVE)", gap)
			if gap < 0 {
				fmt.Println("WARNING: census is BELOW pdffonts. The read path is losing font")
				fmt.Println("dicts -- check that object streams are still being expanded.")
			}
		}
	}
}

// sortedKeys keeps any future histogram output stable.
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var _ = sortedKeys
var _ io.Writer

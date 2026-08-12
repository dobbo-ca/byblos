// Command byblos-fonts classifies every /Type /Font dict in a corpus by what a
// code-to-Unicode decoder would have to do with it.
//
// It makes the pdftotext scope doc's section 4.1 table reproducible. Those
// numbers came from a throwaway program that no longer exists, and section 4.1a
// is what that cost: the dc column was wrong and could not be reconciled,
// because the probe was gone. byb-lez.8 makes it permanent.
//
// The pdfcpu mechanics live in internal/pdfdoc, which design spec section 3
// makes the only package allowed to import pdfcpu. What lives HERE is section
// 4.1's policy: which class each combination of font facts falls into, and the
// precedence that decides the ambiguous cases.
//
// WHY govdocs1 VOUCHED FOR A BROKEN dc COLUMN, and why dc is the load-bearing
// half of any check of this tool: govdocs1 is classic-xref Distiller output
// almost end to end, so it moved only 137 dicts (0.27%) when the read path was
// fixed. The corpus that looked right hid the corpus that was wrong.
//
// A file whose fonts could not be read trustworthily is REPORTED, never counted
// as a file with zero fonts. That silent zero is the whole of section 4.1a's
// defect 2.
//
//	byblos-fonts [-jsonl out.jsonl] [-pdffonts] <dir>
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
	"github.com/dobbo-ca/byblos/internal/sample"
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
	Unreadable  int // files whose fonts could not be read; never counted as zero
	Class       map[string]int
	DNamed      int
	DAbsent     int
	PdfFontsSum int
	CensusOnCmp int
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

	// internal/sample owns which files are candidates, so this tool and the
	// divert sweep cannot disagree about the population before either opens
	// anything. It also refuses a directory it cannot read rather than reporting
	// a smaller corpus (byb-wj2).
	s := summary{Class: map[string]int{}}
	paths, err := sample.Paths(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "byblos-fonts:", err)
		os.Exit(1)
	}
	for _, path := range paths {
		scanFile(path, root, &s, enc, *cmp)
	}
	report(s, *cmp)
}

// shown reports whether a decoder would ever be handed this font directly.
//
// CIDFontType0 and CIDFontType2 descendants are NOT: their Type0 parent is, and
// counting both would double every composite font. Section 4.1 states this
// exclusion and the published table depends on it.
func shown(f pdfdoc.FontFacts) bool {
	return f.Subtype != "CIDFontType0" && f.Subtype != "CIDFontType2"
}

// classify implements section 4.1's precedence exactly, and the order is
// load-bearing: a symbolic font that ALSO carries /Differences counts as C, not
// E, because the /Differences test runs first.
//
// The second return is meaningful only for class D, which is two populations. A
// named encoding is a table lookup; an absent one needs the font program's own
// built-in encoding, which the scope doc files under "research projects, do not
// estimate". That split is what moves the Tier-1 headline from 95% to 92%.
func classify(f pdfdoc.FontFacts) (class string, dNamed bool) {
	if f.Subtype == "Type3" {
		return classG, false
	}
	if f.Subtype == "Type0" {
		if f.ToUnicode {
			return classA, false
		}
		return classF, false
	}
	if f.ToUnicode {
		return classB, false
	}
	if f.Encoding == pdfdoc.EncodingDifferences {
		return classC, false
	}
	if f.Symbolic {
		return classE, false
	}
	return classD, f.Encoding == pdfdoc.EncodingNamed
}

func scanFile(path, root string, s *summary, enc *json.Encoder, cmp bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	s.Files++

	row := fileRow{File: rel, Classes: map[string]int{}}
	fonts, err := pdfdoc.FontDicts(path)
	if err != nil {
		s.Unreadable++
		row.ReadErr = err.Error()
		// pdffonts still runs here, and it must. A file the census could not
		// read is exactly where poppler's count matters most -- dc's four
		// /PageLayout files carry 26 fonts between them, and leaving them out
		// of the denominator reports the gap as +3.01% instead of the +2.50%
		// section 4.1 states.
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

	for _, f := range fonts {
		if !shown(f) {
			continue
		}
		c, named := classify(f)
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
	}
	if row.Fonts > 0 {
		s.FilesWith++
	}
	s.CensusOnCmp += row.Fonts

	if cmp {
		if n, ok := pdffontsCount(path); ok {
			row.PdfFonts = n
			s.PdfFontsSum += n
			s.CmpFiles++
		}
	}
	emit(enc, row)
}

// pdffontsCount is the independent definition: poppler's page-resource walk,
// which is what caught the object-stream defect in the first place.
//
// It counts a DIFFERENT thing on purpose, and the gap has an expected
// direction. pdffonts lists fonts reachable from a page's resources; this tool
// counts every font dict in the xref table. A font dict no page references is
// real and unreferenced, so the census is a SUPERSET. A census total BELOW
// pdffonts means the read path is losing dicts again, which is the regression
// this mode exists to catch.
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
		// diagnostic, not an invariant: it compares against a third-party
		// binary, so its exact value moves with that binary's version. With
		// poppler 26.06.0 it is +2.50% on dc -- section 4.1's figure exactly --
		// and +5.35% on govdocs1 against the 5.39% recorded there, a difference
		// of 16 font rows in 48,582 (0.03%). Clause (c) pins the DIRECTION.
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

// Command byblos-encrypt-census measures the population that a refusal at
// pdfdoc.Open (option (a)) would break, not merely the population that carries
// an /Encrypt dictionary.
//
// Every document under the walked root lands in exactly one bucket:
//
//  1. no /Encrypt.
//  2. /Encrypt present, and pdfdoc.Open (== api.ReadContext with empty
//     UserPW/OwnerPW) succeeds anyway -- the dangerous shape: an
//     owner-password-only document, or one with an empty user password.
//     Byblos reads it today exactly as if it were unencrypted, because
//     nothing checks ctx.XRefTable.Encrypt after a successful Open.
//  3. /Encrypt present, and Open refuses it: pdfcpu could not validate either
//     password against an empty candidate. Byblos never sees these; a
//     refusal at Open would change nothing for them. Told apart from bucket 4
//     by errors.Is against pdfcpu's own pkg/pdfcpu.ErrWrongPassword sentinel
//     (read.go:48), not by matching the error's string: on every error
//     pdfcpu actually produces, a string match on wrongPasswordText and this
//     sentinel check land the same document in the same bucket, so this is
//     not a live behaviour difference today. It is still the right check --
//     it classifies by what the error IS rather than by what its message
//     happens to say, so a future pdfcpu wording change cannot silently
//     misfile a real wrong-password refusal into bucket 4.
//  4. Open fails for any other reason: a genuinely malformed file, a missing
//     trailer, an encryption scheme pdfcpu does not support (an unsupported
//     /V is real and reachable -- it is not "malformed", just unsupported),
//     .... The pinned sample has exactly one such document
//     (internal/sample/pinned.go), and it must not be silently folded into
//     bucket 3.
//
// Bucket 2 is the number that decides between refusing at Open (breaks every
// read-only caller on these documents, which byblos handles correctly today)
// and refusing only at each write seam. For bucket 2 alone, this tool also
// measures whether byblos.Inspect succeeds and the encryption dictionary's
// /P, /V and /R.
//
// It also counts how many pages ExtractPageRaster returns a raster for
// (row.Rasters), but that count alone does NOT show whether encryption broke
// the read path: ExtractPageRaster also declines for entirely ordinary
// reasons (ErrNotSingleRaster -- no image on the page, vector paint, more
// than one image, ...) that have nothing to do with /Encrypt, and this tool
// records no reason and no bucket-1 (unencrypted) control to tell the two
// apart. Read Rasters as "pages ExtractPageRaster returned a raster for", not
// as evidence about encryption specifically.
//
// In SWEEP mode (-jsonl over a directory), the population (Files/Documents/
// Unopenable/Pages) is internal/sample's own count from the walk. In AGGREGATE
// mode (-aggregate over a previously written JSONL file) there is no walk to
// ask, so it is re-derived from the rows instead -- see aggregateFile. Both
// use the SAME definition of Pages (a document's PageCount, 0 if unopenable),
// so the two modes agree byte-for-byte on the same JSONL; see byb-wj2 in
// cmd/byblos-divert for the bug shape this guards against.
//
//	byblos-encrypt-census [-j N] -jsonl out.jsonl <dir>
//	byblos-encrypt-census -aggregate out.jsonl
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/dobbo-ca/byblos"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
	"github.com/dobbo-ca/byblos/internal/sample"
)

const defaultJobs = 4
const jobsEnv = "BYBLOS_JOBS"

func jobs(flagVal int) int {
	if flagVal > 0 {
		return flagVal
	}
	if n, err := strconv.Atoi(os.Getenv(jobsEnv)); err == nil && n > 0 {
		return n
	}
	return defaultJobs
}

// wrongPasswordText is pdfcpu's own error string (pkg/pdfcpu/read.go:48), for
// tests only -- proving TestClassifyUsesErrorsIsNotStringMatching's synthetic
// error actually contains it, while still failing to wrap the sentinel. The
// bucket 3/4 split itself uses pdfdoc.IsWrongPassword (errors.Is against
// pdfcpu.ErrWrongPassword; see classify), not this text.
const wrongPasswordText = "please provide the correct password"

// row is one document's line of the JSONL output.
type row struct {
	Path   string `json:"path"`
	Bucket int    `json:"bucket"`
	Err    string `json:"err,omitempty"`

	// Pages is set for every document that opened (buckets 1 and 2): this
	// row's contribution to Population.Pages (see classify). Nil for buckets
	// 3/4, which contribute 0, same as the walk.
	Pages *int `json:"pages,omitempty"`

	// Bucket 2 only.
	P         *int  `json:"p,omitempty"`
	V         *int  `json:"v,omitempty"`
	R         *int  `json:"r,omitempty"`
	InspectOK *bool `json:"inspect_ok,omitempty"`
	Rasters   *int  `json:"rasters,omitempty"` // pages ExtractPageRaster returned a raster for
}

// summary is the aggregate this tool reports, whether freshly walked or
// reloaded from a JSONL file with -aggregate.
type summary struct {
	Files, Documents, Unopenable, Pages int
	Bucket1, Bucket2, Bucket3, Bucket4  int
	Bucket2InspectOK                    int
	Bucket2InspectFail                  int
}

func main() {
	aggregate := flag.String("aggregate", "", "recompute the summary from a previously written JSONL file; no walk")
	jsonlPath := flag.String("jsonl", "", "write one JSON line per document here")
	j := flag.Int("j", 0, "files to sweep in parallel (default "+
		strconv.Itoa(defaultJobs)+", or $"+jobsEnv+")")
	flag.Parse()

	if *aggregate != "" {
		if flag.NArg() != 0 {
			fmt.Fprintln(os.Stderr, "usage: byblos-encrypt-census -aggregate out.jsonl")
			os.Exit(2)
		}
		s, err := aggregateFile(*aggregate)
		if err != nil {
			fmt.Fprintln(os.Stderr, "byblos-encrypt-census:", err)
			os.Exit(1)
		}
		report(s)
		return
	}

	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: byblos-encrypt-census [-j N] -jsonl out.jsonl <dir>")
		os.Exit(2)
	}

	var jsonl io.WriteCloser = nopCloser{io.Discard}
	if *jsonlPath != "" {
		f, err := os.Create(*jsonlPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "byblos-encrypt-census:", err)
			os.Exit(1)
		}
		jsonl = f
	}
	defer jsonl.Close()

	s, err := sweep(flag.Arg(0), jobs(*j), jsonl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "byblos-encrypt-census:", err)
		os.Exit(1)
	}
	report(s)
}

// sweep walks root and classifies every document, writing one JSON line per
// document to jsonl in lexical path order (matching sample.Doc.Index, the same
// discipline cmd/byblos-divert uses so two runs stay diffable).
func sweep(root string, workers int, jsonl io.Writer) (summary, error) {
	paths, err := sample.Paths(root)
	if err != nil {
		return summary{}, err
	}
	rows := make([]row, len(paths))

	pop, err := sample.Walk(root, workers, func(d sample.Doc) {
		rows[d.Index] = classify(d)
	})
	if err != nil {
		return summary{}, err
	}

	s := summary{Files: pop.Files, Documents: pop.Documents, Unopenable: pop.Unopenable, Pages: pop.Pages}
	enc := json.NewEncoder(jsonl)
	for _, r := range rows {
		tally(&s, r)
		_ = enc.Encode(r)
	}
	return s, nil
}

// classify puts one document into its bucket, off the SAME pdfdoc.Doc
// sample.Walk already opened -- pdfdoc.Doc.EncryptInfo answers the /Encrypt
// fact directly (encryptinfo.go), so there is no second read of the source.
func classify(d sample.Doc) row {
	r := row{Path: d.Path}
	if d.Err != nil {
		r.Err = d.Err.Error()
		if pdfdoc.IsWrongPassword(d.Err) {
			r.Bucket = 3
		} else {
			r.Bucket = 4
		}
		return r
	}

	// d.Pages is this document's own contribution to Population.Pages (see
	// internal/sample.Doc.Pages) -- the SAME definition sweep's summary uses,
	// so a row's Pages and a walk's Pages never disagree (byb-wj2).
	pages := d.Pages
	r.Pages = &pages

	info := d.Doc.EncryptInfo()
	if !info.Encrypted {
		r.Bucket = 1
		return r
	}

	r.Bucket = 2
	r.P, r.V, r.R = &info.P, &info.V, &info.R
	measureBucket2(d, &r)
	return r
}

// measureBucket2 is the sub-split that decides (a) vs (b): of the documents
// byblos opens despite carrying /Encrypt, does its read path actually work?
func measureBucket2(d sample.Doc, r *row) {
	if _, err := d.File.Seek(0, 0); err != nil {
		ok := false
		r.InspectOK = &ok
		return
	}
	infos, err := byblos.Inspect(d.File)
	ok := err == nil
	r.InspectOK = &ok
	if err != nil {
		return
	}

	rasters := 0
	for _, pi := range infos {
		if _, err := d.File.Seek(0, 0); err != nil {
			break
		}
		if _, err := byblos.ExtractPageRaster(d.File, pi.Index); err == nil {
			rasters++
		}
	}
	r.Rasters = &rasters
}

func tally(s *summary, r row) {
	switch r.Bucket {
	case 1:
		s.Bucket1++
	case 2:
		s.Bucket2++
		if r.InspectOK != nil && *r.InspectOK {
			s.Bucket2InspectOK++
		} else {
			s.Bucket2InspectFail++
		}
	case 3:
		s.Bucket3++
	case 4:
		s.Bucket4++
	}
}

// aggregateFile recomputes the summary from a JSONL file this tool already
// wrote, without opening a single PDF. There is no walk to ask, so
// Files/Documents/Unopenable/Pages are all re-derived from the rows
// themselves: Files is the row count, Unopenable is buckets 3+4, Documents is
// the rest, and Pages sums row.Pages -- which classify sets to the SAME
// definition sweep's summary uses (this document's contribution to
// Population.Pages), so this total agrees with a fresh sweep's byte-for-byte
// (byb-wj2 was exactly two definitions sharing one label; there is only one
// definition now).
func aggregateFile(path string) (summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return summary{}, err
	}
	defer f.Close()

	var s summary
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var r row
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return summary{}, fmt.Errorf("aggregate: %w", err)
		}
		s.Files++
		tally(&s, r)
		if r.Bucket == 3 || r.Bucket == 4 {
			s.Unopenable++
		} else {
			s.Documents++
		}
		if r.Pages != nil {
			s.Pages += *r.Pages
		}
	}
	if err := sc.Err(); err != nil {
		return summary{}, err
	}
	return s, nil
}

func report(s summary) {
	fmt.Printf("files              %d\n", s.Files)
	fmt.Printf("documents          %d\n", s.Documents)
	fmt.Printf("unopenable         %d\n", s.Unopenable)
	fmt.Printf("pages              %d\n", s.Pages)
	fmt.Println()
	fmt.Printf("bucket 1  no /Encrypt                                      %d\n", s.Bucket1)
	fmt.Printf("bucket 2  /Encrypt, opens with NO password (dangerous)     %d   <-- THE NUMBER\n", s.Bucket2)
	fmt.Printf("  ...Inspect succeeds                                     %d\n", s.Bucket2InspectOK)
	fmt.Printf("  ...Inspect fails                                        %d\n", s.Bucket2InspectFail)
	fmt.Printf("bucket 3  /Encrypt, refused (password required)           %d\n", s.Bucket3)
	fmt.Printf("bucket 4  Open fails for any other reason                 %d\n", s.Bucket4)
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

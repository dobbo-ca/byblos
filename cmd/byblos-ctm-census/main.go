// Command byblos-ctm-census measures the full composed CTM of every image
// placement over a sample, for byb-06n: what does classify's shear-blind top
// placement actually look like, and how many pages that extract today would
// newly divert if a shear test were added beside the existing skew one.
//
// It runs ONE content.Walk per page (the same walk byblos.Inspect does) and
// decodes no image sample data at all -- see census.go's doc comment for why
// that is enough to reproduce classify's divert reason without a raster
// decode.
//
//	byblos-ctm-census -o out.jsonl [-j N] <dir>   # sweep: write JSONL + print summary
//	byblos-ctm-census -aggregate out.jsonl        # re-derive the summary from a prior JSONL
//
// Sweep mode never reads a prior JSONL and aggregate mode never opens the
// sample -- the two are independent, which is the whole point: the headline
// numbers are reproducible from the JSONL alone, hours after the sweep that
// produced it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/dobbo-ca/byblos/internal/content"
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

// Record is one JSONL line: one image placement, or (PlacementIndex == -1)
// one page carrying no images at all. Every field a downstream aggregation
// needs is here, raw -- nothing is pre-bucketed or pre-thresholded, so a
// bucket boundary or a threshold argued from the data later does not need a
// re-sweep.
type Record struct {
	Doc            string `json:"doc"`
	PageIndex      int    `json:"page_index"`
	PlacementIndex int    `json:"placement_index"` // -1: page carries no images
	IsTop          bool   `json:"is_top"`
	NPlacements    int    `json:"n_placements"`

	M0 float64 `json:"m0"`
	M1 float64 `json:"m1"`
	M2 float64 `json:"m2"`
	M3 float64 `json:"m3"`
	M4 float64 `json:"m4"`
	M5 float64 `json:"m5"`

	SkewDeg      float64  `json:"skew_deg"`
	ShearDeg     *float64 `json:"shear_deg,omitempty"` // nil exactly when Degenerate
	Degenerate   bool     `json:"degenerate"`
	PlacementDeg float64  `json:"placement_deg"`

	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Bitonal bool   `json:"bitonal"`
	Filter  string `json:"filter,omitempty"`

	// PageReason is classify's divert reason for this page (census.go's
	// port of extract.go's classify), or "" when the page would extract
	// today. It is the SAME on every placement row of a page and on the
	// page's own no-image row, which is deliberate: a per-page fact
	// repeated on every row lets an aggregator group by page_reason
	// without a second pass to look it up.
	PageReason string `json:"page_reason"`
}

// docRecords is one document's share of the sweep. It runs on a worker
// goroutine (sample.Walk's contract) and touches nothing shared.
func docRecords(d sample.Doc) []Record {
	if d.Err != nil {
		return nil // unopenable: internal/sample already excludes it from Population
	}
	var out []Record
	ctx := context.Background()
	n := d.Doc.PageCount()
	for pageIdx := 1; pageIdx <= n; pageIdx++ {
		p, err := d.Doc.Page(pageIdx)
		if err != nil {
			out = append(out, Record{Doc: d.Rel, PageIndex: pageIdx, PlacementIndex: -1, PageReason: "page-error"})
			continue
		}
		s, _ := content.Walk(ctx, p.Content, p.Scope, d.Doc)
		if s == nil {
			s = &content.Scan{}
		}
		_, reason := classify(p.CropBox, s, d.Doc.ImageInfo)
		np := len(s.Images)
		if np == 0 {
			out = append(out, Record{Doc: d.Rel, PageIndex: pageIdx, PlacementIndex: -1, PageReason: reason})
			continue
		}
		top := np - 1
		for i, pl := range s.Images {
			m := [6]float64(pl.CTM)
			r := Record{
				Doc: d.Rel, PageIndex: pageIdx, PlacementIndex: i,
				IsTop: i == top, NPlacements: np,
				M0: m[0], M1: m[1], M2: m[2], M3: m[3], M4: m[4], M5: m[5],
				SkewDeg: skewDegrees(m), PlacementDeg: placementDeg(m),
				Degenerate: degenerate(m),
				PageReason: reason,
			}
			if !r.Degenerate {
				sd := shearDegrees(m)
				r.ShearDeg = &sd
			}
			if info, ok := d.Doc.ImageInfo(pl.ID); ok {
				r.Width, r.Height = info.Width, info.Height
				r.Bitonal = info.BPC == 1 || info.ImageMask
				r.Filter = info.Filter
			}
			out = append(out, r)
		}
	}
	return out
}

// sweep walks root and writes every Record to w, in lexical path order
// regardless of which worker finished first -- byblos-divert's Index-keyed
// slot array, ported.
func sweep(root string, workers int, w *json.Encoder) (sample.Population, error) {
	paths, err := sample.Paths(root)
	if err != nil {
		return sample.Population{}, err
	}
	per := make([][]Record, len(paths))
	pop, err := sample.Walk(root, workers, func(d sample.Doc) {
		per[d.Index] = docRecords(d)
	})
	if err != nil {
		return sample.Population{}, err
	}
	for i := range per {
		for _, r := range per[i] {
			if err := w.Encode(r); err != nil {
				return pop, err
			}
		}
	}
	return pop, nil
}

func main() {
	oFlag := flag.String("o", "", "JSONL output path (sweep mode)")
	aggFlag := flag.String("aggregate", "", "re-derive the summary from a prior JSONL, without sweeping")
	j := flag.Int("j", 0, "files to sweep in parallel (default "+strconv.Itoa(defaultJobs)+", or $"+jobsEnv+")")
	flag.Parse()

	if *aggFlag != "" {
		f, err := os.Open(*aggFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "byblos-ctm-census:", err)
			os.Exit(1)
		}
		defer f.Close()
		agg, err := aggregate(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "byblos-ctm-census:", err)
			os.Exit(1)
		}
		agg.Print(os.Stdout)
		return
	}

	if flag.NArg() != 1 || *oFlag == "" {
		fmt.Fprintln(os.Stderr, "usage: byblos-ctm-census -o out.jsonl [-j N] <dir>")
		fmt.Fprintln(os.Stderr, "       byblos-ctm-census -aggregate out.jsonl")
		os.Exit(2)
	}

	out, err := os.Create(*oFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "byblos-ctm-census:", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(out)
	pop, err := sweep(flag.Arg(0), jobs(*j), enc)
	closeErr := out.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "byblos-ctm-census:", err)
		os.Exit(1)
	}
	if closeErr != nil {
		fmt.Fprintln(os.Stderr, "byblos-ctm-census:", closeErr)
		os.Exit(1)
	}

	fmt.Printf("files       %d\n", pop.Files)
	fmt.Printf("unopenable  %d\n", pop.Unopenable)
	fmt.Printf("documents   %d\n", pop.Documents)
	fmt.Printf("pages       %d\n", pop.Pages)
	fmt.Println("wrote", *oFlag)

	// Re-open what was just written and run the SAME aggregate path a later,
	// independent invocation would use -- so the summary printed right after
	// a sweep is provably the aggregate-mode number, not a second
	// computation that could drift from it.
	f, err := os.Open(*oFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "byblos-ctm-census:", err)
		os.Exit(1)
	}
	defer f.Close()
	agg, err := aggregate(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "byblos-ctm-census:", err)
		os.Exit(1)
	}
	agg.Print(os.Stdout)
}

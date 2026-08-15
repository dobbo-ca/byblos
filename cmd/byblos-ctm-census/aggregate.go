package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
)

// gateThresholds is the set of shear lower bounds the headline gated number
// is reported at. 0.05 is the bead's own choice; the rest are here so that
// choice can be argued from where the data actually bends, not assumed.
var gateThresholds = []float64{0.01, 0.05, 0.1, 0.5, 1.0}

// shearBuckets are the upper edges of the shear-degrees histogram, in
// degrees. A placement with shear exactly 0 falls in the first bucket; the
// last edge is +Inf so nothing is dropped.
const numBuckets = 10

var shearBucketEdges = [numBuckets]float64{0, 0.01, 0.05, 0.1, 0.5, 1.0, 2.0, 5.0, 15.0, math.Inf(1)}

func bucketLabel(i int) string {
	lo := "0"
	if i > 0 {
		lo = fmt.Sprintf("%g", shearBucketEdges[i-1])
	}
	hi := fmt.Sprintf("%g", shearBucketEdges[i])
	if math.IsInf(shearBucketEdges[i], 1) {
		hi = "inf"
	}
	if i == 0 {
		return "[0," + hi + "]"
	}
	return "(" + lo + "," + hi + "]"
}

func bucketOf(shear float64) int {
	for i, edge := range shearBucketEdges {
		if shear <= edge {
			return i
		}
	}
	return len(shearBucketEdges) - 1
}

// pageAgg is what one page contributes: its reason, its placement count, and
// (when it has at least one image and a non-degenerate top) the top
// placement's skew/shear, plus whether ANY placement on the page falls in
// the gate window.
type pageAgg struct {
	reason        string
	nPlacements   int
	topDegenerate bool
	topSkew       float64
	topShear      float64
	anyGateHit    [len5]bool // per gateThresholds[i]: some placement (not just top) in (thr,2.0] & skew<=2.0
	topGateHit    [len5]bool // per gateThresholds[i]: TOP placement in (thr,2.0] & skew<=2.0
}

const len5 = 5 // len(gateThresholds); const-folded so the array above can be sized without an init-order dependency

// Aggregate is every headline number byb-06n's acceptance criterion and its
// surrounding context need, computed from a Record stream alone.
type Aggregate struct {
	TotalDocuments    int
	TotalPages        int
	ImageBearingPages int // predicate: n_placements > 0 on that page's row(s)

	TotalPlacements      int
	DegeneratePlacements int
	DegenerateTopPages   int // pages whose TOP placement is degenerate

	// Distribution of shear degrees, over every non-degenerate placement and,
	// separately, over every non-degenerate page-top (one value per
	// image-bearing page, regardless of PageReason).
	PlacementBuckets [numBuckets]int
	PageTopBuckets   [numBuckets]int
	PlacementPct     percentiles
	PageTopPct       percentiles

	// The headline: pages with PageReason=="" (extract today) whose TOP
	// placement -- the one placementReason actually judges -- has shear in
	// (thr,2.0] and skew<=2.0, for thr = gateThresholds[i]. GatedTopByThreshold[1]
	// (thr=0.05) is the bead's own number.
	GatedTopByThreshold [len5]int
	// The same population and thresholds, but counting a page if ANY
	// placement on it (not only the top) falls in the window. This is
	// DELIBERATELY BROADER and is not the bead's number; it exists to show
	// how much broader, per byb-06n's task instructions.
	GatedAnyByThreshold [len5]int
}

type percentiles struct {
	P50, P90, P95, P99, P999, Max float64
	N                             int
}

func percentilesOf(vals []float64) percentiles {
	if len(vals) == 0 {
		return percentiles{}
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	pick := func(p float64) float64 {
		idx := int(p * float64(len(s)-1))
		return s[idx]
	}
	return percentiles{
		P50: pick(0.50), P90: pick(0.90), P95: pick(0.95),
		P99: pick(0.99), P999: pick(0.999), Max: s[len(s)-1],
		N: len(s),
	}
}

// aggregate reads a Record-per-line JSONL stream and computes Aggregate. It
// never opens the sample -- everything it reports is re-derivable from r
// alone, which is the entire point of separating sweep and aggregate modes.
func aggregate(r io.Reader) (Aggregate, error) {
	dec := json.NewDecoder(r)

	docs := map[string]bool{}

	var placementShears []float64
	var pageTopShears []float64

	var a Aggregate

	// Pages are grouped by contiguous (doc,page_index) runs in the file
	// (docRecords emits one page's rows together, and sweep concatenates
	// per-document blocks in path order), so a simple "did the key change"
	// check is enough to close out the previous page -- no map of every page
	// is needed to dedupe.
	var curDoc string
	var curPage int
	var haveCur bool
	var cur pageAgg

	closePage := func() {
		if !haveCur {
			return
		}
		a.TotalPages++
		if cur.nPlacements > 0 {
			a.ImageBearingPages++
		}
		if cur.nPlacements > 0 {
			if cur.topDegenerate {
				a.DegenerateTopPages++
			} else {
				a.PageTopBuckets[bucketOf(cur.topShear)]++
				pageTopShears = append(pageTopShears, cur.topShear)
			}
		}
		if cur.reason == "" {
			for i := range gateThresholds {
				if cur.topGateHit[i] {
					a.GatedTopByThreshold[i]++
				}
				if cur.anyGateHit[i] {
					a.GatedAnyByThreshold[i]++
				}
			}
		}
	}

	for {
		var rec Record
		if err := dec.Decode(&rec); err != nil {
			if err == io.EOF {
				break
			}
			return Aggregate{}, err
		}
		docs[rec.Doc] = true

		if !haveCur || rec.Doc != curDoc || rec.PageIndex != curPage {
			closePage()
			curDoc, curPage = rec.Doc, rec.PageIndex
			haveCur = true
			cur = pageAgg{reason: rec.PageReason, nPlacements: rec.NPlacements}
		}

		if rec.PlacementIndex < 0 {
			continue // page-level no-image (or page-error) row; nothing more to fold in
		}
		a.TotalPlacements++
		if rec.Degenerate {
			a.DegeneratePlacements++
			if rec.IsTop {
				cur.topDegenerate = true
			}
			continue
		}
		shear := *rec.ShearDeg
		placementShears = append(placementShears, shear)
		a.PlacementBuckets[bucketOf(shear)]++

		if rec.IsTop {
			cur.topShear = shear
			cur.topSkew = rec.SkewDeg
		}
		for i, thr := range gateThresholds {
			hit := shear > thr && shear <= 2.0 && rec.SkewDeg <= 2.0
			if hit {
				cur.anyGateHit[i] = true
				if rec.IsTop {
					cur.topGateHit[i] = true
				}
			}
		}
	}
	closePage()

	a.TotalDocuments = len(docs)
	a.PlacementPct = percentilesOf(placementShears)
	a.PageTopPct = percentilesOf(pageTopShears)
	return a, nil
}

// Print writes the headline numbers, human-readable, to w.
func (a Aggregate) Print(w io.Writer) {
	fmt.Fprintf(w, "documents            %d\n", a.TotalDocuments)
	fmt.Fprintf(w, "pages                %d\n", a.TotalPages)
	fmt.Fprintf(w, "image-bearing pages  %d   (predicate: n_placements > 0)\n", a.ImageBearingPages)
	fmt.Fprintf(w, "placements           %d\n", a.TotalPlacements)
	fmt.Fprintf(w, "degenerate placements %d  (zero-length CTM column; shear undefined)\n", a.DegeneratePlacements)
	fmt.Fprintf(w, "degenerate page-tops  %d  (top placement itself degenerate)\n", a.DegenerateTopPages)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "shear_deg distribution, over PLACEMENTS (all image-bearing pages, top and non-top, non-degenerate):")
	for i := range shearBucketEdges {
		fmt.Fprintf(w, "  %-14s %d\n", bucketLabel(i), a.PlacementBuckets[i])
	}
	fmt.Fprintf(w, "  percentiles: p50=%.6f p90=%.6f p95=%.6f p99=%.6f p99.9=%.6f max=%.6f  (n=%d)\n",
		a.PlacementPct.P50, a.PlacementPct.P90, a.PlacementPct.P95, a.PlacementPct.P99, a.PlacementPct.P999, a.PlacementPct.Max, a.PlacementPct.N)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "shear_deg distribution, over PAGES (top placement only, one value per image-bearing page, non-degenerate):")
	for i := range shearBucketEdges {
		fmt.Fprintf(w, "  %-14s %d\n", bucketLabel(i), a.PageTopBuckets[i])
	}
	fmt.Fprintf(w, "  percentiles: p50=%.6f p90=%.6f p95=%.6f p99=%.6f p99.9=%.6f max=%.6f  (n=%d)\n",
		a.PageTopPct.P50, a.PageTopPct.P90, a.PageTopPct.P95, a.PageTopPct.P99, a.PageTopPct.P999, a.PageTopPct.Max, a.PageTopPct.N)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "GATED NUMBER: pages with page_reason==\"\" (extract today) whose relevant placement has")
	fmt.Fprintln(w, "shear in (threshold,2.0] AND skew<=2.0 -- the pages a shear test would newly divert.")
	fmt.Fprintln(w, "  threshold   top-only (THE BEAD'S NUMBER at 0.05)   any-placement-on-page (broader)")
	for i, thr := range gateThresholds {
		marker := ""
		if thr == 0.05 {
			marker = "  <-- bead's number"
		}
		fmt.Fprintf(w, "  %-9g %10d                              %10d%s\n",
			thr, a.GatedTopByThreshold[i], a.GatedAnyByThreshold[i], marker)
	}
}

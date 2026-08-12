package bench

import "slices"

// Metric names one of the six axes design spec section 2 scores.
type Metric string

const (
	MetricSize      Metric = "size"
	MetricLatency   Metric = "latency"
	MetricMemory    Metric = "memory"
	MetricDiskBytes Metric = "disk_bytes"
	MetricWriteIOPS Metric = "write_iops"
	MetricReadIOPS  Metric = "read_iops"
)

// MetricWeights is the objective ladder, verbatim from design spec section 2.
// The values sum to 1.0; TestMetricWeightsSumToOne enforces that.
var MetricWeights = map[Metric]float64{
	MetricSize:      0.40,
	MetricLatency:   0.25,
	MetricMemory:    0.15,
	MetricDiskBytes: 0.10,
	MetricWriteIOPS: 0.06,
	MetricReadIOPS:  0.04,
}

// Deterministic reports whether a metric can be carried in the committed
// baseline. Every metric except latency can (spec section 5.3).
func (m Metric) Deterministic() bool { return m != MetricLatency }

// Sample is one capability measured over one document.
type Sample struct {
	Capability string `json:"capability"`
	Doc        string `json:"doc"`

	// OutBytes is the length of the artifact the capability produced. It is
	// zero for inspect and extract-raster, which return a struct rather than an
	// encoded stream (spec section 3.3), and their size share is therefore zero
	// with no special case needed.
	OutBytes int64 `json:"out_bytes"`

	// AllocBytes is the delta in runtime.MemStats.TotalAlloc across the call.
	// TotalAlloc is cumulative and monotonic, so this is exact regardless of
	// when the garbage collector ran.
	AllocBytes int64 `json:"alloc_bytes"`

	// WChar, SysCW and SysCR are the /proc/self/io deltas. They are meaningful
	// only when DiskCounters is true.
	WChar int64 `json:"wchar"`
	SysCW int64 `json:"syscw"`
	SysCR int64 `json:"syscr"`

	// DiskCounters is false where /proc/self/io does not exist.
	DiskCounters bool `json:"disk_counters"`

	// WallNS holds one timing per repetition, empty when this run did not time
	// this capability. Only the capabilities a diff touches are timed
	// (spec section 5.3).
	WallNS []int64 `json:"wall_ns,omitempty"`

	// PeakRSS is recorded for the reader and never scored.
	PeakRSS     int64  `json:"peak_rss"`
	PeakRSSUnit string `json:"peak_rss_unit"`
}

// Value returns the sample's reading for a metric. ok is false when the metric
// was not measured, which the scorer must treat as "skip", never as zero.
func (s Sample) Value(m Metric) (value float64, ok bool) {
	switch m {
	case MetricSize:
		return float64(s.OutBytes), true
	case MetricMemory:
		return float64(s.AllocBytes), true
	case MetricDiskBytes:
		return float64(s.WChar), s.DiskCounters
	case MetricWriteIOPS:
		return float64(s.SysCW), s.DiskCounters
	case MetricReadIOPS:
		return float64(s.SysCR), s.DiskCounters
	case MetricLatency:
		if len(s.WallNS) == 0 {
			return 0, false
		}
		sorted := slices.Clone(s.WallNS)
		slices.Sort(sorted)
		return float64(sorted[len(sorted)/2]), true
	}
	return 0, false
}

// Run is one whole invocation of the harness over the bench set.
type Run struct {
	GoVersion  string   `json:"go_version"`
	GOOSGOARCH string   `json:"goos_goarch"`
	BenchSet   string   `json:"bench_set_sha256"`
	Samples    []Sample `json:"samples"`
}

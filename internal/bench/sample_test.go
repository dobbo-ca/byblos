package bench

import "testing"

func TestMetricWeightsSumToOne(t *testing.T) {
	var sum float64
	for _, w := range MetricWeights {
		sum += w
	}
	if sum < 0.9999 || sum > 1.0001 {
		t.Errorf("metric weights sum to %v, want 1.0", sum)
	}
}

func TestMetricWeightsMatchTheSpec(t *testing.T) {
	want := map[Metric]float64{
		MetricSize:      0.40,
		MetricLatency:   0.25,
		MetricMemory:    0.15,
		MetricDiskBytes: 0.10,
		MetricWriteIOPS: 0.06,
		MetricReadIOPS:  0.04,
	}
	if len(MetricWeights) != len(want) {
		t.Fatalf("got %d metrics, want %d", len(MetricWeights), len(want))
	}
	for m, w := range want {
		if MetricWeights[m] != w {
			t.Errorf("weight for %s = %v, want %v", m, MetricWeights[m], w)
		}
	}
}

// TestSampleValueReportsAbsentDiskCounters pins that a sample taken where
// /proc/self/io does not exist reports its three disk metrics as absent, so
// the scorer skips them instead of reading a fabricated zero.
func TestSampleValueReportsAbsentDiskCounters(t *testing.T) {
	s := Sample{OutBytes: 100, AllocBytes: 200, DiskCounters: false}

	if v, ok := s.Value(MetricSize); !ok || v != 100 {
		t.Errorf("size = %v, %v; want 100, true", v, ok)
	}
	if v, ok := s.Value(MetricMemory); !ok || v != 200 {
		t.Errorf("memory = %v, %v; want 200, true", v, ok)
	}
	for _, m := range []Metric{MetricDiskBytes, MetricWriteIOPS, MetricReadIOPS} {
		if _, ok := s.Value(m); ok {
			t.Errorf("%s reported present with DiskCounters false", m)
		}
	}
}

// TestSampleLatencyIsTheMedian pins that latency reduces the repetitions to
// their median rather than their mean, so one scheduling stall on a shared
// runner cannot drag the figure.
func TestSampleLatencyIsTheMedian(t *testing.T) {
	s := Sample{WallNS: []int64{100, 102, 101, 99, 900}}
	v, ok := s.Value(MetricLatency)
	if !ok {
		t.Fatal("latency reported absent with five timings recorded")
	}
	if v != 101 {
		t.Errorf("latency = %v, want the median 101", v)
	}
}

func TestSampleLatencyAbsentWhenUntimed(t *testing.T) {
	if _, ok := (Sample{}).Value(MetricLatency); ok {
		t.Error("latency reported present with no timings recorded")
	}
}

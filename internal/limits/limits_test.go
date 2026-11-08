package limits

import (
	"context"
	"errors"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultLimit(t *testing.T) {
	files := func(m map[string]string) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) {
			if v, ok := m[p]; ok {
				return []byte(v), nil
			}
			return nil, os.ErrNotExist
		}
	}
	cases := []struct {
		name    string
		goLimit int64
		files   map[string]string
		want    int64
	}{
		{"gomemlimit", 1500 << 20, map[string]string{"/sys/fs/cgroup/memory.max": "1073741824\n"}, 750 << 20},
		{"cgroup v2", math.MaxInt64, map[string]string{"/sys/fs/cgroup/memory.max": "2147483648\n"}, 1 << 30},
		{"cgroup v2 unlimited", math.MaxInt64, map[string]string{"/sys/fs/cgroup/memory.max": "max\n"}, 0},
		{"cgroup v1", math.MaxInt64, map[string]string{"/sys/fs/cgroup/memory/memory.limit_in_bytes": "1073741824"}, 512 << 20},
		{"cgroup v1 unlimited", math.MaxInt64, map[string]string{"/sys/fs/cgroup/memory/memory.limit_in_bytes": "9223372036854771712"}, 0},
		{"none", math.MaxInt64, nil, 0},
	}
	for _, c := range cases {
		if got := defaultLimit(c.goLimit, files(c.files)); got != c.want {
			t.Errorf("%s: %d; want %d", c.name, got, c.want)
		}
	}
}

// 9A.4 item 3: many operations under one limit never hold more than it.
func TestBudgetHolds(t *testing.T) {
	prev := SetMemoryLimit(100)
	defer SetMemoryLimit(prev)
	var held, peak atomic.Int64
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := Reserve(context.Background(), 40)
			if err != nil {
				t.Error(err)
				return
			}
			h := held.Add(40)
			for p := peak.Load(); h > p && !peak.CompareAndSwap(p, h); p = peak.Load() {
			}
			time.Sleep(time.Millisecond)
			held.Add(-40)
			release()
		}()
	}
	wg.Wait()
	if peak.Load() > 100 {
		t.Errorf("peak held %d > limit 100", peak.Load())
	}
}

func TestErrorClasses(t *testing.T) {
	prev := SetMemoryLimit(100)
	defer SetMemoryLimit(prev)

	_, err := Reserve(context.Background(), 101)
	var le *LimitError
	if !errors.As(err, &le) || !errors.Is(err, ErrLimit) || errors.Is(err, ErrMemoryLimit) {
		t.Errorf("over the whole limit: %v; want a permanent *LimitError", err)
	}

	hold, _ := Reserve(context.Background(), 100)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = Reserve(ctx, 1)
	if !errors.Is(err, ErrMemoryLimit) || errors.Is(err, ErrLimit) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("held by another: %v; want transient ErrMemoryLimit wrapping the ctx error", err)
	}
	hold()
	// The cancelled waiter left nothing behind.
	if r, err := Reserve(context.Background(), 100); err != nil {
		t.Errorf("after release: %v", err)
	} else {
		r()
	}
}

func TestFIFO(t *testing.T) {
	prev := SetMemoryLimit(100)
	defer SetMemoryLimit(prev)
	hold, _ := Reserve(context.Background(), 100)
	var order []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := func(name string, n int64) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := Reserve(context.Background(), n)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			r()
		}()
		time.Sleep(20 * time.Millisecond) // queue in a known order
	}
	start("big", 80)
	start("small", 30) // cannot join big (80+30 > 100), and must not overtake it
	hold()
	wg.Wait()
	if len(order) != 2 || order[0] != "big" {
		t.Errorf("grant order %v; want [big small]", order)
	}
}

func TestNoLimitReservesNothing(t *testing.T) {
	prev := SetMemoryLimit(0)
	defer SetMemoryLimit(prev)
	r, err := Reserve(context.Background(), 1<<50)
	if err != nil {
		t.Fatal(err)
	}
	r()
}

// BenchmarkReserve is the whole per-document cost of the limiter when the
// budget never binds: one reservation and its release.
func BenchmarkReserve(b *testing.B) {
	prev := SetMemoryLimit(1 << 40)
	defer SetMemoryLimit(prev)
	for b.Loop() {
		release, _ := Reserve(context.Background(), 1<<20)
		release()
	}
}

// BenchmarkReserveContended is BenchmarkReserve with a budget that holds two
// reservations while every CPU asks for one, so callers queue. p99-ns is the
// 99th percentile of the time from Reserve to its return, waiting included.
func BenchmarkReserveContended(b *testing.B) {
	prev := SetMemoryLimit(2 << 20)
	defer SetMemoryLimit(prev)
	var mu sync.Mutex
	var lat []time.Duration
	b.RunParallel(func(pb *testing.PB) {
		var mine []time.Duration
		for pb.Next() {
			t := time.Now()
			release, _ := Reserve(context.Background(), 1<<20)
			mine = append(mine, time.Since(t))
			release()
		}
		mu.Lock()
		lat = append(lat, mine...)
		mu.Unlock()
	})
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	b.ReportMetric(float64(lat[len(lat)*99/100]), "p99-ns")
}

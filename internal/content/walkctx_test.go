package content

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// bigStream returns a content stream with n operators and nothing else worth
// walking. "q Q" is a matched save/restore pair, so the walk does real work per
// operator without accumulating state that would blow up memory instead of time.
func bigStream(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n/2; i++ {
		b.WriteString("q Q\n")
	}
	return b.Bytes()
}

// countCtx records every context check. It never reports cancelled, so it
// measures where the boundaries ARE without changing what the walk does.
//
// This is byb-xyn's observeCtx, reused as byb-fem asks. Counting rather than
// timing is deliberate: byb-xyn abandoned elapsed time because it is not an
// assertion, it is a measurement of the machine.
type countCtx struct {
	context.Context
	checks int
}

func (c *countCtx) Err() error { c.checks++; return nil }

// TestWalkChecksGrowWithOperatorCount is byb-fem's acceptance bar, in the form
// byb-xyn established: the property that matters is whether the number of
// context boundaries GROWS WITH THE WORK, not how long anything took.
//
// Before this bead, Walk took no context at all, so a single page with a
// multi-million-operator content stream was uninterruptible for the whole walk
// however long that was — measured at 95.4% of an ExtractPageRasterContext call
// on a 396 KB single-page PDF, with InspectContext ignoring a cancel for 665 ms
// and then returning nil.
func TestWalkChecksGrowWithOperatorCount(t *testing.T) {
	small := &countCtx{Context: context.Background()}
	large := &countCtx{Context: context.Background()}

	if _, err := Walk(small, bigStream(2_000), 0, nil); err != nil {
		t.Fatalf("Walk on the small stream: %v", err)
	}
	if _, err := Walk(large, bigStream(20_000), 0, nil); err != nil {
		t.Fatalf("Walk on the large stream: %v", err)
	}

	if small.checks == 0 || large.checks == 0 {
		t.Fatalf("Walk consulted the context %d and %d times; it must consult it at all",
			small.checks, large.checks)
	}
	// Ten times the operators must buy substantially more boundaries. A fixed
	// handful regardless of size is exactly the defect: it bounds cancellation
	// by the whole walk rather than by a bounded amount of work.
	if large.checks < small.checks*5 {
		t.Errorf("checks did not grow with the stream: %d operators -> %d checks, "+
			"10x operators -> %d checks; cancellation is still bounded by the whole walk",
			2_000, small.checks, large.checks)
	}
	t.Logf("2,000 ops -> %d checks; 20,000 ops -> %d checks", small.checks, large.checks)
}

// stopAt reports cancelled from its Nth check onward, so the cancellation lands
// at a known boundary on every machine with no timing involved. byb-xyn's
// cancelAtCheck, reused.
type stopAt struct {
	context.Context
	checks int
	after  int
}

func (c *stopAt) Err() error {
	c.checks++
	if c.checks > c.after {
		return context.Canceled
	}
	return nil
}

// TestWalkStopsWhenCancelled pins the behavioural half. Counting the checks
// proves where the boundaries are; only this proves they DO anything.
func TestWalkStopsWhenCancelled(t *testing.T) {
	ctx := &stopAt{Context: context.Background(), after: 5}
	_, err := Walk(ctx, bigStream(100_000), 0, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Walk error = %v, want context.Canceled", err)
	}
	// It must stop NEAR the cancellation, not merely report it at the end. A
	// walk that runs to completion and then returns the error would satisfy the
	// errors.Is check above while fixing nothing.
	if ctx.checks > ctx.after+2 {
		t.Errorf("Walk kept checking %d times after cancelling at check %d; it ran on",
			ctx.checks, ctx.after)
	}
}

// TestWalkAcceptsABackgroundContext keeps the ordinary path honest: threading a
// context must not change what a walk reports.
func TestWalkAcceptsABackgroundContext(t *testing.T) {
	src := []byte(strings.Repeat("q Q\n", 10))
	got, err := Walk(context.Background(), src, 0, nil)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if got == nil {
		t.Fatal("Walk returned a nil scan on a valid stream")
	}
	_ = time.Now // keep the import honest if the timing helpers above are trimmed
}

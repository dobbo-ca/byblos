package byblos

import (
	"errors"
	"strings"
	"testing"
)

// This file salvages assertNotImplemented's real content (optimize_test.go,
// removed by byb-b3's GREEN stage): nothing in this package constructs a
// *NotImplemented any more -- RecompressJPEG was the last caller -- so the
// still-exported type needs its own test against a literal value rather than
// going untested.
func TestNotImplementedShape(t *testing.T) {
	ni := &NotImplemented{
		Capability: "some-capability",
		Why:        "some reason",
		Issue:      "byb-xyz",
	}
	var err error = ni

	if !errors.Is(err, ErrNotImplemented) {
		t.Errorf("errors.Is(err, ErrNotImplemented) = false for %v; a caller cannot "+
			"distinguish a missing capability from a failed document", err)
	}
	var got *NotImplemented
	if !errors.As(err, &got) {
		t.Fatalf("errors.As(err, *NotImplemented) = false for %v", err)
	}
	if got != ni {
		t.Errorf("errors.As gave back %v; want the original %v", got, ni)
	}
	// The message has to carry the same facts, because most of the time it is
	// all that reaches a log.
	for _, want := range []string{ni.Capability, ni.Issue} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q does not mention %q", err.Error(), want)
		}
	}
}

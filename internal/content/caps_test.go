package content

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/limits"
)

// The walk caps' defaults, restated so a change to them fails here.
const (
	maxTextShows       = 1 << 16
	maxPaints          = 1 << 18
	maxGStateDepth     = 1024
	maxFormInvocations = 4096
)

func TestWalkCapDefaults(t *testing.T) {
	var l limits.Limits
	if l.TextShows() != maxTextShows || l.Paints() != maxPaints || l.GStateDepth() != maxGStateDepth ||
		l.FormInvocations() != maxFormInvocations || l.FormDepth() != 8 || l.Operands() != 8192 {
		t.Errorf("walk cap defaults moved: %+v", l)
	}
}

func limitName(err error) string {
	var le *limits.LimitError
	if errors.As(err, &le) {
		return le.Name
	}
	return ""
}

// limEnv is an Env that carries Limits, as pdfdoc's does.
type limEnv struct {
	mapEnv
	lim limits.Limits
}

func (e limEnv) Limits() limits.Limits { return e.lim }

func TestWalkLowCapsRefuseByName(t *testing.T) {
	form := mapEnv{{"F1": {Content: []byte("0 0 m f "), Scope: 0}}}
	nested := mapEnv{{"F1": {Content: []byte("/F2 Do"), Scope: 0}, "F2": {Content: []byte("0 0 m f "), Scope: 0}}}
	for _, c := range []struct {
		name string
		src  string
		env  mapEnv
		lim  limits.Limits
	}{
		{"q-depth", "q q q q", form, limits.Limits{MaxGStateDepth: 3}},
		{"form-invocations", "/F1 Do /F1 Do /F1 Do", form, limits.Limits{MaxFormInvocations: 2}},
		{"form-depth", "/F1 Do", nested, limits.Limits{MaxFormDepth: 1}},
	} {
		_, err := Walk(context.Background(), []byte(c.src), 0, limEnv{c.env, c.lim})
		if got := limitName(err); got != c.name {
			t.Errorf("%s: err = %v; want *LimitError %s", c.name, err, c.name)
		}
	}
}

// The truncating caps keep today's behaviour at any setting: the walk
// stops recording and carries on.
func TestWalkLowRecordCapsTruncate(t *testing.T) {
	s, err := Walk(context.Background(), []byte(strings.Repeat("0 0 m 1 1 l S\n", 10)), 0,
		limEnv{mapEnv{}, limits.Limits{MaxPaints: 4}})
	if err != nil || len(s.Paints) != 4 {
		t.Errorf("paints = %d, err = %v; want 4 and nil", len(s.Paints), err)
	}
	s, err = Walk(context.Background(), []byte("BT "+strings.Repeat("(a) Tj ", 10)+"ET"), 0,
		limEnv{mapEnv{}, limits.Limits{MaxTextShows: 3}})
	if err != nil || len(s.TextShows) != 3 || s.TextOps != 10 {
		t.Errorf("shows = %d ops = %d, err = %v; want 3, 10, nil", len(s.TextShows), s.TextOps, err)
	}
}

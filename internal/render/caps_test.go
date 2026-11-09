package render

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/limits"
)

// The render caps' defaults, restated so a change to them fails here.
const (
	defGStateDepth             = 1024
	maxFontRetainedBytes int64 = 16 << 20
)

func TestRenderCapDefaults(t *testing.T) {
	var l limits.Limits
	if l.RasterSide() != 20000 || l.RasterPixels() != 1<<26 || l.GStateDepth() != defGStateDepth ||
		l.RenderFontBytes() != maxFontRetainedBytes || l.PathPoints() != 1<<21 ||
		l.FillWork() != 1<<28 || l.Operands() != 8192 {
		t.Errorf("render cap defaults moved: %+v", l)
	}
}

func limitName(err error) string {
	var le *limits.LimitError
	if errors.As(err, &le) {
		return le.Name
	}
	return ""
}

func TestRenderLowCapsRefuseByName(t *testing.T) {
	for _, c := range []struct {
		name  string
		src   string
		scale float64
		lim   limits.Limits
	}{
		{"raster-side", "", 1, limits.Limits{MaxRasterSide: 50}},
		{"raster-pixels", "", 1, limits.Limits{MaxRasterPixels: 5000}},
		{"q-depth", "q q q q", 1, limits.Limits{MaxGStateDepth: 3}},
		{"path-points", "0 0 m 50 50 l 20 80 l f", 1, limits.Limits{MaxPathPoints: 2}},
		{"fill-work", "0 g 0 0 100 100 re f", 1, limits.Limits{MaxFillWork: 10}},
	} {
		_, err := PageLimits(context.Background(), []byte(c.src), box100, 0, c.scale, nil, nil, c.lim)
		if got := limitName(err); got != c.name {
			t.Errorf("%s: err = %v; want *LimitError %s", c.name, err, c.name)
		}
	}
}

// A float past int64 converts to MinInt64 on amd64 and must still be refused.
func TestRasterSizeHugeScaleRefused(t *testing.T) {
	_, _, _, _, err := rasterSize(box100, math.MaxInt, limits.Limits{})
	if got := limitName(err); got != "raster-side" {
		t.Errorf("err = %v; want *LimitError raster-side", err)
	}
	var le *limits.LimitError
	if errors.As(err, &le) && le.Got <= le.Max {
		t.Errorf("Got = %d, want > Max %d", le.Got, le.Max)
	}
}

// A caller-raised cap must not turn into an image.NewRGBA panic, and the cap
// compares the canvas actually allocated: ceil(w)*ceil(h).
func TestRasterSizeRaisedCapsAndCanvas(t *testing.T) {
	big := limits.Limits{MaxRasterSide: math.MaxInt64, MaxRasterPixels: math.MaxInt64}
	for _, b := range []content.Box{{URX: 2e9, URY: 2e9}, {URX: 9.2e18, URY: 1}} {
		_, _, _, _, err := rasterSize(b, 1, big)
		if got := limitName(err); got != "raster-pixels" {
			t.Errorf("box %+v: err = %v; want *LimitError raster-pixels", b, err)
		}
	}
	// 8191.5 x 8192.4 is under 1<<26 by area but allocates 8192 x 8193 px.
	_, _, _, _, err := rasterSize(content.Box{URX: 8191.5, URY: 8192.4}, 1, limits.Limits{})
	if got := limitName(err); got != "raster-pixels" {
		t.Errorf("err = %v; want *LimitError raster-pixels", err)
	}
}

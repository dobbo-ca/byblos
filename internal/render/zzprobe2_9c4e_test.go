package render

import (
	"context"
	"fmt"
	"image"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
)

// rowProfile reports, for the first and last rows/cols, how dark they are.
func edgeReport(t *testing.T, tag string, img image.Image) {
	b := img.Bounds()
	dark := func(x, y int) int {
		r, _, _, _ := img.At(x, y).RGBA()
		return int(r >> 8)
	}
	// sample middle of each edge
	mx, my := (b.Min.X+b.Max.X)/2, (b.Min.Y+b.Max.Y)/2
	t.Logf("%s %v: leftcols=%d,%d,%d rightcols=%d,%d,%d toprows=%d,%d,%d botrows=%d,%d,%d",
		tag, b,
		dark(b.Min.X, my), dark(b.Min.X+1, my), dark(b.Min.X+2, my),
		dark(b.Max.X-3, my), dark(b.Max.X-2, my), dark(b.Max.X-1, my),
		dark(mx, b.Min.Y), dark(mx, b.Min.Y+1), dark(mx, b.Min.Y+2),
		dark(mx, b.Max.Y-3), dark(mx, b.Max.Y-2), dark(mx, b.Max.Y-1))
}

func TestProbeSlackLocation(t *testing.T) {
	const ux, uy = 240.4, 160.6
	cs := fmt.Sprintf("0 g 0 0 %g %g re f", ux, uy) // fill the whole page
	box := content.Box{LLX: 0, LLY: 0, URX: ux, URY: uy}
	for _, rot := range []int{0, 90, 180, 270} {
		oracle := popplerPNG(t, cropRotPDF(cs, ux, uy, 0, 0, ux, uy, rot))
		got, err := Page(context.Background(), []byte(cs), box, rot, 1, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		edgeReport(t, fmt.Sprintf("poppler rot%d", rot), oracle)
		edgeReport(t, fmt.Sprintf("byblos  rot%d", rot), got)
	}
}

package render

// TestBackgroundAgreesAcrossICCBasedScn pins byb-6ty's acceptance clause 2:
// govdocs1/200088.pdf page 1 opens "/Cs6 cs 1 1 1 scn ... re f" -- a white
// background rect in an unresolved (ICCBased) colour space -- and setComps
// used to drop those operands entirely, leaving the default black in force
// and painting the whole page. Before this fix the orchestrator measured
// 100.0% black at all four /Rotate values; this test pins that the operand-
// count fallback (byb-6ty) brings it back to a mostly-white page instead.
//
// GATED ON BYBLOS_SAMPLE: the sample root is not checked in, so this test
// skips (not fails) when the env var is unset, per the repo convention every
// other *_sample_test.go in this package follows.
import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

func TestBackgroundAgreesAcrossICCBasedScn(t *testing.T) {
	root := os.Getenv("BYBLOS_SAMPLE")
	if root == "" {
		t.Skip("BYBLOS_SAMPLE not set")
	}
	path := filepath.Join(root, "govdocs1", "pdfs", "200088.pdf")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("sample document not present: %v", err)
	}

	doc, err := pdfdoc.Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("pdfdoc.Open: %v", err)
	}
	p, err := doc.Page(1)
	if err != nil {
		t.Fatalf("Page(1): %v", err)
	}
	box := content.Box{LLX: p.CropBox.LLX, LLY: p.CropBox.LLY, URX: p.CropBox.URX, URY: p.CropBox.URY}

	for _, rot := range []int{0, 90, 180, 270} {
		t.Run(fmt.Sprintf("rotate%d", rot), func(t *testing.T) {
			img, err := Page(context.Background(), p.Content, box, rot, 0.5,
				pdfdocImages(doc, p), pdfdocFonts(doc, p))
			if err != nil {
				t.Fatalf("Page(rotate %d): %v", rot, err)
			}
			b := img.Bounds()
			black, total := 0, 0
			for y := b.Min.Y; y < b.Max.Y; y++ {
				for x := b.Min.X; x < b.Max.X; x++ {
					r, g, bl, _ := img.At(x, y).RGBA()
					total++
					if r>>8 < 8 && g>>8 < 8 && bl>>8 < 8 {
						black++
					}
				}
			}
			pct := 100 * float64(black) / float64(total)
			t.Logf("rot %3d: %d/%d black = %.1f%%", rot, black, total, pct)
			if pct > 50 {
				t.Errorf("rot %d: %.1f%% black; want a mostly-white background (ICCBased scn dropped)", rot, pct)
			}
		})
	}
}

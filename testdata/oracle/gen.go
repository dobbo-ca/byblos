//go:build ignore

// Command gen regenerates testdata/oracle/poppler.json from the corpus in
// testdata/corpus. Manual step; run `make oracle`. Requires poppler. Never run
// in CI: the committed JSON is what CI compares against.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	_ "image/png"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

type ImageRow struct {
	Page   int `json:"page"`
	Width  int `json:"width"`
	Height int `json:"height"`
	BPC    int `json:"bpc"`
}

// RasterRow is poppler's own rendering of the one image on a page, reduced to a
// hash of its pixels. This is what makes ExtractPageRaster differentially
// testable: a PNG that decodes is not the same claim as a PNG with the right
// pixels in it.
type RasterRow struct {
	Page   int    `json:"page"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Pixels string `json:"pixels_sha256"`
}

type DocOracle struct {
	Pages      int         `json:"pages"`
	PageWidth  float64     `json:"page_width_pt"`
	PageHeight float64     `json:"page_height_pt"`
	Images     []ImageRow  `json:"images"`
	Rasters    []RasterRow `json:"rasters,omitempty"`
	HasText    bool        `json:"has_text"`
	Error      string      `json:"error,omitempty"`
}

type Oracle struct {
	Tools     string               `json:"tools"`
	Documents map[string]DocOracle `json:"documents"`
}

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return string(out), err
}

// version reads a poppler tool's banner. It uses CombinedOutput because every
// poppler tool prints -v to STDERR; Output() would return an empty string and
// the golden would silently record "".
func version(name string) string {
	out, _ := exec.Command(name, "-v").CombinedOutput()
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

// pixelHash normalises an image to 8-bit RGBA and hashes it. Normalising is
// what makes the comparison meaningful: poppler writes grey PNGs that decode to
// image.Gray while pdfcpu's decode to image.RGBA, and the pixels are identical.
//
// This function is duplicated in oracle_test.go on purpose. gen.go is a
// //go:build ignore program under testdata/ and shares no code with the
// package; the alternative is exporting an oracle helper from the shipped API.
// If you change one, change both — the golden is worthless otherwise.
func pixelHash(im image.Image) string {
	b := im.Bounds()
	h := sha256.New()
	fmt.Fprintf(h, "%dx%d;", b.Dx(), b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := im.At(x, y).RGBA()
			h.Write([]byte{byte(r >> 8), byte(g >> 8), byte(bl >> 8), byte(a >> 8)})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func main() {
	o := Oracle{
		Tools:     strings.Join([]string{version("pdfinfo"), version("pdfimages"), version("pdftotext")}, "; "),
		Documents: map[string]DocOracle{},
	}
	tmp, err := os.MkdirTemp("", "byblos-oracle")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	for _, d := range corpus.All() {
		path := filepath.Join("testdata", "corpus", d.Name+".pdf")
		doc := DocOracle{}

		info, err := run("pdfinfo", path)
		if err != nil {
			doc.Error = "pdfinfo failed"
			o.Documents[d.Name] = doc
			continue
		}
		for _, line := range strings.Split(info, "\n") {
			key, val, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			val = strings.TrimSpace(val)
			switch strings.TrimSpace(key) {
			case "Pages":
				doc.Pages, _ = strconv.Atoi(val)
			case "Page size":
				// "612 x 792 pts (letter)"
				f := strings.Fields(val)
				if len(f) >= 3 {
					doc.PageWidth, _ = strconv.ParseFloat(f[0], 64)
					doc.PageHeight, _ = strconv.ParseFloat(f[2], 64)
				}
			}
		}

		list, err := run("pdfimages", "-list", path)
		if err == nil {
			lines := strings.Split(strings.TrimRight(list, "\n"), "\n")
			if len(lines) > 2 {
				hdr := strings.Fields(lines[0])
				col := map[string]int{}
				for i, h := range hdr {
					col[h] = i
				}
				for _, line := range lines[2:] {
					f := strings.Fields(line)
					if len(f) < len(hdr) {
						continue
					}
					// pdfimages lists a soft mask as a row of its own, type
					// "smask". Byblos counts paintings of image XObjects, and a
					// soft mask is never painted on its own, so keeping those
					// rows would make the two tools disagree about a document
					// they both read correctly.
					if i, ok := col["type"]; ok && i < len(f) && f[i] != "image" {
						continue
					}
					at := func(k string) int {
						i, ok := col[k]
						if !ok || i >= len(f) {
							return 0
						}
						v, _ := strconv.Atoi(f[i])
						return v
					}
					doc.Images = append(doc.Images, ImageRow{
						Page: at("page"), Width: at("width"), Height: at("height"), BPC: at("bpc"),
					})
				}
			}
		}

		// Poppler's own rendering of the page's raster, when the page has
		// exactly one. Pages with zero or several are skipped: there is nothing
		// for ExtractPageRaster to be compared against on those, and inventing
		// a rule here would duplicate classify badly.
		for page := 1; page <= doc.Pages; page++ {
			prefix := filepath.Join(tmp, fmt.Sprintf("%s-%d", d.Name, page))
			n := strconv.Itoa(page)
			if _, err := run("pdfimages", "-png", "-f", n, "-l", n, path, prefix); err != nil {
				continue
			}
			pngs, err := filepath.Glob(prefix + "-*.png")
			if err != nil || len(pngs) != 1 {
				continue
			}
			raw, err := os.ReadFile(pngs[0])
			if err != nil {
				continue
			}
			im, _, err := image.Decode(bytes.NewReader(raw))
			if err != nil {
				continue
			}
			doc.Rasters = append(doc.Rasters, RasterRow{
				Page:   page,
				Width:  im.Bounds().Dx(),
				Height: im.Bounds().Dy(),
				Pixels: pixelHash(im),
			})
		}

		if text, err := run("pdftotext", path, "-"); err == nil {
			doc.HasText = strings.TrimSpace(text) != ""
		}
		o.Documents[d.Name] = doc
	}

	buf, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	out := filepath.Join("testdata", "oracle", "poppler.json")
	if err := os.WriteFile(out, append(buf, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", out)
}

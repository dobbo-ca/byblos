//go:build ignore

// Command ink reports ink coverage and pairwise difference for the byb-8b9.6
// renders. Pure Go on purpose: the measurement should not add a seventh
// external CLI while byb-vv4 is open about the six already here.
//
// "Ink" is the mean darkness, 0 = white page, 1 = solid black. It is the one
// number that decides whether a synthesised arm is even in the right range;
// two pages can share it and still look nothing alike, so it is a necessary
// check, not a sufficient one -- the eye still decides.
package main

import (
	"fmt"
	"image"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
)

func load(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	im, _, err := image.Decode(f)
	return im, err
}

// lum returns luminance in [0,1] using Rec.601, matching how a reader
// perceives a greyish page rather than raw channel means.
func lum(im image.Image, x, y int) float64 {
	r, g, b, _ := im.At(x, y).RGBA()
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 65535.0
}

func stats(im image.Image) (ink, dark float64) {
	b := im.Bounds()
	var sum float64
	var n, d int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			l := lum(im, x, y)
			sum += 1 - l
			if l < 0.5 {
				d++
			}
			n++
		}
	}
	return sum / float64(n), float64(d) / float64(n)
}

// diff returns the mean absolute luminance difference and the fraction of
// pixels differing by more than 10% of full scale.
func diff(a, c image.Image) (mad, frac float64, err error) {
	ab, cb := a.Bounds(), c.Bounds()
	if ab != cb {
		return 0, 0, fmt.Errorf("bounds differ: %v vs %v", ab, cb)
	}
	var sum float64
	var n, big int
	for y := ab.Min.Y; y < ab.Max.Y; y++ {
		for x := ab.Min.X; x < ab.Max.X; x++ {
			d := math.Abs(lum(a, x, y) - lum(c, x, y))
			sum += d
			if d > 0.10 {
				big++
			}
			n++
		}
	}
	return sum / float64(n), float64(big) / float64(n), nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ink <dir-with-real.png-and-arms>")
		os.Exit(2)
	}
	dir := os.Args[1]
	real, err := load(filepath.Join(dir, "real.png"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ink:", err)
		os.Exit(1)
	}
	ri, rd := stats(real)
	fmt.Printf("%-10s ink=%.4f  dark=%.4f  (baseline)\n", "real", ri, rd)

	arms, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	for _, p := range arms {
		name := filepath.Base(p)
		name = name[:len(name)-len(".png")]
		if name == "real" {
			continue
		}
		im, err := load(p)
		if err != nil {
			fmt.Printf("%-10s LOAD ERROR %v\n", name, err)
			continue
		}
		ai, ad := stats(im)
		mad, frac, err := diff(real, im)
		if err != nil {
			fmt.Printf("%-10s %v\n", name, err)
			continue
		}
		fmt.Printf("%-10s ink=%.4f  dark=%.4f  | vs real: ink x%.2f  meanAbsDiff=%.4f  pixels>10%%=%.1f%%\n",
			name, ai, ad, ai/ri, mad, frac*100)
	}
}

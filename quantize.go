package byblos

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sort"
)

// QuantizePNG reduces img to at most colors distinct colours by median cut
// (with Lloyd/k-means refinement) and returns a complete, palette-indexed PNG
// file. See design spec byb-b3 section 1-2 for the accepted rationale.
//
// img must be opaque: image.Image.At().RGBA() returns alpha-premultiplied
// values, and quantizing those directly would be wrong, while an RGBA
// palette is a shape byblos has no use for (ReplaceImage refuses /SMask and
// /Mask outright). colors must be 2..256, exactly the range pngquant itself
// enforces -- clamping would silently substitute a different request than
// the one asked for.
func QuantizePNG(img image.Image, colors int) ([]byte, error) {
	if img == nil {
		return nil, fmt.Errorf("byblos: quantizepng: image is nil")
	}
	b := img.Bounds()
	if b.Empty() {
		return nil, fmt.Errorf("byblos: quantizepng: image bounds %v are empty", b)
	}
	if colors < 2 || colors > 256 {
		return nil, fmt.Errorf("byblos: quantizepng: colors %d is outside 2..256", colors)
	}

	// Histogram at full 24-bit precision: packed 0xRRGGBB -> pixel count.
	hist := map[uint32]int{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a != 0xffff {
				return nil, fmt.Errorf("byblos: quantizepng: image is not opaque at (%d,%d)", x, y)
			}
			packed := uint32(r>>8)<<16 | uint32(g>>8)<<8 | uint32(bl>>8)
			hist[packed]++
		}
	}

	buckets := make([]histBucket, 0, len(hist))
	for packed, n := range hist {
		buckets = append(buckets, histBucket{uint8(packed >> 16), uint8(packed >> 8), uint8(packed), n})
	}
	// Sort for determinism: map iteration order is randomized, and every
	// following step (box splitting, palette assignment) must not depend on
	// it.
	sort.Slice(buckets, func(i, j int) bool {
		bi, bj := buckets[i], buckets[j]
		if bi.r != bj.r {
			return bi.r < bj.r
		}
		if bi.g != bj.g {
			return bi.g < bj.g
		}
		return bi.bl < bj.bl
	})

	palette := medianCutPalette(buckets, colors)

	// Lloyd (k-means) refinement, work-budgeted.
	iters := 20_000_000 / (len(buckets) * len(palette))
	if iters < 1 {
		iters = 1
	}
	if iters > 10 {
		iters = 10
	}
	palette = lloydRefine(buckets, palette, iters)

	// Reorder the palette by descending pixel population (ties broken by
	// ascending RGB, for determinism), matching pngquant. This is purely a
	// relabelling -- nearest-entry assignment, and therefore every pixel's
	// colour, is unchanged -- but the resulting index stream is not: PNG
	// filters and the deflate/LZ77 stage that follows both operate on the
	// index bytes, not the colours behind them. On a scanned page the
	// dominant colour is the paper, and putting it at index 0 makes the
	// paper's index equal the filter type ("None") for predicted rows, both
	// 0x00, so a long paper run becomes one uninterrupted run of zero bytes
	// that deflate can match across scanline boundaries. At any other index
	// the same run is broken by the intervening non-zero filter byte at
	// every row, capping match length at one scanline. Measured on this
	// corpus (Scanpage): byblos's longest single-byte run jumps from 150
	// bytes to pngquant's 10,448 once the dominant colour is at index 0.
	//
	// The tally is computed explicitly (rather than reusing lloydRefine's
	// internal accums) because lloydRefine can return early once nothing
	// moves, in which case its last accums were computed against the
	// palette from the PREVIOUS iteration, not the one it returns.
	pixelCount := make([]int, len(palette))
	for _, bkt := range buckets {
		pixelCount[nearestPaletteIndex(palette, bkt.r, bkt.g, bkt.bl)] += bkt.n
	}
	order := make([]int, len(palette))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		oi, oj := order[i], order[j]
		if pixelCount[oi] != pixelCount[oj] {
			return pixelCount[oi] > pixelCount[oj]
		}
		ci, cj := palette[oi], palette[oj]
		if ci.r != cj.r {
			return ci.r < cj.r
		}
		if ci.g != cj.g {
			return ci.g < cj.g
		}
		return ci.bl < cj.bl
	})
	unordered := palette
	reordered := make([]rgb24, len(palette))
	rank := make([]uint8, len(palette)) // old (unordered) index -> new index
	for newIdx, oldIdx := range order {
		reordered[newIdx] = unordered[oldIdx]
		rank[oldIdx] = uint8(newIdx)
	}
	palette = reordered

	// Build the output image, mapping each pixel to its nearest palette
	// entry, memoized on the exact 24-bit colour. Nearest-entry lookup runs
	// against unordered (the palette in its pre-reorder array order) and is
	// then remapped through rank, rather than searching reordered directly:
	// nearestPaletteIndex breaks exact-distance ties by earliest array
	// position, so searching reordered would let the reorder itself silently
	// change which entry a tied pixel lands on -- contradicting the pixel
	// counts the reorder was computed from just above.
	pal := make(color.Palette, len(palette))
	for i, c := range palette {
		pal[i] = color.RGBA{c.r, c.g, c.bl, 255}
	}
	out := image.NewPaletted(image.Rect(0, 0, b.Dx(), b.Dy()), pal)

	nearest := map[uint32]uint8{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			packed := uint32(r>>8)<<16 | uint32(g>>8)<<8 | uint32(bl>>8)
			idx, ok := nearest[packed]
			if !ok {
				idx = rank[nearestPaletteIndex(unordered, uint8(r>>8), uint8(g>>8), uint8(bl>>8))]
				nearest[packed] = idx
			}
			out.SetColorIndex(x-b.Min.X, y-b.Min.Y, idx)
		}
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, out); err != nil {
		return nil, fmt.Errorf("byblos: quantizepng: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// rgb24 is one palette entry, always a true 8-bit colour: the weighted mean
// of the pixels assigned to it, not a box centre.
type rgb24 struct{ r, g, bl uint8 }

// histBucket is one distinct colour in the source image's histogram, and its
// pixel count.
type histBucket struct {
	r, g, bl uint8
	n        int
}

// medianCutPalette runs median cut over buckets (a histogram, sorted for
// determinism) down to at most n colours.
func medianCutPalette(buckets []histBucket, n int) []rgb24 {
	// Work on index sets rather than re-sorting the shared slice: each box
	// keeps its own slice of bucket indices so a split only touches its own
	// members.
	idx := make([]int, len(buckets))
	for i := range idx {
		idx[i] = i
	}

	type workBox struct {
		members []int
	}
	boxes := []workBox{{members: idx}}

	popOf := func(members []int) int {
		total := 0
		for _, i := range members {
			total += buckets[i].n
		}
		return total
	}
	meanOf := func(members []int) (float64, float64, float64, int) {
		var sr, sg, sb float64
		var total int
		for _, i := range members {
			bkt := buckets[i]
			w := float64(bkt.n)
			sr += w * float64(bkt.r)
			sg += w * float64(bkt.g)
			sb += w * float64(bkt.bl)
			total += bkt.n
		}
		if total == 0 {
			return 0, 0, 0, 0
		}
		return sr / float64(total), sg / float64(total), sb / float64(total), total
	}
	varianceOf := func(members []int) float64 {
		mr, mg, mb, total := meanOf(members)
		if total == 0 {
			return 0
		}
		var v float64
		for _, i := range members {
			bkt := buckets[i]
			dr := float64(bkt.r) - mr
			dg := float64(bkt.g) - mg
			db := float64(bkt.bl) - mb
			v += float64(bkt.n) * (dr*dr + dg*dg + db*db)
		}
		return v
	}

	for len(boxes) < n {
		// Pick the box with the greatest population-weighted variance that
		// can still be split (more than one distinct bucket).
		best := -1
		bestVar := -1.0
		for i, bx := range boxes {
			if len(bx.members) < 2 {
				continue
			}
			if popOf(bx.members) == 0 {
				continue
			}
			v := varianceOf(bx.members)
			if v > bestVar {
				bestVar = v
				best = i
			}
		}
		if best < 0 {
			break // nothing left worth splitting
		}
		bx := boxes[best]

		// Split axis: greatest range in the box.
		minR, maxR := uint8(255), uint8(0)
		minG, maxG := uint8(255), uint8(0)
		minB, maxB := uint8(255), uint8(0)
		for _, i := range bx.members {
			bkt := buckets[i]
			if bkt.r < minR {
				minR = bkt.r
			}
			if bkt.r > maxR {
				maxR = bkt.r
			}
			if bkt.g < minG {
				minG = bkt.g
			}
			if bkt.g > maxG {
				maxG = bkt.g
			}
			if bkt.bl < minB {
				minB = bkt.bl
			}
			if bkt.bl > maxB {
				maxB = bkt.bl
			}
		}
		rangeR := int(maxR) - int(minR)
		rangeG := int(maxG) - int(minG)
		rangeB := int(maxB) - int(minB)

		members := append([]int(nil), bx.members...)
		switch {
		case rangeR >= rangeG && rangeR >= rangeB:
			sort.Slice(members, func(i, j int) bool { return buckets[members[i]].r < buckets[members[j]].r })
		case rangeG >= rangeR && rangeG >= rangeB:
			sort.Slice(members, func(i, j int) bool { return buckets[members[i]].g < buckets[members[j]].g })
		default:
			sort.Slice(members, func(i, j int) bool { return buckets[members[i]].bl < buckets[members[j]].bl })
		}

		// Split at the population-weighted median.
		total := popOf(members)
		half := total / 2
		cum := 0
		split := len(members) - 1
		for i, mi := range members {
			cum += buckets[mi].n
			if cum >= half {
				split = i
				break
			}
		}
		if split < 0 {
			split = 0
		}
		if split >= len(members)-1 {
			split = len(members) - 2
		}
		if split < 0 {
			split = 0
		}
		left := members[:split+1]
		right := members[split+1:]
		if len(right) == 0 {
			break
		}

		boxes[best] = workBox{members: left}
		boxes = append(boxes, workBox{members: right})
	}

	out := make([]rgb24, 0, len(boxes))
	for _, bx := range boxes {
		mr, mg, mb, total := meanOf(bx.members)
		if total == 0 {
			continue
		}
		out = append(out, rgb24{
			r:  uint8(mr + 0.5),
			g:  uint8(mg + 0.5),
			bl: uint8(mb + 0.5),
		})
	}
	if len(out) == 0 && len(buckets) > 0 {
		// Degenerate: a single distinct colour.
		out = append(out, rgb24{buckets[0].r, buckets[0].g, buckets[0].bl})
	}
	return out
}

// lloydRefine assigns each histogram bucket to its nearest palette entry and
// moves each entry to the weighted mean of what it won, iters times, stopping
// early when nothing moves.
func lloydRefine(buckets []histBucket, palette []rgb24, iters int) []rgb24 {
	pal := append([]rgb24(nil), palette...)
	for it := 0; it < iters; it++ {
		type accum struct {
			sr, sg, sb float64
			n          int
		}
		accums := make([]accum, len(pal))
		moved := false
		for _, bkt := range buckets {
			best := nearestPaletteIndex(pal, bkt.r, bkt.g, bkt.bl)
			w := float64(bkt.n)
			accums[best].sr += w * float64(bkt.r)
			accums[best].sg += w * float64(bkt.g)
			accums[best].sb += w * float64(bkt.bl)
			accums[best].n += bkt.n
		}
		next := make([]rgb24, len(pal))
		for i, a := range accums {
			if a.n == 0 {
				next[i] = pal[i] // no pixels assigned: keep it where it was
				continue
			}
			nr := uint8(a.sr/float64(a.n) + 0.5)
			ng := uint8(a.sg/float64(a.n) + 0.5)
			nb := uint8(a.sb/float64(a.n) + 0.5)
			if nr != pal[i].r || ng != pal[i].g || nb != pal[i].bl {
				moved = true
			}
			next[i] = rgb24{nr, ng, nb}
		}
		pal = next
		if !moved {
			break
		}
	}
	return pal
}

// nearestPaletteIndex returns the index of pal's entry nearest to (r,g,b) by
// unweighted Euclidean RGB distance -- PSNR is unweighted MSE, so this is its
// optimum.
func nearestPaletteIndex(pal []rgb24, r, g, b uint8) int {
	best := 0
	bestD := -1
	for i, c := range pal {
		dr := int(c.r) - int(r)
		dg := int(c.g) - int(g)
		db := int(c.bl) - int(b)
		d := dr*dr + dg*dg + db*db
		if bestD < 0 || d < bestD {
			bestD = d
			best = i
		}
	}
	return best
}

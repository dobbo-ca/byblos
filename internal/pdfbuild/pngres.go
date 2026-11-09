package pdfbuild

import (
	"encoding/binary"
	"math"
)

// PNGPhys collects a PNG's pHYs chunks as Pillow reads them for img2pdf: the
// last chunk of unit 1 (metres) and the last of unit 0 (an aspect ratio only).
type PNGPhys struct {
	metre, aspect       [2]uint32
	hasMetre, hasAspect bool
}

// Add reads one pHYs chunk body; one that is too short or has another unit
// changes nothing.
func (p *PNGPhys) Add(b []byte) {
	if len(b) < 9 {
		return
	}
	v := [2]uint32{binary.BigEndian.Uint32(b), binary.BigEndian.Uint32(b[4:])}
	switch b[8] {
	case 1:
		p.metre, p.hasMetre = v, true
	case 0:
		p.aspect, p.hasAspect = v, true
	}
}

// Resolution is the resolution img2pdf takes from the chunks, in whole pixels
// per inch (x, then y): the pixels per metre of unit 1; failing that, for unit
// 0, img2pdf's 96 dpi default scaled along the axis the ratio favours. Zero if
// the chunks give none.
func (p *PNGPhys) Resolution() (x, y float64) {
	switch a := p.aspect; {
	case p.hasMetre:
		return math.RoundToEven(float64(p.metre[0]) * 0.0254), math.RoundToEven(float64(p.metre[1]) * 0.0254)
	case p.hasAspect && a[0] != 0 && a[1] != 0:
		if a[0] > a[1] {
			return math.RoundToEven(float64(96*uint64(a[0])) / float64(a[1])), 96
		}
		return 96, math.RoundToEven(float64(96*uint64(a[1])) / float64(a[0]))
	}
	return 0, 0
}

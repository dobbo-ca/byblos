package jbig2

import (
	"fmt"
	"strings"
)

// Profile describes what a stream's segment headers say, without decoding any of
// them. It exists for the byb-9v0 census: pricing what is left after this
// decoder lands needs the coding VARIANT of every segment, and the decoder's own
// error reports only the first one it refuses.
//
// IT IS THE DECODER'S PARSE, NOT A SECOND ONE. Everything below comes from
// parseSymbolDictHeader, parseTextRegionHeader and parseRegionInfo -- the
// functions planStream and the decoders call. A census written against its own
// header walk would be measuring the census. That is not a hypothetical: the
// existing probe reads segment types back out of an error STRING, and says so,
// precisely because there was no other way to ask before this existed.
//
// It reports every segment rather than stopping at the first unsupported one,
// which is the other thing the error cannot do. A stream whose first refusal is
// a text region may also hold a Huffman dictionary, and counting it under the
// text region alone would price the remaining work against the wrong feature.
type Profile struct {
	Number uint32
	Type   byte
	// Feature is a short stable token naming the coding variant, for grouping.
	// It is "" for a segment type with no variants worth counting.
	Feature string
	// Err is the header parse failure, or "".
	Err string
}

// ProfileStream reports one entry per segment of globals followed by s. A stream
// whose SEGMENT FRAMING does not parse yields no entries and an error; a stream
// whose framing parses but whose individual headers do not yields entries with
// Err set, because the two are different findings.
func ProfileStream(globals, s []byte) ([]Profile, error) {
	if len(globals) > 0 {
		joined := make([]byte, 0, len(globals)+len(s))
		joined = append(joined, globals...)
		s = append(joined, s...)
	}
	parsed, err := parseSegments(s)
	if err != nil {
		return nil, err
	}
	out := make([]Profile, 0, len(parsed.segs))
	for _, sg := range parsed.segs {
		p := Profile{Number: sg.number, Type: sg.typ}
		switch sg.typ {
		case segTypeSymbolDictionary:
			h, err := parseSymbolDictHeader(sg.data)
			if err != nil {
				p.Err = err.Error()
				break
			}
			p.Feature = fmt.Sprintf("sd huff=%d refagg=%d tmpl=%d rtmpl=%d ctx=%d at=%s",
				b2i(h.huff), b2i(h.refAgg), h.template, h.rTemplate, b2i(h.ctxUsed), atToken(h.at, h.nAT, h.template))
			// The Huffman table selectors, which say whether a table segment has
			// to be implemented as well, and the refinement AT, which says which
			// refinement template a dictionary would need. Both are appended
			// only when they mean something, so the common arithmetic token
			// stays one string and groups cleanly.
			if h.huff {
				p.Feature += fmt.Sprintf(" dh=%d dw=%d bm=%d agg=%d", h.huffDH, h.huffDW, h.huffBM, h.huffAgg)
			}
			if h.refAgg && h.rTemplate == 0 {
				p.Feature += fmt.Sprintf(" rat=%d/%d,%d/%d",
					h.rat[0][0], h.rat[0][1], h.rat[1][0], h.rat[1][1])
			}

		case segTypeIntermediateTextRegion, segTypeImmediateTextRegion, segTypeImmediateLosslessTextRegion:
			h, err := parseTextRegionHeader(sg.data)
			if err != nil {
				p.Err = err.Error()
				break
			}
			p.Feature = fmt.Sprintf("tr huff=%d refine=%d strips=%d corner=%d transposed=%d combop=%d defpix=%d dsoff=%d",
				b2i(h.huff), b2i(h.refine), 1<<h.logStrips, h.refCorner,
				b2i(h.transposed), h.combOp, h.defPixel, h.dsOffset)
			if h.huff {
				p.Feature += fmt.Sprintf(" huffflags=%#04x", h.huffFlags)
			}
			if h.refine {
				p.Feature += fmt.Sprintf(" rtmpl=%d", h.rTemplate)
				if h.rTemplate == 0 {
					p.Feature += fmt.Sprintf(" rat=%d/%d,%d/%d",
						h.rat[0][0], h.rat[0][1], h.rat[1][0], h.rat[1][1])
				}
			}

		case segTypeIntermediateGenericRegion, segTypeImmediateGenericRegion, segTypeImmediateLosslessGenericRegion:
			if len(sg.data) < 18 {
				p.Err = fmt.Sprintf("generic region segment is %d bytes; want at least 18", len(sg.data))
				break
			}
			f := sg.data[17]
			tmpl := int((f >> 1) & 3)
			// An MMR region carries no AT field at all, and a truncated one
			// carries less than it declares; the two are different findings and
			// neither is "the AT pixels were moved".
			at := "n/a"
			switch {
			case f&0x01 != 0:
			case len(sg.data) < 18+atFieldBytes(tmpl):
				at = "truncated"
			default:
				at = atToken(readAT(sg.data[18:], tmpl), atPairs(tmpl), tmpl)
			}
			p.Feature = fmt.Sprintf("gr mmr=%d tmpl=%d tpgdon=%d ext=%d at=%s",
				f&1, tmpl, (f>>3)&1, (f>>4)&1, at)
		}
		out = append(out, p)
	}
	return out, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// atPairs is the number of AT pixels a generic region template carries: four for
// template 0 and one for the others (T.88 7.4.6.3).
func atPairs(template int) int {
	if template == 0 {
		return 4
	}
	return 1
}

func atFieldBytes(template int) int { return 2 * atPairs(template) }

func readAT(d []byte, template int) [4][2]int8 {
	var at [4][2]int8
	for i := range atPairs(template) {
		at[i] = [2]int8{int8(d[2*i]), int8(d[2*i+1])}
	}
	return at
}

// nominalATForTemplate is T.88 Table 5. Template 0 uses four pixels; templates 1
// to 3 use one, at (3,-1), (2,-1) and (2,-1).
func nominalATForTemplate(template int) [4][2]int8 {
	switch template {
	case 0:
		return nominalAT
	case 1:
		return [4][2]int8{{3, -1}}
	default:
		return [4][2]int8{{2, -1}}
	}
}

// atToken renders an AT field as "nominal" or as the pairs themselves, so the
// census can count how many streams move a template pixel -- which is the
// difference between a region this decoder reads and one it refuses.
func atToken(at [4][2]int8, n, template int) string {
	// A Huffman symbol dictionary carries no AT field at all (T.88 7.4.4.1.2),
	// so there is nothing to call nominal or otherwise.
	if n == 0 {
		return "n/a"
	}
	if want := nominalATForTemplate(template); at == want {
		return "nominal"
	}
	parts := make([]string, n)
	for i := range n {
		parts[i] = fmt.Sprintf("%d/%d", at[i][0], at[i][1])
	}
	return strings.Join(parts, ",")
}

package jbig2

import "fmt"

// The symbol dictionary decoding procedure of T.88 6.5.5, arithmetic variant.
//
// A symbol dictionary is a list of small bitmaps and nothing else. It paints
// nothing: it is consumed by the text regions that refer to it, which place its
// entries on a page. That is why a stream can carry one in a PDF /JBIG2Globals
// object shared by every page of a document -- the dictionary is the document's
// alphabet and the per-page stream is the typesetting.
//
// THE SHAPE OF THE CODING IS HEIGHT CLASSES. Symbols are emitted sorted by
// height, in runs of equal height, and only the DELTAS are coded: the height
// delta from the previous class, then within a class the width delta from the
// previous symbol. A class ends on OOB from the width decoder. That is the whole
// structure, and it is why nothing in the segment header says how big any symbol
// is -- which is the fact the resource budget has to be built around, because
// every other bitmap in this package is sized by a header before it is
// allocated. See streamBudget.

// decodeSymbolDict decodes one symbol dictionary segment and returns the symbols
// it EXPORTS, which is not the same list as the symbols it decodes: a dictionary
// may re-export symbols it was given as input and may withhold symbols it
// decoded (T.88 6.5.10), and the export flags at the end of the segment say
// which.
//
// input is the concatenation of the exported symbols of the dictionaries this
// one refers to, in the order the header refers to them.
func decodeSymbolDict(sg segment, input []*Bitmap, budget *streamBudget) ([]*Bitmap, error) {
	h, err := parseSymbolDictHeader(sg.data)
	if err != nil {
		return nil, err
	}
	// Refused a second time, having been refused once from the header walk in
	// planStream. Not redundant: this function is reachable on its own, and what
	// it guards is that the MQ decoder yields a decision for any input, so a
	// dictionary decoded under the wrong assumption is a page of plausible wrong
	// glyphs rather than an error.
	if err := h.unsupported(); err != nil {
		return nil, err
	}
	if err := budget.chargeSymbols(int64(h.numNew)); err != nil {
		return nil, err
	}

	d := newDecoder(sg.data[h.dataOff:])
	// One context array for every symbol in the dictionary (T.88 6.5.8.1): the
	// statistics learned on the first glyph are what compress the second, so
	// this is allocated here and not per symbol.
	gb := make(contexts, 1<<16)
	// IADH, IADW and IAEX and no others. T.88 6.5 lists IAAI and the IARD*
	// contexts beside them, and every one of those is read only under SDREFAGG,
	// which unsupported() refused above.
	iadh, iadw, iaex := newIntContexts(), newIntContexts(), newIntContexts()

	newSyms := make([]*Bitmap, 0, min(int(h.numNew), maxStreamSymbols))
	hcHeight := 0
	for len(newSyms) < int(h.numNew) {
		dh, ok := decodeInt(d, iadh)
		if !ok {
			return nil, fmt.Errorf("jbig2: symbol dictionary: OOB where a height class delta was expected")
		}
		hcHeight += dh
		if hcHeight <= 0 {
			return nil, fmt.Errorf("jbig2: symbol dictionary: height class height is %d; heights are positive", hcHeight)
		}
		symWidth, inClass := 0, 0
		for {
			dw, ok := decodeInt(d, iadw)
			if !ok {
				break // OOB ends the height class (T.88 6.5.5 step 4c ii).
			}
			symWidth += dw
			if symWidth <= 0 {
				return nil, fmt.Errorf("jbig2: symbol dictionary: symbol width is %d; widths are positive", symWidth)
			}
			if len(newSyms) == int(h.numNew) {
				return nil, fmt.Errorf("jbig2: symbol dictionary declares %d new symbols and codes more", h.numNew)
			}
			if err := budget.chargeBitmap(int64(symWidth), int64(hcHeight)); err != nil {
				return nil, err
			}
			b, err := decodeGenericRegionGeneral(d, gb, symWidth, hcHeight, h.template, h.at, false)
			if err != nil {
				return nil, err
			}
			newSyms = append(newSyms, b)
			inClass++
		}
		// AN EMPTY HEIGHT CLASS IS REFUSED, AND THAT IS WHAT MAKES THIS LOOP
		// PROVABLY TERMINATE. Its only exit is having decoded SDNUMNEWSYMS
		// symbols, and a height class that codes none makes no progress towards
		// that. With this check every iteration appends at least one symbol, so
		// the loop runs at most SDNUMNEWSYMS times -- a count the header budget
		// already bounds. Without it there is no bound at all, because the
		// number of iterations is then set by the coded data and the coded data
		// does not run out: the MQ decoder yields a decision for any input and
		// reads 1 bits forever past the end (decoder.at), so what it decodes
		// after the last real byte is a repeating sequence, and a repeating
		// "positive height delta, then OOB" is an infinite loop on an input of
		// under a hundred bytes.
		//
		// THE HANG IS AN ARGUMENT, NOT AN OBSERVATION, and it is worth being
		// exact about which. Removing this check does not hang the fixture in
		// symbol_hang_test.go: there the tail happens to decode to a NEGATIVE
		// height delta and the check above catches it on the second iteration.
		// That is an accident of what those particular context states converge
		// to, not a bound -- nothing chooses the sign. The proof above does not
		// depend on finding an input that exhibits it.
		//
		// Refusing it costs nothing real: a class exists to carry symbols of one
		// height, so an empty one conveys nothing an encoder would want to say.
		if inClass == 0 {
			return nil, fmt.Errorf("jbig2: symbol dictionary codes a height class of %d rows "+
				"containing no symbols, having decoded %d of %d", hcHeight, len(newSyms), h.numNew)
		}
	}

	return exportSymbols(d, iaex, input, newSyms, int(h.numEx))
}

// exportSymbols runs the export flag procedure of T.88 6.5.10 over the input and
// newly decoded symbols and returns the ones flagged for export.
//
// The flags are coded as ALTERNATING RUN LENGTHS starting with a run of
// not-exported, so a dictionary that exports everything codes two numbers: a run
// of zero, then a run of everything. A run of zero is therefore legal and common
// and cannot be treated as the end of the list -- but two of them in a row would
// advance nothing, which is why the loop bounds its own iterations rather than
// trusting the stream to terminate it.
func exportSymbols(d *decoder, iaex contexts, input, decoded []*Bitmap, want int) ([]*Bitmap, error) {
	all := make([]*Bitmap, 0, len(input)+len(decoded))
	all = append(all, input...)
	all = append(all, decoded...)

	out := make([]*Bitmap, 0, min(want, len(all)))
	i, exporting := 0, false
	for range 2*len(all) + 2 {
		if i >= len(all) {
			break
		}
		run, ok := decodeInt(d, iaex)
		if !ok {
			return nil, fmt.Errorf("jbig2: symbol dictionary: OOB where an export run length was expected")
		}
		if run < 0 || i+run > len(all) {
			return nil, fmt.Errorf("jbig2: symbol dictionary: export run of %d at %d over %d symbols",
				run, i, len(all))
		}
		if exporting {
			out = append(out, all[i:i+run]...)
		}
		i += run
		exporting = !exporting
	}
	if i < len(all) {
		return nil, fmt.Errorf("jbig2: symbol dictionary: export runs cover %d of %d symbols", i, len(all))
	}
	// The count is in the header and the flags are in the coded data, so they
	// are two independent statements of the same fact. A disagreement means one
	// of them was mis-parsed, and continuing would hand every text region that
	// refers to this dictionary a symbol list of the wrong length -- which
	// renumbers every glyph rather than failing.
	if len(out) != want {
		return nil, fmt.Errorf("jbig2: symbol dictionary declares %d exported symbols and its export "+
			"flags select %d", want, len(out))
	}
	return out, nil
}

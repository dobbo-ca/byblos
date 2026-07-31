package pdfdoc

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"image"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// openCorpus opens a corpus document by name.
func openCorpus(t *testing.T, name string) Doc {
	t.Helper()
	raw, ok := corpus.ByName(name)
	if !ok {
		t.Fatalf("no corpus document named %q", name)
	}
	d, err := Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("opening %q: %v", name, err)
	}
	return d
}

// firstImage resolves the /Im0 image every corpus document names its raster.
func firstImage(t *testing.T, d Doc, page int) int {
	t.Helper()
	p, err := d.Page(page)
	if err != nil {
		t.Fatalf("page %d: %v", page, err)
	}
	xo, ok := d.XObject(p.Scope, "Im0")
	if !ok || !xo.Image {
		t.Fatalf("page %d has no /Im0 image", page)
	}
	return xo.ID
}

func writeDoc(t *testing.T, d Doc) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := d.Write(&out); err != nil {
		t.Fatalf("write: %v", err)
	}
	return out.Bytes()
}

func inflate(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("inflate: %v", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("inflate: %v", err)
	}
	return out
}

func deflateAt(t *testing.T, b []byte, level int) []byte {
	t.Helper()
	var z bytes.Buffer
	zw, err := zlib.NewWriterLevel(&z, level)
	if err != nil {
		t.Fatalf("deflate: %v", err)
	}
	if _, err := zw.Write(b); err != nil {
		t.Fatalf("deflate: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("deflate: %v", err)
	}
	return z.Bytes()
}

// 'indirect-kids' is a named regression fixture, per byb-0he.
//
// Through a bare api.ReadContext this document writes back as 594 bytes with
// zero pages, and both api.WriteContext and the subsequent re-read return nil.
// Nothing about the failure is observable except by counting what came out,
// which is what this test does. Open's normalizePageTree is the only thing
// standing between the write path and that outcome.
func TestIndirectKidsSurvivesAWriteRoundTrip(t *testing.T) {
	raw, _ := corpus.ByName("indirect-kids")
	before := openCorpus(t, "indirect-kids")

	type pageFacts struct {
		media      Rect
		contentLen int
		annots     int
	}
	facts := func(d Doc) []pageFacts {
		t.Helper()
		var got []pageFacts
		for n := 1; n <= d.PageCount(); n++ {
			p, err := d.Page(n)
			if err != nil {
				t.Fatalf("page %d: %v", n, err)
			}
			a, err := d.Annots(n)
			if err != nil {
				t.Fatalf("annots %d: %v", n, err)
			}
			got = append(got, pageFacts{p.MediaBox, len(p.Content), len(a)})
		}
		return got
	}

	want := facts(before)
	if len(want) != 2 {
		t.Fatalf("the fixture should have 2 pages, got %d", len(want))
	}
	if want[0].annots != 1 {
		t.Fatalf("page 1 of the fixture should have 1 annotation, got %d", want[0].annots)
	}

	out := writeDoc(t, before)
	after, err := Open(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("re-opening the written document: %v", err)
	}
	got := facts(after)

	if len(got) != len(want) {
		t.Fatalf("page count %d -> %d (input %d bytes, output %d bytes)",
			len(want), len(got), len(raw), len(out))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("page %d: %+v -> %+v", i+1, want[i], got[i])
		}
	}
}

// Every corpus document must survive a write round trip with its page count
// intact. indirect-kids is the one that historically did not, but a seam that
// only works on the document it was debugged against is not a seam.
func TestEveryCorpusDocumentSurvivesAWriteRoundTrip(t *testing.T) {
	for _, cd := range corpus.All() {
		t.Run(cd.Name, func(t *testing.T) {
			d, err := Open(bytes.NewReader(cd.Data))
			if err != nil {
				// 'malformed' is truncated on purpose and must not open.
				t.Skipf("does not open: %v", err)
			}
			want := d.PageCount()
			out := writeDoc(t, d)
			after, err := Open(bytes.NewReader(out))
			if err != nil {
				t.Fatalf("re-opening the written document: %v", err)
			}
			if got := after.PageCount(); got != want {
				t.Errorf("page count %d -> %d (%d bytes -> %d bytes)",
					want, got, len(cd.Data), len(out))
			}
		})
	}
}

// The substitution must actually reach the writer.
//
// pdfcpu's DereferenceStreamDict returns a pointer to a copy, so mutating
// StreamDict.Raw through it changes nothing the writer will see. The failure
// mode is a document that carries the NEW dictionary over the OLD payload and
// re-reads without error, so this asserts on the bytes rather than on whether
// the write returned nil.
func TestReplaceImageSubstitutesTheStreamBytes(t *testing.T) {
	d := openCorpus(t, "scan")
	id := firstImage(t, d, 1)

	original := append([]byte(nil), d.(*doc).streams[id].Raw...)
	samples := inflate(t, original)

	// Level 1 re-deflates the identical samples into different bytes.
	reencoded := deflateAt(t, samples, 1)
	if bytes.Equal(reencoded, original) {
		t.Fatal("the re-deflated stream is byte-identical, so this proves nothing")
	}

	info, _ := d.ImageInfo(id)
	err := d.ReplaceImage(id, EncodedImage{
		Width:      info.Width,
		Height:     info.Height,
		BPC:        8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       reencoded,
	})
	if err != nil {
		t.Fatalf("ReplaceImage: %v", err)
	}

	out := writeDoc(t, d)
	if bytes.Contains(out, original) {
		t.Error("the written document still contains the original stream bytes")
	}
	if !bytes.Contains(out, reencoded) {
		t.Fatal("the written document does not contain the substituted stream bytes")
	}

	after, err := Open(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("re-opening: %v", err)
	}
	id2 := firstImage(t, after, 1)
	if got := inflate(t, after.(*doc).streams[id2].Raw); !bytes.Equal(got, samples) {
		t.Errorf("samples changed: %d bytes in, %d bytes out", len(samples), len(got))
	}
}

// The acceptance criterion byb-0he names: pdfimages returns the substituted
// bitmap bit-identically. This runs with no encoder present -- the payload is
// the same pixels re-deflated -- so it exercises Raw, /Length and the
// dictionary rewrite independently of B2.
func TestReplaceImagePixelsSurvivePdfimages(t *testing.T) {
	if _, err := exec.LookPath("pdfimages"); err != nil {
		t.Skip("pdfimages not installed (poppler); this is the byb-0he acceptance gate")
	}

	raw, _ := corpus.ByName("scan")
	d := openCorpus(t, "scan")
	id := firstImage(t, d, 1)
	info, _ := d.ImageInfo(id)
	samples := inflate(t, d.(*doc).streams[id].Raw)

	if err := d.ReplaceImage(id, EncodedImage{
		Width:      info.Width,
		Height:     info.Height,
		BPC:        8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       deflateAt(t, samples, 1),
	}); err != nil {
		t.Fatalf("ReplaceImage: %v", err)
	}

	want := pdfimagesPixels(t, raw)
	got := pdfimagesPixels(t, writeDoc(t, d))
	if want == "" {
		t.Fatal("pdfimages extracted nothing from the original document")
	}
	if got != want {
		t.Errorf("pdfimages pixels differ after substitution:\n original %s\n rewritten %s", want, got)
	}
}

// pdfimagesPixels runs pdfimages over pdf and returns a digest of the pixels of
// every image it extracts, in order.
func pdfimagesPixels(t *testing.T, pdf []byte) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(src, pdf, 0o600); err != nil {
		t.Fatalf("writing the input: %v", err)
	}
	cmd := exec.Command("pdfimages", "-png", src, filepath.Join(dir, "img"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pdfimages: %v: %s", err, out)
	}
	pngs, err := filepath.Glob(filepath.Join(dir, "img-*.png"))
	if err != nil {
		t.Fatalf("globbing pdfimages output: %v", err)
	}
	var sb bytes.Buffer
	for _, p := range pngs {
		f, err := os.Open(p)
		if err != nil {
			t.Fatalf("opening %s: %v", p, err)
		}
		im, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			t.Fatalf("decoding %s: %v", p, err)
		}
		b := im.Bounds()
		sb.WriteString(filepath.Base(p))
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bl, a := im.At(x, y).RGBA()
				sb.Write([]byte{byte(r >> 8), byte(g >> 8), byte(bl >> 8), byte(a >> 8)})
			}
		}
	}
	return sha(sb.Bytes())
}

// The shape B3 produces: a quantized palette image is /Indexed samples under
// /FlateDecode with a PNG predictor, not embedded PNG bytes. byb-0he calls this
// out because a seam that only spoke /DeviceGray would have to be reopened.
func TestReplaceImageAcceptsIndexedFlatePredictor(t *testing.T) {
	d := openCorpus(t, "scan")
	id := firstImage(t, d, 1)
	info, _ := d.ImageInfo(id)

	// A 4-entry greyscale palette, one index per pixel, and the per-row filter
	// byte a PNG predictor requires.
	const hival = 3
	palette := []byte{0, 0, 0, 0x55, 0x55, 0x55, 0xAA, 0xAA, 0xAA, 0xFF, 0xFF, 0xFF}
	rowBytes := (info.Width*2 + 7) / 8
	rows := make([]byte, 0, (rowBytes+1)*info.Height)
	for y := 0; y < info.Height; y++ {
		rows = append(rows, 0) // PNG filter type None
		for i := 0; i < rowBytes; i++ {
			rows = append(rows, byte(0x1B))
		}
	}

	if err := d.ReplaceImage(id, EncodedImage{
		Width:  info.Width,
		Height: info.Height,
		BPC:    2,
		ColorSpace: ColorSpace{
			Name: "Indexed", Base: "DeviceRGB", HiVal: hival, Lookup: palette,
		},
		Filter: "FlateDecode",
		DecodeParms: &DecodeParms{
			Predictor: 15, Colors: 1, BitsPerComponent: 2, Columns: info.Width,
		},
		Data: deflateAt(t, rows, 6),
	}); err != nil {
		t.Fatalf("ReplaceImage: %v", err)
	}

	out := writeDoc(t, d)
	after, err := Open(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("re-opening: %v", err)
	}
	id2 := firstImage(t, after, 1)
	got, ok := after.ImageInfo(id2)
	if !ok {
		t.Fatal("the substituted image did not resolve")
	}
	if got.BPC != 2 {
		t.Errorf("/BitsPerComponent = %d, want 2", got.BPC)
	}
	if got.Width != info.Width || got.Height != info.Height {
		t.Errorf("dimensions %dx%d, want %dx%d", got.Width, got.Height, info.Width, info.Height)
	}
	// The palette and the predictor must have reached the file, not just the
	// dictionary this process holds.
	sd := after.(*doc).streams[id2]
	if _, ok := sd.Dict.Find("DecodeParms"); !ok {
		t.Error("/DecodeParms is absent from the written image dictionary")
	}
	if arr := after.(*doc).arrayEntry(sd.Dict, "ColorSpace"); len(arr) != 4 {
		t.Errorf("/ColorSpace is not a 4-element Indexed array: %v", sd.Dict["ColorSpace"])
	}
}

// A substitution that cannot be made correctly must fail loudly. Every case
// here is one where succeeding would produce a plausible, wrong document.
func TestReplaceImageRefusesWhatItCannotDoCorrectly(t *testing.T) {
	good := EncodedImage{
		Width: 4, Height: 4, BPC: 8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       []byte{1, 2, 3},
	}

	t.Run("unresolved id", func(t *testing.T) {
		d := openCorpus(t, "scan")
		if err := d.ReplaceImage(9999, good); err == nil {
			t.Error("replacing an unresolved id succeeded")
		}
	})

	t.Run("smask", func(t *testing.T) {
		d := openCorpus(t, "stacked-smask")
		p, _ := d.Page(1)
		var masked int
		for _, name := range []string{"Im0", "Im1"} {
			if xo, ok := d.XObject(p.Scope, name); ok && xo.Image {
				if info, _ := d.ImageInfo(xo.ID); info.SMask {
					masked = xo.ID
				}
			}
		}
		if masked == 0 {
			t.Skip("no /SMask image in this fixture")
		}
		if err := d.ReplaceImage(masked, good); err == nil {
			t.Error("replacing an /SMask image succeeded")
		}
	})

	bad := map[string]EncodedImage{
		"zero width":       {Width: 0, Height: 4, BPC: 8, ColorSpace: ColorSpace{Name: "DeviceGray"}, Filter: "FlateDecode", Data: []byte{1}},
		"bad bpc":          {Width: 4, Height: 4, BPC: 3, ColorSpace: ColorSpace{Name: "DeviceGray"}, Filter: "FlateDecode", Data: []byte{1}},
		"no filter":        {Width: 4, Height: 4, BPC: 8, ColorSpace: ColorSpace{Name: "DeviceGray"}, Data: []byte{1}},
		"no data":          {Width: 4, Height: 4, BPC: 8, ColorSpace: ColorSpace{Name: "DeviceGray"}, Filter: "FlateDecode"},
		"unknown space":    {Width: 4, Height: 4, BPC: 8, ColorSpace: ColorSpace{Name: "DeviceLab"}, Filter: "FlateDecode", Data: []byte{1}},
		"short palette":    {Width: 4, Height: 4, BPC: 8, ColorSpace: ColorSpace{Name: "Indexed", Base: "DeviceRGB", HiVal: 3, Lookup: []byte{1, 2, 3}}, Filter: "FlateDecode", Data: []byte{1}},
		"bad palette base": {Width: 4, Height: 4, BPC: 8, ColorSpace: ColorSpace{Name: "Indexed", Base: "DeviceLab", HiVal: 0, Lookup: []byte{1}}, Filter: "FlateDecode", Data: []byte{1}},
	}
	for name, img := range bad {
		t.Run(name, func(t *testing.T) {
			d := openCorpus(t, "scan")
			id := firstImage(t, d, 1)
			if err := d.ReplaceImage(id, img); err == nil {
				t.Errorf("%s was accepted", name)
			}
		})
	}
}

// A stale /Decode array silently inverts the substituted samples, so the
// rewrite must remove it.
func TestReplaceImageDropsAStaleDecodeArray(t *testing.T) {
	d := openCorpus(t, "scan")
	id := firstImage(t, d, 1)
	info, _ := d.ImageInfo(id)
	sd := d.(*doc).streams[id]
	samples := inflate(t, sd.Raw)

	// Plant the inverting /Decode a real document could carry.
	sd.Dict["Decode"] = types.Array{types.Integer(1), types.Integer(0)}

	if err := d.ReplaceImage(id, EncodedImage{
		Width: info.Width, Height: info.Height, BPC: 8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       deflateAt(t, samples, 1),
	}); err != nil {
		t.Fatalf("ReplaceImage: %v", err)
	}
	if _, ok := d.(*doc).streams[id].Dict.Find("Decode"); ok {
		t.Error("/Decode survived the substitution and will invert the new samples")
	}
	after, err := Open(bytes.NewReader(writeDoc(t, d)))
	if err != nil {
		t.Fatalf("re-opening: %v", err)
	}
	id2 := firstImage(t, after, 1)
	if _, ok := after.(*doc).streams[id2].Dict.Find("Decode"); ok {
		t.Error("/Decode was written to the file")
	}
}

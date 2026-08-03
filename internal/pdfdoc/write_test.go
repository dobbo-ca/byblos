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
	"strconv"
	"strings"
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

// downscaleGrayByHalf nearest-neighbour subsamples an 8bpc DeviceGray raster
// to half its width and height. It exists so tests can hand ReplaceImage a
// genuinely SMALLER raster -- different pixel content, not a re-deflate of
// the same samples at the same size -- without importing the root package's
// Downsample (byblos imports internal/pdfdoc, so the reverse import would
// cycle).
func downscaleGrayByHalf(samples []byte, w, h int) (out []byte, nw, nh int) {
	nw, nh = w/2, h/2
	out = make([]byte, nw*nh)
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			out[y*nw+x] = samples[(y*2)*w+(x*2)]
		}
	}
	return out, nw, nh
}

// pdfimagesRow is one data row of `pdfimages -list`.
type pdfimagesRow struct {
	Width, Height int
	XPPI, YPPI    float64
}

// pdfimagesList runs `pdfimages -list` over pdf and parses page 1 image 0's
// row: the geometry oracle this test uses to check placement independently
// of the CTM the code under test just wrote.
func pdfimagesList(t *testing.T, pdf []byte) pdfimagesRow {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(src, pdf, 0o600); err != nil {
		t.Fatalf("writing the input: %v", err)
	}
	out, err := exec.Command("pdfimages", "-list", src).CombinedOutput()
	if err != nil {
		t.Fatalf("pdfimages -list: %v: %s", err, out)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("pdfimages -list produced no data row:\n%s", out)
	}
	f := strings.Fields(lines[2])
	if len(f) < 14 {
		t.Fatalf("pdfimages -list row has %d fields, want >= 14: %q", len(f), lines[2])
	}
	width, err := strconv.Atoi(f[3])
	if err != nil {
		t.Fatalf("parsing width from %q: %v", lines[2], err)
	}
	height, err := strconv.Atoi(f[4])
	if err != nil {
		t.Fatalf("parsing height from %q: %v", lines[2], err)
	}
	xppi, err := strconv.ParseFloat(f[12], 64)
	if err != nil {
		t.Fatalf("parsing x-ppi from %q: %v", lines[2], err)
	}
	yppi, err := strconv.ParseFloat(f[13], 64)
	if err != nil {
		t.Fatalf("parsing y-ppi from %q: %v", lines[2], err)
	}
	return pdfimagesRow{Width: width, Height: height, XPPI: xppi, YPPI: yppi}
}

// pdfimagesGrayBytes runs `pdfimages -png` over pdf and returns the decoded
// 8bpc grayscale samples of the single extracted image, row-major.
func pdfimagesGrayBytes(t *testing.T, pdf []byte) []byte {
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
	if len(pngs) != 1 {
		t.Fatalf("pdfimages extracted %d images, want 1", len(pngs))
	}
	f, err := os.Open(pngs[0])
	if err != nil {
		t.Fatalf("opening %s: %v", pngs[0], err)
	}
	defer f.Close()
	im, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decoding %s: %v", pngs[0], err)
	}
	gray, ok := im.(*image.Gray)
	if !ok {
		t.Fatalf("%s decoded as %T, want *image.Gray", pngs[0], im)
	}
	b := gray.Bounds()
	out := make([]byte, 0, b.Dx()*b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := gray.Pix[gray.PixOffset(b.Min.X, y) : gray.PixOffset(b.Min.X, y)+b.Dx()]
		out = append(out, row...)
	}
	return out
}

// byb-a41: ReplaceImage has never been exercised with changed pixel
// dimensions. Every other ReplaceImage test passes the source dimensions
// straight through, which leaves the /Width and /Height rewrite (write.go)
// untested against a raster that actually differs in size -- exactly what
// downsampling (byb-fp6) will ask of it.
//
// This substitutes a genuinely smaller raster (half width and height,
// nearest-neighbour subsampled -- different pixel content, not the same
// pixels re-deflated) and checks:
//
//   - the new pixel dimensions reach the file;
//   - the pixels round-trip;
//   - the page's PLACEMENT of the image is unchanged.
//
// READ THIS BEFORE TRUSTING THE PLACEMENT ARM. The content-stream byte
// comparison below has NO kill power over ReplaceImage, and it is not the
// placement guarantee. ReplaceImage never touches /Contents, so that
// comparison holds under every possible defect in it -- byb-a41's mutation
// pass confirmed it stays green even against a ReplaceImage stubbed out to do
// nothing at all. It earns its place as a NEGATIVE CONTROL: it pins the
// invariant that substituting an image must not rewrite the page's content
// stream, and it would redden if some future change started doing so. That is
// worth asserting. It is not evidence that the CTM survived.
//
// What actually holds placement is that the CTM is untouched by construction
// and the raster is reinterpreted under it; what FAILS when the dimension
// rewrite is wrong is the reopen /Width x /Height check and the pdfimages
// arms. The pdfimages ppi cross-check confirms SCALE only, and coarsely (see
// the comment at its tolerance); it cannot see translation at all, because
// `pdfimages -list` reports no x/y offset.
//
// The pixel-dimension and content-stream checks need no oracle, but the
// exec.LookPath guard below sits above all of them, so this test skips
// entirely in test-no-oracles rather than running its oracle-free half.
func TestReplaceImageWithSmallerDimensionsPreservesPlacement(t *testing.T) {
	raw, _ := corpus.ByName("scan")
	d := openCorpus(t, "scan")
	id := firstImage(t, d, 1)
	info, _ := d.ImageInfo(id)
	samples := inflate(t, d.(*doc).streams[id].Raw)

	small, nw, nh := downscaleGrayByHalf(samples, info.Width, info.Height)
	if nw >= info.Width || nh >= info.Height {
		t.Fatalf("downscale did not shrink the raster: %dx%d -> %dx%d", info.Width, info.Height, nw, nh)
	}

	pageBefore, err := d.Page(1)
	if err != nil {
		t.Fatalf("page 1 before substitution: %v", err)
	}
	contentBefore := append([]byte(nil), pageBefore.Content...)

	if err := d.ReplaceImage(id, EncodedImage{
		Width:      nw,
		Height:     nh,
		BPC:        8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       deflateAt(t, small, 6),
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
	if got.Width != nw || got.Height != nh {
		t.Errorf("/Width x /Height = %dx%d, want %dx%d", got.Width, got.Height, nw, nh)
	}

	pageAfter, err := after.Page(1)
	if err != nil {
		t.Fatalf("page 1 after substitution: %v", err)
	}
	if !bytes.Equal(pageAfter.Content, contentBefore) {
		t.Errorf("page content stream changed after ReplaceImage; placement CTM may have moved:\n before %q\n after  %q",
			contentBefore, pageAfter.Content)
	}

	if _, err := exec.LookPath("pdfimages"); err != nil {
		t.Skip("pdfimages not installed (poppler); this is the byb-a41 acceptance gate")
	}

	wantRow := pdfimagesList(t, raw)
	gotRow := pdfimagesList(t, out)
	if gotRow.Width != nw || gotRow.Height != nh {
		t.Errorf("pdfimages -list reports %dx%d, want %dx%d", gotRow.Width, gotRow.Height, nw, nh)
	}
	// The image's physical size on the page (pixels / ppi, in inches, times
	// 72), computed from pdfimages' own numbers, must be unchanged even
	// though the pixel dimensions and therefore the ppi both halved. This is
	// a scale-only cross-check: pdfimages quantizes ppi to an integer, and
	// at these pixel counts one integer step of ppi swings physW/H by tens
	// of points, far more than tol -- it passes here only because both
	// sides land on exact integer ppi by construction of the fixture. Measured
	// (byb-a41): at the rewritten size one integer ppi step moves physW by
	// ~32pt, so this arm is blind to a placement error of up to ~+/-16pt, and
	// widening tol to 1e9 does not redden anything -- its real kills all come
	// from the gotRow.Width/Height comparison just above, not from this
	// tolerance. Do NOT read the content-stream check above as covering the
	// gap: it cannot fail for any ReplaceImage defect at all (see the doc
	// comment on this test).
	wantPhysW := float64(wantRow.Width) / wantRow.XPPI * 72
	wantPhysH := float64(wantRow.Height) / wantRow.YPPI * 72
	gotPhysW := float64(gotRow.Width) / gotRow.XPPI * 72
	gotPhysH := float64(gotRow.Height) / gotRow.YPPI * 72
	const tol = 0.5 // points; exact only when both sides quantize to the same integer ppi
	if diff := gotPhysW - wantPhysW; diff < -tol || diff > tol {
		t.Errorf("placement width moved: %.2fpt -> %.2fpt", wantPhysW, gotPhysW)
	}
	if diff := gotPhysH - wantPhysH; diff < -tol || diff > tol {
		t.Errorf("placement height moved: %.2fpt -> %.2fpt", wantPhysH, gotPhysH)
	}

	gotPixels := pdfimagesGrayBytes(t, out)
	if !bytes.Equal(gotPixels, small) {
		t.Errorf("pdfimages pixels do not match the substituted raster: %d bytes vs %d bytes", len(gotPixels), len(small))
	}
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

	// ISO 32000-1 7.3.8.1: every stream shall be an indirect object. There is
	// no corpus document with a direct image stream, so the precondition is
	// synthesized the same way a real one would present it to ReplaceImage:
	// no entry in d.refs for the id.
	t.Run("direct object", func(t *testing.T) {
		d := openCorpus(t, "scan")
		id := firstImage(t, d, 1)
		delete(d.(*doc).refs, id)
		if err := d.ReplaceImage(id, good); err == nil {
			t.Error("replacing an image with no cross-reference entry succeeded")
		}
	})

	bad := map[string]EncodedImage{
		"zero width":       {Width: 0, Height: 4, BPC: 8, ColorSpace: ColorSpace{Name: "DeviceGray"}, Filter: "FlateDecode", Data: []byte{1}},
		"zero height":      {Width: 4, Height: 0, BPC: 8, ColorSpace: ColorSpace{Name: "DeviceGray"}, Filter: "FlateDecode", Data: []byte{1}},
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

// ImageInfo(id) must reflect the new dimensions immediately after
// ReplaceImage returns -- byb-fp6's write seam and any caller that inspects
// the document before Write depends on this, not just on what eventually
// lands in the file.
func TestReplaceImageInfoReflectsTheNewDimensionsImmediately(t *testing.T) {
	d := openCorpus(t, "scan")
	id := firstImage(t, d, 1)
	info, _ := d.ImageInfo(id)
	samples := inflate(t, d.(*doc).streams[id].Raw)

	small, nw, nh := downscaleGrayByHalf(samples, info.Width, info.Height)
	if nw == nh {
		t.Fatalf("the fixture downscaled to a square %dx%d; this test cannot catch a transposed width/height", nw, nh)
	}

	if err := d.ReplaceImage(id, EncodedImage{
		Width: nw, Height: nh, BPC: 8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       deflateAt(t, small, 6),
	}); err != nil {
		t.Fatalf("ReplaceImage: %v", err)
	}

	got, ok := d.ImageInfo(id)
	if !ok {
		t.Fatal("ImageInfo no longer resolves the id after ReplaceImage")
	}
	if got.Width != nw || got.Height != nh {
		t.Errorf("ImageInfo() after ReplaceImage = %dx%d, want %dx%d", got.Width, got.Height, nw, nh)
	}
}

// A filter and pipeline a prior encoding left behind must not survive a
// substitution that declares a different one: Decode would silently
// misinterpret the new bytes as the old codec.
func TestReplaceImageRewritesFilterAndFilterPipeline(t *testing.T) {
	d := openCorpus(t, "scan")
	id := firstImage(t, d, 1)
	info, _ := d.ImageInfo(id)
	sd := d.(*doc).streams[id]
	samples := inflate(t, sd.Raw)

	// Plant a stale filter and pipeline, distinct from what this
	// substitution declares.
	sd.Dict["Filter"] = types.Name("DCTDecode")
	sd.FilterPipeline = []types.PDFFilter{{Name: "DCTDecode"}}

	newSamples := append([]byte(nil), samples...)
	for i := range newSamples {
		newSamples[i] ^= 0xFF // distinct content, so decoding through the wrong pipeline is visible
	}
	data := deflateAt(t, newSamples, 6)

	if err := d.ReplaceImage(id, EncodedImage{
		Width: info.Width, Height: info.Height, BPC: 8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       data,
	}); err != nil {
		t.Fatalf("ReplaceImage: %v", err)
	}

	if got, ok := sd.Dict.Find("Filter"); !ok || got != types.Name("FlateDecode") {
		t.Errorf("/Filter = %v, want FlateDecode", got)
	}
	if len(sd.FilterPipeline) != 1 || sd.FilterPipeline[0].Name != "FlateDecode" {
		t.Errorf("FilterPipeline = %v, want a single FlateDecode stage", sd.FilterPipeline)
	}

	// Prove it rather than just asserting it: decode through the pipeline
	// this process now holds and check the samples that come back.
	if err := sd.Decode(); err != nil {
		t.Fatalf("Decode with the rewritten pipeline: %v", err)
	}
	if !bytes.Equal(sd.Content, newSamples) {
		t.Error("decoding through the post-replace pipeline did not recover the substituted samples")
	}
}

// A /DecodeParms an earlier, predictor-using substitution left behind must
// not survive a later one that declares none -- a stale PNG predictor would
// corrupt unpredicted samples.
func TestReplaceImageDropsAStaleDecodeParms(t *testing.T) {
	d := openCorpus(t, "scan")
	id := firstImage(t, d, 1)
	info, _ := d.ImageInfo(id)
	sd := d.(*doc).streams[id]

	if err := d.ReplaceImage(id, EncodedImage{
		Width: info.Width, Height: info.Height, BPC: 8,
		ColorSpace:  ColorSpace{Name: "Indexed", Base: "DeviceGray", HiVal: 1, Lookup: []byte{0, 255}},
		Filter:      "FlateDecode",
		DecodeParms: &DecodeParms{Predictor: 15, Colors: 1, BitsPerComponent: 8, Columns: info.Width},
		Data:        deflateAt(t, make([]byte, info.Width*info.Height), 6),
	}); err != nil {
		t.Fatalf("first ReplaceImage: %v", err)
	}
	if _, ok := sd.Dict.Find("DecodeParms"); !ok {
		t.Fatal("the first substitution did not leave /DecodeParms behind; this test proves nothing")
	}

	if err := d.ReplaceImage(id, EncodedImage{
		Width: info.Width, Height: info.Height, BPC: 8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       deflateAt(t, make([]byte, info.Width*info.Height), 6),
	}); err != nil {
		t.Fatalf("second ReplaceImage: %v", err)
	}
	if _, ok := sd.Dict.Find("DecodeParms"); ok {
		t.Error("/DecodeParms survived a substitution that declared none")
	}
}

// The /DecodeParms dict() renders must map each field to its own key, and
// must omit a field left at its zero value rather than writing an explicit
// 0 -- an explicit /BitsPerComponent 0 is not the same as no entry at all.
func TestReplaceImageDecodeParmsDict(t *testing.T) {
	t.Run("keys are not transposed", func(t *testing.T) {
		d := openCorpus(t, "scan")
		id := firstImage(t, d, 1)
		info, _ := d.ImageInfo(id)
		sd := d.(*doc).streams[id]

		if err := d.ReplaceImage(id, EncodedImage{
			Width: info.Width, Height: info.Height, BPC: 8,
			ColorSpace:  ColorSpace{Name: "DeviceGray"},
			Filter:      "FlateDecode",
			DecodeParms: &DecodeParms{Predictor: 15, Colors: 1, BitsPerComponent: 8, Columns: info.Width},
			Data:        deflateAt(t, make([]byte, info.Width*info.Height), 6),
		}); err != nil {
			t.Fatalf("ReplaceImage: %v", err)
		}

		parms := d.(*doc).dictEntry(sd.Dict, "DecodeParms")
		if parms == nil {
			t.Fatal("/DecodeParms is not a dict")
		}
		if got := d.(*doc).intEntry(parms, "Colors"); got != 1 {
			t.Errorf("/DecodeParms/Colors = %d, want 1", got)
		}
		if got := d.(*doc).intEntry(parms, "Columns"); got != info.Width {
			t.Errorf("/DecodeParms/Columns = %d, want %d", got, info.Width)
		}
	})

	t.Run("unset fields are omitted, not written as zero", func(t *testing.T) {
		d := openCorpus(t, "scan")
		id := firstImage(t, d, 1)
		info, _ := d.ImageInfo(id)
		sd := d.(*doc).streams[id]

		if err := d.ReplaceImage(id, EncodedImage{
			Width: info.Width, Height: info.Height, BPC: 8,
			ColorSpace:  ColorSpace{Name: "DeviceGray"},
			Filter:      "FlateDecode",
			DecodeParms: &DecodeParms{Predictor: 15},
			Data:        deflateAt(t, make([]byte, info.Width*info.Height), 6),
		}); err != nil {
			t.Fatalf("ReplaceImage: %v", err)
		}

		parms := d.(*doc).dictEntry(sd.Dict, "DecodeParms")
		if parms == nil {
			t.Fatal("/DecodeParms is not a dict")
		}
		for _, k := range []string{"Colors", "BitsPerComponent", "Columns"} {
			if hasEntry(parms, k) {
				t.Errorf("/DecodeParms unexpectedly sets %q (want it omitted, unset)", k)
			}
		}
		if got := d.(*doc).intEntry(parms, "Predictor"); got != 15 {
			t.Errorf("/DecodeParms/Predictor = %d, want 15", got)
		}
	})
}

// The decoded-content cache must be invalidated by ReplaceImage, not merely
// the Raw bytes: a stale Content left in place is a live substitution the
// mutation stage found -- pdfcpu's own StreamDict.Encode re-derives Raw from
// Content whenever Content is non-nil, so a caller that decodes the image
// before replacing it and later calls Encode would silently get the old
// raster back.
func TestReplaceImageInvalidatesTheDecodedContentCache(t *testing.T) {
	d := openCorpus(t, "scan")
	id := firstImage(t, d, 1)
	sd := d.(*doc).streams[id]

	if err := sd.Decode(); err != nil {
		t.Fatalf("priming the decoded-content cache: %v", err)
	}
	if sd.Content == nil {
		t.Fatal("Decode left Content nil; this test proves nothing")
	}

	info, _ := d.ImageInfo(id)
	newSamples := make([]byte, info.Width*info.Height)
	for i := range newSamples {
		newSamples[i] = 0x42
	}
	if err := d.ReplaceImage(id, EncodedImage{
		Width: info.Width, Height: info.Height, BPC: 8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       deflateAt(t, newSamples, 6),
	}); err != nil {
		t.Fatalf("ReplaceImage: %v", err)
	}

	if sd.Content != nil {
		t.Error("the decoded-content cache survived ReplaceImage; a later Encode() would resurrect the old raster")
	}

	before := append([]byte(nil), sd.Raw...)
	if err := sd.Encode(); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !bytes.Equal(sd.Raw, before) {
		t.Error("Encode() after ReplaceImage changed Raw: the stale decoded-content cache clobbered the substitution")
	}
}

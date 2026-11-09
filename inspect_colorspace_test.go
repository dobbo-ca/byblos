package byblos

import (
	"bytes"
	"fmt"
	"testing"
)

// ImageRef.ColorSpace and BPC come from the image object the cross-reference table
// resolves, not from the file's text: a decoy name in a string or a comment of the
// dictionary, or a superseded earlier definition of the object, must not be read.
func TestInspectImageRefColourSpaceIsTheParsedObject(t *testing.T) {
	objs := func(img string) []string {
		return []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R /Resources << /XObject << /Im1 5 0 R >> >> >>",
			"<< /Length 28 >>\nstream\nq 100 0 0 100 0 0 cm /Im1 Do Q\nendstream",
			img,
		}
	}
	write := func(objs []string) ([]byte, int) {
		var b bytes.Buffer
		b.WriteString("%PDF-1.7\n")
		offs := make([]int, len(objs))
		for i, o := range objs {
			offs[i] = b.Len()
			fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
		}
		x := b.Len()
		fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
		for _, o := range offs {
			fmt.Fprintf(&b, "%010d 00000 n \n", o)
		}
		fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
		return b.Bytes(), x
	}
	img := func(extra, cs string) string {
		return "<< /Type /XObject /Subtype /Image /Width 1 /Height 1 " + extra + " /ColorSpace " + cs + " /BitsPerComponent 8 /Length 4 >>\nstream\nabcd\nendstream"
	}
	incremental := func() []byte {
		b, prev := write(objs(img("", "/DeviceRGB")))
		off := len(b)
		b = append(b, fmt.Sprintf("5 0 obj\n%s\nendobj\n", img("", "/DeviceCMYK"))...)
		x := len(b)
		return append(b, fmt.Sprintf("xref\n5 1\n%010d 00000 n \ntrailer\n<< /Size 6 /Root 1 0 R /Prev %d >>\nstartxref\n%d\n%%%%EOF\n", off, prev, x)...)
	}
	plain, _ := write(objs(img("", "/DeviceGray")))
	str, _ := write(objs(img("/Note (/ColorSpace /DeviceRGB)", "/DeviceCMYK")))
	comment, _ := write(objs(img("% /ColorSpace /DeviceRGB\n", "/DeviceCMYK")))
	array, _ := write(objs(img("", "[/ICCBased 1 0 R]")))
	for _, c := range []struct {
		name string
		pdf  []byte
		want string
	}{{"plain", plain, "DeviceGray"}, {"string", str, "DeviceCMYK"}, {"comment", comment, "DeviceCMYK"}, {"incremental", incremental(), "DeviceCMYK"}, {"array", array, ""}} {
		pages, err := Inspect(bytes.NewReader(c.pdf))
		if err != nil || len(pages) != 1 || len(pages[0].Images) != 1 {
			t.Fatalf("%s: Inspect = %+v, %v", c.name, pages, err)
		}
		if ref := pages[0].Images[0]; ref.ColorSpace != c.want || ref.BPC != 8 {
			t.Errorf("%s: ColorSpace=%q BPC=%d; want %q and 8", c.name, ref.ColorSpace, ref.BPC, c.want)
		}
	}
}

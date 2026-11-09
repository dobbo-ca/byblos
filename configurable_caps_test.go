package byblos

// Red-first tests for owner decision D2 (kleio-1eb.74.23.1): every cap that was
// a package constant is a Limits field, zero meaning today's default, and a
// breach is a permanent *LimitError with a stable name.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func limitNamed(err error) string {
	var le *LimitError
	if errors.As(err, &le) && errors.Is(err, ErrLimit) {
		return le.Name
	}
	return ""
}

func contentPDF(content string) []byte {
	return bombPDF(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content), "4 0 R")
}

func smallPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestEmbedPNGLimitsRefusesByName(t *testing.T) {
	data := smallPNG(t)
	if _, _, err := EmbedPNGLimits(data, Limits{}); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	_, _, err := EmbedPNGLimits(data, Limits{MaxEncodedImagePixels: 15})
	if limitNamed(err) != "encoded-image-pixels" {
		t.Errorf("err = %v; want *LimitError encoded-image-pixels", err)
	}
}

func TestBuildPDFLimitsRefusesByName(t *testing.T) {
	img, _, err := EmbedPNG(smallPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	pages := []BuildPage{{Image: img, WidthPt: 4, HeightPt: 4}}
	if err := BuildPDFLimits(context.Background(), &bytes.Buffer{}, pages, Limits{}); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	var out bytes.Buffer
	err = BuildPDFLimits(context.Background(), &out, pages, Limits{MaxEncodedImagePixels: 15})
	if limitNamed(err) != "encoded-image-pixels" || out.Len() != 0 {
		t.Errorf("err = %v, wrote %d bytes; want *LimitError encoded-image-pixels and nothing written", err, out.Len())
	}
}

func TestDownsampleLimitsRefusesByName(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 40))
	img.Set(1, 1, color.RGBA{A: 255})
	if _, err := DownsampleLimits(context.Background(), img, 300, 150, Limits{}); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	_, err := DownsampleLimits(context.Background(), img, 300, 150, Limits{MaxImagePixels: 1599})
	if limitNamed(err) != "image-pixels" {
		t.Errorf("err = %v; want *LimitError image-pixels", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DownsampleLimits(ctx, img, 300, 150, Limits{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx: err = %v; want context.Canceled", err)
	}
}

func TestRenderPageLimitsRefusesByName(t *testing.T) {
	for _, c := range []struct {
		name    string
		content string
		lim     Limits
	}{
		{"raster-side", "0 g", Limits{MaxRasterSide: 100}},
		{"raster-pixels", "0 g", Limits{MaxRasterPixels: 1000}},
		{"q-depth", "q q q q", Limits{MaxGStateDepth: 3}},
		{"path-points", "0 0 m 100 100 l 50 200 l f", Limits{MaxPathPoints: 2}},
		{"fill-work", "0 g 0 0 612 792 re f", Limits{MaxFillWork: 10}},
	} {
		pdf := contentPDF(c.content)
		if _, err := RenderPageLimits(context.Background(), bytes.NewReader(pdf), 1, 400, Limits{}); err != nil {
			t.Fatalf("%s: defaults: %v", c.name, err)
		}
		_, err := RenderPageLimits(context.Background(), bytes.NewReader(pdf), 1, 400, c.lim)
		if limitNamed(err) != c.name {
			t.Errorf("%s: err = %v; want *LimitError %s", c.name, err, c.name)
		}
	}
}

// The content walk's caps reach it through InspectDocument's Limits.
func TestInspectDocumentWalkCapsRefuseByName(t *testing.T) {
	for _, c := range []struct {
		name    string
		content string
		lim     Limits
	}{
		{"q-depth", "q q q q", Limits{MaxGStateDepth: 3}},
	} {
		pdf := contentPDF(c.content)
		if _, err := InspectDocument(context.Background(), bytes.NewReader(pdf), Limits{}); err != nil {
			t.Fatalf("%s: defaults: %v", c.name, err)
		}
		_, err := InspectDocument(context.Background(), bytes.NewReader(pdf), c.lim)
		if limitNamed(err) != c.name {
			t.Errorf("%s: err = %v; want *LimitError %s", c.name, err, c.name)
		}
	}
}

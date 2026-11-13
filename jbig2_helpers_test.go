package byblos

import (
	"context"

	jbig2 "github.com/dobbo-ca/jbig2-go"
)

// errUnsupportedJBIG2 is what a coding mode jbig2-go does not decode wraps.
var errUnsupportedJBIG2 = jbig2.ErrUnsupportedFeature

// encodeJBIG2Generic is the fixture encoder: a bitmap to an embedded JBIG2
// stream through jbig2-go.
func encodeJBIG2Generic(b *Bitmap) ([]byte, error) {
	return jbig2.Encode(context.Background(),
		&jbig2.Bitmap{W: b.Width, H: b.Height, Stride: b.Stride, Pix: b.Pix}, jbig2.Limits{})
}

// decodeJBIG2Generic decodes a stream that needs no globals, for fixtures.
func decodeJBIG2Generic(data []byte) (*Bitmap, error) {
	b, err := jbig2.Decode(context.Background(), data, nil, jbig2.Limits{})
	if err != nil {
		return nil, err
	}
	return &Bitmap{Width: b.W, Height: b.H, Stride: b.Stride, Pix: b.Pix}, nil
}

// encodeJBIG2Image packages encodeJBIG2Generic as the ready EncodedImage the
// builders take.
func encodeJBIG2Image(b *Bitmap) (EncodedImage, error) {
	data, err := encodeJBIG2Generic(b)
	if err != nil {
		return EncodedImage{}, err
	}
	return EncodedImage{
		Width:      b.Width,
		Height:     b.Height,
		BPC:        1,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "JBIG2Decode",
		Data:       data,
	}, nil
}

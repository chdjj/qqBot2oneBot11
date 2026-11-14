// Package media prepares images for QQ's direct upload limits.
package media

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	// GIF and WebP decoders are registered for side effects.
	_ "golang.org/x/image/webp"
	_ "image/gif"
)

// InlineLimit is the platform's cap for a single direct (file_data) upload.
// Anything larger must go through the chunked upload flow.
const InlineLimit = 5 << 20 // 5 MiB

// JPEG re-encoding knobs. QQ only needs the bytes to fit under the limit, but
// we start high to keep text in screenshots legible.
var qualitySteps = []int{92, 88, 84, 80, 75, 70, 65, 60, 55, 50}

// maxDimensionSteps progressively shrink very large images, which is the only
// way to get some screenshots and photos under 5 MiB at a usable quality.
var maxDimensionSteps = []int{0, 2560, 2048, 1600, 1280, 1000}

// Prepared is the result of normalising an image for upload.
type Prepared struct {
	// JPEG holds re-encoded bytes, always image/jpeg.
	JPEG []byte
	Name string
	// Width and Height describe the encoded image.
	Width, Height int
	// Resized reports whether the longest edge was reduced.
	Resized      bool
	OriginalSize int
}

// IsImage reports whether decoding this payload is worth attempting. It is a
// cheap magic-byte check so we never feed a video or archive to the decoder.
func IsImage(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	switch {
	case data[0] == 0xFF && data[1] == 0xD8: // JPEG
		return true
	case data[0] == 0x89 && string(data[1:4]) == "PNG":
		return true
	case string(data[0:4]) == "GIF8":
		return true
	case string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return true
	case string(data[0:2]) == "BM": // BMP
		return true
	}
	return false
}

// PrepareJPEG decodes any supported image and re-encodes it as JPEG, shrinking
// quality and then dimensions until it fits within limit.
//
// It returns an error only when the image cannot be decoded or cannot be made
// small enough — the caller may then fall back to the chunked upload.
func PrepareJPEG(data []byte, limit int, baseName string) (*Prepared, error) {
	if limit <= 0 {
		limit = InlineLimit
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("无法解码图片（%s）: %w", format, err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil, errors.New("图片尺寸为零")
	}

	// PNG with transparency looks wrong on black after JPEG conversion, so
	// flatten onto white first.
	canvas := flattenOnWhite(img)

	out := &Prepared{Name: baseName, OriginalSize: len(data)}

	for _, maxDim := range maxDimensionSteps {
		scaled, sw, sh := canvas, w, h
		if maxDim > 0 && (w > maxDim || h > maxDim) && maxDim < w {
			scaled, sw, sh = resize(canvas, maxDim)
			out.Resized = true
		}
		for _, q := range qualitySteps {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: q}); err != nil {
				return nil, fmt.Errorf("JPEG 编码失败: %w", err)
			}
			if buf.Len() <= limit {
				out.JPEG = buf.Bytes()
				out.Width, out.Height = sw, sh
				return out, nil
			}
		}
	}
	return nil, fmt.Errorf("图片压缩后仍有 %d 字节，超过 %d 限制", len(out.JPEG), limit)
}

// FlattenMode is preserved for callers that want the flattened image itself.
func flattenOnWhite(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	// Fill with white so transparent PNG regions do not turn black in JPEG.
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(x, y, color.White)
		}
	}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			sr, sg, sb, sa := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if sa == 0 {
				continue
			}
			// Simple alpha blend over white.
			a := float64(sa) / 65535.0
			r := (float64(sr)/65535.0)*a + 1.0*(1-a)
			g := (float64(sg)/65535.0)*a + 1.0*(1-a)
			bl := (float64(sb)/65535.0)*a + 1.0*(1-a)
			dst.Set(x, y, color.RGBA{
				R: uint8(r * 255), G: uint8(g * 255), B: uint8(bl * 255), A: 255,
			})
		}
	}
	return dst
}

// resize scales so the longest edge equals maxDim, using nearest-neighbour
// sampling. The images involved are large screenshots and photos where the
// quality difference is invisible after JPEG encoding.
func resize(src image.Image, maxDim int) (image.Image, int, int) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := float64(maxDim) / float64(max(w, h))
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + y*h/nh
		for x := 0; x < nw; x++ {
			sx := b.Min.X + x*w/nw
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst, nw, nh
}

// PNGToJPEG is a small helper kept for tests.
var _ = png.Decode

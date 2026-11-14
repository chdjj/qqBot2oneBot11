package media

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// noisyImage builds an image with enough detail that JPEG cannot compress it
// to nothing, so the size-reduction path is actually exercised.
func noisyImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Deterministic pseudo-random noise: PNG cannot find patterns in it, so the
	// encoder really has to work and the input is genuinely large.
	seed := uint32(12345)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			seed = seed*1664525 + 1013904223
			img.Set(x, y, color.RGBA{
				R: uint8(seed >> 24),
				G: uint8(seed >> 16),
				B: uint8(seed >> 8),
				A: 255,
			})
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPrepareJPEGAlwaysProducesJPEG(t *testing.T) {
	pngData := encodePNG(t, noisyImage(800, 600))
	out, err := PrepareJPEG(pngData, InlineLimit, "a.png")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out.JPEG, []byte{0xFF, 0xD8}) {
		t.Fatalf("output is not JPEG: % x", out.JPEG[:4])
	}
	if out.Width != 800 || out.Height != 600 {
		t.Fatalf("dimensions changed unexpectedly: %dx%d", out.Width, out.Height)
	}
	if out.OriginalSize != len(pngData) {
		t.Fatalf("original size recorded as %d, want %d", out.OriginalSize, len(pngData))
	}
}

func TestPrepareJPEGShrinksUnderLimit(t *testing.T) {
	big := encodePNG(t, noisyImage(3000, 2000))
	if len(big) < 64<<10 {
		t.Skipf("source PNG only %d bytes; cannot exercise compression", len(big))
	}
	// A deliberately tiny limit forces both quality and dimension reduction.
	const limit = 512 << 10
	out, err := PrepareJPEG(big, limit, "big.png")
	if err != nil {
		t.Fatalf("should reach %d bytes: %v", limit, err)
	}
	if len(out.JPEG) > limit {
		t.Fatalf("output %d bytes exceeds limit %d", len(out.JPEG), limit)
	}
	if out.Width >= 3000 {
		t.Fatalf("expected downscaling, got width %d", out.Width)
	}
	if !out.Resized {
		t.Fatal("Resized should be true after downscaling")
	}
}

func TestPrepareJPEGFitsInlineLimit(t *testing.T) {
	// A realistic screenshot-sized image must land under 5 MiB.
	big := encodePNG(t, noisyImage(2500, 1600))
	out, err := PrepareJPEG(big, InlineLimit, "shot.png")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.JPEG) > InlineLimit {
		t.Fatalf("output %d bytes exceeds inline limit", len(out.JPEG))
	}
	// It should usually compress well without resizing; report if not.
	t.Logf("source %d bytes -> jpeg %d bytes (%dx%d, resized=%v)",
		len(big), len(out.JPEG), out.Width, out.Height, out.Resized)
}

func TestPrepareJPEGTransparencyBecomesWhite(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64)) // all zero = transparent black
	out, err := PrepareJPEG(encodePNG(t, img), InlineLimit, "t.png")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(out.JPEG))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decoded.At(10, 10).RGBA()
	// Transparent pixels must not become black.
	if r < 0xE000 || g < 0xE000 || b < 0xE000 {
		t.Fatalf("transparent area became %04x %04x %04x, want near white", r, g, b)
	}
}

func TestPrepareJPEGRejectsGarbage(t *testing.T) {
	if _, err := PrepareJPEG([]byte("not an image at all"), InlineLimit, "x"); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestIsImage(t *testing.T) {
	pngData := encodePNG(t, noisyImage(8, 8))
	if !IsImage(pngData) {
		t.Fatal("PNG should be detected")
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, noisyImage(8, 8), nil)
	if !IsImage(buf.Bytes()) {
		t.Fatal("JPEG should be detected")
	}
	if IsImage([]byte("hello world this is text")) {
		t.Fatal("text must not be treated as an image")
	}
	if IsImage([]byte("%PDF-1.4")) {
		t.Fatal("PDF must not be treated as an image")
	}
}

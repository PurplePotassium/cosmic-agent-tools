package chroma

import (
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func testImage() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 16), G: uint8(y * 16), B: 128, A: 255})
		}
	}
	return img
}

// encodeAs writes the test image at path in the given byte format, regardless
// of the path's extension.
func encodeAs(t *testing.T, path, format string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	switch format {
	case "png":
		err = png.Encode(f, testImage())
	case "jpeg":
		err = jpeg.Encode(f, testImage(), nil)
	case "gif":
		err = gif.Encode(f, testImage(), nil)
	default:
		t.Fatalf("unknown format %q", format)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// decodedFormat reports what the bytes at path actually decode as.
func decodedFormat(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, format, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return format
}

func TestEnsurePNGKeepsGenuinePNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "real.png")
	encodeAs(t, path, "png")
	before, _ := os.ReadFile(path)

	format, converted, err := EnsurePNG(path)
	if err != nil || converted || format != "png" {
		t.Fatalf("EnsurePNG = %q, %v, %v; want png, false, nil", format, converted, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("genuine PNG was rewritten")
	}
}

func TestEnsurePNGConvertsMislabeledBytes(t *testing.T) {
	for _, actual := range []string{"jpeg", "gif"} {
		path := filepath.Join(t.TempDir(), "fake.png")
		encodeAs(t, path, actual)

		format, converted, err := EnsurePNG(path)
		if err != nil || !converted || format != actual {
			t.Fatalf("EnsurePNG(%s bytes) = %q, %v, %v; want %s, true, nil", actual, format, converted, err, actual)
		}
		if got := decodedFormat(t, path); got != "png" {
			t.Fatalf("after conversion the file decodes as %q; want png", got)
		}
	}
}

func TestEnsurePNGRejectsNonImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.png")
	if err := os.WriteFile(path, []byte("%PDF-1.7 definitely not pixels"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsurePNG(path); err == nil {
		t.Fatal("EnsurePNG accepted non-image bytes")
	}
}

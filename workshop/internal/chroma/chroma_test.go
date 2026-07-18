package chroma

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeImg writes a 64x64 PNG: bg fill with a centered 24x24 fg square.
func writeImg(t *testing.T, path string, bg, fg color.NRGBA) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if x >= 20 && x < 44 && y >= 20 && y < 44 {
				img.SetNRGBA(x, y, fg)
			} else {
				img.SetNRGBA(x, y, bg)
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func at(t *testing.T, path string, x, y int) color.NRGBA {
	t.Helper()
	img, err := decode(path)
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := img.At(x, y).RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

func TestFractionKeyish(t *testing.T) {
	dir := t.TempDir()
	greenish := filepath.Join(dir, "green.png")
	writeImg(t, greenish, color.NRGBA{R: 20, G: 220, B: 30, A: 255}, color.NRGBA{R: 20, G: 220, B: 30, A: 255})
	frac, err := FractionKeyish(greenish, KeyGreen)
	if err != nil || frac < 0.99 {
		t.Fatalf("all-green frac = %v, err = %v; want ~1", frac, err)
	}
	plain := filepath.Join(dir, "plain.png")
	writeImg(t, plain, color.NRGBA{R: 90, G: 90, B: 90, A: 255}, color.NRGBA{R: 200, A: 255})
	frac, err = FractionKeyish(plain, KeyGreen)
	if err != nil || frac != 0 {
		t.Fatalf("gray/red frac = %v, err = %v; want 0", frac, err)
	}
}

func TestVerifyTransparencyRejectsOpaque(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.png")
	writeImg(t, in, color.NRGBA{R: 90, G: 90, B: 90, A: 255}, color.NRGBA{R: 200, A: 255})
	if err := VerifyTransparency(in); err == nil {
		t.Fatal("an all-opaque image must fail transparency verification")
	}
}

func TestValidRemover(t *testing.T) {
	for _, ok := range append([]string{""}, Removers...) {
		if !ValidRemover(ok) {
			t.Fatalf("ValidRemover(%q) = false; want true", ok)
		}
	}
	if ValidRemover("photoshop") {
		t.Fatal(`ValidRemover("photoshop") = true; want false`)
	}
}

func TestRemoveUnknownBackend(t *testing.T) {
	if err := Remove(t.Context(), "photoshop", "", "in.png", "out.png", KeyGreen); err == nil {
		t.Fatal("unknown remover must error")
	}
}

// Exercises the real ffmpeg backend when ffmpeg is installed (skipped
// otherwise, so the gate stays green on machines without it).
func TestRemoveFFmpegReal(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.png")
	out := filepath.Join(dir, "out.png")
	writeImg(t, in, color.NRGBA{R: 12, G: 245, B: 20, A: 255}, color.NRGBA{R: 200, G: 30, B: 30, A: 255})
	if err := RemoveFFmpeg(t.Context(), in, out, KeyGreen); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTransparency(out); err != nil {
		t.Fatal(err)
	}
	if p := at(t, out, 32, 32); p.A != 255 || p.R < 150 {
		t.Fatalf("subject pixel = %+v; want opaque red", p)
	}
	if p := at(t, out, 2, 2); p.A > 20 {
		t.Fatalf("background pixel alpha = %d; want ~0", p.A)
	}
}

func TestRemoveFFmpegRejectsUnscreenedImage(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.png")
	writeImg(t, in, color.NRGBA{R: 180, G: 60, B: 60, A: 255}, color.NRGBA{R: 20, G: 20, B: 20, A: 255})
	if err := RemoveFFmpeg(t.Context(), in, filepath.Join(dir, "out.png"), KeyGreen); err == nil {
		t.Fatal("keying a red-background image as green should fail")
	}
}

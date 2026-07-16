// Package chroma removes a flat green/blue screen background from a single
// image, producing a straight-alpha transparent PNG. It is the last stage of
// the art-gen-trans flow: the image model repaints the background as a flat
// key color, then one of these backends keys it out.
//
// Three interchangeable backends (operator-selectable live from the
// dashboard, kv key "art.remover"):
//
//   - builtin: pure Go color-distance keyer with edge despill. No external
//     dependency, milliseconds per image, ideal for the flat machine-painted
//     screens this flow produces.
//   - corridorkey: the CorridorKey neural keyer (github.com/gw1108/CorridorKey
//     fork, installed at [art].corridorkey_dir). ML-quality edges (hair,
//     soft shadows) at the cost of model-load time per call — CPU-only
//     installs take tens of seconds.
//   - ffmpeg: ffmpeg's colorkey+despill filter chain. Robust, widely
//     installed, a good middle ground when it is already on PATH.
package chroma

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	_ "image/gif"  // decoder registration: the image model may emit any of these
	_ "image/jpeg" // despite being asked for PNG
)

// Key is the screen color being removed.
type Key int

const (
	KeyGreen Key = iota
	KeyBlue
)

func (k Key) String() string {
	if k == KeyBlue {
		return "blue"
	}
	return "green"
}

// Removers is the ordered set of selectable backends (first = default).
var Removers = []string{"builtin", "corridorkey", "ffmpeg"}

// ValidRemover reports whether name is a selectable backend ("" = default).
func ValidRemover(name string) bool {
	if name == "" {
		return true
	}
	for _, r := range Removers {
		if r == name {
			return true
		}
	}
	return false
}

// Remove keys the screen color out of inPath and writes a transparent PNG to
// outPath using the named backend ("" = builtin). corridorKeyDir is the
// CorridorKey checkout (used only by that backend).
func Remove(ctx context.Context, remover, corridorKeyDir, inPath, outPath string, key Key) error {
	switch remover {
	case "", "builtin":
		return RemoveBuiltin(inPath, outPath, key)
	case "corridorkey":
		return RemoveCorridorKey(ctx, corridorKeyDir, inPath, outPath, key)
	case "ffmpeg":
		return RemoveFFmpeg(ctx, inPath, outPath, key)
	default:
		return fmt.Errorf("chroma: unknown remover %q (available: %s)", remover, strings.Join(Removers, ", "))
	}
}

// --- builtin ---

// Thresholds for the alpha ramp, in 8-bit RGB Euclidean distance from the
// estimated screen color: at most t0 away = fully transparent, at least t1
// away = fully opaque, linear in between. The screens are machine-painted and
// nearly flat, so the band mostly catches anti-aliased subject edges.
const (
	builtinT0 = 60.0
	builtinT1 = 130.0
)

// RemoveBuiltin is the dependency-free keyer: estimate the actual screen
// color from the border, ramp alpha by color distance, despill edges.
func RemoveBuiltin(inPath, outPath string, key Key) error {
	img, err := decode(inPath)
	if err != nil {
		return err
	}
	screen, frac, err := estimateScreen(img, key)
	if err != nil {
		return err
	}
	if frac < 0.30 {
		return fmt.Errorf("chroma: border is only %.0f%% %s — image does not look %s-screened", frac*100, key, key)
	}

	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl := rgb8(img.At(x, y))
			d := dist(r, g, bl, screen[0], screen[1], screen[2])
			var a uint8
			switch {
			case d <= builtinT0:
				a = 0
			case d >= builtinT1:
				a = 255
			default:
				a = uint8(255 * (d - builtinT0) / (builtinT1 - builtinT0))
			}
			if a > 0 && a < 255 {
				r, g, bl = despill(r, g, bl, key)
			}
			out.SetNRGBA(x-b.Min.X, y-b.Min.Y, color.NRGBA{R: r, G: g, B: bl, A: a})
		}
	}
	return writePNG(outPath, out)
}

// FractionKeyish reports the fraction of pixels whose color is dominated by
// the key channel — the engine's "is this sprite genuinely green?" evaluation
// that decides green vs blue screen for the rescreen step.
func FractionKeyish(path string, key Key) (float64, error) {
	img, err := decode(path)
	if err != nil {
		return 0, err
	}
	b := img.Bounds()
	total, hits := 0, 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl := rgb8(img.At(x, y))
			total++
			if isKeyish(r, g, bl, key) {
				hits++
			}
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("chroma: %s: empty image", path)
	}
	return float64(hits) / float64(total), nil
}

// VerifyTransparency checks that path is a decodable image whose alpha
// channel actually varies: some pixels transparent (the keyed background) and
// some opaque (the subject survived). Guards against a keyer silently
// emitting an all-opaque or all-blank result.
func VerifyTransparency(path string) error {
	img, err := decode(path)
	if err != nil {
		return err
	}
	b := img.Bounds()
	clear, opaque := 0, 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			switch {
			case a < 0x1000:
				clear++
			case a > 0xf000:
				opaque++
			}
		}
	}
	total := b.Dx() * b.Dy()
	if total == 0 {
		return fmt.Errorf("chroma: %s: empty image", path)
	}
	if clear == 0 {
		return fmt.Errorf("chroma: %s has no transparent pixels — background was not keyed out", path)
	}
	if opaque < total/100 {
		return fmt.Errorf("chroma: %s is almost entirely transparent — the subject was keyed away too", path)
	}
	return nil
}

// estimateScreen averages the border-ring pixels dominated by the key channel
// and returns that color plus the fraction of border pixels that qualified.
func estimateScreen(img image.Image, key Key) ([3]uint8, float64, error) {
	b := img.Bounds()
	if b.Dx() < 4 || b.Dy() < 4 {
		return [3]uint8{}, 0, fmt.Errorf("chroma: image too small (%dx%d)", b.Dx(), b.Dy())
	}
	ring := b.Dy() / 50
	if r2 := b.Dx() / 50; r2 < ring {
		ring = r2
	}
	if ring < 2 {
		ring = 2
	}
	var sumR, sumG, sumB, hits, total int
	sample := func(x, y int) {
		r, g, bl := rgb8(img.At(x, y))
		total++
		if isKeyish(r, g, bl, key) {
			sumR += int(r)
			sumG += int(g)
			sumB += int(bl)
			hits++
		}
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		inYRing := y < b.Min.Y+ring || y >= b.Max.Y-ring
		for x := b.Min.X; x < b.Max.X; x++ {
			if inYRing || x < b.Min.X+ring || x >= b.Max.X-ring {
				sample(x, y)
			}
		}
	}
	if hits == 0 {
		return [3]uint8{}, 0, fmt.Errorf("chroma: no %s-dominant pixels on the image border", key)
	}
	return [3]uint8{uint8(sumR / hits), uint8(sumG / hits), uint8(sumB / hits)}, float64(hits) / float64(total), nil
}

// isKeyish: the key channel clearly dominates both others.
func isKeyish(r, g, b uint8, key Key) bool {
	const margin = 40
	if key == KeyBlue {
		return int(b) > int(r)+margin && int(b) > int(g)+margin
	}
	return int(g) > int(r)+margin && int(g) > int(b)+margin
}

// despill clamps the key channel to the max of the other two — the standard
// spill fix for the halo the ramp leaves on anti-aliased edges.
func despill(r, g, b uint8, key Key) (uint8, uint8, uint8) {
	if key == KeyBlue {
		if m := max8(r, g); b > m {
			b = m
		}
	} else {
		if m := max8(r, b); g > m {
			g = m
		}
	}
	return r, g, b
}

func max8(a, b uint8) uint8 {
	if a > b {
		return a
	}
	return b
}

func rgb8(c color.Color) (uint8, uint8, uint8) {
	r, g, b, _ := c.RGBA()
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
}

func dist(r1, g1, b1, r2, g2, b2 uint8) float64 {
	dr := float64(r1) - float64(r2)
	dg := float64(g1) - float64(g2)
	db := float64(b1) - float64(b2)
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

func decode(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("chroma: decode %s: %w", path, err)
	}
	return img, nil
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// --- corridorkey ---

// DiscoverCorridorKey returns the first location that actually contains the
// corridorkey CLI: the configured dir, the WORKSHOP_CORRIDORKEY env override,
// then the conventional checkout location. Falls back to the configured value
// (possibly empty) so RemoveCorridorKey can error with the real search root.
func DiscoverCorridorKey(configured string) string {
	candidates := []string{configured, os.Getenv("WORKSHOP_CORRIDORKEY")}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, `C:\GameDev\CorridorKey`)
	}
	for _, c := range candidates {
		if c != "" && CorridorKeyExe(c) != "" {
			return c
		}
	}
	return configured
}

// CorridorKeyExe resolves the corridorkey CLI inside a checkout dir, or "" if
// absent. The venv-installed entry point is cwd-independent and finds its own
// model weights (see the CorridorKey fork's README).
func CorridorKeyExe(dir string) string {
	if dir == "" {
		return ""
	}
	exe := filepath.Join(dir, ".venv", "Scripts", "corridorkey.exe")
	if runtime.GOOS != "windows" {
		exe = filepath.Join(dir, ".venv", "bin", "corridorkey")
	}
	if info, err := os.Stat(exe); err == nil && !info.IsDir() {
		return exe
	}
	return ""
}

// RemoveCorridorKey shells out to the CorridorKey neural keyer.
func RemoveCorridorKey(ctx context.Context, dir, inPath, outPath string, key Key) error {
	exe := CorridorKeyExe(dir)
	if exe == "" {
		return fmt.Errorf("chroma: corridorkey CLI not found under %q — install it (run install.ps1 in the CorridorKey checkout) or set [art].corridorkey_dir", dir)
	}
	absIn, err := filepath.Abs(inPath)
	if err != nil {
		return err
	}
	absOut, err := filepath.Abs(outPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absOut), 0o755); err != nil {
		return err
	}
	// --device cuda is deliberate and NOT auto: on a CPU-only torch install
	// "auto" silently falls back to CPU inference, where one image measured
	// 2+ hours (2026-07). Better to fail fast with corridorkey's own error
	// and have the operator provision CUDA (uv sync --extra cuda) or switch
	// remover than to wedge an art pass all afternoon.
	cmd := exec.CommandContext(ctx, exe, "--device", "cuda", "clean", absIn,
		"--output", absOut, "--screen-color", key.String(), "--json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("chroma: corridorkey clean failed: %v: %s", err, tail(string(out), 800))
	}
	if _, err := os.Stat(absOut); err != nil {
		return fmt.Errorf("chroma: corridorkey reported success but wrote no %s", absOut)
	}
	return nil
}

// --- ffmpeg ---

// RemoveFFmpeg keys via ffmpeg's colorkey+despill chain, keyed on the actual
// screen color estimated from the border (flat machine-painted screens are
// never exactly #00FF00).
func RemoveFFmpeg(ctx context.Context, inPath, outPath string, key Key) error {
	exe, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("chroma: ffmpeg not on PATH — install it (winget install ffmpeg) or pick another remover")
	}
	img, err := decode(inPath)
	if err != nil {
		return err
	}
	screen, frac, err := estimateScreen(img, key)
	if err != nil {
		return err
	}
	if frac < 0.30 {
		return fmt.Errorf("chroma: border is only %.0f%% %s — image does not look %s-screened", frac*100, key, key)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	filter := fmt.Sprintf("colorkey=0x%02X%02X%02X:0.28:0.08,despill=type=%s",
		screen[0], screen[1], screen[2], key)
	cmd := exec.CommandContext(ctx, exe, "-y", "-i", inPath,
		"-vf", filter, "-frames:v", "1", "-update", "1", outPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("chroma: ffmpeg colorkey failed: %v: %s", err, tail(string(out), 800))
	}
	if _, err := os.Stat(outPath); err != nil {
		return fmt.Errorf("chroma: ffmpeg reported success but wrote no %s", outPath)
	}
	return nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

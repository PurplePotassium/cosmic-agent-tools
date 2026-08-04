package chroma

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"os"

	_ "golang.org/x/image/bmp"  // decoder registration: the image model may
	_ "golang.org/x/image/tiff" // emit any of these despite being asked for
	_ "golang.org/x/image/webp" // PNG — EnsurePNG re-encodes them
)

// EnsurePNG verifies that the file at path genuinely contains PNG bytes —
// never trusting the extension — and, when the bytes are actually another
// decodable image format (JPEG, WebP, GIF, BMP, TIFF), re-encodes the pixels
// as a PNG in place. Returns the detected format and whether a conversion
// happened. An undecodable file is an error; the message names the sniffed
// container when the magic bytes are recognizable.
func EnsurePNG(path string) (format string, converted bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	img, format, err := image.Decode(f)
	f.Close()
	if err != nil {
		return "", false, fmt.Errorf("chroma: %s is not a decodable image (%s): %w", path, sniffLabel(path), err)
	}
	if format == "png" {
		return format, false, nil
	}
	if err := writePNG(path, img); err != nil {
		return format, false, fmt.Errorf("chroma: re-encode %s (%s) as PNG: %w", path, format, err)
	}
	return format, true, nil
}

// sniffLabel names the container the leading magic bytes suggest, for formats
// image.Decode has no registered decoder for. Best-effort — errors collapse
// to "unreadable".
func sniffLabel(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "unreadable"
	}
	defer f.Close()
	head := make([]byte, 32)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case n == 0:
		return "empty file"
	case n >= 12 && bytes.Equal(head[4:8], []byte("ftyp")):
		// AVIF/HEIC and friends share the ISO-BMFF "ftyp" box.
		return "isobmff container, brand " + string(bytes.TrimSpace(head[8:12]))
	case bytes.HasPrefix(head, []byte("%PDF")):
		return "pdf"
	case bytes.HasPrefix(head, []byte{0x00, 0x00, 0x01, 0x00}):
		return "ico"
	case bytes.HasPrefix(bytes.TrimLeft(head, " \t\r\n"), []byte("<")):
		return "markup text (svg/html?)"
	default:
		return fmt.Sprintf("unrecognized leading bytes % X", head[:min(n, 8)])
	}
}

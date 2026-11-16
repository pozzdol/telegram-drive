// Package thumb makes small JPEG previews of images for the grid and photo gallery.
package thumb

import (
	"bufio"
	"bytes"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	Size      = 480        // longest side in px; sharp on 2x screens for ~240 px tiles
	maxPixels = 60_000_000 // refuse to decode larger images (~240 MB of RAM)
)

// Supported reports whether Make can decode this MIME type.
func Supported(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	return false
}

// Make decodes an image from r, applies the EXIF orientation (phone photos are often stored
// sideways), scales it to fit Size and writes a JPEG to dst atomically.
func Make(r io.Reader, dst string) error {
	br := bufio.NewReaderSize(r, 256<<10)
	head, _ := br.Peek(256 << 10) // EXIF and image header live at the start
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(head)); err == nil && cfg.Width*cfg.Height > maxPixels {
		return errors.New("image too large for a thumbnail")
	}
	orientation, _ := parseExif(head)
	src, _, err := image.Decode(br)
	if err != nil {
		return err
	}

	b := src.Bounds()
	scale := min(1, float64(Size)/float64(max(b.Dx(), b.Dy())))
	tw, th := max(1, int(float64(b.Dx())*scale)), max(1, int(float64(b.Dy())*scale))
	out := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.Draw(out, out.Bounds(), image.White, image.Point{}, draw.Src) // transparent PNG -> white, not black
	draw.BiLinear.Scale(out, out.Bounds(), src, b, draw.Over, nil)

	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(f, orient(out, orientation), &jpeg.Options{Quality: 80}); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// orient applies EXIF orientation 2-8 (mirror/rotate) so the thumbnail is upright.
func orient(src *image.RGBA, o int) image.Image {
	if o < 2 || o > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var nx, ny int
			switch o {
			case 2:
				nx, ny = w-1-x, y
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				nx, ny = x, h-1-y
			case 5:
				nx, ny = y, x
			case 6: // rotate 90° clockwise
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8: // rotate 90° counter-clockwise
				nx, ny = y, w-1-x
			}
			dst.SetRGBA(nx, ny, src.RGBAAt(x, y))
		}
	}
	return dst
}

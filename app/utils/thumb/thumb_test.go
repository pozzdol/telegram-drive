package thumb

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A 40x20 JPEG tagged "rotate 90° clockwise" must come out as a 20x40 thumbnail,
// with its red left edge turned into the top edge.
func TestMakeAppliesExifOrientation(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := range 20 {
		for x := range 40 {
			c := color.RGBA{0, 0, 255, 255}
			if x < 10 {
				c = color.RGBA{255, 0, 0, 255}
			}
			src.Set(x, y, c)
		}
	}
	var enc bytes.Buffer
	jpeg.Encode(&enc, src, &jpeg.Options{Quality: 95})

	// APP1 Exif, big-endian TIFF, IFD0 with one entry: Orientation (0x0112) = 6.
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0, 0, 0, 0, 0, 0, 0}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	app1 := append([]byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
	raw := enc.Bytes()
	withExif := append(append(append([]byte{}, raw[:2]...), app1...), raw[2:]...)

	if o, _ := parseExif(withExif); o != 6 {
		t.Fatalf("orientation: want 6, got %d", o)
	}
	dst := filepath.Join(t.TempDir(), "t.jpg")
	if err := Make(bytes.NewReader(withExif), dst); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(dst)
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 20 || b.Dy() != 40 {
		t.Fatalf("size: want 20x40, got %dx%d", b.Dx(), b.Dy())
	}
	r, _, bl, _ := img.At(10, 2).RGBA()
	if r < bl {
		t.Fatalf("top edge should be red after rotation, got r=%d b=%d", r>>8, bl>>8)
	}
}

// DateTimeOriginal in the Exif sub-IFD, with its +07:00 offset, becomes the capture time.
func TestTakenAt(t *testing.T) {
	be := binary.BigEndian
	tf := make([]byte, 96)
	copy(tf, "MM\x00\x2a\x00\x00\x00\x08")
	entry := func(at int, tag, typ uint16, count, val uint32) {
		be.PutUint16(tf[at:], tag)
		be.PutUint16(tf[at+2:], typ)
		be.PutUint32(tf[at+4:], count)
		be.PutUint32(tf[at+8:], val)
	}
	be.PutUint16(tf[8:], 2) // IFD0: orientation + Exif pointer
	entry(10, 0x0112, 3, 1, 6<<16)
	entry(22, 0x8769, 4, 1, 38)
	be.PutUint16(tf[38:], 2) // Exif IFD: DateTimeOriginal + OffsetTimeOriginal
	entry(40, 0x9003, 2, 20, 68)
	entry(52, 0x9011, 2, 7, 88)
	copy(tf[68:], "2024:08:17 09:30:00\x00")
	copy(tf[88:], "+07:00\x00")

	payload := append([]byte("Exif\x00\x00"), tf...)
	jpg := append([]byte{0xFF, 0xD8, 0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
	jpg = append(jpg, 0xFF, 0xDA, 0, 2)

	o, taken := parseExif(jpg)
	if o != 6 {
		t.Fatalf("orientation: want 6, got %d", o)
	}
	want := time.Date(2024, 8, 17, 2, 30, 0, 0, time.UTC)
	if !taken.Equal(want) {
		t.Fatalf("taken: want %v, got %v", want, taken)
	}
	if got := TakenAt(bytes.NewReader(jpg)); !got.Equal(want) {
		t.Fatalf("TakenAt: want %v, got %v", want, got)
	}
	if !TakenAt(bytes.NewReader([]byte{0xFF, 0xD8, 0xFF, 0xDA})).IsZero() {
		t.Fatal("JPEG without EXIF must give zero time")
	}
}

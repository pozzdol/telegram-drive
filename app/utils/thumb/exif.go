package thumb

import (
	"encoding/binary"
	"io"
	"strings"
	"time"
)

// TakenAt reads when a JPEG photo was taken from its EXIF (DateTimeOriginal, else DateTime).
// Zero time if the file has no usable date. Only the first 256 KiB are read.
func TakenAt(r io.Reader) time.Time {
	b, _ := io.ReadAll(io.LimitReader(r, 256<<10))
	_, t := parseExif(b)
	return t
}

// parseExif returns the orientation (1 = upright) and capture time of a JPEG.
func parseExif(b []byte) (orientation int, taken time.Time) {
	orientation = 1
	t := findTIFF(b)
	if len(t) < 8 {
		return
	}
	r := tiff{t: t}
	switch string(t[:2]) {
	case "II":
		r.bo = binary.LittleEndian
	case "MM":
		r.bo = binary.BigEndian
	default:
		return
	}
	ifd0 := int(r.bo.Uint32(t[4:]))
	if o, ok := r.u16(ifd0, 0x0112); ok {
		orientation = o
	}
	var when, offset string
	if p, ok := r.u32(ifd0, 0x8769); ok { // pointer to the Exif sub-IFD
		when, offset = r.str(p, 0x9003), r.str(p, 0x9011) // DateTimeOriginal, OffsetTimeOriginal
	}
	if when == "" {
		when = r.str(ifd0, 0x0132) // DateTime (last modified) as a fallback
	}
	return orientation, exifTime(when, offset)
}

// exifTime parses "2006:01:02 15:04:05". Without an offset tag the camera's wall clock
// is taken as the server's time zone.
func exifTime(s, offset string) time.Time {
	if len(s) < 19 {
		return time.Time{}
	}
	loc := time.Local
	if z, err := time.Parse("-07:00", offset); err == nil {
		loc = z.Location()
	}
	t, err := time.ParseInLocation("2006:01:02 15:04:05", s[:19], loc)
	if err != nil || t.Year() < 1990 { // "0000:00:00 00:00:00" and dead camera clocks
		return time.Time{}
	}
	return t
}

// findTIFF returns the TIFF block inside a JPEG's APP1 "Exif" segment, or nil.
func findTIFF(b []byte) []byte {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return nil
		}
		marker, size := b[i+1], int(binary.BigEndian.Uint16(b[i+2:]))
		if marker == 0xDA { // start of image data: no EXIF before it
			return nil
		}
		end := i + 2 + size
		if marker == 0xE1 && size >= 8 && end <= len(b) && string(b[i+4:i+10]) == "Exif\x00\x00" {
			return b[i+10 : end]
		}
		i = end
	}
	return nil
}

type tiff struct {
	t  []byte
	bo binary.ByteOrder
}

// entry finds tag in the IFD at offset ifd and returns where its value lives.
func (r tiff) entry(ifd int, tag uint16) (count, at int, ok bool) {
	if ifd < 8 || ifd+2 > len(r.t) {
		return 0, 0, false
	}
	for k := range int(r.bo.Uint16(r.t[ifd:])) {
		e := ifd + 2 + k*12
		if e+12 > len(r.t) {
			return 0, 0, false
		}
		if r.bo.Uint16(r.t[e:]) != tag {
			continue
		}
		size := map[uint16]int{3: 2, 4: 4, 5: 8}[r.bo.Uint16(r.t[e+2:])]
		count = int(r.bo.Uint32(r.t[e+4:]))
		at = e + 8
		if max(size, 1)*count > 4 { // too big to inline: the field holds an offset
			at = int(r.bo.Uint32(r.t[e+8:]))
		}
		return count, at, true
	}
	return 0, 0, false
}

func (r tiff) u16(ifd int, tag uint16) (int, bool) {
	_, at, ok := r.entry(ifd, tag)
	if !ok || at+2 > len(r.t) {
		return 0, false
	}
	return int(r.bo.Uint16(r.t[at:])), true
}

func (r tiff) u32(ifd int, tag uint16) (int, bool) {
	_, at, ok := r.entry(ifd, tag)
	if !ok || at+4 > len(r.t) {
		return 0, false
	}
	return int(r.bo.Uint32(r.t[at:])), true
}

func (r tiff) str(ifd int, tag uint16) string {
	n, at, ok := r.entry(ifd, tag)
	if !ok || n <= 0 || at+n > len(r.t) {
		return ""
	}
	return strings.TrimRight(string(r.t[at:at+n]), "\x00 ")
}

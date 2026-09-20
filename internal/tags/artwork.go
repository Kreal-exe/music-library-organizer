package tags

// Bringing embedded artwork down to a size a player can actually use.
//
// A cover saved at full resolution turns a tag of a few hundred kilobytes into
// one of several megabytes, and a phone's media scanner stops reading a tag
// long before that — so the file's real artist and title never reach the
// player at all. The picture itself is worth keeping; the resolution is not,
// since it is drawn at a few hundred pixels on any screen it will ever meet.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"

	// Registered so a cover in any of these formats can be read.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/bogem/id3v2/v2"
)

const (
	// ScannerTagLimit is the size beyond which a media scanner gives up on a
	// file's tags. Android's limit is three megabytes; one uncompressed cover
	// passes it on its own.
	ScannerTagLimit = 3 << 20

	// What a cover is reduced to: large enough to fill any phone screen and
	// most desktop ones, small enough that a whole library of them costs
	// nothing.
	artworkEdge = 1000

	// Artwork already below this is left alone whatever its dimensions, since
	// re-encoding it would only lose quality for nothing.
	artworkKeepBelow = 400 << 10
)

// errArtworkFine means the picture needs no work.
var errArtworkFine = errors.New("artwork is already small enough")

// shrinkArtwork re-encodes one embedded picture as a JPEG no wider or taller
// than artworkEdge. It returns the new bytes and their media type.
func shrinkArtwork(data []byte) ([]byte, string, error) {
	if len(data) <= artworkKeepBelow {
		return nil, "", errArtworkFine
	}

	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}

	scaled := fitWithin(source, artworkEdge)

	var out bytes.Buffer
	if err := jpeg.Encode(&out, scaled, &jpeg.Options{Quality: 85}); err != nil {
		return nil, "", err
	}

	// Re-encoding a picture that was already efficient can make it bigger.
	if out.Len() >= len(data) {
		return nil, "", errArtworkFine
	}
	return out.Bytes(), "image/jpeg", nil
}

// fitWithin scales an image down so that neither side is longer than edge,
// leaving it alone if it already fits.
//
// Each destination pixel is the average of the source pixels it covers, which
// is what keeps a cover's text readable at a quarter of its size; a plain
// nearest-neighbour pick would leave it ragged.
func fitWithin(source image.Image, edge int) image.Image {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= edge && height <= edge {
		return source
	}

	scale := float64(edge) / float64(max(width, height))
	outWidth := max(1, int(float64(width)*scale))
	outHeight := max(1, int(float64(height)*scale))

	out := image.NewRGBA(image.Rect(0, 0, outWidth, outHeight))

	for y := range outHeight {
		// The band of source rows this row of the output covers.
		y0 := bounds.Min.Y + y*height/outHeight
		y1 := max(y0+1, bounds.Min.Y+(y+1)*height/outHeight)

		for x := range outWidth {
			x0 := bounds.Min.X + x*width/outWidth
			x1 := max(x0+1, bounds.Min.X+(x+1)*width/outWidth)

			var r, g, b, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					pr, pg, pb, pa := source.At(sx, sy).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					b += uint64(pb)
					a += uint64(pa)
					n++
				}
			}
			if n == 0 {
				continue
			}

			offset := out.PixOffset(x, y)
			out.Pix[offset+0] = uint8(r / n >> 8)
			out.Pix[offset+1] = uint8(g / n >> 8)
			out.Pix[offset+2] = uint8(b / n >> 8)
			out.Pix[offset+3] = uint8(a / n >> 8)
		}
	}
	return out
}

/* Per format --------------------------------------------------------------- */

// shrinkID3Artwork replaces every picture in an ID3 tag with a smaller copy,
// and reports whether anything changed.
func shrinkID3Artwork(tag *id3v2.Tag) bool {
	id := tag.CommonID("Attached picture")

	frames := tag.GetFrames(id)
	if len(frames) == 0 {
		return false
	}

	pictures := make([]id3v2.PictureFrame, 0, len(frames))
	changed := false

	for _, frame := range frames {
		picture, ok := frame.(id3v2.PictureFrame)
		if !ok {
			// A frame this library did not recognise would be lost by the
			// rewrite below, so the tag is left exactly as it is.
			return false
		}

		if smaller, mime, err := shrinkArtwork(picture.Picture); err == nil {
			picture.Picture, picture.MimeType = smaller, mime
			changed = true
		}
		pictures = append(pictures, picture)
	}

	if !changed {
		return false
	}

	tag.DeleteFrames(id)
	for _, picture := range pictures {
		tag.AddAttachedPicture(picture)
	}
	return true
}

// shrinkMP4Artwork does the same for the cover atoms of an MP4 item list.
func shrinkMP4Artwork(ilst *node) bool {
	changed := false

	for _, item := range ilst.kids {
		if item.typ != "covr" {
			continue
		}

		payload, kind, ok := atomData(item.body)
		if !ok || (kind != dataJPEG && kind != dataPNG) {
			continue
		}
		smaller, _, err := shrinkArtwork(payload)
		if err != nil {
			continue
		}

		item.body = dataBox(dataJPEG, smaller)
		changed = true
	}
	return changed
}

// FLAC picture block: the image is at the end, behind its media type, its
// description and the dimensions, each of them length-prefixed.
func shrinkFLACPicture(block []byte) ([]byte, bool) {
	if len(block) < 32 {
		return nil, false
	}

	read := func(at int) (int, bool) {
		if at+4 > len(block) {
			return 0, false
		}
		return int(binary.BigEndian.Uint32(block[at : at+4])), true
	}

	offset := 4 // Picture type.
	mimeLen, ok := read(offset)
	if !ok || mimeLen < 0 || offset+4+mimeLen > len(block) {
		return nil, false
	}
	offset += 4 + mimeLen
	mimeAt := offset - mimeLen

	descLen, ok := read(offset)
	if !ok || descLen < 0 || offset+4+descLen > len(block) {
		return nil, false
	}
	offset += 4 + descLen

	// Width, height, depth and colour count, then the image itself.
	offset += 16
	dataLen, ok := read(offset)
	if !ok || offset+4+dataLen > len(block) {
		return nil, false
	}
	image := block[offset+4 : offset+4+dataLen]

	smaller, mime, err := shrinkArtwork(image)
	if err != nil {
		return nil, false
	}

	var out bytes.Buffer
	out.Write(block[:4])
	binary.Write(&out, binary.BigEndian, uint32(len(mime)))
	out.WriteString(mime)
	out.Write(block[mimeAt+mimeLen : offset]) // Description and dimensions.
	binary.Write(&out, binary.BigEndian, uint32(len(smaller)))
	out.Write(smaller)
	return out.Bytes(), true
}

package tags

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"github.com/bogem/id3v2/v2"
)

// bigCover draws a picture that does not compress away, so the tag it goes
// into is as unreadable to a phone as a real cover saved at full size.
func bigCover(t *testing.T, edge int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, edge, edge))
	seed := uint32(1)
	for y := range edge {
		for x := range edge {
			seed = seed*1664525 + 1013904223
			img.Set(x, y, color.RGBA{uint8(seed >> 24), uint8(seed >> 16), uint8(x), 255})
		}
	}

	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// A cover of several megabytes puts the whole tag past what a media scanner
// reads, so the file's artist never reaches the player.
func TestShrinkingArtworkLeavesEverythingElseAlone(t *testing.T) {
	path := buildID3v2(t, 3, false, []byte("audio"))

	cover := bigCover(t, 1400)
	if len(cover) < ScannerTagLimit {
		t.Fatalf("the fixture cover is only %d bytes", len(cover))
	}

	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	tag.AddAttachedPicture(id3v2.PictureFrame{
		Encoding:    id3v2.EncodingUTF8,
		MimeType:    "image/png",
		PictureType: id3v2.PTFrontCover,
		Picture:     cover,
	})
	if err := tag.Save(); err != nil {
		t.Fatal(err)
	}
	tag.Close()

	before, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if before.TagBytes <= ScannerTagLimit {
		t.Fatalf("the fixture's tag is only %d bytes", before.TagBytes)
	}

	if err := Write(path, Edit{ShrinkArtwork: true}); err != nil {
		t.Fatalf("write: %v", err)
	}

	after, err := Read(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if after.TagBytes > ScannerTagLimit {
		t.Errorf("the tag is still %d bytes", after.TagBytes)
	}
	if after.Artist != before.Artist || after.Title != before.Title {
		t.Errorf("the fields changed: %q — %q", after.Artist, after.Title)
	}

	// The cover itself has to survive, only smaller.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("APIC")) {
		t.Fatal("the artwork was dropped instead of shrunk")
	}

	shrunk := coverOf(t, path)
	if shrunk == nil {
		t.Fatal("no picture came back")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(shrunk))
	if err != nil {
		t.Fatalf("the picture no longer decodes: %v", err)
	}
	if config.Width > artworkEdge || config.Height > artworkEdge {
		t.Errorf("the cover is still %dx%d", config.Width, config.Height)
	}
}

func coverOf(t *testing.T, path string) []byte {
	t.Helper()

	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tag.Close()

	for _, frame := range tag.GetFrames(tag.CommonID("Attached picture")) {
		if picture, ok := frame.(id3v2.PictureFrame); ok {
			return picture.Picture
		}
	}
	return nil
}

// A cover already small enough is left exactly as it was, since re-encoding it
// would only lose quality.
func TestSmallArtworkIsLeftAlone(t *testing.T) {
	small := bigCover(t, 200)
	if _, _, err := shrinkArtwork(small); err == nil {
		t.Errorf("a %d-byte picture was re-encoded", len(small))
	}
}

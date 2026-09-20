package tags

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// buildID3v2 writes a file with a 2.3 or 2.4 tag whose frame sizes are stored
// the way sizeAsSynchsafe says, which is how a tagger gets it wrong: 2.3 sizes
// are plain integers and 2.4 sizes are synchsafe, and both are written the
// other way round in the wild.
func buildID3v2(t *testing.T, version byte, sizeAsSynchsafe bool, audio []byte) string {
	t.Helper()

	frame := func(id, text string) []byte {
		body := append([]byte{3}, []byte(text)...) // UTF-8 encoding byte.
		size := len(body)

		out := []byte(id)
		if sizeAsSynchsafe {
			out = append(out, byte(size>>21&0x7F), byte(size>>14&0x7F), byte(size>>7&0x7F), byte(size&0x7F))
		} else {
			out = append(out, byte(size>>24), byte(size>>16), byte(size>>8), byte(size))
		}
		out = append(out, 0, 0) // Frame flags.
		return append(out, body...)
	}

	var frames bytes.Buffer
	frames.Write(frame(frameTitle, "Fall Back"))
	frames.Write(frame(frameArtist, "Juice WRLD"))
	frames.Write(frame(frameAlbum, "JUICE UNRELEASED"))
	frames.Write(frame(frameTrackNo, "1"))
	frames.Write(bytes.Repeat([]byte{0}, 64)) // Padding.

	size := frames.Len()
	header := []byte{'I', 'D', '3', version, 0, 0,
		byte(size >> 21 & 0x7F), byte(size >> 14 & 0x7F),
		byte(size >> 7 & 0x7F), byte(size & 0x7F)}

	var file bytes.Buffer
	file.Write(header)
	file.Write(frames.Bytes())
	file.Write(audio)

	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, file.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A tag whose sizes are written the way the other version writes them is still
// read: what matters is which reading lands on the next frame.
func TestFrameSizesAreReadEitherWay(t *testing.T) {
	cases := []struct {
		name      string
		version   byte
		synchsafe bool
	}{
		{"2.3 as specified", 3, false},
		{"2.3 with synchsafe sizes", 3, true},
		{"2.4 as specified", 4, true},
		{"2.4 with plain sizes", 4, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := buildID3v2(t, c.version, c.synchsafe, []byte("audio"))

			_, frames, err := parseID3v2Frames(path)
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}
			if len(frames) != 4 {
				t.Fatalf("read %d frames, expected 4", len(frames))
			}

			track := trackFromID3Frames(frames)
			if track.Artist != "Juice WRLD" || track.Title != "Fall Back" {
				t.Errorf("read %q — %q", track.Artist, track.Title)
			}
			if track.TrackNo != 1 {
				t.Errorf("track number %d, expected 1", track.TrackNo)
			}
		})
	}
}

// The parser stops where the tag stops making sense instead of losing the
// fields it had already read.
func TestParseKeepsWhatItReadBeforeGarbage(t *testing.T) {
	path := buildID3v2(t, 3, false, []byte("audio"))

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Overwrite the third frame's header with picture data.
	copy(raw[10+(10+11)+(10+12):], []byte{0xf7, 0xe9, 0x93, 0xf8, 0x11, 0x22, 0x33, 0x44})
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	_, frames, err := parseID3v2Frames(path)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("read %d frames, expected the two before the damage", len(frames))
	}
	if track := trackFromID3Frames(frames); track.Title != "Fall Back" {
		t.Errorf("title came back as %q", track.Title)
	}
}

/* The tag at the end of the file ------------------------------------------ */

// appendID3v1 puts the obsolete 128-byte tag after the audio, as the taggers
// that wrote both tags did.
func appendID3v1(t *testing.T, path, title, artist, album string) {
	t.Helper()

	field := func(text string, width int) []byte {
		out := make([]byte, width)
		copy(out, text)
		return out
	}

	var tag bytes.Buffer
	tag.WriteString("TAG")
	tag.Write(field(title, 30))
	tag.Write(field(artist, 30))
	tag.Write(field(album, 30))
	tag.Write(field("2017", 4))
	tag.Write(field("", 30)) // Comment.
	tag.WriteByte(12)        // Genre.

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(tag.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func hasID3v1(t *testing.T, path string) bool {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(raw) >= 128 && string(raw[len(raw)-128:len(raw)-125]) == "TAG"
}

// Android's media database reads the tag at the end of the file whenever the
// real one is too big for it, so a write has to leave the two agreeing.
func TestWritingKeepsTheTagAtTheEndInStep(t *testing.T) {
	path := buildID3v2(t, 3, false, []byte("audio"))
	appendID3v1(t, path, "Fall Back", "Juice WRLD | @uploads_channel", "JUICE UNRELEASED")

	if !hasID3v1(t, path) {
		t.Fatal("the fixture has no tag at the end")
	}

	if err := Write(path, Edit{Artist: Str("Juice WRLD")}); err != nil {
		t.Fatalf("write: %v", err)
	}

	old, ok := readID3v1(path)
	if !ok {
		t.Fatal("the tag at the end was removed instead of corrected")
	}
	if old.Artist != "Juice WRLD" {
		t.Errorf("the tag at the end still says %q", old.Artist)
	}

	track, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if track.Artist != "Juice WRLD" {
		t.Errorf("artist came back as %q", track.Artist)
	}
}

// A name that tag cannot spell is not written into it in a mangled form.
func TestATagAtTheEndThatCannotHoldTheNameIsRemoved(t *testing.T) {
	path := buildID3v2(t, 3, false, []byte("audio"))
	appendID3v1(t, path, "Old Title", "Old Artist", "Old Album")

	if err := Write(path, Edit{Artist: Str("Кишлак")}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if hasID3v1(t, path) {
		t.Error("a Cyrillic name was written into a Latin-1 tag")
	}
}

// Asking for it takes it off outright.
func TestRemovingTheTagAtTheEnd(t *testing.T) {
	path := buildID3v2(t, 3, false, []byte("audio"))
	appendID3v1(t, path, "Fall Back", "Juice WRLD | @uploads_channel", "JUICE UNRELEASED")

	track, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !track.LegacyTag {
		t.Error("the file was not reported as carrying one")
	}

	if err := Write(path, Edit{RemoveLegacyTag: true}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if hasID3v1(t, path) {
		t.Error("the tag at the end survived")
	}
}

// Nothing the old tag held may be lost with it.
func TestTheTagAtTheEndIsKeptBeforeItIsRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "only-v1.mp3")
	if err := os.WriteFile(path, []byte("audio without any tag at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	appendID3v1(t, path, "Old Title", "Old Artist", "Old Album")

	track, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if track.Artist != "Old Artist" || track.Title != "Old Title" {
		t.Fatalf("a file with only the old tag read as %q — %q", track.Artist, track.Title)
	}

	if err := Write(path, Edit{Genre: Str("Trap")}); err != nil {
		t.Fatalf("write: %v", err)
	}

	track, err = Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if track.Artist != "Old Artist" || track.Album != "Old Album" {
		t.Errorf("the old tag was lost: %q — %q", track.Artist, track.Album)
	}
	if track.Genre != "Trap" {
		t.Errorf("genre came back as %q", track.Genre)
	}
}

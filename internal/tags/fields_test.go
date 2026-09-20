package tags_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"musiclibraryorganizer/internal/tags"
)

// Editing several tracks at once is the point of the table view, so every
// field it offers has to survive a write in every format.
func TestWritesEveryField(t *testing.T) {
	requireFFmpeg(t)

	for _, c := range codecs {
		t.Run(c.name, func(t *testing.T) {
			path := fixture(t, c.name, c.args)

			edit := tags.Edit{
				Artist:      tags.Str("New Artist"),
				AlbumArtist: tags.Str("New Album Artist"),
				Album:       tags.Str("New Album"),
				Title:       tags.Str("New Title"),
				Genre:       tags.Str("Trap"),
				Year:        tags.Num(2019),
				TrackNo:     tags.Num(7),
				DiscNo:      tags.Num(2),
			}
			if err := tags.Write(path, edit); err != nil {
				t.Fatalf("write: %v", err)
			}

			track := mustRead(t, path)
			for _, field := range []struct {
				name string
				got  string
				want string
			}{
				{"Artist", track.Artist, "New Artist"},
				{"AlbumArtist", track.AlbumArtist, "New Album Artist"},
				{"Album", track.Album, "New Album"},
				{"Title", track.Title, "New Title"},
				{"Genre", track.Genre, "Trap"},
			} {
				if field.got != field.want {
					t.Errorf("%s = %q, expected %q", field.name, field.got, field.want)
				}
			}
			if track.Year != 2019 {
				t.Errorf("Year = %d, expected 2019", track.Year)
			}
			if track.TrackNo != 7 {
				t.Errorf("TrackNo = %d, expected 7", track.TrackNo)
			}
			if track.DiscNo != 2 {
				t.Errorf("DiscNo = %d, expected 2", track.DiscNo)
			}

			// A third-party reader has to agree, not just our own.
			probed := probeTags(t, path)
			if probed["genre"] != "Trap" {
				t.Errorf("ffprobe genre = %q", probed["genre"])
			}
			if probed["title"] != "New Title" {
				t.Errorf("ffprobe title = %q", probed["title"])
			}
			requirePlayable(t, path)
		})
	}
}

// Setting one field must leave the others exactly as they were.
func TestWritingOneFieldLeavesTheRest(t *testing.T) {
	requireFFmpeg(t)

	for _, c := range codecs {
		t.Run(c.name, func(t *testing.T) {
			path := fixture(t, c.name, c.args)
			before := mustRead(t, path)

			if err := tags.Write(path, tags.Edit{Genre: tags.Str("Emo Rap")}); err != nil {
				t.Fatalf("write: %v", err)
			}

			after := mustRead(t, path)
			if after.Genre != "Emo Rap" {
				t.Errorf("Genre = %q", after.Genre)
			}
			if after.Artist != before.Artist || after.Album != before.Album || after.Title != before.Title {
				t.Errorf("other fields moved: %+v vs %+v", before, after)
			}
			requirePlayable(t, path)
		})
	}
}

/* ID3v2.2 ----------------------------------------------------------------- */

// buildID3v22 writes a file whose tag is version 2.2, which no current encoder
// produces but which real collections are still full of.
func buildID3v22(t *testing.T, audio []byte) string {
	t.Helper()

	frame := func(id, text string) []byte {
		body := append([]byte{0}, []byte(text)...) // Latin-1 encoding byte.
		out := []byte(id)
		out = append(out, byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
		return append(out, body...)
	}

	var frames bytes.Buffer
	frames.Write(frame("TT2", "Old Title"))
	frames.Write(frame("TP1", "Old Artist"))
	frames.Write(frame("TP2", "Various Artists"))
	frames.Write(frame("TAL", "Old Album"))
	frames.Write(frame("TCO", "Hardcore"))
	frames.Write(frame("TRK", "3"))
	frames.Write(frame("TYE", "1998"))
	frames.Write(frame("TCP", "1"))

	// The header size is four synchsafe bytes: seven bits each.
	size := frames.Len()
	header := []byte{'I', 'D', '3', 2, 0, 0,
		byte(size >> 21 & 0x7F), byte(size >> 14 & 0x7F),
		byte(size >> 7 & 0x7F), byte(size & 0x7F)}

	var file bytes.Buffer
	file.Write(header)
	file.Write(frames.Bytes())
	file.Write(audio)

	path := filepath.Join(t.TempDir(), "old.mp3")
	if err := os.WriteFile(path, file.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// audioOf returns the audio of a freshly encoded file, with its tag stripped.
func audioOf(t *testing.T, path string) []byte {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 10 || string(raw[:3]) != "ID3" {
		return raw
	}

	size := int(raw[6]&0x7F)<<21 | int(raw[7]&0x7F)<<14 | int(raw[8]&0x7F)<<7 | int(raw[9]&0x7F)
	return raw[10+size:]
}

func TestReadsID3v22(t *testing.T) {
	requireFFmpeg(t)

	source := fixture(t, "sample.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "3"})
	path := buildID3v22(t, audioOf(t, source))

	track := mustRead(t, path)
	if track.Title != "Old Title" {
		t.Errorf("Title = %q", track.Title)
	}
	if track.Artist != "Old Artist" {
		t.Errorf("Artist = %q", track.Artist)
	}
	if track.AlbumArtist != "Various Artists" {
		t.Errorf("AlbumArtist = %q", track.AlbumArtist)
	}
	if track.Album != "Old Album" {
		t.Errorf("Album = %q", track.Album)
	}
	if track.Genre != "Hardcore" {
		t.Errorf("Genre = %q", track.Genre)
	}
	if track.TrackNo != 3 {
		t.Errorf("TrackNo = %d", track.TrackNo)
	}
	if track.Year != 1998 {
		t.Errorf("Year = %d", track.Year)
	}
	if !track.Compilation {
		t.Error("the compilation flag was not read")
	}
}

// Writing to a 2.2 file upgrades its tag and keeps everything that was in it.
func TestWritingUpgradesID3v22(t *testing.T) {
	requireFFmpeg(t)

	source := fixture(t, "sample.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "3"})
	path := buildID3v22(t, audioOf(t, source))

	err := tags.Write(path, tags.Edit{
		AlbumArtist:       tags.Str(""),
		RemoveCompilation: true,
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	// The tag is no longer version 2.2.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if raw[3] < 3 {
		t.Errorf("tag version is still %d", raw[3])
	}

	track := mustRead(t, path)
	if track.AlbumArtist != "" {
		t.Errorf("AlbumArtist survived: %q", track.AlbumArtist)
	}
	if track.Compilation {
		t.Error("the compilation flag was not cleared")
	}
	for _, field := range []struct{ name, got, want string }{
		{"Title", track.Title, "Old Title"},
		{"Artist", track.Artist, "Old Artist"},
		{"Album", track.Album, "Old Album"},
		{"Genre", track.Genre, "Hardcore"},
	} {
		if field.got != field.want {
			t.Errorf("%s = %q, expected %q", field.name, field.got, field.want)
		}
	}
	if track.Year != 1998 {
		t.Errorf("Year = %d, expected 1998", track.Year)
	}

	requirePlayable(t, path)
}

// The album art in a 2.2 tag has to survive the upgrade: losing it would be a
// silent, unrecoverable change to someone's collection.
func TestUpgradeKeepsArtwork(t *testing.T) {
	requireFFmpeg(t)

	source := fixture(t, "sample.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "3"})
	audio := audioOf(t, source)

	// A one-pixel JPEG is enough: what matters is that the bytes come back.
	image := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0xFF, 0xD9}

	var picture bytes.Buffer
	picture.WriteByte(0)         // Latin-1 encoding.
	picture.WriteString("JPG")   // Image format, as version 2.2 names it.
	picture.WriteByte(3)         // Picture type: front cover.
	picture.WriteString("cover") // Description.
	picture.WriteByte(0)         // Terminator.
	picture.Write(image)

	body := picture.Bytes()
	var frames bytes.Buffer
	frames.WriteString("PIC")
	frames.Write([]byte{byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))})
	frames.Write(body)

	size := frames.Len()
	var file bytes.Buffer
	file.Write([]byte{'I', 'D', '3', 2, 0, 0,
		byte(size >> 21 & 0x7F), byte(size >> 14 & 0x7F),
		byte(size >> 7 & 0x7F), byte(size & 0x7F)})
	file.Write(frames.Bytes())
	file.Write(audio)

	path := filepath.Join(t.TempDir(), "art.mp3")
	if err := os.WriteFile(path, file.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tags.Write(path, tags.Edit{Genre: tags.Str("Trap")}); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, image) {
		t.Error("the artwork did not survive the upgrade")
	}
	if !bytes.Contains(raw, []byte("image/jpeg")) {
		t.Error("the picture frame was not converted to a media type")
	}

	// ffprobe must still see a cover stream.
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries",
		"stream=codec_name", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	if !bytes.Contains(out, []byte("mjpeg")) {
		t.Errorf("no cover stream after the upgrade: %s", out)
	}
}

// ID3v2.4 packs several values into one frame with a null byte between them.
// Read whole, that shows up as "City MorgueSosMula" with an empty box in the
// middle, and the two artists never separate.
func TestMultiValueTagsAreSplit(t *testing.T) {
	cases := map[string]string{
		"City Morgue\x00SosMula": "City Morgue; SosMula",
		"Juice WRLD":             "Juice WRLD",
		"A\x00B\x00C":            "A; B; C",
		"Trailing\x00":           "Trailing",
		"\x00Leading":            "Leading",
		"Bell\x07inside":         "Bellinside",
	}
	for in, want := range cases {
		if got := tags.CleanTextForTest(in); got != want {
			t.Errorf("cleanText(%q) = %q, expected %q", in, got, want)
		}
	}
}

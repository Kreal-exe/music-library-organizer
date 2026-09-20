package tags_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"musiclibraryorganizer/internal/tags"
)

// The handlers are exercised against files a real encoder produced, because the
// bugs worth catching are about container layout rather than our own
// round-tripping. ffmpeg builds the fixtures and ffprobe reads the results back.

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found, skipping", tool)
		}
	}
}

type codec struct {
	name string
	args []string
}

var codecs = []codec{
	{"sample.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "3"}},
	{"sample-v24.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "4"}},
	{"sample.flac", []string{"-c:a", "flac"}},
	{"sample.m4a", []string{"-c:a", "aac"}},
}

// fixture encodes three seconds of silence carrying the tags of a compilation
// rip. Extra ffmpeg arguments are appended before the output path.
func fixture(t *testing.T, name string, codecArgs []string, extra ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)

	args := []string{
		"-v", "error", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", "3",
		"-metadata", "artist=Real Artist",
		"-metadata", "album=Test Album",
		"-metadata", "title=Track One",
		"-metadata", "album_artist=Various Artists",
		"-metadata", "compilation=1",
	}
	args = append(args, codecArgs...)
	args = append(args, extra...)
	args = append(args, path)

	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, out)
	}
	return path
}

func probeTags(t *testing.T, path string) map[string]string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format_tags",
		"-of", "default=noprint_wrappers=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}

	got := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		got[strings.ToLower(strings.TrimPrefix(key, "TAG:"))] = value
	}
	return got
}

// requirePlayable decodes the whole file, so a container corrupted while its
// metadata was edited cannot pass as a success.
func requirePlayable(t *testing.T, path string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil || len(out) > 0 {
		t.Fatalf("file no longer decodes after editing: %v\n%s", err, out)
	}
}

func mustRead(t *testing.T, path string) tags.Track {
	t.Helper()
	track, err := tags.Read(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return track
}

func TestReadsExistingTags(t *testing.T) {
	requireFFmpeg(t)

	for _, c := range codecs {
		t.Run(c.name, func(t *testing.T) {
			track := mustRead(t, fixture(t, c.name, c.args))

			if track.Artist != "Real Artist" {
				t.Errorf("Artist = %q", track.Artist)
			}
			if track.AlbumArtist != "Various Artists" {
				t.Errorf("AlbumArtist = %q", track.AlbumArtist)
			}
			if track.Album != "Test Album" {
				t.Errorf("Album = %q", track.Album)
			}
			if track.Title != "Track One" {
				t.Errorf("Title = %q", track.Title)
			}
			if !track.Compilation {
				t.Error("compilation flag was not read")
			}
			if track.HasLyrics {
				t.Error("lyrics were never written but came back")
			}
			// Three seconds of silence, give or take encoder padding.
			if track.Seconds < 2 || track.Seconds > 4 {
				t.Errorf("Seconds = %d, expected about 3", track.Seconds)
			}
		})
	}
}

func TestErasesAlbumArtistAndCompilation(t *testing.T) {
	requireFFmpeg(t)

	for _, c := range codecs {
		t.Run(c.name, func(t *testing.T) {
			path := fixture(t, c.name, c.args)

			err := tags.Write(path, tags.Edit{
				AlbumArtist:           tags.Str(""),
				RemoveCompilation:     true,
				RemoveAlbumArtistSort: true,
			})
			if err != nil {
				t.Fatalf("write: %v", err)
			}

			got := probeTags(t, path)
			if got["album_artist"] != "" {
				t.Errorf("album_artist survived: %q", got["album_artist"])
			}
			if got["compilation"] != "" {
				t.Errorf("compilation survived: %q", got["compilation"])
			}
			for key, want := range map[string]string{
				"artist": "Real Artist",
				"album":  "Test Album",
				"title":  "Track One",
			} {
				if got[key] != want {
					t.Errorf("%s = %q, expected %q", key, got[key], want)
				}
			}

			requirePlayable(t, path)

			track := mustRead(t, path)
			if track.AlbumArtist != "" || track.Compilation {
				t.Errorf("after editing: AlbumArtist=%q Compilation=%v", track.AlbumArtist, track.Compilation)
			}
		})
	}
}

func TestSetsAlbumArtistToArtist(t *testing.T) {
	requireFFmpeg(t)

	for _, c := range codecs {
		t.Run(c.name, func(t *testing.T) {
			path := fixture(t, c.name, c.args)

			if err := tags.Write(path, tags.Edit{AlbumArtist: tags.Str("Real Artist")}); err != nil {
				t.Fatalf("write: %v", err)
			}

			if got := probeTags(t, path)["album_artist"]; got != "Real Artist" {
				t.Errorf("album_artist = %q", got)
			}
			requirePlayable(t, path)
		})
	}
}

// Renaming is the operation that changes a value's length, which is what makes
// an MP4 rewrite move sample data.
func TestRenamesArtistIncludingNonLatin(t *testing.T) {
	requireFFmpeg(t)

	for _, c := range codecs {
		t.Run(c.name, func(t *testing.T) {
			path := fixture(t, c.name, c.args)

			const renamed = "Гражданская оборона"
			if err := tags.Write(path, tags.Edit{Artist: tags.Str(renamed)}); err != nil {
				t.Fatalf("write: %v", err)
			}

			if got := mustRead(t, path).Artist; got != renamed {
				t.Errorf("Artist = %q, expected %q", got, renamed)
			}
			if got := probeTags(t, path)["artist"]; got != renamed {
				t.Errorf("ffprobe artist = %q, expected %q", got, renamed)
			}
			requirePlayable(t, path)
		})
	}
}

func TestEmbedsLyrics(t *testing.T) {
	requireFFmpeg(t)

	const lyrics = "First line\nSecond line\nТретья строка"

	for _, c := range codecs {
		t.Run(c.name, func(t *testing.T) {
			path := fixture(t, c.name, c.args)

			if err := tags.Write(path, tags.Edit{Lyrics: tags.Str(lyrics)}); err != nil {
				t.Fatalf("write: %v", err)
			}

			track := mustRead(t, path)
			if !track.HasLyrics {
				t.Fatal("lyrics did not come back")
			}
			if track.Lyrics != lyrics {
				t.Errorf("Lyrics = %q, expected %q", track.Lyrics, lyrics)
			}
			if track.Artist != "Real Artist" {
				t.Errorf("Artist was lost: %q", track.Artist)
			}
			requirePlayable(t, path)
		})
	}
}

// A file with no metadata at all makes the MP4 writer build the whole
// udta/meta/ilst chain from scratch.
func TestEmbedsLyricsIntoUntaggedFile(t *testing.T) {
	requireFFmpeg(t)

	const lyrics = "Only the lyrics"

	for _, c := range codecs {
		t.Run(c.name, func(t *testing.T) {
			path := fixture(t, c.name, c.args, "-map_metadata", "-1")

			if err := tags.Write(path, tags.Edit{Lyrics: tags.Str(lyrics)}); err != nil {
				t.Fatalf("write: %v", err)
			}

			if got := mustRead(t, path).Lyrics; got != lyrics {
				t.Errorf("Lyrics = %q, expected %q", got, lyrics)
			}
			requirePlayable(t, path)
		})
	}
}

func TestRepeatedEditIsStable(t *testing.T) {
	requireFFmpeg(t)

	for _, c := range codecs {
		t.Run(c.name, func(t *testing.T) {
			path := fixture(t, c.name, c.args)

			edit := tags.Edit{
				Artist:            tags.Str("Real Artist"),
				AlbumArtist:       tags.Str("Real Artist"),
				Lyrics:            tags.Str("La la la"),
				RemoveCompilation: true,
			}
			for i := range 3 {
				if err := tags.Write(path, edit); err != nil {
					t.Fatalf("write %d: %v", i+1, err)
				}
			}

			track := mustRead(t, path)
			if track.Artist != "Real Artist" || track.AlbumArtist != "Real Artist" {
				t.Errorf("Artist=%q AlbumArtist=%q", track.Artist, track.AlbumArtist)
			}
			if track.Lyrics != "La la la" {
				t.Errorf("Lyrics = %q", track.Lyrics)
			}
			requirePlayable(t, path)
		})
	}
}

func TestBackupKeepsOriginal(t *testing.T) {
	requireFFmpeg(t)

	path := fixture(t, "sample.flac", []string{"-c:a", "flac"})
	err := tags.Write(path, tags.Edit{AlbumArtist: tags.Str(""), Backup: true})
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	backup := path + ".bak"
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("no backup was made: %v", err)
	}
	if got := probeTags(t, backup)["album_artist"]; got != "Various Artists" {
		t.Errorf("album_artist in the backup = %q", got)
	}
}

func TestEmptyEditLeavesFileAlone(t *testing.T) {
	requireFFmpeg(t)

	path := fixture(t, "sample.m4a", []string{"-c:a", "aac"})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := tags.Write(path, tags.Edit{}); err != nil {
		t.Fatalf("write: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("an empty edit changed the file")
	}
}

func TestUnsupportedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if tags.Supported(path) {
		t.Error("txt must not count as supported")
	}
	if _, err := tags.Read(path); err == nil {
		t.Error("expected an error for an unsupported format")
	}
}

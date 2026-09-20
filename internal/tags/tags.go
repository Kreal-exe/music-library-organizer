// Package tags reads and writes the metadata this tool edits — artist, album
// artist, the compilation flag and lyrics — across MP3, FLAC and MP4 files.
//
// Music players build their artist list from whatever album-artist field a file
// carries, so a compilation tagged "Various Artists", or a track credited to
// "Artist feat. Guest", shows up as an artist nobody wants. Everything here
// exists to bring those fields back under control.
package tags

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Track is the metadata this tool cares about, read from one file.
type Track struct {
	Path        string `json:"path"`
	Format      string `json:"format"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	AlbumArtist string `json:"albumArtist"`
	Album       string `json:"album"`
	Genre       string `json:"genre"`
	TrackNo     int    `json:"trackNo"`
	DiscNo      int    `json:"discNo"`
	Year        int    `json:"year"`
	Compilation bool   `json:"compilation"`
	HasSort     bool   `json:"hasSort"`
	// LegacyTag marks an MP3 still carrying the obsolete 128-byte tag at the
	// end of the file. Android's media database prefers it to the real tags,
	// so a file with one shows names nothing else in the library agrees with.
	LegacyTag bool `json:"legacyTag"`
	// TagBytes is how much of the file its metadata takes up. A player's
	// scanner stops reading long before this can grow without limit, and one
	// cover saved at full resolution is enough to get there.
	TagBytes  int           `json:"tagBytes"`
	Lyrics    string        `json:"-"`
	HasLyrics bool          `json:"hasLyrics"`
	Duration  time.Duration `json:"-"`
	Seconds   int           `json:"seconds"`
}

// Edit describes a change to one file. A nil field is left untouched; a pointer
// to the empty string removes the field entirely.
type Edit struct {
	Artist      *string `json:"artist,omitempty"`
	AlbumArtist *string `json:"albumArtist,omitempty"`
	Album       *string `json:"album,omitempty"`
	Title       *string `json:"title,omitempty"`
	Genre       *string `json:"genre,omitempty"`
	Lyrics      *string `json:"lyrics,omitempty"`

	// Numbers are cleared by setting them to zero.
	Year    *int `json:"year,omitempty"`
	TrackNo *int `json:"trackNo,omitempty"`
	DiscNo  *int `json:"discNo,omitempty"`

	RemoveCompilation     bool `json:"removeCompilation,omitempty"`
	RemoveAlbumArtistSort bool `json:"removeAlbumArtistSort,omitempty"`
	RemoveLegacyTag       bool `json:"removeLegacyTag,omitempty"`
	// ShrinkArtwork re-encodes embedded covers down to a size a player reads.
	ShrinkArtwork bool `json:"shrinkArtwork,omitempty"`

	// Backup writes a ".bak" copy beside the file before it is modified.
	Backup bool `json:"backup,omitempty"`
}

// Empty reports whether the edit would change nothing.
func (e Edit) Empty() bool {
	for _, field := range []*string{e.Artist, e.AlbumArtist, e.Album, e.Title, e.Genre, e.Lyrics} {
		if field != nil {
			return false
		}
	}
	for _, number := range []*int{e.Year, e.TrackNo, e.DiscNo} {
		if number != nil {
			return false
		}
	}
	return !e.RemoveCompilation && !e.RemoveAlbumArtistSort && !e.RemoveLegacyTag && !e.ShrinkArtwork
}

// Str and Num are conveniences for building an Edit field.
func Str(s string) *string { return &s }
func Num(n int) *int       { return &n }

// ErrUnsupported marks a file whose extension this package does not handle.
var ErrUnsupported = errors.New("unsupported format")

type format struct {
	name  string
	read  func(string) (Track, error)
	write func(string, Edit) error
}

var formats = map[string]format{
	".mp3":  {"MP3", readMP3, writeMP3},
	".flac": {"FLAC", readFLAC, writeFLAC},
	".m4a":  {"M4A", readMP4, writeMP4},
	".m4b":  {"M4A", readMP4, writeMP4},
	".m4p":  {"M4A", readMP4, writeMP4},
	".mp4":  {"MP4", readMP4, writeMP4},
	".aac":  {"M4A", readMP4, writeMP4},
}

// Supported reports whether this package can read the file at path.
func Supported(path string) bool {
	_, ok := formats[strings.ToLower(filepath.Ext(path))]
	return ok
}

// Read loads the metadata of one file.
func Read(path string) (Track, error) {
	f, ok := formats[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return Track{}, ErrUnsupported
	}

	track, err := f.read(path)
	if err != nil {
		return Track{}, err
	}

	track.Path = path
	track.Format = f.name
	track.HasLyrics = strings.TrimSpace(track.Lyrics) != ""
	track.Seconds = int(track.Duration.Round(time.Second) / time.Second)
	return track, nil
}

// Write applies an edit to one file, leaving it untouched if the edit is empty.
func Write(path string, edit Edit) error {
	f, ok := formats[strings.ToLower(filepath.Ext(path))]
	if !ok {
		return ErrUnsupported
	}
	if edit.Empty() {
		return nil
	}

	info, _ := os.Stat(path)
	if edit.Backup {
		if err := backup(path); err != nil {
			return wrap("backup", err)
		}
	}
	if err := f.write(path, edit); err != nil {
		return err
	}

	// Erasing a tag should not reshuffle libraries that sort by "recently
	// modified", so the original timestamp is put back.
	if info != nil {
		_ = os.Chtimes(path, info.ModTime(), info.ModTime())
	}
	return nil
}

// backup copies path to path+".bak". An existing backup is kept: it came from
// an earlier run and is therefore closer to the original.
func backup(path string) error {
	dst := path + ".bak"
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

// replaceFile swaps a freshly written temporary file in for the original, so a
// half-written file never replaces a good track.
func replaceFile(tmp, path string) error {
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return wrap("replacing file", err)
	}
	return nil
}

// normalizeKey folds away the case, spacing and punctuation that taggers
// disagree about, so "ALBUM ARTIST", "album_artist" and "AlbumArtist" match.
func normalizeKey(s string) string {
	return strings.NewReplacer(" ", "", "_", "", "-", "").Replace(strings.ToUpper(strings.TrimSpace(s)))
}

// cleanText tidies a value read out of a tag.
//
// ID3v2.4 separates several values inside one frame with a null byte, and
// Vorbis and MP4 files written from such a tag inherit the habit. Read whole,
// those come out as "City MorgueSosmula" with an empty box between the names.
// Splitting them onto a separator the artist reduction already understands
// turns them back into two artists. Any other control character is dropped,
// since it can only show up as a box.
func cleanText(value string) string {
	if value == "" {
		return ""
	}

	parts := strings.FieldsFunc(value, func(r rune) bool { return r == 0 })
	for i, part := range parts {
		parts[i] = strings.TrimSpace(strings.Map(dropControl, part))
	}

	kept := parts[:0]
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "; ")
}

func dropControl(r rune) rune {
	// Newlines and tabs are legitimate inside lyrics; nothing else below space
	// belongs in a tag.
	if r < 0x20 && r != '\n' && r != '\t' {
		return -1
	}
	return r
}

func wrap(stage string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", stage, err)
}

// Field keys shared by the Vorbis-comment and MP4 freeform handlers, matched
// after normalizeKey.
var (
	albumArtistKeys = []string{"ALBUMARTIST", "TPE2"}
	compilationKeys = []string{"COMPILATION", "ITUNESCOMPILATION", "TCMP"}
	sortKeys        = []string{"ALBUMARTISTSORT", "ALBUMARTISTSORTORDER", "SORTALBUMARTIST", "TSO2"}
	lyricsKeys      = []string{"LYRICS", "UNSYNCEDLYRICS", "UNSYNCHRONISEDLYRICS"}
)

func matchesAny(key string, candidates []string) bool {
	norm := normalizeKey(key)
	for _, candidate := range candidates {
		if norm == candidate {
			return true
		}
	}
	return false
}

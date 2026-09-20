package tags

// ID3v2 tagging. Standard fields live in their own frames; anything else is a
// TXXX frame keyed by a free-form description, which taggers spell in several
// ways — including stuffing a native frame name such as "TCMP" in there.

import (
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/bogem/id3v2/v2"
)

const (
	frameTitle       = "TIT2"
	frameArtist      = "TPE1"
	frameAlbumArtist = "TPE2"
	frameAlbum       = "TALB"
	frameGenre       = "TCON"
	frameTrackNo     = "TRCK"
	frameDiscNo      = "TPOS"
	frameCompilation = "TCMP"
	frameSortAA      = "TSO2"
	frameLyrics      = "USLT"
	frameUserText    = "TXXX"
	frameYear24      = "TDRC"
	frameYear23      = "TYER"
)

func readMP3(path string) (Track, error) {
	var track Track

	// The library below refuses ID3v2.2, which rippers of the late nineties
	// wrote and which is still sitting in real collections.
	if isID3v22(path) {
		track, err := readMP3v22(path)
		track.TagBytes = id3TagSize(path)
		return track, err
	}

	// The file is opened here rather than by the library, which leaves it open
	// when it gives up on a tag — and a file still open cannot be replaced.
	f, err := os.Open(path)
	if err != nil {
		return track, wrap("reading ID3", err)
	}
	defer f.Close()

	tag, err := id3v2.ParseReader(f, id3v2.Options{Parse: true})
	if err != nil {
		// A tag the library cannot walk end to end still holds its fields, so
		// they are read out of it by hand rather than losing the file.
		if lenient, lerr := readMP3Lenient(path); lerr == nil {
			lenient.TagBytes = id3TagSize(path)
			return withID3v1(lenient, path), nil
		}
		return track, wrap("reading ID3", err)
	}

	track.Title = cleanText(tag.GetTextFrame(frameTitle).Text)
	track.Artist = cleanText(tag.GetTextFrame(frameArtist).Text)
	track.AlbumArtist = cleanText(tag.GetTextFrame(frameAlbumArtist).Text)
	track.Album = cleanText(tag.GetTextFrame(frameAlbum).Text)
	track.Genre = cleanText(tag.GetTextFrame(frameGenre).Text)
	track.TrackNo = leadingNumber(tag.GetTextFrame(frameTrackNo).Text)
	track.DiscNo = leadingNumber(tag.GetTextFrame(frameDiscNo).Text)
	track.Compilation = isTrue(tag.GetTextFrame(frameCompilation).Text)
	track.HasSort = tag.GetTextFrame(frameSortAA).Text != ""

	track.Year = parseYear(tag.GetTextFrame(frameYear24).Text)
	if track.Year == 0 {
		track.Year = parseYear(tag.GetTextFrame(frameYear23).Text)
	}

	for _, frame := range tag.GetFrames(tag.CommonID("Unsynchronised lyrics/text transcription")) {
		if uslf, ok := frame.(id3v2.UnsynchronisedLyricsFrame); ok && uslf.Lyrics != "" {
			track.Lyrics = uslf.Lyrics
			break
		}
	}

	for _, udtf := range userTextFrames(tag) {
		switch {
		case matchesAny(udtf.Description, albumArtistKeys) && track.AlbumArtist == "":
			track.AlbumArtist = cleanText(udtf.Value)
		case matchesAny(udtf.Description, lyricsKeys) && track.Lyrics == "":
			track.Lyrics = udtf.Value
		case matchesAny(udtf.Description, compilationKeys) && isTrue(udtf.Value):
			track.Compilation = true
		case matchesAny(udtf.Description, sortKeys) && udtf.Value != "":
			track.HasSort = true
		}
	}

	track.Duration = mp3Duration(path)
	track.TagBytes = id3TagSize(path)

	return withID3v1(track, path), nil
}

// withID3v1 fills in from the old tag at the end of the file whatever the
// modern one at the front does not say. A file carrying only the old tag would
// otherwise read as having no artist at all.
func withID3v1(track Track, path string) Track {
	old, ok := readID3v1(path)
	if !ok {
		return track
	}
	track.LegacyTag = true

	if track.Title != "" && track.Artist != "" && track.Album != "" {
		return track
	}

	for _, field := range []struct {
		into  *string
		value string
	}{
		{&track.Title, old.Title},
		{&track.Artist, old.Artist},
		{&track.Album, old.Album},
	} {
		if *field.into == "" {
			*field.into = field.value
		}
	}
	if track.Year == 0 {
		track.Year = parseYear(old.Year)
	}
	if track.TrackNo == 0 {
		track.TrackNo = old.Track
	}
	return track
}

func writeMP3(path string, edit Edit) error {
	// A 2.2 tag is brought up to 2.3 first; from there the ordinary path
	// handles it, and the save below lifts it the rest of the way to 2.4.
	if isID3v22(path) {
		if err := upgradeID3v22(path); err != nil {
			return err
		}
	}

	// A tag the library cannot walk is written back out frame by frame as it
	// was read by hand, which leaves it something it can open. The check comes
	// first because the library holds the file open when it gives up, and a
	// file still open cannot be replaced.
	if !canParseID3(path) {
		if err := rebuildID3Tag(path); err != nil {
			return wrap("repairing ID3", err)
		}
	}

	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return wrap("reading ID3", err)
	}
	defer tag.Close()

	// Everything is stored as UTF-8. ID3v2.3 offers only ISO-8859-1, which
	// cannot hold Cyrillic, and UTF-16, which the underlying library writes
	// with a mismatched BOM and an odd-length pad — so a v2.3 tag is raised to
	// v2.4, where UTF-8 is part of the spec.
	encoding := id3v2.EncodingUTF8
	if tag.Version() < 4 {
		tag.SetVersion(4)
	}
	reencodeAsUTF8(tag)

	if edit.ShrinkArtwork {
		shrinkID3Artwork(tag)
	}

	// TXXX frames can only be deleted a whole ID at a time, so the survivors
	// are collected and written back.
	var dropKeys [][]string

	for _, field := range []struct {
		id    string
		value *string
	}{
		{frameArtist, edit.Artist},
		{frameAlbum, edit.Album},
		{frameTitle, edit.Title},
		{frameGenre, edit.Genre},
	} {
		if field.value != nil {
			setTextFrame(tag, field.id, encoding, *field.value)
		}
	}
	for _, number := range []struct {
		id    string
		value *int
	}{
		{frameTrackNo, edit.TrackNo},
		{frameDiscNo, edit.DiscNo},
	} {
		if number.value != nil {
			setTextFrame(tag, number.id, encoding, formatNumber(*number.value))
		}
	}
	if edit.Year != nil {
		// A v2.4 tag records the year in TDRC; the older TYER is removed so the
		// two cannot disagree.
		tag.DeleteFrames(frameYear23)
		setTextFrame(tag, frameYear24, encoding, formatNumber(*edit.Year))
	}
	if edit.AlbumArtist != nil {
		setTextFrame(tag, frameAlbumArtist, encoding, *edit.AlbumArtist)
		if *edit.AlbumArtist == "" {
			dropKeys = append(dropKeys, albumArtistKeys)
		}
	}
	if edit.RemoveCompilation {
		tag.DeleteFrames(frameCompilation)
		dropKeys = append(dropKeys, compilationKeys)
	}
	if edit.RemoveAlbumArtistSort {
		tag.DeleteFrames(frameSortAA)
		dropKeys = append(dropKeys, sortKeys)
	}
	if edit.Lyrics != nil {
		tag.DeleteFrames(tag.CommonID("Unsynchronised lyrics/text transcription"))
		dropKeys = append(dropKeys, lyricsKeys)
		if *edit.Lyrics != "" {
			tag.AddUnsynchronisedLyricsFrame(id3v2.UnsynchronisedLyricsFrame{
				Encoding: encoding,
				Language: "xxx", // Undetermined, per ISO 639-2.
				Lyrics:   *edit.Lyrics,
			})
		}
	}

	if len(dropKeys) > 0 {
		pruneUserTextFrames(tag, dropKeys)
	}

	// Whatever only the old tag at the end of the file said is moved into this
	// one before that tag is taken off below.
	mergeID3v1(tag, path, encoding)

	if err := tag.Save(); err != nil {
		return wrap("writing ID3", err)
	}
	if err := tag.Close(); err != nil {
		return wrap("writing ID3", err)
	}

	// The obsolete tag at the end of the file is what Android's media database
	// reads whenever this one is too large for it, so it is either brought
	// into line or, if the user asked for it, taken off.
	// Removing it is refused for the one file that needs it: a tag too large
	// for a media scanner to read leaves the small one at the end as the only
	// thing naming the artist, and taking it off would lose the name entirely.
	if edit.RemoveLegacyTag && !tagTooBigToScan(path) {
		if err := stripID3v1(path); err != nil {
			return wrap("writing ID3", err)
		}
		return nil
	}
	if err := syncID3v1(path, tag); err != nil {
		return wrap("writing ID3", err)
	}
	return nil
}

// reencodeAsUTF8 rewrites every frame already in the tag as UTF-8. Frames
// parsed from a v2.3 tag keep the encoding they were stored with, and saving
// them unchanged would push UTF-16 text back through the broken writer.
func reencodeAsUTF8(tag *id3v2.Tag) {
	snapshot := tag.AllFrames()
	ids := make([]string, 0, len(snapshot))
	for id := range snapshot {
		ids = append(ids, id)
	}
	sort.Strings(ids) // Map order is random; keep output reproducible.

	tag.DeleteAllFrames()
	for _, id := range ids {
		for _, frame := range snapshot[id] {
			tag.AddFrame(id, withUTF8(frame))
		}
	}
}

// withUTF8 returns the frame with its text encoding switched to UTF-8. Frames
// that carry no text, such as pictures' binary payloads, are returned as they
// are.
func withUTF8(frame id3v2.Framer) id3v2.Framer {
	encoding := id3v2.EncodingUTF8

	switch f := frame.(type) {
	case id3v2.TextFrame:
		f.Encoding = encoding
		return f
	case id3v2.UserDefinedTextFrame:
		f.Encoding = encoding
		return f
	case id3v2.CommentFrame:
		f.Encoding = encoding
		return f
	case id3v2.UnsynchronisedLyricsFrame:
		f.Encoding = encoding
		return f
	case id3v2.PictureFrame:
		f.Encoding = encoding
		return f
	default:
		return frame
	}
}

func setTextFrame(tag *id3v2.Tag, id string, encoding id3v2.Encoding, value string) {
	tag.DeleteFrames(id)
	if value != "" {
		tag.AddTextFrame(id, encoding, value)
	}
}

func userTextFrames(tag *id3v2.Tag) []id3v2.UserDefinedTextFrame {
	var out []id3v2.UserDefinedTextFrame
	for _, frame := range tag.GetFrames(tag.CommonID("User defined text information frame")) {
		if udtf, ok := frame.(id3v2.UserDefinedTextFrame); ok {
			out = append(out, udtf)
		}
	}
	return out
}

// pruneUserTextFrames drops the TXXX frames whose description matches any of
// the given key sets, and restores the rest.
func pruneUserTextFrames(tag *id3v2.Tag, keySets [][]string) {
	var keep []id3v2.UserDefinedTextFrame
	dropped := false

	for _, udtf := range userTextFrames(tag) {
		matched := false
		for _, keys := range keySets {
			if matchesAny(udtf.Description, keys) {
				matched = true
				break
			}
		}
		if matched {
			dropped = true
			continue
		}
		keep = append(keep, udtf)
	}
	if !dropped {
		return
	}

	tag.DeleteFrames(frameUserText)
	for _, udtf := range keep {
		tag.AddUserDefinedTextFrame(udtf)
	}
}

// leadingNumber reads the "3" out of values like "3", "3/12" or "03".
func leadingNumber(s string) int {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0
	}
	return n
}

// formatNumber renders a tag number, with zero meaning the field is cleared.
func formatNumber(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func isTrue(s string) bool {
	switch strings.TrimSpace(s) {
	case "", "0", "false", "no":
		return false
	}
	return true
}

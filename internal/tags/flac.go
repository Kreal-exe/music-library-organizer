package tags

// FLAC tagging. Everything lives in one VORBIS_COMMENT block as free-form
// "KEY=value" lines, and taggers disagree on the spelling of the keys, so they
// are matched through normalizeKey.

import (
	"os"
	"strings"
	"time"

	flacvorbis "github.com/go-flac/flacvorbis/v2"
	flac "github.com/go-flac/go-flac/v2"
)

const (
	vorbisTitle       = "TITLE"
	vorbisArtist      = "ARTIST"
	vorbisAlbumArtist = "ALBUMARTIST"
	vorbisAlbum       = "ALBUM"
	vorbisTrackNo     = "TRACKNUMBER"
	vorbisDiscNo      = "DISCNUMBER"
	vorbisDate        = "DATE"
	vorbisLyrics      = "LYRICS"
	vorbisGenre       = "GENRE"
)

func readFLAC(path string) (Track, error) {
	var track Track

	f, err := flac.ParseFile(path)
	if err != nil {
		return track, wrap("reading FLAC", err)
	}
	defer f.Close()

	if info, err := f.GetStreamInfo(); err == nil && info.SampleRate > 0 {
		track.Duration = time.Duration(info.SampleCount) * time.Second /
			time.Duration(info.SampleRate)
	}

	for _, meta := range f.Meta {
		track.TagBytes += len(meta.Data) + 4 // Each block carries a four-byte header.
	}

	block := commentBlock(f)
	if block == nil {
		return track, nil
	}

	comment, err := flacvorbis.ParseFromMetaDataBlock(*block)
	if err != nil {
		return track, wrap("reading FLAC comments", err)
	}

	for _, entry := range comment.Comments {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		value = cleanText(value)

		switch norm := normalizeKey(key); {
		case norm == vorbisTitle:
			track.Title = value
		case norm == vorbisArtist && track.Artist == "":
			track.Artist = value
		case norm == vorbisAlbum:
			track.Album = value
		case norm == vorbisGenre:
			track.Genre = value
		case norm == vorbisTrackNo:
			track.TrackNo = leadingNumber(value)
		case norm == vorbisDiscNo:
			track.DiscNo = leadingNumber(value)
		case norm == vorbisDate:
			track.Year = parseYear(value)
		case matchesAny(key, albumArtistKeys) && track.AlbumArtist == "":
			track.AlbumArtist = value
		case matchesAny(key, lyricsKeys) && track.Lyrics == "":
			track.Lyrics = value
		case matchesAny(key, compilationKeys):
			track.Compilation = isTrue(value)
		case matchesAny(key, sortKeys):
			track.HasSort = value != ""
		}
	}

	return track, nil
}

func writeFLAC(path string, edit Edit) error {
	f, err := flac.ParseFile(path)
	if err != nil {
		return wrap("reading FLAC", err)
	}
	defer f.Close()

	block := commentBlock(f)
	comment := flacvorbis.New()
	if block != nil {
		comment, err = flacvorbis.ParseFromMetaDataBlock(*block)
		if err != nil {
			return wrap("reading FLAC comments", err)
		}
	}

	comment.Comments = applyFLACEdit(comment.Comments, edit)

	if edit.ShrinkArtwork {
		for i, meta := range f.Meta {
			if meta.Type != flac.Picture {
				continue
			}
			if smaller, ok := shrinkFLACPicture(meta.Data); ok {
				f.Meta[i] = &flac.MetaDataBlock{Type: flac.Picture, Data: smaller}
			}
		}
	}

	rebuilt := comment.Marshal()
	if block != nil {
		for i, meta := range f.Meta {
			if meta.Type == flac.VorbisComment {
				f.Meta[i] = &rebuilt
				break
			}
		}
	} else {
		f.Meta = append(f.Meta, &rebuilt)
	}

	// Written beside the original and swapped in, so a half-written file never
	// replaces a good track.
	tmp := path + ".mlm-tmp"
	if err := f.Save(tmp); err != nil {
		os.Remove(tmp)
		return wrap("writing FLAC", err)
	}
	return replaceFile(tmp, path)
}

// applyFLACEdit rewrites the comment list: matching keys are dropped, and the
// new values are appended once.
func applyFLACEdit(comments []string, edit Edit) []string {
	type change struct {
		keys  []string
		write string // The key to write under, empty when only removing.
		value *string
	}

	changes := []change{
		{keys: []string{vorbisArtist}, write: vorbisArtist, value: edit.Artist},
		{keys: albumArtistKeys, write: vorbisAlbumArtist, value: edit.AlbumArtist},
		{keys: []string{vorbisAlbum}, write: vorbisAlbum, value: edit.Album},
		{keys: []string{vorbisTitle}, write: vorbisTitle, value: edit.Title},
		{keys: []string{vorbisGenre}, write: vorbisGenre, value: edit.Genre},
		{keys: lyricsKeys, write: vorbisLyrics, value: edit.Lyrics},
	}
	for _, number := range []struct {
		key   string
		value *int
	}{
		{vorbisDate, edit.Year},
		{vorbisTrackNo, edit.TrackNo},
		{vorbisDiscNo, edit.DiscNo},
	} {
		if number.value != nil {
			changes = append(changes, change{
				keys:  []string{number.key},
				write: number.key,
				value: Str(formatNumber(*number.value)),
			})
		}
	}
	if edit.RemoveCompilation {
		changes = append(changes, change{keys: compilationKeys, value: Str("")})
	}
	if edit.RemoveAlbumArtistSort {
		changes = append(changes, change{keys: sortKeys, value: Str("")})
	}

	kept := comments[:0]
	for _, entry := range comments {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			kept = append(kept, entry)
			continue
		}

		drop := false
		for _, c := range changes {
			if c.value != nil && matchesAny(key, c.keys) {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, entry)
		}
	}

	for _, c := range changes {
		if c.value != nil && *c.value != "" && c.write != "" {
			kept = append(kept, c.write+"="+*c.value)
		}
	}
	return kept
}

func commentBlock(f *flac.File) *flac.MetaDataBlock {
	for _, block := range f.Meta {
		if block.Type == flac.VorbisComment {
			return block
		}
	}
	return nil
}

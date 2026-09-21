// Package plan turns the choices made in the interface into per-file edits.
//
// Everything is computed before anything is written, so the user sees exactly
// which files change and how, and a run can be cancelled without leaving the
// library half-converted.
//
// Two kinds of choice arrive here. Rules apply across the whole library — the
// artist names to merge, what to do with the album artist. Manual edits apply
// to tracks picked out in the table, and win over the rules, because they were
// the more deliberate act.
package plan

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"musiclibraryorganizer/internal/library"
	"musiclibraryorganizer/internal/tags"
)

// How the album-artist field should be treated.
const (
	AlbumArtistKeep       = "keep"
	AlbumArtistClear      = "clear"
	AlbumArtistFromArtist = "fromArtist"
)

// Rules are the library-wide choices.
type Rules struct {
	// Rename maps an artist name found in the files to the name to keep. It is
	// how several spellings and guest credits collapse onto one artist.
	Rename map[string]string `json:"rename"`

	// AlbumRename gives tracks another album name, by path. An album name is
	// only unique together with the artist it is by, and the interface has
	// already worked out which tracks make up each album, so it says exactly
	// which tracks it means.
	AlbumRename map[string]string `json:"albumRename"`
	// ClearAlbum lists the tracks whose album field goes, by path: the ones
	// carrying a name that is no album, such as the site they came from.
	ClearAlbum []string `json:"clearAlbum"`

	// AlbumArtistSet gives tracks the album artist the rest of their album
	// carries, by path. A player files an album by its name and its album
	// artist together, so one album whose tracks disagree on the second shows
	// up as two albums of the same name.
	AlbumArtistSet map[string]string `json:"albumArtistSet"`
	// ClearCompilation lists the tracks whose compilation flag goes, by path:
	// a stray flag on one track of an album files that track apart.
	ClearCompilation []string `json:"clearCompilation"`

	AlbumArtistMode   string `json:"albumArtistMode"`
	RemoveCompilation bool   `json:"removeCompilation"`
	RemoveSort        bool   `json:"removeSort"`
	RemoveLegacy      bool   `json:"removeLegacy"`
	ShrinkArtwork     bool   `json:"shrinkArtwork"`
	Backup            bool   `json:"backup"`
}

// Change is one file's pending edit.
type Change struct {
	Path   string  `json:"path"`
	Fields []Field `json:"fields"`
	edit   tags.Edit
}

// Edit exposes the change to be written, for callers that have to send it
// somewhere other than the local disk.
func (c Change) Edit() tags.Edit { return c.edit }

// Field is one value the user will see change.
type Field struct {
	Name   string `json:"name"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Summary counts what a plan would do, for the confirmation step.
type Summary struct {
	Files             int `json:"files"`
	ArtistRenamed     int `json:"artistRenamed"`
	AlbumArtistSet    int `json:"albumArtistSet"`
	AlbumArtistClear  int `json:"albumArtistCleared"`
	CompilationRemove int `json:"compilationRemoved"`
	GenreSet          int `json:"genreSet"`
	AlbumRenamed      int `json:"albumRenamed"`
}

// Build works out the edit for every track the choices touch. Manual carries
// the edits made to individual tracks, keyed by path.
func Build(lib *library.Library, rules Rules, manual map[string]tags.Edit) []Change {
	sets := trackSets{
		clearAlbum:       pathSet(rules.ClearAlbum),
		clearCompilation: pathSet(rules.ClearCompilation),
	}

	changes := []Change{}
	for _, track := range lib.Tracks {
		if change, ok := buildOne(track, rules, sets, manual[track.Path]); ok {
			changes = append(changes, change)
		}
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

// wanted is what a track's fields should end up as.
type wanted struct {
	artist      string
	albumArtist string
	album       string
	title       string
	genre       string
	year        int
	trackNo     int
	discNo      int
}

// trackSets are the rules that name tracks by path, ready to be looked up.
type trackSets struct {
	clearAlbum       map[string]bool
	clearCompilation map[string]bool
}

func pathSet(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, path := range paths {
		set[path] = true
	}
	return set
}

func buildOne(track tags.Track, rules Rules, sets trackSets, manual tags.Edit) (Change, bool) {
	want := wanted{
		artist:      rename(track.Artist, rules),
		albumArtist: rename(track.AlbumArtist, rules),
		album:       track.Album,
		title:       track.Title,
		genre:       track.Genre,
		year:        track.Year,
		trackNo:     track.TrackNo,
		discNo:      track.DiscNo,
	}

	if renamed := strings.TrimSpace(rules.AlbumRename[track.Path]); renamed != "" && renamed != want.album {
		// A disc filed as an album of its own keeps its place in the album it
		// rejoins, unless the track already says which disc it is on.
		if disc := library.DiscOf(want.album); disc > 0 && want.discNo == 0 && library.DiscOf(renamed) == 0 {
			want.discNo = disc
		}
		want.album = renamed
	}
	if sets.clearAlbum[track.Path] {
		want.album = ""
	}

	switch rules.AlbumArtistMode {
	case AlbumArtistClear:
		want.albumArtist = ""
	case AlbumArtistFromArtist:
		want.albumArtist = want.artist
	}
	// Removing every album artist leaves the albums in agreement already;
	// otherwise the one the album shares wins over what the mode made of it.
	if shared, ok := rules.AlbumArtistSet[track.Path]; ok && rules.AlbumArtistMode != AlbumArtistClear {
		want.albumArtist = strings.TrimSpace(shared)
	}

	// An edit made by hand to this track overrides whatever the rules decided.
	overrideString(&want.artist, manual.Artist)
	overrideString(&want.albumArtist, manual.AlbumArtist)
	overrideString(&want.album, manual.Album)
	overrideString(&want.title, manual.Title)
	overrideString(&want.genre, manual.Genre)
	overrideInt(&want.year, manual.Year)
	overrideInt(&want.trackNo, manual.TrackNo)
	overrideInt(&want.discNo, manual.DiscNo)

	change := Change{Path: track.Path}

	textFields := []struct {
		label  string
		before string
		after  string
		into   **string
	}{
		{"Artist", track.Artist, want.artist, &change.edit.Artist},
		{"Album artist", track.AlbumArtist, want.albumArtist, &change.edit.AlbumArtist},
		{"Album", track.Album, want.album, &change.edit.Album},
		{"Title", track.Title, want.title, &change.edit.Title},
		{"Genre", track.Genre, want.genre, &change.edit.Genre},
	}
	for _, field := range textFields {
		if field.after != field.before {
			*field.into = tags.Str(field.after)
			change.Fields = append(change.Fields, Field{field.label, field.before, field.after})
		}
	}

	numberFields := []struct {
		label  string
		before int
		after  int
		into   **int
	}{
		{"Year", track.Year, want.year, &change.edit.Year},
		{"Track number", track.TrackNo, want.trackNo, &change.edit.TrackNo},
		{"Disc number", track.DiscNo, want.discNo, &change.edit.DiscNo},
	}
	for _, field := range numberFields {
		if field.after != field.before {
			*field.into = tags.Num(field.after)
			change.Fields = append(change.Fields, Field{
				field.label, formatNumber(field.before), formatNumber(field.after),
			})
		}
	}

	if manual.Lyrics != nil && *manual.Lyrics != track.Lyrics {
		change.edit.Lyrics = manual.Lyrics
		change.Fields = append(change.Fields, Field{"Lyrics", summarise(track.Lyrics), summarise(*manual.Lyrics)})
	}
	if (rules.RemoveCompilation || manual.RemoveCompilation || sets.clearCompilation[track.Path]) && track.Compilation {
		change.edit.RemoveCompilation = true
		change.Fields = append(change.Fields, Field{"Compilation flag", "yes", ""})
	}
	if (rules.RemoveSort || manual.RemoveAlbumArtistSort) && track.HasSort {
		change.edit.RemoveAlbumArtistSort = true
		change.Fields = append(change.Fields, Field{"Album artist sort", "set", ""})
	}
	if (rules.RemoveLegacy || manual.RemoveLegacyTag) && track.LegacyTag {
		change.edit.RemoveLegacyTag = true
		change.Fields = append(change.Fields, Field{"Old tag at the end of the file", "set", ""})
	}
	if (rules.ShrinkArtwork || manual.ShrinkArtwork) && track.TagBytes > tags.ScannerTagLimit {
		change.edit.ShrinkArtwork = true
		change.Fields = append(change.Fields, Field{
			"Artwork", megabytes(track.TagBytes) + " of tags", "shrunk to fit a player",
		})
	}

	if change.edit.Empty() {
		return Change{}, false
	}
	change.edit.Backup = rules.Backup
	return change, true
}

func overrideString(target *string, value *string) {
	if value != nil {
		*target = strings.TrimSpace(*value)
	}
}

func overrideInt(target *int, value *int) {
	if value != nil {
		*target = *value
	}
}

// rename applies the library-wide renames to one name.
func rename(name string, rules Rules) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if renamed, ok := rules.Rename[name]; ok {
		return strings.TrimSpace(renamed)
	}
	return name
}

func formatNumber(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// summarise shortens lyrics to something that fits on a line of the preview.
func summarise(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	line, _, _ := strings.Cut(text, "\n")
	if runes := []rune(line); len(runes) > 48 {
		line = string(runes[:48]) + "…"
	}
	return line
}

// Summarize counts the changes by kind.
func Summarize(changes []Change) Summary {
	summary := Summary{Files: len(changes)}
	for _, change := range changes {
		if change.edit.Artist != nil {
			summary.ArtistRenamed++
		}
		if change.edit.Genre != nil {
			summary.GenreSet++
		}
		if change.edit.Album != nil {
			summary.AlbumRenamed++
		}
		if change.edit.AlbumArtist != nil {
			if *change.edit.AlbumArtist == "" {
				summary.AlbumArtistClear++
			} else {
				summary.AlbumArtistSet++
			}
		}
		if change.edit.RemoveCompilation {
			summary.CompilationRemove++
		}
	}
	return summary
}

// Result reports one applied change.
type Result struct {
	Path  string `json:"path"`
	Error string `json:"error,omitempty"`
}

// Apply writes the planned edits, reporting each file as it goes. It stops at
// the first cancellation but never rolls back what already succeeded — each
// file is written atomically on its own.
func Apply(ctx context.Context, changes []Change, report func(int, Result)) error {
	for i, change := range changes {
		if err := ctx.Err(); err != nil {
			return err
		}

		result := Result{Path: change.Path}
		if err := tags.Write(change.Path, change.edit); err != nil {
			result.Error = err.Error()
		}
		if report != nil {
			report(i, result)
		}
	}
	return nil
}

// megabytes is how a tag's size is written in the list of changes.
func megabytes(bytes int) string {
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
}

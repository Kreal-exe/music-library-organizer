package library

// Working out what the album list should be.
//
// A player shows one album per distinct album name, so the same record ends up
// in several places: "Wish You Were Here", "wish you were here" and "Wish You
// Were Here (Remastered 2011)" are three albums, and so is a download whose
// album field carries the name of whoever shared it. This file folds those
// back into one album per record, the way artists.go does for names.
//
// Unlike an artist name, an album name is not unique on its own — two artists
// can both have a "Greatest Hits" — so an album is always an album by
// somebody, and names are only ever folded together within one artist.

import (
	"regexp"
	"sort"
	"strings"

	"musiclibraryorganizer/internal/tags"
)

// Why an album name folds into an album. KindExact and KindSpelling are shared
// with the artists.
const (
	KindEdition   = "edition"   // A "(Deluxe)", "[Explicit]" or "- Single" was dropped.
	KindWatermark = "watermark" // Whoever shared the file was dropped.
	KindDisc      = "disc"      // One disc of the album, filed as an album of its own.
	KindOrder     = "order"     // The same words in another order.
)

// Album is one record the library should end up with.
type Album struct {
	Name string `json:"name"`
	// Owner identifies the artist the album is by, as AlbumOwner works it out;
	// Artist is how that artist is written.
	Owner  string `json:"owner"`
	Artist string `json:"artist"`
	Tracks int    `json:"tracks"`

	// Sources are the album names in the files that fold into this one, the
	// busiest first.
	Sources []AlbumSource `json:"sources"`
}

// AlbumSource is one album name as it appears in the files.
type AlbumSource struct {
	Value  string `json:"value"`
	Tracks int    `json:"tracks"`
	Kind   string `json:"kind"`
	// Paths are the tracks carrying this name, so the interface can list them
	// without working out who each album is by all over again.
	Paths []string `json:"paths"`
}

// VariousArtists is the owner of a compilation, whose tracks are by everybody
// and belong together all the same.
const VariousArtists = "\x00various"

// AlbumOwner says which artist a track's album belongs to: the album artist
// when there is one, since that is the field meant for it, and the track's
// artist otherwise. Guests are dropped and the spelling folded, so a "feat."
// on one track does not split its album off from the rest.
func AlbumOwner(track tags.Track) string {
	if track.Compilation {
		return VariousArtists
	}
	name := strings.TrimSpace(track.AlbumArtist)
	if name == "" {
		name = strings.TrimSpace(track.Artist)
	}
	lead, _, _ := leadOf(name)
	return spellingKey(lead)
}

// summarizeAlbums builds the album view of the library.
func summarizeAlbums(tracks []tags.Track) (albums []Album, raw int) {
	type source struct {
		value  string
		owner  string
		tracks int
		paths  []string
		// names counts how the owner is written on these tracks, so the album
		// can say who it is by.
		names map[string]int
	}

	sources := map[[2]string]*source{}
	for _, track := range tracks {
		value := strings.TrimSpace(track.Album)
		if value == "" {
			continue
		}
		owner := AlbumOwner(track)
		id := [2]string{owner, value}

		s := sources[id]
		if s == nil {
			s = &source{value: value, owner: owner, names: map[string]int{}}
			sources[id] = s
		}
		s.tracks++
		s.paths = append(s.paths, track.Path)

		name := strings.TrimSpace(track.AlbumArtist)
		if name == "" {
			name = strings.TrimSpace(track.Artist)
		}
		if owner == VariousArtists {
			name = "Various artists"
		} else {
			name, _, _ = leadOf(name)
		}
		s.names[name]++
	}

	// A track flagged as part of a compilation is filed under nobody in
	// particular, which splits it off from the rest of its album whenever only
	// some of the tracks carry the flag. When somebody does have an album of
	// that name, the flagged tracks go with it.
	named := map[string]*source{}
	for _, s := range sources {
		if s.owner == VariousArtists {
			continue
		}
		key := albumKey(s.value)
		if best := named[key]; best == nil || s.tracks > best.tracks {
			named[key] = s
		}
	}
	for id, s := range sources {
		best := named[albumKey(s.value)]
		if s.owner != VariousArtists || best == nil {
			continue
		}
		delete(sources, id)
		into := sources[[2]string{best.owner, s.value}]
		if into == nil {
			s.owner = best.owner
			sources[[2]string{best.owner, s.value}] = s
			continue
		}
		into.tracks += s.tracks
		into.paths = append(into.paths, s.paths...)
		for name, count := range s.names {
			into.names[name] += count
		}
	}

	type bucket struct {
		owner   string
		sources []*source
	}
	buckets := map[[2]string]*bucket{}
	for _, s := range sources {
		id := [2]string{s.owner, albumKey(s.value)}
		b := buckets[id]
		if b == nil {
			b = &bucket{owner: s.owner}
			buckets[id] = b
		}
		b.sources = append(b.sources, s)
	}

	albums = make([]Album, 0, len(buckets))
	for _, b := range buckets {
		var candidates, artists []Name
		album := Album{Owner: b.owner}

		for _, s := range b.sources {
			// A disc number is never part of the album's name, so the name is
			// chosen from what is left without it.
			candidates = append(candidates, Name{Value: stripDisc(s.value), Tracks: s.tracks})
			for name, count := range s.names {
				artists = append(artists, Name{Value: name, Tracks: count})
			}
			album.Tracks += s.tracks
		}
		album.Name = bestAlbumName(candidates)
		album.Artist = bestName(artists)

		for _, s := range b.sources {
			album.Sources = append(album.Sources, AlbumSource{
				Value:  s.value,
				Tracks: s.tracks,
				Kind:   albumKind(s.value, album.Name),
				Paths:  s.paths,
			})
		}
		sort.Slice(album.Sources, func(i, j int) bool {
			if album.Sources[i].Tracks != album.Sources[j].Tracks {
				return album.Sources[i].Tracks > album.Sources[j].Tracks
			}
			return album.Sources[i].Value < album.Sources[j].Value
		})

		albums = append(albums, album)
	}

	sort.Slice(albums, func(i, j int) bool {
		if albums[i].Tracks != albums[j].Tracks {
			return albums[i].Tracks > albums[j].Tracks
		}
		if a, b := strings.ToLower(albums[i].Name), strings.ToLower(albums[j].Name); a != b {
			return a < b
		}
		return albums[i].Owner < albums[j].Owner
	})
	return albums, len(sources)
}

// bestAlbumName picks the name to keep for a record. The most used wins, as it
// does for artists; among equals, the plain name beats one carrying an edition
// or a watermark, since that is what the record is called.
func bestAlbumName(candidates []Name) string {
	plain := func(value string) bool { return stripAlbum(value) == value }

	var best Name
	for _, candidate := range candidates {
		switch {
		case best.Value == "", candidate.Tracks > best.Tracks:
			best = candidate
		case candidate.Tracks < best.Tracks:
		case plain(candidate.Value) != plain(best.Value):
			if plain(candidate.Value) {
				best = candidate
			}
		case betterSpelling(candidate, best):
			best = candidate
		}
	}
	return best.Value
}

// albumKind says how a name in the files relates to the name kept.
func albumKind(value, kept string) string {
	switch {
	case value == kept:
		return KindExact
	case spellingKey(value) == spellingKey(kept):
		return KindSpelling
	case DiscOf(value) > 0 && stripDisc(value) != value:
		return KindDisc
	case unwatermark(value) != value && spellingKey(unwatermark(value)) == spellingKey(unwatermark(kept)):
		return KindWatermark
	case stripAlbum(value) == value && stripAlbum(kept) == kept:
		return KindOrder
	default:
		return KindEdition
	}
}

/* Folding ------------------------------------------------------------------ */

// albumKey folds an album name down to what every edition and spelling of the
// record share. The words are taken in any order, since a release is as often
// filed "Моя кассета - твой первый диск" as "Твой первый диск - моя кассета".
func albumKey(value string) string {
	var words []string
	for _, word := range editionSplit.Split(strings.ToLower(stripAlbum(value)), -1) {
		if word != "" {
			words = append(words, word)
		}
	}
	if len(words) == 0 {
		// A name made only of symbols is still a name; keep it apart from the
		// other names made only of symbols.
		return strings.ToLower(strings.TrimSpace(value))
	}
	sort.Strings(words)
	return strings.Join(words, " ")
}

// A disc of an album, as a name carries it: "(CD1)", "[Disc 2]", "- CD 2".
var discQualifier = regexp.MustCompile(`(?i)\s*(?:[\(\[\{]\s*(?:cd|disc|disk|диск)\s*(\d+)\s*[\)\]\}]|[-–—]\s*(?:cd|disc|disk|диск)\s*(\d+))\s*$`)

// DiscOf reads the disc number an album name carries, or 0.
func DiscOf(value string) int {
	m := discQualifier.FindStringSubmatch(strings.TrimSpace(value))
	if m == nil {
		return 0
	}
	digits := m[1] + m[2]
	n := 0
	for _, r := range digits {
		n = n*10 + int(r-'0')
	}
	return n
}

// stripDisc removes the disc a name carries.
func stripDisc(value string) string {
	value = strings.TrimSpace(value)
	if loc := discQualifier.FindStringIndex(value); loc != nil && loc[0] > 0 {
		return strings.TrimSpace(value[:loc[0]])
	}
	return value
}

// stripAlbum removes what a release or a re-uploader adds to an album's name:
// a watermark, then edition qualifiers from the end, as many as there are —
// "Album (Deluxe Edition) [Explicit]" is still "Album".
func stripAlbum(value string) string {
	value = stripDisc(unwatermark(strings.TrimSpace(value)))

	for {
		trimmed := strings.TrimSpace(value)
		if m := trailingBracket.FindStringSubmatchIndex(trimmed); m != nil && m[0] > 0 {
			if isEdition(trimmed[m[2]:m[3]]) {
				value = trimmed[:m[0]]
				continue
			}
		}
		if m := trailingDash.FindStringSubmatchIndex(trimmed); m != nil && m[0] > 0 {
			if isEdition(trimmed[m[2]:m[3]]) {
				value = trimmed[:m[0]]
				continue
			}
		}
		return trimmed
	}
}

var (
	trailingBracket = regexp.MustCompile(`\s*[\(\[\{]([^\(\)\[\]\{\}]*)[\)\]\}]\s*$`)
	trailingDash    = regexp.MustCompile(`\s+[-–—]\s+([^-–—]+)$`)
)

// unwatermark drops the shared-by credit a download carries in its album
// field: "Album | @channel", "Album (t.me/channel)".
func unwatermark(value string) string {
	parts := strings.Split(value, "|")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" && !watermark.MatchString(part) {
			kept = append(kept, part)
		}
	}
	if len(kept) == 0 {
		return strings.TrimSpace(value)
	}
	value = strings.Join(kept, " | ")

	if m := trailingBracket.FindStringSubmatchIndex(value); m != nil && m[0] > 0 {
		if watermark.MatchString(strings.TrimSpace(value[m[2]:m[3]])) {
			return strings.TrimSpace(value[:m[0]])
		}
	}
	return value
}

// editionWords are what a qualifier naming an edition of a record is made of.
// A bracket counts as an edition only when every word in it is one of these,
// so "(Deluxe Edition)" and "[2011 Remaster]" go while "(Live at Wembley)" and
// "(Instrumentals)" — different records — stay.
var editionWords = map[string]bool{
	"deluxe": true, "edition": true, "version": true, "explicit": true, "clean": true,
	"remaster": true, "remastered": true, "remasters": true, "bonus": true, "track": true,
	"tracks": true, "expanded": true, "anniversary": true, "special": true, "single": true,
	"ep": true, "lp": true, "album": true, "reissue": true, "digital": true, "standard": true,
	"extended": true, "super": true, "limited": true, "collector": true, "collectors": true,
	"international": true, "mono": true, "stereo": true, "hd": true, "hq": true, "cd": true,
	"web": true, "mp3": true, "flac": true, "kbps": true, "hi": true, "res": true,
	"the": true, "and": true, "with": true, "of": true, "s": true,
	"us": true, "uk": true, "eu": true, "jp": true, "japan": true, "japanese": true,
	"делюкс": true, "издание": true, "версия": true, "сингл": true, "альбом": true,
	"переиздание": true, "ремастер": true, "бонус": true, "трек": true, "треки": true,
}

var (
	editionSplit  = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	editionNumber = regexp.MustCompile(`^\d+(st|nd|rd|th)?$`)
)

func isEdition(qualifier string) bool {
	words := editionSplit.Split(strings.ToLower(qualifier), -1)
	seen := false
	for _, word := range words {
		if word == "" {
			continue
		}
		if !editionWords[word] && !editionNumber.MatchString(word) {
			return false
		}
		seen = true
	}
	return seen
}

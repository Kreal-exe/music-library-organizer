package library

// Working out what the artist list should be.
//
// A player shows one row per distinct artist string, so a collection ends up
// with far more artists than it has: the same name spelled three ways, and a
// separate artist for every guest credit. What the owner wants is the short
// list — the artists the music is actually by — so that is what this file
// computes: one Target per real artist, carrying every raw name that folds
// into it, and why.

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"musiclibraryorganizer/internal/tags"
)

// Why a raw name folds into a target.
const (
	KindExact     = "exact"     // Already the target name.
	KindSpelling  = "spelling"  // Same name, written differently.
	KindFeature   = "feature"   // An explicit "feat." credit was dropped.
	KindSeparator = "separator" // Split on "&", ",", ";" or "/".
)

// Target is one artist the library should end up with.
type Target struct {
	Name   string `json:"name"`
	Tracks int    `json:"tracks"`
	Albums int    `json:"albums"`

	// Sources are the names found in the files that fold into this one, the
	// busiest first. A target with a single exact source needs no attention.
	Sources []Source `json:"sources"`
}

// Source is one artist string as it appears in the files.
type Source struct {
	Value  string   `json:"value"`
	Tracks int      `json:"tracks"`
	Kind   string   `json:"kind"`
	Guests []string `json:"guests,omitempty"`
}

// Name is one distinct artist string with its counts, used for the album
// artist side of the library.
type Name struct {
	Value  string `json:"value"`
	Tracks int    `json:"tracks"`
	Albums int    `json:"albums"`
}

// Summary is what the interface renders after a scan.
type Summary struct {
	// Targets is the artist list the library reduces to.
	Targets []Target `json:"targets"`
	// RawArtists is how many distinct names the files actually carry, which is
	// what a player would show today.
	RawArtists int `json:"rawArtists"`

	AlbumArtists []Name `json:"albumArtists"`

	TrackCount       int `json:"trackCount"`
	AlbumArtistCount int `json:"albumArtistCount"`
	CompilationCount int `json:"compilationCount"`
	SortCount        int `json:"sortCount"`
	LegacyCount      int `json:"legacyCount"`
	OversizedCount   int `json:"oversizedCount"`
	WithLyrics       int `json:"withLyrics"`
}

// Summarize builds the artist view of a scanned library.
func (l *Library) Summarize() Summary {
	artists := newCounter()
	albumArtists := newCounter()

	summary := Summary{TrackCount: len(l.Tracks), Targets: []Target{}, AlbumArtists: []Name{}}

	for _, track := range l.Tracks {
		if track.AlbumArtist != "" {
			summary.AlbumArtistCount++
		}
		if track.Compilation {
			summary.CompilationCount++
		}
		if track.HasSort {
			summary.SortCount++
		}
		if track.LegacyTag {
			summary.LegacyCount++
		}
		if track.TagBytes > tags.ScannerTagLimit {
			summary.OversizedCount++
		}
		if track.HasLyrics {
			summary.WithLyrics++
		}

		artist := strings.TrimSpace(track.Artist)
		albumArtist := strings.TrimSpace(track.AlbumArtist)

		if artist != "" {
			artists.add(artist, track.Album)
		}
		if albumArtist != "" {
			albumArtists.add(albumArtist, track.Album)
			// Album artists join the same reduction, because a guest credit or
			// a misspelling there produces an extra artist just the same. When
			// both fields hold the same name the track is still only one
			// track, so it is not counted twice.
			if albumArtist != artist {
				artists.add(albumArtist, track.Album)
			}
		}
	}

	raw := artists.names()
	summary.RawArtists = len(raw)
	summary.Targets = reduce(raw, artists)
	summary.AlbumArtists = albumArtists.names()

	return summary
}

// reduce folds the raw names into one target per real artist.
func reduce(raw []Name, counts *counter) []Target {
	type bucket struct {
		primaries []Name // The lead artist of each raw name, for naming.
		sources   []Source
		tracks    int
		albums    map[string]bool
	}

	buckets := map[string]*bucket{}

	for _, name := range raw {
		lead, kind, guests := leadOf(name.Value)
		key := spellingKey(lead)

		b := buckets[key]
		if b == nil {
			b = &bucket{albums: map[string]bool{}}
			buckets[key] = b
		}

		b.primaries = append(b.primaries, Name{Value: lead, Tracks: name.Tracks})
		b.sources = append(b.sources, Source{
			Value:  name.Value,
			Tracks: name.Tracks,
			Kind:   kind,
			Guests: guests,
		})
		b.tracks += name.Tracks
		for album := range counts.albums[name.Value] {
			b.albums[album] = true
		}
	}

	targets := make([]Target, 0, len(buckets))
	for _, b := range buckets {
		target := Target{
			Name:   bestName(b.primaries),
			Tracks: b.tracks,
			Albums: len(b.albums),
		}

		// Why a source folds in can only be settled once the target name is
		// known: a name with nothing split off is either the target itself or
		// another way of spelling it.
		for _, source := range b.sources {
			switch {
			case source.Value == target.Name:
				source.Kind = KindExact
				source.Guests = nil
			case source.Kind == KindExact:
				source.Kind = KindSpelling
			}
			target.Sources = append(target.Sources, source)
		}
		sort.SliceStable(target.Sources, func(i, j int) bool {
			return target.Sources[i].Tracks > target.Sources[j].Tracks
		})

		targets = append(targets, target)
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Tracks != targets[j].Tracks {
			return targets[i].Tracks > targets[j].Tracks
		}
		return strings.ToLower(targets[i].Name) < strings.ToLower(targets[j].Name)
	})
	return targets
}

// leadOf reduces a credit to the artist it is really by, and says how.
func leadOf(value string) (lead, kind string, guests []string) {
	split, ok := SplitArtists(value)
	if !ok {
		return value, KindExact, nil
	}
	if split.Certain {
		return split.Primary, KindFeature, split.Rest
	}
	return split.Primary, KindSeparator, split.Rest
}

// bestName picks the spelling to keep for a group: the most used, and among
// equals the one written the way a name normally is.
func bestName(candidates []Name) string {
	totals := map[string]int{}
	for _, candidate := range candidates {
		totals[candidate.Value] += candidate.Tracks
	}

	var best Name
	for value, tracks := range totals {
		if betterSpelling(Name{Value: value, Tracks: tracks}, best) {
			best = Name{Value: value, Tracks: tracks}
		}
	}
	return best.Value
}

/* Counting ---------------------------------------------------------------- */

type counter struct {
	tracks map[string]int
	albums map[string]map[string]bool
}

func newCounter() *counter {
	return &counter{
		tracks: map[string]int{},
		albums: map[string]map[string]bool{},
	}
}

func (c *counter) add(name, album string) {
	c.tracks[name]++
	if album != "" {
		if c.albums[name] == nil {
			c.albums[name] = map[string]bool{}
		}
		c.albums[name][album] = true
	}
}

func (c *counter) names() []Name {
	out := make([]Name, 0, len(c.tracks))
	for value, count := range c.tracks {
		out = append(out, Name{Value: value, Tracks: count, Albums: len(c.albums[value])})
	}

	// Busiest first, then alphabetically. Two spellings of one name fold to the
	// same lower-case string, so the raw value breaks the last tie and keeps
	// the order the same from run to run.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tracks != out[j].Tracks {
			return out[i].Tracks > out[j].Tracks
		}
		if folded := strings.ToLower(out[i].Value); folded != strings.ToLower(out[j].Value) {
			return folded < strings.ToLower(out[j].Value)
		}
		return out[i].Value < out[j].Value
	})
	return out
}

/* Naming ------------------------------------------------------------------ */

// betterSpelling decides which of two spellings to keep. The most used wins;
// when a collection uses each of them once — which is common — the one written
// the way a name normally is wins, and the raw value settles anything still
// tied so the choice never changes between runs.
func betterSpelling(candidate, best Name) bool {
	if best.Value == "" {
		return true
	}
	if candidate.Tracks != best.Tracks {
		return candidate.Tracks > best.Tracks
	}
	if a, b := spellingQuality(candidate.Value), spellingQuality(best.Value); a != b {
		return a > b
	}
	return candidate.Value < best.Value
}

// spellingQuality rates how a name is written: capitalised but not shouted in
// full capitals reads as the intended form, and all lower case as the least
// deliberate.
func spellingQuality(value string) int {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) == 0 {
		return 0
	}

	var hasUpper, hasLower bool
	for _, r := range runes {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		}
	}

	switch {
	case unicode.IsUpper(runes[0]) && hasLower:
		return 3 // "Juice WRLD", "Kino"
	case unicode.IsUpper(runes[0]):
		return 2 // "JUICE WRLD"
	case hasUpper:
		return 1 // "juice WRLD"
	default:
		return 0 // "juice wrld"
	}
}

// spellingKey folds a name down to what two spellings of the same artist share:
// case, a leading article, and punctuation all go.
var notLetterOrDigit = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func spellingKey(value string) string {
	key := strings.ToLower(strings.TrimSpace(value))
	key = strings.TrimPrefix(key, "the ")
	return notLetterOrDigit.ReplaceAllString(key, "")
}

/* Splitting --------------------------------------------------------------- */

// Split describes a credit that names more than one artist.
type Split struct {
	Primary string   `json:"primary"`
	Rest    []string `json:"rest"`
	// Certain marks a split driven by an explicit "feat."-style marker rather
	// than a bare separator, which can also occur inside a real band name.
	Certain bool `json:"certain"`
}

// An explicit guest marker, optionally wrapped in brackets: these are credits,
// not part of the artist's name.
var guestMarker = regexp.MustCompile(`(?i)\s*[\(\[]?\s*\b(?:feat|ft|featuring|with|w/|prod\. by|vs|versus)\b\.?\s+`)

// Bare separators between several artists. A name may legitimately contain any
// of them — "Earth, Wind & Fire", "AC/DC" — so a split on one of these is only
// ever a suggestion. The pipe is the exception: nothing is called that, and it
// is what a re-uploader puts before their own name.
var bareSeparator = regexp.MustCompile(`\s*(?:\||;|\s/\s|\s[xX]\s|\s&\s|,\s)\s*`)

// Whoever shared the file, written into the artist field beside the artist:
// "Juice WRLD | @uploads_channel", "Кино | t.me/rock". A player files each of
// these as an artist of its own, which is exactly the mess this tool exists to
// clear up, and none of them is anybody's name.
var watermark = regexp.MustCompile(`(?i)^(?:@|(?:https?://)?(?:www\.)?(?:t\.me|vk\.com|telegram\.me|youtu)\b)`)

// dropWatermarks removes the shared-by credits from a split, unless that is
// all there was — a name we do not understand is better than no name.
func dropWatermarks(parts []string) []string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if !watermark.MatchString(strings.TrimSpace(part)) {
			kept = append(kept, part)
		}
	}
	if len(kept) == 0 {
		return parts
	}
	return kept
}

// SplitArtists separates a combined credit into its lead artist and the rest.
func SplitArtists(value string) (Split, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Split{}, false
	}

	if loc := guestMarker.FindStringIndex(value); loc != nil && loc[0] > 0 {
		primary := strings.TrimSpace(value[:loc[0]])
		rest := splitRest(value[loc[1]:])
		if primary != "" {
			return Split{Primary: primary, Rest: rest, Certain: true}, true
		}
	}

	if parts := bareSeparator.Split(value, -1); len(parts) > 1 {
		cleaned := make([]string, 0, len(parts))
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				cleaned = append(cleaned, part)
			}
		}
		cleaned = dropWatermarks(cleaned)

		if len(cleaned) > 1 {
			return Split{Primary: cleaned[0], Rest: cleaned[1:]}, true
		}
		// Everything beside the artist was a shared-by credit, so the artist
		// is what the name really was.
		if len(cleaned) == 1 && cleaned[0] != value {
			return Split{Primary: cleaned[0], Certain: true}, true
		}
	}

	return Split{}, false
}

// splitRest tidies the trailing part of a credit, which often carries the
// closing bracket of a "(feat. …)" and further separators.
func splitRest(rest string) []string {
	rest = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rest), ")]"))

	var out []string
	for _, part := range bareSeparator.Split(rest, -1) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

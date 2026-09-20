package lyrics

// Deciding whether a search result really is the track in hand. Filenames and
// tags in a real collection carry a lot of noise — "(Official Audio)",
// "[Prod. Nick Mira]", "feat." credits — that a lyrics database does not, so
// both sides are cleaned before they are compared.

import (
	"regexp"
	"strings"
	"time"
)

// Bracketed asides that describe the recording rather than name the song.
var noiseChunk = regexp.MustCompile(`(?i)[\(\[]\s*(?:feat|ft|featuring|with|prod|produced)\b[^\)\]]*[\)\]]|` +
	`[\(\[]\s*(?:official\b[^\)\]]*|lyrics?|audio|visuali[sz]er|music video|video|hq|hd|explicit|clean|dirty|cdq|full|snippet|leak(?:ed)?|unreleased|og|master(?:ed)?|remaster(?:ed)?[^\)\]]*)\s*[\)\]]`)

// Trailing "- Remastered 2011" style qualifiers.
var noiseSuffix = regexp.MustCompile(`(?i)\s+-\s+(?:remaster(?:ed)?|single version|album version|radio edit|bonus track|deluxe)\b.*$`)

// A leading track number, as ripped filenames often carry.
var leadingNumber = regexp.MustCompile(`^\s*\d{1,3}\s*[-.)]\s+`)

var punctuation = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// cleanTitle strips the decoration around a song title.
func cleanTitle(title string) string {
	title = strings.TrimSpace(title)
	title = leadingNumber.ReplaceAllString(title, "")
	title = noiseChunk.ReplaceAllString(title, " ")
	title = noiseSuffix.ReplaceAllString(title, "")

	// A trailing "feat. …" with no brackets around it.
	if loc := guestSuffix.FindStringIndex(title); loc != nil && loc[0] > 0 {
		title = title[:loc[0]]
	}
	return strings.TrimSpace(strings.Join(strings.Fields(title), " "))
}

// A guest credit, in either alphabet: "feat.", "ft", and the "п.у." a Russian
// release writes for the same thing.
//
// The Russian spellings are kept out of the \b guard, because a word boundary
// here is an ASCII one: it never matches beside a Cyrillic letter, and the
// credit would stay in the name that goes to the databases.
var guestSuffix = regexp.MustCompile(`(?i)\s+(?:\b(?:feat|ft|featuring|w/)\b\.?|п\.\s?у\.?|при участии|совместно с)\s+`)

// The same credit as a title carries it, where a bracket usually holds it:
// "Boomerang (feat. Lil Yachty)".
var guestCredit = regexp.MustCompile(`(?i)[\(\[]?\s*(?:\b(?:feat|ft|featuring|with)\b\.?|п\.\s?у\.?|при участии)\s+`)

// Anything left inside brackets, and the markers an unreleased recording
// carries at the end of its name.
var (
	bracketedAside = regexp.MustCompile(`[\(\[][^\)\]]*[\)\]]`)
	versionSuffix  = regexp.MustCompile(`(?i)\s+(?:v\.?\d+|og|stem|stems|solo|snippet|master|cdq)$`)
)

// bareTitle is the song's name with every qualifier taken off, including the
// ones cleanTitle leaves alone because they can belong to a title.
//
// A collection of unreleased music is full of them — "Deprived (Session Edit)",
// "Devil Horns (v1)" — and a database that has the song files it under the
// plain name, so searching for the decorated one finds nothing at all.
func bareTitle(title string) string {
	bare := bracketedAside.ReplaceAllString(cleanTitle(title), " ")
	bare = versionSuffix.ReplaceAllString(bare, "")
	bare = strings.TrimSpace(strings.Join(strings.Fields(bare), " "))
	if bare == "" {
		return cleanTitle(title) // The whole title was inside brackets.
	}
	return bare
}

// Names that are not artists. A downloader writes where the file came from
// into the artist field, and a compilation with nobody credited gets a
// stand-in; either way the real name is in the album artist instead.
var placeholderNames = map[string]bool{
	"vk": true, "vkcom": true, "vkmusic": true, "vkmp3": true, "vkmusicru": true,
	"unknown": true, "unknownartist": true, "noartist": true, "none": true,
	"various": true, "variousartists": true, "va": true,
	"youtube": true, "soundcloud": true, "spotify": true, "telegram": true,
	"mp3": true, "audio": true, "music": true,
	"музыка": true, "сборник": true, "неизвестен": true, "неизвестный": true,
	"неизвестныйисполнитель": true,
}

func placeholderArtist(name string) bool { return placeholderNames[fold(name)] }

// leadArtist drops the guests, since databases file a song under its lead.
func leadArtist(artist string) string {
	artist = dropWatermark(artist)

	if loc := guestSuffix.FindStringIndex(artist); loc != nil && loc[0] > 0 {
		return strings.TrimSpace(artist[:loc[0]])
	}
	for _, sep := range []string{"|", ";", " & ", " / ", " x ", ", "} {
		if before, _, found := strings.Cut(artist, sep); found {
			if before = strings.TrimSpace(before); before != "" {
				return before
			}
		}
	}
	return strings.TrimSpace(artist)
}

// Whoever shared the file, written into the artist field beside the artist.
var handle = regexp.MustCompile(`(?i)\s*[|,;]?\s*(?:@\S+|(?:https?://)?(?:www\.)?(?:t\.me|vk\.com|telegram\.me)/\S*)\s*`)

// dropWatermark takes the re-uploader's name back out of a credit, so that
// "Juice WRLD | @uploads_channel" is searched for as Juice WRLD.
func dropWatermark(artist string) string {
	// Taking the handle out can leave the separator that held it in place.
	stripped := strings.Trim(handle.ReplaceAllString(artist, " "), " |,;-")
	if stripped == "" {
		return strings.TrimSpace(artist) // It was nothing but a handle.
	}
	return strings.Join(strings.Fields(stripped), " ")
}

func fold(s string) string {
	return punctuation.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "")
}

// score rates how well a candidate matches the query, from 0 to 1.
func score(q Query, candidate Match) float64 {
	title := similarity(cleanTitle(q.Title), cleanTitle(candidate.Title))

	// "Deprived (Session Edit)" and "Deprived (Studio Session)" are the same
	// song recorded twice, so the names are compared without their qualifiers
	// as well — a shade below the full comparison, so a candidate whose name
	// really is the same still wins.
	if bare := 0.95 * similarity(bareTitle(q.Title), bareTitle(candidate.Title)); bare > title {
		title = bare
	}

	// The tag may credit guests the database leaves out, or the other way
	// round, so the lead artist is compared too and the better reading counts.
	artist := similarity(q.Artist, candidate.Artist)
	if lead := similarity(leadArtist(q.Artist), leadArtist(candidate.Artist)); lead > artist {
		artist = lead
	}

	// Neither name has to be written the same way on both sides. A file called
	// "ZillaKami x SosMula - Bukkake" and a database entry for "City Morgue
	// (Ft. ZillaKami)" share the names that matter and nothing else, and
	// letter-by-letter they look unrelated, so the words they have in common
	// count too — a little below a real match, since sharing some words is
	// weaker evidence than being the same name.
	if shared := 0.9 * wordOverlap(q.Artist, candidate.Artist); shared > artist {
		artist = shared
	}

	// A collaboration is filed under whoever the database considers the lead,
	// which is often the guest named in our title: "Act Right (feat. GDo)" is
	// GDo's song there. The guest is on the track either way, so a candidate
	// credited to them is credited to somebody who was in the room.
	for _, guest := range q.Guests {
		named := 0.95 * similarity(guest, candidate.Artist)
		if shared := 0.9 * wordOverlap(guest, candidate.Artist); shared > named {
			named = shared
		}
		if named > artist {
			artist = named
		}
	}
	if shared := 0.9 * wordOverlap(q.Title, candidate.Title); shared > title {
		title = shared
	}

	total := 0.6*title + 0.4*artist

	// Two songs with the same name by the same artist are told apart by how
	// long they run.
	if q.Duration > 0 && candidate.Duration > 0 {
		gap := q.Duration - candidate.Duration
		if gap < 0 {
			gap = -gap
		}
		if gap <= 3*time.Second {
			total += 0.06
		} else if gap > 20*time.Second {
			total -= 0.15
		}
	}

	return clamp(total)
}

// withoutLeadingArtist drops the "Artist - " that a title repeats.
//
// A file named "Artist - Title.mp3" is often tagged with the whole of that as
// its title, and searching for the artist twice finds nothing at all.
func withoutLeadingArtist(title string, artists []string) string {
	for _, artist := range artists {
		if artist == "" {
			continue
		}
		for _, separator := range []string{" - ", " — ", " – ", " -- "} {
			prefix := artist + separator
			if len(title) > len(prefix) && strings.EqualFold(title[:len(prefix)], prefix) {
				return strings.TrimSpace(title[len(prefix):])
			}
		}
	}
	return title
}

// guestsIn reads the names credited after a "feat." in a title.
func guestsIn(title string) []string {
	loc := guestCredit.FindStringIndex(title)
	if loc == nil {
		return nil
	}

	// The credit runs to the end of the title, or to the bracket that closed
	// around it.
	tail := title[loc[1]:]
	if end := strings.IndexAny(tail, ")]"); end >= 0 {
		tail = tail[:end]
	}

	var guests []string
	for _, name := range guestSeparator.Split(tail, -1) {
		if name = strings.TrimSpace(name); len(name) > 2 {
			guests = append(guests, name)
		}
	}
	return guests
}

var guestSeparator = regexp.MustCompile(`\s*(?:&|,|;|\+|\band\b|\bи\b)\s*`)

// wordOverlap is the share of the shorter side's words that also appear on the
// other, so a name that carries extra words is still recognised by the ones it
// has. Short words are ignored: "the" and "x" are in half the names there are.
func wordOverlap(a, b string) float64 {
	left, right := words(a), words(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	if len(left) > len(right) {
		left, right = right, left
	}

	have := make(map[string]bool, len(right))
	for _, word := range right {
		have[word] = true
	}

	shared := 0
	for _, word := range left {
		if have[word] {
			shared++
		}
	}
	return float64(shared) / float64(len(left))
}

func words(s string) []string {
	var out []string
	for _, field := range punctuation.Split(strings.ToLower(strings.TrimSpace(s)), -1) {
		if len([]rune(field)) > 2 {
			out = append(out, field)
		}
	}
	return out
}

func clamp(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}

// similarity compares two strings once their case and punctuation are folded
// away, scoring an exact match highest and edit distance otherwise.
func similarity(a, b string) float64 {
	a, b = fold(a), fold(b)
	switch {
	case a == "" || b == "":
		return 0
	case a == b:
		return 1
	case strings.Contains(a, b) || strings.Contains(b, a):
		return 0.9
	}

	longest := max(len(a), len(b))
	return clamp(1 - float64(levenshtein(a, b))/float64(longest))
}

// levenshtein is the usual two-row edit distance, over bytes: both inputs are
// already folded, so the difference between bytes and runes does not change
// which candidate wins.
func levenshtein(a, b string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}

	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(current[j-1]+1, previous[j]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}

var blankRun = regexp.MustCompile(`\n{3,}`)

// tidyLyrics normalizes line endings and rejects the placeholders databases
// return in place of real words.
func tidyLyrics(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	text = blankRun.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	text = strings.TrimSpace(text)

	switch strings.ToLower(text) {
	case "", "[instrumental]", "instrumental", "(instrumental)":
		return ""
	}
	// Anything this short is a stub rather than a song.
	if len([]rune(text)) < 25 {
		return ""
	}
	return text
}

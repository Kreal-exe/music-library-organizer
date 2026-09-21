package lyrics

// A pasted link is read here. The user has already decided which song the page
// is for, so any lyrics site will do, not just the ones the search asks.
//
// Genius, AZLyrics and Amalgama lay their words out in ways that need a reader
// of their own. Every other site gets the same two readings, and the fuller one
// wins: the words a page declares for search engines, which is all Musixmatch
// has, and the longest run of lines broken by <br>, which is how Letras,
// SongLyrics and most small sites print a song. The result is shown before it
// is used, so a page this reads wrongly costs nothing.
//
// Lyrics.com fills its page in with a script, so there is nothing to read.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html/charset"
)

type pageReader struct {
	client  *http.Client
	limiter *limiter
}

// site is a host whose name is worth showing and, for some, whose layout needs
// its own reader.
type site struct {
	name string
	read func(page string) string
}

var sites = map[string]site{
	"genius.com":          {"Genius", extractGeniusLyrics},
	"amalgama-lab.com":    {"Amalgama", extractAmalgama},
	"azlyrics.com":        {"AZLyrics", extractAZLyrics},
	"musixmatch.com":      {name: "Musixmatch"},
	"letras.mus.br":       {name: "Letras"},
	"letras.com":          {name: "Letras"},
	"songlyrics.com":      {name: "SongLyrics"},
	"lyricstranslate.com": {name: "LyricsTranslate"},
}

// siteFor names the site behind a host, and says how to read its pages.
func siteFor(host string) site {
	host = strings.ToLower(host)
	for _, prefix := range []string{"www.", "m.", "ru.", "en."} {
		host = strings.TrimPrefix(host, prefix)
	}
	known, ok := sites[host]
	if !ok {
		known.name = host
	}
	if known.read == nil {
		known.read = extractAnyLyrics
	}
	return known
}

// read fetches one page and picks the words out of it.
func (r *pageReader) read(ctx context.Context, address *url.URL) (source, text string, err error) {
	site := siteFor(address.Host)

	if err := r.limiter.wait(ctx); err != nil {
		return site.name, "", err
	}
	resp, err := fetch(ctx, r.client, site.name, address.String(), "text/html")
	if err != nil {
		return site.name, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return site.name, "", fmt.Errorf("%s replied %s", site.name, resp.Status)
	}

	// Russian sites in particular still serve windows-1251; the header or the
	// page itself says so, and the text is turned into UTF-8 before reading.
	body, err := charset.NewReader(limitBody(resp), resp.Header.Get("Content-Type"))
	if err != nil {
		return site.name, "", fmt.Errorf("%s: %w", site.name, err)
	}
	page, err := io.ReadAll(body)
	if err != nil {
		return site.name, "", fmt.Errorf("%s: %w", site.name, err)
	}
	return site.name, site.read(string(page)), nil
}

// extractAnyLyrics reads a page from a site with no reader of its own.
//
// Both readings are taken because either can fall short: SongLyrics declares
// only the opening verses, and Musixmatch prints nothing a reader could find.
func extractAnyLyrics(page string) string {
	declared, printed := extractDeclared(page), extractBrokenLines(page)
	if len(printed) > len(declared) {
		return printed
	}
	return declared
}

// The words as a page declares them in its data: schema.org's
// "lyrics": {"text": …}, or the "lyrics": {"body": …} Musixmatch ships with
// the page. Nothing inside the object before the words may open another one,
// which keeps the match from wandering into the rest of the data.
var declaredLyrics = regexp.MustCompile(`"lyrics"\s*:\s*\{[^{}]*?"(?:text|body)"\s*:\s*("(?:[^"\\]|\\.)*")`)

func extractDeclared(page string) string {
	match := declaredLyrics.FindStringSubmatch(page)
	if match == nil {
		return ""
	}
	var text string
	if err := json.Unmarshal([]byte(match[1]), &text); err != nil {
		return ""
	}
	// Musixmatch marks its copy by writing apostrophes as primes.
	return strings.TrimSpace(strings.ReplaceAll(text, "′", "'"))
}

var (
	// Everything that is never the words: code, styling, and comments, one of
	// which AZLyrics puts right at the top of the song.
	hiddenMarkup = regexp.MustCompile(`(?is)<script\b.*?</script>|<style\b.*?</style>|<noscript\b.*?</noscript>|<!--.*?-->`)
	// Tags that end one part of a page and start another. A paragraph is not
	// among them: sites put each verse in its own.
	sectionTag = regexp.MustCompile(`(?i)</?(?:div|section|article|aside|main|header|footer|nav|ul|ol|li|table|tr|td|h[1-6]|form|button|iframe|body)\b[^>]*>`)
	paraEnd    = regexp.MustCompile(`(?i)</p>`)
	// An element with nothing in it; nesting is undone one layer at a time.
	emptyElement = regexp.MustCompile(`(?i)<(?:div|ins|aside|section|span)\b[^>]*>\s*</(?:div|ins|aside|section|span)>`)
	// A line break in the markup itself is only layout. AZLyrics follows every
	// <br> with one, which would otherwise double every line.
	sourceBreak = regexp.MustCompile(`[ \t]*[\r\n]+[ \t]*`)
)

// A song is at least this many lines; a shorter run is an address or a menu.
const minBrokenLines = 4

// extractBrokenLines finds the part of the page with the most line breaks in
// it, which on a lyrics site is the song.
//
// Sites slot advertisements between the verses, empty until a script fills
// them. Those are taken out first, so the song is not cut in two where one
// sits.
func extractBrokenLines(page string) string {
	page = hiddenMarkup.ReplaceAllString(page, "")
	for {
		emptied := emptyElement.ReplaceAllString(page, "")
		if emptied == page {
			break
		}
		page = emptied
	}

	best, most := "", 0
	for _, part := range sectionTag.Split(page, -1) {
		if n := len(lineBreak.FindAllStringIndex(part, -1)); n > most {
			best, most = part, n
		}
	}
	if most < minBrokenLines {
		return ""
	}

	best = sourceBreak.ReplaceAllString(best, " ")
	lines := strings.Split(htmlToText(paraEnd.ReplaceAllString(best, "\n\n")), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.Join(lines, "\n")
}

// AZLyrics opens the song with a comment addressed to scrapers, which marks
// exactly where it starts; the title printed just above would otherwise be
// taken for its first lines.
var azlyricsStart = regexp.MustCompile(`(?i)<!--[^>]*Usage of azlyrics\.com content`)

func extractAZLyrics(page string) string {
	loc := azlyricsStart.FindStringIndex(page)
	if loc == nil {
		return extractBrokenLines(page)
	}
	song := page[loc[0]:]
	if end := strings.Index(strings.ToLower(song), "</div>"); end >= 0 {
		song = song[:end]
	}
	return extractBrokenLines(song)
}

// Amalgama sets the original beside a translation, one line per pair, and an
// empty pair where the verse breaks. A line holding a link is the site's own
// note pointing at another song, not part of this one.
var (
	amalgamaLine = regexp.MustCompile(`(?is)<div class="original">(.*?)</div>`)
	linkTag      = regexp.MustCompile(`(?i)<a\b`)
)

func extractAmalgama(page string) string {
	var lines []string
	for _, match := range amalgamaLine.FindAllStringSubmatch(page, -1) {
		if linkTag.MatchString(match[1]) {
			continue
		}
		lines = append(lines, htmlToText(match[1]))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// errNoWords says a page was read but nothing on it looked like a song.
var errNoWords = errors.New("no lyrics could be found on that page")

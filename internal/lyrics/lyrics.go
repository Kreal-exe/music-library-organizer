// Package lyrics finds song lyrics for a track and hands back plain text ready
// to be embedded in its tags.
//
// Sources are tried in order and the first confident match wins. LRCLIB covers
// released music well but knows almost nothing about leaks and unreleased
// tracks, which is exactly where Genius is strongest, so the two together cover
// far more of a real collection than either alone.
package lyrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// ErrNotFound means no source had lyrics confident enough to embed.
var ErrNotFound = errors.New("no lyrics found")

// Query describes the track to look up.
type Query struct {
	Artist string
	Title  string
	// AlbumArtist is the second opinion on who made the track. Files from a
	// download service often carry the service's own name as the artist and
	// the real one only here.
	AlbumArtist string
	Album       string
	Duration    time.Duration

	// Path is the file itself. Its name is the last thing to go on when the
	// tags name the wrong artist: a download keeps "Artist - Title" in the
	// file name long after the artist field has been overwritten.
	Path string
}

// Match is one candidate set of lyrics.
type Match struct {
	Source string  `json:"source"`
	Artist string  `json:"artist"`
	Title  string  `json:"title"`
	Lyrics string  `json:"lyrics"`
	URL    string  `json:"url"`
	Score  float64 `json:"score"`

	// Synced is the same words with a timestamp on every line, when the
	// source has them. Phone players show these first, and scroll them with
	// the song; they are saved beside the track rather than in the tag.
	Synced string `json:"-"`

	// Duration is the candidate track length, when the source reports one.
	Duration time.Duration `json:"-"`
}

// provider is one lyrics source.
type provider interface {
	name() string
	search(ctx context.Context, q Query) ([]Match, error)
}

// Finder looks up lyrics across the configured sources.
type Finder struct {
	// pages reads the lyrics off one page, for a link the user found.
	pages *genius

	// rounds are the sources in the order they are asked. Everything in one
	// round is tried for every reading of the track's name before the next
	// round starts, so a source that can only confirm a name it is given never
	// answers ahead of one that can actually search.
	rounds [][]provider

	// MinScore is how close a result's artist and title must be before it is
	// accepted. Embedding the wrong lyrics is worse than embedding none.
	MinScore float64
}

// NewFinder builds a finder over the default sources.
func NewFinder() *Finder {
	client := &http.Client{Timeout: 20 * time.Second}

	pages := &genius{client: client, limiter: newLimiter(700 * time.Millisecond)}

	return &Finder{
		pages: pages,
		rounds: [][]provider{
			{
				&lrclib{client: client, limiter: newLimiter(250 * time.Millisecond)},
				// Genius is only asked about tracks LRCLIB could not place, and
				// is paced more slowly because it serves full web pages.
				pages,
			},
			{
				// A round of its own, and last: it cannot search, only confirm
				// the name it is handed, so anything it says is taken on trust
				// and must not get in ahead of a source that looked.
				&lyricsovh{client: client, limiter: newLimiter(400 * time.Millisecond)},
			},
		},
		MinScore: 0.62,
	}
}

// Find returns the best lyrics for a track, or ErrNotFound.
func (f *Finder) Find(ctx context.Context, q Query) (Match, error) {
	searches := attempts(q)
	if len(searches) == 0 {
		return Match{}, ErrNotFound
	}

	var firstErr error
	answered := 0

	for _, round := range f.rounds {
		for _, attempt := range searches {
			for _, p := range round {
				if err := ctx.Err(); err != nil {
					return Match{}, err
				}

				results, err := p.search(ctx, attempt)
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				answered++

				best := Match{}
				for _, candidate := range results {
					candidate.Score = score(attempt, candidate)
					if candidate.Lyrics != "" && candidate.Score > best.Score {
						best = candidate
					}
				}
				if best.Score >= f.MinScore {
					best.Lyrics = tidyLyrics(best.Lyrics)
					if best.Lyrics != "" {
						return best, nil
					}
				}
			}
		}
	}

	// A source that fell over is only worth reporting if no source answered at
	// all; otherwise the track was searched for properly and simply is not
	// there, and saying "503" would send the user looking for a fault.
	if answered == 0 && firstErr != nil {
		return Match{}, firstErr
	}
	return Match{}, ErrNotFound
}

// attempts lists the searches worth making for one track, best first.
//
// The tags of a real collection are wrong in two ordinary ways: the artist
// field holds something that is not an artist, and the title carries a
// qualifier no database knows about. Each is answered by one more search
// rather than by guessing, and the whole list is only worked through while
// nothing has been found.
func attempts(q Query) []Query {
	var artists []string
	add := func(raw string) {
		name := leadArtist(strings.TrimSpace(raw))
		if name == "" || placeholderArtist(name) {
			return
		}
		for _, have := range artists {
			if fold(have) == fold(name) {
				return
			}
		}
		artists = append(artists, name)
	}
	add(q.Artist)
	add(q.AlbumArtist)

	fileArtist, fileTitle := nameFromFile(q.Path)
	add(fileArtist)

	if len(artists) == 0 {
		// Every name was a stand-in, so the tag is all there is to go on.
		for _, raw := range []string{q.Artist, q.AlbumArtist} {
			if name := leadArtist(strings.TrimSpace(raw)); name != "" {
				artists = append(artists, name)
				break
			}
		}
	}

	titles := []string{cleanTitle(q.Title)}
	if bare := bareTitle(q.Title); !strings.EqualFold(bare, titles[0]) {
		titles = append(titles, bare)
	}
	if fileTitle = cleanTitle(fileTitle); fileTitle != "" {
		titles = append(titles, fileTitle)
	}

	if len(artists) == 0 || titles[0] == "" {
		return nil
	}

	var out []Query
	seen := map[string]bool{}
	pair := func(artist, title string) {
		if artist == "" || title == "" || len(out) >= maxAttempts {
			return
		}
		key := fold(artist) + "\x00" + fold(title)
		if seen[key] {
			return
		}
		seen[key] = true

		attempt := q
		attempt.Artist, attempt.Title = artist, title
		out = append(out, attempt)
	}

	for _, title := range titles {
		for _, artist := range artists {
			pair(artist, title)
		}
	}
	return out
}

// How many searches one track is worth. Every reading beyond the first is only
// reached when everything before it came back empty, but a library of
// thousands of tracks is scanned one request at a time, so the list has to end.
const maxAttempts = 5

// nameFromFile reads "Artist - Title" out of a file name. A tagger can write
// anything into the fields; the name a file was downloaded under is the one
// thing that was true when it was made.
func nameFromFile(path string) (artist, title string) {
	name := filepath.Base(strings.ReplaceAll(path, `\`, "/"))
	if name == "." || name == "/" {
		return "", ""
	}
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = leadingNumber.ReplaceAllString(strings.TrimSpace(name), "")

	for _, sep := range []string{" — ", " – ", " - ", " -- "} {
		if before, after, found := strings.Cut(name, sep); found {
			return strings.TrimSpace(before), strings.TrimSpace(after)
		}
	}
	// No separator: the whole name is as likely to be the title as anything.
	return "", strings.TrimSpace(name)
}

// How long to wait before trying a request again. A free service under load
// answers 503 for a moment and is there again straight after, and a library of
// thousands of tracks meets that moment often.
var retryDelays = []time.Duration{time.Second, 4 * time.Second}

// fetch performs a GET, retrying while the answer is a hiccup rather than an
// answer. The response is handed back open for the caller to read and close.
func fetch(ctx context.Context, client *http.Client, source, target, accept string) (*http.Response, error) {
	var last error

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}

		resp, err := client.Do(req)
		switch {
		case err != nil:
			last = fmt.Errorf("%s: %w", source, err)
		case !worthRetrying(resp.StatusCode):
			return resp, nil
		default:
			resp.Body.Close()
			last = fmt.Errorf("%s replied %s", source, resp.Status)
		}

		if attempt >= len(retryDelays) {
			return nil, last
		}
		timer := time.NewTimer(retryDelays[attempt])
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
}

// worthRetrying reports whether a status means "not now" rather than "no".
func worthRetrying(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// limiter spaces out requests to one host, so a scan of a large library stays a
// polite trickle rather than a burst.
type limiter struct {
	gap  time.Duration
	last chan time.Time
}

func newLimiter(gap time.Duration) *limiter {
	last := make(chan time.Time, 1)
	last <- time.Time{}
	return &limiter{gap: gap, last: last}
}

func (l *limiter) wait(ctx context.Context) error {
	select {
	case previous := <-l.last:
		delay := l.gap - time.Since(previous)
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				l.last <- previous
				return ctx.Err()
			}
		}
		l.last <- time.Now()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// FromURL reads the lyrics off one page, for a track the search cannot place.
//
// Some songs are filed under a name nothing in the file resembles — a Russian
// act catalogued under a transliteration, a leak credited to whoever leaked
// it — and no amount of searching will bridge that. Pointing at the page is
// the answer, and it is also the one case where a match needs no scoring: the
// user has already decided this is the song.
func (f *Finder) FromURL(ctx context.Context, link string) (Match, error) {
	address, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (address.Scheme != "http" && address.Scheme != "https") || address.Host == "" {
		return Match{}, errors.New("that is not a web address")
	}
	if host := strings.TrimPrefix(strings.ToLower(address.Host), "www."); host != "genius.com" {
		return Match{}, fmt.Errorf("%s is not understood — paste a genius.com song page", address.Host)
	}

	text, err := f.pages.fetchLyrics(ctx, address.String())
	if err != nil {
		return Match{}, err
	}
	if text = tidyLyrics(text); text == "" {
		return Match{}, errors.New("that page has no lyrics on it")
	}

	return Match{Source: "Genius", URL: address.String(), Lyrics: text, Score: 1}, nil
}

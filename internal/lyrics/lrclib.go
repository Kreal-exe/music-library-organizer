package lyrics

// LRCLIB is an open lyrics database with no key and no registration. It is the
// first source tried because its entries are already plain text and it reports
// track length, which settles which of two songs with the same name this is.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const lrclibEndpoint = "https://lrclib.net/api/search"

type lrclib struct {
	client  *http.Client
	limiter *limiter
}

func (l *lrclib) name() string { return "LRCLIB" }

type lrclibEntry struct {
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

func (l *lrclib) search(ctx context.Context, q Query) ([]Match, error) {
	artist := leadArtist(q.Artist)

	// The field search is exact about the artist's name, which is how it tells
	// two songs of the same name apart; the free-text search is not, and finds
	// the entries filed under "Artist, Guest" or with the album in the name.
	byField := url.Values{"artist_name": {artist}, "track_name": {q.Title}}
	byText := url.Values{"q": {artist + " " + q.Title}}

	var firstErr error
	for _, query := range []url.Values{byField, byText} {
		matches, err := l.ask(ctx, query)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(matches) > 0 {
			return matches, nil
		}
	}
	return nil, firstErr
}

func (l *lrclib) ask(ctx context.Context, query url.Values) ([]Match, error) {
	if err := l.limiter.wait(ctx); err != nil {
		return nil, err
	}

	resp, err := fetch(ctx, l.client, l.name(), lrclibEndpoint+"?"+query.Encode(), "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LRCLIB replied %s", resp.Status)
	}

	var entries []lrclibEntry
	if err := json.NewDecoder(limitBody(resp)).Decode(&entries); err != nil {
		return nil, fmt.Errorf("LRCLIB: %w", err)
	}

	matches := make([]Match, 0, len(entries))
	for _, entry := range entries {
		if entry.Instrumental || entry.PlainLyrics == "" {
			continue
		}
		matches = append(matches, Match{
			Source:   l.name(),
			Artist:   entry.ArtistName,
			Title:    entry.TrackName,
			Lyrics:   entry.PlainLyrics,
			Synced:   entry.SyncedLyrics,
			Duration: time.Duration(entry.Duration * float64(time.Second)),
		})
	}
	return matches, nil
}

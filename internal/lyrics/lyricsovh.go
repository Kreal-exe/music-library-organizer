package lyrics

// lyrics.ovh is the third opinion. It has no search: it answers about one
// artist and one title, and says nothing at all if it does not know them. That
// makes it useless for finding a song, and worth asking about the songs the
// other two could not place — its index is built from somewhere else, so it
// carries tracks neither of them has.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

const lyricsOvhEndpoint = "https://api.lyrics.ovh/v1/"

type lyricsovh struct {
	client  *http.Client
	limiter *limiter
}

func (l *lyricsovh) name() string { return "lyrics.ovh" }

func (l *lyricsovh) search(ctx context.Context, q Query) ([]Match, error) {
	artist := leadArtist(q.Artist)
	if artist == "" || q.Title == "" {
		return nil, nil
	}

	if err := l.limiter.wait(ctx); err != nil {
		return nil, err
	}

	target := lyricsOvhEndpoint + url.PathEscape(artist) + "/" + url.PathEscape(q.Title)
	resp, err := fetch(ctx, l.client, l.name(), target, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// The service answers 404 for everything it does not have, which is most
	// of what it is asked about.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lyrics.ovh replied %s", resp.Status)
	}

	var answer struct {
		Lyrics string `json:"lyrics"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(limitBody(resp)).Decode(&answer); err != nil {
		return nil, fmt.Errorf("lyrics.ovh: %w", err)
	}
	if answer.Lyrics == "" {
		return nil, nil
	}

	// There is nothing to compare against: the service was asked about this
	// artist and this title and answered about them, so the names it matched
	// are the ones it was given.
	return []Match{{
		Source: l.name(),
		Artist: artist,
		Title:  q.Title,
		Lyrics: answer.Lyrics,
	}}, nil
}

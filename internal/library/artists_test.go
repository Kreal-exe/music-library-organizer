package library

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"musiclibraryorganizer/internal/tags"
)

func TestSplitArtists(t *testing.T) {
	cases := []struct {
		in      string
		primary string
		rest    []string
		certain bool
		split   bool
	}{
		{in: "Juice WRLD", split: false},
		{in: "Earth, Wind & Fire", primary: "Earth", rest: []string{"Wind", "Fire"}, split: true},
		{in: "AC/DC", split: false},
		{in: "Juice WRLD feat. Trippie Redd", primary: "Juice WRLD", rest: []string{"Trippie Redd"}, certain: true, split: true},
		{in: "Juice WRLD (feat. Trippie Redd)", primary: "Juice WRLD", rest: []string{"Trippie Redd"}, certain: true, split: true},
		{in: "Juice WRLD [ft. Lil Uzi Vert]", primary: "Juice WRLD", rest: []string{"Lil Uzi Vert"}, certain: true, split: true},
		{in: "ZillaKami x SosMula", primary: "ZillaKami", rest: []string{"SosMula"}, split: true},
		{in: "A feat. B & C", primary: "A", rest: []string{"B", "C"}, certain: true, split: true},
		{in: "Artist A; Artist B", primary: "Artist A", rest: []string{"Artist B"}, split: true},
		{in: "Kino", split: false},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := SplitArtists(tc.in)
			if ok != tc.split {
				t.Fatalf("split = %v, expected %v (%+v)", ok, tc.split, got)
			}
			if !tc.split {
				return
			}
			if got.Primary != tc.primary {
				t.Errorf("Primary = %q, expected %q", got.Primary, tc.primary)
			}
			if !reflect.DeepEqual(got.Rest, tc.rest) {
				t.Errorf("Rest = %q, expected %q", got.Rest, tc.rest)
			}
			if got.Certain != tc.certain {
				t.Errorf("Certain = %v, expected %v", got.Certain, tc.certain)
			}
		})
	}
}

func TestSpellingKeyFoldsVariants(t *testing.T) {
	same := [][]string{
		{"The Beatles", "Beatles", "the beatles", "The  Beatles"},
		{"Guns N' Roses", "Guns N Roses", "guns n' roses"},
		{"Гражданская Оборона", "гражданская оборона"},
	}
	for _, group := range same {
		want := spellingKey(group[0])
		for _, value := range group[1:] {
			if got := spellingKey(value); got != want {
				t.Errorf("spellingKey(%q) = %q, expected %q", value, got, want)
			}
		}
	}

	if spellingKey("Beatles") == spellingKey("Beatles Tribute") {
		t.Error("two different artists folded to one key")
	}
}

func lib(tracks ...tags.Track) *Library {
	return &Library{Tracks: tracks}
}

// find returns the target with the given name.
func find(targets []Target, name string) (Target, bool) {
	for _, target := range targets {
		if target.Name == name {
			return target, true
		}
	}
	return Target{}, false
}

// The point of the whole tool: a pile of spellings and guest credits has to
// collapse into the handful of artists the music is actually by.
func TestReducesToPrimaryArtists(t *testing.T) {
	got := lib(
		tags.Track{Path: "1.mp3", Artist: "Juice WRLD", Album: "Leaks"},
		tags.Track{Path: "2.mp3", Artist: "juice wrld", Album: "Leaks"},
		tags.Track{Path: "3.mp3", Artist: "JUICE WRLD", Album: "Leaks"},
		tags.Track{Path: "4.mp3", Artist: "Juice WRLD feat. Trippie Redd", Album: "Leaks"},
		tags.Track{Path: "5.mp3", Artist: "Juice WRLD & Young Thug", Album: "Leaks"},
		tags.Track{Path: "6.mp3", Artist: "Eminem", Album: "Recovery"},
		tags.Track{Path: "7.mp3", Artist: "Eminem feat. Rihanna", Album: "Recovery"},
	).Summarize()

	if got.RawArtists != 7 {
		t.Errorf("RawArtists = %d, expected 7", got.RawArtists)
	}
	if len(got.Targets) != 2 {
		t.Fatalf("%d targets, expected 2: %+v", len(got.Targets), got.Targets)
	}

	juice, ok := find(got.Targets, "Juice WRLD")
	if !ok {
		t.Fatalf("Juice WRLD is not a target: %+v", got.Targets)
	}
	if juice.Tracks != 5 {
		t.Errorf("Juice WRLD: %d tracks, expected 5", juice.Tracks)
	}
	if len(juice.Sources) != 5 {
		t.Errorf("Juice WRLD: %d sources, expected 5: %+v", len(juice.Sources), juice.Sources)
	}

	// Each source has to say why it folds in, so the interface can show it and
	// the user can detach the ones that are wrong.
	kinds := map[string]string{}
	for _, source := range juice.Sources {
		kinds[source.Value] = source.Kind
	}
	expected := map[string]string{
		"Juice WRLD":                    KindExact,
		"juice wrld":                    KindSpelling,
		"JUICE WRLD":                    KindSpelling,
		"Juice WRLD feat. Trippie Redd": KindFeature,
		"Juice WRLD & Young Thug":       KindSeparator,
	}
	for value, want := range expected {
		if kinds[value] != want {
			t.Errorf("%q folded as %q, expected %q", value, kinds[value], want)
		}
	}
}

func TestTargetKeepsTheBestSpelling(t *testing.T) {
	cases := []struct {
		names []string
		want  string
	}{
		{[]string{"кино", "Кино"}, "Кино"},
		{[]string{"JUICE WRLD", "juice wrld", "Juice WRLD"}, "Juice WRLD"},
		{[]string{"beatles", "BEATLES"}, "BEATLES"},
	}

	for _, tc := range cases {
		var tracks []tags.Track
		for i, name := range tc.names {
			tracks = append(tracks, tags.Track{
				Path:   fmt.Sprintf("%d.mp3", i),
				Artist: name,
				Album:  "Album",
			})
		}

		got := lib(tracks...).Summarize()
		if len(got.Targets) != 1 {
			t.Fatalf("%v: %d targets, expected 1", tc.names, len(got.Targets))
		}
		if got.Targets[0].Name != tc.want {
			t.Errorf("%v: kept %q, expected %q", tc.names, got.Targets[0].Name, tc.want)
		}
	}
}

// The same library must always produce the same answer, whatever order the
// maps happen to iterate.
func TestReductionIsStable(t *testing.T) {
	tracks := []tags.Track{
		{Path: "1.mp3", Artist: "Кино", Album: "A"},
		{Path: "2.mp3", Artist: "кино", Album: "A"},
		{Path: "3.mp3", Artist: "КИНО", Album: "A"},
	}

	want := lib(tracks...).Summarize().Targets[0].Name
	for i := range 40 {
		if got := lib(tracks...).Summarize().Targets[0].Name; got != want {
			t.Fatalf("run %d gave %q instead of %q", i, got, want)
		}
	}
}

// An album artist is an artist too: a guest credit or a misspelling there
// produces the same extra row in a player.
func TestAlbumArtistsJoinTheReduction(t *testing.T) {
	got := lib(
		tags.Track{Path: "1.mp3", Artist: "Juice WRLD", AlbumArtist: "juice wrld", Album: "Leaks"},
	).Summarize()

	if len(got.Targets) != 1 || got.Targets[0].Name != "Juice WRLD" {
		t.Fatalf("targets = %+v", got.Targets)
	}
	if len(got.AlbumArtists) != 1 || got.AlbumArtists[0].Value != "juice wrld" {
		t.Errorf("album artists = %+v", got.AlbumArtists)
	}
}

func TestSummarizeCounts(t *testing.T) {
	got := lib(
		tags.Track{Path: "1.mp3", Artist: "A", AlbumArtist: "Various Artists", Album: "Hits", Compilation: true},
		tags.Track{Path: "2.mp3", Artist: "B", AlbumArtist: "Various Artists", Album: "Hits", Compilation: true, HasSort: true},
		tags.Track{Path: "3.mp3", Artist: "C", Album: "Solo", HasLyrics: true},
	).Summarize()

	if got.TrackCount != 3 {
		t.Errorf("TrackCount = %d", got.TrackCount)
	}
	if got.AlbumArtistCount != 2 {
		t.Errorf("AlbumArtistCount = %d", got.AlbumArtistCount)
	}
	if got.CompilationCount != 2 {
		t.Errorf("CompilationCount = %d", got.CompilationCount)
	}
	if got.SortCount != 1 {
		t.Errorf("SortCount = %d", got.SortCount)
	}
	if got.WithLyrics != 1 {
		t.Errorf("WithLyrics = %d", got.WithLyrics)
	}
}

// A tidy collection reports empty lists, not nulls: the interface iterates
// these fields directly, and a null there stops the whole view rendering.
func TestEmptySummaryMarshalsAsLists(t *testing.T) {
	encoded, err := json.Marshal((&Library{}).Summarize())
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"targets", "albumArtists"} {
		if !strings.Contains(string(encoded), fmt.Sprintf(`"%s":[]`, field)) {
			t.Errorf("%s is not serialised as an empty list: %s", field, encoded)
		}
	}
}

func TestScanResultHasEmptyErrorList(t *testing.T) {
	scanned, err := Scan(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(scanned)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"errors":[]`) {
		t.Errorf("errors is not serialised as an empty list: %s", encoded)
	}
}

func TestSplitArtistsDropsTheUploader(t *testing.T) {
	cases := []struct {
		value   string
		primary string
		rest    int
	}{
		{"Juice WRLD | @uploads_channel", "Juice WRLD", 0},
		{"Кино | t.me/rock", "Кино", 0},
		{"Juice WRLD | Future | @uploads_channel", "Juice WRLD", 1},
		{"City Morgue", "", 0},
	}

	for _, c := range cases {
		split, ok := SplitArtists(c.value)
		if c.primary == "" {
			if ok {
				t.Errorf("SplitArtists(%q) split a plain name into %+v", c.value, split)
			}
			continue
		}
		if !ok {
			t.Errorf("SplitArtists(%q) did not split", c.value)
			continue
		}
		if split.Primary != c.primary {
			t.Errorf("SplitArtists(%q) led with %q, expected %q", c.value, split.Primary, c.primary)
		}
		if len(split.Rest) != c.rest {
			t.Errorf("SplitArtists(%q) kept %v beside it, expected %d name(s)", c.value, split.Rest, c.rest)
		}
	}
}

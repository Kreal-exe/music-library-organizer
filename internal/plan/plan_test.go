package plan

import (
	"testing"

	"musiclibraryorganizer/internal/library"
	"musiclibraryorganizer/internal/tags"
)

func lib(tracks ...tags.Track) *library.Library {
	return &library.Library{Tracks: tracks}
}

// find returns the planned change for one path.
func find(changes []Change, path string) (Change, bool) {
	for _, change := range changes {
		if change.Path == path {
			return change, true
		}
	}
	return Change{}, false
}

func noManual() map[string]tags.Edit { return nil }

func TestRenamesArtists(t *testing.T) {
	changes := Build(lib(
		tags.Track{Path: "a.mp3", Artist: "juice wrld", Album: "Leaks"},
		tags.Track{Path: "b.mp3", Artist: "Juice WRLD", Album: "Leaks"},
	), Rules{
		Rename:          map[string]string{"juice wrld": "Juice WRLD"},
		AlbumArtistMode: AlbumArtistKeep,
	}, noManual())

	if len(changes) != 1 || changes[0].Path != "a.mp3" {
		t.Fatalf("expected one change for a.mp3: %+v", changes)
	}
	if got := *changes[0].edit.Artist; got != "Juice WRLD" {
		t.Errorf("Artist = %q", got)
	}
}

// A rename follows the artist into the album-artist field, so an album does
// not end up split between two spellings.
func TestRenameAppliesToAlbumArtist(t *testing.T) {
	changes := Build(lib(
		tags.Track{Path: "a.mp3", Artist: "Beatles", AlbumArtist: "Beatles"},
	), Rules{
		Rename:          map[string]string{"Beatles": "The Beatles"},
		AlbumArtistMode: AlbumArtistKeep,
	}, noManual())

	change, ok := find(changes, "a.mp3")
	if !ok {
		t.Fatal("no change was built")
	}
	if got := *change.edit.AlbumArtist; got != "The Beatles" {
		t.Errorf("AlbumArtist = %q", got)
	}
}

func TestAlbumArtistModes(t *testing.T) {
	track := tags.Track{Path: "a.mp3", Artist: "Real Artist", AlbumArtist: "Various Artists"}

	t.Run("clear", func(t *testing.T) {
		change, ok := find(Build(lib(track), Rules{AlbumArtistMode: AlbumArtistClear}, noManual()), "a.mp3")
		if !ok {
			t.Fatal("no change was built")
		}
		if got := *change.edit.AlbumArtist; got != "" {
			t.Errorf("AlbumArtist = %q, expected an empty string", got)
		}
	})

	t.Run("fromArtist", func(t *testing.T) {
		change, ok := find(Build(lib(track), Rules{AlbumArtistMode: AlbumArtistFromArtist}, noManual()), "a.mp3")
		if !ok {
			t.Fatal("no change was built")
		}
		if got := *change.edit.AlbumArtist; got != "Real Artist" {
			t.Errorf("AlbumArtist = %q", got)
		}
	})

	t.Run("keep", func(t *testing.T) {
		if changes := Build(lib(track), Rules{AlbumArtistMode: AlbumArtistKeep}, noManual()); len(changes) != 0 {
			t.Errorf("nothing must change without rules: %+v", changes)
		}
	})
}

func TestCompilationRemovedOnlyWhenPresent(t *testing.T) {
	changes := Build(lib(
		tags.Track{Path: "a.mp3", Artist: "A", Compilation: true},
		tags.Track{Path: "b.mp3", Artist: "B"},
	), Rules{RemoveCompilation: true, AlbumArtistMode: AlbumArtistKeep}, noManual())

	if len(changes) != 1 || changes[0].Path != "a.mp3" {
		t.Fatalf("expected one change for a.mp3: %+v", changes)
	}
	if !changes[0].edit.RemoveCompilation {
		t.Error("the compilation flag was not cleared")
	}
}

/* Edits made in the table ------------------------------------------------- */

// Setting a genre across a selection is the everyday use of the table.
func TestManualGenreAcrossSelection(t *testing.T) {
	manual := map[string]tags.Edit{
		"a.mp3": {Genre: tags.Str("Emo Rap")},
		"b.mp3": {Genre: tags.Str("Emo Rap")},
	}

	changes := Build(lib(
		tags.Track{Path: "a.mp3", Artist: "A", Genre: "Rap"},
		tags.Track{Path: "b.mp3", Artist: "B"},
		tags.Track{Path: "c.mp3", Artist: "C", Genre: "Rock"},
	), Rules{AlbumArtistMode: AlbumArtistKeep}, manual)

	if len(changes) != 2 {
		t.Fatalf("%d changes, expected 2: %+v", len(changes), changes)
	}
	for _, change := range changes {
		if got := *change.edit.Genre; got != "Emo Rap" {
			t.Errorf("%s: Genre = %q", change.Path, got)
		}
		if change.edit.Artist != nil {
			t.Errorf("%s: the artist was touched as well", change.Path)
		}
	}
}

// A track already carrying the wanted value is not rewritten for nothing.
func TestManualEditMatchingTheFileIsNoChange(t *testing.T) {
	manual := map[string]tags.Edit{"a.mp3": {Genre: tags.Str("Rap")}}

	changes := Build(lib(
		tags.Track{Path: "a.mp3", Artist: "A", Genre: "Rap"},
	), Rules{AlbumArtistMode: AlbumArtistKeep}, manual)

	if len(changes) != 0 {
		t.Errorf("a no-op edit produced changes: %+v", changes)
	}
}

// What the user typed for one track wins over what a library-wide rule wanted.
func TestManualEditOverridesRules(t *testing.T) {
	manual := map[string]tags.Edit{"a.mp3": {Artist: tags.Str("Lil Peep")}}

	changes := Build(lib(
		tags.Track{Path: "a.mp3", Artist: "juice wrld"},
	), Rules{
		Rename:          map[string]string{"juice wrld": "Juice WRLD"},
		AlbumArtistMode: AlbumArtistKeep,
	}, manual)

	change, ok := find(changes, "a.mp3")
	if !ok {
		t.Fatal("no change was built")
	}
	if got := *change.edit.Artist; got != "Lil Peep" {
		t.Errorf("Artist = %q, expected the hand edit to win", got)
	}
}

func TestManualNumbersAndClearing(t *testing.T) {
	manual := map[string]tags.Edit{
		"a.mp3": {Year: tags.Num(2019), TrackNo: tags.Num(4), Genre: tags.Str("")},
	}

	changes := Build(lib(
		tags.Track{Path: "a.mp3", Artist: "A", Genre: "Rock", Year: 1999, TrackNo: 1},
	), Rules{AlbumArtistMode: AlbumArtistKeep}, manual)

	change, ok := find(changes, "a.mp3")
	if !ok {
		t.Fatal("no change was built")
	}
	if *change.edit.Year != 2019 {
		t.Errorf("Year = %d", *change.edit.Year)
	}
	if *change.edit.TrackNo != 4 {
		t.Errorf("TrackNo = %d", *change.edit.TrackNo)
	}
	if *change.edit.Genre != "" {
		t.Errorf("Genre = %q, expected it cleared", *change.edit.Genre)
	}
}

func TestSummarizeCounts(t *testing.T) {
	changes := Build(lib(
		tags.Track{Path: "a.mp3", Artist: "Beatles", AlbumArtist: "Various Artists", Compilation: true},
		tags.Track{Path: "b.mp3", Artist: "Beatles"},
	), Rules{
		Rename:            map[string]string{"Beatles": "The Beatles"},
		AlbumArtistMode:   AlbumArtistFromArtist,
		RemoveCompilation: true,
	}, map[string]tags.Edit{"a.mp3": {Genre: tags.Str("Rock")}})

	got := Summarize(changes)
	if got.Files != 2 {
		t.Errorf("Files = %d", got.Files)
	}
	if got.ArtistRenamed != 2 {
		t.Errorf("ArtistRenamed = %d", got.ArtistRenamed)
	}
	if got.AlbumArtistSet != 2 {
		t.Errorf("AlbumArtistSet = %d", got.AlbumArtistSet)
	}
	if got.CompilationRemove != 1 {
		t.Errorf("CompilationRemove = %d", got.CompilationRemove)
	}
	if got.GenreSet != 1 {
		t.Errorf("GenreSet = %d", got.GenreSet)
	}
}

// An empty rule set and no hand edits must leave every file alone.
func TestNoRulesNoChanges(t *testing.T) {
	changes := Build(lib(
		tags.Track{Path: "a.mp3", Artist: "A", AlbumArtist: "B", Genre: "Rock", Compilation: true},
	), Rules{AlbumArtistMode: AlbumArtistKeep}, noManual())

	if len(changes) != 0 {
		t.Errorf("changes were built with no rules: %+v", changes)
	}
}

// An album rename reaches the tracks it names, and a disc rejoining its
// album keeps its number.
func TestRenamesAlbums(t *testing.T) {
	changes := Build(lib(
		tags.Track{Path: "cd2.mp3", Artist: "X", Album: "Record (CD2)", TrackNo: 3},
		tags.Track{Path: "other.mp3", Artist: "Y", Album: "Record (CD2)"},
	), Rules{AlbumRename: map[string]string{"cd2.mp3": "Record"}}, noManual())

	if len(changes) != 1 || changes[0].Path != "cd2.mp3" {
		t.Fatalf("expected one change for cd2.mp3: %+v", changes)
	}
	if got := *changes[0].edit.Album; got != "Record" {
		t.Errorf("Album = %q", got)
	}
	if got := changes[0].edit.DiscNo; got == nil || *got != 2 {
		t.Errorf("DiscNo = %v, expected 2", got)
	}
	if got := Summarize(changes).AlbumRenamed; got != 1 {
		t.Errorf("AlbumRenamed = %d", got)
	}
}

func TestClearsAlbum(t *testing.T) {
	changes := Build(lib(
		tags.Track{Path: "a.mp3", Artist: "X", Album: "VK"},
		tags.Track{Path: "b.mp3", Artist: "Y", Album: "VK"},
	), Rules{ClearAlbum: []string{"a.mp3"}}, noManual())

	if len(changes) != 1 || changes[0].Path != "a.mp3" {
		t.Fatalf("expected one change for a.mp3: %+v", changes)
	}
	if got := changes[0].edit.Album; got == nil || *got != "" {
		t.Errorf("Album = %v, expected it cleared", got)
	}
}

// One album whose tracks disagree on the album artist is two albums on a
// phone; the tracks that lack it are given the one the rest carry.
func TestAlbumArtistMadeTheSameAcrossAnAlbum(t *testing.T) {
	changes := Build(lib(
		tags.Track{Path: "a.mp3", Artist: "Juice WRLD", AlbumArtist: "Juice WRLD", Album: "JUICE UNRELEASED"},
		tags.Track{Path: "b.mp3", Artist: "Juice WRLD", Album: "JUICE UNRELEASED"},
		tags.Track{Path: "c.mp3", Artist: "Juice WRLD", AlbumArtist: "Juice WRLD", Album: "JUICE UNRELEASED", Compilation: true},
	), Rules{
		AlbumArtistSet:   map[string]string{"b.mp3": "Juice WRLD"},
		ClearCompilation: []string{"c.mp3"},
	}, noManual())

	b, ok := find(changes, "b.mp3")
	if !ok || b.edit.AlbumArtist == nil || *b.edit.AlbumArtist != "Juice WRLD" {
		t.Errorf("b.mp3 = %+v", b)
	}
	c, ok := find(changes, "c.mp3")
	if !ok || !c.edit.RemoveCompilation {
		t.Errorf("c.mp3 = %+v", c)
	}
	if _, ok := find(changes, "a.mp3"); ok {
		t.Error("a.mp3 already agreed and should not change")
	}
}

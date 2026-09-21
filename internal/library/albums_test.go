package library

import (
	"testing"

	"musiclibraryorganizer/internal/tags"
)

func TestStripAlbum(t *testing.T) {
	cases := map[string]string{
		"Wish You Were Here":                        "Wish You Were Here",
		"Wish You Were Here (Remastered 2011)":      "Wish You Were Here",
		"Death Race for Love (Bonus Track Version)": "Death Race for Love",
		"Goodbye & Good Riddance [Explicit]":        "Goodbye & Good Riddance",
		"Legends Never Die - Single":                "Legends Never Die",
		"Album (Deluxe Edition) [Explicit]":         "Album",
		"Album (2019)":                              "Album",
		"Album (25th Anniversary Edition)":          "Album",
		"Группа крови (Переиздание)":                "Группа крови",
		"Album | @uploads_channel":                  "Album",
		"Album (t.me/channel)":                      "Album",
		"Live at Wembley (Live)":                    "Live at Wembley (Live)",
		"Album (Instrumentals)":                     "Album (Instrumentals)",
		"Album (Slowed + Reverb)":                   "Album (Slowed + Reverb)",
		"(What's the Story) Morning Glory?":         "(What's the Story) Morning Glory?",
		"Title - Part Two":                          "Title - Part Two",
		"@channel":                                  "@channel",
	}
	for in, want := range cases {
		if got := stripAlbum(in); got != want {
			t.Errorf("stripAlbum(%q) = %q, expected %q", in, got, want)
		}
	}
}

func albumsOf(tracks ...tags.Track) []Album {
	albums, _ := summarizeAlbums(tracks)
	return albums
}

func TestAlbumsFoldEditionsAndSpellings(t *testing.T) {
	albums := albumsOf(
		tags.Track{Path: "1", Artist: "Juice WRLD", Album: "Legends Never Die"},
		tags.Track{Path: "2", Artist: "Juice WRLD", Album: "Legends Never Die"},
		tags.Track{Path: "3", Artist: "Juice WRLD feat. Halsey", Album: "legends never die"},
		tags.Track{Path: "4", Artist: "juice wrld", Album: "Legends Never Die [Explicit]"},
		tags.Track{Path: "5", Artist: "Juice WRLD", Album: "Legends Never Die | @leaks"},
	)
	if len(albums) != 1 {
		t.Fatalf("expected one album, got %+v", albums)
	}
	album := albums[0]
	if album.Name != "Legends Never Die" || album.Tracks != 5 || album.Artist != "Juice WRLD" {
		t.Errorf("album = %+v", album)
	}

	kinds := map[string]string{}
	for _, source := range album.Sources {
		kinds[source.Value] = source.Kind
	}
	want := map[string]string{
		"Legends Never Die":            KindExact,
		"legends never die":            KindSpelling,
		"Legends Never Die [Explicit]": KindEdition,
		"Legends Never Die | @leaks":   KindWatermark,
	}
	for value, kind := range want {
		if kinds[value] != kind {
			t.Errorf("%q: kind %q, expected %q", value, kinds[value], kind)
		}
	}
}

// Two artists' albums of the same name are two records.
func TestAlbumsStayApartAcrossArtists(t *testing.T) {
	albums := albumsOf(
		tags.Track{Path: "1", Artist: "Queen", Album: "Greatest Hits"},
		tags.Track{Path: "2", Artist: "ABBA", Album: "Greatest Hits"},
		tags.Track{Path: "3", Artist: "Various", AlbumArtist: "ABBA", Album: "greatest hits"},
	)
	if len(albums) != 2 {
		t.Fatalf("expected two albums, got %+v", albums)
	}
	for _, album := range albums {
		if album.Artist == "ABBA" && album.Tracks != 2 {
			t.Errorf("ABBA's album should hold 2 tracks: %+v", album)
		}
	}
}

// A compilation belongs together whoever each track is by.
func TestCompilationIsOneAlbum(t *testing.T) {
	albums := albumsOf(
		tags.Track{Path: "1", Artist: "A", Album: "Now 50", Compilation: true},
		tags.Track{Path: "2", Artist: "B", Album: "Now 50", Compilation: true},
	)
	if len(albums) != 1 || albums[0].Tracks != 2 {
		t.Fatalf("expected one album of two tracks, got %+v", albums)
	}
}

// With every spelling used equally, the plain name is kept over an edition.
func TestAlbumKeepsPlainName(t *testing.T) {
	albums := albumsOf(
		tags.Track{Path: "1", Artist: "X", Album: "Record (Deluxe)"},
		tags.Track{Path: "2", Artist: "X", Album: "Record"},
	)
	if len(albums) != 1 || albums[0].Name != "Record" {
		t.Fatalf("expected Record, got %+v", albums)
	}
}

func TestAlbumsFoldDiscsAndWordOrder(t *testing.T) {
	albums := albumsOf(
		tags.Track{Path: "1", Artist: "Lil Uzi Vert", Album: "Eternal Atake (Deluxe) - LUV vs. The World 2 (CD1)"},
		tags.Track{Path: "2", Artist: "Lil Uzi Vert", Album: "Eternal Atake (Deluxe) - LUV vs. The World 2 (CD2)"},
		tags.Track{Path: "3", Artist: "Lil Uzi Vert", Album: "Eternal Atake (Deluxe) - LUV vs. The World 2 (CD2)"},
		tags.Track{Path: "4", Artist: "Тима Белорусских", Album: "Моя кассета - твой первый диск"},
		tags.Track{Path: "5", Artist: "Тима Белорусских", Album: "Твой первый диск - моя кассета"},
		tags.Track{Path: "6", Artist: "Кишлак", Album: "Пацанский эмо-рэп"},
		tags.Track{Path: "7", Artist: "Кишлак", Album: "Пацанский эмо рэп 2"},
	)
	names := map[string]Album{}
	for _, album := range albums {
		names[album.Name] = album
	}
	if a, ok := names["Eternal Atake (Deluxe) - LUV vs. The World 2"]; !ok || a.Tracks != 3 {
		t.Errorf("discs were not folded into one album: %+v", albums)
	} else if a.Sources[0].Kind != KindDisc {
		t.Errorf("kind = %q, expected %q", a.Sources[0].Kind, KindDisc)
	}
	if len(albums) != 4 {
		t.Errorf("expected 4 albums, got %d: %+v", len(albums), albums)
	}
	if DiscOf("Record (CD2)") != 2 || DiscOf("Record [Disc 11]") != 11 || DiscOf("Record") != 0 {
		t.Error("DiscOf misread a disc number")
	}
}

// One track flagged as a compilation stays with the rest of its album.
func TestStrayCompilationFlagJoinsAlbum(t *testing.T) {
	albums := albumsOf(
		tags.Track{Path: "1", Artist: "Juice WRLD", Album: "JUICE UNRELEASED"},
		tags.Track{Path: "2", Artist: "Juice WRLD", Album: "JUICE UNRELEASED"},
		tags.Track{Path: "3", Artist: "Juice WRLD", Album: "JUICE UNRELEASED", Compilation: true},
	)
	if len(albums) != 1 || albums[0].Tracks != 3 || albums[0].Artist != "Juice WRLD" {
		t.Fatalf("expected one album of 3 tracks by Juice WRLD: %+v", albums)
	}
}

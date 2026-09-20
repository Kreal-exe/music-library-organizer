package plan_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"musiclibraryorganizer/internal/library"
	"musiclibraryorganizer/internal/plan"
	"musiclibraryorganizer/internal/tags"
)

// A collection with the problems this tool exists to fix: one artist spelled
// three ways, guest credits promoted into the artist field, and a compilation
// whose album-artist turns every track into "Various Artists".
var fixtures = []struct {
	name        string
	codec       []string
	artist      string
	albumArtist string
	album       string
	title       string
	compilation bool
}{
	{"01 lucid.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "3"}, "Juice WRLD", "Various Artists", "Leaks", "Lucid Dreams", true},
	{"02 bandit.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "4"}, "Juice WRLD feat. NBA YoungBoy", "Various Artists", "Leaks", "Bandit", true},
	{"03 righteous.flac", []string{"-c:a", "flac"}, "juice wrld", "Various Artists", "Leaks", "Righteous", true},
	{"04 wishing.m4a", []string{"-c:a", "aac"}, "JUICE WRLD", "", "Legends Never Die", "Wishing Well", false},
	{"05 lie.mp3", []string{"-c:a", "libmp3lame", "-id3v2_version", "3"}, "Eminem feat. Rihanna", "Eminem", "Recovery", "Love The Way You Lie", false},
	{"06 oborona.flac", []string{"-c:a", "flac"}, "Гражданская оборона", "Гражданская Оборона", "Всё идёт по плану", "Всё идёт по плану", false},
}

func buildLibrary(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found, skipping", tool)
		}
	}

	root := t.TempDir()
	for _, f := range fixtures {
		args := []string{
			"-v", "error", "-y",
			"-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", "1",
			"-metadata", "artist=" + f.artist,
			"-metadata", "album=" + f.album,
			"-metadata", "title=" + f.title,
		}
		if f.albumArtist != "" {
			args = append(args, "-metadata", "album_artist="+f.albumArtist)
		}
		if f.compilation {
			args = append(args, "-metadata", "compilation=1")
		}
		args = append(args, f.codec...)
		args = append(args, filepath.Join(root, "Музыка", f.name))

		if err := os.MkdirAll(filepath.Join(root, "Музыка"), 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %s: %v\n%s", f.name, err, out)
		}
	}
	return root
}

func TestEndToEnd(t *testing.T) {
	root := buildLibrary(t)
	ctx := context.Background()

	lib, err := library.Scan(ctx, root, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(lib.Errors) != 0 {
		t.Fatalf("read errors: %+v", lib.Errors)
	}
	if len(lib.Tracks) != len(fixtures) {
		t.Fatalf("read %d tracks, expected %d", len(lib.Tracks), len(fixtures))
	}

	summary := lib.Summarize()
	if summary.CompilationCount != 3 {
		t.Errorf("%d compilations, expected 3", summary.CompilationCount)
	}
	// One fixture deliberately carries no album artist at all.
	if summary.AlbumArtistCount != len(fixtures)-1 {
		t.Errorf("%d with an album artist, expected %d", summary.AlbumArtistCount, len(fixtures)-1)
	}

	// Every spelling and guest credit must collapse onto the artist the music
	// is by, which is the whole point of the reduction.
	var juice, oborona *library.Target
	for i, target := range summary.Targets {
		switch target.Name {
		case "Juice WRLD":
			juice = &summary.Targets[i]
		case "Гражданская оборона", "Гражданская Оборона":
			oborona = &summary.Targets[i]
		}
	}
	if juice == nil {
		t.Fatalf("Juice WRLD is not a target: %+v", summary.Targets)
	}
	if len(juice.Sources) != 4 {
		t.Errorf("%d sources under Juice WRLD, expected 4: %+v", len(juice.Sources), juice.Sources)
	}
	if oborona == nil {
		t.Errorf("the Cyrillic spellings were not reduced: %+v", summary.Targets)
	}

	// Build the plan the interface would: every source renamed onto its target,
	// the album artist following the artist, and the compilation flag cleared.
	rename := map[string]string{}
	for _, target := range summary.Targets {
		for _, source := range target.Sources {
			if source.Value != target.Name {
				rename[source.Value] = target.Name
			}
		}
	}

	changes := plan.Build(lib, plan.Rules{
		Rename:            rename,
		AlbumArtistMode:   plan.AlbumArtistFromArtist,
		RemoveCompilation: true,
	}, nil)
	if len(changes) != len(fixtures) {
		t.Fatalf("%d changes, expected %d", len(changes), len(fixtures))
	}

	if err := plan.Apply(ctx, changes, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Rescan: the library should now hold exactly the artists the user asked
	// for, with no album-artist drift and no compilation flags left.
	after, err := library.Scan(ctx, root, nil)
	if err != nil {
		t.Fatalf("повторное scan: %v", err)
	}
	summary = after.Summarize()

	if summary.CompilationCount != 0 {
		t.Errorf("compilations remain: %d", summary.CompilationCount)
	}

	// Nothing is left to reduce: every target is now a single exact name.
	for _, target := range summary.Targets {
		if len(target.Sources) != 1 || target.Sources[0].Kind != library.KindExact {
			t.Errorf("%q still folds several names: %+v", target.Name, target.Sources)
		}
	}

	got := map[string]int{}
	for _, target := range summary.Targets {
		got[target.Name] = target.Tracks
	}
	if len(got) != 3 {
		t.Errorf("%d artists, expected 3: %+v", len(got), got)
	}
	if got["Juice WRLD"] != 4 {
		t.Errorf("Juice WRLD: %d tracks, expected 4 (full list: %+v)", got["Juice WRLD"], got)
	}
	if got["Eminem"] != 1 {
		t.Errorf("Eminem: %d tracks, expected 1 (full list: %+v)", got["Eminem"], got)
	}
	// Either capitalisation of the Cyrillic name is a fair winner of the tie.
	if got["Гражданская оборона"]+got["Гражданская Оборона"] != 1 {
		t.Errorf("the Cyrillic artist was not reduced to one: %+v", got)
	}

	// Every album artist now matches its track's artist.
	for _, track := range after.Tracks {
		if track.AlbumArtist != track.Artist {
			t.Errorf("%s: album artist %q, artist %q",
				filepath.Base(track.Path), track.AlbumArtist, track.Artist)
		}
		if track.Title == "" {
			t.Errorf("%s: the title was lost", filepath.Base(track.Path))
		}
	}

	// Nothing may have been corrupted along the way.
	for _, track := range after.Tracks {
		out, err := exec.Command("ffmpeg", "-v", "error", "-i", track.Path, "-f", "null", "-").CombinedOutput()
		if err != nil || len(out) > 0 {
			t.Errorf("%s does not decode: %v\n%s", filepath.Base(track.Path), err, out)
		}
	}
}

// Rescanning after a run must find nothing left to do.
func TestPlanIsIdempotent(t *testing.T) {
	root := buildLibrary(t)
	ctx := context.Background()

	rules := plan.Rules{
		AlbumArtistMode:   plan.AlbumArtistClear,
		RemoveCompilation: true,
	}

	for pass := 1; pass <= 2; pass++ {
		lib, err := library.Scan(ctx, root, nil)
		if err != nil {
			t.Fatalf("проход %d, scan: %v", pass, err)
		}

		changes := plan.Build(lib, rules, nil)
		if pass == 2 && len(changes) != 0 {
			t.Fatalf("the second pass found changes: %+v", changes)
		}
		if err := plan.Apply(ctx, changes, nil); err != nil {
			t.Fatalf("проход %d, apply: %v", pass, err)
		}
	}
}

// A file the tool cannot parse must be reported, not silently dropped.
func TestScanReportsUnreadableFiles(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, "broken.mp3")
	if err := os.WriteFile(broken, []byte("this is not an mp3"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	lib, err := library.Scan(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	// A file with no ID3 tag at all reads as an empty track rather than an
	// error, so either outcome is fine — it just must not vanish.
	if len(lib.Tracks)+len(lib.Errors) != 1 {
		t.Errorf("%d tracks, %d errors, expected exactly one result", len(lib.Tracks), len(lib.Errors))
	}
	_ = tags.Supported(broken)
}

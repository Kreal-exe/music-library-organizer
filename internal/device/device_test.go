package device

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"musiclibraryorganizer/internal/tags"
	"musiclibraryorganizer/internal/wire"
)

func TestParseDevices(t *testing.T) {
	// The daemon's own chatter shares the stream with the listing.
	const out = `* daemon not running; starting now at tcp:5037
* daemon started successfully
List of devices attached
S4MPL3000000001        device product:sample_x1 model:Sample_X1 device:samplex transport_id:2
B7XX111                unauthorized transport_id:3
C9YY222                offline transport_id:4
`

	got := parseDevices(out)
	if len(got) != 3 {
		t.Fatalf("%d devices, expected 3: %+v", len(got), got)
	}

	ready := got[0]
	if ready.Serial != "S4MPL3000000001" || !ready.Ready {
		t.Errorf("the ready device was parsed wrong: %+v", ready)
	}
	if ready.Model != "Sample X1" {
		t.Errorf("model = %q", ready.Model)
	}
	if ready.Hint != "" {
		t.Errorf("a ready device must carry no hint: %q", ready.Hint)
	}

	// A phone waiting for its owner to tap "allow" must say so, since that is
	// the whole fix and it happens on the phone, not here.
	if got[1].Ready || !strings.Contains(got[1].Hint, "Allow USB debugging") {
		t.Errorf("unauthorised device: %+v", got[1])
	}
	if got[2].Ready || got[2].Hint == "" {
		t.Errorf("offline device: %+v", got[2])
	}
}

func TestParseDevicesWhenNoneAttached(t *testing.T) {
	got := parseDevices("List of devices attached\n\n")
	if len(got) != 0 {
		t.Errorf("expected an empty list, got %+v", got)
	}
}

// Album folders are full of apostrophes, and a mis-quoted path would send a
// fragment of a filename to the phone as a command.
func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/sdcard/Music":             `'/sdcard/Music'`,
		"/sdcard/Music/Guns N' Ros": `'/sdcard/Music/Guns N'\'' Ros'`,
		"/sdcard/Музыка":            `'/sdcard/Музыка'`,
		"/sdcard/a b; rm -rf /":     `'/sdcard/a b; rm -rf /'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, expected %q", in, got, want)
		}
	}
}

func TestAgentForArchitecture(t *testing.T) {
	for _, abi := range []string{"arm64-v8a", "ARM64-V8A", "aarch64"} {
		if _, err := agentFor(abi); err != nil {
			t.Errorf("agentFor(%q) returned an error: %v", abi, err)
		}
	}
	if _, err := agentFor("armeabi-v7a"); err == nil {
		t.Error("expected a clear error for a 32-bit phone")
	}
}

// The embedded agent has to actually be there: an empty one would only fail
// once a phone was plugged in.
func TestEmbeddedAgentIsPresent(t *testing.T) {
	binary, err := agentFor("arm64-v8a")
	if err != nil {
		t.Fatal(err)
	}
	if len(binary) < 1<<20 {
		t.Fatalf("the embedded agent is suspiciously small: %d bytes", len(binary))
	}
	if !strings.HasPrefix(string(binary[:4]), "\x7fELF") {
		t.Error("the embedded agent does not look like a Linux executable")
	}
}

/* Driving a phone without a phone ----------------------------------------- */

// fakeADB stands in for adb: it answers commands from a script and records
// what it was asked, so the conversation with the phone can be checked.
type fakeADB struct {
	replies map[string]string
	lines   []string // what Stream hands back
	calls   []string
	pushed  map[string]string // what was pushed, by where it went on the phone
}

func (f *fakeADB) Run(_ context.Context, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	f.calls = append(f.calls, joined)

	if len(args) >= 3 && args[len(args)-3] == "push" {
		content, err := os.ReadFile(args[len(args)-2])
		if err != nil {
			return nil, err
		}
		if f.pushed == nil {
			f.pushed = map[string]string{}
		}
		f.pushed[args[len(args)-1]] = string(content)
	}

	for prefix, reply := range f.replies {
		if strings.Contains(joined, prefix) {
			return []byte(reply), nil
		}
	}
	return nil, nil
}

func (f *fakeADB) Stream(_ context.Context, onLine func([]byte) error, args ...string) error {
	f.calls = append(f.calls, strings.Join(args, " "))
	for _, line := range f.lines {
		if err := onLine([]byte(line)); err != nil {
			return err
		}
	}
	return nil
}

// streamThrough feeds a message stream into the handler a Phone would use.
func streamThrough(t *testing.T, lines []string, handle func(wire.Message) error) {
	t.Helper()
	for _, line := range lines {
		trimmed := trimCR([]byte(line))
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		var msg wire.Message
		if err := json.Unmarshal(trimmed, &msg); err != nil {
			continue
		}
		if err := handle(msg); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanStreamCollectsTracksAndFailures(t *testing.T) {
	lines := []string{
		"* daemon started",                    // noise before the stream
		`{"type":"start","done":0,"total":3}`, //
		`{"type":"track","track":{"path":"/sdcard/Music/a.mp3","artist":"Кино","title":"Группа крови","albumArtist":"кино"},"done":0,"total":0}` + "\r",
		`{"type":"error","path":"/sdcard/Music/broken.mp3","error":"чтение ID3: повреждён","done":0,"total":0}`,
		`{"type":"track","track":{"path":"/sdcard/Music/b.flac","artist":"Juice WRLD"},"done":0,"total":0}`,
		"not json at all",
		`{"type":"done","done":3,"total":3}`,
	}

	result := &ScanResult{Tracks: []tags.Track{}, Failed: []Failure{}}
	total := 0
	var progress []Progress

	streamThrough(t, lines, func(msg wire.Message) error {
		switch msg.Type {
		case wire.TypeStart:
			total = msg.Total
		case wire.TypeTrack:
			result.Tracks = append(result.Tracks, *msg.Track)
		case wire.TypeError:
			result.Failed = append(result.Failed, Failure{Path: msg.Path, Reason: msg.Error})
		case wire.TypeDone:
			result.Scanned = msg.Done
			return nil
		}
		progress = append(progress, Progress{Done: len(result.Tracks) + len(result.Failed), Total: total})
		return nil
	})

	if len(result.Tracks) != 2 {
		t.Fatalf("%d tracks, expected 2: %+v", len(result.Tracks), result.Tracks)
	}
	if result.Tracks[0].Artist != "Кино" || result.Tracks[0].AlbumArtist != "кино" {
		t.Errorf("Cyrillic was lost: %+v", result.Tracks[0])
	}
	if len(result.Failed) != 1 || result.Failed[0].Path != "/sdcard/Music/broken.mp3" {
		t.Errorf("the unreadable file was not recorded: %+v", result.Failed)
	}
	if result.Scanned != 3 {
		t.Errorf("Scanned = %d", result.Scanned)
	}
	if len(progress) == 0 || progress[len(progress)-1].Total != 3 {
		t.Errorf("progress never reaches the end: %+v", progress)
	}
}

// A plan built here must survive the trip to the phone and back unchanged:
// this is the exact JSON the agent will read.
func TestPlanSurvivesEncoding(t *testing.T) {
	items := []wire.Item{{
		Path: "/sdcard/Music/Гражданская оборона/01 Всё идёт по плану.mp3",
		Edit: tags.Edit{
			Artist:            tags.Str("Гражданская оборона"),
			AlbumArtist:       tags.Str(""),
			Lyrics:            tags.Str("Первая строка\nSecond line"),
			RemoveCompilation: true,
		},
	}}

	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}

	var decoded []wire.Item
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(items, decoded) {
		t.Fatalf("the plan changed in transit:\n%+v\n%+v", items, decoded)
	}

	// An absent field must stay absent: it means "leave this tag alone", and a
	// zero value would instead erase it.
	if strings.Contains(string(encoded), `"albumArtistSort"`) {
		t.Error("a field nobody set appeared in the plan")
	}
	var raw []map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	edit := raw[0]["edit"].(map[string]any)
	if _, present := edit["removeAlbumArtistSort"]; present {
		t.Error("an unset removal reached the plan")
	}
	if value, present := edit["albumArtist"]; !present || value != "" {
		t.Errorf("clearing the album artist was sent wrong: %v", edit)
	}
}

func TestDevicesUsesRunner(t *testing.T) {
	fake := &fakeADB{replies: map[string]string{
		"devices": "List of devices attached\nSERIAL1\tdevice model:Test_Phone\n",
	}}

	got, err := Devices(context.Background(), fake)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Serial != "SERIAL1" || !got[0].Ready {
		t.Fatalf("devices were parsed wrong: %+v", got)
	}
	if len(fake.calls) != 1 || !strings.Contains(fake.calls[0], "devices -l") {
		t.Errorf("the wrong command was issued: %v", fake.calls)
	}
}

// Editing tags empties a phone's playlists unless they are read again: a
// playlist holds database row numbers, and rewriting a track gives it a new
// row. This is the check that the playlists are not forgotten.
func TestRescanReadsThePlaylistsAgain(t *testing.T) {
	fake := &fakeADB{replies: map[string]string{
		"find": "/sdcard/Music/Favourites.m3u\n/sdcard/Music/Рус.m3u\n",
	}}
	phone := &Phone{adb: fake, Serial: "SERIAL1"}

	phone.Rescan(context.Background(), []string{"/sdcard/Music/a.mp3"}, nil)
	phone.RescanPlaylists(context.Background(), "/sdcard/Music")

	if got := fake.pushed[rescanPath]; got != "/sdcard/Music/a.mp3\x00" {
		t.Errorf("the phone was sent %q to re-read", got)
	}

	want := []string{
		"xargs -0 -P",
		"touch '/sdcard/Music/Favourites.m3u'",
		"scan_file --arg '/sdcard/Music/Favourites.m3u'",
		"touch '/sdcard/Music/Рус.m3u'",
		"scan_file --arg '/sdcard/Music/Рус.m3u'",
	}
	for _, phrase := range want {
		found := false
		for _, call := range fake.calls {
			if strings.Contains(call, phrase) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the phone was never asked to %q\ncalls: %v", phrase, fake.calls)
		}
	}
}

// A collection too large to name file by file is read as a whole, and the
// playlists still follow.
func TestRescanFallsBackToTheWholeVolume(t *testing.T) {
	fake := &fakeADB{}
	phone := &Phone{adb: fake, Serial: "SERIAL1"}

	paths := make([]string, 2500)
	for i := range paths {
		paths[i] = "/sdcard/Music/track.mp3"
	}
	phone.Rescan(context.Background(), paths, nil)
	phone.RescanPlaylists(context.Background(), "/sdcard/Music")

	if len(fake.calls) > 10 {
		t.Errorf("2500 files were scanned one by one: %d calls", len(fake.calls))
	}
	joined := strings.Join(fake.calls, "\n")
	if !strings.Contains(joined, "scan_volume") {
		t.Error("the volume was never read")
	}
	if !strings.Contains(joined, "-iname \"*.m3u\"") {
		t.Error("the playlists were not looked for")
	}
}

// The scanner sometimes reads a playlist while the tracks it names are still
// being written, and then it finds fewer of them than the file lists. The
// count is checked against the file rather than trusted.
func TestReimportStopsWhenThePlaylistIsWhole(t *testing.T) {
	fake := &fakeADB{replies: map[string]string{
		"find":                             "/sdcard/Music/Favourites.m3u\n",
		"grep -vc":                         "3\n",
		"playlists --projection _id:_data": "Row: 0 _id=17162, _data=/sdcard/Music/Favourites.m3u\n",
		"members":                          "Row: 0 audio_id=1\nRow: 1 audio_id=2\nRow: 2 audio_id=3\n",
	}}
	phone := &Phone{adb: fake, Serial: "SERIAL1"}

	phone.RescanPlaylists(context.Background(), "/sdcard/Music")

	if touches := countCalls(fake, "touch"); touches != 1 {
		t.Errorf("a whole playlist was offered to the scanner %d times", touches)
	}
}

// When it keeps coming back short, the retries stop rather than going on for
// ever: a playlist may name a track that is no longer on the phone.
func TestReimportGivesUpOnAPlaylistThatStaysShort(t *testing.T) {
	fake := &fakeADB{replies: map[string]string{
		"find":                             "/sdcard/Music/Favourites.m3u\n",
		"grep -vc":                         "5\n",
		"playlists --projection _id:_data": "Row: 0 _id=17162, _data=/sdcard/Music/Favourites.m3u\n",
		"members":                          "Row: 0 audio_id=1\n",
	}}
	phone := &Phone{adb: fake, Serial: "SERIAL1"}

	phone.RescanPlaylists(context.Background(), "/sdcard/Music")

	touches := countCalls(fake, "touch")
	if touches < 2 || touches > reimportTries {
		t.Errorf("a short playlist was offered %d times, expected between 2 and %d", touches, reimportTries)
	}
}

func countCalls(fake *fakeADB, phrase string) int {
	n := 0
	for _, call := range fake.calls {
		if strings.Contains(call, phrase) {
			n++
		}
	}
	return n
}

// The files are re-read on the phone several at a time from one list, and
// each one done is counted, whatever its name holds.
func TestRescanWorksThroughTheListOnThePhone(t *testing.T) {
	paths := []string{"/sdcard/Music/It's \"Here\" $(now).mp3", "/sdcard/Music/Рус/b.flac"}
	fake := &fakeADB{lines: []string{"scanned", "noise", "scanned\r"}}
	phone := &Phone{adb: fake, Serial: "SERIAL1"}

	var seen []Progress
	phone.Rescan(context.Background(), paths, func(p Progress) { seen = append(seen, p) })

	if got := fake.pushed[rescanPath]; got != paths[0]+"\x00"+paths[1]+"\x00" {
		t.Errorf("the phone was sent %q", got)
	}
	if len(seen) != 2 || seen[1] != (Progress{Done: 2, Total: 2}) {
		t.Errorf("progress = %+v", seen)
	}
	for _, call := range fake.calls {
		if strings.Contains(call, "scan_file --arg '") {
			t.Errorf("a file was re-read on its own: %s", call)
		}
	}
}

// Deleting quotes every name and reports only what is really gone.
func TestDeleteRemovesAndChecks(t *testing.T) {
	fake := &fakeADB{replies: map[string]string{"[ -e '/sdcard/Music/kept.m4a' ]": "yes"}}
	phone := &Phone{adb: fake, Serial: "SERIAL1"}

	gone, err := phone.Delete(context.Background(), []string{"/sdcard/Music/It's bad.m4a", "/sdcard/Music/kept.m4a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 1 || gone[0] != "/sdcard/Music/It's bad.m4a" {
		t.Errorf("gone = %q", gone)
	}
	joined := strings.Join(fake.calls, "\n")
	if !strings.Contains(joined, `rm -f -- '/sdcard/Music/It'\''s bad.m4a' '/sdcard/Music/kept.m4a'`) {
		t.Errorf("rm was not quoted as expected:\n%s", joined)
	}
}

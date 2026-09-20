package device

import (
	"context"
	"encoding/json"
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
}

func (f *fakeADB) Run(_ context.Context, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	f.calls = append(f.calls, joined)

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

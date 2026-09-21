package device

// Deploying and driving the on-phone agent.
//
// The agent binary is carried inside this application and copied to the phone
// on first use. Everything after that is a stream of small JSON messages: the
// audio never leaves the phone.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"musiclibraryorganizer/internal/tags"
	"musiclibraryorganizer/internal/wire"
)

// Where the agent lives on the phone. /data/local/tmp is the one place the adb
// shell user may both write to and execute from.
const (
	agentPath  = "/data/local/tmp/mlm-agent"
	planPath   = "/data/local/tmp/mlm-plan.json"
	rescanPath = "/data/local/tmp/mlm-rescan.txt"
)

// How many files the phone is asked to re-read at once. Each request starts a
// program on the phone that takes about a second to answer, nearly all of it
// spent starting up, so one at a time made a job of 1222 files wait twenty
// minutes; eight at once take a sixth of that.
const rescanJobs = 8

// ErrNoAgent means no agent build is available for this phone.
var ErrNoAgent = errors.New("no agent build for this phone")

// Streamer runs adb and hands back its output line by line, which is what lets
// progress appear while a scan of thousands of files is still running.
type Streamer interface {
	Stream(ctx context.Context, onLine func([]byte) error, args ...string) error
}

func (a ADB) Stream(ctx context.Context, onLine func([]byte) error, args ...string) error {
	cmd := exec.CommandContext(ctx, a.Path, args...)
	hideConsole(cmd)

	var stderr strings.Builder
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	// A line carries one track's tags; the default limit is generous already,
	// but an unusually long comment field should not end the scan.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var lineErr error
	for scanner.Scan() {
		if lineErr = onLine(scanner.Bytes()); lineErr != nil {
			break
		}
	}

	if lineErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return lineErr
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}

	if err := cmd.Wait(); err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return fmt.Errorf("adb: %s", message)
		}
		return err
	}
	return nil
}

// conn is everything a Phone asks of adb: one-shot commands, and one whose
// output is read as it arrives. Naming it makes the conversation with the
// phone something a test can stand in for.
type conn interface {
	Runner
	Streamer
}

// Phone is a connected device with the agent in place.
type Phone struct {
	adb    conn
	Serial string
}

// Connect makes sure the phone is carrying the current agent.
func Connect(ctx context.Context, adb ADB, serial string) (*Phone, error) {
	phone := &Phone{adb: adb, Serial: serial}

	abi, err := phone.property(ctx, "ro.product.cpu.abi")
	if err != nil {
		return nil, err
	}

	binary, err := agentFor(abi)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoAgent, abi)
	}

	// The build already on the phone decides whether anything is copied, so
	// the usual case costs one quick command. An agent from an earlier release
	// reports a different build and is replaced, which is what keeps the phone
	// from reading tags with last month's code.
	if out, err := phone.run(ctx, "shell", agentPath+" version"); err == nil {
		if strings.TrimSpace(strings.ReplaceAll(string(out), "\r", "")) == wire.Build(binary) {
			return phone, nil
		}
	}
	if err := phone.deploy(ctx, binary); err != nil {
		return nil, err
	}
	return phone, nil
}

func (p *Phone) run(ctx context.Context, args ...string) ([]byte, error) {
	return p.adb.Run(ctx, append([]string{"-s", p.Serial}, args...)...)
}

func (p *Phone) property(ctx context.Context, name string) (string, error) {
	out, err := p.run(ctx, "shell", "getprop "+name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.ReplaceAll(string(out), "\r", "")), nil
}

func (p *Phone) deploy(ctx context.Context, binary []byte) error {
	local, err := os.CreateTemp("", "mlm-agent-*")
	if err != nil {
		return err
	}
	defer os.Remove(local.Name())

	if _, err := local.Write(binary); err != nil {
		local.Close()
		return err
	}
	if err := local.Close(); err != nil {
		return err
	}

	if _, err := p.run(ctx, "push", local.Name(), agentPath); err != nil {
		return fmt.Errorf("could not copy the agent to the phone: %w", err)
	}
	if _, err := p.run(ctx, "shell", "chmod 755 "+agentPath); err != nil {
		return fmt.Errorf("could not make the agent runnable on the phone: %w", err)
	}
	return nil
}

// HasFolder reports whether a folder exists on the phone.
func (p *Phone) HasFolder(ctx context.Context, dir string) bool {
	out, err := p.run(ctx, "shell", "[ -d "+shellQuote(dir)+" ] && echo yes || echo no")
	return err == nil && strings.Contains(string(out), "yes")
}

// Folders lists the immediate subfolders of a folder on the phone, so the
// interface can offer somewhere to start. Only folders are listed: a music
// folder is full of playlists and artwork, and none of them is somewhere to
// navigate into.
func (p *Phone) Folders(ctx context.Context, dir string) ([]string, error) {
	out, err := p.run(ctx, "shell",
		"find "+shellQuote(dir)+" -mindepth 1 -maxdepth 1 -type d 2>/dev/null")
	if err != nil {
		return nil, err
	}

	prefix := strings.TrimSuffix(dir, "/") + "/"
	names := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		path := strings.TrimSpace(line)
		if path == "" {
			continue
		}
		if name := strings.TrimPrefix(path, prefix); name != "" && !strings.HasPrefix(name, ".") {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	return names, nil
}

// Progress reports how far a scan or a write has got.
type Progress struct {
	Done  int
	Total int
}

// ScanResult is what reading a phone folder produced.
type ScanResult struct {
	Tracks  []tags.Track
	Failed  []Failure
	Scanned int
}

// Failure is one file the agent could not read or write.
type Failure struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Scan reads the tags of every supported file under a folder on the phone.
func (p *Phone) Scan(ctx context.Context, dir string, report func(Progress)) (*ScanResult, error) {
	result := &ScanResult{Tracks: []tags.Track{}, Failed: []Failure{}}
	total := 0

	err := p.stream(ctx, func(msg wire.Message) error {
		switch msg.Type {
		case wire.TypeStart:
			total = msg.Total
		case wire.TypeTrack:
			result.Tracks = append(result.Tracks, *msg.Track)
		case wire.TypeError:
			if msg.Path == "" {
				return errors.New(msg.Error)
			}
			result.Failed = append(result.Failed, Failure{Path: msg.Path, Reason: msg.Error})
		case wire.TypeDone:
			result.Scanned = msg.Done
			return nil
		}

		if report != nil {
			report(Progress{Done: len(result.Tracks) + len(result.Failed), Total: total})
		}
		return nil
	}, agentPath+" scan "+shellQuote(dir))

	if err != nil {
		return nil, err
	}
	return result, nil
}

// Written is one file the agent finished with.
type Written struct {
	Path  string
	Error string
}

// Apply hands the plan to the phone and writes the tags there.
func (p *Phone) Apply(ctx context.Context, items []wire.Item, report func(Progress), onFile func(Written)) error {
	if len(items) == 0 {
		return nil
	}

	encoded, err := json.Marshal(items)
	if err != nil {
		return err
	}

	local := filepath.Join(os.TempDir(), "mlm-plan.json")
	if err := os.WriteFile(local, encoded, 0o600); err != nil {
		return err
	}
	defer os.Remove(local)

	if _, err := p.run(ctx, "push", local, planPath); err != nil {
		return fmt.Errorf("could not send the plan to the phone: %w", err)
	}
	// The plan carries lyrics and artist names; it has no business staying on
	// the phone once it has been used.
	defer func() { _, _ = p.run(context.WithoutCancel(ctx), "shell", "rm -f "+planPath) }()

	done, total := 0, len(items)
	return p.stream(ctx, func(msg wire.Message) error {
		switch msg.Type {
		case wire.TypeStart:
			total = msg.Total
		case wire.TypeWritten, wire.TypeError:
			if msg.Type == wire.TypeError && msg.Path == "" {
				return errors.New(msg.Error)
			}
			done++
			if onFile != nil {
				onFile(Written{Path: msg.Path, Error: msg.Error})
			}
			if report != nil {
				report(Progress{Done: done, Total: total})
			}
		case wire.TypeDone:
			if report != nil {
				report(Progress{Done: msg.Done, Total: msg.Total})
			}
		}
		return nil
	}, agentPath+" apply "+planPath)
}

// stream runs one agent command and decodes its message stream.
func (p *Phone) stream(ctx context.Context, handle func(wire.Message) error, command string) error {
	// exec-out passes bytes through untouched; a plain shell would rewrite the
	// line endings and break the JSON stream.
	return p.adb.Stream(ctx, func(line []byte) error {
		line = trimCR(line)
		if len(line) == 0 || line[0] != '{' {
			return nil // Noise from the shell, not a message.
		}

		var msg wire.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			return nil // A malformed line is not worth ending the run over.
		}
		return handle(msg)
	}, "-s", p.Serial, "exec-out", command)
}

func trimCR(line []byte) []byte {
	for len(line) > 0 && (line[len(line)-1] == '\r' || line[len(line)-1] == '\n') {
		line = line[:len(line)-1]
	}
	return line
}

// Rescan asks the phone's media database to re-read the files that changed, so
// a player shows the new tags without the user clearing its cache.
//
// It says nothing about playlists: those are put right once, by
// RescanPlaylists, when the whole job is over. Doing it after every batch of
// files costs half a minute each time and is undone by the next batch anyway.
//
// The list of files goes to the phone in one piece and is worked through there,
// several files at a time, with a line coming back as each one is done, which
// is what report counts.
func (p *Phone) Rescan(ctx context.Context, paths []string, report func(Progress)) {
	const maxFiles = 2000

	if len(paths) > maxFiles {
		// Too many to name one by one, so the whole volume is read instead.
		_, _ = p.run(ctx, "shell",
			"content call --uri content://media --method scan_volume --arg external_primary")
		return
	}
	if len(paths) == 0 {
		return
	}

	if err := p.rescanTogether(ctx, paths, report); err == nil || ctx.Err() != nil {
		return
	}

	// The list could not be sent or worked through, so the files are asked
	// for one by one, which is slow but needs nothing on the phone.
	for i, path := range paths {
		if ctx.Err() != nil {
			return
		}
		p.scanFile(ctx, path)
		if report != nil {
			report(Progress{Done: i + 1, Total: len(paths)})
		}
	}
}

// rescanTogether sends the list and has the phone re-read it rescanJobs files
// at a time. The paths travel separated by NUL bytes and reach the command as
// a single argument each, so no name needs quoting however it is spelled.
func (p *Phone) rescanTogether(ctx context.Context, paths []string, report func(Progress)) error {
	local := filepath.Join(os.TempDir(), "mlm-rescan.txt")
	if err := os.WriteFile(local, []byte(strings.Join(paths, "\x00")+"\x00"), 0o600); err != nil {
		return err
	}
	defer os.Remove(local)

	if _, err := p.run(ctx, "push", local, rescanPath); err != nil {
		return err
	}
	defer func() { _, _ = p.run(context.WithoutCancel(ctx), "shell", "rm -f "+rescanPath) }()

	command := fmt.Sprintf("xargs -0 -P %d -n 1 sh -c "+
		`'content call --uri content://media/external --method scan_file --arg "$0" >/dev/null 2>&1; echo scanned'`+
		" < %s", rescanJobs, rescanPath)

	done := 0
	return p.adb.Stream(ctx, func(line []byte) error {
		if string(trimCR(line)) != "scanned" {
			return nil
		}
		done++
		if report != nil {
			report(Progress{Done: done, Total: len(paths)})
		}
		return nil
	}, "-s", p.Serial, "exec-out", command)
}

// Delete removes files from the phone and has the media database forget
// them. It reports the paths that are gone afterwards, which is what the
// interface trusts rather than the command's exit status.
func (p *Phone) Delete(ctx context.Context, paths []string) ([]string, error) {
	const perCommand = 50 // A shell line has a length limit.

	for start := 0; start < len(paths); start += perCommand {
		chunk := paths[start:min(start+perCommand, len(paths))]
		quoted := make([]string, len(chunk))
		for i, path := range chunk {
			quoted[i] = shellQuote(path)
		}
		if _, err := p.run(ctx, "shell", "rm -f -- "+strings.Join(quoted, " ")); err != nil {
			return nil, err
		}
	}

	var gone []string
	for _, path := range paths {
		if !p.exists(ctx, path) {
			gone = append(gone, path)
		}
	}
	// A scan of a file that is no longer there drops it from the database.
	p.Rescan(ctx, gone, nil)
	return gone, nil
}

// exists says whether a file is on the phone.
func (p *Phone) exists(ctx context.Context, path string) bool {
	out, err := p.run(ctx, "shell", "[ -e "+shellQuote(path)+" ] && echo yes")
	return err == nil && strings.TrimSpace(string(out)) == "yes"
}

// RescanPlaylists asks the phone to read its playlist files again.
//
// This is not housekeeping. A playlist on Android is a list of database row
// numbers, not of file names, and rewriting a track's tags gives that track a
// new row — so every playlist that pointed at the old one silently loses the
// entry. On a real phone that emptied six playlists of about eleven hundred
// tracks between them.
//
// The playlist files on disk are untouched by any of this, so having the
// scanner read them again puts every entry back. It skips a file whose
// timestamp it has already seen, which is why each one is touched first.
func (p *Phone) RescanPlaylists(ctx context.Context, root string) {
	if root == "" || ctx.Err() != nil {
		return
	}

	out, err := p.run(ctx, "shell", "find "+shellQuote(root)+
		` -maxdepth 4 \( -iname "*.m3u" -o -iname "*.m3u8" -o -iname "*.pls" \) 2>/dev/null`)
	if err != nil {
		return
	}

	// The writes have only just gone in, and reading a playlist replaces its
	// contents with whatever resolves at that moment — so a moment is given to
	// the indexing before any of that is read.
	if !pause(ctx, reimportPause) {
		return
	}

	// One listing of every playlist the database holds, rather than a lookup
	// per attempt: each round trip to the phone costs about half a second.
	known := p.playlistIDs(ctx)

	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		playlist := strings.TrimSpace(line)
		if playlist == "" {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		p.reimport(ctx, playlist, known[playlist])
	}
}

// playlistIDs maps each playlist file the database knows to its row number.
func (p *Phone) playlistIDs(ctx context.Context) map[string]string {
	out, err := p.run(ctx, "shell",
		"content query --uri content://media/external/audio/playlists --projection _id:_data")
	if err != nil {
		return nil
	}

	ids := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		match := playlistRow.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}

		// The database stores the real path; the search above finds the same
		// file through /sdcard, which is a link to it.
		path := strings.TrimSpace(match[2])
		ids[path] = match[1]
		ids[strings.Replace(path, "/storage/emulated/0", "/sdcard", 1)] = match[1]
	}
	return ids
}

var playlistRow = regexp.MustCompile(`_id=(\d+), _data=(.+?)\s*$`)

// How many times one playlist is offered to the scanner before giving up, and
// how long to wait before trying again.
//
// A scan can land while the tracks it is looking for are still being indexed,
// and then it finds fewer of them than the file lists — and, because reading a
// playlist replaces its contents with whatever resolved, such a scan takes
// entries away rather than putting them back. The pause is what lets the
// writes settle first.
const (
	reimportTries = 3
	reimportPause = 2 * time.Second
)

// reimport has the scanner read one playlist file, and checks that it worked.
//
// The count is compared against the file rather than trusted, because the
// failure this guards against is silent: the file keeps every line either way,
// and only the database comes back short. A playlist can legitimately list a
// track that is no longer on the phone, so the test is not that the counts
// match — it is that another attempt stops finding more.
func (p *Phone) reimport(ctx context.Context, playlist, id string) {
	listed := p.countEntries(ctx, playlist)
	best := -1

	for attempt := range reimportTries {
		if attempt > 0 && !pause(ctx, reimportPause) {
			return
		}
		if ctx.Err() != nil {
			return
		}

		_, _ = p.run(ctx, "shell", "touch "+shellQuote(playlist))
		p.scanFile(ctx, playlist)

		if id == "" {
			return // Not a playlist the database holds; nothing to check.
		}
		got := p.countMembers(ctx, id)
		if got >= listed || got <= best {
			return
		}
		best = got
	}
}

// pause waits, and reports whether the wait finished rather than the job
// being called off.
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// countEntries is how many tracks a playlist file names.
func (p *Phone) countEntries(ctx context.Context, playlist string) int {
	out, err := p.run(ctx, "shell", `grep -vc "^#" `+shellQuote(playlist)+" 2>/dev/null")
	if err != nil {
		return 0
	}
	return number(out)
}

// countMembers is how many of them the database currently holds.
func (p *Phone) countMembers(ctx context.Context, id string) int {
	members, err := p.run(ctx, "shell",
		"content query --uri content://media/external/audio/playlists/"+id+
			"/members --projection audio_id")
	if err != nil {
		return 0
	}
	return bytes.Count(members, []byte("Row:"))
}

// number reads the first whole number out of a command's output.
func number(out []byte) int {
	match := firstNumber.FindSubmatch(out)
	if match == nil {
		return 0
	}
	n, err := strconv.Atoi(string(match[1]))
	if err != nil {
		return 0
	}
	return n
}

var firstNumber = regexp.MustCompile(`(\d+)`)

// scanFile tells the media database to read one file again.
func (p *Phone) scanFile(ctx context.Context, path string) {
	_, _ = p.run(ctx, "shell",
		"content call --uri content://media/external --method scan_file --arg "+shellQuote(path))
}

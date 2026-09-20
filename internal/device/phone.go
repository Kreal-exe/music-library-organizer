package device

// Deploying and driving the on-phone agent.
//
// The agent binary is carried inside this application and copied to the phone
// on first use. Everything after that is a stream of small JSON messages: the
// audio never leaves the phone.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"musiclibraryorganizer/internal/tags"
	"musiclibraryorganizer/internal/wire"
)

// Where the agent lives on the phone. /data/local/tmp is the one place the adb
// shell user may both write to and execute from.
const (
	agentPath = "/data/local/tmp/mlm-agent"
	planPath  = "/data/local/tmp/mlm-plan.json"
)

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
func (p *Phone) Rescan(ctx context.Context, root string, paths []string) {
	const maxFiles = 2000

	if len(paths) > maxFiles {
		// Too many to name one by one, so the whole volume is read instead.
		_, _ = p.run(ctx, "shell",
			"content call --uri content://media --method scan_volume --arg external_primary")
	} else {
		for _, path := range paths {
			if ctx.Err() != nil {
				return
			}
			p.scanFile(ctx, path)
		}
	}

	p.rescanPlaylists(ctx, root)
}

// rescanPlaylists asks the phone to read its playlist files again.
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
func (p *Phone) rescanPlaylists(ctx context.Context, root string) {
	if root == "" || ctx.Err() != nil {
		return
	}

	out, err := p.run(ctx, "shell", "find "+shellQuote(root)+
		` -maxdepth 4 \( -iname "*.m3u" -o -iname "*.m3u8" -o -iname "*.pls" \) 2>/dev/null`)
	if err != nil {
		return
	}

	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		playlist := strings.TrimSpace(line)
		if playlist == "" {
			continue
		}
		if ctx.Err() != nil {
			return
		}

		_, _ = p.run(ctx, "shell", "touch "+shellQuote(playlist))
		p.scanFile(ctx, playlist)
	}
}

// scanFile tells the media database to read one file again.
func (p *Phone) scanFile(ctx context.Context, path string) {
	_, _ = p.run(ctx, "shell",
		"content call --uri content://media/external --method scan_file --arg "+shellQuote(path))
}

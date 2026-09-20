// Package device talks to an Android phone over adb.
//
// A phone plugged in as a media device appears in the file manager, but MTP is
// not a filesystem: those folders have no path an ordinary program can open.
// adb is Android's own protocol and does give real file access, so it is what
// this tool uses to read a collection off a phone and write the fixed tags
// back.
package device

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrNoADB means the adb executable could not be found.
var ErrNoADB = errors.New("adb not found")

// Runner executes adb. The real one shells out; tests supply their own.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// ADB is a located adb executable.
type ADB struct {
	Path string
}

func (a ADB) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, a.Path, args...)
	hideConsole(cmd)

	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return out, fmt.Errorf("adb %s: %s", args[0], message)
		}
		return out, fmt.Errorf("adb %s: %w", args[0], err)
	}
	return out, nil
}

// Find locates adb: on PATH first, then where the usual installers put it.
func Find() (ADB, error) {
	if path, err := exec.LookPath("adb"); err == nil {
		return ADB{Path: path}, nil
	}

	for _, candidate := range searchPaths() {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return ADB{Path: candidate}, nil
		}
	}
	return ADB{}, ErrNoADB
}

func searchPaths() []string {
	name := "adb"
	if runtime.GOOS == "windows" {
		name = "adb.exe"
	}

	home, _ := os.UserHomeDir()
	var roots []string

	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		roots = append(roots,
			filepath.Join(local, "Android", "Sdk", "platform-tools"),
			filepath.Join(local, "Microsoft", "WinGet", "Packages",
				"Google.PlatformTools_Microsoft.Winget.Source_8wekyb3d8bbwe", "platform-tools"),
			filepath.Join(os.Getenv("ProgramFiles"), "platform-tools"),
		)
	case "darwin":
		roots = append(roots,
			filepath.Join(home, "Library", "Android", "sdk", "platform-tools"),
			"/opt/homebrew/bin",
			"/usr/local/bin",
		)
	default:
		roots = append(roots,
			filepath.Join(home, "Android", "Sdk", "platform-tools"),
			"/usr/local/bin",
			"/usr/bin",
		)
	}

	if sdk := os.Getenv("ANDROID_HOME"); sdk != "" {
		roots = append([]string{filepath.Join(sdk, "platform-tools")}, roots...)
	}

	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		paths = append(paths, filepath.Join(root, name))
	}
	return paths
}

// Device is one attached phone.
type Device struct {
	Serial string `json:"serial"`
	State  string `json:"state"`
	Model  string `json:"model"`

	// Ready is true only when the phone will actually accept commands.
	Ready bool `json:"ready"`
	// Hint explains what to do when it will not.
	Hint string `json:"hint,omitempty"`
}

// Devices lists the attached phones.
func Devices(ctx context.Context, r Runner) ([]Device, error) {
	out, err := r.Run(ctx, "devices", "-l")
	if err != nil {
		return nil, err
	}
	return parseDevices(string(out)), nil
}

func parseDevices(out string) []Device {
	devices := []Device{}

	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "*") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		device := Device{Serial: fields[0], State: fields[1]}
		for _, field := range fields[2:] {
			if model, found := strings.CutPrefix(field, "model:"); found {
				device.Model = strings.ReplaceAll(model, "_", " ")
			}
		}

		switch device.State {
		case "device":
			device.Ready = true
		case "unauthorized":
			device.Hint = "tap \"Allow USB debugging\" on the phone"
		case "offline":
			device.Hint = "the phone is not responding — reconnect the cable"
		default:
			device.Hint = "phone is connected in " + device.State
		}
		devices = append(devices, device)
	}

	return devices
}

// shellQuote wraps a path for the phone's shell, which is plain sh. Album
// folders are full of apostrophes, so closing the quote, escaping the
// apostrophe and reopening is the whole job.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

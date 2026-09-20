// Command agent is the part of this tool that runs on the phone.
//
// A phone collection is tens of gigabytes, and copying it to the computer and
// back to change a few hundred bytes of tags per file would move all of it
// twice. Instead this small static binary is pushed to the phone over adb and
// does the reading and writing there, so only the tags themselves travel.
//
// It speaks newline-delimited JSON on stdout: one object per file as it goes,
// which is what lets the desktop side show progress live.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"musiclibraryorganizer/internal/tags"
	"musiclibraryorganizer/internal/wire"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()

	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)

	switch os.Args[1] {
	case "version":
		fmt.Println(wire.SelfBuild())
	case "scan":
		requireArg(2, "scan <folder>")
		scan(encoder, out, os.Args[2])
	case "apply":
		requireArg(2, "apply <plan.json>")
		apply(encoder, out, os.Args[2])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: agent version | scan <folder> | apply <plan.json>")
	os.Exit(2)
}

func requireArg(index int, form string) {
	if len(os.Args) <= index {
		fmt.Fprintln(os.Stderr, "usage: agent "+form)
		os.Exit(2)
	}
}

// flushEvery keeps the stream moving without a syscall per file.
const flushEvery = 64

func scan(encoder *json.Encoder, out *bufio.Writer, root string) {
	paths := collect(root)

	_ = encoder.Encode(wire.Message{Type: wire.TypeStart, Total: len(paths)})
	out.Flush()

	for i, path := range paths {
		track, err := tags.Read(path)
		if err != nil {
			_ = encoder.Encode(wire.Message{Type: wire.TypeError, Path: path, Error: err.Error()})
		} else {
			_ = encoder.Encode(wire.Message{Type: wire.TypeTrack, Track: &track})
		}
		if i%flushEvery == 0 {
			out.Flush()
		}
	}

	_ = encoder.Encode(wire.Message{Type: wire.TypeDone, Done: len(paths), Total: len(paths)})
	out.Flush()
}

func apply(encoder *json.Encoder, out *bufio.Writer, planPath string) {
	raw, err := os.ReadFile(planPath)
	if err != nil {
		_ = encoder.Encode(wire.Message{Type: wire.TypeError, Error: err.Error()})
		out.Flush()
		os.Exit(1)
	}

	var plan []wire.Item
	if err := json.Unmarshal(raw, &plan); err != nil {
		_ = encoder.Encode(wire.Message{Type: wire.TypeError, Error: "could not read the plan: " + err.Error()})
		out.Flush()
		os.Exit(1)
	}

	_ = encoder.Encode(wire.Message{Type: wire.TypeStart, Total: len(plan)})
	out.Flush()

	for i, entry := range plan {
		result := wire.Message{Type: wire.TypeWritten, Path: entry.Path}
		if err := tags.Write(entry.Path, entry.Edit); err != nil {
			result.Type, result.Error = wire.TypeError, err.Error()
		}
		_ = encoder.Encode(result)
		if i%flushEvery == 0 {
			out.Flush()
		}
	}

	_ = encoder.Encode(wire.Message{Type: wire.TypeDone, Done: len(plan), Total: len(plan)})
	out.Flush()
}

// collect lists the supported files under root, skipping the copies this tool
// leaves behind and the bookkeeping folders players scatter around.
func collect(root string) []string {
	var paths []string

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // An unreadable folder is skipped, not fatal.
		}
		if d.IsDir() {
			// The chosen folder is scanned whatever it is called; the skip
			// list only keeps the walk out of folders below it.
			if path != root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".bak") || strings.HasSuffix(path, ".mlm-tmp") {
			return nil
		}
		if tags.Supported(path) {
			paths = append(paths, path)
		}
		return nil
	})

	return paths
}

func skipDir(name string) bool {
	switch name {
	case "Android", "LOST.DIR":
		return true
	}
	return strings.HasPrefix(name, ".")
}

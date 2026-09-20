// Package library scans a folder of music and aggregates it the way a player
// does: not as a list of files, but as the set of artist names those files add
// up to. That set is what the user edits.
package library

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"musiclibraryorganizer/internal/tags"
)

// Library is the result of one scan.
type Library struct {
	Root   string       `json:"root"`
	Tracks []tags.Track `json:"tracks"`
	Errors []Failure    `json:"errors"`
}

// Failure records a file that could not be read, so the user is told rather
// than silently given an incomplete picture.
type Failure struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Progress reports how far a scan has got.
type Progress struct {
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Current string `json:"current"`
}

// Scan reads every supported file under root. Reading tags is dominated by
// per-file seeks, so files are read in parallel.
func Scan(ctx context.Context, root string, report func(Progress)) (*Library, error) {
	paths, err := collect(ctx, root)
	if err != nil {
		return nil, err
	}

	lib := &Library{
		Root:   root,
		Tracks: make([]tags.Track, 0, len(paths)),
		Errors: []Failure{},
	}
	if len(paths) == 0 {
		return lib, nil
	}

	workers := min(runtime.NumCPU(), 8)

	var (
		mu    sync.Mutex
		done  int
		wg    sync.WaitGroup
		queue = make(chan string)
	)

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range queue {
				track, err := tags.Read(path)

				mu.Lock()
				if err != nil {
					lib.Errors = append(lib.Errors, Failure{Path: path, Reason: err.Error()})
				} else {
					lib.Tracks = append(lib.Tracks, track)
				}
				done++
				if report != nil {
					report(Progress{Done: done, Total: len(paths), Current: path})
				}
				mu.Unlock()
			}
		}()
	}

	for _, path := range paths {
		select {
		case <-ctx.Done():
			close(queue)
			wg.Wait()
			return lib, ctx.Err()
		case queue <- path:
		}
	}
	close(queue)
	wg.Wait()

	// Workers finish out of order; a stable order keeps the interface from
	// reshuffling between scans.
	sort.Slice(lib.Tracks, func(i, j int) bool { return lib.Tracks[i].Path < lib.Tracks[j].Path })
	sort.Slice(lib.Errors, func(i, j int) bool { return lib.Errors[i].Path < lib.Errors[j].Path })

	return lib, nil
}

// collect walks the tree and lists the files worth reading.
func collect(ctx context.Context, root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("the chosen path is not a folder")
	}

	var paths []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // An unreadable folder is skipped, not fatal.
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			// The chosen folder is scanned whatever it is called; the skip
			// list only keeps the walk out of folders below it.
			if path != root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		// Backups this tool wrote are copies of tracks already in the list.
		if strings.HasSuffix(path, ".bak") || strings.HasSuffix(path, ".mlm-tmp") {
			return nil
		}
		if tags.Supported(path) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(paths)
	return paths, nil
}

func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "$RECYCLE.BIN", "System Volume Information":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// FromTracks builds a library out of tags read somewhere other than this
// computer's own disk — on a phone, for instance — so everything downstream
// works the same whichever the collection came from.
func FromTracks(root string, tracks []tags.Track, failures []Failure) *Library {
	if tracks == nil {
		tracks = []tags.Track{}
	}
	if failures == nil {
		failures = []Failure{}
	}

	sort.Slice(tracks, func(i, j int) bool { return tracks[i].Path < tracks[j].Path })
	sort.Slice(failures, func(i, j int) bool { return failures[i].Path < failures[j].Path })

	return &Library{Root: root, Tracks: tracks, Errors: failures}
}

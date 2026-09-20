//go:build !windows && !darwin

package main

import (
	"os/exec"
	"path/filepath"
)

// reveal opens the containing folder, which is as close as most Linux file
// managers get to selecting a file.
func reveal(path string) error {
	return exec.Command("xdg-open", filepath.Dir(path)).Start()
}

//go:build darwin

package main

import "os/exec"

// reveal opens Finder with the file selected.
func reveal(path string) error {
	return exec.Command("open", "-R", path).Run()
}

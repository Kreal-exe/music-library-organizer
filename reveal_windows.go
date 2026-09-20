//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// reveal opens Explorer with the file selected.
func reveal(path string) error {
	cmd := exec.Command("explorer", "/select,"+path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	// Explorer reports a non-zero status even when it worked, so the error is
	// not worth passing on.
	_ = cmd.Run()
	return nil
}

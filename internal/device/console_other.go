//go:build !windows

package device

import "os/exec"

// hideConsole has nothing to do outside Windows, where spawning a process does
// not create a window.
func hideConsole(*exec.Cmd) {}

//go:build windows

package device

import (
	"os/exec"
	"syscall"
)

// hideConsole keeps adb from flashing a console window on screen. A scan makes
// many calls, and without this each one blinks over the interface.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

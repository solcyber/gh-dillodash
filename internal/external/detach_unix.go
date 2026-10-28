//go:build !windows

package external

import (
	"os/exec"
	"syscall"
)

// detach puts the child in its own session so the terminal's signals
// (and its death) never reach the browser.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

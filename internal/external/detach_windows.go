//go:build windows

package external

import "os/exec"

// detach is a no-op on Windows: `cmd /c start` returns at once.
func detach(cmd *exec.Cmd) {}

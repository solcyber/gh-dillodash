//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
)

// execSelf runs argv in this console and exits with its code: Windows
// cannot replace a running process.
var execSelf = func(path string, argv, env []string) error {
	cmd := exec.Command(path, argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, os.Stdout, os.Stderr, env
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

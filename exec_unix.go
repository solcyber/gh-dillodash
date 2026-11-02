//go:build !windows

package main

import "syscall"

// execSelf replaces this process with argv: the restart into a new
// release keeps the terminal and the process's place in it.
var execSelf = func(path string, argv, env []string) error {
	return syscall.Exec(path, argv, env)
}

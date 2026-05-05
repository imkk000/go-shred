//go:build darwin

package main

import (
	"os"
	"syscall"
)

func fullSync(f *os.File) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_FULLFSYNC, 0); errno != 0 {
		return f.Sync()
	}
	return nil
}

//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package main

import (
	"os"
	"syscall"
)

func lockRoomDirectory(dir *os.File) error {
	return syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

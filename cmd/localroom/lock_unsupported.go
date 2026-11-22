//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package main

import (
	"errors"
	"os"
)

func lockRoomDirectory(*os.File) error {
	return errors.New("local room requires operating-system file locking")
}

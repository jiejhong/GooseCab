//go:build !linux

package main

import (
	"errors"
	"net"
)

func listenDemoWriteSocket(string, uint32) (net.Listener, error) {
	return nil, errors.New("demo writer requires Linux Unix socket peer credentials")
}

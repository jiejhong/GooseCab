//go:build linux

package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

type ingressListener struct {
	*net.UnixListener
	uid uint32
}

func listenDemoWriteSocket(path string, uid uint32) (net.Listener, error) {
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0750 {
		return nil, errors.New("demo writer socket requires a dedicated 0750 directory")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("demo writer socket directory must be Gateway-owned")
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil || canonical != dir {
		return nil, errors.New("demo writer socket directory must not resolve through symbolic links")
	}
	raw, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err = os.Chown(path, -1, int(owner.Gid)); err == nil {
		err = os.Chmod(path, 0660)
	}
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return &ingressListener{UnixListener: raw, uid: uid}, nil
}

func (l *ingressListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		if ingressPeerUID(conn, l.uid) {
			return conn, nil
		}
		_ = conn.Close()
	}
}

func ingressPeerUID(conn *net.UnixConn, uid uint32) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var credentials *syscall.Ucred
	var peerErr error
	err = raw.Control(func(fd uintptr) {
		credentials, peerErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	return err == nil && peerErr == nil && credentials != nil && credentials.Uid == uid
}

package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jiejhong/GooseCab/internal/gateway"
)

func samePortTLS(config *gateway.RoomChatConfig, listenAddr, certFile, keyFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" {
		return nil, nil
	}
	if config == nil || config.TrustLoopbackProxy || certFile == "" || keyFile == "" {
		return nil, errors.New("same-port TLS requires both certificate files and a protected room policy without proxy trust")
	}
	origin, err := url.Parse(config.Origin)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" {
		return nil, errors.New("same-port TLS requires an HTTPS room origin")
	}
	_, listened, err := net.SplitHostPort(listenAddr)
	port, portErr := strconv.Atoi(listened)
	external := origin.Port()
	if external == "" {
		external = "443"
	}
	if err != nil || portErr != nil || port < 1 || port > 65535 || strconv.Itoa(port) != listened || external != listened {
		return nil, errors.New("same-port TLS origin must use the existing listener port")
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, errors.New("could not load the same-port TLS certificate and key")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	now := time.Now()
	if err != nil || leaf.VerifyHostname(origin.Hostname()) != nil ||
		now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("same-port TLS certificate must be current and match the room origin")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{pair},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	}, nil
}

const (
	prefaceTimeout = 5 * time.Second
	prefaceLimit   = 128
)

type acceptResult struct {
	conn net.Conn
	err  error
}

// samePortListener preserves the HTTP stream and gives net/http a real
// *tls.Conn for TLS. Separate bounded preface readers keep one idle connection
// from blocking an ordinary HTTP or WebSocket client at Accept.
type samePortListener struct {
	net.Listener
	tlsConfig *tls.Config
	ready     chan acceptResult
	done      chan struct{}
	pending   chan struct{}
	closeOnce sync.Once
}

func newSamePortListener(raw net.Listener, config *tls.Config) *samePortListener {
	l := &samePortListener{Listener: raw, tlsConfig: config, ready: make(chan acceptResult),
		done: make(chan struct{}), pending: make(chan struct{}, prefaceLimit)}
	go l.accept()
	return l
}

func (l *samePortListener) Accept() (net.Conn, error) {
	select {
	case result := <-l.ready:
		return result.conn, result.err
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *samePortListener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		close(l.done)
		err = l.Listener.Close()
	})
	return err
}

func (l *samePortListener) accept() {
	for {
		select {
		case l.pending <- struct{}{}:
		case <-l.done:
			return
		}
		conn, err := l.Listener.Accept()
		if err == nil {
			go l.classify(conn)
			continue
		}
		<-l.pending
		select {
		case l.ready <- acceptResult{err: err}:
		case <-l.done:
			return
		}
		if temporary, ok := err.(net.Error); !ok || !temporary.Temporary() {
			return
		}
	}
}

type prefacedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *prefacedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (l *samePortListener) classify(raw net.Conn) {
	defer func() { <-l.pending }()
	reader := bufio.NewReader(raw)
	if raw.SetReadDeadline(time.Now().Add(prefaceTimeout)) != nil {
		_ = raw.Close()
		return
	}
	first, err := reader.Peek(1)
	if err != nil || raw.SetReadDeadline(time.Time{}) != nil {
		_ = raw.Close()
		return
	}
	var conn net.Conn = &prefacedConn{Conn: raw, reader: reader}
	// TLS ClientHello starts with a TLS handshake record (22). HTTP methods
	// start with an ASCII token. net/http and crypto/tls still parse and validate
	// their own protocols; this byte never authorizes a request.
	if first[0] == 22 {
		conn = tls.Server(conn, l.tlsConfig)
	}
	select {
	case l.ready <- acceptResult{conn: conn}:
	case <-l.done:
		_ = raw.Close()
	}
}

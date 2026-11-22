package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	config := flag.String("config", "", "explicit local-only JSON policy file")
	state := flag.String("state", "", "private directory for the independent local room database")
	port := flag.Int("port", 0, "IPv4 loopback TCP port; 0 selects a free port")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runLocalRoom(ctx, *config, *state, *port); err != nil {
		log.Fatal(err)
	}
}

func runLocalRoom(parent context.Context, filename, state string, port int) error {
	if filename == "" || state == "" || port < 0 || port > 65535 {
		return errors.New("explicit -config and -state, and a valid loopback -port, are required")
	}
	const maxConfig = 128 << 10
	f, err := os.Open(filename)
	if err != nil {
		return errors.New("unable to open local room config")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfig+1))
	_ = f.Close()
	var config localConfig
	if err != nil || len(data) > maxConfig || decodeJSON(data, &config) != nil {
		return errors.New("invalid local room config")
	}
	policy, err := newRoomPolicy(config)
	if err != nil {
		return err
	}
	db, err := openRoomDB(state, policy.room)
	if err != nil {
		return fmt.Errorf("open local room state: %w", err)
	}
	defer db.Close()
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer listener.Close()

	handler := newRoomServer(listener.Addr().String(), policy, db)
	defer func() {
		// Wait for an accepted persistence operation before releasing the
		// process lock, including after a timed-out HTTP shutdown.
		handler.mu.Lock()
		db.failed = true
		handler.mu.Unlock()
	}()
	server := &http.Server{
		Handler:           handler,
		BaseContext:       func(net.Listener) context.Context { return parent },
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	shutdown := make(chan error, 1)
	stop := context.AfterFunc(parent, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := server.Shutdown(ctx)
		if err != nil {
			_ = server.Close()
		}
		shutdown <- err
	})
	defer stop()
	log.Printf("GooseCab local room listening at http://%s", listener.Addr())
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return <-shutdown
}

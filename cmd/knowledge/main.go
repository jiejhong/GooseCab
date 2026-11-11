// Command knowledge handles explicit local knowledge authoring/sharing/review.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/jiejhong/GooseCab/internal/knowledge"
	"github.com/jiejhong/GooseCab/internal/relay"
)

func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func read(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if len(b) > int(limit) {
		return nil, fmt.Errorf("file exceeds limit")
	}
	return b, e
}
func write(path string, b []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.Write(b); e != nil {
		return e
	}
	return f.Sync()
}
func run() error {
	mode := flag.String("mode", "", "put, search, share, unshare, export, review, import, send, receive")
	db := flag.String("knowledge", "", "agent-local persistent database")
	id := flag.String("id", "", "local agent ID")
	file := flag.String("file", "", "explicit input/output JSON file")
	collection := flag.String("collection", "", "explicit shared collection")
	recipient := flag.String("recipient", "", "named export/relay recipient")
	query := flag.String("query", "", "local search query")
	digest := flag.String("approve-sha256", "", "approve importing exactly this reviewed package digest")
	ticketPath := flag.String("ticket", "", "private relay ticket path")
	stage := flag.String("stage-dir", "", "existing recipient staging directory (receive only, no import)")
	url := flag.String("url", "http://127.0.0.1:8312", "gateway HTTP(S) URL")
	token := flag.String("token", os.Getenv("GOOSECAB_AGENT_TOKEN"), "gateway bearer token")
	flag.Parse()
	output := func(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
	if *mode == "review" {
		b, e := read(*file, knowledge.MaxPackageBytes)
		if e != nil {
			return e
		}
		v, e := knowledge.Inspect(bytes.NewReader(b))
		if e != nil {
			return e
		}
		return output(v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := relay.Client{URL: *url, Token: *token}
	if *mode == "receive" {
		b, e := read(*ticketPath, 8192)
		if e != nil {
			return e
		}
		var t relay.Ticket
		if e = json.Unmarshal(b, &t); e != nil {
			return e
		}
		if t.Recipient != *id || t.Size > knowledge.MaxPackageBytes {
			return fmt.Errorf("wrong recipient or oversized knowledge transfer")
		}
		path, e := client.ReceiveFile(ctx, t, *stage, "knowledge-"+t.ID+".json")
		if e != nil {
			return e
		}
		fmt.Println(path)
		return nil
	}
	s, e := knowledge.Open(*db, *id)
	if e != nil {
		return e
	}
	defer s.Close()
	switch *mode {
	case "put":
		b, e := read(*file, knowledge.MaxRecordBytes)
		if e != nil {
			return e
		}
		var r knowledge.Record
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if e = d.Decode(&r); e != nil {
			return e
		}
		ref, e := s.Put(r)
		if e != nil {
			return e
		}
		return output(ref)
	case "search":
		r, e := s.Search(*query)
		if e != nil {
			return e
		}
		return output(r)
	case "share":
		b, e := read(*file, 1<<20)
		if e != nil {
			return e
		}
		var c knowledge.Collection
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		if e = d.Decode(&c); e != nil {
			return e
		}
		return s.Share(c)
	case "unshare":
		return s.Unshare(*collection)
	case "export":
		b, e := s.Export(*collection, *recipient)
		if e != nil {
			return e
		}
		if e = write(*file, b); e != nil {
			return e
		}
		return output(map[string]string{"sha256": knowledge.Digest(b)})
	case "import":
		b, e := read(*file, knowledge.MaxPackageBytes)
		if e != nil {
			return e
		}
		return s.Import(b, *digest)
	case "send":
		// Rebuild from the approved local collection, never transmit arbitrary files.
		b, e := s.Export(*collection, *recipient)
		if e != nil {
			return e
		}
		f, e := os.OpenFile(*ticketPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		defer f.Close()
		t, e := client.Create(ctx, *id, *recipient, int64(len(b)), knowledge.Digest(b))
		if e != nil {
			return e
		}
		receive := t
		receive.SendSecret = ""
		if e = json.NewEncoder(f).Encode(receive); e != nil {
			return e
		}
		if e = f.Sync(); e != nil {
			return e
		}
		fmt.Fprintf(os.Stderr, "Privately deliver receiver ticket; verify sender=%s recipient=%s collection=%s sha256=%s. Receiving only stages the file, never imports.\n", *id, *recipient, *collection, knowledge.Digest(b))
		return client.Send(ctx, t, bytes.NewReader(b))
	default:
		return fmt.Errorf("unknown mode")
	}
}

// transfer is an explicitly invoked agent-local relay client. It exposes no file server.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/jiejhong/GooseCab/internal/relay"
	"io"
	"log"
	"os"
	"time"
)

func main() {
	mode := flag.String("mode", "", "create, send, or receive")
	base := flag.String("url", "http://127.0.0.1:8312", "Gateway base URL (HTTPS outside local development)")
	ticket := flag.String("ticket", "", "local private ticket JSON path")
	source := flag.String("source", "", "agent-local source file")
	sender := flag.String("sender", "", "sender agent ID")
	recipient := flag.String("recipient", "", "recipient agent ID")
	dir := flag.String("dir", "", "trusted local receiving directory")
	name := flag.String("name", "", "locally chosen destination basename")
	flag.Parse()
	if *ticket == "" {
		log.Fatal("-ticket is required")
	}
	c := relay.Client{URL: *base, Token: os.Getenv("GOOSECAB_AGENT_TOKEN")}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if *mode == "create" {
		f, e := os.Open(*source)
		must(e)
		defer f.Close()
		st, e := f.Stat()
		must(e)
		if !st.Mode().IsRegular() || st.Size() > relay.DefaultMaxSize {
			log.Fatal("source must be a bounded regular file")
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(f, relay.DefaultMaxSize+1))
		must(e)
		t, e := c.Create(ctx, *sender, *recipient, n, hex.EncodeToString(h.Sum(nil)))
		must(e)
		out, e := os.OpenFile(*ticket, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		must(e)
		must(json.NewEncoder(out).Encode(t))
		must(out.Close())
		fmt.Println("Private one-time ticket created; share only the recipient role secret securely.")
		return
	}
	f, e := os.Open(*ticket)
	must(e)
	var t relay.Ticket
	must(json.NewDecoder(io.LimitReader(f, 4096)).Decode(&t))
	must(f.Close())
	switch *mode {
	case "send":
		src, e := os.Open(*source)
		must(e)
		defer src.Close()
		st, e := src.Stat()
		must(e)
		if !st.Mode().IsRegular() {
			log.Fatal("source must be a regular file")
		}
		must(c.Send(ctx, t, src))
		fmt.Println("Receiver verified bytes.")
	case "receive":
		dest, e := c.ReceiveFile(ctx, t, *dir, *name)
		must(e)
		fmt.Println(dest)
	default:
		log.Fatal("-mode must be create, send, or receive")
	}
}
func must(e error) {
	if e != nil {
		log.Fatal(e)
	}
}

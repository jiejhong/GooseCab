// Command skill explicitly teaches/learns declarative text recipes. Package
// bytes and activation stay on agent disks, never in gateway event storage.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/jiejhong/GooseCab/internal/relay"
	"github.com/jiejhong/GooseCab/internal/skills"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	mode := flag.String("mode", "", "teach, learn, install, execute, rollback, or list")
	database := flag.String("skills", "", "agent-local skill database")
	file := flag.String("file", "", "local package JSON file (teach/install)")
	ticketPath := flag.String("ticket", "", "private local relay ticket file (teach writes, learn reads)")
	url := flag.String("url", "http://127.0.0.1:8312", "HTTP(S) gateway base URL")
	token := flag.String("token", os.Getenv("GOOSECAB_AGENT_TOKEN"), "gateway bearer token")
	teacher := flag.String("teacher", "", "teacher agent ID")
	learner := flag.String("learner", "", "learner agent ID")
	capability := flag.String("capability", "", "expected capability ID")
	version := flag.Int("version", 0, "expected package version")
	hash := flag.String("sha256", "", "independently verified SHA256 package digest")
	text := flag.String("text", "", "text for offline execution")
	timeout := flag.Duration("timeout", 2*time.Minute, "transfer deadline")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	c := relay.Client{URL: *url, Token: *token}
	if *mode == "teach" {
		f, e := os.Open(*file)
		if e != nil {
			return e
		}
		defer f.Close()
		p, digest, e := skills.Inspect(f)
		if e != nil {
			return e
		}
		if _, e = f.Seek(0, io.SeekStart); e != nil {
			return e
		}
		stat, e := f.Stat()
		if e != nil {
			return e
		}
		// Reserve the private ticket path before creating network state.
		ticketFile, e := os.OpenFile(*ticketPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		defer ticketFile.Close()
		ticket, e := c.Create(ctx, *teacher, *learner, stat.Size(), digest)
		if e != nil {
			return e
		}
		receiveTicket := ticket
		receiveTicket.SendSecret = ""
		if e = json.NewEncoder(ticketFile).Encode(receiveTicket); e != nil {
			return e
		}
		if e = ticketFile.Sync(); e != nil {
			return e
		}
		fmt.Fprintf(os.Stderr, "Ticket saved locally. Securely deliver it to learner; independently verify capability=%s version=%d sha256=%s. Waiting for learner.\n", p.Capability, p.Version, digest)
		if e = c.Send(ctx, ticket, f); e != nil {
			return e
		}
		fmt.Println("Receiver acknowledged completion. With cmd/skill learn, this follows local validation and installation.")
		return nil
	}
	s, e := skills.Open(*database)
	if e != nil {
		return e
	}
	defer s.Close()
	switch *mode {
	case "learn":
		if !skills.ValidCapability(*capability) || *version < 1 || len(*hash) != 64 {
			return fmt.Errorf("learn requires expected -capability, -version and independently verified -sha256")
		}
		f, e := os.Open(*ticketPath)
		if e != nil {
			return e
		}
		var ticket relay.Ticket
		d := json.NewDecoder(io.LimitReader(f, 4097))
		d.DisallowUnknownFields()
		e = d.Decode(&ticket)
		f.Close()
		if e != nil {
			return e
		}
		if ticket.Recipient != *learner || *learner == "" || ticket.Sender != *teacher || *teacher == "" || ticket.SHA256 != *hash || ticket.Size < 1 || ticket.Size > skills.MaxPackageBytes {
			return fmt.Errorf("ticket does not match expected teacher, learner, digest or package bounds")
		}
		staged, e := os.CreateTemp("", "goosecab-skill-*")
		if e != nil {
			return e
		}
		defer os.Remove(staged.Name())
		defer staged.Close()
		if e = c.ReceiveAndFinalize(ctx, ticket, staged, func() error {
			if _, err := staged.Seek(0, io.SeekStart); err != nil {
				return err
			}
			return s.Install(staged, *hash, *capability, *version)
		}); e != nil {
			return e
		}
		fmt.Println("Learned package evaluated and activated locally. Restart worker with this -skills database to announce it.")
		return nil
	case "install":
		f, e := os.Open(*file)
		if e != nil {
			return e
		}
		defer f.Close()
		return s.Install(f, *hash, *capability, *version)
	case "execute":
		output, e := s.Execute(*capability, *text)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"text": output})
	case "rollback":
		return s.Rollback(*capability, *version)
	case "list":
		ids, e := s.Capabilities()
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(ids)
	default:
		return fmt.Errorf("choose -mode teach, learn, install, execute, rollback or list")
	}
}

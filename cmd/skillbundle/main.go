// Command skillbundle manages agent-local, reviewable Skill packages. Use the
// existing transfer CLI to transport ZIP bytes; receipt never implies approval.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/jiejhong/GooseCab/internal/protocol"
	"github.com/jiejhong/GooseCab/internal/skillbundle"
)

func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
func run() error {
	mode := flag.String("mode", "", "pack, stage, inspect, approve, rollback, list, execute")
	root := flag.String("root", "", "protected local Skill store directory")
	file := flag.String("file", "", "local ZIP path (pack creates, stage reads)")
	dir := flag.String("dir", "", "source directory containing manifest.json and declared files")
	hash := flag.String("sha256", "", "independently reviewed archive SHA256")
	cap := flag.String("capability", "", "expected cap.skill.<name>")
	version := flag.Int("version", 0, "expected positive version")
	text := flag.String("text", "", "bounded text input for offline execution")
	flag.Parse()
	emit := func(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
	if *mode == "pack" {
		source, e := os.Open(*dir + "/manifest.json")
		if e != nil {
			return e
		}
		m, e := skillbundle.ReadManifest(source)
		source.Close()
		if e != nil {
			return e
		}

		f, e := os.OpenFile(*file, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		in, e := skillbundle.Pack(*dir, m, f)
		ce := f.Close()
		if e != nil {
			os.Remove(*file)
			return e
		}
		if ce != nil {
			return ce
		}
		return emit(in)
	}
	s, e := skillbundle.Open(*root)
	if e != nil {
		return e
	}
	defer s.Close()
	switch *mode {
	case "stage":
		f, e := os.Open(*file)
		if e != nil {
			return e
		}
		defer f.Close()
		in, e := s.Stage(f)
		if e != nil {
			return e
		}
		return emit(in)
	case "inspect":
		in, e := s.Inspect(*hash)
		if e != nil {
			return e
		}
		return emit(in)
	case "approve":
		if e = s.Approve(*hash, *cap, *version); e != nil {
			return e
		}
		return emit(map[string]string{"status": "locally approved and activated", "sha256": *hash})
	case "rollback":
		return s.Rollback(*cap, *version, *hash)
	case "list":
		a, e := s.Announcements()
		if e != nil {
			return e
		}
		return emit(a)
	case "execute":
		r := s.Invoke(context.Background(), protocol.CapabilityInvoke{RequestID: "local", CapabilityID: *cap, Payload: map[string]any{"text": *text}})
		if e = emit(r); e != nil {
			return e
		}
		if r.Status != "completed" {
			return fmt.Errorf("execution failed")
		}
		return nil
	default:
		return fmt.Errorf("unknown mode")
	}
}

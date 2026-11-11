// mcp manages inert connection manifests and explicit local approvals.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/jiejhong/GooseCab/internal/mcpclient"
	"io"
	"os"
	"strings"
)

func main() {
	db := flag.String("state", "mcp.db", "local approval database")
	file := flag.String("file", "", "manifest input/output file")
	digest := flag.String("sha256", "", "exact reviewed manifest SHA256")
	token := flag.String("token-env", "", "local environment variable containing bearer token (never token value)")
	private := flag.Bool("allow-private", false, "explicitly approve private addresses for this endpoint")
	loop := flag.Bool("allow-http-loopback", false, "test only: allow HTTP literal loopback endpoint")
	callers := flag.String("callers", "", "required comma-separated exact caller IDs")
	rooms := flag.String("rooms", "", "optional comma-separated exact room IDs")
	flag.Parse()
	die := func(e error) { fmt.Fprintln(os.Stderr, e); os.Exit(1) }
	if flag.NArg() != 1 {
		die(fmt.Errorf("usage: mcp [flags] import|export|approve|revoke|list"))
	}
	s, e := mcpclient.Open(*db)
	if e != nil {
		die(e)
	}
	defer s.Close()
	switch flag.Arg(0) {
	case "import":
		f, e := os.Open(*file)
		if e != nil {
			die(e)
		}
		b, e := io.ReadAll(io.LimitReader(f, mcpclient.MaxManifestBytes+1))
		f.Close()
		if e != nil {
			die(e)
		}
		d, e := s.Import(b)
		if e != nil {
			die(e)
		}
		fmt.Println(d)
	case "export":
		b, e := s.Export(*digest)
		if e != nil {
			die(e)
		}
		if *file == "" {
			os.Stdout.Write(b)
		} else if e = os.WriteFile(*file, b, 0600); e != nil {
			die(e)
		}
	case "approve":
		if *digest == "" {
			die(fmt.Errorf("approval requires exact reviewed -sha256"))
		}
		if e = s.Approve(*digest, mcpclient.Binding{AllowedCallers: splitIDs(*callers), AllowedRooms: splitIDs(*rooms), TokenEnv: *token, AllowPrivate: *private, AllowHTTPLoopback: *loop}); e != nil {
			die(e)
		}
		fmt.Println("Approved locally:", *digest)
	case "revoke":
		if e = s.Revoke(*digest); e != nil {
			die(e)
		}
		fmt.Println("Revoked locally:", *digest)
	case "list":
		entries, e := s.List()
		if e != nil {
			die(e)
		}
		json.NewEncoder(os.Stdout).Encode(entries)
	default:
		die(fmt.Errorf("unknown command"))
	}
}

func splitIDs(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

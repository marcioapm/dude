// Command fake-slack serves the Slack stand-in (internal/fakeslack) for the
// E2E suite and a local demo: the Web API under /api, the OAuth pages, and
// /_fake/ controls to set up workspaces and read what dude posted.
//
//	fake-slack -listen 127.0.0.1:0 -client-id ID -client-secret SECRET -addr-file PATH
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "address to serve on")
	clientID := flag.String("client-id", "fake-client", "the app's OAuth client id")
	clientSecret := flag.String("client-secret", "fake-secret", "the app's OAuth client secret")
	addrFile := flag.String("addr-file", "", "write the bound address here once listening")
	flag.Parse()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	if *addrFile != "" {
		if err := os.WriteFile(*addrFile, []byte(ln.Addr().String()), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	log.Fatal(http.Serve(ln, fakeslack.New(*clientID, *clientSecret)))
}

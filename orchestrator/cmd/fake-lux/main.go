// Command fake-lux serves the fake lux over HTTP, for the end-to-end suite.
//
// Its agents are dude's scripted agent (internal/fakeagent), and their pushes
// land in the git repository each Run's spec names, resolved under -root —
// the directory the suite's fake GitHub serves.
//
// Each Run's checkout, which exec runs in, is a clone under -workspaces.
//
//	fake-lux -listen 127.0.0.1:0 -root /path/served/by/git-daemon -key KEY -addr-file PATH -workspaces DIR
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "address to serve on")
	root := flag.String("root", "", "directory git:// repository URLs resolve under, as git daemon's --base-path")
	key := flag.String("key", "", "API key to accept")
	addrFile := flag.String("addr-file", "", "write the bound address here once listening")
	workspaces := flag.String("workspaces", "", "where each Run's checkout is made (default: the system's temporary directory)")
	legacyInput := flag.Bool("legacy-input", false, "acknowledge input as a lux before input phases: one lux.input with no phase, at the turn's end")
	nextTurnInput := flag.Bool("next-turn-input", false, "a harness that reads input only between turns (lands next_turn)")
	failUnreadOnInterrupt := flag.Bool("fail-unread-on-interrupt", false, "an interrupt fails input the agent took and had not read, as a lux before it carried it into the next turn")
	oldPools := flag.Bool("old-pools", false, "list pools as a lux before host sizes: no hostSize, hostSizeFrom, instanceType or isDefault")
	memoryShare := flag.Float64("memory-share", 0.95, "report each placement's memoryLimit as this share of the memory its spec asks for; 0 reports none, as an older lux")
	flag.Parse()

	srv := fakelux.New("", *key, nil)
	srv.LegacyInput, srv.NextTurnInput = *legacyInput, *nextTurnInput
	srv.FailUnreadOnInterrupt = *failUnreadOnInterrupt
	srv.MemoryShare = *memoryShare
	if *oldPools {
		srv.Pools = fakelux.OldPools()
	}
	srv.Workspaces = *workspaces
	srv.RepoFor = func(u string) string { return repoPath(*root, u) }
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	if *addrFile != "" {
		if err := os.WriteFile(*addrFile, []byte(ln.Addr().String()), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	log.Printf("fake lux on %s", ln.Addr())
	log.Fatal(http.Serve(ln, srv.Handler()))
}

// repoPath maps git://host:port/owner/repo.git to <root>/owner/repo.git, as
// git daemon serves it.
func repoPath(root, u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.Index(u, "/"); i >= 0 {
		u = u[i:]
	}
	return root + u
}

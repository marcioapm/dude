// Command fake-lux serves the fake lux over HTTP, for the end-to-end suite.
//
// Agents follow a script chosen by their phase, and pushes land in the git
// repository the Run's spec names — resolved under -root, the directory the
// suite's fake GitHub serves.
//
//	fake-lux -listen 127.0.0.1:0 -root /path/served/by/git-daemon -key KEY -addr-file PATH
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "address to serve on")
	root := flag.String("root", "", "directory git:// repository URLs resolve under, as git daemon's --base-path")
	key := flag.String("key", "", "API key to accept")
	addrFile := flag.String("addr-file", "", "write the bound address here once listening")
	flag.Parse()

	srv := fakelux.New("", *key, nil)
	srv.RepoFor = func(u string) string { return repoPath(*root, u) }
	srv.Decide = script(srv.RepoFor)
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

// script is what each phase's agent does. A prompt that starts with a sleep
// (dude sends one for a work item marked [hang]) never finishes its turn,
// which is how the suite gets a live agent to steer, pause and abort.
func script(repoFor func(string) string) func(map[string]any) fakelux.Behaviour {
	return func(spec map[string]any) fakelux.Behaviour {
		repo := repoFor(url(spec))
		labels, _ := spec["labels"].(map[string]any)
		wl, _ := spec["workload"].(map[string]any)
		prompt, _ := wl["prompt"].(string)
		hang := strings.HasPrefix(prompt, "sleep ")
		switch labels["dude.phase"] {
		case "implement":
			return fakelux.Behaviour{Hang: hang, Reply: "Implemented it.", Tools: []string{"bash"},
				Commit: map[string]string{"FACTORY.md": "Written by " + str(labels["dude.run"]) + "\n"}}
		case "review":
			// Reviewing a tree that has the fixer's file is reviewing a fix:
			// clean, so the loop converges after one round.
			if exec.Command("git", "-C", repo, "cat-file", "-e", ref(spec)+":FIXED.md").Run() == nil {
				return fakelux.Behaviour{Reply: "Reviewed the fix; no further problems."}
			}
			return fakelux.Behaviour{Reply: "One problem:\n\n```yaml\n---\nseverity: blocking\ncategory: correctness\n" +
				"file: FACTORY.md\nline: 1\ntitle: FACTORY.md does not record the fix\n" +
				"description: The change is missing a record that the review was addressed.\n" +
				"suggested_fix: Add a file naming what was fixed.\n```\n"}
		case "fix":
			return fakelux.Behaviour{Reply: "Addressed it.",
				Commit: map[string]string{"FIXED.md": "addressed by " + str(labels["dude.run"]) + "\n"}}
		case "simplify":
			return fakelux.Behaviour{Reply: "Simplified.",
				Commit: map[string]string{"SIMPLE.md": "simplified by " + str(labels["dude.run"]) + "\n"}}
		}
		return fakelux.Behaviour{Reply: "Nothing to do."}
	}
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

func url(spec map[string]any) string {
	return field(spec, "url")
}

func ref(spec map[string]any) string {
	return field(spec, "ref")
}

func field(spec map[string]any, name string) string {
	git, _ := spec["git"].(map[string]any)
	repos, _ := git["repositories"].([]any)
	if len(repos) == 0 {
		return ""
	}
	r, _ := repos[0].(map[string]any)
	return str(r[name])
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

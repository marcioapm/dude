package agenttools_test

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cli builds the dude CLI once per test binary.
func cli(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "dude")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/dude").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// luxService stands in for lux's service proxy: a unix socket in the
// "container" that forwards to dude's tools and adds the Run's credential,
// which the CLI never sees.
func luxService(t *testing.T, upstream, token string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "dude.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse(upstream)
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Header.Set("Authorization", "Bearer "+token)
	}
	srv := &http.Server{Handler: proxy}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "unix:" + sock
}

func TestTheCLIWorksThroughLuxsSocketWithoutTheToken(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_sh", "implementer", "running")
	bin := cli(t)
	artifacts := t.TempDir()
	env := append(os.Environ(), "LUX_SERVICE_DUDE="+luxService(t, f.url, token), "LUX_ARTIFACTS="+artifacts,
		"DUDE_TOOLS_TOKEN=", "DUDE_TOOLS_URL=")
	dude := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}

	out, err := dude("work", "create", "--title", "Split hyphenated words", "--goal", "found while truncating",
		"--epic", "Text utilities", "--criterion", "re-enter stays whole")
	if err != nil || !strings.Contains(out, "TEXT-2") {
		t.Fatalf("work create: %v\n%s", err, out)
	}
	out, err = dude("work", "list", "--json")
	var listed struct {
		WorkItems []struct{ Key, Epic string } `json:"workItems"`
	}
	if err != nil || json.Unmarshal([]byte(out), &listed) != nil || len(listed.WorkItems) != 2 ||
		listed.WorkItems[1].Epic != "Text utilities" {
		t.Fatalf("work list: %v\n%s", err, out)
	}
	if out, err = dude("epic", "list"); err != nil || !strings.Contains(out, "Text utilities") {
		t.Errorf("epic list: %v\n%s", err, out)
	}
	if out, err = dude("event", "progress", "--data", `{"done":1,"of":2}`); err != nil || !strings.Contains(out, "agent.custom.progress") {
		t.Errorf("event: %v\n%s", err, out)
	}
	// A refusal is said plainly, with a failing exit.
	if out, err = dude("event", "Not A Type"); err == nil || !strings.Contains(out, "lowercase") {
		t.Errorf("a bad event type: %v\n%s", err, out)
	}

	// Publishing is local: into $LUX_ARTIFACTS, nothing sent anywhere.
	notes := filepath.Join(t.TempDir(), "notes.md")
	_ = os.WriteFile(notes, []byte("# Notes\n"), 0o644)
	if out, err = dude("publish", notes, "--name", "design/notes.md"); err != nil {
		t.Fatalf("publish: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(artifacts, "design", "notes.md")); string(b) != "# Notes\n" {
		t.Errorf("published file = %q", b)
	}
	if out, err = dude("publish", notes, "--name", "../../etc/x"); err == nil {
		t.Errorf("published outside the directory: %s", out)
	}

	// Asking is the last thing a turn does.
	if out, err = dude("ask", "Keep hyphenated words whole?", "--choice", "yes", "--choice", "no"); err != nil ||
		!strings.Contains(out, "End your turn") {
		t.Errorf("ask: %v\n%s", err, out)
	}
}

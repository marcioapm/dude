package agenttools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/ids"
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
	// Unix socket paths are bounded by sockaddr_un, including the temp root.
	dir, err := os.MkdirTemp("", "lux-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "dude.sock")
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

	out, err := dude("task", "create", "--title", "Split hyphenated words", "--goal", "found while truncating",
		"--epic", "Text utilities", "--criterion", "re-enter stays whole")
	if err != nil || !strings.Contains(out, "TEXT-2") {
		t.Fatalf("work create: %v\n%s", err, out)
	}
	out, err = dude("task", "list", "--json")
	var listed struct {
		Tasks []struct{ Key, Epic string } `json:"tasks"`
	}
	if err != nil || json.Unmarshal([]byte(out), &listed) != nil || len(listed.Tasks) != 2 ||
		listed.Tasks[1].Epic != "Text utilities" {
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

	// Memory, from the shell: add, find, read.
	out, err = dude("memory", "add", "--title", "Hyphenated words stay whole", "--content", "Truncate never splits re-enter.",
		"--kind", "fact", "--about", "TEXT-1")
	var added struct{ ID string }
	if err != nil || json.Unmarshal([]byte(out), &added) != nil || !strings.HasPrefix(added.ID, "mem_") {
		t.Fatalf("memory add: %v\n%s", err, out)
	}
	if out, err = dude("memory", "search", "hyphenated", "words", "--type", "memory"); err != nil || !strings.Contains(out, added.ID) {
		t.Errorf("memory search: %v\n%s", err, out)
	}
	if out, err = dude("memory", "show", added.ID); err != nil || !strings.Contains(out, "re-enter") || !strings.Contains(out, "TEXT-1") {
		t.Errorf("memory show: %v\n%s", err, out)
	}
	if out, err = dude("memory", "add", "--title", "x"); err == nil || !strings.Contains(out, "usage") {
		t.Errorf("memory add without content: %v\n%s", err, out)
	}

	// Publishing on an older lux ($LUX_ARTIFACTS set) is a local copy, nothing
	// sent anywhere; on lux#77 it is lux-shim's (cmd/dude's tests).
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

// dude ask --questions-json asks several questions through the same
// ask_person, held to the same limits; a bad array is refused before
// anything is sent, and the tool's refusals come back plainly.
func TestTheCLIAsksSeveralQuestions(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_cliask", "implementer", "running")
	bin := cli(t)
	env := append(os.Environ(), "LUX_SERVICE_DUDE="+luxService(t, f.url, token), "DUDE_TOOLS_TOKEN=", "DUDE_TOOLS_URL=")
	dude := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}
	asked := func() int {
		var n int
		_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM questions WHERE run_id = 'run_cliask'`).Scan(&n)
		return n
	}
	for _, args := range [][]string{
		{"ask", "--questions-json", `{"header":"x"}`},
		{"ask", "--questions-json", `[{"header":"x"`},
		{"ask", "Also this?", "--questions-json", `[{"header":"A","question":"a?"}]`},
		{"ask", "--questions-json", `[{"header":"A","question":"a?"}]`, "--choice", "x"},
		{"ask", "--questions-json", `[{"header":"A","question":"a?"}]`, "--action", "retry"},
	} {
		if out, err := dude(args...); err == nil || !strings.Contains(out, "usage: dude ask") {
			t.Errorf("%v: %v\n%s", args, err, out)
		}
	}
	five := `[{"header":"1","question":"a?"},{"header":"2","question":"b?"},{"header":"3","question":"c?"},{"header":"4","question":"d?"},{"header":"5","question":"e?"}]`
	if out, err := dude("ask", "--questions-json", five); err == nil || !strings.Contains(out, "ask 1 to 4 questions in one call; you asked 5") {
		t.Errorf("five questions: %v\n%s", err, out)
	}
	if n := asked(); n != 0 {
		t.Fatalf("a refused ask recorded %d questions", n)
	}
	out, err := dude("ask", "--questions-json", `[{"header":"Scope","question":"Retry 4xx?","choices":[{"label":"No","description":"our bug","recommended":true},{"label":"Yes"}]},
		{"header":"Tests","question":"Which layers?","multiple":true,"choices":[{"label":"Unit"},{"label":"API"}]}]`)
	if err != nil || !strings.Contains(out, "End your turn") {
		t.Fatalf("ask --questions-json: %v\n%s", err, out)
	}
	var prompt string
	var raw []byte
	_ = f.owner.QueryRow(context.Background(), `SELECT prompt, items FROM questions WHERE run_id = 'run_cliask'`).Scan(&prompt, &raw)
	var items []struct {
		Header   string
		Multiple bool
		Choices  []struct {
			Label       string
			Recommended bool
		}
	}
	_ = json.Unmarshal(raw, &items)
	if prompt != "2 questions: Scope, Tests" || len(items) != 2 || !items[0].Choices[0].Recommended || !items[1].Multiple {
		t.Errorf("asked %q %s", prompt, raw)
	}
}

// dude diff's arguments reach run_diff: the Run (else the caller's own),
// the paths, and the list's flags.
func TestDudeDiffPassesItsArgumentsToTheTool(t *testing.T) {
	f := setup(t)
	token := f.run(t, "run_clidiff", "reviewer", "running")
	sibling := ids.New(ids.Run)
	f.run(t, sibling, "implementer", "completed")
	hunks := f.fiveFiles(t, sibling)
	f.fiveFiles(t, "run_clidiff")

	// What the CLI sent, as lux's socket forwards it.
	var mu sync.Mutex
	var bodies []string
	recorder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.URL.Path+" "+string(b))
		mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.URL.Scheme, r.URL.Host, r.RequestURI = "http", strings.TrimPrefix(f.url, "http://"), ""
		res, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer res.Body.Close()
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}))
	t.Cleanup(recorder.Close)
	bin := cli(t)
	env := append(os.Environ(), "LUX_SERVICE_DUDE="+luxService(t, recorder.URL, token), "DUDE_TOOLS_TOKEN=", "DUDE_TOOLS_URL=")
	dude := func(args ...string) (listed, string) {
		t.Helper()
		mu.Lock()
		bodies = nil
		mu.Unlock()
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		var got listed
		if err != nil || json.Unmarshal(out.Bytes(), &got) != nil {
			t.Fatalf("dude %v: %v\n%s", args, err, out.String())
		}
		mu.Lock()
		defer mu.Unlock()
		if len(bodies) != 1 {
			t.Fatalf("dude %v sent %v", args, bodies)
		}
		return got, bodies[0]
	}

	got, sent := dude("diff")
	if sent != "/tools/run_diff {}" || got.Run != "run_clidiff" || got.TotalFiles != 5 {
		t.Errorf("dude diff sent %s, showed run %s", sent, got.Run)
	}
	got, sent = dude("diff", "--name-status", "--limit", "2", "--offset", "1")
	if sent != `/tools/run_diff {"limit":2,"nameStatus":true,"offset":1}` ||
		!slices.Equal(paths(got.Files), []string{"b.go", "c.go"}) || len(got.Files[0]) != 2 || !got.HasMore {
		t.Errorf("dude diff --name-status --limit 2 --offset 1 sent %s, showed %v", sent, got.Files)
	}
	got, sent = dude("diff", sibling, "a.go", "c.go")
	if sent != `/tools/run_diff {"paths":["a.go","c.go"],"run":"`+sibling+`"}` || got.Run != sibling ||
		len(got.Files) != 2 || got.Files[0]["patch"] != hunks["a.go"] {
		t.Errorf("dude diff RUN a.go c.go sent %s, showed %v", sent, got.Files)
	}
	// A path that only looks like a Run's id is a path.
	if _, sent = dude("diff", "run_tests/x.go"); sent != `/tools/run_diff {"paths":["run_tests/x.go"]}` {
		t.Errorf("dude diff run_tests/x.go sent %s", sent)
	}
	// A repository-qualified path goes as given.
	repos := ids.New(ids.Run)
	f.run(t, repos, "implementer", "completed")
	repoHunks := f.storeTwoRepos(t, repos)
	got, sent = dude("diff", repos, "api/a.go")
	if sent != `/tools/run_diff {"paths":["api/a.go"],"run":"`+repos+`"}` ||
		!slices.Equal(paths(got.Files), []string{"api/a.go"}) || got.Files[0]["patch"] != repoHunks["api/a.go"] {
		t.Errorf("dude diff RUN api/a.go sent %s, showed %v", sent, got.Files)
	}
}

// Command dude-cli drives a dude server from any shell, as a person: tasks,
// runs, brainstorms, and any API call. It is installed as `dude` on people's
// machines; it is not the agent CLI (cmd/dude) that runs inside a Run.
//
// `dude login URL` keeps a key per organisation (keystore.go): commands act
// in the last one logged in to, or --org / DUDE_ORG; DUDE_API_KEY and
// DUDE_URL override it. Output is indented JSON.
//
// TASK is a key (ABS-12) or an id (wi_…). RUN is a run id (run_…), or a TASK
// for its newest agent run in the latest attempt. A text argument of - reads stdin.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/version"
)

const defaultURL = "https://dude.absmartly.dev"

const usageHead = `dude — drive a dude server from any shell. Output is JSON.

TASK is a key (ABS-12) or id (wi_…); RUN is a run id (run_…) or a TASK, meaning
its newest agent run in the latest attempt, not a preview. A text argument of -
reads stdin.

Signing in (a key is made in dude: Your settings → Keys)
  login URL [--file]          log in to the key's organisation on the dude at URL;
                              the key is read from stdin, checked, and kept
  orgs | use ORG              the organisations logged in to; act in another one
  logout [ORG] | whoami       forget a key; the account in use
  --org ORG                   before any command: act in ORG this once

dude itself
  upgrade [--version vX.Y.Z] [--check]
                              replace this binary with a GitHub release of the CLI
  guide [PAGE]                dude's guide
  api METHOD PATH [JSON | -]  any API call
  version | help

What the commands act on
`

const usageTail = `
dude NOUN for its commands; dude NOUN VERB --help for one's options.

Keys: ` + "`dude login URL`" + ` keeps one per organisation, under its slug, in the
macOS keychain, the Secret Service keyring (secret-tool, on Linux) or the
Windows Credential Manager; on a machine with none, in the credentials file
(` + "`~/.config/dude/credentials.toml`" + `, mode 0600), which also lists the
organisations and their URLs. Commands act in the last one logged in to, or in
--org / DUDE_ORG. DUDE_API_KEY overrides the stored key; DUDE_URL naming
another server needs DUDE_API_KEY too (default URL ` + defaultURL + `).
`

// nouns are what the commands keyed "NOUN VERB" act on, in dude --help's order.
var nouns = []struct{ name, help string }{
	{"task", "tasks: record, change, start, stop and close them; talk to their conductor"},
	{"run", "an agent's run: what it changed; steer, pause, resume, abort or restart it"},
	{"session", "an agent session and its child sessions"},
	{"question", "the questions agents asked, and answering them"},
	{"finding", "review findings, and settling them"},
	{"review-question", "reviewers' questions to a task's owner"},
	{"pr", "a task's pull requests: merge, update, rerun checks, request reviews"},
	{"feedback", "your review of a task's work, drafted and sent to its agents"},
	{"project", "projects and their boards"},
	{"repo", "a project's repositories, and agents' requests for another"},
	{"epic", "epics: a project's tasks grouped, with their agents' conversations"},
	{"note", "notes pinned on an epic, which every agent on it reads"},
	{"brainstorm", "an agent to think something through with"},
	{"memory", "what agents and people remembered"},
	{"automation", "work nobody starts by hand: on a schedule or on GitHub events"},
	{"person", "the organisation's people, and inviting more"},
	{"judgement", "the judgement calls dude's models made"},
}

func usage() string {
	var b strings.Builder
	b.WriteString(usageHead)
	width := 0
	for _, n := range nouns {
		width = max(width, len(n.name))
	}
	for _, n := range nouns {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, n.name, n.help)
	}
	b.WriteString(usageTail)
	return b.String()
}

// verbs are a noun's commands' verbs, sorted; none for a word that is no noun.
func verbs(noun string) []string {
	var out []string
	for name := range commands {
		if v, ok := strings.CutPrefix(name, noun+" "); ok {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// nounUsage is `dude NOUN`'s answer: each verb, its arguments and its help.
func nounUsage(noun string) string {
	vs := verbs(noun)
	lines, width := make([]string, len(vs)), 0
	for i, v := range vs {
		lines[i] = strings.TrimSpace(v + " " + commands[noun+" "+v].args)
		width = max(width, len(lines[i]))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "usage: dude %s VERB [arguments]\n\n", noun)
	for i, v := range vs {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, lines[i], commands[noun+" "+v].help)
	}
	fmt.Fprintf(&b, "\ndude %s VERB --help for one's options.\n", noun)
	return b.String()
}

func isHelp(arg string) bool { return arg == "help" || arg == "-h" || arg == "--help" }

// commandName is the command args names, a top-level one or "NOUN VERB", and
// the arguments after it. It is "" for a noun alone or followed by help.
func commandName(args []string) (string, []string, error) {
	if _, ok := commands[args[0]]; ok && !strings.Contains(args[0], " ") {
		return args[0], args[1:], nil
	}
	vs := verbs(args[0])
	if len(vs) == 0 {
		return "", nil, fmt.Errorf("unknown command %q; see dude --help", args[0])
	}
	if len(args) == 1 || isHelp(args[1]) {
		return "", nil, nil
	}
	name := args[0] + " " + args[1]
	if _, ok := commands[name]; !ok {
		return "", nil, fmt.Errorf("dude %s has no %q; its commands: %s", args[0], args[1], strings.Join(vs, ", "))
	}
	return name, args[2:], nil
}

func main() {
	c := &cli{
		creds:  resolve,
		login:  login,
		logout: logout,
		http:   apiClient(nil),
		stdin:  os.Stdin,
		out:    os.Stdout,
		errOut: os.Stderr,
		term:   osTerminal{int(os.Stdin.Fd())},
		// DUDE_RELEASES_URL is for tests: a fake GitHub releases API.
		updater: newUpdater(os.Getenv("DUDE_RELEASES_URL")),
	}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		c.prompt = func(s string) { fmt.Fprint(os.Stderr, s) }
	}
	if err := c.run(os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "dude:", err)
		}
		os.Exit(1)
	}
}

type cli struct {
	// creds is the server and key for an organisation ("" for the current).
	creds func(org string) (base, key string, err error)
	// login and logout keep credentials; tests replace them.
	login  func(slug, base, key string, toFile bool) (string, error)
	logout func(slug string) error
	// org is --org, before the command.
	org    string
	base   string
	http   *http.Client
	stdin  io.Reader
	out    io.Writer
	errOut io.Writer
	// term, when stdin is a terminal, reads login's key without echo.
	term terminal
	// updater is upgrade's; nil in tests that do not set one.
	updater *updater
	cached  string
	// prompt, when stdin is a terminal, asks for what is read next.
	prompt func(string)
}

// apiClient follows no redirect: a sign-in proxy answers 302, and following
// it would print the sign-in page as success and, on the same host, send the
// key along.
func apiClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Timeout:       60 * time.Second,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

const signInAdvice = "it is behind a sign-in; check the URL (dude whoami), or the server's API bypass for keys"

type obj = map[string]any

// httpError is a non-2xx answer, with the server's own words.
type httpError struct {
	method, path string
	status       int
	body         string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("%s %s: %d %s", e.method, e.path, e.status, e.body)
}

func (c *cli) request(method, path string, body any) (any, error) {
	if c.cached == "" {
		base, k, err := c.creds(c.org)
		if err != nil {
			return nil, err
		}
		c.base, c.cached = strings.TrimRight(base, "/"), k
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cached)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", c.base)
	// Cloudflare refuses clients without a user agent of their own (error 1010).
	req.Header.Set("User-Agent", "dude-cli/"+version.Version)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 3 {
		where := resp.Header.Get("Location")
		if u, err := resp.Location(); err == nil {
			where = u.String()
		}
		if where != "" {
			where = " to " + where
		}
		return nil, fmt.Errorf("%s %s: the server redirected (%d)%s: %s", method, path, resp.StatusCode, where, signInAdvice)
	}
	if resp.StatusCode/100 != 2 {
		return nil, &httpError{method, path, resp.StatusCode, strings.TrimSpace(string(raw))}
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "text/html" && strings.HasPrefix(path, "/v1/") {
		return nil, fmt.Errorf("%s %s: the server answered with an HTML page, not JSON: %s", method, path, signInAdvice)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return obj{}, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return obj{"body": string(raw)}, nil
	}
	return v, nil
}

// getObj is a GET whose answer is a JSON object.
func (c *cli) getObj(path string) (obj, error) {
	v, err := c.request("GET", path, nil)
	if err != nil {
		return nil, err
	}
	o, ok := v.(obj)
	if !ok {
		return nil, fmt.Errorf("GET %s: expected an object", path)
	}
	return o, nil
}

func query(params ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(params); i += 2 {
		if params[i+1] != "" {
			v.Set(params[i], params[i+1])
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

func (c *cli) text(s string) (string, error) {
	if s != "-" {
		return s, nil
	}
	b, err := io.ReadAll(c.stdin)
	return string(b), err
}

func str(o obj, k string) string { s, _ := o[k].(string); return s }

func list(o obj, k string) []obj {
	items, _ := o[k].([]any)
	out := make([]obj, 0, len(items))
	for _, it := range items {
		if m, ok := it.(obj); ok {
			out = append(out, m)
		}
	}
	return out
}

func pick(o obj, keys ...string) obj {
	out := obj{}
	for _, k := range keys {
		if v, ok := o[k]; ok {
			out[k] = v
		}
	}
	return out
}

func pickAll(items []obj, keys ...string) []obj {
	out := make([]obj, len(items))
	for i, it := range items {
		out[i] = pick(it, keys...)
	}
	return out
}

func (c *cli) projects() ([]obj, error) {
	o, err := c.getObj("/v1/projects")
	if err != nil {
		return nil, err
	}
	return list(o, "projects"), nil
}

// project is a project by key (ABS), slug, name or id.
func (c *cli) project(ref string) (obj, error) {
	ps, err := c.projects()
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if ref == str(p, "id") || ref == str(p, "slug") || strings.EqualFold(ref, str(p, "key")) ||
			strings.EqualFold(ref, str(p, "name")) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no project %q; see dude project list", ref)
}

func (c *cli) projectID(ref string) (string, error) {
	p, err := c.project(ref)
	if err != nil {
		return "", err
	}
	return str(p, "id"), nil
}

func (c *cli) tasks(projectID, status string) ([]obj, error) {
	o, err := c.getObj("/v1/tasks" + query("projectId", projectID, "status", status))
	if err != nil {
		return nil, err
	}
	return list(o, "tasks"), nil
}

// task is a task by key (ABS-12) or id, with its runs.
func (c *cli) task(ref string) (obj, error) {
	return c.getObj("/v1/tasks/" + url.PathEscape(ref))
}

func (c *cli) taskID(ref string) (string, error) {
	if strings.HasPrefix(ref, "wi_") {
		return ref, nil
	}
	t, err := c.task(ref)
	if err != nil {
		return "", err
	}
	return str(t, "id"), nil
}

// runID is a run id as given, or a task's newest run.
func (c *cli) runID(ref string) (string, error) {
	if strings.HasPrefix(ref, "run_") {
		return ref, nil
	}
	t, err := c.task(ref)
	if err != nil {
		return "", err
	}
	return taskRunID(t, ref)
}

func taskRunID(t obj, ref string) (string, error) {
	for _, run := range list(t, "runs") {
		if str(run, "kind") == "agent" {
			return str(run, "id"), nil
		}
	}
	return "", fmt.Errorf("%s has no agent runs", ref)
}

// brief is a task without the bulky parts: people as names.
func brief(t obj) obj {
	out := pick(t, "key", "id", "title", "status", "escalation", "goal", "acceptanceCriteria", "repositories")
	if owner, ok := t["owner"].(obj); ok {
		out["owner"] = owner["name"]
	}
	if _, ok := t["runs"]; ok {
		out["runs"] = pickAll(list(t, "runs"), "id", "attempt", "kind", "role", "status", "createdAt")
	}
	return out
}

// taskRepos turns NAME or NAME:read into the API's {id, access}.
func (c *cli) taskRepos(projectID string, names []string) ([]obj, error) {
	p, err := c.getObj("/v1/projects/" + url.PathEscape(projectID))
	if err != nil {
		return nil, err
	}
	known := map[string]string{}
	for _, r := range list(p, "repositories") {
		known[str(r, "name")] = str(r, "id")
	}
	out := []obj{}
	for _, n := range names {
		name, access, _ := strings.Cut(n, ":")
		id, ok := known[name]
		if !ok {
			have := make([]string, 0, len(known))
			for k := range known {
				have = append(have, k)
			}
			sort.Strings(have)
			return nil, fmt.Errorf("--repo %q is not in this project; it has: %s", name, strings.Join(have, ", "))
		}
		switch access {
		case "":
			access = "write"
		case "read", "write":
		default:
			return nil, fmt.Errorf("--repo %q: access is read or write", n)
		}
		out = append(out, obj{"id": id, "access": access})
	}
	return out, nil
}

// many is a repeatable string flag.
type many []string

func (m *many) String() string     { return strings.Join(*m, ",") }
func (m *many) Set(v string) error { *m = append(*m, v); return nil }

// ints is a repeatable integer flag.
type ints []int

func (m *ints) String() string { return fmt.Sprint(*m) }
func (m *ints) Set(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("not a number: %s", v)
	}
	*m = append(*m, n)
	return nil
}

// command is one subcommand: its positional arguments, as names for usage
// (a trailing ? marks an optional one), and what it does.
type command struct {
	args  string
	help  string
	flags func(fs *flag.FlagSet) doer
}

type doer = func(c *cli, args []string) (any, error)

// parse lets flags and positional arguments mix, as `dude task show ABS-1 --x`.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func (c *cli) run(args []string) error {
	for len(args) > 0 && (args[0] == "--org" || strings.HasPrefix(args[0], "--org=")) {
		if v, ok := strings.CutPrefix(args[0], "--org="); ok {
			c.org, args = v, args[1:]
		} else if len(args) > 1 {
			c.org, args = args[1], args[2:]
		} else {
			return errors.New("--org needs an organisation's slug")
		}
	}
	if len(args) == 0 || isHelp(args[0]) {
		if len(args) > 2 {
			if _, ok := commands[args[1]+" "+args[2]]; ok {
				return c.run([]string{args[1], args[2], "--help"})
			}
		}
		if len(args) > 1 && len(verbs(args[1])) > 0 {
			return c.run(args[1:2])
		}
		fmt.Fprint(c.out, usage())
		return nil
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Fprintln(c.out, version.Version)
		return nil
	}
	name, rest, err := commandName(args)
	if err != nil {
		return err
	}
	if name == "" {
		fmt.Fprint(c.out, nounUsage(args[0]))
		return nil
	}
	cmd := commands[name]
	fs := flag.NewFlagSet("dude "+name, flag.ContinueOnError)
	fs.SetOutput(c.out)
	fs.Usage = func() {
		fmt.Fprintf(c.out, "usage: dude %s %s\n\n%s\n", name, cmd.args, cmd.help)
		fs.PrintDefaults()
	}
	do := cmd.flags(fs)
	pos, err := parse(fs, rest)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	min, max := 0, 0
	for _, a := range strings.Fields(cmd.args) {
		max++
		if strings.HasSuffix(a, "...") {
			max = -1 // the rest, one or more
			min++
			break
		}
		if !strings.HasSuffix(a, "?") {
			min++
		}
	}
	if len(pos) < min || (max >= 0 && len(pos) > max) {
		return fmt.Errorf("usage: dude %s %s", name, cmd.args)
	}
	for len(pos) < max {
		pos = append(pos, "")
	}
	out, err := do(c, pos)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(c.out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// noFlags is a command with no options.
func noFlags(do doer) func(*flag.FlagSet) doer {
	return func(*flag.FlagSet) doer { return do }
}

// onTask posts body to /v1/tasks/ID/suffix.
func (c *cli) onTask(method, ref, suffix string, body any) (any, error) {
	id, err := c.taskID(ref)
	if err != nil {
		return nil, err
	}
	return c.request(method, "/v1/tasks/"+url.PathEscape(id)+suffix, body)
}

func (c *cli) onRun(method, ref, suffix string, body any) (any, error) {
	id, err := c.runID(ref)
	if err != nil {
		return nil, err
	}
	return c.request(method, "/v1/runs/"+url.PathEscape(id)+suffix, body)
}

// itemAnswer is one item's answer: the choices picked and the text, if any.
func (c *cli) itemAnswer(text string, choices ints) (obj, error) {
	ans := obj{"choices": append([]int{}, choices...)}
	if text != "" {
		t, err := c.text(text)
		if err != nil {
			return nil, err
		}
		ans["text"] = t
	}
	return ans, nil
}

func startFlags(fs *flag.FlagSet, route string) doer {
	override := fs.Bool("override", false, "start even though it is blocked")
	note := fs.String("note", "", "why, recorded with an override")
	return func(c *cli, a []string) (any, error) {
		body := obj{}
		if *override {
			body["override"] = true
			if *note != "" {
				body["note"] = *note
			}
		}
		return c.onTask("POST", a[0], route, body)
	}
}

func reasonFlags(fs *flag.FlagSet, route string) doer {
	reason := fs.String("reason", "", "why")
	return func(c *cli, a []string) (any, error) {
		body := obj{}
		if *reason != "" {
			body["reason"] = *reason
		}
		return c.onRun("POST", a[0], route, body)
	}
}

var commands = map[string]command{
	"api": {"METHOD PATH BODY?", "any API call: dude api GET /v1/me; dude api POST /v1/… '{json}' (or - for stdin)",
		noFlags(func(c *cli, a []string) (any, error) {
			var body any
			if a[2] != "" {
				raw, err := c.text(a[2])
				if err != nil {
					return nil, err
				}
				if err := json.Unmarshal([]byte(raw), &body); err != nil {
					return nil, fmt.Errorf("body is not JSON: %w", err)
				}
			}
			path := a[1]
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			return c.request(strings.ToUpper(a[0]), path, body)
		})},

	// Reading
	"project list": {"", "the projects you can see", noFlags(func(c *cli, _ []string) (any, error) {
		ps, err := c.projects()
		if err != nil {
			return nil, err
		}
		return pickAll(ps, "key", "name", "slug", "id"), nil
	})},
	"repo list": {"PROJECT", "a project's repositories", noFlags(func(c *cli, a []string) (any, error) {
		pid, err := c.projectID(a[0])
		if err != nil {
			return nil, err
		}
		p, err := c.getObj("/v1/projects/" + url.PathEscape(pid))
		if err != nil {
			return nil, err
		}
		return pickAll(list(p, "repositories"), "name", "url", "defaultBranch"), nil
	})},
	"epic list": {"PROJECT", "a project's epics", noFlags(func(c *cli, a []string) (any, error) {
		pid, err := c.projectID(a[0])
		if err != nil {
			return nil, err
		}
		return c.request("GET", "/v1/projects/"+url.PathEscape(pid)+"/epics", nil)
	})},
	"task list": {"PROJECT", "a project's tasks", func(fs *flag.FlagSet) doer {
		status := fs.String("status", "", "received, queued, running, awaiting_input, review, ready_to_merge, done, …")
		return func(c *cli, a []string) (any, error) {
			pid, err := c.projectID(a[0])
			if err != nil {
				return nil, err
			}
			ts, err := c.tasks(pid, *status)
			if err != nil {
				return nil, err
			}
			return pickAll(ts, "key", "title", "status"), nil
		}
	}},
	"task show": {"TASK", "one task: status, why it waits, goal, runs", noFlags(func(c *cli, a []string) (any, error) {
		t, err := c.task(a[0])
		if err != nil {
			return nil, err
		}
		return brief(t), nil
	})},
	"task raw": {"TASK", "one task as the API returns it", noFlags(func(c *cli, a []string) (any, error) {
		return c.task(a[0])
	})},
	"task relations": {"TASK", "what blocks a task, what it blocks, what it relates to",
		noFlags(func(c *cli, a []string) (any, error) { return c.onTask("GET", a[0], "/relations", nil) })},
	"task metrics": {"TASK", "a task's cost and time",
		noFlags(func(c *cli, a []string) (any, error) { return c.onTask("GET", a[0], "/metrics", nil) })},
	"pr list": {"TASK", "a task's pull requests", noFlags(func(c *cli, a []string) (any, error) {
		id, err := c.taskID(a[0])
		if err != nil {
			return nil, err
		}
		o, err := c.getObj("/v1/pull-requests" + query("taskId", id))
		if err != nil {
			return nil, err
		}
		return o["pullRequests"], nil
	})},
	"run show": {"RUN", "one run in full", noFlags(func(c *cli, a []string) (any, error) {
		return c.onRun("GET", a[0], "", nil)
	})},
	"run diff": {"RUN", "what a run changed", noFlags(func(c *cli, a []string) (any, error) {
		return c.onRun("GET", a[0], "/diff", nil)
	})},

	// Projects and epics
	"project create": {"", "a new project (organisation admins)", func(fs *flag.FlagSet) doer {
		name := fs.String("name", "", "its name (required)")
		slug := fs.String("slug", "", "lowercase-with-dashes (required)")
		key := fs.String("key", "", "task key prefix, e.g. WEB (default: from the slug)")
		desc := fs.String("description", "", "what it is")
		var repos many
		fs.Var(&repos, "repo", "a repository, NAME=URL; repeatable")
		return func(c *cli, _ []string) (any, error) {
			if *name == "" || *slug == "" {
				return nil, errors.New("project create: --name and --slug are required")
			}
			rs := []obj{}
			for _, r := range repos {
				n, u, ok := strings.Cut(r, "=")
				if !ok {
					return nil, fmt.Errorf("--repo %q: give NAME=URL", r)
				}
				rs = append(rs, obj{"name": n, "url": u})
			}
			body := obj{"name": *name, "slug": *slug, "description": *desc, "repositories": rs}
			if *key != "" {
				body["key"] = *key
			}
			v, err := c.request("POST", "/v1/projects", body)
			if err != nil {
				return nil, err
			}
			p, _ := v.(obj)
			return pick(p, "key", "name", "slug", "id", "repositories"), nil
		}
	}},
	"repo add": {"PROJECT", "add a repository to a project", func(fs *flag.FlagSet) doer {
		name := fs.String("name", "", "its name in the project (required)")
		u := fs.String("url", "", "its clone URL (required)")
		branch := fs.String("branch", "main", "its default branch")
		return func(c *cli, a []string) (any, error) {
			if *name == "" || *u == "" {
				return nil, errors.New("repo add: --name and --url are required")
			}
			pid, err := c.projectID(a[0])
			if err != nil {
				return nil, err
			}
			return c.request("POST", "/v1/projects/"+url.PathEscape(pid)+"/repositories",
				obj{"name": *name, "url": *u, "defaultBranch": *branch})
		}
	}},
	"epic create": {"PROJECT", "a new epic in a project", func(fs *flag.FlagSet) doer {
		title := fs.String("title", "", "its title (required)")
		desc := fs.String("description", "", "- reads stdin")
		return func(c *cli, a []string) (any, error) {
			if *title == "" {
				return nil, errors.New("epic create: --title is required")
			}
			d, err := c.text(*desc)
			if err != nil {
				return nil, err
			}
			pid, err := c.projectID(a[0])
			if err != nil {
				return nil, err
			}
			return c.request("POST", "/v1/projects/"+url.PathEscape(pid)+"/epics", obj{"title": *title, "description": d})
		}
	}},

	// Tasks
	"task create": {"PROJECT", "record a task; --start starts delivery, --talk starts its conductor to plan it with you",
		func(fs *flag.FlagSet) doer {
			title := fs.String("title", "", "what changes, in one line (required)")
			goal := fs.String("goal", "", "why it matters and what should change, 16+ characters (required); - reads stdin")
			epic := fs.String("epic", "", "epic id")
			start := fs.Bool("start", false, "start delivery right away")
			talk := fs.Bool("talk", false, "start its conductor to plan it first; nothing is built yet")
			from := addSourceFlags(fs)
			var criteria, repos, blockedBy, blocks, related many
			fs.Var(&criteria, "criterion", "an acceptance criterion; repeatable")
			fs.Var(&repos, "repo", "a repository it works on, NAME or NAME:read; repeatable (default: the project's only one)")
			fs.Var(&blockedBy, "blocked-by", "a TASK it waits for; repeatable")
			fs.Var(&blocks, "blocks", "a TASK that waits for it; repeatable")
			fs.Var(&related, "related", "a related TASK; repeatable")
			return func(c *cli, a []string) (any, error) {
				if *title == "" || *goal == "" {
					return nil, errors.New("task create: --title and --goal are required")
				}
				if *start && *talk {
					return nil, errors.New("task create: --start or --talk, not both")
				}
				g, err := c.text(*goal)
				if err != nil {
					return nil, err
				}
				pid, err := c.projectID(a[0])
				if err != nil {
					return nil, err
				}
				rs := []obj{}
				if len(repos) > 0 {
					if rs, err = c.taskRepos(pid, repos); err != nil {
						return nil, err
					}
				}
				body := obj{"projectId": pid, "title": *title, "goal": g,
					"acceptanceCriteria": append([]string{}, criteria...), "repositories": rs}
				if *from.pr != "" || *from.branch != "" {
					if len(rs) != 1 {
						return nil, errors.New("task create: --from-pr and --from-branch need exactly one --repo, the repository they are in")
					}
				}
				repoID := ""
				if len(rs) == 1 {
					repoID, _ = rs[0]["id"].(string)
				}
				src, err := from.body(repoID)
				if err != nil {
					return nil, err
				}
				if src != nil {
					body["source"] = src
				}
				if *epic != "" {
					body["epicId"] = *epic
				}
				v, err := c.request("POST", "/v1/tasks", body)
				if err != nil {
					return nil, err
				}
				t, _ := v.(obj)
				out := brief(t)
				tid := "/v1/tasks/" + url.PathEscape(str(t, "id"))
				for _, rel := range []struct {
					kind   string
					others many
				}{{"blocked_by", blockedBy}, {"blocks", blocks}, {"related", related}} {
					for _, other := range rel.others {
						if _, err := c.request("POST", tid+"/relations", obj{"kind": rel.kind, "task": other}); err != nil {
							return nil, fmt.Errorf("%s was created, but relating it failed: %w", str(t, "key"), err)
						}
					}
				}
				if *start {
					if out["started"], err = c.request("POST", tid+"/deliver", obj{}); err != nil {
						return nil, fmt.Errorf("%s was created, but starting it failed: %w", str(t, "key"), err)
					}
				} else if *talk {
					if out["talking"], err = c.request("POST", tid+"/talk", obj{}); err != nil {
						return nil, fmt.Errorf("%s was created, but starting its conductor failed: %w", str(t, "key"), err)
					}
				}
				return out, nil
			}
		}},
	"task update": {"TASK", "change a task's title, goal, criteria, epic, repositories or owner",
		func(fs *flag.FlagSet) doer {
			title := fs.String("title", "", "a new title")
			goal := fs.String("goal", "", "a new goal; - reads stdin")
			noCriteria := fs.Bool("no-criteria", false, "remove all criteria")
			epic := fs.String("epic", "", "epic id, or none")
			owner := fs.String("owner", "", "a person's id")
			var criteria, repos many
			fs.Var(&criteria, "criterion", "replaces all criteria; repeatable")
			fs.Var(&repos, "repo", "replaces its repositories, NAME or NAME:read; repeatable")
			return func(c *cli, a []string) (any, error) {
				body := obj{}
				if *title != "" {
					body["title"] = *title
				}
				if *goal != "" {
					g, err := c.text(*goal)
					if err != nil {
						return nil, err
					}
					body["goal"] = g
				}
				if *noCriteria {
					body["acceptanceCriteria"] = []string{}
				} else if len(criteria) > 0 {
					body["acceptanceCriteria"] = []string(criteria)
				}
				if *epic == "none" {
					body["epicId"] = nil
				} else if *epic != "" {
					body["epicId"] = *epic
				}
				if *owner != "" {
					body["ownerId"] = *owner
				}
				if len(body) == 0 && len(repos) == 0 {
					return nil, errors.New("task update: nothing to change")
				}
				t, err := c.task(a[0])
				if err != nil {
					return nil, err
				}
				if len(repos) > 0 {
					if body["repositories"], err = c.taskRepos(str(t, "projectId"), repos); err != nil {
						return nil, err
					}
				}
				v, err := c.request("PATCH", "/v1/tasks/"+url.PathEscape(str(t, "id")), body)
				if err != nil {
					return nil, err
				}
				u, _ := v.(obj)
				return brief(u), nil
			}
		}},
	"task start": {"TASK", "start delivery: agents implement, review and open a PR",
		func(fs *flag.FlagSet) doer { return startFlags(fs, "/deliver") }},
	"task talk": {"TASK", "start its conductor to plan it with you in Chat; nothing is built until it decides",
		func(fs *flag.FlagSet) doer { return startFlags(fs, "/talk") }},
	"task auto-start": {"TASK", "start it by itself once its blockers merge", func(fs *flag.FlagSet) doer {
		how := fs.String("how", "deliver", "deliver or talk")
		return func(c *cli, a []string) (any, error) {
			if *how != "deliver" && *how != "talk" {
				return nil, errors.New("task auto-start: --how is deliver or talk")
			}
			return c.onTask("PUT", a[0], "/auto-start", obj{"how": *how})
		}
	}},
	"task no-auto-start": {"TASK", "cancel auto-start",
		noFlags(func(c *cli, a []string) (any, error) { return c.onTask("DELETE", a[0], "/auto-start", nil) })},
	"task relate": {"TASK", "relate two tasks", func(fs *flag.FlagSet) doer {
		blocks := fs.String("blocks", "", "a TASK that waits for this one")
		blockedBy := fs.String("blocked-by", "", "a TASK this one waits for")
		related := fs.String("related", "", "a related TASK")
		why := fs.String("why", "", "why")
		return func(c *cli, a []string) (any, error) {
			var kind, other string
			n := 0
			for _, g := range [][2]string{{"blocks", *blocks}, {"blocked_by", *blockedBy}, {"related", *related}} {
				if g[1] != "" {
					kind, other = g[0], g[1]
					n++
				}
			}
			if n != 1 {
				return nil, errors.New("task relate: give exactly one of --blocks, --blocked-by, --related")
			}
			body := obj{"kind": kind, "task": other}
			if *why != "" {
				body["why"] = *why
			}
			return c.onTask("POST", a[0], "/relations", body)
		}
	}},
	"task unrelate": {"TASK KIND OTHER", "remove a relation; KIND is blocks, blocked_by or related",
		noFlags(func(c *cli, a []string) (any, error) {
			switch a[1] {
			case "blocks", "blocked_by", "related":
			default:
				return nil, errors.New("task unrelate: KIND is blocks, blocked_by or related")
			}
			return c.onTask("DELETE", a[0], "/relations/"+a[1]+"/"+url.PathEscape(a[2]), nil)
		})},
	"task done": {"TASK", "mark finished work that has nothing to merge done",
		noFlags(func(c *cli, a []string) (any, error) { return c.onTask("POST", a[0], "/done", obj{}) })},

	// Talking to the agents
	"task chat": {"TASK TEXT", "a message in a task's Chat: starts or tells its conductor",
		func(fs *flag.FlagSet) doer {
			aside := fs.Bool("aside", false, "for people only; the conductor does not act on it")
			return func(c *cli, a []string) (any, error) {
				t, err := c.text(a[1])
				if err != nil {
					return nil, err
				}
				body := obj{"text": t}
				if *aside {
					body["aside"] = true
				}
				return c.onTask("POST", a[0], "/chat", body)
			}
		}},
	"question answer": {"QUESTION TEXT?", "answer an agent's question (id from question list or task show)",
		func(fs *flag.FlagSet) doer {
			var choices ints
			fs.Var(&choices, "choice", "index of a choice; repeatable")
			return func(c *cli, a []string) (any, error) {
				if a[1] == "" && len(choices) == 0 {
					return nil, errors.New("question answer: give the text, --choice, or both")
				}
				ans, err := c.itemAnswer(a[1], choices)
				if err != nil {
					return nil, err
				}
				// The orchestrator takes text or answers, never both (422).
				body := obj{"answers": []obj{ans}}
				if len(choices) == 0 {
					body = obj{"text": ans["text"]}
				}
				return c.request("POST", "/v1/questions/"+url.PathEscape(a[0])+"/answer", body)
			}
		}},
	"task decide": {"TASK ACTION", "the owner's decision after delivery stopped for them (see task show's escalation)",
		func(fs *flag.FlagSet) doer {
			note := fs.String("note", "", "a note with the decision")
			return func(c *cli, a []string) (any, error) {
				body := obj{"action": a[1]}
				if *note != "" {
					body["note"] = *note
				}
				return c.onTask("POST", a[0], "/decide", body)
			}
		}},
	"run steer": {"RUN TEXT", "tell a running agent something, read at its next step",
		func(fs *flag.FlagSet) doer {
			interrupt := fs.Bool("interrupt", false, "stop its current turn: only when that work is wasted")
			return func(c *cli, a []string) (any, error) {
				t, err := c.text(a[1])
				if err != nil {
					return nil, err
				}
				body := obj{"text": t}
				if *interrupt {
					body["interrupt"] = true
				}
				return c.onRun("POST", a[0], "/steer", body)
			}
		}},
	"run pause": {"RUN", "pause a run", func(fs *flag.FlagSet) doer {
		hard := fs.Bool("hard", false, "stop now, not at the next safe point")
		reason := fs.String("reason", "", "why")
		return func(c *cli, a []string) (any, error) {
			body := obj{"mode": "graceful"}
			if *hard {
				body["mode"] = "hard"
			}
			if *reason != "" {
				body["reason"] = *reason
			}
			return c.onRun("POST", a[0], "/pause", body)
		}
	}},
	"run resume": {"RUN", "resume a run", func(fs *flag.FlagSet) doer {
		return reasonFlags(fs, "/resume")
	}},
	"run abort": {"RUN", "abort a run", func(fs *flag.FlagSet) doer {
		return reasonFlags(fs, "/abort")
	}},
	"run restart": {"RUN NOTE", "a fresh run in a stalled run's slot; NOTE says what to do differently",
		func(fs *flag.FlagSet) doer {
			tier := fs.String("tier", "", "model tier id")
			return func(c *cli, a []string) (any, error) {
				body := obj{"note": a[1]}
				if *tier != "" {
					body["tier"] = *tier
				}
				return c.onRun("POST", a[0], "/restart", body)
			}
		}},

	// Brainstorms
	"brainstorm list": {"", "your brainstorm sessions", func(fs *flag.FlagSet) doer {
		archived := fs.Bool("archived", false, "the archived ones")
		return func(c *cli, _ []string) (any, error) {
			a := ""
			if *archived {
				a = "1"
			}
			return c.request("GET", "/v1/brainstorms"+query("archived", a), nil)
		}
	}},
	"brainstorm create": {"MESSAGE", "start a brainstorm session: an agent to think something through with",
		func(fs *flag.FlagSet) doer {
			title := fs.String("title", "", "its title")
			tier := fs.String("tier", "", "model tier id")
			var projects many
			fs.Var(&projects, "project", "a project it may look at; repeatable")
			return func(c *cli, a []string) (any, error) {
				m, err := c.text(a[0])
				if err != nil {
					return nil, err
				}
				ps := []obj{}
				for _, p := range projects {
					pid, err := c.projectID(p)
					if err != nil {
						return nil, err
					}
					ps = append(ps, obj{"projectId": pid})
				}
				body := obj{"message": m, "projects": ps}
				if *title != "" {
					body["title"] = *title
				}
				if *tier != "" {
					body["tier"] = *tier
				}
				return c.request("POST", "/v1/brainstorms", body)
			}
		}},
	"brainstorm show": {"ID", "one brainstorm session", noFlags(func(c *cli, a []string) (any, error) {
		return c.request("GET", "/v1/brainstorms/"+url.PathEscape(a[0]), nil)
	})},
	"brainstorm chat": {"ID TEXT", "a message in a brainstorm session", noFlags(func(c *cli, a []string) (any, error) {
		t, err := c.text(a[1])
		if err != nil {
			return nil, err
		}
		return c.request("POST", "/v1/brainstorms/"+url.PathEscape(a[0])+"/chat", obj{"text": t})
	})},
}

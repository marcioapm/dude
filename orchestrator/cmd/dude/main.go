// Command dude is the dude CLI inside an agent's container: the work it is
// part of, and dude's tools, from a shell — for the agent, and for any
// script it runs.
//
//	dude task list [--text T]             the project's tasks
//	dude epic list                        the project's epics
//	dude repo list | request NAME --reason R [--write]
//	dude task create --title T --goal G [--epic E] [--criterion C]...
//	dude ask "question" [--choice C]...   ask a person; end your turn after
//	dude memory search QUERY [--type T]... [--limit N]
//	dude memory show ID                   one memory in full
//	dude memory add --title T --content C [--kind K] [--about KEY]... [--org]
//	dude event TYPE [--data JSON]         record an event on this run
//	dude diff [RUN] [PATH...] [--name-status] [--limit N] [--offset N]
//	                                      what a Run of this task changed
//	dude findings [ID...]                 a conductor's: the task's findings
//	dude prs                              a conductor's: the task's pull requests
//	dude phase start PHASE [--category C]... [--finding ID]... [--note N]
//	dude decide ACTION [--note N]         a conductor's: the decision waited on
//	dude finding dismiss ID --reason R    a conductor's: leave a finding as it is
//	dude task update [--goal G] [--criterion C]... [--no-criteria]
//	dude publish FILE [--name NAME]       keep a file for people (local)
//	dude tools                            what this run may use
//
// Output is indented JSON.
//
// It acts as its Run, but never holds the Run's credential: lux serves dude's
// tools on a local socket ($LUX_SERVICE_DUDE) and adds the credential on
// the way out. (For tests without lux, DUDE_TOOLS_URL and DUDE_TOOLS_TOKEN
// name the tools directly.) What is local — publishing a file — needs no
// network at all.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/version"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "dude:", err)
		os.Exit(1)
	}
}

// strings flag, repeatable.
type many []string

func (m *many) String() string     { return strings.Join(*m, ",") }
func (m *many) Set(v string) error { *m = append(*m, v); return nil }

// runID is how `dude diff` tells a Run from a path: an id as internal/ids
// mints it, run_ then 9 base36 characters of time and 16 hex.
var runID = regexp.MustCompile(`^run_[0-9a-z]{9}[0-9a-f]{16}$`)

func run(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(out, usage)
		return nil
	}
	if args[0] == "--version" {
		fmt.Fprintln(out, version.Version)
		return nil
	}
	cmd, rest := args[0], args[1:]
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") && (cmd == "task" || cmd == "epic" || cmd == "repo" || cmd == "memory" ||
		cmd == "phase" || cmd == "finding") {
		cmd, rest = cmd+" "+rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("dude "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "print JSON")
	switch cmd {
	case "tools":
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return show(out, *asJSON, get("/tools"))
	case "task list":
		text := fs.String("text", "", "only work mentioning this")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return show(out, *asJSON, call("list_tasks", map[string]any{"text": *text}))
	case "epic list":
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return show(out, *asJSON, call("list_epics", map[string]any{}))
	case "repo list":
		if _, err := parse(fs, rest); err != nil {
			return err
		}
		return show(out, *asJSON, call("list_repositories", map[string]any{}))
	case "repo request":
		write := fs.Bool("write", false, "you need to change it (an implementer); otherwise read only")
		wait := fs.Bool("wait", false, "you cannot go on without it: end your turn after, and be resumed when it is decided")
		reason := fs.String("reason", "", "why this work needs it")
		args, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(args) != 1 || *reason == "" {
			return errors.New(`usage: dude repo request NAME --reason "why" [--write] [--wait]`)
		}
		return show(out, *asJSON, call("request_repository", map[string]any{"repository": args[0], "write": *write, "reason": *reason, "wait": *wait}))
	case "task create":
		title := fs.String("title", "", "what should change, in one line")
		goal := fs.String("goal", "", "why it matters and what should change; required, at least 16 characters")
		epic := fs.String("epic", "", "an existing epic's title")
		var criteria many
		fs.Var(&criteria, "criterion", "a thing that must be true when it is done (repeatable)")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return show(out, *asJSON, call("create_task", map[string]any{"title": *title, "goal": *goal,
			"epic": *epic, "acceptanceCriteria": []string(criteria)}))
	case "memory search":
		var types many
		fs.Var(&types, "type", "only memory, task, epic or project (repeatable)")
		limit := fs.Int("limit", 0, "how many results, 1 to 20 (default 8)")
		args, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(args) == 0 {
			return errors.New(`usage: dude memory search "what you want to know" [--type T]... [--limit N]`)
		}
		body := map[string]any{"query": strings.Join(args, " ")}
		if len(types) > 0 {
			body["types"] = []string(types)
		}
		if *limit > 0 {
			body["limit"] = *limit
		}
		return show(out, *asJSON, call("search_memory", body))
	case "memory show":
		args, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(args) != 1 {
			return errors.New("usage: dude memory show ID")
		}
		return show(out, *asJSON, call("get_memory", map[string]any{"id": args[0]}))
	case "memory add":
		title := fs.String("title", "", "one line another agent can scan in a list")
		content := fs.String("content", "", "the fact, procedure or note, in Markdown; - reads it from stdin")
		kind := fs.String("kind", "", "fact (default), procedure or note")
		org := fs.Bool("org", false, "true in every project of the organization, not only this one")
		var about many
		fs.Var(&about, "about", "a task key (TEXT-12) or epic title it is about (repeatable)")
		if _, err := parse(fs, rest); err != nil {
			return err
		}
		text := *content
		if text == "-" {
			b, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
			if err != nil {
				return err
			}
			text = string(b)
		}
		if *title == "" || strings.TrimSpace(text) == "" {
			return errors.New(`usage: dude memory add --title "one line" --content "what to remember" [--kind K] [--about KEY]... [--org]`)
		}
		body := map[string]any{"title": *title, "content": text}
		if *kind != "" {
			body["kind"] = *kind
		}
		if len(about) > 0 {
			body["about"] = []string(about)
		}
		if *org {
			body["scope"] = "organization"
		}
		return show(out, *asJSON, call("remember", body))
	case "ask":
		var choices many
		fs.Var(&choices, "choice", "an answer to offer (repeatable)")
		args, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(args) != 1 {
			return errors.New(`usage: dude ask "question" [--choice C]...`)
		}
		return show(out, *asJSON, call("ask_person", map[string]any{"question": args[0], "choices": []string(choices)}))
	case "event":
		data := fs.String("data", "", "JSON to go with it")
		args, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(args) != 1 {
			return errors.New("usage: dude event TYPE [--data JSON]")
		}
		body := map[string]any{"type": args[0]}
		if *data != "" {
			if !json.Valid([]byte(*data)) {
				return errors.New("--data is not JSON")
			}
			body["data"] = json.RawMessage(*data)
		}
		return show(out, *asJSON, call("emit_event", body))
	case "diff":
		nameStatus := fs.Bool("name-status", false, "each file's path and status only")
		limit := fs.Int("limit", 0, "files per page, 1 to 1000 (default 200)")
		offset := fs.Int("offset", 0, "files to skip, for the next page")
		args, err := parse(fs, rest)
		if err != nil {
			return err
		}
		body := map[string]any{}
		if len(args) > 0 && runID.MatchString(args[0]) {
			body["run"], args = args[0], args[1:]
		}
		if len(args) > 0 {
			body["paths"] = args
		}
		if *nameStatus {
			body["nameStatus"] = true
		}
		if *limit != 0 {
			body["limit"] = *limit
		}
		if *offset != 0 {
			body["offset"] = *offset
		}
		return show(out, *asJSON, call("run_diff", body))
	case "findings":
		args, err := parse(fs, rest)
		if err != nil {
			return err
		}
		body := map[string]any{}
		if len(args) > 0 {
			body["ids"] = args
		}
		return show(out, *asJSON, call("findings", body))
	case "prs":
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			return errors.New("usage: dude prs")
		}
		return show(out, *asJSON, call("pull_requests", map[string]any{}))
	case "phase start":
		var categories, findings many
		fs.Var(&categories, "category", "review: a reviewer to run (repeatable); none runs those the change warrants")
		fs.Var(&findings, "finding", "fix: an open finding to fix (repeatable); none fixes every open one")
		note := fs.String("note", "", "what you ask of the Run")
		args, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(args) != 1 {
			return errors.New("usage: dude phase start implement|review|fix|simplify|test [--category C]... [--finding ID]... [--note N]")
		}
		body := map[string]any{"phase": args[0]}
		if len(categories) > 0 {
			body["categories"] = []string(categories)
		}
		if len(findings) > 0 {
			body["findings"] = []string(findings)
		}
		if *note != "" {
			body["note"] = *note
		}
		return show(out, *asJSON, call("start_phase", body))
	case "decide":
		note := fs.String("note", "", "why; for ask_person, the question")
		args, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(args) != 1 {
			return errors.New("usage: dude decide next|ask_person|wait|open_pull_request [--note N]")
		}
		return show(out, *asJSON, call("decide", map[string]any{"action": args[0], "note": *note}))
	case "finding dismiss":
		reason := fs.String("reason", "", "why it is left as it is")
		args, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(args) != 1 || *reason == "" {
			return errors.New(`usage: dude finding dismiss ID --reason "why"`)
		}
		return show(out, *asJSON, call("dismiss_finding", map[string]any{"id": args[0], "reason": *reason}))
	case "task update":
		goal := fs.String("goal", "", "the task's goal, whole")
		clear := fs.Bool("no-criteria", false, "clear the acceptance criteria")
		var criteria many
		fs.Var(&criteria, "criterion", "an acceptance criterion (repeatable): the whole list")
		if _, err := parse(fs, rest); err != nil {
			return err
		}
		body := map[string]any{}
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "goal" {
				body["goal"] = *goal
			}
		})
		if len(criteria) > 0 || *clear {
			body["acceptanceCriteria"] = list(criteria)
		}
		if len(body) == 0 {
			return errors.New(`usage: dude task update [--goal G] [--criterion C]... [--no-criteria]`)
		}
		return show(out, *asJSON, call("update_task", body))
	case "publish":
		name := fs.String("name", "", "the name people see (default: the file's)")
		args, err := parse(fs, rest)
		if err != nil {
			return err
		}
		if len(args) != 1 {
			return errors.New("usage: dude publish FILE [--name NAME]")
		}
		return show(out, *asJSON, func() (json.RawMessage, error) { return publish(args[0], *name) })
	}
	return fmt.Errorf("unknown command %q (dude help)", cmd)
}

// list is a repeatable flag's values as JSON reads them: [] for none.
func list(m many) []string {
	if m == nil {
		return []string{}
	}
	return m
}

// parse parses flags wherever they are among the arguments — `publish FILE
// --name N`, `ask "question" --choice A` — since Go's flags stop at the
// first argument: parse, take that argument, parse what follows, repeat.
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

// publish copies a file into $LUX_ARTIFACTS, which lux collects when the
// container stops and dude shows with the task.
func publish(path, name string) (json.RawMessage, error) {
	dir := os.Getenv("LUX_ARTIFACTS")
	if dir == "" {
		return nil, errors.New("LUX_ARTIFACTS is not set: publishing works inside a run")
	}
	if name == "" {
		name = filepath.Base(path)
	}
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "..") {
		return nil, fmt.Errorf("name %q must stay inside the published files", name)
	}
	src, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return nil, err
	}
	dst := filepath.Join(dir, clean)
	// Already there (written straight into $LUX_ARTIFACTS, as a browser's
	// video often is): nothing to copy. Copying would truncate the file
	// before reading it, and publish it empty.
	if have, err := os.Stat(dst); err == nil && os.SameFile(info, have) {
		return published(clean, info.Size())
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(dst)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return published(clean, n)
}

func published(name string, bytes int64) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"published": name, "bytes": bytes,
		"note": "kept when your run stops, and shown with the task"})
}

// ---- talking to dude ------------------------------------------------------

// client reaches dude's tools: through lux's local socket, or (tests) a URL
// with a token.
func client() (*http.Client, string, string, error) {
	if svc := os.Getenv("LUX_SERVICE_DUDE"); svc != "" {
		sock, ok := strings.CutPrefix(svc, "unix:")
		if !ok {
			// A loopback address, if lux serves it that way.
			return &http.Client{Timeout: 60 * time.Second}, strings.TrimRight(svc, "/"), "", nil
		}
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}
		return &http.Client{Transport: tr, Timeout: 60 * time.Second}, "http://dude", "", nil
	}
	if u := os.Getenv("DUDE_TOOLS_URL"); u != "" {
		return &http.Client{Timeout: 60 * time.Second}, strings.TrimRight(u, "/"), os.Getenv("DUDE_TOOLS_TOKEN"), nil
	}
	return nil, "", "", errors.New("dude's tools are not available in this run (no LUX_SERVICE_DUDE)")
}

func call(tool string, args any) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) {
		body, _ := json.Marshal(args)
		return request("POST", "/tools/"+tool, body)
	}
}

func get(path string) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) { return request("GET", path, nil) }
}

func request(method, path string, body []byte) (json.RawMessage, error) {
	c, base, token, err := client()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching dude: %w", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return nil, errors.New(e.Error)
		}
		return nil, fmt.Errorf("dude answered %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// show prints a result as indented JSON: read by models and people alike,
// and by scripts. (--json is accepted, and changes nothing.)
func show(out io.Writer, _ bool, do func() (json.RawMessage, error)) error {
	raw, err := do()
	if err != nil {
		return err
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		fmt.Fprintln(out, string(raw))
		return nil
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(out, string(b))
	return nil
}

const usage = `dude — the work you are part of, and dude's tools, from the shell.

  dude task list [--text T]                  the project's tasks
  dude epic list                             the project's epics, in priority order
  dude task create --title T --goal G [--epic E] [--criterion C]...
                                             record work found outside your task
                                             (a person decides whether it is done)
  dude repo list                             the project's repositories: which you have, which you could ask for
  dude repo request NAME --reason R [--write] [--wait]
                                             ask a person for another of them
  dude ask "question" [--choice C]...        ask a person; then end your turn —
                                             the answer is your next message
  dude memory search QUERY [--type T]... [--limit N]
                                             what is known here: memories, tasks, epics
                                             and projects, by words and meaning, best first
  dude memory show ID                        one memory in full
  dude memory add --title T --content C [--kind fact|procedure|note]
                  [--about KEY]... [--org]   save what the next agent should know
                                             (--content - reads stdin); live at once
  dude event TYPE [--data JSON]              record an event on this run, e.g.
                                             dude event progress --data '{"done":3,"of":10}'
  dude diff [RUN] [PATH...] [--name-status] [--limit N] [--offset N]
                                             what a Run changed: this Run's checkout, uncommitted
                                             work included, against the commit it started from
                                             (one snapshot, no history). RUN (run_…) is yours by
                                             default, or another of your task's. Without PATH the
                                             changed files, most changed first, with line counts
                                             (--limit default 200, max 1000; hasMore says there is
                                             another page); --name-status, path and status only.
                                             With PATHs (exact, as listed) their changes as
                                             unified diff text, at most 2,000 lines a call
  dude findings [ID...]                      a conductor's: the task's review findings and how each
                                             was settled; with IDs (fnd_…), those in full
  dude prs                                   a conductor's: the task's pull requests, their checks,
                                             reviews and feedback
  dude phase start PHASE [--category C]... [--finding ID]... [--note N]
                                             a conductor's: take the decision the delivery waits on
                                             by starting implement, review, fix, simplify or test
  dude decide ACTION [--note N]              a conductor's: next, ask_person, wait or
                                             open_pull_request (after the person answered Open)
  dude finding dismiss ID --reason R         a conductor's: leave an open finding as it is
  dude task update [--goal G] [--criterion C]... [--no-criteria]
                                             a conductor's: write what Chat settled into the task,
                                             before the implementer starts
  dude publish FILE [--name NAME]            keep a file for people, shown with the task
  dude tools                                 the tools this run may use
  dude --version                             this CLI's version

Output is JSON.
`

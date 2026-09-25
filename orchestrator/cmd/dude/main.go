// Command dude is the dude CLI inside an agent's container: the work it is
// part of, and dude's tools, from a shell — for the agent, and for any
// script it runs.
//
//	dude work list [--text T]             the project's work items
//	dude epic list                        the project's epics
//	dude repo list | request NAME --reason R [--write]
//	dude work create --title T --goal G [--epic E] [--criterion C]...
//	dude ask "question" [--choice C]...   ask a person; end your turn after
//	dude event TYPE [--data JSON]         record an event on this run
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
	"strings"
	"time"
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

func run(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(out, usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") && (cmd == "work" || cmd == "epic" || cmd == "repo") {
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
	case "work list":
		text := fs.String("text", "", "only work mentioning this")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return show(out, *asJSON, call("list_work", map[string]any{"text": *text}))
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
	case "work create":
		title := fs.String("title", "", "what should change, in one line")
		goal := fs.String("goal", "", "why, and what someone needs to know")
		epic := fs.String("epic", "", "an existing epic's title")
		var criteria many
		fs.Var(&criteria, "criterion", "a thing that must be true when it is done (repeatable)")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return show(out, *asJSON, call("create_work_item", map[string]any{"title": *title, "goal": *goal,
			"epic": *epic, "acceptanceCriteria": []string(criteria)}))
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
// container stops and dude shows with the work item.
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
	dst := filepath.Join(dir, clean)
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
	return json.Marshal(map[string]any{"published": clean, "bytes": n,
		"note": "kept when your run stops, and shown with the work item"})
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

  dude work list [--text T]                  the project's work items
  dude epic list                             the project's epics, in priority order
  dude work create --title T --goal G [--epic E] [--criterion C]...
                                             record work found outside your task
                                             (a person decides whether it is done)
  dude repo list                             the project's repositories: which you have, which you could ask for
  dude repo request NAME --reason R [--write] [--wait]
                                             ask a person for another of them
  dude ask "question" [--choice C]...        ask a person; then end your turn —
                                             the answer is your next message
  dude event TYPE [--data JSON]              record an event on this run, e.g.
                                             dude event progress --data '{"done":3,"of":10}'
  dude publish FILE [--name NAME]            keep a file for people, shown with the work item
  dude tools                                 the tools this run may use

Output is JSON.
`

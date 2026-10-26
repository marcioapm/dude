package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"
)

func init() {
	for name, cmd := range filterCommands {
		moreCommands[name] = cmd
	}
	for name, cmd := range moreCommands {
		if _, dup := commands[name]; dup {
			panic("dude-cli: command defined twice: " + name)
		}
		commands[name] = cmd
	}
}

var filterCommands = map[string]command{
	"finding list": {"TASK?", "review findings: a task's, or a run's with --run", func(fs *flag.FlagSet) doer {
		status := fs.String("status", "", "e.g. open")
		run := fs.String("run", "", "a RUN's findings instead")
		return func(c *cli, a []string) (any, error) {
			taskID, runID, err := c.taskOrRun(a[0], *run)
			if err != nil {
				return nil, err
			}
			return c.request("GET", "/v1/findings"+query("taskId", taskID, "runId", runID, "status", *status), nil)
		}
	}},
	"question list": {"TASK?", "questions agents asked: a task's, a run's with --run, or every open one",
		func(fs *flag.FlagSet) doer {
			run := fs.String("run", "", "a RUN's questions instead")
			return func(c *cli, a []string) (any, error) {
				taskID, runID, err := c.taskOrRun(a[0], *run)
				if err != nil {
					return nil, err
				}
				return c.request("GET", "/v1/questions"+query("taskId", taskID, "runId", runID), nil)
			}
		}},
}

// taskOrRun resolves an optional TASK and an optional --run.
func (c *cli) taskOrRun(task, run string) (string, string, error) {
	var taskID, runID string
	var resolved obj
	var err error
	if task != "" {
		if resolved, err = c.task(task); err != nil {
			return "", "", err
		}
		taskID = str(resolved, "id")
	}
	if run != "" {
		if run == task {
			runID, err = taskRunID(resolved, task)
		} else {
			runID, err = c.runID(run)
		}
		if err != nil {
			return "", "", err
		}
	}
	return taskID, runID, nil
}

// personOf is /v1/me's person, without the bulky parts.
func personOf(me obj) obj {
	p, _ := me["person"].(obj)
	return pick(p, "id", "name", "email", "role")
}

func (c *cli) post(path string, body any) (any, error) { return c.request("POST", path, body) }

func esc(s string) string { return url.PathEscape(s) }

func withNote(fs *flag.FlagSet, build func(c *cli, a []string, note string) (any, error)) doer {
	note := fs.String("note", "", "a note with it")
	return func(c *cli, a []string) (any, error) { return build(c, a, *note) }
}

func setIf(body obj, k, v string) obj {
	if v != "" {
		body[k] = v
	}
	return body
}

func oneOf(what, v string, allowed ...string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return fmt.Errorf("%s is %s", what, strings.Join(allowed, ", "))
}

// terminal is stdin when it may be a terminal; tests replace it.
type terminal interface {
	isTerminal() bool
	readPassword() ([]byte, error)
}

type osTerminal struct{ fd int }

func (t osTerminal) isTerminal() bool { return term.IsTerminal(t.fd) }

// readPassword reads with echo off. A SIGINT or SIGTERM kills the process
// before x/term's deferred restore runs, so the saved state is restored here
// before exiting.
func (t osTerminal) readPassword() ([]byte, error) {
	st, err := term.GetState(t.fd)
	if err != nil {
		return term.ReadPassword(t.fd)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	defer func() {
		signal.Stop(sigs)
		close(done)
	}()
	go watchInterrupt(sigs, done, func() error { return term.Restore(t.fd, st) }, os.Stderr, os.Exit)
	return term.ReadPassword(t.fd)
}

// watchInterrupt waits for a signal on sigs until done closes. On a signal
// it restores the terminal, ends the prompt's line on w and exits with
// 128 + the signal's number, a shell's code for death by it (130 for SIGINT).
func watchInterrupt(sigs <-chan os.Signal, done <-chan struct{}, restore func() error, w io.Writer, exit func(int)) {
	select {
	case sig := <-sigs:
		restore()
		fmt.Fprintln(w)
		code := 130
		if s, ok := sig.(syscall.Signal); ok {
			code = 128 + int(s)
		}
		exit(code)
	case <-done:
	}
}

// readKey is login's key: typed without echo on a terminal, else the first
// line of stdin.
func (c *cli) readKey(prompt string) (string, error) {
	if c.term != nil && c.term.isTerminal() {
		fmt.Fprint(c.errOut, prompt)
		b, err := c.term.readPassword()
		fmt.Fprintln(c.errOut)
		if err != nil {
			return "", fmt.Errorf("login: reading the key: %w", err)
		}
		if key := strings.TrimSpace(string(b)); key != "" {
			return key, nil
		}
		return "", errors.New("login: the key is empty")
	}
	line, rerr := bufio.NewReader(c.stdin).ReadString('\n')
	key := strings.TrimSpace(line)
	if key == "" {
		if rerr != nil {
			return "", errors.New("login: no key on stdin")
		}
		return "", errors.New("login: the key is empty")
	}
	return key, nil
}

var moreCommands = map[string]command{
	// Credentials
	"login": {"URL", "log in to an organisation on the dude at URL with a key from Your settings → Keys (read from stdin)",
		func(fs *flag.FlagSet) doer {
			toFile := fs.Bool("file", false, "keep the key in the credentials file, not the OS's keystore")
			return func(c *cli, a []string) (any, error) {
				base, err := normalURL(a[0])
				if err != nil {
					return nil, err
				}
				key, err := c.readKey("Key for " + base + " (dude: Your settings → Keys): ")
				if err != nil {
					return nil, err
				}
				c.base, c.cached = base, key
				me, err := c.getObj("/v1/me")
				if err != nil {
					return nil, fmt.Errorf("login: %s refused the key: %w", base, err)
				}
				org, _ := me["organization"].(obj)
				slug := str(org, "slug")
				if slug == "" {
					return nil, errors.New("login: the key names no organisation")
				}
				where, err := c.login(slug, base, key, *toFile)
				if err != nil {
					return nil, err
				}
				return obj{"organization": pick(org, "slug", "name"), "url": base, "person": personOf(me), "keyIn": where}, nil
			}
		}},
	"logout": {"ORG?", "forget an organisation's key (default: the current one)", noFlags(func(c *cli, a []string) (any, error) {
		creds, err := loadCredentials()
		if err != nil {
			return nil, err
		}
		slug := a[0]
		if slug == "" {
			slug, _ = creds.selected(c.org)
		}
		if slug == "" {
			return nil, errors.New("logout: not logged in")
		}
		if err := c.logout(slug); err != nil {
			return nil, err
		}
		after, _ := loadCredentials()
		return obj{"loggedOut": slug, "current": after.Current}, nil
	})},
	"orgs": {"", "the organisations you are logged in to; current is the one commands act in", noFlags(func(c *cli, _ []string) (any, error) {
		creds, err := loadCredentials()
		if err != nil {
			return nil, err
		}
		cur, _ := creds.selected(c.org)
		out := []obj{}
		for _, s := range creds.slugs() {
			out = append(out, obj{"slug": s, "url": creds.Orgs[s].URL, "current": s == cur})
		}
		return out, nil
	})},
	"use": {"ORG", "act in another organisation you are logged in to, from now on", noFlags(func(c *cli, a []string) (any, error) {
		creds, err := loadCredentials()
		if err != nil {
			return nil, err
		}
		if _, ok := creds.Orgs[a[0]]; !ok {
			return nil, fmt.Errorf("not logged in to %q; logged in to: %s", a[0], strings.Join(creds.slugs(), ", "))
		}
		creds.Current = a[0]
		if err := creds.save(); err != nil {
			return nil, err
		}
		return obj{"current": a[0]}, nil
	})},
	"whoami": {"", "the account, organisation and role the key acts as", noFlags(func(c *cli, _ []string) (any, error) {
		me, err := c.getObj("/v1/me")
		if err != nil {
			return nil, err
		}
		org, _ := me["organization"].(obj)
		return obj{"person": personOf(me), "organization": pick(org, "slug", "name", "id"), "url": c.base}, nil
	})},
	"person list": {"", "the organisation's people", noFlags(func(c *cli, _ []string) (any, error) {
		return c.request("GET", "/v1/people", nil)
	})},
	"person invite": {"EMAIL...", "invite people to the organisation (admins)", func(fs *flag.FlagSet) doer {
		role := fs.String("role", "member", "member or admin")
		return func(c *cli, a []string) (any, error) {
			if err := oneOf("--role", *role, "member", "admin"); err != nil {
				return nil, err
			}
			inv := []obj{}
			for _, e := range a {
				inv = append(inv, obj{"email": e})
			}
			return c.post("/v1/organization/invitations", obj{"invitations": inv, "role": *role})
		}
	}},

	// Projects
	"project overview": {"PROJECT", "a project's board, by epic", noFlags(func(c *cli, a []string) (any, error) {
		pid, err := c.projectID(a[0])
		if err != nil {
			return nil, err
		}
		return c.request("GET", "/v1/projects/"+esc(pid)+"/overview", nil)
	})},

	// Tasks
	"task assign": {"TASK PERSON...", "set who is on a task; the first person is its owner", noFlags(func(c *cli, a []string) (any, error) {
		return c.onTask("PUT", a[0], "/people", obj{"people": a[1:]})
	})},
	"task retry": {"TASK", "retry a task whose conductor failed",
		noFlags(func(c *cli, a []string) (any, error) { return c.onTask("POST", a[0], "/retry", nil) })},
	"task recover": {"TASK ACTION?", "a stopped task: without ACTION, how it can recover; with it (resume, retry, restart), do it",
		func(fs *flag.FlagSet) doer {
			override := fs.Bool("override", false, "restart even though it is blocked")
			return withNote(fs, func(c *cli, a []string, note string) (any, error) {
				if a[1] == "" {
					return c.onTask("GET", a[0], "/recover", nil)
				}
				if err := oneOf("ACTION", a[1], "resume", "retry", "restart"); err != nil {
					return nil, err
				}
				body := setIf(obj{"action": a[1]}, "note", note)
				if *override {
					body["override"] = true
				}
				return c.onTask("POST", a[0], "/recover", body)
			})
		}},
	"task decider": {"TASK WHO", "who takes a task's delivery decisions: policy or conductor", func(fs *flag.FlagSet) doer {
		openPR := fs.Bool("open-pr", false, "with conductor: it may open the pull request too")
		draft := fs.Bool("draft", false, "the pull request opens as a draft")
		return func(c *cli, a []string) (any, error) {
			if err := oneOf("WHO", a[1], "policy", "conductor"); err != nil {
				return nil, err
			}
			body := obj{"decider": a[1]}
			if *openPR {
				body["openPullRequest"] = true
			}
			if *draft {
				body["draft"] = true
			}
			return c.onTask("POST", a[0], "/decider", body)
		}
	}},
	"task reset-machine": {"TASK ROLE", "throw away a task's agent machine for ROLE (e.g. implementer); the next one starts fresh",
		func(fs *flag.FlagSet) doer {
			return withNote(fs, func(c *cli, a []string, note string) (any, error) {
				return c.onTask("DELETE", a[0], "/agent-machines/"+esc(a[1]), setIf(obj{}, "note", note))
			})
		}},
	"task artifacts": {"TASK", "files a task's agents published", noFlags(func(c *cli, a []string) (any, error) {
		id, err := c.taskID(a[0])
		if err != nil {
			return nil, err
		}
		return c.request("GET", "/v1/artifacts"+query("taskId", id), nil)
	})},
	"task events": {"TASK", "a task's event history; use nextCursor as --after for the next page", func(fs *flag.FlagSet) doer {
		run := fs.String("run", "", "a RUN's events instead")
		after := fs.String("after", "", "events after this cursor (nextCursor from the previous page)")
		limit := fs.String("limit", "", "at most this many events")
		return func(c *cli, a []string) (any, error) {
			if *run != "" {
				id, err := c.runID(*run)
				if err != nil {
					return nil, err
				}
				return c.request("GET", "/v1/events"+query("runId", id, "after", *after, "limit", *limit), nil)
			}
			id, err := c.taskID(a[0])
			if err != nil {
				return nil, err
			}
			return c.request("GET", "/v1/events"+query("taskId", id, "after", *after, "limit", *limit), nil)
		}
	}},
	"task servers": {"TASK", "a task's dev servers and preview", noFlags(func(c *cli, a []string) (any, error) {
		return c.onTask("GET", a[0], "/servers", nil)
	})},
	"task preview": {"TASK", "start a task's preview environment (--stop stops it)", func(fs *flag.FlagSet) doer {
		stop := fs.Bool("stop", false, "stop it instead")
		return func(c *cli, a []string) (any, error) {
			if *stop {
				return c.onTask("DELETE", a[0], "/preview", nil)
			}
			return c.onTask("POST", a[0], "/preview", nil)
		}
	}},

	// Runs and sessions
	"run leave": {"RUN", "leave a stalled run as it is", noFlags(func(c *cli, a []string) (any, error) {
		return c.onRun("POST", a[0], "/leave", nil)
	})},
	"run directives": {"RUN", "what people told a run (steers)", noFlags(func(c *cli, a []string) (any, error) {
		return c.onRun("GET", a[0], "/directives", nil)
	})},
	"session show": {"ID", "one agent session, with its child sessions", noFlags(func(c *cli, a []string) (any, error) {
		return c.request("GET", "/v1/sessions/"+esc(a[0]), nil)
	})},

	// Findings and pull requests
	"finding resolve": {"FINDING STATUS", "a finding's status: resolved, accepted (leave it as it is) or open", func(fs *flag.FlagSet) doer {
		return withNote(fs, func(c *cli, a []string, note string) (any, error) {
			if err := oneOf("STATUS", a[1], "resolved", "accepted", "open"); err != nil {
				return nil, err
			}
			return c.post("/v1/findings/"+esc(a[0])+"/resolve", setIf(obj{"status": a[1]}, "note", note))
		})
	}},
	"pr merge": {"PR", "merge a pull request (id from pr list)", func(fs *flag.FlagSet) doer {
		method := fs.String("method", "", "merge, squash or rebase (default: the organisation's)")
		return func(c *cli, a []string) (any, error) {
			return c.post("/v1/pull-requests/"+esc(a[0])+"/merge", setIf(obj{}, "method", *method))
		}
	}},
	"pr update-branch": {"PR", "bring a pull request's branch up to date with its base", noFlags(func(c *cli, a []string) (any, error) {
		return c.post("/v1/pull-requests/"+esc(a[0])+"/update-branch", nil)
	})},
	"pr rerun-checks": {"PR", "rerun a pull request's failed checks", noFlags(func(c *cli, a []string) (any, error) {
		return c.post("/v1/pull-requests/"+esc(a[0])+"/rerun-failed", nil)
	})},
	"pr reviewers": {"PR LOGIN...", "request reviews on a pull request", noFlags(func(c *cli, a []string) (any, error) {
		return c.post("/v1/pull-requests/"+esc(a[0])+"/reviewers", obj{"logins": a[1:]})
	})},

	// Repository requests
	"repo requests": {"TASK?", "agents' requests for another repository: a task's, or every pending one",
		noFlags(func(c *cli, a []string) (any, error) {
			id := ""
			if a[0] != "" {
				var err error
				if id, err = c.taskID(a[0]); err != nil {
					return nil, err
				}
			}
			return c.request("GET", "/v1/repository-requests"+query("taskId", id), nil)
		})},
	"repo decide": {"REQUEST DECISION", "approve or decline a repository request", func(fs *flag.FlagSet) doer {
		return withNote(fs, func(c *cli, a []string, note string) (any, error) {
			if err := oneOf("DECISION", a[1], "approve", "decline"); err != nil {
				return nil, err
			}
			return c.post("/v1/repository-requests/"+esc(a[0])+"/decide", setIf(obj{"approve": a[1] == "approve"}, "note", note))
		})
	}},

	// Epics
	"epic update": {"EPIC", "change an epic", func(fs *flag.FlagSet) doer {
		title := fs.String("title", "", "a new title")
		desc := fs.String("description", "", "a new description; - reads stdin")
		state := fs.String("state", "", "planned, active, done, or none")
		position := fs.Int("position", -1, "its place in the project's order")
		return func(c *cli, a []string) (any, error) {
			body := setIf(obj{}, "title", *title)
			if *desc != "" {
				d, err := c.text(*desc)
				if err != nil {
					return nil, err
				}
				body["description"] = d
			}
			switch *state {
			case "":
			case "none":
				body["state"] = nil
			default:
				if err := oneOf("--state", *state, "planned", "active", "done", "none"); err != nil {
					return nil, err
				}
				body["state"] = *state
			}
			if *position >= 0 {
				body["position"] = *position
			}
			if len(body) == 0 {
				return nil, errors.New("epic update: nothing to change")
			}
			return c.request("PATCH", "/v1/epics/"+esc(a[0]), body)
		}
	}},
	"epic delete": {"EPIC", "delete an epic; its tasks stay in the project", noFlags(func(c *cli, a []string) (any, error) {
		return c.request("DELETE", "/v1/epics/"+esc(a[0]), nil)
	})},
	"epic metrics": {"EPIC", "an epic's cost and time", noFlags(func(c *cli, a []string) (any, error) {
		return c.request("GET", "/v1/epics/"+esc(a[0])+"/metrics", nil)
	})},
	"epic peers": {"EPIC", "what each task on an epic is doing", noFlags(func(c *cli, a []string) (any, error) {
		return c.request("GET", "/v1/epics/"+esc(a[0])+"/peers", nil)
	})},
	"epic threads": {"EPIC", "the conversations between an epic's agents", noFlags(func(c *cli, a []string) (any, error) {
		return c.request("GET", "/v1/epics/"+esc(a[0])+"/threads", nil)
	})},
	"note list": {"EPIC", "the notes pinned on an epic, which every agent on it reads", noFlags(func(c *cli, a []string) (any, error) {
		return c.request("GET", "/v1/epics/"+esc(a[0])+"/notes", nil)
	})},
	"note add": {"EPIC TEXT", "pin a note on an epic", func(fs *flag.FlagSet) doer {
		replaces := fs.Int("replaces", 0, "the number of a note it replaces")
		return func(c *cli, a []string) (any, error) {
			t, err := c.text(a[1])
			if err != nil {
				return nil, err
			}
			body := obj{"text": t}
			if *replaces > 0 {
				body["replaces"] = *replaces
			}
			return c.post("/v1/epics/"+esc(a[0])+"/notes", body)
		}
	}},
	"note retire": {"EPIC N", "unpin note N from an epic", noFlags(func(c *cli, a []string) (any, error) {
		if n, err := strconv.Atoi(a[1]); err != nil || n <= 0 {
			return nil, errors.New("note retire: N is a note's number")
		}
		return c.post("/v1/epics/"+esc(a[0])+"/notes/"+a[1]+"/retire", nil)
	})},

	// Memory
	"memory search": {"QUERY", "search what agents and people remembered", func(fs *flag.FlagSet) doer {
		project := fs.String("project", "", "only a PROJECT's")
		types := fs.String("type", "", "only these types, comma separated")
		limit := fs.Int("limit", 0, "at most this many")
		words := fs.Bool("words", false, "match words, not meaning")
		return func(c *cli, a []string) (any, error) {
			pid := ""
			if *project != "" {
				var err error
				if pid, err = c.projectID(*project); err != nil {
					return nil, err
				}
			}
			l, mode := "", ""
			if *limit > 0 {
				l = strconv.Itoa(*limit)
			}
			if *words {
				mode = "words"
			}
			return c.request("GET", "/v1/memory/search"+query("q", a[0], "project", pid, "types", *types, "limit", l, "mode", mode), nil)
		}
	}},
	"memory list": {"", "stored memories", func(fs *flag.FlagSet) doer {
		project := fs.String("project", "", "only a PROJECT's")
		text := fs.String("text", "", "only those mentioning this")
		archived := fs.Bool("archived", false, "the archived ones")
		return func(c *cli, _ []string) (any, error) {
			pid := ""
			if *project != "" {
				var err error
				if pid, err = c.projectID(*project); err != nil {
					return nil, err
				}
			}
			arch := ""
			if *archived {
				arch = "true"
			}
			return c.request("GET", "/v1/memory/memories"+query("project", pid, "q", *text, "archived", arch), nil)
		}
	}},
	"memory show": {"ID", "one memory in full", noFlags(func(c *cli, a []string) (any, error) {
		return c.request("GET", "/v1/memory/memories/"+esc(a[0]), nil)
	})},
	"memory add": {"", "remember something for agents to find", func(fs *flag.FlagSet) doer {
		title := fs.String("title", "", "its title (required)")
		content := fs.String("content", "", "what to remember (required); - reads stdin")
		kind := fs.String("kind", "fact", "fact, procedure or note")
		project := fs.String("project", "", "a PROJECT it belongs to (default: the organisation)")
		return func(c *cli, _ []string) (any, error) {
			if *title == "" || *content == "" {
				return nil, errors.New("memory add: --title and --content are required")
			}
			if err := oneOf("--kind", *kind, "fact", "procedure", "note"); err != nil {
				return nil, err
			}
			t, err := c.text(*content)
			if err != nil {
				return nil, err
			}
			body := obj{"title": *title, "content": t, "kind": *kind}
			if *project != "" {
				pid, err := c.projectID(*project)
				if err != nil {
					return nil, err
				}
				body["projectId"] = pid
			}
			return c.post("/v1/memory/memories", body)
		}
	}},
	"memory archive": {"ID", "archive a memory (memory unarchive brings it back)", noFlags(func(c *cli, a []string) (any, error) {
		return c.post("/v1/memory/memories/"+esc(a[0])+"/archive", nil)
	})},
	"memory unarchive": {"ID", "bring an archived memory back", noFlags(func(c *cli, a []string) (any, error) {
		return c.post("/v1/memory/memories/"+esc(a[0])+"/restore", nil)
	})},

	// Brainstorms
	"brainstorm rename": {"ID TITLE", "rename a brainstorm", noFlags(func(c *cli, a []string) (any, error) {
		return c.post("/v1/brainstorms/"+esc(a[0])+"/title", obj{"title": a[1]})
	})},
	"brainstorm retry": {"ID", "retry a brainstorm whose agent failed", noFlags(func(c *cli, a []string) (any, error) {
		return c.post("/v1/brainstorms/"+esc(a[0])+"/retry", nil)
	})},
	"brainstorm archive": {"ID", "archive a brainstorm", noFlags(func(c *cli, a []string) (any, error) {
		return c.post("/v1/brainstorms/"+esc(a[0])+"/archive", nil)
	})},
	"brainstorm unarchive": {"ID", "bring an archived brainstorm back", noFlags(func(c *cli, a []string) (any, error) {
		return c.post("/v1/brainstorms/"+esc(a[0])+"/unarchive", nil)
	})},
	"brainstorm answer": {"ID QUESTION TEXT?", "answer a brainstorm agent's question", func(fs *flag.FlagSet) doer {
		var choices ints
		fs.Var(&choices, "choice", "index of a choice; repeatable")
		return func(c *cli, a []string) (any, error) {
			if a[2] == "" && len(choices) == 0 {
				return nil, errors.New("brainstorm answer: give the text, --choice, or both")
			}
			ans, err := c.itemAnswer(a[2], choices)
			if err != nil {
				return nil, err
			}
			return c.post("/v1/brainstorms/"+esc(a[0])+"/questions/"+esc(a[1])+"/answer", obj{"answers": []obj{ans}})
		}
	}},
	"brainstorm file": {"ID PROPOSAL ITEM...", "file items of a brainstorm's proposal as tasks (ITEM: their indexes)",
		noFlags(func(c *cli, a []string) (any, error) {
			items := []int{}
			for _, s := range a[2:] {
				n, err := strconv.Atoi(s)
				if err != nil || n < 0 {
					return nil, fmt.Errorf("brainstorm file: ITEM %q is not an index", s)
				}
				items = append(items, n)
			}
			return c.post("/v1/brainstorms/"+esc(a[0])+"/file", obj{"proposalId": a[1], "items": items})
		})},
	"brainstorm invite": {"ID PERSON...", "invite people to a brainstorm", func(fs *flag.FlagSet) doer {
		role := fs.String("role", "chat", "chat or read")
		return func(c *cli, a []string) (any, error) {
			if err := oneOf("--role", *role, "chat", "read"); err != nil {
				return nil, err
			}
			return c.post("/v1/brainstorms/"+esc(a[0])+"/people", obj{"people": a[1:], "role": *role})
		}
	}},
}

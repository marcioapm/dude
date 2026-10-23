package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
)

// The commands for what the API gained after the first two sets: feedback
// on a task's work, reviewers' questions, verification, closing, archiving
// and stopping work, pull request conflicts, tasks started from existing
// work, dude's guide, and judgement calls.

func init() {
	for name, cmd := range lifecycleCommands {
		if _, dup := commands[name]; dup {
			panic("dude-cli: command defined twice: " + name)
		}
		commands[name] = cmd
	}
}

// submissionKey makes a feedback submission idempotent: a retry of the same
// call is not a second submission.
func submissionKey() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "cli-" + hex.EncodeToString(b)
}

// lineRange is A or A-B.
func lineRange(s string) (int, int, error) {
	a, b, ranged := strings.Cut(s, "-")
	start, err := strconv.Atoi(a)
	if err != nil || start < 1 {
		return 0, 0, fmt.Errorf("--lines %q: give N or N-M", s)
	}
	end := start
	if ranged {
		if end, err = strconv.Atoi(b); err != nil || end < start {
			return 0, 0, fmt.Errorf("--lines %q: give N or N-M, N ≤ M", s)
		}
	}
	return start, end, nil
}

// ownDrafts are the ids of the caller's drafts on a task's current attempt.
func (c *cli) ownDrafts(taskID string) ([]string, error) {
	o, err := c.getObj("/v1/tasks/" + esc(taskID) + "/feedback")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, f := range list(o, "feedback") {
		if str(f, "status") == "draft" {
			ids = append(ids, str(f, "id"))
		}
	}
	return ids, nil
}

// sourceFlags are create's --from-pr / --from-branch: existing work a new
// task takes over, in a repository given by name.
type sourceFlags struct{ pr, branch, base, mode *string }

func addSourceFlags(fs *flag.FlagSet) sourceFlags {
	return sourceFlags{
		pr:     fs.String("from-pr", "", "start from this pull request (number or URL) in --repo's repository"),
		branch: fs.String("from-branch", "", "start from this branch in --repo's repository"),
		base:   fs.String("base", "", "with --from-branch: the branch it is based on"),
		mode:   fs.String("mode", "", "continue (work on it, the default) or review (only review it)"),
	}
}

// body is the API's source, or nil; repoID is the repository it is in.
func (s sourceFlags) body(repoID string) (obj, error) {
	if *s.pr == "" && *s.branch == "" {
		if *s.base != "" || *s.mode != "" {
			return nil, errors.New("--base and --mode go with --from-pr or --from-branch")
		}
		return nil, nil
	}
	if *s.pr != "" && *s.branch != "" {
		return nil, errors.New("--from-pr or --from-branch, not both")
	}
	if *s.mode != "" {
		if err := oneOf("--mode", *s.mode, "continue", "review"); err != nil {
			return nil, err
		}
	}
	src := obj{"repositoryId": repoID}
	if *s.pr != "" {
		if *s.base != "" {
			return nil, errors.New("--base goes with --from-branch")
		}
		src["kind"], src["pullRequest"] = "pr", *s.pr
	} else {
		src["kind"], src["branch"] = "branch", *s.branch
		setIf(src, "base", *s.base)
	}
	return setIf(src, "mode", *s.mode), nil
}

var lifecycleCommands = map[string]command{
	// Feedback: people's review comments on a task's work, delivered to its
	// agents in batches.
	"feedback": {"TASK", "feedback on a task's work: everyone's submitted items and your drafts", func(fs *flag.FlagSet) doer {
		attempt := fs.Int("attempt", 0, "an earlier attempt's (default: the current one)")
		return func(c *cli, a []string) (any, error) {
			id, err := c.taskID(a[0])
			if err != nil {
				return nil, err
			}
			at := ""
			if *attempt > 0 {
				at = strconv.Itoa(*attempt)
			}
			return c.request("GET", "/v1/tasks/"+esc(id)+"/feedback"+query("attempt", at), nil)
		}
	}},
	"feedback-draft": {"TASK TEXT", "draft a feedback item: on the work in general, or with --path on lines of a run's diff",
		func(fs *flag.FlagSet) doer {
			path := fs.String("path", "", "a file in the run's diff")
			lines := fs.String("lines", "", "with --path: N or N-M")
			side := fs.String("side", "new", "with --path: new or old, the diff's side the lines are on")
			run := fs.String("run", "", "with --path: the RUN whose diff (default: the task's newest)")
			return func(c *cli, a []string) (any, error) {
				t, err := c.text(a[1])
				if err != nil {
					return nil, err
				}
				task, err := c.task(a[0])
				if err != nil {
					return nil, err
				}
				anchor := obj{"kind": "general"}
				if *path != "" {
					if *lines == "" {
						return nil, errors.New("feedback-draft: --path needs --lines")
					}
					start, end, err := lineRange(*lines)
					if err != nil {
						return nil, err
					}
					if err := oneOf("--side", *side, "new", "old"); err != nil {
						return nil, err
					}
					var runID string
					if *run == "" || *run == a[0] {
						runID, err = taskRunID(task, a[0])
					} else {
						runID, err = c.runID(*run)
					}
					if err != nil {
						return nil, err
					}
					anchor = obj{"kind": "code", "runId": runID, "path": *path, "side": *side, "startLine": start, "endLine": end}
				} else if *lines != "" || *run != "" {
					return nil, errors.New("feedback-draft: --lines and --run go with --path")
				}
				return c.post("/v1/tasks/"+esc(str(task, "id"))+"/feedback/drafts", obj{"body": t, "anchor": anchor})
			}
		}},
	"feedback-submit": {"TASK DRAFT?", "send drafts to the task's agents: the ones named, or all of yours",
		noFlags(func(c *cli, a []string) (any, error) {
			id, err := c.taskID(a[0])
			if err != nil {
				return nil, err
			}
			drafts := []string{}
			if a[1] != "" {
				drafts = strings.Split(a[1], ",")
			} else if drafts, err = c.ownDrafts(id); err != nil {
				return nil, err
			}
			if len(drafts) == 0 {
				return nil, errors.New("feedback-submit: you have no drafts on this task")
			}
			return c.post("/v1/tasks/"+esc(id)+"/feedback/submit", obj{"draftIds": drafts, "submissionKey": submissionKey()})
		})},
	"feedback-send": {"TASK TEXT", "draft one general item and send it at once", noFlags(func(c *cli, a []string) (any, error) {
		t, err := c.text(a[1])
		if err != nil {
			return nil, err
		}
		id, err := c.taskID(a[0])
		if err != nil {
			return nil, err
		}
		v, err := c.post("/v1/tasks/"+esc(id)+"/feedback/drafts", obj{"body": t, "anchor": obj{"kind": "general"}})
		if err != nil {
			return nil, err
		}
		draft, _ := v.(obj)
		return c.post("/v1/tasks/"+esc(id)+"/feedback/submit",
			obj{"draftIds": []string{str(draft, "id")}, "submissionKey": submissionKey()})
	})},
	"feedback-delete": {"DRAFT", "delete one of your drafts", noFlags(func(c *cli, a []string) (any, error) {
		return c.request("DELETE", "/v1/feedback/"+esc(a[0]), nil)
	})},
	"feedback-reply": {"ITEM TEXT", "reply on a feedback item", noFlags(func(c *cli, a []string) (any, error) {
		t, err := c.text(a[1])
		if err != nil {
			return nil, err
		}
		return c.post("/v1/feedback/"+esc(a[0])+"/reply", obj{"body": t})
	})},
	"feedback-resolve": {"ITEM", "mark a feedback item resolved", func(fs *flag.FlagSet) doer {
		return withNote(fs, func(c *cli, a []string, note string) (any, error) {
			return c.post("/v1/feedback/"+esc(a[0])+"/resolve", setIf(obj{}, "note", note))
		})
	}},
	"feedback-reopen": {"ITEM TEXT?", "reopen a feedback item, saying why", noFlags(func(c *cli, a []string) (any, error) {
		body := obj{}
		if a[1] != "" {
			t, err := c.text(a[1])
			if err != nil {
				return nil, err
			}
			body["body"] = t
		}
		return c.post("/v1/feedback/"+esc(a[0])+"/reopen", body)
	})},

	// Reviewers' questions hold delivery until the task's owner settles them.
	"review-questions": {"TASK", "questions reviewers asked the task's owner", noFlags(func(c *cli, a []string) (any, error) {
		return c.onTask("GET", a[0], "/review-questions", nil)
	})},
	"review-answer": {"QUESTION TEXT", "answer a reviewer's question (the task's owner)", noFlags(func(c *cli, a []string) (any, error) {
		t, err := c.text(a[1])
		if err != nil {
			return nil, err
		}
		return c.post("/v1/review-questions/"+esc(a[0])+"/answer", obj{"answer": t})
	})},
	"review-withdraw": {"QUESTION", "withdraw a reviewer's question unanswered (the task's owner)", func(fs *flag.FlagSet) doer {
		reason := fs.String("reason", "", "why")
		return func(c *cli, a []string) (any, error) {
			return c.post("/v1/review-questions/"+esc(a[0])+"/withdraw", setIf(obj{}, "reason", *reason))
		}
	}},
	"verification": {"TASK", "how a task's work was verified", noFlags(func(c *cli, a []string) (any, error) {
		return c.onTask("GET", a[0], "/verification", nil)
	})},

	// Ending, archiving and stopping.
	"close": {"TASK RESOLUTION", "close a task without doing it: wont_do, duplicate or cant_reproduce", func(fs *flag.FlagSet) doer {
		dup := fs.String("duplicate-of", "", "with duplicate: the TASK it duplicates")
		removeRel := fs.Bool("remove-relations", false, "remove its relations to other tasks")
		closePR := fs.Bool("close-pr", false, "close its open pull request")
		return withNote(fs, func(c *cli, a []string, note string) (any, error) {
			if err := oneOf("RESOLUTION", a[1], "wont_do", "duplicate", "cant_reproduce"); err != nil {
				return nil, err
			}
			if *dup != "" && a[1] != "duplicate" {
				return nil, errors.New("close: --duplicate-of goes with duplicate")
			}
			body := setIf(setIf(obj{"resolution": a[1]}, "duplicateOf", *dup), "note", note)
			if *removeRel {
				body["removeRelations"] = true
			}
			if *closePR {
				body["closePullRequest"] = true
			}
			return c.onTask("POST", a[0], "/close", body)
		})
	}},
	"reopen": {"TASK", "reopen a closed task", noFlags(func(c *cli, a []string) (any, error) {
		return c.onTask("POST", a[0], "/reopen", nil)
	})},
	"archive": {"TASK", "archive a task; one not ended needs --resolution, and is closed with it", func(fs *flag.FlagSet) doer {
		resolution := fs.String("resolution", "", "wont_do, duplicate or cant_reproduce")
		dup := fs.String("duplicate-of", "", "with duplicate: the TASK it duplicates")
		return withNote(fs, func(c *cli, a []string, note string) (any, error) {
			if *resolution != "" {
				if err := oneOf("--resolution", *resolution, "wont_do", "duplicate", "cant_reproduce"); err != nil {
					return nil, err
				}
			}
			return c.onTask("POST", a[0], "/archive",
				setIf(setIf(setIf(obj{}, "resolution", *resolution), "duplicateOf", *dup), "note", note))
		})
	}},
	"unarchive": {"TASK", "bring an archived task back", noFlags(func(c *cli, a []string) (any, error) {
		return c.onTask("POST", a[0], "/unarchive", nil)
	})},
	"epic-archive": {"EPIC", "archive an epic with its tasks", func(fs *flag.FlagSet) doer {
		endedOnly := fs.Bool("ended-only", false, "archive only its tasks that have ended")
		return func(c *cli, a []string) (any, error) {
			body := obj{}
			if *endedOnly {
				body["withEndedTasks"] = true
			}
			return c.post("/v1/epics/"+esc(a[0])+"/archive", body)
		}
	}},
	"epic-unarchive": {"EPIC", "bring an archived epic back", noFlags(func(c *cli, a []string) (any, error) {
		return c.post("/v1/epics/"+esc(a[0])+"/unarchive", nil)
	})},
	"stop": {"TASK", "stop all of a task's work where it stands (--restart: and start its next attempt)", func(fs *flag.FlagSet) doer {
		restart := fs.Bool("restart", false, "start its next attempt")
		decider := fs.String("decider", "", "with --restart: policy or conductor decides the next attempt")
		return withNote(fs, func(c *cli, a []string, note string) (any, error) {
			body := setIf(obj{}, "note", note)
			if *restart {
				body["andRestart"] = true
			}
			if *decider != "" {
				if !*restart {
					return nil, errors.New("stop: --decider goes with --restart")
				}
				if err := oneOf("--decider", *decider, "policy", "conductor"); err != nil {
					return nil, err
				}
				body["decider"] = *decider
			}
			return c.onTask("POST", a[0], "/stop", body)
		})
	}},
	"resolve-conflicts": {"PR", "have an agent resolve a pull request's merge conflicts", noFlags(func(c *cli, a []string) (any, error) {
		return c.post("/v1/pull-requests/"+esc(a[0])+"/resolve-conflicts", nil)
	})},

	// Dude's own guide and its judgement calls.
	"guide": {"PAGE?", "dude's guide: its pages, or one in full", noFlags(func(c *cli, a []string) (any, error) {
		if a[0] == "" {
			return c.request("GET", "/v1/help", nil)
		}
		return c.request("GET", "/v1/help/"+esc(a[0]), nil)
	})},
	"judgements": {"", "the judgement calls dude's models made lately", func(fs *flag.FlagSet) doer {
		asker := fs.String("asker", "", "only this asker's")
		before := fs.String("before", "", "the page after this call's id")
		limit := fs.Int("limit", 0, "at most this many")
		return func(c *cli, _ []string) (any, error) {
			l := ""
			if *limit > 0 {
				l = strconv.Itoa(*limit)
			}
			return c.request("GET", "/v1/judgements"+query("asker", *asker, "before", *before, "limit", l), nil)
		}
	}},
	"judgement": {"ID", "one judgement call in full", noFlags(func(c *cli, a []string) (any, error) {
		return c.request("GET", "/v1/judgements/"+esc(a[0]), nil)
	})},
}

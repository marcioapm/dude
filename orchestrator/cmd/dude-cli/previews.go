package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"
)

func previewCommand(create bool) func(*flag.FlagSet) doer {
	return func(fs *flag.FlagSet) doer {
		wait := fs.Duration("wait", 0, "wait until every autostart preview server is ready (e.g. 2m; 0: request startup only)")
		var start *bool
		if create {
			start = fs.Bool("start", false, "also wake all autostart preview servers")
		}
		return func(c *cli, a []string) (any, error) {
			if *wait < 0 {
				return nil, errors.New("--wait must not be negative")
			}
			wake := !create || *start
			if !wake {
				var supplied bool
				fs.Visit(func(f *flag.Flag) { supplied = supplied || f.Name == "wait" })
				if supplied {
					return nil, errors.New("preview create: --wait requires --start")
				}
			}
			// One deadline also bounds name resolution and in-flight HTTP calls.
			limit := 30 * time.Second
			if *wait > 0 {
				limit = *wait
			}
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			scoped := *c
			c = &scoped
			c.ctx = ctx
			var preview obj
			var err error
			id := a[0]
			if create {
				pid, e := c.projectID(a[0])
				if e != nil {
					return nil, e
				}
				got, e := c.post("/v1/projects/"+esc(pid)+"/branch-previews", obj{"branch": a[1]})
				if e != nil {
					return nil, e
				}
				preview, _ = got.(obj)
				id = str(preview, "id")
			} else {
				if a[1] != "" {
					id, err = c.branchPreviewID(a[0], a[1])
					if err != nil {
						return nil, err
					}
				}
				preview, err = c.getObj("/v1/branch-previews/" + esc(id))
				if err != nil {
					return nil, err
				}
			}
			if id == "" {
				return nil, errors.New("branch preview response has no id")
			}
			latest := "waiting for server allocation"
			fail := func(err error) (any, error) {
				return nil, fmt.Errorf("preview %s: %w (latest: %s); inspect dude preview show %s and dude api GET /v1/runs/%s/servers", id, err, latest, id, id)
			}
			started := map[string]bool{}
			firstView, staleErr := true, ""
			allocationTimeout := c.previewAllocationTimeout
			if allocationTimeout <= 0 {
				allocationTimeout = 30 * time.Second
			}
			allocationDeadline := time.Now().Add(allocationTimeout)
			for {
				status := str(preview, "status")
				switch status {
				case "pending", "scheduled", "starting", "running", "paused":
				default:
					return fail(fmt.Errorf("preview is not live (%s)", status))
				}
				if !wake {
					latest = "status=" + status
					if len(list(preview, "servers")) > 0 {
						return preview, nil
					}
				} else {
					view, e := c.getObj("/v1/runs/" + esc(id) + "/servers")
					if e != nil {
						return fail(e)
					}
					run, _ := view["run"].(obj)
					status = str(run, "state")
					preview["status"] = status
					latest = "status=" + status
					latest += " luxState=" + str(run, "luxState") + " stage=" + str(run, "previewStage")
					switch str(run, "state") {
					case "completed", "failed", "aborted":
						return fail(fmt.Errorf("preview run ended (%s): %s", str(run, "state"), str(run, "error")))
					}
					if firstView {
						staleErr, firstView = str(run, "error"), false
					}
					if msg := str(run, "error"); msg != "" {
						latest += " error=" + msg
						if len(started) > 0 && msg != staleErr {
							return fail(fmt.Errorf("startup failed: %s", msg))
						}
					}
					servers := map[string]obj{}
					for _, s := range list(view, "servers") {
						servers[str(s, "name")] = s
					}
					expected, allocated, ready := 0, true, true
					wakeRequested := true
					states := []string{}
					for _, recipe := range list(view, "recipes") {
						if recipe["autostartInPreviews"] != true {
							continue
						}
						expected++
						name := str(recipe, "name")
						s, exists := servers[name]
						state := str(s, "state")
						states = append(states, name+"="+state+"/"+str(s, "serverState"))
						if !exists || str(s, "url") == "" || status == "pending" {
							allocated = false
							ready = false
							continue
						}
						requested := started[name]
						current := requested && s["epoch"] != nil && s["epoch"] == run["epoch"] && str(run, "luxState") == "running"
						if str(s, "serverState") == "waking" {
							started[name] = true
						}
						if !started[name] {
							if _, e := c.post("/v1/runs/"+esc(id)+"/servers/"+esc(name)+"/start", nil); e != nil {
								// Another server's wake can move the preview to scheduled
								// before its lux Run has been allocated. Re-read, bounded
								// by the same deadline; all other refusals are failures.
								var he *httpError
								if errors.As(e, &he) && he.status == 409 && (strings.Contains(he.body, `"not_running"`) || strings.Contains(he.body, "the preview is no longer asleep; try again")) {
									if status != "scheduled" && status != "starting" {
										wakeRequested = false
									}
									ready = false
									continue
								}
								return fail(fmt.Errorf("starting %s: %w", name, e))
							}
							started[name] = true
							ready = false // Only a fresh live read after the wake may establish readiness.
						}
						if (current && state == "exited") || (requested && str(s, "serverState") == "no answer") {
							return fail(fmt.Errorf("server %s failed: state=%s serverState=%s", name, state, str(s, "serverState")))
						}
						if state != "ready" || (str(s, "serverState") != "" && str(s, "serverState") != "ready") || (str(s, "runId") != "" && str(s, "runId") != str(run, "luxRunId")) || (s["epoch"] != nil && run["epoch"] != nil && s["epoch"] != run["epoch"]) {
							ready = false
						}
					}
					latest += " servers=[" + strings.Join(states, ", ") + "]"
					if expected == 0 {
						return fail(errors.New("no autostart preview server recipes configured"))
					}
					if allocated && ((*wait == 0 && wakeRequested) || (ready && run["asleep"] != true && str(run, "luxState") == "running")) {
						preview["servers"], preview["run"] = view["servers"], run
						preview["ready"] = ready && run["asleep"] != true && str(run, "luxState") == "running"
						return preview, nil
					}
					if !allocated && time.Now().After(allocationDeadline) {
						return fail(errors.New("timed out allocating preview servers"))
					}
				}
				poll := c.previewPoll
				if poll <= 0 {
					poll = time.Second
				}
				timer := time.NewTimer(poll)
				select {
				case <-ctx.Done():
					timer.Stop()
					return fail(fmt.Errorf("timed out waiting for preview: %w", ctx.Err()))
				case <-timer.C:
				}
				if !wake {
					preview, err = c.getObj("/v1/branch-previews/" + esc(id))
					if err != nil {
						return fail(err)
					}
				}
			}
		}
	}
}

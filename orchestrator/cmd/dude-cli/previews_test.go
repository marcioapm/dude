package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPreviewLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		mode string
		want string
	}{
		{"create asleep", []string{"create", "ABS", "feature/login"}, "", ""},
		{"create start", []string{"create", "ABS", "feature/login", "--start"}, "", ""},
		{"create start wait", []string{"create", "ABS", "feature/login", "--start", "--wait", "1s"}, "", ""},
		{"start id", []string{"start", "run_bp"}, "", ""},
		{"start branch", []string{"start", "ABS", "feature/login", "--wait", "1s"}, "", ""},
		{"already ready", []string{"start", "run_bp", "--wait", "1s"}, "ready", ""},
		{"wake allocation race", []string{"start", "run_bp", "--wait", "1s"}, "race", ""},
		{"running not ready", []string{"start", "run_bp", "--wait", "50ms"}, "starting", "deadline"},
		{"stale process", []string{"start", "run_bp", "--wait", "50ms"}, "stale", "deadline"},
		{"wrong attachment", []string{"start", "run_bp", "--wait", "50ms"}, "attachment", "deadline"},
		{"stale epoch", []string{"start", "run_bp", "--wait", "50ms"}, "epoch", "deadline"},
		{"startup error", []string{"start", "run_bp", "--wait", "1s"}, "error", "image failed"},
		{"server exited", []string{"start", "run_bp", "--wait", "1s"}, "exited", "web failed"},
		{"terminal", []string{"start", "run_bp"}, "terminal", "not live"},
		{"poll HTTP error", []string{"create", "ABS", "feature/login"}, "pollError", "503"},
		{"readiness HTTP error", []string{"start", "run_bp", "--wait", "1s"}, "viewError", "503"},
		{"start HTTP error", []string{"start", "run_bp"}, "startError", "starting web"},
		{"blocked HTTP deadline", []string{"start", "run_bp", "--wait", "30ms"}, "blocked", "deadline"},
		{"empty allocation", []string{"start", "run_bp", "--wait", "30ms"}, "empty", "deadline"},
		{"missing recipe", []string{"start", "run_bp", "--wait", "30ms"}, "noRecipes", "no autostart"},
		{"missing branch", []string{"start", "ABS", "missing"}, "", "not_found"},
		{"missing id", []string{"start", "run_missing"}, "", "404"},
		{"wait requires start", []string{"create", "ABS", "feature/login", "--wait", "0"}, "", "requires --start"},
		{"negative wait", []string{"start", "run_bp", "--wait=-1s"}, "", "must not be negative"},
		{"invalid duration", []string{"start", "run_bp", "--wait", "oops"}, "", "invalid value"},
		{"create negative wait", []string{"create", "ABS", "feature/login", "--start", "--wait=-1s"}, "", "must not be negative"},
		{"unknown flag", []string{"start", "run_bp", "--start"}, "", "flag provided but not defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts, reads, polls, views := []string{}, 0, 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "POST" {
					posts = append(posts, r.URL.Path)
				}
				switch r.URL.Path {
				case "/v1/projects":
					fmt.Fprint(w, `{"projects":[{"id":"prj_1","key":"ABS"}]}`)
				case "/v1/projects/prj_1/branch-previews":
					if r.Method == "GET" {
						fmt.Fprint(w, `{"previews":[{"id":"run_bp","branch":"feature/login"}]}`)
						return
					}
					fmt.Fprint(w, `{"id":"run_bp","status":"pending","servers":[]}`)
				case "/v1/branch-previews/run_bp":
					polls++
					if tc.mode == "pollError" {
						w.WriteHeader(503)
						return
					}
					status := "running"
					if tc.mode == "terminal" {
						status = "completed"
					}
					json.NewEncoder(w).Encode(obj{"id": "run_bp", "status": status, "servers": []obj{{"name": "web", "url": "https://web.test"}}})
				case "/v1/runs/run_bp/servers":
					views++
					if tc.mode == "blocked" {
						<-r.Context().Done()
						return
					}
					if tc.mode == "viewError" {
						w.WriteHeader(503)
						return
					}
					state, serverState, attachment := "ready", "ready", "lux_1"
					if reads == 0 && tc.mode != "ready" || tc.mode == "starting" {
						state = "starting"
					}
					if tc.mode == "stale" {
						state, serverState = "ready", "asleep"
					}
					if tc.mode == "attachment" {
						attachment = "lux_old"
					}
					if tc.mode == "exited" {
						state = "exited"
					}
					reads++
					if reads == 2 && tc.mode == "" {
						state = "starting"
					}
					run := obj{"id": "run_bp", "luxRunId": "lux_1", "state": "running", "luxState": "running", "asleep": false, "epoch": 2}
					if tc.mode == "error" {
						run["error"] = "image failed"
					}
					servers := []obj{{"name": "web", "state": state, "serverState": serverState, "runId": attachment, "url": "https://web.test"}, {"name": "api", "state": "ready", "serverState": "ready", "runId": "lux_1", "url": "https://api.test"}, {"name": "unrelated", "state": "starting"}}
					if tc.mode == "exited" {
						servers[0]["epoch"] = 2
					}
					if tc.mode == "epoch" {
						servers[0]["epoch"] = 1
					}
					// Allocation is asynchronous even after the preview itself exists.
					if views == 1 && tc.mode == "" || tc.mode == "empty" {
						servers = nil
					}
					recipes := []obj{{"name": "web", "autostartInPreviews": true}, {"name": "api", "autostartInPreviews": true}, {"name": "manual", "autostartInPreviews": false}}
					if tc.mode == "noRecipes" {
						recipes = nil
					}
					json.NewEncoder(w).Encode(obj{"run": run, "servers": servers, "recipes": recipes})
				case "/v1/runs/run_bp/servers/web/start", "/v1/runs/run_bp/servers/api/start":
					if tc.mode == "race" && views == 1 {
						w.WriteHeader(409)
						fmt.Fprint(w, `{"error":{"code":"conflict","message":"the preview is no longer asleep; try again"}}`)
						return
					}
					if tc.mode == "startError" {
						w.WriteHeader(503)
						return
					}
					if r.ContentLength > 0 {
						t.Error("start must send empty body")
					}
					fmt.Fprint(w, `{}`)
				default:
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			began := time.Now()
			out, err := runCLI(t, srv, "", append([]string{"preview"}, tc.args...)...)
			if tc.mode == "blocked" && time.Since(began) > time.Second {
				t.Fatal("HTTP call outlived wait deadline")
			}
			if tc.want != "" {
				switch tc.name {
				case "missing branch", "missing id", "wait requires start", "negative wait", "create negative wait", "invalid duration", "unknown flag":
					if len(posts) != 0 {
						t.Fatalf("invalid or missing preview must not mutate: %v", posts)
					}
				}
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("wanted %q: out=%s err=%v", tc.want, out, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			createOnly := tc.name == "create asleep"
			wakes := 0
			for _, path := range posts {
				if strings.HasSuffix(path, "/start") {
					wakes++
				} else if tc.args[0] != "create" {
					t.Fatalf("start created: %v", posts)
				}
			}
			if createOnly {
				if !strings.Contains(out, "https://web.test") {
					t.Fatalf("create printed no polled URL: %s", out)
				}
				if wakes != 0 || views != 0 || polls == 0 {
					t.Fatalf("create lifecycle: posts=%v views=%d polls=%d", posts, views, polls)
				}
			} else if wakes != 2 && !(tc.mode == "race" && wakes == 4) {
				t.Fatalf("must wake all expected servers: %v", posts)
			}
			if strings.Contains(strings.Join(tc.args, " "), "--wait") && !strings.Contains(out, `"ready": true`) {
				t.Fatalf("not ready: %s", out)
			}
		})
	}
}

func TestPreviewWakeInProgress(t *testing.T) {
	for _, mode := range []string{"waking", "conflict", "cold conflict"} {
		t.Run(mode, func(t *testing.T) {
			views, posts := 0, 0
			began := time.Now()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "POST":
					posts++
					w.WriteHeader(409)
					fmt.Fprint(w, `{"error":{"code":"not_running"}}`)
				case strings.HasSuffix(r.URL.Path, "/servers"):
					views++
					state := "starting"
					if mode == "waking" {
						state = "waking"
					}
					fmt.Fprintf(w, `{"run":{"state":"starting","luxState":"starting"},"recipes":[{"name":"web","autostartInPreviews":true}],"servers":[{"name":"web","url":"https://web.test","state":"starting","serverState":%q}]}`, state)
				default:
					fmt.Fprint(w, `{"id":"run_bp","status":"starting"}`)
				}
			}))
			defer srv.Close()
			args := []string{"preview", "start", "run_bp", "--wait", "0"}
			if mode == "cold conflict" {
				args[len(args)-1] = "31s"
			}
			out, err := runCLI(t, srv, "", args...)
			if mode == "cold conflict" {
				if err == nil || !strings.Contains(err.Error(), "deadline") || strings.Contains(err.Error(), "allocating") {
					t.Fatalf("cold wake must use wait deadline, not allocation cap: %v", err)
				}
				return
			}
			wantPosts := 1
			if mode == "waking" {
				wantPosts = 0
			}
			if err != nil || views != 1 || posts != wantPosts || !strings.Contains(out, `"ready": false`) || time.Since(began) > time.Second {
				t.Fatalf("wake already requested: views=%d posts=%d out=%s err=%v", views, posts, out, err)
			}
		})
	}
}

func TestPreviewFailureFreshness(t *testing.T) {
	for _, failure := range []string{"exited", "no answer"} {
		for _, mode := range []string{"pre-wake", "old epoch", "lux starting", "current"} {
			t.Run(failure+"/"+mode, func(t *testing.T) {
				views, posts := 0, 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch {
					case r.Method == "POST":
						posts++
						fmt.Fprint(w, `{}`)
					case strings.HasSuffix(r.URL.Path, "/servers"):
						views++
						state, serverState, luxState, epoch := "ready", "ready", "running", 2
						if views <= 2 || mode == "current" {
							if failure == "exited" {
								state = failure
							} else {
								serverState = failure
							}
						}
						if views == 2 {
							switch mode {
							case "pre-wake":
								state, serverState = "ready", "ready"
							case "old epoch":
								epoch = 1
							case "lux starting":
								luxState = "starting"
							}
						}
						fmt.Fprintf(w, `{"run":{"state":"running","luxState":%q,"epoch":2},"recipes":[{"name":"web","autostartInPreviews":true}],"servers":[{"name":"web","url":"https://web.test","state":%q,"serverState":%q,"epoch":%d}]}`, luxState, state, serverState, epoch)
					default:
						fmt.Fprint(w, `{"id":"run_bp","status":"running"}`)
					}
				}))
				defer srv.Close()
				out, err := runCLI(t, srv, "", "preview", "start", "run_bp", "--wait", "1s")
				if mode == "current" {
					if err == nil || !strings.Contains(err.Error(), "web failed") || views != 2 || posts != 1 {
						t.Fatalf("current failure must fail fast: views=%d posts=%d err=%v", views, posts, err)
					}
					return
				}
				wantViews := 3
				if mode == "pre-wake" {
					wantViews = 2
				}
				if err != nil || views != wantViews || posts != 1 || !strings.Contains(out, `"ready": true`) {
					t.Fatalf("stale failure must recover: views=%d posts=%d out=%s err=%v", views, posts, out, err)
				}
			})
		}
	}
}

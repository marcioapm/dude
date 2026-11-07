package fakelux

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

func execTestRun(t *testing.T, s *Server, spec lux.Spec) *Run {
	t.Helper()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	run := &Run{ID: "exec-test", State: "running", Epoch: 1, starts: 1, Spec: raw, workspace: t.TempDir()}
	run.cond = sync.NewCond(&s.mu)
	s.runs[run.ID] = run
	return run
}

func execTestWait(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// A completed handshake proves the handler was admitted; a marker proves the
// shell and its pipe-inheriting child started before shutdown is requested.
func TestExecCloseCancelsWebsocket(t *testing.T) {
	for _, running := range []bool{false, true} {
		name := "waiting-for-command"
		if running {
			name = "running-command"
		}
		t.Run(name, func(t *testing.T) {
			s := New("", "k", nil)
			run := execTestRun(t, s, lux.Spec{})
			handlerDone := make(chan struct{})
			handler := s.Handler()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handlerDone)
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)
			t.Cleanup(s.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/runs/"+run.ID+"/exec",
				&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer k"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer ws.CloseNow()
			if running {
				probe := newCommandProbe(t)
				if err := wsjson.Write(ctx, ws, map[string]any{"command": []string{"sh", "-eu", "-c", holdScript, "sh", probe.dir}}); err != nil {
					t.Fatal(err)
				}
				probe.awaitReady(t)
			}
			closed := make(chan struct{})
			go func() { s.Close(); close(closed) }()
			execTestWait(t, closed, "Close")
			execTestWait(t, handlerDone, "exec handler cancellation")
		})
	}
}

func TestExecBeforeStopCancellation(t *testing.T) {
	for _, bodyRead := range []bool{false, true} {
		name := "command"
		if bodyRead {
			name = "http-body"
		}
		t.Run(name, func(t *testing.T) {
			s := New("", "k", nil)
			t.Cleanup(s.Close)
			probe := newCommandProbe(t)
			spec := lux.Spec{Workload: lux.Workload{BeforeStop: &lux.BeforeStop{
				Command: []string{"sh", "-eu", "-c", holdScript, "sh", probe.dir}, Timeout: "1h",
			}}}
			ready := make(chan struct{})
			if bodyRead {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					close(ready)
					<-r.Context().Done()
				}))
				t.Cleanup(srv.Close)
				spec.Workload.Services = []lux.Service{{Name: "dude", URL: srv.URL}}
			}
			run := execTestRun(t, s, spec)
			done := make(chan struct{})
			go func() { s.Timeout(run.ID); close(done) }()
			if bodyRead {
				execTestWait(t, ready, "HTTP headers")
			} else {
				probe.awaitReady(t)
			}
			// Shutdown must acquire mu even while the hook's command or body
			// read is blocked, and the hook must not complete the Timeout.
			closed := make(chan struct{})
			go func() { s.Close(); close(closed) }()
			execTestWait(t, closed, "Close during beforeStop")
			execTestWait(t, done, "Timeout cancellation")
			s.mu.Lock()
			defer s.mu.Unlock()
			if run.State != "running" || len(run.records) != 1 {
				t.Fatalf("shutdown mutated run: state=%s records=%d", run.State, len(run.records))
			}
		})
	}
}

func TestExecBeforeStopStateChangeAllowsCallerToContinue(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "beforeStop"
		if timeout {
			name = "Timeout"
		}
		t.Run(name, func(t *testing.T) {
			s := New("", "k", nil)
			t.Cleanup(s.Close)
			ready, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(ready)
				select {
				case <-release:
					_, _ = io.WriteString(w, "{}")
				case <-r.Context().Done():
				}
			}))
			defer func() {
				once.Do(func() { close(release) })
				srv.Close()
			}()
			run := execTestRun(t, s, lux.Spec{Workload: lux.Workload{
				BeforeStop: &lux.BeforeStop{Command: []string{"true"}},
				Services:   []lux.Service{{Name: "dude", URL: srv.URL}},
			}})
			done := make(chan struct{})
			var proceed bool
			go func() {
				defer close(done)
				if timeout {
					s.Timeout(run.ID)
					return
				}
				s.mu.Lock()
				defer s.mu.Unlock()
				proceed = s.beforeStop(run)
			}()
			execTestWait(t, ready, "hook request")
			s.mu.Lock()
			run.State = "stopping"
			s.mu.Unlock()
			once.Do(func() { close(release) })
			execTestWait(t, done, "state-changed hook")
			if !timeout && !proceed {
				t.Fatal("state-only change prevented caller from re-evaluating current state")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if run.State != "stopping" || len(run.records) != 1 {
				t.Fatalf("stale hook mutated run: state=%s records=%d", run.State, len(run.records))
			}
		})
	}
}

type execTestTransport func(*http.Request) (*http.Response, error)

func (f execTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A transport can take time to unwind after cancellation. Close must join the
// entire synchronous Timeout, not merely signal its context and return.
func TestExecCloseJoinsTimeout(t *testing.T) {
	s := New("", "k", nil)
	t.Cleanup(s.Close)
	ready, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	original := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: execTestTransport(func(r *http.Request) (*http.Response, error) {
		close(ready)
		<-r.Context().Done()
		close(canceled)
		<-release
		return nil, r.Context().Err()
	})}
	defer func() { http.DefaultClient = original }()
	run := execTestRun(t, s, lux.Spec{Workload: lux.Workload{
		BeforeStop: &lux.BeforeStop{Command: []string{"true"}},
		Services:   []lux.Service{{Name: "dude", URL: "http://hook.invalid"}},
	}})
	done := make(chan struct{})
	go func() { s.Timeout(run.ID); close(done) }()
	execTestWait(t, ready, "admitted Timeout")
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	execTestWait(t, canceled, "hook cancellation")
	select {
	case <-closed:
		t.Fatal("Close returned before Timeout unwound")
	default:
	}
	once.Do(func() { close(release) })
	execTestWait(t, closed, "Close joining Timeout")
	execTestWait(t, done, "Timeout return")
}

func TestExecTimeoutRejectsShutdownAdmission(t *testing.T) {
	s := New("", "k", nil)
	run := execTestRun(t, s, lux.Spec{})
	s.Close()
	s.Timeout(run.ID)
	if run.State != "running" || len(run.records) != 0 {
		t.Fatalf("Timeout mutated run after shutdown: state=%s records=%d", run.State, len(run.records))
	}
}

func TestExecBeforeStopSerializesStopAndTerminate(t *testing.T) {
	for _, first := range []string{"stop", "terminate"} {
		t.Run(first+"-first", func(t *testing.T) {
			s := New("", "k", nil)
			t.Cleanup(s.Close)
			ready, release := make(chan struct{}), make(chan struct{})
			var signal, unblock sync.Once
			hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				signal.Do(func() { close(ready) })
				select {
				case <-release:
					_, _ = io.WriteString(w, "{}")
				case <-r.Context().Done():
				}
			}))
			defer func() {
				unblock.Do(func() { close(release) })
				hook.Close()
			}()
			run := execTestRun(t, s, lux.Spec{Workload: lux.Workload{
				BeforeStop: &lux.BeforeStop{Command: []string{"sh", "-c", "printf hook >> side-effects"}},
				Services:   []lux.Service{{Name: "dude", URL: hook.URL}},
			}})
			handler := s.Handler()
			call := func(action string) (<-chan struct{}, *httptest.ResponseRecorder) {
				done := make(chan struct{})
				res := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v1/runs/"+run.ID+"/"+action, nil)
				req.Header.Set("Authorization", "Bearer k")
				go func() {
					defer close(done)
					handler.ServeHTTP(res, req)
				}()
				return done, res
			}
			firstDone, firstRes := call(first)
			execTestWait(t, ready, "first hook HTTP request")
			second := "terminate"
			if first == "terminate" {
				second = "stop"
			}
			secondDone, secondRes := call(second)
			// Both handlers append Calls under mu immediately before beforeStop.
			// Observing both calls while the first hook is blocked proves overlap,
			// without adding a scheduling hook or a test-only waiter counter.
			deadline := time.After(5 * time.Second)
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for {
				s.mu.Lock()
				both := len(run.Calls) == 2
				s.mu.Unlock()
				if both {
					break
				}
				select {
				case <-deadline:
					t.Fatal("second handler did not enter beforeStop")
				case <-tick.C:
				}
			}
			unblock.Do(func() { close(release) })
			execTestWait(t, firstDone, "first stop handler")
			execTestWait(t, secondDone, "second stop handler")
			if firstRes.Code != http.StatusAccepted || secondRes.Code != http.StatusAccepted {
				t.Fatalf("responses: first=%d second=%d", firstRes.Code, secondRes.Code)
			}
			s.mu.Lock()
			state := run.State
			s.mu.Unlock()
			if !lux.Terminated(state) {
				t.Fatalf("final state=%s, want terminated", state)
			}
			data, err := os.ReadFile(filepath.Join(run.workspace, "side-effects"))
			if err != nil || string(data) != "hook" {
				t.Fatalf("hook side effects=%q err=%v; want exactly one execution", data, err)
			}
		})
	}
}

func TestExecBeforeStopWaitCanceledByShutdown(t *testing.T) {
	s := New("", "k", nil)
	t.Cleanup(s.Close)
	run := execTestRun(t, s, lux.Spec{})
	// Model an owner still unwinding; cancellation must wake its waiter
	// without requiring the owner's completion channel to close first.
	run.beforeStopDone = make(chan struct{})
	entered, done := make(chan struct{}), make(chan struct{})
	var proceed bool
	go func() {
		defer close(done)
		s.mu.Lock()
		defer s.mu.Unlock()
		close(entered)
		proceed = s.beforeStop(run)
	}()
	execTestWait(t, entered, "beforeStop waiter")
	// Acquiring mu proves the waiter released it to wait on the channel.
	s.mu.Lock()
	s.mu.Unlock()
	s.Close()
	execTestWait(t, done, "canceled beforeStop waiter")
	if proceed {
		t.Fatal("shutdown allowed waiting caller to continue")
	}
}

func TestExecBeforeStopDiscardsSupersededResult(t *testing.T) {
	s := New("", "k", nil)
	t.Cleanup(s.Close)
	ready, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(ready)
		select {
		case <-release:
			_, _ = io.WriteString(w, "{}")
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	run := execTestRun(t, s, lux.Spec{Workload: lux.Workload{
		BeforeStop: &lux.BeforeStop{Command: []string{"true"}},
		Services:   []lux.Service{{Name: "dude", URL: srv.URL}},
	}})
	done := make(chan struct{})
	go func() { s.Timeout(run.ID); close(done) }()
	execTestWait(t, ready, "hook HTTP request")
	s.mu.Lock()
	run.starts++
	s.mu.Unlock()
	once.Do(func() { close(release) })
	execTestWait(t, done, "superseded hook")
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.State != "running" || len(run.records) != 1 {
		t.Fatalf("stale hook mutated run: state=%s records=%d", run.State, len(run.records))
	}
}

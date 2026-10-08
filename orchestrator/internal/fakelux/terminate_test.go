package fakelux

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// post asks the fake for a Run's action by its raw path: the lux client
// has no Terminate.
func post(t *testing.T, fake *Server, id, action string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "/v1/runs/"+id+"/"+action, nil)
	req.Header.Set("Authorization", "Bearer k")
	rec := httptest.NewRecorder()
	fake.Handler().ServeHTTP(rec, req)
	return rec.Code
}

func refused(err error, status int, code string) bool {
	le, ok := lux.AsError(err)
	return ok && le.Status == status && le.Code == code
}

// The fake ends a Run for good as the lux it plays: terminated, by
// /terminate or its deprecated alias /cancel; or, as a lux from before,
// cancelled, by /cancel only. Either way the Run does not resume.
func TestTheFakeEndsARunAsEachLuxDoes(t *testing.T) {
	for _, c := range []struct {
		name, action, want string
		old                bool
		status             int
	}{
		{"terminate", "terminate", "terminated", false, 202},
		{"cancel alias", "cancel", "terminated", false, 202},
		{"cancel before terminate", "cancel", "cancelled", true, 202},
		{"terminate before it existed", "terminate", "", true, 404},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
			fake.CancelledState = c.old
			client, run := submitGeneric(t, fake, "k-"+c.name)
			waitState(t, client, run.ID, "running")
			if got := post(t, fake, run.ID, c.action); got != c.status {
				t.Fatalf("POST %s: %d, want %d", c.action, got, c.status)
			}
			if c.want == "" {
				if got := fake.State(run.ID); got != "running" {
					t.Errorf("an endpoint this lux lacks ended the Run: %s", got)
				}
				return
			}
			if got := fake.State(run.ID); got != c.want {
				t.Errorf("state %s, want %s", got, c.want)
			}
			if _, err := client.Resume(context.Background(), run.ID, lux.ResumeInput{}); !refused(err, 409, "not_resumable") {
				t.Errorf("resuming a Run ended for good: %v", err)
			}
		})
	}
}

// A succeeded Run resumes on lux now, as a stopped one does; a lux from
// before refuses it.
func TestASucceededRunResumesOnlyOnTheNewLux(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "old"}[old], func(t *testing.T) {
			fake := New("", "k", func(map[string]any) Behaviour { return Behaviour{Hang: true} })
			fake.CancelledState = old
			client, run := submitGeneric(t, fake, "k")
			waitState(t, client, run.ID, "running")
			fake.Succeed(run.ID)
			_, err := client.Resume(context.Background(), run.ID, lux.ResumeInput{})
			if old {
				if !refused(err, 409, "not_resumable") {
					t.Errorf("an old lux resumed a succeeded Run: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			waitState(t, client, run.ID, "running")
		})
	}
}

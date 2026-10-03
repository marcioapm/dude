package phases

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// The sender has read its batch — a person's message C, then a wake
// note's retry B — when the first note A's late read arrives, which
// withdraws an unsent retry. While the sender is sending C, B is withdrawn
// and never sent to lux. While it is sending B itself, B is in flight: it
// counts as sent, is not withdrawn, and is recorded sent (two notes heard,
// the accepted duplicate).
func TestARetryWithdrawnWhileTheSenderRunsIsNeverSent(t *testing.T) {
	for _, blockIn := range []string{"C", "B"} {
		t.Run("sending "+blockIn, func(t *testing.T) {
			w := newWakeWorld(t)
			a := w.wake()
			w.exec(`UPDATE directives SET sent_at = now() WHERE run_id = 'run_'||$1`)
			w.receive(failedAfterAccepted(a, "the agent stopped"))
			w.exec(`INSERT INTO directives (id, organization_id, task_id, run_id, text, created_at)
				VALUES ('dir_c', $1, 'wi_'||$1, 'run_'||$1, 'and the docs', now() - interval '1 minute')`)
			w.wake()
			var b string
			_ = w.owner.QueryRow(context.Background(), `SELECT COALESCE(max(directive_id), '') FROM conductor_wake_attempts
				WHERE directive_id <> $1`, a).Scan(&b)
			if b == "" {
				t.Fatalf("no retry queued after %q", a)
			}
			block := map[string]string{"C": "dir_c", "B": b}[blockIn]

			var mu sync.Mutex
			var sent []string
			in, release := make(chan struct{}), make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
				var body struct {
					RequestID string `json:"requestId"`
				}
				_ = json.NewDecoder(req.Body).Decode(&body)
				mu.Lock()
				sent = append(sent, body.RequestID)
				mu.Unlock()
				if body.RequestID == block {
					close(in)
					<-release
				}
				rw.WriteHeader(http.StatusAccepted)
			}))
			defer srv.Close()
			w.s.Lux = lux.New(srv.URL, "k")

			r := w.tr.run
			r.Status, r.LuxState, r.LuxRunID = statusRunning, "running", "lux_"+w.org
			done := make(chan error, 1)
			go func() {
				_, err := w.s.deliverDirectives(context.Background(), r)
				done <- err
			}()
			<-in
			w.receive(consumed(a))
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			var failed, sentAt bool
			_ = w.owner.QueryRow(context.Background(), `SELECT failed_at IS NOT NULL, sent_at IS NOT NULL FROM directives WHERE id = $1`, b).
				Scan(&failed, &sentAt)
			if blockIn == "C" {
				if slices.Contains(sent, b) || sentAt {
					t.Errorf("the withdrawn retry %s was sent to lux (sent %v, recorded %v)", b, sent, sentAt)
				}
				if !failed {
					t.Errorf("the retry %s is not withdrawn", b)
				}
				return
			}
			if failed || !sentAt {
				t.Errorf("the retry in flight: withdrawn %v, recorded sent %v; want kept and sent", failed, sentAt)
			}
		})
	}
}

package orchestrator_test

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/embeddings"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// memoryEvents are the ledger events about a memory after cursor that
// viewer may read, by the ledger's own visibility predicate (the one the
// public history and the live stream apply: session_visible).
func (s *sessionWorld) memoryEvents(viewer, memoryID string, after int64) []string {
	s.t.Helper()
	var out []string
	if err := s.app.InOrg(t0(), s.org, func(tx pgx.Tx) error {
		rows, err := tx.Query(t0(), `SELECT event_type || ' ' || payload::text FROM events
			WHERE cursor > $1 AND payload->>'memoryId' = $2 AND session_visible(session_id, $3) ORDER BY cursor`, after, memoryID, viewer)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		s.t.Fatal(err)
	}
	return out
}

func (s *sessionWorld) cursor() int64 {
	s.t.Helper()
	var c int64
	if err := s.owner.QueryRow(t0(), `SELECT coalesce(max(cursor), 0) FROM events`).Scan(&c); err != nil {
		s.t.Fatal(err)
	}
	return c
}

// A member changing a session's memory — its title, archiving it,
// restoring it — records that on the session: its members read the event,
// nobody else does (an invitee, someone else, an admin not in it), and
// the private title goes nowhere else. An organisation memory's changes
// stay everyone's, as before.
func TestChangingASessionMemoryIsRecordedOnTheSession(t *testing.T) {
	for _, action := range []string{"update", "archive", "restore"} {
		t.Run(action, func(t *testing.T) {
			s := newSessionWorld(t)
			s.withTools()
			id := s.session()
			// The organisation's admin, as an accepted member, makes the change;
			// a second admin is not in the session.
			s.join(id, s.admin, "chat")
			otherAdmin := "per_admin2_" + s.org
			mustExec(t, s.owner, `INSERT INTO people (id, organization_id, name, role) VALUES ($1, $2, 'Bea Boss', 'admin')`, otherAdmin, s.org)
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people", map[string]any{"people": []string{s.ana}, "role": "chat"})
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "plan"})
			run := s.started(id)
			status, out := s.tool(run, "remember", `{"title":"Confidential zephyr acquisition","content":"Private terms."}`)
			if status != 200 {
				t.Fatalf("remember: %d %v", status, out)
			}
			mid := out["id"].(string)
			if action == "restore" {
				s.ok(s.admin, "POST", "/internal/memory/memories/"+mid+"/archive", nil)
			}
			after := s.cursor()
			if action == "update" {
				s.ok(s.admin, "PATCH", "/internal/memory/memories/"+mid, map[string]any{"title": "Confidential zephyr acquisition, signed"})
			} else {
				s.ok(s.admin, "POST", "/internal/memory/memories/"+mid+"/"+action, nil)
			}
			want := map[string]string{"update": "memory.updated", "archive": "memory.archived", "restore": "memory.restored"}[action]
			if n := s.count(`SELECT count(*) FROM events WHERE cursor > $1 AND event_type = $2 AND payload->>'memoryId' = $3 AND session_id = $4`,
				after, want, mid, id); n != 1 {
				t.Errorf("%s on the session: %d events, want 1", want, n)
			}
			for _, member := range []string{s.marcio, s.admin} {
				if got := s.memoryEvents(member, mid, after); len(got) != 1 {
					t.Errorf("member %s reads %d events, want the %s: %v", member, len(got), want, got)
				}
			}
			// A pending invitee, someone else, an admin not in it.
			for _, who := range []string{s.ana, s.outsider, otherAdmin} {
				if got := s.memoryEvents(who, mid, after); len(got) != 0 {
					t.Errorf("%s reads the session memory's change: %v", who, got)
				}
				if n := s.count(`SELECT count(*) FROM events WHERE cursor > $1 AND session_visible(session_id, $2)
					AND payload::text ILIKE '%zephyr%'`, after, who); n != 0 {
					t.Errorf("%s reads the private title in %d events", who, n)
				}
			}
		})
	}

	t.Run("an organisation memory's changes stay everyone's", func(t *testing.T) {
		s := newSessionWorld(t)
		created := s.ok(s.admin, "POST", "/internal/memory/memories", map[string]any{"title": "Meter windows", "content": "24h"})
		mid := created["id"].(string)
		after := s.cursor()
		s.ok(s.admin, "PATCH", "/internal/memory/memories/"+mid, map[string]any{"title": "Meter windows, 48h"})
		s.ok(s.admin, "POST", "/internal/memory/memories/"+mid+"/archive", nil)
		s.ok(s.admin, "POST", "/internal/memory/memories/"+mid+"/restore", nil)
		if n := s.count(`SELECT count(*) FROM events WHERE cursor > $1 AND payload->>'memoryId' = $2 AND session_id IS NULL`, after, mid); n != 3 {
			t.Errorf("%d session-less events, want 3", n)
		}
		if got := s.memoryEvents(s.outsider, mid, after); len(got) != 3 {
			t.Errorf("someone else reads %d of the organisation memory's changes, want 3: %v", len(got), got)
		}
	})
}

// sameVector embeds every text as one constant vector: whatever is
// searched for is nearest to every document stored with that vector.
type sameVector struct{}

const sameVectorModel = "test/same-vector"

func (sameVector) Embed(_ context.Context, texts []string, _ embeddings.Purpose) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, 768)
		for j := range out[i] {
			out[i][j] = 0.1
		}
	}
	return out, nil
}
func (sameVector) Model() string   { return sameVectorModel }
func (sameVector) Dimensions() int { return 768 }

// A session's memory found by meaning alone — by a query sharing no word
// with it — is found by its members and its agent, and by nobody else:
// someone else, an admin not in it, a pending invitee, a member once
// removed, a task's agent in the same project, another session's agent.
func TestASessionMemoryFoundByMeaningIsItsMembersAlone(t *testing.T) {
	s := newSessionWorld(t)
	tools := httptest.NewServer((&agenttools.Server{DB: s.app, Log: quiet, Forges: forge.Resolver{DB: s.app}, Embedder: sameVector{}}).Handler())
	t.Cleanup(tools.Close)
	s.syncer.Agent.ToolsURL, s.syncer.Agent.ToolsService = tools.URL, true
	apiSrv := httptest.NewServer((&api.Server{DB: s.app, Lux: s.syncer.Lux, Token: "svc", Log: quiet, Kick: func() {}, Embedder: sameVector{}}).Handler())
	t.Cleanup(apiSrv.Close)
	s.api = apiSrv.URL

	id := s.session()
	s.join(id, s.joao, "read")
	s.join(id, s.ana, "chat")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people", map[string]any{"people": []string{s.outsider}, "role": "chat"})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "plan"})
	run := s.started(id)
	status, out := s.tool(run, "remember", `{"title":"Confidential zephyr acquisition","content":"Private terms."}`)
	if status != 200 {
		t.Fatalf("remember: %d %v", status, out)
	}
	mid := out["id"].(string)
	orgMem := "mem_vec_org_" + s.org
	mustExec(t, s.owner, `INSERT INTO memories (id, organization_id, title, content, kind, author_kind)
		VALUES ($1, $2, 'Quarterly metering', 'Counted daily.', 'fact', 'person')`, orgMem, s.org)
	s.until("both memories to be indexed", func() bool {
		return s.count(`SELECT count(*) FROM search_documents WHERE source_id = ANY($1)`, []string{mid, orgMem}) == 2
	})
	mustExec(t, s.owner, `UPDATE search_documents SET embedding = array_fill(0.1, ARRAY[768])::halfvec, embedding_model = $2
		WHERE source_id = ANY($1)`, []string{mid, orgMem}, sameVectorModel)
	// No word of it is in either memory: only their meaning can find them.
	const query = "unrelated pelican"
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people/"+s.ana+"/remove", nil)

	search := func(who string) (string, map[string]any) {
		t.Helper()
		status, out := s.as(who, "GET", "/internal/memory/search?q="+url.QueryEscape(query), nil)
		if status != 200 {
			t.Fatalf("search as %s: %d %v", who, status, out)
		}
		if out["mode"] != "hybrid" {
			t.Fatalf("search as %s was not by meaning: %v", who, out)
		}
		return fmtJSON(out), out
	}
	for _, who := range []string{s.marcio, s.joao} {
		body, _ := search(who)
		if !strings.Contains(body, mid) || !strings.Contains(body, orgMem) {
			t.Errorf("member %s does not find the session's memory and the organisation's by meaning: %s", who, body)
		}
	}
	otherAdmin := "per_admin2_" + s.org
	mustExec(t, s.owner, `INSERT INTO people (id, organization_id, name, role) VALUES ($1, $2, 'Bea Boss', 'admin')`, otherAdmin, s.org)
	for name, who := range map[string]string{"a pending invitee": s.outsider, "an admin not in it": s.admin,
		"a second admin": otherAdmin, "a removed member": s.ana} {
		body, _ := search(who)
		if !strings.Contains(body, orgMem) {
			t.Errorf("%s does not find the organisation's memory by meaning: the check would be empty: %s", name, body)
		}
		if strings.Contains(body, mid) || strings.Contains(strings.ToLower(body), "zephyr") {
			t.Errorf("%s finds the session's memory by meaning: %s", name, body)
		}
	}

	// The session's own agent finds it; a task's agent in the same project
	// and another session's agent do not.
	if _, body := s.toolRaw(run, "search_memory", `{"query":"`+query+`"}`); !strings.Contains(body, mid) {
		t.Errorf("the session's agent does not find its memory by meaning: %s", body)
	}
	token, hash := agenttools.NewToken()
	task := s.taskIn(s.project, "Elsewhere", s.marcio)
	mustExec(t, s.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, mcp_token_hash)
		VALUES ('run_vec_'||$1, $1, $2, $3, 1, 'running', 'implement', 'implementer', $4)`, s.org, s.project, task, hash)
	other := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Other",
		"projects": []map[string]any{{"projectId": s.project, "repositoryIds": []string{}}}})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+other+"/chat", map[string]any{"text": "hi"})
	otherRun := s.started(other)
	for name, call := range map[string]func() (int, string){
		"a task's agent":          func() (int, string) { return s.toolAs(token, "search_memory", `{"query":"`+query+`"}`) },
		"another session's agent": func() (int, string) { return s.toolRaw(otherRun, "search_memory", `{"query":"`+query+`"}`) },
	} {
		status, body := call()
		if status != 200 || !strings.Contains(body, orgMem) {
			t.Errorf("%s does not find the organisation's memory by meaning: the check would be empty: %d %s", name, status, body)
		}
		if strings.Contains(body, mid) || strings.Contains(strings.ToLower(body), "zephyr") {
			t.Errorf("%s finds the session's memory by meaning: %s", name, body)
		}
	}
}

package orchestrator_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/marciomartins/dude/orchestrator/internal/agenttools"
	"github.com/marciomartins/dude/orchestrator/internal/api"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/notify"
)

// A session with no linked projects still searches its organisation's
// memories: an organisation memory is found, a project's is not.
func TestASessionWithNothingLinkedSearchesOrganisationMemory(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	orgMem, prjMem := "mem_org_"+s.org, "mem_prj_"+s.org
	mustExec(t, s.owner, `INSERT INTO memories (id, organization_id, project_id, title, content, kind, author_kind)
		VALUES ($1, $3, NULL, 'Meter windows', 'The meter keys expire after 24h.', 'fact', 'person'),
		       ($2, $3, $4, 'Meter project note', 'The meter lives in billing.', 'fact', 'person')`, orgMem, prjMem, s.org, s.project)
	id := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Nothing linked"})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "search memory"})
	run := s.started(id)
	s.until("the memories to be indexed", func() bool {
		return s.count(`SELECT count(*) FROM search_documents WHERE source_id = ANY($1)`, []string{orgMem, prjMem}) == 2
	})

	status, body := s.toolRaw(run, "search_memory", `{"query":"meter"}`)
	if status != 200 {
		t.Fatalf("search_memory with nothing linked: %d %s", status, body)
	}
	if !strings.Contains(body, orgMem) {
		t.Errorf("the organisation's memory was not found: %s", body)
	}
	if strings.Contains(body, prjMem) {
		t.Errorf("a project's memory was found by a session that links no project: %s", body)
	}
}

// What a session's agent remembers is the session's: its accepted members
// read it (and see it was the brainstorm's), its own agent finds it; nobody
// else does — not someone else in the organisation, not an invitee who has
// not accepted, not an admin, not another agent — through any memory read.
// The organisation's and its linked project's memories stay its to read.
func TestASessionsMemoriesAreItsMembersAlone(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.join(id, s.joao, "read")
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people", map[string]any{"people": []string{s.ana}, "role": "chat"})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "private planning"})
	run := s.started(id)

	// Only session scope: an organisation or project scope is refused.
	for _, args := range []string{
		`{"scope":"organization","title":"Zephyr","content":"x"}`,
		`{"project":"BL","title":"Zephyr","content":"x"}`,
	} {
		if status, body := s.toolRaw(run, "remember", args); status != 422 {
			t.Errorf("remember %s: %d %s, want refused", args, status, body)
		}
	}
	status, out := s.tool(run, "remember", `{"title":"Confidential zephyr plan","content":"Private session decides to acquire Zephyr."}`)
	if status != 200 {
		t.Fatalf("remember: %d %v", status, out)
	}
	mid := out["id"].(string)
	var project, session string
	_ = s.owner.QueryRow(t.Context(), `SELECT coalesce(project_id, ''), coalesce(session_id, '') FROM memories WHERE id = $1`, mid).
		Scan(&project, &session)
	if project != "" || session != id {
		t.Errorf("stored in project %q, session %q; want the session's alone", project, session)
	}
	// Someone else's memories, which the session reads as before.
	orgMem, prjMem, webMem := "mem_org_"+s.org, "mem_prj_"+s.org, "mem_web_"+s.org
	mustExec(t, s.owner, `INSERT INTO memories (id, organization_id, project_id, title, content, kind, author_kind)
		VALUES ($1, $4, NULL, 'Zephyr org fact', 'zephyr everywhere', 'fact', 'person'),
		       ($2, $4, $5, 'Zephyr billing fact', 'zephyr in billing', 'fact', 'person'),
		       ($3, $4, $6, 'Zephyr web fact', 'zephyr in web', 'fact', 'person')`, orgMem, prjMem, webMem, s.org, s.project, s.webProject)
	s.until("the memories to be indexed", func() bool {
		return s.count(`SELECT count(*) FROM search_documents WHERE source_id = ANY($1)`, []string{mid, orgMem, prjMem, webMem}) == 4
	})

	// Members read it, with its provenance.
	for _, who := range []string{s.marcio, s.joao} {
		status, out := s.as(who, "GET", "/internal/memory/memories/"+mid, nil)
		author, _ := out["author"].(map[string]any)
		if status != 200 || author["runId"] != run || author["role"] != "brainstorm" {
			t.Errorf("member %s reads %d %v", who, status, out)
		}
		if body := s.body(who, "/internal/memory/search?mode=words&q=zephyr"); !strings.Contains(body, mid) {
			t.Errorf("member %s does not find it: %s", who, body)
		}
		if body := s.body(who, "/internal/memory/memories?q=zephyr"); !strings.Contains(body, mid) {
			t.Errorf("member %s does not list it: %s", who, body)
		}
	}
	// Nobody else, through any read; nor its Run as anyone's provenance.
	for _, who := range []string{s.outsider, s.ana, s.admin} {
		if status, out := s.as(who, "GET", "/internal/memory/memories/"+mid, nil); status != 404 || strings.Contains(fmtJSON(out), "zephyr") {
			t.Errorf("%s reads the session's memory: %d %v", who, status, out)
		}
		for _, path := range []string{"/internal/memory/search?mode=words&q=zephyr", "/internal/memory/search?q=zephyr&project=" + s.project,
			"/internal/memory/memories?q=zephyr", "/internal/memory/memories?scope=organization", "/internal/memory/memories?project=" + s.project,
			"/internal/memory/memories?author=agent", "/internal/memory/index"} {
			if body := s.body(who, path); strings.Contains(body, mid) || strings.Contains(body, "acquire Zephyr") || strings.Contains(body, run) {
				t.Errorf("%s %s shows the session's memory: %s", who, path, body)
			}
		}
		for _, path := range []string{"/internal/memory/memories/" + mid, "/internal/memory/memories/" + mid + "/archive"} {
			method := map[bool]string{true: "PATCH", false: "POST"}[!strings.HasSuffix(path, "archive")]
			if status, _ := s.as(who, method, path, map[string]any{"title": "taken"}); status != 404 {
				t.Errorf("%s %s %s: %d, want 404", who, method, path, status)
			}
		}
	}
	// Another agent — a task's, in the same project — neither finds nor reads it.
	token, hash := agenttools.NewToken()
	task := s.taskIn(s.project, "Elsewhere", s.marcio)
	mustExec(t, s.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, mcp_token_hash)
		VALUES ('run_other_'||$1, $1, $2, $3, 1, 'running', 'implement', 'implementer', $4)`, s.org, s.project, task, hash)
	for tool, args := range map[string]string{"search_memory": `{"query":"zephyr"}`, "get_memory": `{"id":"` + mid + `"}`} {
		status, body := s.toolAs(token, tool, args)
		if tool == "get_memory" && status != 422 || tool == "search_memory" && strings.Contains(body, mid) ||
			strings.Contains(body, "acquire Zephyr") || strings.Contains(body, run) {
			t.Errorf("a task's agent %s: %d %s", tool, status, body)
		}
	}
	if _, body := s.toolAs(token, "search_memory", `{"query":"zephyr"}`); !strings.Contains(body, orgMem) || !strings.Contains(body, prjMem) {
		t.Errorf("a task's agent no longer finds the organisation's and its project's memories: %s", body)
	}
	// The session's agent finds its own, the organisation's and its linked project's; not an unlinked project's.
	_, body := s.toolRaw(run, "search_memory", `{"query":"zephyr"}`)
	for _, want := range []string{mid, orgMem, prjMem} {
		if !strings.Contains(body, want) {
			t.Errorf("the session's agent does not find %s: %s", want, body)
		}
	}
	if strings.Contains(body, webMem) {
		t.Errorf("the session's agent finds an unlinked project's memory: %s", body)
	}
	if status, body := s.toolRaw(run, "get_memory", `{"id":"`+mid+`"}`); status != 200 || !strings.Contains(body, "acquire Zephyr") {
		t.Errorf("the session's agent reads its memory: %d %s", status, body)
	}
	// Another session's agent does not.
	other := s.ok(s.marcio, "POST", "/internal/sessions", map[string]any{"title": "Other",
		"projects": []map[string]any{{"projectId": s.project, "repositoryIds": []string{}}}})["id"].(string)
	s.ok(s.marcio, "POST", "/internal/sessions/"+other+"/chat", map[string]any{"text": "hi"})
	otherRun := s.started(other)
	for tool, args := range map[string]string{"search_memory": `{"query":"zephyr"}`, "get_memory": `{"id":"` + mid + `"}`} {
		status, body := s.toolRaw(otherRun, tool, args)
		if tool == "get_memory" && status != 422 || tool == "search_memory" && strings.Contains(body, mid) ||
			strings.Contains(body, "acquire Zephyr") {
			t.Errorf("another session's agent %s: %d %s", tool, status, body)
		}
	}
	// Removed, a member reads it no more.
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people/"+s.joao+"/remove", nil)
	if status, _ := s.as(s.joao, "GET", "/internal/memory/memories/"+mid, nil); status != 404 {
		t.Errorf("a removed member still reads it: %d", status)
	}
}

// A question put to one member who is then removed, or can no longer chat,
// is withdrawn: closed unanswered, the agent told, and what others said
// meanwhile reaches it. Nobody's message is taken as the answer.
func TestAQuestionToSomeoneWhoLeavesIsWithdrawn(t *testing.T) {
	for _, how := range []string{"removed", "demoted", "handed over"} {
		t.Run(how, func(t *testing.T) {
			s := newSessionWorld(t)
			s.withTools()
			id := s.session()
			s.join(id, s.ana, "chat")
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "plan"})
			run := s.started(id)
			// Handing over, Márcio keeps read: his question is withdrawn, Ana's message held for it goes.
			asked, writer, name := s.ana, s.marcio, "Ana Nunes"
			if how == "handed over" {
				asked, writer, name = s.marcio, s.ana, "Márcio Martins"
			}
			status, out := s.tool(run, "ask_person", `{"question":"Private decision?","to":"`+name+`"}`)
			if status != 200 {
				t.Fatalf("ask_person: %d %v", status, out)
			}
			qid := out["questionId"].(string)
			s.ok(writer, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "held note"})
			switch how {
			case "removed":
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people/"+asked+"/remove", nil)
			case "demoted":
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people/"+asked+"/role", map[string]any{"role": "read"})
			default:
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/owner", map[string]any{"person": s.ana, "keep": "read"})
			}
			writerName := "Márcio Martins"
			if writer == s.ana {
				writerName = "Ana Nunes"
			}

			var qstatus, answer string
			_ = s.owner.QueryRow(t0(), `SELECT status::text, coalesce(answer, '') FROM questions WHERE id = $1`, qid).Scan(&qstatus, &answer)
			if qstatus != "cancelled" || answer != "" {
				t.Errorf("the question is %s with answer %q; want cancelled, unanswered", qstatus, answer)
			}
			if n := s.count(`SELECT count(*) FROM events WHERE event_type = 'question.closed' AND payload->>'questionId' = $1
				AND payload->>'by' = 'withdrawn'`, qid); n != 1 {
				t.Errorf("question.closed (withdrawn): %d", n)
			}
			s.until("the agent to hear the withdrawal and the held message", func() bool {
				s.pump()
				v := s.luxRun(run)
				all := strings.Join(append(v.inputs, v.resumes...), "\n")
				return strings.Contains(all, "Your question to "+name+" is withdrawn") && strings.Contains(all, writerName+": held note")
			})
			if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1 AND sent_at IS NULL`, run); n != 0 {
				t.Errorf("%d directives still unsent", n)
			}
			// A message now goes as a message, not as an answer.
			if out := s.ok(writer, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "next"}); out["questionId"] != nil {
				t.Errorf("a later message answered a withdrawn question: %v", out)
			}
		})
	}
}

// A session's question reaches by push only someone who may still answer
// it, when the push goes: not a member removed or made a reader before
// the notifier swept, and nobody once it is closed.
func TestAQuestionIsPushedOnlyToWhoMayStillAnswerIt(t *testing.T) {
	for _, how := range []string{"still a member", "removed", "demoted", "answered", "to nobody: owner removed it", "reader, question still open"} {
		t.Run(how, func(t *testing.T) {
			s := newSessionWorld(t)
			s.withTools()
			id := s.session()
			s.join(id, s.ana, "chat")
			var sent atomic.Int32
			var body atomic.Value
			push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sent.Add(1)
				b, _ := io.ReadAll(r.Body)
				body.Store(len(b))
				w.WriteHeader(201)
			}))
			defer push.Close()
			key, err := ecdh.P256().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			auth := make([]byte, 16)
			_, _ = rand.Read(auth)
			to := s.ana
			if how == "to nobody: owner removed it" {
				to = s.marcio
			}
			mustExec(t, s.owner, `INSERT INTO push_subscriptions (endpoint, organization_id, person_id, p256dh, auth) VALUES ($1, $2, $3, $4, $5)`,
				push.URL, s.org, to, base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(auth))
			n := &notify.Notifier{DB: s.app, Log: quiet, Subject: "ops@example.com", HTTP: push.Client()}
			if _, _, err := n.Keys(t0()); err != nil {
				t.Fatal(err)
			}
			s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "plan"})
			run := s.started(id)
			ask := `{"question":"Confidential acquisition price?","to":"Ana"}`
			if how == "to nobody: owner removed it" {
				ask = `{"question":"Confidential acquisition price?"}`
			}
			if status, out := s.tool(run, "ask_person", ask); status != 200 {
				t.Fatal(out)
			}
			switch how {
			case "removed":
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people/"+s.ana+"/remove", nil)
			case "demoted":
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people/"+s.ana+"/role", map[string]any{"role": "read"})
			case "answered":
				s.ok(s.ana, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "a lot"})
			case "to nobody: owner removed it":
				// The owner, the push's recipient, hands it to Ana and leaves.
				s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/owner", map[string]any{"person": s.ana, "keep": "leave"})
			case "reader, question still open":
				// The role alone decides, whatever else closed or did not.
				mustExec(t, s.owner, `UPDATE session_people SET role = 'read' WHERE session_id = $1 AND person_id = $2`, id, s.ana)
			}
			if _, err := n.Sweep(t0()); err != nil {
				t.Fatal(err)
			}
			want := int32(0)
			if how == "still a member" {
				want = 1
			}
			if got := sent.Load(); got != want {
				t.Errorf("%d notifications, want %d", got, want)
			}
		})
	}
}

// A session Run's artifacts are its members' to download, as everything of
// it is: the internal route checks the person it acts for, and with no
// person names no session artifact at all. A task's are as they were.
func TestASessionsArtifactsAreItsMembersAlone(t *testing.T) {
	s := newSessionWorld(t)
	s.lux.Decide = func(map[string]any) fakelux.Behaviour {
		return fakelux.Behaviour{Reply: "private", Publish: map[string]string{"private.md": "private session artifact"}}
	}
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people", map[string]any{"people": []string{s.ana}})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "plan"})
	run := s.started(id)
	mustExec(t, s.owner, `UPDATE runs SET control = 'pause_graceful' WHERE id = $1`, run)
	s.until("the session's artifact", func() bool {
		s.pump()
		return s.count(`SELECT count(*) FROM artifacts WHERE run_id = $1`, run) > 0
	})
	var aid string
	if err := s.owner.QueryRow(t0(), `SELECT id FROM artifacts WHERE run_id = $1 LIMIT 1`, run).Scan(&aid); err != nil {
		t.Fatal(err)
	}
	path := "/internal/artifacts/" + aid + "/content"
	if status, out := s.as(s.marcio, "GET", path, nil); status != 200 {
		t.Errorf("the owner downloads it: %d %v", status, out)
	}
	for _, who := range []string{s.outsider, s.ana, s.admin} {
		status, out := s.as(who, "GET", path, nil)
		if status != 404 || strings.Contains(fmtJSON(out), "private session artifact") {
			t.Errorf("%s downloads the session's artifact: %d %v", who, status, out)
		}
	}
	if status, body := s.get(path, s.org); status != 404 || strings.Contains(body, "private session artifact") {
		t.Errorf("with no person, a session's artifact is downloaded: %d %s", status, body)
	}
}

// A session's agent is driven only through its session: the task routes
// that steer, pause, resume or abort a Run, answer its question, or run its
// servers find no such Run — for its owner as for anyone else — and change
// nothing.
func TestASessionsRunIsNotATasksToControl(t *testing.T) {
	s := newSessionWorld(t)
	s.withTools()
	id := s.session()
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/people", map[string]any{"people": []string{s.ana}})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/chat", map[string]any{"text": "plan"})
	run := s.started(id)
	_, out := s.tool(run, "ask_person", `{"question":"Which window?"}`)
	qid, _ := out["questionId"].(string)
	before := s.count(`SELECT count(*) FROM directives WHERE run_id = $1`, run)
	routes := []struct{ method, path string }{
		{"POST", "/internal/runs/" + run + "/steer"}, {"POST", "/internal/runs/" + run + "/pause"},
		{"POST", "/internal/runs/" + run + "/resume"}, {"POST", "/internal/runs/" + run + "/abort"},
		{"POST", "/internal/questions/" + qid + "/answer"},
		{"GET", "/internal/runs/" + run + "/servers"}, {"POST", "/internal/runs/" + run + "/servers"},
		{"POST", "/internal/runs/" + run + "/servers/start-all"}, {"POST", "/internal/runs/" + run + "/servers/stop-all"},
		{"POST", "/internal/runs/" + run + "/servers/web/start"}, {"DELETE", "/internal/runs/" + run + "/servers/web"},
		{"GET", "/internal/runs/" + run + "/servers/web/log"},
	}
	for _, who := range []string{s.marcio, s.ana, s.outsider, s.admin} {
		for _, r := range routes {
			status, body := s.as(who, r.method, r.path, map[string]any{"text": "go", "name": "web", "command": "true"})
			if status != 404 || strings.Contains(fmtJSON(body), "Which window") {
				t.Errorf("%s %s as %s: %d %v, want 404", r.method, r.path, who, status, body)
			}
		}
	}
	var status, control string
	_ = s.owner.QueryRow(t0(), `SELECT status::text, coalesce(control::text, '') FROM runs WHERE id = $1`, run).Scan(&status, &control)
	if status == "aborted" || control != "none" {
		t.Errorf("the session's Run was controlled: status %s, control %q", status, control)
	}
	if n := s.count(`SELECT count(*) FROM directives WHERE run_id = $1`, run); n != before {
		t.Errorf("%d directives queued through task routes", n-before)
	}
	if n := s.count(`SELECT count(*) FROM questions WHERE id = $1 AND status = 'open'`, qid); n != 1 {
		t.Errorf("the question was settled through a task route")
	}
}

// queryCount counts the statements a pool runs.
type queryCount struct{ n atomic.Int64 }

func (q *queryCount) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.n.Add(1)
	return ctx
}
func (q *queryCount) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// A session's detail reads its proposal cards — every one it ever had,
// with each item's filing and who may file it — in a number of queries
// that does not grow with them: one session with 1 card and one with 40
// cards of 10 items (half filed, edits and comments among them) cost the
// same. And it says the same thing, item by item, as before.
func TestASessionsDetailReadsItsCardsInBoundedQueries(t *testing.T) {
	s := newSessionWorld(t)
	counter := &queryCount{}
	cfg, err := pgxpool.ParseConfig(s.app.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(t0(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	srv := httptest.NewServer((&api.Server{DB: &db.DB{Pool: pool}, Lux: s.syncer.Lux, Token: "svc", Log: quiet, Kick: func() {}}).Handler())
	t.Cleanup(srv.Close)
	get := func(person, session string) (map[string]any, int64) {
		req, _ := http.NewRequest("GET", srv.URL+"/internal/sessions/"+session, nil)
		for k, v := range map[string]string{"Authorization": "Bearer svc", "X-Dude-Organization": s.org,
			"X-Dude-Credential-Kind": "person", "X-Dude-Person": person, "X-Dude-Actor": person} {
			req.Header.Set(k, v)
		}
		before := counter.n.Load()
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		if res.StatusCode != 200 {
			t.Fatalf("detail: %d %v", res.StatusCode, out)
		}
		return out, counter.n.Load() - before
	}

	mine := s.taskIn(s.project, "Daily rollup", s.marcio)
	anas := s.taskIn(s.project, "Ana's task", s.ana)
	card := func() []delivery.ProposalItem {
		items := []delivery.ProposalItem{{Kind: "epic", Project: "BL", Title: "Metering"}}
		for i := 0; i < 6; i++ {
			items = append(items, delivery.ProposalItem{Kind: "task", Project: "BL", Epic: "Metering", Title: fmt.Sprintf("Meter %d", i),
				Goal: "Count experiment runs per org per day"})
		}
		return append(items,
			delivery.ProposalItem{Kind: "edit", Task: s.keyOf(mine), After: &delivery.TaskText{Goal: ptr("A goal long enough to keep, changed")}},
			delivery.ProposalItem{Kind: "edit", Task: s.keyOf(anas), After: &delivery.TaskText{Goal: ptr("A goal long enough to keep, Ana's")}},
			delivery.ProposalItem{Kind: "comment", Task: s.keyOf(anas), Text: "a note"})
	}
	small := s.session()
	s.join(small, s.ana, "chat")
	s.proposal(small, card())
	big := s.session()
	s.join(big, s.ana, "chat")
	var last string
	for i := 0; i < 40; i++ {
		last = s.proposal(big, card())
		if i%2 == 0 {
			s.ok(s.marcio, "POST", "/internal/sessions/"+big+"/file", map[string]any{"proposalId": last, "items": []int{0, 1, 2, 3}})
		}
	}

	_, few := get(s.ana, small)
	out, many := get(s.ana, big)
	t.Logf("queries: %d for 1 card, %d for 40", few, many)
	if many != few {
		t.Errorf("the detail ran %d queries with 40 cards, %d with 1: it must not grow with the cards", many, few)
	}
	cards, _ := out["proposals"].([]any)
	if len(cards) != 40 {
		t.Fatalf("%d cards", len(cards))
	}
	// What each item says, for Ana (who can chat): filed by Márcio; hers to
	// file; Márcio's edit not hers to file, and why.
	first := cards[0].(map[string]any)["status"].([]any)
	want := []string{`"filed":true`, `"filed":true`, `"filed":true`, `"filed":true`, `"canFile":true`, `"canFile":true`, `"canFile":true`,
		`"canFile":false`, `"canFile":true`, `"canFile":true`}
	for i, w := range want {
		if got := fmtJSON(first[i]); !strings.Contains(got, w) {
			t.Errorf("item %d: %s, want %s", i, got, w)
		}
	}
	if got := fmtJSON(first[0]); !strings.Contains(got, `"filedBy":"Márcio Martins"`) || !strings.Contains(got, `"key":`) {
		t.Errorf("a filed item says who and what: %s", got)
	}
	if got := fmtJSON(first[7]); !strings.Contains(got, "Márcio") {
		t.Errorf("Márcio's edit says who may file it: %s", got)
	}
	lastCard := cards[39].(map[string]any)["status"].([]any)
	if got := fmtJSON(lastCard[0]); !strings.Contains(got, `"canFile":true`) {
		t.Errorf("an unfiled card's epic: %s", got)
	}
}

// ownerOf is the session's owner, and whether person is still in it.
func (s *sessionWorld) ownerOf(session, person string) (string, bool) {
	var owner string
	_ = s.owner.QueryRow(t0(), `SELECT person_id FROM session_people WHERE session_id = $1 AND role = 'owner'`, session).Scan(&owner)
	return owner, s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND person_id = $2`, session, person) == 1
}

// A handover invitation is the owner's who made it: once the session has
// another owner, it hands over nothing. Accepting it then makes its
// invitee an ordinary member, and the owner and their place stay as they are.
func TestAHandoverInvitationOutlivesNoChangeOfOwner(t *testing.T) {
	s := newSessionWorld(t)
	id := s.session()
	s.join(id, s.ana, "chat")
	// Márcio offers it to João (leaving), then hands it to Ana at once.
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/owner", map[string]any{"person": s.joao, "keep": "leave"})
	s.ok(s.marcio, "POST", "/internal/sessions/"+id+"/owner", map[string]any{"person": s.ana, "keep": "read"})
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND becomes_owner`, id); n != 0 {
		t.Errorf("a handover invitation outlived the change of owner: %d", n)
	}
	s.ok(s.joao, "POST", "/internal/sessions/"+id+"/accept", nil)
	if owner, anaIn := s.ownerOf(id, s.ana); owner != s.ana || !anaIn {
		t.Errorf("after João accepts the old offer: owner %s, Ana in it %v; want Ana, still in it", owner, anaIn)
	}
	var marcio, joao string
	_ = s.owner.QueryRow(t0(), `SELECT role FROM session_people WHERE session_id = $1 AND person_id = $2`, id, s.marcio).Scan(&marcio)
	_ = s.owner.QueryRow(t0(), `SELECT role FROM session_people WHERE session_id = $1 AND person_id = $2`, id, s.joao).Scan(&joao)
	if marcio != "read" || joao != "chat" {
		t.Errorf("Márcio is %q (want read, as he chose), João %q (want chat, an ordinary member)", marcio, joao)
	}

	// The same through an accepted handover: Ana offers it to João, then
	// to Otto, who accepts first; João's offer is gone with Ana's ownership.
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/people/"+s.joao+"/remove", nil)
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/owner", map[string]any{"person": s.joao, "keep": "leave"})
	if n := s.count(`SELECT count(*) FROM session_people WHERE session_id = $1 AND becomes_owner`, id); n != 1 {
		t.Fatalf("João's offer: %d", n)
	}
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/people", map[string]any{"people": []string{s.outsider}, "role": "chat"})
	s.ok(s.outsider, "POST", "/internal/sessions/"+id+"/accept", nil)
	s.ok(s.ana, "POST", "/internal/sessions/"+id+"/owner", map[string]any{"person": s.outsider, "keep": "chat"})
	s.ok(s.joao, "POST", "/internal/sessions/"+id+"/accept", nil)
	if owner, ottoIn := s.ownerOf(id, s.outsider); owner != s.outsider || !ottoIn {
		t.Errorf("João's stale offer took the session from Otto: owner %s, Otto in it %v", owner, ottoIn)
	}
	if _, anaIn := s.ownerOf(id, s.ana); !anaIn {
		t.Errorf("Ana was removed by João's stale offer (its keep was leave)")
	}

	// An offer that outlived its owner some other way (written before
	// offers were voided with a change of owner): accepting it checks, under
	// the lock, that its maker still owns the session.
	s.ok(s.outsider, "POST", "/internal/sessions/"+id+"/people/"+s.joao+"/remove", nil)
	mustExec(t, s.owner, `INSERT INTO session_people (session_id, person_id, organization_id, role, invited_by, becomes_owner, handover_keep)
		VALUES ($1, $2, $3, 'chat', $4, true, 'leave')`, id, s.joao, s.org, s.ana)
	s.ok(s.joao, "POST", "/internal/sessions/"+id+"/accept", nil)
	if owner, ottoIn := s.ownerOf(id, s.outsider); owner != s.outsider || !ottoIn {
		t.Errorf("an offer by Ana, no longer the owner, took the session from Otto: owner %s, Otto in it %v", owner, ottoIn)
	}
}

// toolRaw calls one of the session agent's tools, its body as the agent reads it.
func (s *sessionWorld) toolRaw(runID, name, args string) (int, string) {
	s.t.Helper()
	v := s.luxRun(runID)
	if v == nil {
		s.t.Fatalf("no lux Run for %s", runID)
	}
	return s.callTool(s.syncer.Agent.ToolsURL, v.raw, name, args)
}

// toolAs calls a tool with a Run's bearer token.
func (s *sessionWorld) toolAs(token, name, args string) (int, string) {
	s.t.Helper()
	spec, _ := json.Marshal(map[string]any{"secrets": []map[string]string{{"name": "DUDE_TOOLS_AUTH", "value": "Bearer " + token}}})
	return s.callTool(s.syncer.Agent.ToolsURL, string(spec), name, args)
}

// body is a GET's body as a person, as text.
func (s *sessionWorld) body(person, path string) string {
	s.t.Helper()
	_, out := s.as(person, "GET", path, nil)
	return fmtJSON(out)
}

func fmtJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func t0() context.Context { return context.Background() }

package delivery

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"text/template"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// The conversation so far: what a talker (a session's agent, a task's
// conductor) said and was told, rendered from the ledger for an agent that
// starts without its harness transcript — a new Run, or a resume lux could
// only give a blank session. People's and the agent's words are verbatim;
// tool calls are one line each from what the ledger keeps of them.

// replayToolCap bounds a tool call's input, and its output, in bytes.
const replayToolCap = 4096

// Talker names whose conversation is replayed: a session, or a task.
type Talker struct{ SessionID, TaskID string }

// replayEvent is one ledger event as the renderer reads it. Who is the
// actor's name when a person wrote it; Heard is false for a message whose
// directive never reached the agent (it is handed on, or failed, and the
// next agent gets it as its own input if at all). RepeatsHeard is true for
// a resend (payload supersedes) of a directive the agent did hear.
type replayEvent struct {
	Cursor       int64
	Type         string
	RunID        string
	ActorType    string
	ActorID      string
	Who          string
	Payload      map[string]any
	Heard        bool
	RepeatsHeard bool
}

// replayTypes are the events the replay renders, or reads to render one.
var replayTypes = []string{
	EvChatMessage, EvRunSteered, evAgentMessage, EvQuestionAsked, evQuestionAnswered, EvQuestionClosed,
	evSessionTold, EvConductorWoken, EvSessionProposed, EvSessionFiled, EvDecisionTaken, EvFindingResolved,
	EvTaskUpdated, EvSessionRenamed, evToolCalled, evToolCompleted, evSessionReplaced, evContextCompacted,
}

// The events the replay reads that other packages write (the phase
// syncer, the API), under the names they are written with.
const (
	evAgentMessage     = "agent.message"
	evQuestionAnswered = "question.answered"
	evSessionTold      = "session.told"
	evToolCalled       = "agent.tool.called"
	evToolCompleted    = "agent.tool.completed"
	evSessionReplaced  = "agent.session.replaced"
	evContextCompacted = "agent.context.compacted"
)

// talkerScope (SQL, over events e; $1 the session or task): the talker's
// ledger. A session's: every event of the session (events_session_of_run
// stamps the session on each event of its Runs). A task's: its conductor
// Runs' events, never a phase Run's; every writer of a task's chat.message
// puts it on a conductor Run.
const (
	sessionScope = `e.session_id = $1`
	taskScope    = `(e.task_id = $1 AND e.run_id IN (SELECT id FROM runs WHERE task_id = $1 AND role = 'conductor' AND kind = 'agent'))`
)

// heardDirective (SQL, over events e; key a literal payload key): the
// directive the payload names, or a resend of it, reached e's Run. Two legs,
// so the first is a primary-key probe.
func heardDirective(key string) string {
	id := `e.payload->>'` + key + `'`
	return `(EXISTS (SELECT 1 FROM directives d WHERE d.id = ` + id + ` AND d.run_id = e.run_id AND d.delivered_at IS NOT NULL)
		OR EXISTS (SELECT 1 FROM directives d WHERE d.resends = ` + id + ` AND d.run_id = e.run_id AND d.delivered_at IS NOT NULL))`
}

// Replay is the talker's conversation so far under its heading, "" for a
// talker that has had no Run yet, or whose ledger says nothing worth
// replaying.
func Replay(ctx context.Context, tx pgx.Tx, of Talker) (string, error) {
	events, err := replayEvents(ctx, tx, of)
	if err != nil || len(events) == 0 {
		return "", err
	}
	body := renderReplayFrom(fromCompaction(events))
	if body == "" {
		return "", nil
	}
	return replayHeading(body), nil
}

// replayHeading is the rendered replay under its heading and lead.
func replayHeading(body string) string { return render(replayTmpl, body) }

// fromCompaction is where the replay starts: the newest compaction summary
// the agent's harness kept (agent.context.compacted), with the Run whose
// agent wrote it, and the events after it; with none, a zero start and
// every event. A summary covers whatever the conversation before it held,
// a replay a Run was given included, so the newest is enough.
// Another source of summaries (a harness's own record) would be read here.
func fromCompaction(events []replayEvent) (replayStart, []replayEvent) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != evContextCompacted {
			continue
		}
		if s, _ := events[i].Payload["summary"].(string); strings.TrimSpace(s) != "" {
			return replayStart{summary: s, run: events[i].RunID}, events[i+1:]
		}
	}
	return replayStart{}, events
}

// replayStart is the compaction summary a replay starts from ("" for
// none) and the Run whose agent wrote it.
type replayStart struct{ summary, run string }

// renderReplayFrom is the summary under its heading, if any, then the
// events rendered, within the budget.
func renderReplayFrom(start replayStart, events []replayEvent) string {
	return fitReplay(start, replayEntries(events), replayBudgetTokens)
}

// A replay's budget, in tokens estimated at charsPerToken runes each.
const (
	replayBudgetTokens = 100_000
	charsPerToken      = 4
)

func replayTokens(s string) int {
	return (utf8.RuneCountInString(s) + charsPerToken - 1) / charsPerToken
}

var outputDropped = replayLine("output-dropped", nil)

// fitReplay renders the entries within budget tokens, shedding while over:
// tool outputs, oldest first, keeping the call; then tool calls whole,
// oldest first; then the oldest turns whole, said in one line. Words are
// never cut, and the summary is never dropped.
func fitReplay(start replayStart, entries []replayEntry, budget int) string {
	entries = slices.Clone(entries)
	limit := budget * charsPerToken
	// Sizes are kept by difference, not re-rendered at each step: a dropped
	// entry takes its text, its blank line, and the take-over line before it
	// when it begins a Run's turn.
	chars := utf8.RuneCountInString(joinReplay(start, entries, 0))
	takeOver := takeOvers(start, entries)
	keep := slices.Repeat([]bool{true}, len(entries))
	drop := func(i int) {
		keep[i], chars = false, chars-utf8.RuneCountInString(entries[i].text())-2
		if takeOver[i] {
			chars -= utf8.RuneCountInString(tookOver) + 2
		}
	}
	// whileOver visits the entries oldest first until the size fits.
	whileOver := func(step func(i int)) {
		for i := 0; i < len(entries) && chars > limit; i++ {
			step(i)
		}
	}
	whileOver(func(i int) {
		if entries[i].tool && entries[i].output != outputDropped {
			chars -= utf8.RuneCountInString(entries[i].output) - utf8.RuneCountInString(outputDropped)
			entries[i].output = outputDropped
		}
	})
	whileOver(func(i int) {
		if entries[i].tool {
			drop(i)
		}
	})
	omitted := 0
	if chars > limit {
		chars += utf8.RuneCountInString(omittedLine(len(entries))) + 2
	}
	whileOver(func(i int) {
		if keep[i] {
			drop(i)
			omitted++
		}
	})
	kept := entries[:0]
	for i, e := range entries {
		if keep[i] {
			kept = append(kept, e)
		}
	}
	// The estimate cannot know which take-over lines survive the drops: the
	// oldest turns go until the rendering itself fits.
	out := joinReplay(start, kept, omitted)
	for len(kept) > 0 && utf8.RuneCountInString(out) > limit {
		kept, omitted = kept[1:], omitted+1
		out = joinReplay(start, kept, omitted)
	}
	return out
}

// tookOver marks where another Run's agent took the conversation on.
var tookOver = replayLine("took-over", nil)

// takeOvers is, per entry, whether a take-over line goes before it: its
// Run differs from the last Run seen, counting from the summary's.
func takeOvers(start replayStart, entries []replayEntry) []bool {
	at := make([]bool, len(entries))
	last := start.run
	for i, e := range entries {
		at[i] = last != "" && e.run != "" && e.run != last
		if e.run != "" {
			last = e.run
		}
	}
	return at
}

func omittedLine(n int) string { return replayLine("omitted", n) }

// replayLine renders briefings/replay-lines.md's line name with data.
func replayLine(name string, data any) string {
	t, ok := replayLineTmpls[name]
	if !ok {
		panic("no replay line " + name)
	}
	return render(t, data)
}

// replayLineTmpls are the lines replayLine renders, resolved at init.
var replayLineTmpls = func() map[string]*template.Template {
	out := map[string]*template.Template{}
	for _, name := range []string{"summary", "omitted", "took-over", "pr-reply", "you", "said", "answered", "image-ref",
		"github", "someone", "withdrawn", "settled-elsewhere", "dude", "earlier-proposal", "filed", "proposed", "decided",
		"finding", "task-changed", "renamed", "session-replaced", "asked", "tool", "no-output-recorded", "output-dropped",
		"no-output", "tail-cut", "capped", "stderr", "error", "exit"} {
		out[name] = mustTemplate("briefings/replay-lines/" + name)
	}
	return out
}()

// The replay's fixed lines.
var (
	replayNoOutputRecorded = replayLine("no-output-recorded", nil)
	replayNoOutput         = replayLine("no-output", nil)
	replayWithdrawn        = replayLine("withdrawn", nil)
	replaySettledElsewhere = replayLine("settled-elsewhere", nil)
	replaySessionReplaced  = replayLine("session-replaced", nil)
	replaySomeone          = replayLine("someone", nil)
)

// joinReplay renders the summary under its heading, then the entries,
// after a line saying how many earlier ones were omitted; a take-over line
// goes wherever the Run changes, from the summary's Run on.
func joinReplay(start replayStart, entries []replayEntry, omitted int) string {
	var b strings.Builder
	if omitted > 0 {
		b.WriteString(omittedLine(omitted))
	}
	takeOver := takeOvers(start, entries)
	for i, e := range entries {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if takeOver[i] {
			b.WriteString(tookOver + "\n\n")
		}
		b.WriteString(e.text())
	}
	body := b.String()
	if start.summary == "" {
		return body
	}
	return replayLine("summary", struct{ Summary, Body string }{strings.TrimSpace(start.summary), body})
}

func replayEvents(ctx context.Context, tx pgx.Tx, of Talker) ([]replayEvent, error) {
	// A session renamed before its first Run has events and nothing to
	// replay; a task's scope already requires a conductor Run.
	scope, id, hadRun := sessionScope, of.SessionID, `EXISTS (SELECT 1 FROM runs WHERE session_id = $1 AND role = 'brainstorm' AND kind = 'agent')`
	if of.SessionID == "" {
		scope, id, hadRun = taskScope, of.TaskID, `true`
	}
	// The correlated heard probes inflate the plan's cost past
	// jit_above_cost on a large ledger: JIT then takes ~1 s of a query
	// that runs in ~70 ms. Off for this query only, and put back after:
	// the caller's transaction goes on (a briefing, a hand-over).
	var jitWas string
	if err := tx.QueryRow(ctx, `SELECT current_setting('jit'), set_config('jit', 'off', true)`).Scan(&jitWas, nil); err != nil {
		return nil, err
	}
	if beforeReplayQuery != nil {
		beforeReplayQuery(ctx, tx)
	}
	// The start subquery's own "events e" shadows the outer e: scope filters the compactions it reads.
	rows, err := tx.Query(ctx, `SELECT e.cursor, e.event_type, COALESCE(e.run_id, ''), e.actor_type, COALESCE(e.actor_id, ''),
			COALESCE((SELECT p.name FROM people p WHERE p.id = e.actor_id),
				(SELECT p.name FROM api_keys k JOIN people p ON p.id = k.person_id WHERE k.id = e.actor_id), ''),
			e.payload,
			NOT (e.payload ? 'directiveId') OR `+heardDirective("directiveId")+`,
			e.payload ? 'supersedes' AND `+heardDirective("supersedes")+`
		FROM events e WHERE `+hadRun+` AND `+scope+` AND e.event_type = ANY($2)
			AND e.cursor >= COALESCE((SELECT max(e.cursor) FROM events e WHERE `+scope+`
				AND e.event_type = '`+evContextCompacted+`' AND btrim(e.payload->>'summary') <> ''), 0)
		ORDER BY e.cursor`, id, replayTypes)
	if err != nil {
		return nil, err
	}
	events, err := pgx.CollectRows(rows, pgx.RowToStructByPos[replayEvent])
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('jit', $1, true)`, jitWas); err != nil {
		return nil, err
	}
	return events, nil
}

// beforeReplayQuery, when set (by tests), runs in the replay's transaction
// right before its ledger query, with the query's settings in force.
var beforeReplayQuery func(context.Context, pgx.Tx)

// replayEntry is one rendered thing: words (a message, a note, a line),
// or a tool call with its output.
type replayEntry struct {
	run    string
	words  string
	tool   bool
	call   string
	output string
}

func (e replayEntry) text() string {
	if !e.tool {
		return e.words
	}
	return e.call + " → " + e.output
}

func replayEntries(events []replayEvent) []replayEntry {
	// A proposal's line says what became of it: its filings come later.
	// A filing of a proposal before the replay's start is said on its own.
	filed := map[string][]string{}
	proposed := map[string]bool{}
	for _, ev := range events {
		switch ev.Type {
		case EvSessionProposed:
			id, _ := ev.Payload["proposalId"].(string)
			proposed[id] = true
		case EvSessionFiled:
			id, items := filings(ev.Payload)
			filed[id] = append(filed[id], items...)
		}
	}
	var out []replayEntry
	calls := map[string]int{}
	add := func(ev replayEvent, words string) {
		if words != "" {
			out = append(out, replayEntry{run: ev.RunID, words: words})
		}
	}
	for _, ev := range events {
		p := ev.Payload
		str := func(k string) string { s, _ := p[k].(string); return s }
		if !ev.Heard {
			continue
		}
		switch ev.Type {
		case EvChatMessage:
			if str("by") == "conductor" {
				gh, _ := p["github"].(map[string]any)
				repo, _ := gh["repo"].(string)
				n, _ := gh["number"].(float64)
				add(ev, replayLine("pr-reply", struct {
					Repo, Text string
					Number     int
				}{repo, replayText(str("text")), int(n)}))
				continue
			}
			add(ev, said(personOf(ev), str("text"), p["attachments"]))
		case EvRunSteered:
			// A resend (Retry, Interrupt now) of words the agent heard
			// repeats them; a Retry of a failed steer is their delivery.
			if ev.RepeatsHeard || str("by") == "conductor" {
				continue
			}
			add(ev, said(personOf(ev), str("text"), p["attachments"]))
		case evAgentMessage:
			if t := str("text"); strings.TrimSpace(t) != "" {
				add(ev, replayLine("you", t))
			}
		case EvQuestionAsked:
			add(ev, askedLine(p))
		case evQuestionAnswered:
			note := str("note")
			if note != "" {
				note = replayText(note)
			}
			add(ev, replayLine("answered", struct {
				Who, Text, Note string
				Images          []string
			}{personOf(ev), replayText(str("answer")), note, imageNames(p["attachments"])}))
		case EvQuestionClosed:
			if str("by") == "withdrawn" {
				add(ev, replayWithdrawn)
			} else {
				add(ev, replaySettledElsewhere)
			}
		case evSessionTold, EvConductorWoken:
			add(ev, replayLine("dude", str("text")))
		case EvSessionProposed:
			add(ev, proposedLine(p, filed[str("proposalId")]))
		case EvSessionFiled:
			if id, items := filings(p); !proposed[id] && len(items) > 0 {
				add(ev, replayLine("earlier-proposal", items))
			}
		case EvDecisionTaken:
			add(ev, replayLine("decided", struct{ Point, Action, Phase, Note string }{pointWords(str("point")), str("action"),
				str("phase"), str("note")}))
		case EvFindingResolved:
			if str("by") == "conductor" {
				// In the words the briefing's findings use (AcceptedBy).
				add(ev, replayLine("finding", struct{ ID, Settled string }{str("findingId"), AcceptedBy(str("note"))}))
			}
		case EvTaskUpdated:
			if str("by") == "conductor" {
				_, goal := p["goal"]
				_, criteria := p["acceptanceCriteria"]
				add(ev, replayLine("task-changed", struct{ Goal, Criteria bool }{goal, criteria}))
			}
		case EvSessionRenamed:
			add(ev, replayLine("renamed", struct {
				Who, Title string
				You        bool
			}{personOf(ev), str("title"), str("by") == ByAgent}))
		case evSessionReplaced:
			add(ev, replaySessionReplaced)
		case evToolCalled:
			key := ev.RunID + "/" + str("callId")
			calls[key] = len(out)
			out = append(out, replayEntry{run: ev.RunID, tool: true, call: toolCall(str("tool"), p["input"]),
				output: replayNoOutputRecorded})
		case evToolCompleted:
			key := ev.RunID + "/" + str("callId")
			i, ok := calls[key]
			if !ok || str("callId") == "" {
				// Reported only once finished: its completion is the whole call.
				i = len(out)
				out = append(out, replayEntry{run: ev.RunID, tool: true, call: toolCall(str("tool"), p["input"])})
			}
			delete(calls, key)
			out[i].output = toolOutput(p)
		}
	}
	return out
}

// personOf is who wrote an event: the person, a GitHub login, or someone.
func personOf(ev replayEvent) string {
	if gh, ok := ev.Payload["github"].(map[string]any); ok {
		if login, _ := gh["login"].(string); login != "" {
			return replayLine("github", login)
		}
	}
	if ev.Who != "" {
		return ev.Who
	}
	if login, ok := strings.CutPrefix(ev.ActorID, "github:"); ok {
		return replayLine("github", login)
	}
	return replaySomeone
}

// said is "Name: words", the images it carried named after the words.
func said(who, text string, attachments any) string {
	return replayLine("said", struct {
		Who, Text string
		Images    []string
	}{who, replayText(text), imageNames(attachments)})
}

// imageNames are the names of the images a message carried, "" for one
// without.
func imageNames(attachments any) []string {
	var out []string
	for _, a := range asList(attachments) {
		m, _ := a.(map[string]any)
		name, _ := m["name"].(string)
		out = append(out, name)
	}
	return out
}

// replayText is a message's words with each image it shows named.
func replayText(s string) string {
	return ReplaceImageRefs(s, func(r ImageRef) string { return replayLine("image-ref", r.name()) })
}

func askedLine(p map[string]any) string {
	var items []QuestionItem
	raw, _ := json.Marshal(p["items"])
	_ = json.Unmarshal(raw, &items)
	type item struct {
		Header, Question string
		Labels           []string
	}
	asked := make([]item, len(items))
	for i, it := range items {
		asked[i] = item{it.Header, it.Question, it.Labels()}
	}
	to, _ := p["toName"].(string)
	prompt, _ := p["prompt"].(string)
	return replayLine("asked", struct {
		To, Prompt string
		Items      []item
	}{to, prompt, asked})
}

// filings are a session.filed's proposal and, per item filed, who filed
// it as what ("item 1 filed by Ana as BL-9"; items are 0-based stored).
func filings(p map[string]any) (string, []string) {
	id, _ := p["proposalId"].(string)
	by, _ := p["by"].(string)
	var out []string
	for _, f := range asList(p["filed"]) {
		m, _ := f.(map[string]any)
		n, _ := m["item"].(float64)
		key, _ := m["key"].(string)
		out = append(out, replayLine("filed", struct {
			N       int
			By, Key string
		}{int(n) + 1, by, key}))
	}
	return id, out
}

func proposedLine(p map[string]any, filed []string) string {
	var items []ProposalItem
	raw, _ := json.Marshal(p["items"])
	_ = json.Unmarshal(raw, &items)
	return replayLine("proposed", struct {
		Items []ProposalItem
		Filed []string
	}{items, filed})
}

func pointWords(point string) string {
	if l := pointLabel[point]; l != "" {
		return l
	}
	return point
}

// toolCall is a call as one line: tool(input), its input capped.
func toolCall(tool string, input any) string {
	tool = replayLine("tool", tool)
	in := ""
	if input != nil {
		raw, _ := json.Marshal(input)
		in = string(raw)
	}
	return tool + "(" + capReplay(in) + ")"
}

// toolOutput is what the ledger kept of a call's output (toolResult's
// shape: output, or stdout and stderr, each a head and maybe a tail),
// with its failure and exit code. Each stored head and tail is capped on
// its own: the writer keeps them within the cap already, and capping them
// joined would cut the tail's end, where a command says how it ended.
func toolOutput(p map[string]any) string {
	var parts []string
	for _, k := range []string{"output", "stdout", "stderr"} {
		m, ok := p[k].(map[string]any)
		if !ok {
			continue
		}
		head, _ := m["head"].(string)
		s := capReplay(head)
		if tail, _ := m["tail"].(string); tail != "" {
			n, _ := m["omittedBytes"].(float64)
			s += replayLine("tail-cut", int(n)) + capReplay(tail)
		}
		if k == "stderr" {
			s = replayLine("stderr", s)
		}
		parts = append(parts, s)
	}
	out := strings.Join(parts, "\n")
	if status, _ := p["status"].(string); status == "error" {
		out = replayLine("error", out)
	}
	if code, ok := p["exitCode"].(float64); ok && code != 0 {
		out += replayLine("exit", int(code))
	}
	if strings.TrimSpace(out) == "" {
		return replayNoOutput
	}
	return out
}

// capReplay keeps s up to replayToolCap bytes, cut on a character
// boundary, and says how much it cut.
func capReplay(s string) string {
	if len(s) <= replayToolCap {
		return s
	}
	cut := replayToolCap
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + replayLine("capped", len(s)-cut)
}

func asList(v any) []any { l, _ := v.([]any); return l }

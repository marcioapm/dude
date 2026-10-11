package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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
// next agent gets it as its own input if at all).
type replayEvent struct {
	Cursor    int64
	Type      string
	RunID     string
	ActorType string
	ActorID   string
	Who       string
	Payload   map[string]any
	Heard     bool
}

// replayTypes are the events the replay renders, or reads to render one.
var replayTypes = []string{
	EvChatMessage, EvRunSteered, "agent.message", EvQuestionAsked, "question.answered", EvQuestionClosed,
	"session.told", EvConductorWoken, EvSessionProposed, EvSessionFiled, EvDecisionTaken, EvFindingResolved,
	EvTaskUpdated, EvSessionRenamed, "agent.tool.called", "agent.tool.completed", "agent.session.replaced", evContextCompacted,
}

// talkerScope (SQL, over events e; $1 the session or task): the talker's
// ledger. A session's: every event of the session (events_session_of_run
// stamps the session on each event of its Runs). A task's: its Chat —
// chat.message and its conductor Runs' events, never a phase Run's.
const (
	sessionScope = `e.session_id = $1`
	taskScope    = `(e.task_id = $1 AND (e.event_type = 'chat.message'
		OR e.run_id IN (SELECT id FROM runs WHERE task_id = $1 AND role = 'conductor' AND kind = 'agent')))`
)

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
	return "## The conversation so far\n\n" + replayLead + "\n\n" + body, nil
}

// fromCompaction is where the replay starts: the newest compaction summary
// the agent's harness kept (agent.context.compacted), and the events after
// it; with none, "" and every event. A summary covers whatever the
// conversation before it held, a replay a Run was given included, so the
// newest is enough.
// Another source of summaries (a harness's own record) would be read here.
func fromCompaction(events []replayEvent) (string, []replayEvent) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type != evContextCompacted {
			continue
		}
		if s, _ := events[i].Payload["summary"].(string); strings.TrimSpace(s) != "" {
			return s, events[i+1:]
		}
	}
	return "", events
}

// evContextCompacted is the phase syncer's record of a compaction.
const evContextCompacted = "agent.context.compacted"

// renderReplayFrom is the summary under its heading, if any, then the
// events rendered, within the budget.
func renderReplayFrom(summary string, events []replayEvent) string {
	return fitReplay(summary, replayEntries(events), replayBudgetTokens)
}

// replayBudgetTokens bounds a replay, estimated by replayTokens.
const replayBudgetTokens = 100_000

// replayTokens estimates the tokens of s: a token per four characters.
func replayTokens(s string) int { return (utf8.RuneCountInString(s) + 3) / 4 }

const outputDropped = "[output dropped]"

// fitReplay renders the entries within budget tokens, shedding while over:
// tool outputs, oldest first, keeping the call; then tool calls whole,
// oldest first; then the oldest turns whole, said in one line. Words are
// never cut, and the summary is never dropped.
func fitReplay(summary string, entries []replayEntry, budget int) string {
	entries = slices.Clone(entries)
	limit := budget * 4
	// Sizes are kept by difference, not re-rendered at each step: a
	// dropped entry takes its text and the blank line before it.
	chars := utf8.RuneCountInString(joinReplay(summary, entries, 0))
	// A dropped entry takes its text, its blank line, and the take-over
	// line before it when it begins a Run's turn.
	takeOver := make([]int, len(entries))
	last := ""
	for i, e := range entries {
		if last != "" && e.run != "" && e.run != last {
			takeOver[i] = utf8.RuneCountInString(tookOver) + 2
		}
		if e.run != "" {
			last = e.run
		}
	}
	size := func(i int) int { return utf8.RuneCountInString(entries[i].text()) + 2 + takeOver[i] }
	for i := range entries {
		if chars <= limit {
			break
		}
		if entries[i].tool && entries[i].output != outputDropped {
			chars -= utf8.RuneCountInString(entries[i].output) - utf8.RuneCountInString(outputDropped)
			entries[i].output = outputDropped
		}
	}
	keep := make([]bool, len(entries))
	for i := range keep {
		keep[i] = true
	}
	for i, e := range entries {
		if chars <= limit {
			break
		}
		if e.tool {
			keep[i], chars = false, chars-size(i)
		}
	}
	omitted := 0
	if chars > limit {
		chars += utf8.RuneCountInString(omittedLine(len(entries))) + 2
	}
	for i := range entries {
		if chars <= limit {
			break
		}
		if keep[i] {
			keep[i], chars = false, chars-size(i)
			omitted++
		}
	}
	kept := entries[:0]
	for i, e := range entries {
		if keep[i] {
			kept = append(kept, e)
		}
	}
	// The estimate cannot know which take-over lines survive the drops: the
	// oldest turns go until the rendering itself fits.
	out := joinReplay(summary, kept, omitted)
	for len(kept) > 0 && utf8.RuneCountInString(out) > limit {
		kept, omitted = kept[1:], omitted+1
		out = joinReplay(summary, kept, omitted)
	}
	return out
}

// tookOver marks where another Run's agent took the conversation on.
const tookOver = "[A new agent took over here.]"

func omittedLine(n int) string { return fmt.Sprintf("[%d earlier messages omitted]", n) }

// joinReplay renders the summary under its heading, then the entries,
// after a line saying how many earlier ones were omitted.
func joinReplay(summary string, entries []replayEntry, omitted int) string {
	var b strings.Builder
	if omitted > 0 {
		b.WriteString(omittedLine(omitted))
	}
	last := ""
	for _, e := range entries {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if last != "" && e.run != "" && e.run != last {
			b.WriteString(tookOver + "\n\n")
		}
		if e.run != "" {
			last = e.run
		}
		b.WriteString(e.text())
	}
	body := b.String()
	if summary == "" {
		return body
	}
	out := "## Earlier, as the agent summarised it\n\n" + strings.TrimSpace(summary)
	if body != "" {
		out += "\n\n## Since then\n\n" + body
	}
	return out
}

const replayLead = "What was said here before you, oldest first, from dude's record (\"You\" is this conversation's agent, " +
	"before you). Tool calls show their input and output, cut where long; thoughts are not kept. Images are named, not attached."

func replayEvents(ctx context.Context, tx pgx.Tx, of Talker) ([]replayEvent, error) {
	scope, id, role := sessionScope, of.SessionID, "brainstorm"
	where := `session_id = $1`
	if of.SessionID == "" {
		scope, id, role, where = taskScope, of.TaskID, "conductor", `task_id = $1`
	}
	var had bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE `+where+` AND role = $2::agent_role AND kind = 'agent')`,
		id, role).Scan(&had); err != nil || !had {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT e.cursor, e.event_type, COALESCE(e.run_id, ''), e.actor_type, COALESCE(e.actor_id, ''),
			COALESCE((SELECT p.name FROM people p WHERE p.id = e.actor_id),
				(SELECT p.name FROM api_keys k JOIN people p ON p.id = k.person_id WHERE k.id = e.actor_id), ''),
			e.payload,
			NOT (e.payload ? 'directiveId')
				OR EXISTS (SELECT 1 FROM directives d WHERE d.id = e.payload->>'directiveId' AND d.run_id = e.run_id AND d.delivered_at IS NOT NULL)
				OR EXISTS (SELECT 1 FROM directives d WHERE d.resends = e.payload->>'directiveId' AND d.run_id = e.run_id AND d.delivered_at IS NOT NULL)
		FROM events e WHERE `+scope+` AND e.event_type = ANY($2)
			AND e.cursor >= COALESCE((SELECT max(e.cursor) FROM events e WHERE `+scope+`
				AND e.event_type = '`+evContextCompacted+`' AND btrim(e.payload->>'summary') <> ''), 0)
		ORDER BY e.cursor`, id, replayTypes)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[replayEvent])
}

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

// renderReplay renders the events, in their order, as the agent reads them.
func renderReplay(events []replayEvent) string { return joinReplay("", replayEntries(events), 0) }

func replayEntries(events []replayEvent) []replayEntry {
	// A proposal's line says what became of it: its filings come later.
	filed := map[string][]string{}
	for _, ev := range events {
		if ev.Type != EvSessionFiled {
			continue
		}
		id, _ := ev.Payload["proposalId"].(string)
		by, _ := ev.Payload["by"].(string)
		if by == "" {
			by = "someone"
		}
		for _, f := range asList(ev.Payload["filed"]) {
			m, _ := f.(map[string]any)
			n, _ := m["item"].(float64)
			key, _ := m["key"].(string)
			filed[id] = append(filed[id], fmt.Sprintf("item %d filed by %s as %s", int(n)+1, by, key))
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
				add(ev, fmt.Sprintf("You (on pull request %s#%d): %s", repo, int(n), replayText(str("text"))))
				continue
			}
			add(ev, said(personOf(ev), str("text"), p["attachments"]))
		case EvRunSteered:
			// A resend (Retry, Interrupt now) repeats words already said.
			if str("supersedes") != "" || str("by") == "conductor" {
				continue
			}
			add(ev, said(personOf(ev), str("text"), p["attachments"]))
		case "agent.message":
			if t := str("text"); strings.TrimSpace(t) != "" {
				add(ev, "You: "+t)
			}
		case EvQuestionAsked:
			add(ev, askedLine(p))
		case "question.answered":
			line := said(personOf(ev)+" answered", str("answer"), p["attachments"])
			if note := str("note"); note != "" {
				line += "\n" + personOf(ev) + ", also: " + replayText(note)
			}
			add(ev, line)
		case EvQuestionClosed:
			if str("by") == "withdrawn" {
				add(ev, "dude: your question was withdrawn; nobody answered it.")
			} else {
				add(ev, "dude: your question was settled elsewhere, unanswered.")
			}
		case "session.told", EvConductorWoken:
			add(ev, "dude: "+str("text"))
		case EvSessionProposed:
			add(ev, proposedLine(p, filed[str("proposalId")]))
		case EvDecisionTaken:
			line := fmt.Sprintf("You decided, at %s: %s", pointWords(str("point")), str("action"))
			if ph := str("phase"); ph != "" {
				line += " (" + ph + ")"
			}
			if note := str("note"); note != "" {
				line += ": " + oneLine(note)
			}
			add(ev, line+".")
		case EvFindingResolved:
			if str("by") == "conductor" {
				// In the words the briefing's findings use (AcceptedBy).
				add(ev, fmt.Sprintf("Finding %s: %s.", str("findingId"), AcceptedBy(str("note"))))
			}
		case EvTaskUpdated:
			if str("by") == "conductor" {
				var what []string
				if _, ok := p["goal"]; ok {
					what = append(what, "goal")
				}
				if _, ok := p["acceptanceCriteria"]; ok {
					what = append(what, "acceptance criteria")
				}
				add(ev, "You changed the task's "+strings.Join(what, " and ")+".")
			}
		case EvSessionRenamed:
			if str("by") == ByAgent {
				add(ev, fmt.Sprintf("You named the session %q.", str("title")))
			} else {
				add(ev, fmt.Sprintf("%s named the session %q.", personOf(ev), str("title")))
			}
		case "agent.session.replaced":
			add(ev, "[Here the agent's session restarted without its conversation.]")
		case "agent.tool.called":
			key := ev.RunID + "/" + str("callId")
			calls[key] = len(out)
			out = append(out, replayEntry{run: ev.RunID, tool: true, call: toolCall(str("tool"), p["input"]),
				output: "[no output recorded]"})
		case "agent.tool.completed":
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
			return login + " (GitHub)"
		}
	}
	if ev.Who != "" {
		return ev.Who
	}
	if login, ok := strings.CutPrefix(ev.ActorID, "github:"); ok {
		return login + " (GitHub)"
	}
	return "Someone"
}

// said is "Name: words", the images it carried named after the words.
func said(who, text string, attachments any) string {
	line := who + ": " + replayText(text)
	for _, a := range asList(attachments) {
		m, _ := a.(map[string]any)
		if name, _ := m["name"].(string); name != "" {
			line += " [image: " + name + "]"
		} else {
			line += " [image]"
		}
	}
	return line
}

// replayText is a message's words with each image it shows named.
func replayText(s string) string {
	return ReplaceImageRefs(s, func(r ImageRef) string { return "[image: " + r.name() + "]" })
}

func askedLine(p map[string]any) string {
	var items []QuestionItem
	raw, _ := json.Marshal(p["items"])
	_ = json.Unmarshal(raw, &items)
	who := "You asked"
	if to, _ := p["toName"].(string); to != "" {
		who += " " + to
	}
	if len(items) == 0 {
		prompt, _ := p["prompt"].(string)
		return who + ": " + prompt
	}
	item := func(it QuestionItem) string {
		s := it.Question
		if labels := it.Labels(); len(labels) > 0 {
			s += " (choices: " + strings.Join(labels, "; ") + ")"
		}
		return s
	}
	if len(items) == 1 {
		return who + ": " + item(items[0])
	}
	var b strings.Builder
	b.WriteString(who + ":")
	for i, it := range items {
		fmt.Fprintf(&b, "\n%d. %s — %s", i+1, it.Header, item(it))
	}
	return b.String()
}

func proposedLine(p map[string]any, filed []string) string {
	var items []ProposalItem
	raw, _ := json.Marshal(p["items"])
	_ = json.Unmarshal(raw, &items)
	parts := make([]string, len(items))
	for i, it := range items {
		switch it.Kind {
		case "epic", "task":
			parts[i] = fmt.Sprintf("%d. %s %q in %s", i+1, it.Kind, it.Title, it.Project)
		case "edit":
			parts[i] = fmt.Sprintf("%d. an edit to %s", i+1, it.Task)
		case "comment":
			parts[i] = fmt.Sprintf("%d. a comment on %s", i+1, it.Task)
		default:
			parts[i] = fmt.Sprintf("%d. %s", i+1, it.Kind)
		}
	}
	outcome := "none filed"
	if len(filed) > 0 {
		outcome = strings.Join(filed, ", ")
	}
	return "You proposed: " + strings.Join(parts, "; ") + " — " + outcome + "."
}

func pointWords(point string) string {
	if l := pointLabel[point]; l != "" {
		return l
	}
	return point
}

// toolCall is a call as one line: tool(input), its input capped.
func toolCall(tool string, input any) string {
	if tool == "" {
		tool = "tool"
	}
	in := ""
	if input != nil {
		raw, _ := json.Marshal(input)
		in = string(raw)
	}
	return tool + "(" + capReplay(in) + ")"
}

// toolOutput is what the ledger kept of a call's output (toolResult's
// shape: output, or stdout and stderr, each a head and maybe a tail),
// capped, with its failure and exit code.
func toolOutput(p map[string]any) string {
	var parts []string
	for _, k := range []string{"output", "stdout", "stderr"} {
		m, ok := p[k].(map[string]any)
		if !ok {
			continue
		}
		head, _ := m["head"].(string)
		s := head
		if tail, _ := m["tail"].(string); tail != "" {
			n, _ := m["omittedBytes"].(float64)
			s += fmt.Sprintf("\n[… %d bytes cut …]\n", int(n)) + tail
		}
		if k == "stderr" {
			s = "stderr: " + s
		}
		parts = append(parts, s)
	}
	out := capReplay(strings.Join(parts, "\n"))
	if status, _ := p["status"].(string); status == "error" {
		out = "error: " + out
	}
	if code, ok := p["exitCode"].(float64); ok && code != 0 {
		out += fmt.Sprintf(" (exit %d)", int(code))
	}
	if strings.TrimSpace(out) == "" {
		return "[no output]"
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
	return s[:cut] + fmt.Sprintf("[… %d bytes cut]", len(s)-cut)
}

func asList(v any) []any { l, _ := v.([]any); return l }

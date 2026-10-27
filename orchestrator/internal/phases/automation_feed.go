package phases

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"
	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// AutomationRunRef names an automation run whose output is translated
// into the ledger: its id (automation_runs), organisation and project.
type AutomationRunRef struct {
	ID, Org, ProjectID string
	// A command's or server's run: its plain output is the log. An
	// agent's plain stdout repeats its reply and is not recorded.
	Lines bool
}

// AutomationFeed translates an automation run's agent output with the
// Run's translator (agentEvent and the harness translators it calls), so
// an automation run's messages, thoughts and tool calls are the same
// events, with the same payloads, as a task's Run's — keyed by
// automation_run_id. What a Run's translator also does with that output
// (turn state, stall clocks, resumes, directives) has no row to act on
// here and is skipped; the automations loop decides the run's end.
type AutomationFeed struct {
	t *translator
	s *Syncer
	// A command's or server's output line not yet ended by a newline, per stream.
	partial map[string]string
	// Per dude tool, the cursor of the last of its records joined to a completion.
	joined map[string]int64
	logs   bool
	// A server's log: the newest line time recorded, and how many lines at it.
	serverAt   int64
	serverSeen int
}

// automationFeedState is automation_runs.feed_state.
type automationFeedState struct {
	Message   string            `json:"message,omitempty"`
	Thought   string            `json:"thought,omitempty"`
	Open      []string          `json:"open,omitempty"`
	Calls     map[string]string `json:"calls,omitempty"`
	Announced map[string]string `json:"announced,omitempty"`
	Harness   json.RawMessage   `json:"harness,omitempty"`
	Partial   map[string]string `json:"partial,omitempty"`
	// The placement whose session the agent has: frames of a later one
	// before its lux.session are a resume's replay. At least 1, so a run
	// on a kept session (placement 2 on) skips the replay its resume starts with.
	SessionEpoch int              `json:"sessionEpoch,omitempty"`
	Joined       map[string]int64 `json:"joined,omitempty"`
	ServerAt     int64            `json:"serverAt,omitempty"`
	ServerSeen   int              `json:"serverSeen,omitempty"`
}

// LoadAutomationFeed restores the feed of run as its last batch saved it.
func LoadAutomationFeed(ctx context.Context, tx pgx.Tx, run AutomationRunRef) (*AutomationFeed, error) {
	var st automationFeedState
	if err := tx.QueryRow(ctx, `SELECT feed_state FROM automation_runs WHERE id = $1`, run.ID).Scan(&st); err != nil {
		return nil, err
	}
	t := &translator{run: phaseRun{AutomationRunID: run.ID, ID: run.ID, Org: run.Org, ProjectID: run.ProjectID, Status: statusRunning},
		seenCalls: map[string]string{}, openCalls: map[string]bool{}, waitsOn: map[string][]string{}, announced: st.Announced}
	if len(st.Harness) > 0 {
		if err := json.Unmarshal(st.Harness, &t.harnessState); err != nil {
			return nil, err
		}
	}
	t.message.WriteString(st.Message)
	t.sessionEpoch = max(st.SessionEpoch, 1)
	f := &AutomationFeed{t: t, s: &Syncer{}, partial: st.Partial, joined: st.Joined, logs: run.Lines, serverAt: st.ServerAt, serverSeen: st.ServerSeen}
	t.completing = f.joinDudeRecord
	t.thought.WriteString(st.Thought)
	maps.Copy(t.seenCalls, st.Calls)
	for _, id := range st.Open {
		t.openCalls[id] = true
	}
	return f, nil
}

// dudeTools are the tool server's tools an automation's agent has, whose
// records (agent.tool.dude, and post_to_chat's posts and refusals) join
// the harness's completion of the call.
var dudeTools = map[string]bool{"create_task": true, "github_search": true, "github_issue": true, "github_pull_request": true,
	"github_check_log": true, "github_comment": true, "post_to_chat": true}

// joinDudeRecord puts dude's record of one of its tools on the harness's
// completion of the call, as payload.dude {eventId, result, posted, error}.
// The tool server knows a call by its MCP request, not by the harness's
// call id, so a completion takes the oldest record of its tool not yet
// taken: the server answers before the harness reports the call done, so
// the record is in by then, and calls of one tool end in the order the
// server answered them.
func (f *AutomationFeed) joinDudeRecord(ctx context.Context, tx pgx.Tx, payload map[string]any) error {
	name, _ := payload["tool"].(string)
	tool := strings.TrimPrefix(name, "dude_")
	if !dudeTools[tool] || payload["status"] == "aborted" {
		return nil
	}
	var id, typ string
	var cursor int64
	var p map[string]any
	err := tx.QueryRow(ctx, `SELECT id, event_type, cursor, payload FROM events
		WHERE automation_run_id = $1 AND cursor > $2 AND payload->>'tool' = $3
		  AND (event_type = 'agent.tool.dude' OR event_type = 'slack.message.refused')
		ORDER BY cursor LIMIT 1`, f.t.run.ID, f.joined[tool], tool).Scan(&id, &typ, &cursor, &p)
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	dude := map[string]any{"eventId": id}
	if typ == "slack.message.refused" {
		dude["error"] = p["error"]
		payload["status"] = "error"
	} else if r, ok := p["result"]; ok {
		dude["result"] = r
	}
	if tool == "post_to_chat" && typ == "agent.tool.dude" {
		var posted map[string]any
		err := tx.QueryRow(ctx, `SELECT payload FROM events WHERE automation_run_id = $1 AND event_type = 'slack.message.posted'
			AND cursor > $2 AND cursor < $3 ORDER BY cursor DESC LIMIT 1`, f.t.run.ID, f.joined[tool], cursor).Scan(&posted)
		if err != nil && !db.IsNotFound(err) {
			return err
		}
		if posted != nil {
			dude["posted"] = map[string]any{"channel": posted["channel"], "channelId": posted["channelId"], "ts": posted["ts"],
				"permalink": posted["permalink"]}
		}
	}
	if f.joined == nil {
		f.joined = map[string]int64{}
	}
	f.joined[tool] = cursor
	payload["dude"] = dude
	return nil
}

// Apply translates one frame of the run's lux output. An agent's record
// goes through the Run's translator; a command's or server's plain output
// is recorded line by line (automation.log.line); the rest is the loop's.
func (f *AutomationFeed) Apply(ctx context.Context, tx pgx.Tx, fr lux.Frame) error {
	if fr.Kind != "record" {
		return nil
	}
	if fr.Event == nil {
		if !f.logs {
			return nil
		}
		return f.lines(ctx, tx, fr.Channel, fr.Data)
	}
	switch fr.Event.Type {
	case "lux.session":
		// A resumed agent replays its conversation before it says its
		// session is back: that replay is an earlier run's (replaying).
		if fr.Epoch > f.t.sessionEpoch {
			f.t.sessionEpoch = fr.Epoch
		}
		return nil
	case "lux.activity":
		var d struct{ Activity string }
		_ = json.Unmarshal(fr.Event.Data, &d)
		if d.Activity == "idle" && f.t.claudeHalf == "" {
			return f.t.idle(ctx, tx, f.s)
		}
		if d.Activity == "idle" {
			f.t.claudeIdle = true
		}
		return nil
	}
	if strings.HasPrefix(fr.Event.Type, "lux.") {
		return nil
	}
	return f.t.agentEvent(ctx, tx, f.s, fr)
}

// EvLogLine is one line a command or server printed: {stream, text}.
const EvLogLine = "automation.log.line"

// EvLogExited ends a command's lines: {exitCode}.
const EvLogExited = "automation.log.exited"

// logLineMax bounds one recorded line; past it the line is cut, saying so.
const logLineMax = 4096

func (f *AutomationFeed) lines(ctx context.Context, tx pgx.Tx, stream, data string) error {
	if stream != "stderr" {
		stream = "stdout"
	}
	if f.partial == nil {
		f.partial = map[string]string{}
	}
	text := f.partial[stream] + data
	parts := strings.Split(text, "\n")
	f.partial[stream] = parts[len(parts)-1]
	if len(f.partial[stream]) > logLineMax {
		parts = append(parts[:len(parts)-1], f.partial[stream], "")
		f.partial[stream] = ""
	}
	for _, line := range parts[:len(parts)-1] {
		if err := f.line(ctx, tx, stream, line); err != nil {
			return err
		}
	}
	return nil
}

func (f *AutomationFeed) line(ctx context.Context, tx pgx.Tx, stream, line string) error {
	line = strings.TrimSuffix(line, "\r")
	p := map[string]any{"stream": stream, "text": line}
	if len(line) > logLineMax {
		p["text"], p["cut"] = cutRunes(line[:logLineMax], false), len(line)
	}
	return f.s.event(ctx, tx, f.t.run, EvLogLine, ledger.ActorAgent, p)
}

// End records what the run's output left unfinished when it ended: a
// line with no newline, a reply being streamed, and each call still open,
// as aborted with note (why the run stopped; "" for none). exitCode, when
// not nil, ends a command's lines.
func (f *AutomationFeed) End(ctx context.Context, tx pgx.Tx, note string, exitCode ...int) error {
	for _, stream := range []string{"stdout", "stderr"} {
		if rest := f.partial[stream]; rest != "" {
			f.partial[stream] = ""
			if err := f.line(ctx, tx, stream, rest); err != nil {
				return err
			}
		}
	}
	if err := f.t.flush(ctx, tx, f.s); err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(f.t.openCalls)) {
		p := map[string]any{"tool": f.t.seenCalls[id], "callId": id, "status": "aborted"}
		if note != "" {
			p["note"] = note
		}
		if err := f.t.callEnded(ctx, tx, f.s, nil, p); err != nil {
			return err
		}
	}
	if len(exitCode) > 0 {
		return f.s.event(ctx, tx, f.t.run, EvLogExited, ledger.ActorSystem, map[string]any{"exitCode": exitCode[0]})
	}
	return nil
}

// ServerLine is one line of a server's container log as lux keeps it.
type ServerLine struct {
	T      int64  `json:"t"` // Unix milliseconds
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// ServerLines records the lines of a server's log (lux's tail, oldest
// first) not recorded before: each after the newest time recorded, and
// at that time those of its lines not yet counted.
func (f *AutomationFeed) ServerLines(ctx context.Context, tx pgx.Tx, lines []ServerLine) error {
	seen := 0
	for _, l := range lines {
		if l.T < f.serverAt {
			continue
		}
		if l.T == f.serverAt {
			seen++
			if seen <= f.serverSeen {
				continue
			}
		} else {
			f.serverAt, f.serverSeen, seen = l.T, 0, 1
		}
		f.serverSeen = seen
		stream := l.Stream
		if stream != "stderr" {
			stream = "stdout"
		}
		for _, text := range strings.Split(strings.TrimRight(l.Text, "\n"), "\n") {
			p := map[string]any{"stream": stream, "text": text, "server": true}
			if len(text) > logLineMax {
				p["text"], p["cut"] = cutRunes(text[:logLineMax], false), len(text)
			}
			if _, err := ledger.Append(ctx, tx, ledger.Event{Type: EvLogLine, OrganizationID: f.t.run.Org, ProjectID: f.t.run.ProjectID,
				AutomationRunID: f.t.run.ID, OccurredAt: time.UnixMilli(l.T), ActorType: ledger.ActorAgent, ActorID: f.t.run.ID,
				Source: ledger.SourceRunner, Payload: p}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Save keeps what the feed holds for the next batch, in tx.
func (f *AutomationFeed) Save(ctx context.Context, tx pgx.Tx) error {
	t := f.t
	harness, err := json.Marshal(t.harnessState)
	if err != nil {
		return err
	}
	calls := map[string]string{}
	for id := range t.openCalls {
		calls[id] = t.seenCalls[id]
	}
	st := automationFeedState{Message: t.message.String(), Thought: t.thought.String(), Open: slices.Sorted(maps.Keys(t.openCalls)),
		Calls: calls, Announced: t.announced, Harness: harness, Partial: f.partial, SessionEpoch: t.sessionEpoch, Joined: f.joined,
		ServerAt: f.serverAt, ServerSeen: f.serverSeen}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	red, err := ledger.Redactor(ctx, tx, t.run.ID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE automation_runs SET feed_state = $2::jsonb WHERE id = $1`, t.run.ID, red.JSON(raw))
	return err
}

// OpenCalls are the calls started and not ended, by id.
func (f *AutomationFeed) OpenCalls() []string { return slices.Sorted(maps.Keys(f.t.openCalls)) }

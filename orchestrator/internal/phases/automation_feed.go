package phases

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// AutomationRunRef names an automation run whose output is translated
// into the ledger: its id (automation_runs), organisation and project.
type AutomationRunRef struct {
	ID, Org, ProjectID string
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
	SessionEpoch int `json:"sessionEpoch,omitempty"`
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
	t.thought.WriteString(st.Thought)
	maps.Copy(t.seenCalls, st.Calls)
	for _, id := range st.Open {
		t.openCalls[id] = true
	}
	return &AutomationFeed{t: t, s: &Syncer{}, partial: st.Partial}, nil
}

// Apply translates one frame of the run's lux output. An agent's record
// goes through the Run's translator; a command's or server's plain output
// is recorded line by line (automation.log.line); the rest is the loop's.
func (f *AutomationFeed) Apply(ctx context.Context, tx pgx.Tx, fr lux.Frame) error {
	if fr.Kind != "record" {
		return nil
	}
	if fr.Event == nil {
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
		Calls: calls, Announced: t.announced, Harness: harness, Partial: f.partial, SessionEpoch: t.sessionEpoch}
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

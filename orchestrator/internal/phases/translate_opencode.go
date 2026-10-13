package phases

import (
	"cmp"
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// opencodeEvent translates what lux relays of OpenCode's event bus about
// child sessions (opencode.<type>), which ACP — the Run's own session
// only — never says: a task tool's subagent session (session), which
// parent call it is (task), its tool parts (part), its completed messages
// (message), and a permission asked on it (permission).
func (t *translator) opencodeEvent(ctx context.Context, tx pgx.Tx, s *Syncer, typ string, f lux.Frame) error {
	if t.replaying(f.Epoch) {
		return nil
	}
	var e opencodeRecord
	if json.Unmarshal(f.Event.Data, &e) != nil {
		return nil
	}
	switch typ {
	case "session":
		// A session with no parent is the Run's own.
		if e.ParentID == "" || e.ID == "" {
			return nil
		}
		c := t.opencodeChild(e.ID)
		c.Title = cmp.Or(c.Title, e.Title)
		if e.ParentID != t.SessionID && t.child(e.ParentID) != nil {
			c.ParentChildID = e.ParentID
		}
		if c.CallID == "" {
			return nil // started once its task says which call it is
		}
		return t.startChild(ctx, tx, s, c)
	case "task":
		if e.ChildSessionID == "" || e.CallID == "" {
			return nil
		}
		c := t.opencodeChild(e.ChildSessionID)
		c.CallID, c.Model = e.CallID, cmp.Or(c.Model, e.Model)
		if e.SessionID != t.SessionID && t.child(e.SessionID) != nil {
			c.ParentChildID = e.SessionID
		}
		if c.AgentType == "" || c.Title == "" {
			t.opencodeTaskInput(ctx, tx, c)
		}
		if err := t.startChild(ctx, tx, s, c); err != nil {
			return err
		}
		switch e.Status {
		case "completed":
			return t.endChild(ctx, tx, s, c, "completed", "", nil, 0)
		case "error":
			return t.endChild(ctx, tx, s, c, "failed", "", nil, 0)
		}
	case "part":
		c := t.child(e.SessionID)
		if c == nil || e.CallID == "" {
			return nil
		}
		switch e.Status {
		case "running":
			return t.callStarted(ctx, tx, s, c, map[string]any{"tool": e.Tool, "callId": e.CallID, "input": e.Input, "title": e.Title})
		case "completed", "error":
			if err := t.callStarted(ctx, tx, s, c, map[string]any{"tool": e.Tool, "callId": e.CallID, "input": e.Input,
				"title": e.Title}); err != nil {
				return err
			}
			st, out := "completed", e.Output
			if e.Status == "error" {
				st, out = "error", cmp.Or(e.Error, e.Output)
			}
			payload := map[string]any{"tool": e.Tool, "callId": e.CallID, "status": st, "title": e.Title}
			if out != "" {
				payload["output"] = capOutput(out)
			}
			return t.callEnded(ctx, tx, s, c, payload)
		default:
			// pending: announced, its input not known yet.
			t.touchChild(c, e.Tool+" pending", nil)
		}
	case "message":
		c := t.child(e.SessionID)
		if c == nil {
			return nil
		}
		var tokens map[string]any
		if tk := e.Tokens; tk != nil {
			tokens = map[string]any{"input": tk.Input, "output": tk.Output, "cacheRead": tk.Cache.Read, "cacheWrite": tk.Cache.Write}
			c.Tokens.Input += tk.Input + tk.Cache.Read
			c.Tokens.Cached += tk.Cache.Read
			c.Tokens.Output += tk.Output
		}
		return t.childMessage(ctx, tx, s, c, e.Text, e.Cost, tokens)
	case "permission":
		c := t.child(e.SessionID)
		if c == nil {
			return nil
		}
		return t.childWaiting(ctx, tx, s, c, e.Permission, e.Patterns, e.Answered)
	}
	return nil
}

// opencodeRecord is every field of the opencode.* records the translator
// reads; each type uses its own.
type opencodeRecord struct {
	ID             string  `json:"id"`
	ParentID       string  `json:"parentID"`
	Title          string  `json:"title"`
	SessionID      string  `json:"sessionID"`
	CallID         string  `json:"callID"`
	ChildSessionID string  `json:"childSessionID"`
	Model          string  `json:"model"`
	Status         string  `json:"status"`
	Tool           string  `json:"tool"`
	Input          any     `json:"input"`
	Output         string  `json:"output"`
	Error          string  `json:"error"`
	Text           string  `json:"text"`
	Cost           float64 `json:"cost"`
	Tokens         *struct {
		Input  int64 `json:"input"`
		Output int64 `json:"output"`
		Cache  struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Permission string   `json:"permission"`
	Patterns   []string `json:"patterns"`
	Answered   string   `json:"answered"`
}

// opencodeChild is the child session id, kept from its first mention.
func (t *translator) opencodeChild(id string) *childState {
	if c := t.child(id); c != nil {
		return c
	}
	return t.addChild(&childState{ID: id, Kind: childTask})
}

// opencodeTaskInput names a child by the task call that started it: the
// description the parent gave and the subagent's type, from the call as
// the ledger has it (ACP's rawInput).
func (t *translator) opencodeTaskInput(ctx context.Context, tx pgx.Tx, c *childState) {
	var input struct {
		Description  string `json:"description"`
		SubagentType string `json:"subagent_type"`
	}
	var raw []byte
	if tx.QueryRow(ctx, `SELECT payload->'input' FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'callId' = $3
		ORDER BY cursor LIMIT 1`, t.run.ID, evToolCalled, c.CallID).Scan(&raw) != nil {
		return
	}
	if json.Unmarshal(raw, &input) == nil {
		c.Title, c.AgentType = cmp.Or(input.Description, c.Title), cmp.Or(c.AgentType, input.SubagentType)
	}
}

package phases

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// EvRunSetupProgress is the latest line a repository's setup script
// printed while it runs (RunSetupProgress in
// packages/domain/src/events/types.ts). Payload: { repository, line, epoch }:
// the last non-empty line of init's output, redacted, at most
// setupLineMax characters, written at most every setupLineEvery.
const EvRunSetupProgress = "run.setup.progress"

// stageSetup is the start stage of a repository's setup script: written
// when init says it runs one (setupRunningPrefix), not on init's start,
// so a resume whose marker skips the script, or a Run with none, has none.
const stageSetup = "setup"

// stageRunning is lux's stage of the harness coming up, written again
// once a setup script is done (with setup: true): the start goes on to the
// agent, while lux already reports the Run running.
const stageRunning = "running"

const (
	setupLineMax   = 200
	setupLineEvery = 3 * time.Second
)

// ansiEscape matches a terminal escape sequence: CSI, OSC (to BEL or ST),
// or a lone escape and its next byte.
var ansiEscape = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?|.)")

// setupRunning is the repository the last marker line in tail names, ""
// when there is none: the script it introduced has started.
func setupRunning(tail string) string {
	i := strings.LastIndex(tail, setupRunningPrefix)
	if i < 0 || (i > 0 && tail[i-1] != '\n') {
		return ""
	}
	name, ok := strings.CutSuffix(tail[i+len(setupRunningPrefix):], "\n")
	if j := strings.IndexByte(name, '\n'); j >= 0 {
		name, ok = name[:j], true
	}
	if !ok {
		return ""
	}
	return name
}

// setupLine is the last non-empty line init printed after its last marker
// line, as a terminal shows it (only what follows a carriage return, no
// escape codes), "" when there is none. A line that begins where tail was
// cut is not known whole (a value redaction must see could be cut in two):
// it is passed over.
func setupLine(tail string, cut bool) string {
	if i := strings.LastIndex(tail, "\n"+setupRunningPrefix); i >= 0 {
		tail, cut = tail[i+1:], false
		if j := strings.IndexByte(tail, '\n'); j >= 0 {
			tail = tail[j+1:]
		} else {
			return ""
		}
	} else if strings.HasPrefix(tail, setupRunningPrefix) && !cut {
		j := strings.IndexByte(tail, '\n')
		if j < 0 {
			return ""
		}
		tail = tail[j+1:]
	}
	lines := strings.Split(tail, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if i == 0 && cut {
			return ""
		}
		l := lines[i]
		if j := strings.LastIndex(strings.TrimRight(l, "\r"), "\r"); j >= 0 {
			l = l[j+1:]
		}
		l = strings.TrimSpace(ansiEscape.ReplaceAllString(l, ""))
		if l != "" && !strings.HasPrefix(l, setupRunningPrefix) && !strings.HasPrefix(l, setupFailedPrefix) {
			return l
		}
	}
	return ""
}

// shorten keeps the first max characters of s, an ellipsis after them.
func shorten(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// setupProgress follows init while it runs a setup script: the setup
// stage once its marker line names the repository, its latest line at
// most every setupLineEvery, and, once init is done, the agent's stage.
// was is the init state before f was recorded. A failed init writes
// nothing here: its failure path is the Run's end (ended).
func (t *translator) setupProgress(ctx context.Context, tx pgx.Tx, s *Syncer, f lux.Frame, was InitOutput) error {
	if f.Event != nil {
		if f.Event.Type != "lux.init" || !was.Running || t.init.Running || !was.Staged || t.init.Failed != nil {
			return nil
		}
		// Done with every script: the harness comes up next.
		return s.event(ctx, tx, t.run, EvRunStage, ledger.ActorSystem,
			map[string]any{"stage": stageRunning, "since": s.clock().UTC(), "epoch": t.epochOf(f), "setup": true})
	}
	o := &t.init
	if t.run.AutomationRunID != "" || !o.Running || (f.Channel != "stdout" && f.Channel != "stderr") {
		return nil
	}
	repo := setupRunning(o.Tail)
	if repo == "" && !o.Staged {
		return nil
	}
	now := s.clock()
	if !o.Staged {
		o.Staged, o.Repository = true, repo
		return s.event(ctx, tx, t.run, EvRunStage, ledger.ActorSystem,
			map[string]any{"stage": stageSetup, "since": now.UTC(), "epoch": t.epochOf(f), "repository": repo})
	}
	if repo != "" {
		o.Repository = repo
	}
	if !o.LineAt.IsZero() && now.Sub(o.LineAt) < setupLineEvery {
		return nil
	}
	line := setupLine(o.Tail, len(o.Tail) >= initOutputMax)
	if line == "" {
		return nil
	}
	red, err := ledger.Redactor(ctx, tx, t.run.ID)
	if err != nil {
		return err
	}
	// Redacted whole, then shortened: a value cut in two is never matched.
	line = shorten(red.String(line), setupLineMax)
	if line == o.Line {
		return nil
	}
	o.Line, o.LineAt = line, now
	return s.event(ctx, tx, t.run, EvRunSetupProgress, ledger.ActorSystem,
		map[string]any{"repository": o.Repository, "line": line, "epoch": t.epochOf(f)})
}

func (t *translator) epochOf(f lux.Frame) int {
	if f.Epoch > 0 {
		return f.Epoch
	}
	return t.epoch
}

// clock is the syncer's time: its test clock, else now.
func (s *Syncer) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

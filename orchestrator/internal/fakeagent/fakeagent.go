// Package fakeagent is the scripted agent dude's tests run instead of a
// model: what each phase does, written once.
//
// It is played two ways. Against the fake lux (the default suites), the
// fake plays it directly as a Behaviour. Against a real lux (the contract
// suite), it is compiled to a lux-fake script — lux-fake being the
// scripted ACP agent lux ships for tests — and sent as the Run's prompt.
// Both describe the same agent, so the two suites test the same thing.
//
// The loop it exercises: the implementer commits; the reviewer raises one
// blocking finding unless the tree already holds the fixer's file; the fixer
// adds that file; the re-review is then clean; the simplifier commits.
// Deciding from the tree rather than a counter keeps a replayed Run
// deterministic.
//
// A model named "fake/hang" never finishes its turn, which is how a test
// gets a live agent to steer, pause and abort.
package fakeagent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PublishedDir is $LUX_ARTIFACTS in a lux container. (lux.PublishedDir; not
// imported, to keep this package free of the client.)
const PublishedDir = "/.lux/run/artifacts"

// ModelPrefix selects the scripted agent in a project's model settings.
const ModelPrefix = "fake/"

// HangModel keeps its agent busy until stopped.
const HangModel = "fake/hang"

// CommandModel's implementer runs a long command and never finishes its
// turn: a steer sent meanwhile is taken at once and read at its next step,
// once the command finishes (the fake lux's POST /fake/runs/{id}/finish-tools).
const CommandModel = "fake/command"

// ToolsModel's implementer calls dude's list_tasks tool (through lux-fake's
// MCP client) before its usual work, so a real lux's handling of dude's
// tools is exercised.
const ToolsModel = "fake/tools"

// RequestModel's implementer asks for the project's "web" repository to read,
// and waits (never finishing its turn) until it is resumed with it.
const RequestModel = "fake/request"

// WaitModel's implementer asks for the project's "web" repository and ends
// its turn waiting on it: parked until a person decides, and its work done
// in the turn the resume starts.
const WaitModel = "fake/wait"

// LiveModel's implementer writes files into its checkout without committing
// them, and keeps working (never finishing its turn): what the live diff
// shows while an agent works. It saves notes and a screenshot for people as
// it goes, so a pause collects them; resumed, it finishes, saving its notes
// again (a second version) and committing.
const LiveModel = "fake/live"

// LiveNotes is what LiveModel's implementer saves: while working, then when
// it finishes.
var LiveNotes = [2]string{"# Notes\n\nStill working.", "# Notes\n\nFinished."}

// LiveScreenshot is a 1×1 PNG LiveModel's implementer saves as it works.
const LiveScreenshot = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89" +
	"\x00\x00\x00\rIDATx\x9cc\xf8\xcf\xc0\xf0\x1f\x00\x05\x00\x01\xff\x89\x99=\x1d\x00\x00\x00\x00IEND\xaeB`\x82"

// LiveEdits are what LiveModel's implementer writes: a new file and a
// change to the README the test repositories start with.
var LiveEdits = map[string]string{
	"LIVE.md":   "# Live\n\nWritten while the agent works.\n",
	"README.md": "# target\n\nChanged while the agent works.\n",
}

// StuckModel's reviewer raises its finding as the scripted one does, and
// judges every fix of it "still": a review loop that gets stuck.
const StuckModel = "fake/stuck"

// StallModel's first reviewer of a task hangs inside an open tool call
// that never settles — OpenCode's task sub-agent, as on run 2 — while every
// later one reviews as the scripted one does: a Run to restart.
const StallModel = "fake/stall"

// StallCall is StallModel's open call: [title, kind, raw input as JSON].
var StallCall = [3]string{"task", "other", `{"description":"Review worker state behavior","prompt":"Read the worker and check its state"}`}

// SilentModel's first reviewer of a task starts its turn, plans and reads,
// then says and does nothing more, with no tool call open — a model call
// that never answers, as on run 3 — while every later one reviews as the
// scripted one does.
const SilentModel = "fake/silent"

// StillVerdict is StuckModel's reviewer's judgement of its earlier finding.
const StillVerdict = "```yaml\nverdicts:\n  F1: still\n```\n"

// AskModel's implementer asks a person first, with dude's ask_person tool,
// and does its work in the turn the answer starts.
const AskModel = "fake/ask"

// Question is what AskModel's implementer asks: ask_person's arguments.
const Question = `{"question":"Should FACTORY.md be in English?","choices":["yes","no"]}`

// ConductorQuestion is what AskModel's conductor asks, in its first turn.
const ConductorQuestion = `{"question":"Make it a follow-up task?","choices":["Yes","No"]}`

// Is says whether a model is the scripted agent rather than a real one.
func Is(model string) bool { return strings.HasPrefix(model, ModelPrefix) }

// FixedFile is what the fixer writes, and what tells the reviewer a fix
// has been made.
const FixedFile = "FIXED.md"

// Finding is the one blocking finding the reviewer raises, in the format
// the review prompt asks a model for.
const Finding = "---\n" +
	"severity: blocking\n" +
	"category: correctness\n" +
	"file: FACTORY.md\n" +
	"line: 1\n" +
	"title: FACTORY.md does not record the fix\n" +
	"description: The change is missing a record that the review was addressed.\n" +
	"suggested_fix: Add a file naming what was fixed.\n"

// Verdict is the reviewer's judgement of its earlier finding once the fix
// is in the tree, in the format the re-review prompt asks for.
const Verdict = "```yaml\nverdicts:\n  F1: fixed\n```\n"

// NothingToReview is the scripted reviewer's reply to work with no code.
const NothingToReview = "Read what was published; nothing to add."

// Step is what the agent does for one phase.
type Step struct {
	// Files to commit, path → one line of content; none changes nothing.
	Commit map[string]string
	// Commit message.
	Message string
	// What it replies.
	Reply string
	// Never finishes its turn.
	Hang bool
	// Its first turn asks a person this (ask_person's arguments) and ends.
	Ask string
	// Files it publishes for people (into $LUX_ARTIFACTS), name → one line.
	Publish map[string]string
	// dude tools it calls before replying, [tool, JSON arguments].
	Tools [][2]string
	// Files it writes into its checkout and does not commit, path →
	// content.
	Edits map[string]string
	// Files it saves for people while it works (into $LUX_ARTIFACTS),
	// before its first turn hangs: collected when its container stops.
	PublishNow map[string]string
	// Files it writes into its checkout in the turn it finishes, after
	// a Hang is woken: a change a person watching can see arrive.
	FinishEdits map[string]string
	// Its tool calls stay running: a long command (with Hang, never done).
	LongCommand bool
}

// Notes is what the implementer publishes: a short account of its work, as
// the prompt invites an agent to leave.
const Notes = "NOTES.md"

// Conductor is the phase label a task's conductor runs under (it has no
// phase: its role names it).
const Conductor = "conductor"

// Brainstorm is the label a session's agent runs under; scripted, it is
// played as the conductor is: each input's tool lines called, then a reply
// quoting the input.
const Brainstorm = "brainstorm"

// BrainstormMessage heads the first message in a session agent's briefing.
const BrainstormMessage = "## The first message\n\n"

// brainstormLead is how a session agent's briefing names its session
// (delivery.sessionBriefing): its title, quoted.
const brainstormLead = "Brainstorm, this is the session "

// ConductorReply is the scripted conductor's answer to one input: the line
// of its briefing that names the task, quoted, so a test sees the briefing
// arrived, and the input it answers, quoted back.
func ConductorReply(task, input string) string {
	said, _, _ := strings.Cut(strings.TrimSpace(input), "\n")
	return fmt.Sprintf("Briefed on %q. You asked: %q. Read-only: I changed nothing.", task, said)
}

// BrainstormReply is the scripted session agent's answer to one input: a
// session has no task, so it quotes the session's title from its briefing.
func BrainstormReply(title, input string) string {
	said, _, _ := strings.Cut(strings.TrimSpace(input), "\n")
	return fmt.Sprintf("In the session %q. You asked: %q. Read-only: I changed nothing.", title, said)
}

// ConductorScript is the scripted conductor's first turn, from the
// briefing dude wrote it: the reply to the message the briefing ends with.
// It is the Run's prompt in place of the briefing, as every scripted
// phase's script is (for a real lux, a lux-fake script). The tools the
// message names (ConductorCalls) are called first. A session agent's
// briefing names a session, not a task: its reply quotes the title.
func ConductorScript(briefing string) string {
	task := ""
	if _, after, ok := strings.Cut(briefing, "## The task\n\n"); ok {
		task, _, _ = strings.Cut(after, "\n")
	}
	title, session := sessionTitle(briefing)
	message := ""
	if i := strings.LastIndex(briefing, "'s message\n\n"); i >= 0 {
		message = briefing[i+len("'s message\n\n"):]
	} else if i := strings.LastIndex(briefing, BrainstormMessage); i >= 0 {
		message = briefing[i+len(BrainstormMessage):]
		message, _, _ = strings.Cut(message, "\n\n## How you work")
	}
	var b strings.Builder
	for _, c := range ConductorCalls(message) {
		if Local(c[0]) {
			b.WriteString(localScript(c[0], c[1]))
			continue
		}
		fmt.Fprintf(&b, "http dude POST /tools/%s %s\n", c[0], c[1])
	}
	if session {
		return b.String() + "echo " + BrainstormReply(title, message)
	}
	return b.String() + "echo " + ConductorReply(task, message)
}

// sessionTitle is the session's title a session agent's briefing opens
// with, and whether it is one.
func sessionTitle(briefing string) (string, bool) {
	rest, ok := strings.CutPrefix(briefing, brainstormLead)
	if !ok {
		return "", false
	}
	var title string
	if _, err := fmt.Sscanf(rest, "%q", &title); err != nil {
		return "", true
	}
	return title, true
}

// localScript is a local call as lux-fake's script says it: write and
// commit; git has no lux-fake equivalent and is left out.
func localScript(name, args string) string {
	var in struct {
		Path, Content, Message string
	}
	_ = json.Unmarshal([]byte(args), &in)
	switch name {
	case LocalWrite:
		return fmt.Sprintf("write %s %s\n", in.Path, strings.ReplaceAll(in.Content, "\n", " "))
	case LocalCommit:
		return "commit " + in.Message + "\n"
	case LocalArtifact:
		return fmt.Sprintf("write %s/%s %s\n", PublishedDir, in.Path, strings.ReplaceAll(in.Content, "\n", " "))
	}
	return ""
}

// ConductorCallPrefix starts a line of a person's message that the
// scripted conductor carries out: "tool: NAME {json}" calls dude's tool
// NAME with those arguments, as a real conductor would decide to — what
// lets a test drive the conductor's decisions through Chat.
const ConductorCallPrefix = "tool: "

// The scripted conductor's own work in its checkout, named as tools in a
// message (ConductorCalls) but done in the container rather than called
// on dude: write {"path","content"} writes a file in its first
// repository, commit {"message"} commits everything there, git
// {"args"} runs git there (the fake lux only), and artifact
// {"path","content"} publishes a file for people into $LUX_ARTIFACTS.
const (
	LocalWrite    = "write"
	LocalCommit   = "commit"
	LocalGit      = "git"
	LocalArtifact = "artifact"
)

// Local says whether a call is the scripted conductor's own work in its
// container rather than one of dude's tools.
func Local(name string) bool {
	return name == LocalWrite || name == LocalCommit || name == LocalGit || name == LocalArtifact
}

// ConductorCalls are the tool calls an input asks the scripted conductor
// for, in order: each line "tool: NAME {json}" ([name, JSON arguments]).
func ConductorCalls(input string) [][2]string {
	var out [][2]string
	for _, line := range strings.Split(input, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), ConductorCallPrefix)
		if !ok {
			continue
		}
		name, args, _ := strings.Cut(strings.TrimSpace(rest), " ")
		if args = strings.TrimSpace(args); args == "" {
			args = "{}"
		}
		if name != "" {
			out = append(out, [2]string{name, args})
		}
	}
	return out
}

// ConductorTurn is the scripted conductor's reply to a later input, from
// its script (ConductorScript): the same task or session, the new input.
func ConductorTurn(script, input string) string {
	first := script[strings.LastIndex(script, "echo ")+len("echo "):]
	var named string
	if _, err := fmt.Sscanf(first, "In the session %q.", &named); err == nil {
		return BrainstormReply(named, input)
	}
	if _, err := fmt.Sscanf(first, "Briefed on %q.", &named); err != nil {
		return first
	}
	return ConductorReply(named, input)
}

// For is the agent's step for a phase Run. fixed says whether the tree it
// starts from already has the fixer's file — which only the reviewer reads.
func For(phase, model, runID string, fixed bool) Step {
	if model == HangModel {
		return Step{Hang: true}
	}
	if model == CommandModel && phase == "implement" {
		return Step{Hang: true, LongCommand: true}
	}
	if model == LiveModel && phase == "implement" {
		return Step{Hang: true, Edits: LiveEdits,
			PublishNow: map[string]string{Notes: LiveNotes[0], "screenshot.png": LiveScreenshot,
				"coverage.html": `<!doctype html><title>Coverage</title><script>parent.document.title = "pwned"</script><p>87%</p>`},
			Publish:     map[string]string{Notes: LiveNotes[1]},
			FinishEdits: map[string]string{"DONE.md": "Finished while you watched.\n"},
			Commit:      map[string]string{"*:FACTORY.md": "Written by run " + runID}, Message: "Add FACTORY.md for " + runID,
			Reply: "Implemented it."}
	}
	switch phase {
	case "implement":
		// In every repository it may change: work across two is two commits.
		step := Step{Commit: map[string]string{"*:FACTORY.md": "Written by run " + runID},
			Message: "Add FACTORY.md for " + runID, Reply: "Implemented it.",
			Publish: map[string]string{Notes: "# What changed\n\nAdded FACTORY.md for " + runID + "."}}
		if model == AskModel {
			step.Ask = Question
		}
		if model == WaitModel {
			step.Tools = [][2]string{{"request_repository", `{"repository":"web","reason":"the client calls this API","wait":true}`}}
		}
		if model == RequestModel {
			step.Tools = [][2]string{{"request_repository", `{"repository":"web","reason":"the client calls this API"}`}}
			step.Hang = true
		}
		if model == ToolsModel {
			step.Tools = [][2]string{
				{"emit_event", `{"type":"progress","data":{"done":1,"of":2,"step":"writing FACTORY.md"}}`},
				{"emit_event", `{"type":"progress","data":{"done":2,"of":2,"step":"committing"}}`},
			}
		}
		return step
	case "fix":
		// The content names the Run, so a second fix is still a change.
		return Step{Commit: map[string]string{FixedFile: "addressed by " + runID},
			Message: "Address review findings for " + runID, Reply: "Addressed it."}
	case "simplify":
		return Step{Commit: map[string]string{"SIMPLE.md": "simplified by " + runID},
			Message: "Simplify " + runID, Reply: "Simplified."}
	case "review":
		if fixed && model == StuckModel {
			return Step{Reply: "The finding is still there.\n\n" + StillVerdict}
		}
		if fixed {
			// Shown the finding it raised, it judges the fix.
			return Step{Reply: "Reviewed the fix; no further problems.\n\n" + Verdict}
		}
		return Step{Reply: "One problem:\n\n```yaml\n" + Finding + "```\n"}
	case Conductor, Brainstorm:
		// Each turn's reply is ConductorReply, from its briefing and input.
		step := Step{Reply: "Read-only: I changed nothing."}
		if model == AskModel {
			step.Ask = ConductorQuestion
		}
		return step
	}
	return Step{Reply: "Nothing to do for " + phase + "."}
}

// Script compiles the agent's step to lux-fake's script language
// (lux/cmd/lux-fake), for a real lux.
//
// The reviewer decides inside the container, from the tree it checked out,
// as a real one would.
func Script(phase, model, runID string) string {
	if model == HangModel || model == CommandModel && phase == "implement" {
		return "sleep 3600"
	}
	if model == LiveModel && phase == "implement" {
		var b strings.Builder
		for _, path := range []string{"LIVE.md", "README.md"} {
			// lux-fake writes one line; the file's line breaks stay escaped.
			fmt.Fprintf(&b, "write %s %s\n", path, strings.ReplaceAll(LiveEdits[path], "\n", " "))
		}
		b.WriteString("sleep 3600")
		return b.String()
	}
	if phase == "review" {
		// Built in a file and read back, because separate reply lines would
		// run into one another; reported only when the fixer's file is not
		// in the tree this review checked out.
		var b strings.Builder
		for _, line := range strings.Split(strings.TrimSuffix(Finding, "\n"), "\n") {
			b.WriteString("append /tmp/review.yaml " + line + "\n")
		}
		b.WriteString("unless-exists " + FixedFile + " read /tmp/review.yaml\n")
		verdict := Verdict
		if model == StuckModel {
			verdict = StillVerdict
		}
		for _, line := range strings.Split(strings.TrimSuffix(verdict, "\n"), "\n") {
			b.WriteString("append /tmp/verdict.yaml " + line + "\n")
		}
		b.WriteString("if-exists " + FixedFile + " read /tmp/verdict.yaml\n")
		b.WriteString("echo reviewed")
		return b.String()
	}
	step := For(phase, model, runID, false)
	var b strings.Builder
	if model == ToolsModel && phase == "implement" {
		// Both ways dude's tools reach an agent on lux: MCP, and the local
		// service socket the dude CLI uses (no token in the container).
		b.WriteString("mcp-call dude list_tasks \n")
		b.WriteString(`http dude POST /tools/emit_event {"type":"progress","data":{"done":1,"of":1,"step":"through the socket"}}` + "\n")
	}
	for path, line := range step.Commit {
		// In the workdir: the one repository. (Work across several needs
		// lux-fake's `cd`, which is coming; the contract suite uses one.)
		path = strings.TrimPrefix(path, "*:")
		fmt.Fprintf(&b, "append %s %s\n", path, line)
	}
	for name, text := range step.Publish {
		// lux-fake writes one line; the Markdown's line breaks stay escaped.
		fmt.Fprintf(&b, "write %s/%s %s\n", PublishedDir, name, strings.ReplaceAll(text, "\n", " "))
	}
	if len(step.Commit) > 0 {
		b.WriteString("commit " + step.Message + "\n")
	}
	if step.Ask != "" {
		// Its work done first, as lux-fake runs one script per prompt: then
		// it asks, and ends its turn. The answer is its next prompt, which
		// lux-fake only echoes — enough to show the conversation resumed.
		b.WriteString("http dude POST /tools/ask_person " + step.Ask + "\n")
		b.WriteString("echo I asked; waiting for the answer.")
		return b.String()
	}
	b.WriteString("echo " + step.Reply)
	return b.String()
}

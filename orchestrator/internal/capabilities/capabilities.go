// Package capabilities is what an agent may use, in one place: each
// capability is a named grant of dude tools and harness-native tools, and
// each agent role has a default set of them (docs/design/agent-tools.md,
// Capabilities). Every consumer — the tool server's dispatch and listing,
// the instruction file, a Run's harness configuration — asks Resolve.
package capabilities

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
)

// Key names a capability: stable, as a settings UI and stored overrides
// will refer to it.
type Key string

const (
	TasksRead       Key = "tasks.read"
	TasksCreate     Key = "tasks.create"
	Memory          Key = "memory"
	RunBasics       Key = "run.basics"
	Ask             Key = "ask"
	Machines        Key = "machines"
	Coordination    Key = "coordination"
	FeedbackHandle  Key = "feedback.handle"
	FeedbackResolve Key = "feedback.resolve"
	PRAct           Key = "pr.act"
	BaseSync        Key = "base.sync"
	GitHubRead      Key = "github.read"
	DeliveryRead    Key = "delivery.read"
	Conduct         Key = "conduct"
	Session         Key = "session"
	SessionAgents   Key = "session.agents"
	WebSearch       Key = "web_search"
	WebFetch        Key = "web_fetch"
)

// The harness-native tools a capability can unlock: each harness turns
// them on or off in its own way (phases/spec.go).
const (
	NativeWebSearch = "web_search"
	NativeWebFetch  = "web_fetch"
)

// Capability is one grant: what a person reads in settings, and what it
// unlocks. A tool name is unlocked whichever definition answers it: a
// session agent's own (agenttools/brainstorm.go) or the task agents'.
type Capability struct {
	Key         Key
	Description string
	Tools       []string
	Native      []string
}

// All are the capabilities, in the order a settings page lists them.
var All = []Capability{
	{Key: TasksRead, Description: "List the project's tasks, epics and repositories.",
		Tools: []string{"list_tasks", "list_epics", "list_repositories"}},
	{Key: TasksCreate, Description: "Create tasks for a person to start.",
		Tools: []string{"create_task"}},
	{Key: Memory, Description: "Search the project's memory and remember things for later.",
		Tools: []string{"search_memory", "get_memory", "remember"}},
	{Key: RunBasics, Description: "Report progress, read what the task's Runs changed, and ask for another repository.",
		Tools: []string{"emit_event", "run_diff", "request_repository"}},
	{Key: Ask, Description: "Ask a person a question and wait for the answer.",
		Tools: []string{"ask_person"}},
	{Key: Machines, Description: "List machine sizes and move to a bigger machine.",
		Tools: []string{"list_machine_sizes", "request_machine"}},
	// Reviewers, the simplifier and QA read their peers, never write: a
	// reviewer that messages would be deciding.
	{Key: Coordination, Description: "Message peer tasks, wait for their replies, and pin notes on the epic.",
		Tools: []string{"message", "wait_for_reply", "note"}},
	{Key: FeedbackHandle, Description: "Read people's feedback on the task and reply to it.",
		Tools: []string{"feedback", "feedback_reply"}},
	{Key: FeedbackResolve, Description: "Resolve feedback with the commit that fixed it.",
		Tools: []string{"feedback_resolve"}},
	{Key: PRAct, Description: "Reply on the task's pull requests and resolve their review threads.",
		Tools: []string{"reply_on_pull_request", "resolve_thread"}},
	{Key: BaseSync, Description: "Bring the base branch into the checkout.",
		Tools: []string{"sync_base"}},
	{Key: GitHubRead, Description: "Search and read GitHub issues, pull requests and check logs.",
		Tools: []string{"github_search", "github_issue", "github_pull_request", "github_check_log"}},
	{Key: DeliveryRead, Description: "Read review findings and the task's pull requests.",
		Tools: []string{"findings", "pull_requests"}},
	{Key: Conduct, Description: "Drive the task's delivery: start phases and agents, steer and restart Runs, decide, update the task, publish, and act on its pull requests.",
		Tools: []string{"start_phase", "start_agent", "steer", "restart_run", "decide", "dismiss_finding",
			"decide_escalation", "update_task", "publish", "request_review", "mark_ready", "rerun_failed_checks"}},
	{Key: Session, Description: "Propose tasks and relations for a member to file, and name the session.",
		Tools: []string{"propose", "relate", "name_session"}},
	{Key: SessionAgents, Description: "Start read-only investigator agents to research the linked repositories.",
		Tools: []string{"start_agent"}},
	{Key: WebSearch, Description: "Search the web with the coding agent's own search tool.",
		Native: []string{NativeWebSearch}},
	{Key: WebFetch, Description: "Read web pages with the coding agent's own fetch tool.",
		Native: []string{NativeWebFetch}},
}

// Defaults are each agent role's capabilities when nothing overrides them.
// "" is any role not listed (a Run whose role is unknown).
var Defaults = map[string][]Key{
	"conductor": {TasksRead, TasksCreate, Memory, RunBasics, Ask, Machines, Coordination, FeedbackHandle, PRAct,
		BaseSync, GitHubRead, DeliveryRead, Conduct, WebSearch, WebFetch},
	"brainstorm": {TasksRead, Memory, Ask, Machines, GitHubRead, DeliveryRead, Session, SessionAgents, WebSearch, WebFetch},
	"implementer": {TasksRead, TasksCreate, Memory, RunBasics, Ask, Machines, Coordination, FeedbackHandle,
		FeedbackResolve, PRAct, BaseSync, WebSearch, WebFetch},
	"reviewer":     {TasksRead, Memory, RunBasics, Machines, WebSearch, WebFetch},
	"fixer":        {TasksRead, Memory, RunBasics, Machines, WebSearch, WebFetch},
	"simplifier":   {TasksRead, Memory, RunBasics, Machines, BaseSync, WebSearch, WebFetch},
	"qa_browser":   {TasksRead, Memory, RunBasics, Machines, WebSearch, WebFetch},
	"investigator": {TasksRead, TasksCreate, Memory, RunBasics, Ask, Machines, Coordination, WebSearch, WebFetch},
	"":             {TasksRead, Memory, RunBasics, WebSearch, WebFetch},
}

// Agent is who the capabilities are for: a Run's role, organisation, and
// its project and task Run, or its session.
type Agent struct {
	Role, Org, Project, Run, Session string
}

// Grants are the capabilities one agent has. The zero value grants nothing.
type Grants struct {
	keys []Key
}

// Resolve is the capabilities a has. Today its role's defaults; a stored
// per-organisation or per-agent override is applied here, in tx, and
// nowhere else.
func Resolve(ctx context.Context, tx pgx.Tx, a Agent) (Grants, error) {
	return Default(a.Role), nil
}

// Default is role's row of Defaults, or the "" row for a role it does not
// list. For callers with no agent to resolve (a settings page's built-in
// prompt) and for tests.
func Default(role string) Grants {
	keys, ok := Defaults[role]
	if !ok {
		keys = Defaults[""]
	}
	return Of(keys...)
}

// Of grants exactly keys.
func Of(keys ...Key) Grants {
	return Grants{keys: slices.Clone(keys)}
}

// Without is g less keys.
func (g Grants) Without(keys ...Key) Grants {
	return Grants{keys: slices.DeleteFunc(slices.Clone(g.keys), func(k Key) bool { return slices.Contains(keys, k) })}
}

func (g Grants) Has(k Key) bool { return slices.Contains(g.keys, k) }

// Tool says some granted capability unlocks the dude tool name.
func (g Grants) Tool(name string) bool {
	return g.unlocks(func(c Capability) []string { return c.Tools }, name)
}

// Native says some granted capability unlocks the harness-native tool name
// (NativeWebSearch, NativeWebFetch).
func (g Grants) Native(name string) bool {
	return g.unlocks(func(c Capability) []string { return c.Native }, name)
}

func (g Grants) unlocks(of func(Capability) []string, name string) bool {
	for _, c := range All {
		if g.Has(c.Key) && slices.Contains(of(c), name) {
			return true
		}
	}
	return false
}

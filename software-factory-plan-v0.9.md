# Software Factory — Architecture & Delivery Plan

**Status:** Draft v0.9  
**Date:** 2026-09-21  
**Primary stack:** TypeScript backend/control plane on Bun, TypeScript frontend, PostgreSQL, Go or Rust worker daemon, Python 3.14 tooling and black-box/E2E tests, containerized work runtimes  
**Agent runtime:** OpenCode, wrapped behind an internal driver interface

---

## 1. Executive summary

Build the software factory as two deliberately separate layers:

1. **A deterministic, durable control plane** that owns tasks, state transitions, policies, external integrations, waiting, retries, human intervention, cost accounting, event history, and metrics.
2. **An agent execution plane** that runs OpenCode sessions for the work that actually benefits from model reasoning: investigation, implementation, review, fixing, simplification, and exploratory QA.

The most important architectural rule is:

> **The LLM is a worker, not the workflow engine and not the source of truth.**

Long waits—human questions, CI, PR reviews, capacity, timers—must happen in the deterministic control plane without an agent running or consuming tokens. The orchestrator is only woken when there is genuinely new information that requires judgment.

The control plane should own a durable macro state machine. Within an active coding phase, one OpenCode **orchestrator** session owns the micro-plan and can spawn specialized subagents. This gives the desired agent autonomy without making the entire product dependent on a long-running prompt loop.

The design should be integration-driven from day one: Slack/Discord, Jira/Linear, GitHub or another SCM, worker provisioning, cost accounting, memory, artifact storage, and OpenCode should all sit behind explicit interfaces.

---

## 2. Goals

The system should:

- Accept work from Slack or Discord via an `@` mention or explicit command.
- Understand the request in the context of the company, team, repositories, and previous work.
- Ask clarifying questions when required.
- Require an explicit confirmation before work begins.
- Create a ticket in Jira or Linear once work is confirmed.
- Provision an isolated execution environment.
- Start an OpenCode orchestrator session.
- Allow that orchestrator to spawn specialist subagents.
- Support investigation, implementation, review, fixing, simplification, testing, browser/computer-use QA, and PR creation.
- Repeat review → fix until policy says there are no blocking findings.
- Repeat test → fix until required tests pass or the run is escalated.
- Open one or more PRs.
- React to PR comments/reviews via webhooks rather than polling.
- Repeat review-comment → fix → push → wait until the PR is approved and merge-ready.
- Let any agent ask a human a blocking or non-blocking question.
- Let humans pause, resume, steer, inspect, retry, or abort work at any point.
- Support multiple repositories within one task.
- Make all significant activity inspectable.
- Record an append-only event history sufficient to calculate product and engineering metrics.
- Track model cost by task, phase, agent, session, repository, and time.
- Minimize model calls, especially re-wakes of the supervising orchestrator.
- Make policies configurable by organization, team, repository, risk class, and task.
- Make runner provisioning pluggable: local/static nodes first, remote/dynamic capacity later.
- Make tools and agent roles pluggable.
- Maintain both searchable source context and curated long-term memory.

---

## 3. Non-goals for the first version

Do not initially try to:

- Fully replace CI/CD.
- Automatically merge every PR.
- Allow arbitrary production access.
- Build a bespoke container runtime.
- Build a full project-management product.
- Make agents responsible for durable scheduling or retry logic.
- Give agents unrestricted credentials.
- Persist “memory” only as opaque chat history.
- Depend on polling Slack, Linear/Jira, GitHub, or CI when webhooks/events exist.
- Re-run an LLM simply to determine whether an external state changed.

---

## 4. Core design principles

### 4.1 Deterministic control plane, probabilistic workers

The backend controls:

- workflow state;
- retries;
- timers;
- waiting;
- permissions;
- policy checks;
- budgets;
- queueing;
- capacity;
- integration calls;
- lifecycle;
- audit events.

Agents control:

- reasoning;
- planning;
- code investigation;
- code changes;
- review judgment;
- fixing;
- simplification;
- exploratory QA.

An agent can **request** a state-changing action, but the control plane decides whether it is allowed.

### 4.2 Events first

Every meaningful action creates a structured event.

Events are append-only. Current task state can be represented in normal relational tables/materialized views, but the event ledger remains the authoritative audit trail of *what happened and when*.

This is what makes metrics, debugging, replay, incident analysis, and session inspection possible.

### 4.3 No token spend for deterministic work

Prefer code, webhooks, static tools, and stored state for:

- waiting;
- detecting CI completion;
- detecting PR review changes;
- checking approval state;
- policy decisions;
- task status rendering;
- cost aggregation;
- ticket creation from a known structured task;
- branch creation;
- diff statistics;
- linting;
- formatting;
- type checking;
- test execution;
- static analysis;
- dependency caching;
- routing questions;
- computing durations and metrics;
- retry/backoff;
- worker health checks.

### 4.4 Agents are disposable; state is durable

A failed process or worker should not destroy the task's history.

Important state belongs in the control plane or durable artifact storage, not only in an agent process.

### 4.5 Human interaction is a first-class workflow primitive

Questions, approvals, steering, pause/resume, and abort are not chat hacks. They are explicit domain objects and workflow signals.

### 4.6 Security policy must not rely on the model obeying text

The real permission boundary is code:

- credentials;
- filesystem mounts;
- network policy;
- worker isolation;
- allowed tools;
- SCM permissions;
- policy engine;
- approval gates.

Agent prompts are an additional instruction layer, not the security boundary.

---

## 5. High-level architecture

```text
                  ┌──────────────────────┐
                  │  Slack / Discord     │
                  └──────────┬───────────┘
                             │ events / replies
                    ┌────────▼─────────┐
                    │ Chat Adapters     │
                    └────────┬─────────┘
                             │ normalized commands
┌────────────────────────────▼─────────────────────────────┐
│                    CONTROL PLANE                         │
│                                                        │
│  API / Auth / Task Service                             │
│  Durable Workflow Engine                              │
│  Policy Engine                                         │
│  Context + Memory Service                              │
│  Question / Human Interaction Service                  │
│  Event Ledger + Metrics                                │
│  Cost Accounting                                       │
│  Integration Adapters                                  │
│  Worker Scheduler / Provisioner                        │
│                                                        │
└──────┬──────────────┬──────────────┬──────────────┬─────┘
       │              │              │              │
       │              │              │              │
  ┌────▼─────┐   ┌────▼────┐   ┌────▼─────┐  ┌────▼─────┐
  │PostgreSQL│   │Jira/     │   │Git/PR/CI │  │Object    │
  │+ search  │   │Linear    │   │provider  │  │storage   │
  └──────────┘   └─────────┘   └──────────┘  └──────────┘
       │
       │ assignment
       ▼
┌─────────────────────────────────────────────────────────┐
│                  EXECUTION PLANE                        │
│                                                        │
│  Worker Node                                           │
│   └─ Task Runtime                                      │
│       ├─ N repositories / worktrees                    │
│       ├─ OpenCode orchestrator                         │
│       ├─ OpenCode subagents                            │
│       ├─ build/test services                           │
│       ├─ browser / computer-use QA                     │
│       └─ artifact capture                              │
│                                                        │
└─────────────────────────────────────────────────────────┘
```

---

## 6. Durable workflow model

### 6.1 Macro state machine

Suggested task states:

```text
RECEIVED
  ↓
INTAKE
  ↓
CLARIFYING ───────────────┐
  ↓                      │ human answer
AWAITING_CONFIRMATION ◄───┘
  ↓ confirmed
TICKET_CREATING
  ↓
QUEUED
  ↓
PROVISIONING
  ↓
INVESTIGATING
  ↓
IMPLEMENTING
  ↓
REVIEWING ────────────────┐
  │ blocking findings     │
  └──► FIXING ────────────┘
  ↓ clean enough by policy
SIMPLIFYING
  ↓
TESTING ──────────────────┐
  │ failure               │
  └──► TEST_FIXING ───────┘
  ↓ required tests pass
PR_OPENING
  ↓
AWAITING_CI_AND_REVIEW ────────────────────────┐
  │ actionable review / failed CI              │
  └──► PR_FIXING ─► PUSHING ───────────────────┘
  ↓ approved + required checks satisfied
READY_TO_MERGE
  ↓ merge observed / manually completed
DONE
```

Cross-cutting states:

```text
WAITING_FOR_HUMAN
PAUSING
PAUSED
ABORTING
ABORTED
FAILED
ESCALATED
```

Do not encode every subagent operation as a new task state. The macro state should remain understandable to humans.

### 6.2 Macro orchestration vs. micro orchestration

The **control plane** owns macro orchestration:

- when a phase starts;
- when a phase is complete;
- when to wait;
- when to retry;
- when to stop;
- whether policy permits the next step.

The **OpenCode orchestrator** owns micro orchestration during an active phase:

- what to inspect;
- which specialist subagents to spawn;
- implementation sequence;
- resolving review findings;
- deciding what code change addresses a test failure.

This separation substantially reduces orchestrator wakeups.

### 6.3 Durable workflow engine

Recommended approach: use a durable workflow runtime such as **Temporal** for task workflows and signals, while keeping PostgreSQL as the application/domain datastore and append-only event ledger.

Why it fits:

- tasks may run for hours or days;
- workflows spend much of that time waiting;
- human answers arrive asynchronously;
- PR/CI events arrive asynchronously;
- pause/resume/abort are signals;
- retries and timeouts matter;
- worker failures must not lose workflow progress.

If introducing Temporal is undesirable, define a `WorkflowRuntime` interface and initially implement it using PostgreSQL with:

- durable workflow rows;
- transactional outbox;
- `FOR UPDATE SKIP LOCKED` job leasing;
- durable timers;
- idempotency keys;
- explicit signal inboxes;
- retry/dead-letter state.

Do not let the rest of the application depend directly on Temporal APIs. That preserves the option to change runtime later.

---

## 7. Intake from Slack and Discord

### 7.1 Chat adapter interface

```python
class ChatAdapter(Protocol):
    async def post_message(...)
    async def post_question(...)
    async def update_message(...)
    async def add_reaction(...)
    async def resolve_identity(...)
    async def get_thread_context(...)
```

Implement:

- `SlackChatAdapter`
- `DiscordChatAdapter`

Normalize inbound platform events into internal events such as:

```json
{
  "type": "chat.work_requested",
  "platform": "slack",
  "workspace_id": "...",
  "channel_id": "...",
  "thread_id": "...",
  "message_id": "...",
  "actor_id": "...",
  "text": "...",
  "attachments": []
}
```

### 7.2 Intake behavior

When mentioned:

1. Persist the raw inbound event.
2. Resolve the human identity and organization/team.
3. Resolve channel defaults, likely repositories, and policy.
4. Fetch only the context needed for intake.
5. Produce a structured `TaskSpec`.
6. Detect missing or ambiguous required fields.
7. Ask clarifying questions if needed.
8. Post a concise proposed scope.
9. Require explicit `Confirm / Edit / Cancel`.
10. Only after confirmation:
    - freeze `TaskSpec v1`;
    - create the Jira/Linear ticket;
    - start the work workflow.

### 7.3 TaskSpec

Example:

```yaml
task_id: task_...
title: Add export option to invoices
goal: >
  Add CSV export for the invoice list.
acceptance_criteria:
  - Export respects active filters.
  - Result is UTF-8 CSV with documented columns.
  - UI provides visible success/error feedback.
repositories:
  - repo: web
    role: frontend
    base_ref: main
  - repo: api
    role: backend
    base_ref: main
constraints:
  - No schema migration.
non_goals:
  - Scheduled exports.
requested_by: user_...
source_conversation: conv_...
policy_profile: standard-product-change
```

### 7.4 Avoid wasting a model call at intake

Run deterministic inference first:

- channel → default team;
- channel → likely repos;
- URLs → repository/ticket references;
- commands/options → priority;
- known user → identity;
- existing thread → current task.

Only invoke an intake model when natural-language interpretation is actually needed.

---

## 8. Human questions and agent-to-human communication

### 8.1 Do not block a tool call for hours

Every agent gets an internal tool:

```text
ask_user(
    question,
    options?,
    reason,
    blocking=true,
    target="requester" | user_id | role,
    context?
)
```

The tool should **not** hold open a synchronous RPC while waiting for the human.

Instead:

1. Create a durable `Question` row.
2. Emit `question.asked`.
3. Post the question into the task's Slack/Discord thread.
4. Mark the relevant agent run as `WAITING_FOR_HUMAN`.
5. Return a structured `question_pending` result to the agent.
6. End/suspend the current model turn.
7. The durable workflow waits without token usage.
8. On human reply:
   - validate authorization;
   - persist `question.answered`;
   - compute wait duration;
   - resume the relevant phase/session with the answer and a small delta context.

This can wrap or replace OpenCode's native `question` tool so the user experience is externalized into Slack/Discord.

### 8.2 Question data model

```text
Question
- id
- task_id
- agent_session_id
- asked_by_agent
- target_user_or_role
- question
- options[]
- blocking
- status
- asked_at
- answered_at
- answer
- conversation_ref
- expires_at
```

### 8.3 Human commands

From Slack/Discord, CLI, or web UI:

```text
status
pause
resume
abort
steer <instruction>
retry <phase/run>
answer <question>
show cost
show findings
show PRs
show sessions
```

Platform-specific buttons are useful, but all actions should map to the same backend commands.

---

## 9. OpenCode integration

### 9.1 Put OpenCode behind a driver

Do not allow core domain code to depend directly on OpenCode.

```python
class AgentRuntime(Protocol):
    async def create_session(...)
    async def prompt(...)
    async def abort(...)
    async def list_children(...)
    async def get_messages(...)
    async def get_diff(...)
    async def subscribe_events(...)
```

Implementation:

```text
OpenCodeAgentRuntime
```

Because the primary backend is Python, there are two reasonable implementation choices:

1. Launch/connect to the OpenCode server and use its HTTP API from Python.
2. Run a tiny TypeScript sidecar/bridge that uses the official OpenCode SDK and expose a narrow internal RPC API.

Start with whichever is easier to keep stable, but preserve the `AgentRuntime` boundary.

### 9.2 One primary orchestrator per task

The task gets one durable logical orchestrator identity:

```text
task -> orchestrator session -> child/subagent sessions
```

Do not create a new orchestrator for every tiny event.

### 9.3 Suggested agent roles

Start with:

- `investigator`
- `implementer`
- `reviewer`
- `security_reviewer`
- `simplifier`
- `test_planner`
- `qa_browser`
- `pr_fixer`

Not every task uses every role.

### 9.4 Agent permissions

Examples:

**Reviewer**

- repository read: allow;
- diff read: allow;
- tests/static tools: allow;
- edits: deny;
- git push: deny;
- secrets: deny.

**Implementer**

- repository read/write: allow;
- tests: allow;
- branch-local git operations: allow;
- push: gated by policy/control plane;
- production/network/secrets: deny unless explicitly granted.

**QA browser**

- browser tools: allow;
- source edit: normally deny;
- test credentials: scoped only to QA environment;
- network: allowlist.

### 9.5 Agent outputs must be structured

For important phase boundaries, require machine-readable output, for example:

```json
{
  "status": "complete",
  "summary": "...",
  "findings": [],
  "changed_repositories": ["web", "api"],
  "recommended_next_action": "review"
}
```

Do not parse critical state transitions out of arbitrary prose if a schema can be used.

---

## 10. Minimizing orchestrator wakeups

This is a first-class product requirement.

### 10.1 Wake only on semantic deltas

Do **not** wake the orchestrator to ask:

- Is CI done yet?
- Is the PR approved yet?
- Did someone reply yet?
- Is a worker available yet?
- What is the current cost?
- Did a timer expire?

The control plane can answer all of those.

Wake it when:

- a human answer contains new requirements;
- a blocking review comment arrives;
- CI fails with information that requires code judgment;
- a meaningful steering instruction arrives;
- the workflow enters a model-reasoning phase.

### 10.2 Resume with a compact delta

Maintain a structured `TaskSnapshot`.

When the orchestrator is resumed, provide:

- current `TaskSpec`;
- current policy;
- current phase;
- unresolved findings;
- relevant test/CI failures;
- human answers since last wake;
- PR review comments since last wake;
- changed repository heads;
- only relevant retrieved memories.

Avoid replaying the entire conversation.

### 10.3 Coalesce events

For PR reviews/comments:

- receive every event immediately;
- persist every event;
- debounce model wakeup for a short configurable interval;
- batch comments that arrived together.

Approval or resolved-thread events often require no LLM call at all.

### 10.4 Use deterministic pre-processing

Before sending a failure to an agent, collect:

- exact failing tests;
- relevant logs;
- diff;
- changed files;
- static-analysis output;
- service health;
- screenshots/traces if present.

The model should reason over a focused failure packet instead of spending tokens discovering deterministic facts.

---

## 11. Review → fix → simplify loop

### 11.1 Review phase

The orchestrator launches one or more read-only reviewer subagents based on policy.

Potential reviewers:

- general correctness;
- security;
- performance;
- frontend/UI;
- database/migrations;
- API compatibility.

Each finding is structured:

```yaml
finding_id: finding_...
severity: blocking | high | medium | low | note
category: correctness
repo: api
file: src/...
line: 123
description: ...
evidence: ...
suggested_fix: ...
status: open
```

### 11.2 Policy decides what blocks

Example:

```yaml
review:
  blocking_severities:
    - blocking
    - high
  required_reviewers:
    - correctness
  conditional_reviewers:
    security:
      when_paths_match:
        - "auth/**"
        - "payments/**"
    database:
      when_paths_match:
        - "migrations/**"
  max_iterations: 5
```

The reviewer does not decide whether the entire task may progress. It reports findings. The policy engine applies the rule.

### 11.3 Fix loop

If blocking findings remain:

1. send only unresolved findings + diff to fixer/implementer;
2. make changes;
3. run deterministic targeted checks;
4. re-review relevant deltas;
5. mark findings resolved, superseded, or still open;
6. stop at policy limit and escalate if the loop is not converging.

### 11.4 Simplifier

Run only after blocking review findings are clear.

The simplifier should be constrained to:

- remove needless complexity;
- improve names/structure;
- remove dead code introduced by the change;
- consolidate obvious duplication;
- preserve behavior;
- avoid widening scope.

After simplification, rerun deterministic checks and optionally a lightweight review of the simplifier diff.

---

## 12. Testing, computer use, and video evidence

### 12.1 Layered QA

Use the cheapest and most deterministic layer first:

1. formatting;
2. lint/static analysis;
3. type checks;
4. unit tests;
5. integration tests;
6. existing E2E tests;
7. deterministic Playwright scenarios;
8. model-driven exploratory browser/computer use;
9. full desktop interaction only when required.

Computer use should not replace a normal E2E test suite.

### 12.2 Browser QA service

Provide a `BrowserQA` capability as a task-local service:

```text
qa.open(url)
qa.click(...)
qa.type(...)
qa.screenshot(...)
qa.read_console()
qa.read_network_failures()
qa.start_recording()
qa.stop_recording()
qa.attach_artifact(...)
```

Implementation can use Playwright beneath the tool surface.

The agent does not need direct access to browser credentials. The QA service can inject task-scoped auth.

### 12.3 Recording videos

For web UI flows, use Playwright video recording.

Policy examples:

```yaml
qa:
  browser_required_for:
    - frontend-change
  video:
    mode: ui_changes_only
    keep_on_success: true
    keep_on_failure: true
```

Suggested evidence bundle:

```text
qa/
  scenario.json
  screenshots/
  video.webm
  playwright-trace.zip
  console.log
  network-errors.json
  test-report.json
```

For native desktop workflows, use a virtual display plus screen recording, but treat that as a separate capability from browser QA because isolation and OS automation are more complex.

### 12.4 “Computer use” agent

The computer-use agent should receive:

- a clear scenario;
- expected outcomes;
- allowed domains/apps;
- bounded credentials;
- a maximum action count;
- a maximum time/cost budget.

It should produce:

- pass/fail;
- step log;
- screenshots at important points;
- video when policy requests it;
- discovered defects as structured findings.

### 12.5 Avoid recording secrets

Mask or avoid:

- passwords;
- API keys;
- secret manager screens;
- customer PII;
- production data.

Artifacts need their own retention and access-control policy.

---

## 13. Pull request lifecycle

### 13.1 SCM adapter

```python
class ScmAdapter(Protocol):
    async def create_branch(...)
    async def push(...)
    async def open_pull_request(...)
    async def get_pull_request(...)
    async def list_reviews(...)
    async def list_review_threads(...)
    async def get_checks(...)
    async def comment(...)
```

Implement GitHub first if that is your primary SCM, but keep the abstraction.

### 13.2 PR opening

The control plane should open the PR after the required pre-PR policy gates succeed.

PR body can be rendered from structured data without a fresh model call:

- task summary;
- ticket link;
- acceptance criteria;
- repositories changed;
- test results;
- review summary;
- QA evidence;
- video link when present;
- known limitations.

### 13.3 PR waiting must be webhook-driven

Subscribe to events for:

- PR reviews;
- review comments;
- review threads;
- issue/PR comments;
- CI/check status;
- branch pushes;
- merge/close.

Do not wake the orchestrator just because a webhook arrived.

First classify deterministically:

```text
approval -> update state
resolved thread -> update state
CI success -> update state
CI failure -> possibly wake fixer
new actionable review text -> batch, then wake
non-actionable bot/status comment -> store only
```

### 13.4 Review loop

```text
WAITING_FOR_REVIEW
  ↓ review comments / changes requested
COLLECT_REVIEW_DELTA
  ↓
WAKE_ORCHESTRATOR
  ↓
FIX
  ↓
RUN_REQUIRED_CHECKS
  ↓
PUSH
  ↓
WAITING_FOR_REVIEW
```

Store a cursor/high-water mark so a resumed agent only sees new review information.

### 13.5 Ready to merge

`READY_TO_MERGE` should be deterministic from policy, for example:

```text
required CI green
AND required approvals satisfied
AND zero unresolved blocking review findings
AND branch is current enough according to policy
AND cost/security gates satisfied
```

Whether the factory itself merges should be a separate policy option.

---

## 14. Multiple repositories per task

Make multi-repository work a native concept, not a later hack.

### 14.1 TaskRepo

```text
TaskRepo
- task_id
- repo_id
- role
- base_ref
- branch_name
- workspace_path
- starting_sha
- current_sha
- pull_request_id
- status
```

### 14.2 Workspace

Example:

```text
/workspace/task_123/
  repos/
    web/
    api/
    shared-schema/
  artifacts/
  runtime/
  task-manifest.json
```

### 14.3 Cross-repo behavior

Support:

- shared task branch naming;
- dependency ordering;
- local path overrides;
- Docker Compose/task environment spanning repos;
- separate commits and PRs per repo;
- one ticket linked to all PRs;
- cross-repo acceptance tests.

Policy can decide whether all PRs must be ready before the task becomes `READY_TO_MERGE`.

### 14.4 Repository metadata

Keep deterministic repository metadata to save agent discovery time:

```yaml
repo:
  name: api
commands:
  setup: uv sync
  lint: ruff check .
  typecheck: pyright
  unit_test: pytest tests/unit
  integration_test: pytest tests/integration
  dev: uv run app
services:
  - postgres
healthcheck:
  url: http://localhost:8000/health
policy_profile: backend-standard
```

Agents should not have to rediscover standard commands on every task.

---

## 15. Context and memory

Treat **source context** and **curated memory** as different systems.

### 15.1 Source context

Authoritative material:

- engineering handbook;
- architecture docs;
- ADRs;
- repository docs;
- `AGENTS.md`;
- API docs;
- runbooks;
- product rules;
- security standards;
- team conventions.

Index it with:

- PostgreSQL full-text search;
- optional `pgvector` semantic index;
- metadata filters;
- source hashes and timestamps.

Prefer lexical/structured retrieval first. Add semantic retrieval when it actually improves results.

### 15.2 Curated memory

Memory is learned operational knowledge, for example:

- “This service requires X before Y.”
- “The billing sandbox uses this startup sequence.”
- “Reviewer feedback repeatedly requires this pattern.”
- “These two repositories normally change together.”

A memory record should have:

```text
Memory
- id
- scope: company | team | repo | task-type | user
- key
- content
- provenance[]
- created_at
- last_verified_at
- confidence
- status: candidate | active | stale | rejected
- sensitivity
- expires_at?
```

### 15.3 Memory curation

Do not let agents silently rewrite organizational truth.

Use:

```text
raw events -> memory candidates -> validation/dedup -> active memory
```

Sources can be:

- structured task outcomes;
- repeated human corrections;
- explicit `remember this` actions;
- accepted agent proposals;
- periodically curated completion summaries.

To save tokens, generate candidate memories from structured events where possible. Only use an LLM for ambiguous consolidation/deduplication.

### 15.4 Provenance is mandatory

Every learned memory should point back to:

- task;
- PR;
- human answer;
- document;
- event;
- repository revision.

This makes bad memory debuggable.

---

## 16. Policy engine

### 16.1 Layering

Suggested precedence:

```text
organization default
  < team policy
  < repository policy
  < task-class policy
  < task-specific approved override
```

### 16.2 Policy examples

```yaml
version: 1

agent:
  max_orchestrator_wakes: 12
  max_subagents: 20

cost:
  soft_limit_usd: 25
  hard_limit_usd: 60
  on_soft_limit: ask_human
  on_hard_limit: pause

review:
  blocking_severities: [blocking, high]
  max_iterations: 5
  required_reviewers: [correctness]

testing:
  require:
    - lint
    - unit
  browser_qa_when:
    - frontend_changed

pr:
  allow_open: true
  allow_force_push: false
  allow_auto_merge: false
  required_approvals: 1

security:
  network_mode: allowlist
  allow_secret_scopes:
    - qa_readonly
  deny_commands:
    - "git push --force*"

paths:
  "migrations/**":
    require_human_approval: true
    extra_reviewers: [database]
  "auth/**":
    extra_reviewers: [security]
```

### 16.3 Policy evaluation must be explainable

CLI/UI:

```text
factory policy explain TASK-123 action=push
```

should show:

```text
DENIED
- repo policy forbids force push
- matched rule: pr.allow_force_push=false
```

No model needed.

---

## 17. Runner architecture

### 17.1 Yes, Docker-in-Docker is possible—but use it carefully

There are three common shapes:

#### A. Host Docker socket mounted into the task container

```text
task container -> /var/run/docker.sock -> host daemon
```

**Do not use this for autonomous agents.**

Access to the host Docker daemon is effectively host-level control. It destroys the isolation boundary.

#### B. Docker-in-Docker inside the task container

```text
worker
  └─ task container
      └─ dockerd
          └─ build/test containers
```

This works, including rootless variants, but standard DinD setups still require significant privilege. Treat it as a convenience mechanism, not a hostile-code sandbox.

#### C. Recommended trust boundary: disposable worker VM/node

```text
control plane
  └─ ephemeral/static worker VM
       ├─ agent runtime
       └─ worker-local container daemon
            ├─ app
            ├─ DB
            ├─ browser
            └─ test services
```

The VM/node is the hard boundary. Inside it, the agent can have broad freedom without exposing the control plane or other tenants.

This is the preferred long-term shape.

### 17.2 Start with static workers

Define a worker daemon:

```text
factory-runner
```

It:

- registers with the control plane;
- reports capabilities;
- heartbeats;
- accepts leases;
- creates task workspaces;
- starts/stops OpenCode;
- starts nested task services;
- streams logs/events;
- uploads artifacts;
- cleans up.

Example capability record:

```json
{
  "worker_id": "runner-01",
  "labels": ["linux", "x86_64", "browser"],
  "cpu": 16,
  "memory_gb": 64,
  "disk_gb_free": 400,
  "supports_nested_containers": true,
  "supports_browser": true
}
```

### 17.3 Provisioner interface

```python
class WorkerProvisioner(Protocol):
    async def acquire(requirements) -> WorkerLease: ...
    async def release(lease) -> None: ...
    async def suspend(lease) -> None: ...
    async def resume(lease) -> None: ...
```

Implementations can later include:

- `StaticPoolProvisioner`
- `KubernetesProvisioner`
- `AwsEc2Provisioner`
- `HetznerProvisioner`
- `NomadProvisioner`
- other internal compute backends

The scheduler should know capabilities, not provider-specific APIs.

### 17.4 Waiting and worker economics

Initially, keep the task runtime alive while waiting for short human/PR events; it consumes no model tokens.

Later, support:

```text
checkpoint -> destroy/suspend worker -> reacquire -> restore
```

for long waits or expensive dynamic workers.

Do not make this a v1 requirement unless capacity is tight.

---

## 18. Event model and observability

### 18.1 Event envelope

```json
{
  "event_id": "evt_...",
  "event_type": "agent.session.started",
  "occurred_at": "2026-09-21T20:00:00Z",
  "organization_id": "org_...",
  "task_id": "task_...",
  "workflow_id": "wf_...",
  "run_id": "run_...",
  "actor": {
    "type": "system|human|agent|integration",
    "id": "..."
  },
  "source": "control-plane|runner|github|slack|...",
  "correlation_id": "...",
  "causation_id": "...",
  "payload": {}
}
```

### 18.2 Useful event types

```text
task.requested
task.spec_created
task.confirmed
task.paused
task.resumed
task.steered
task.aborted

ticket.created

workflow.transitioned

worker.requested
worker.leased
worker.released

repo.cloned
repo.branch_created
git.commit_created
git.push_completed

agent.session.started
agent.session.stopped
agent.subagent.started
agent.subagent.stopped
agent.tool.called
agent.tool.completed
agent.wake.started
agent.wake.completed

question.asked
question.answered

review.started
review.finding.created
review.finding.resolved
review.completed

test.started
test.completed

qa.scenario.started
qa.scenario.completed
artifact.created

pr.opened
pr.review.received
pr.comment.received
pr.approved
pr.check.completed
pr.ready_to_merge
pr.merged

cost.sampled
budget.soft_limit_reached
budget.hard_limit_reached
```

### 18.3 Persist OpenCode events

The OpenCode live event stream is useful for inspection, but the factory should persist normalized copies into its own event system.

This provides:

- replay;
- durable inspection;
- correlations across integrations;
- stable analytics even if OpenCode event schemas change.

### 18.4 Metrics derivable from events

Per task:

- wall-clock lead time;
- time to first agent action;
- active agent time;
- human wait time;
- worker queue/provision time;
- implementation duration;
- review duration;
- QA duration;
- time waiting for PR reviewers;
- number of orchestrator wakes;
- number of subagents;
- number of human questions;
- number of steering interventions;
- review/fix iterations;
- test/fix iterations;
- PR review/fix iterations;
- commits and pushes;
- cost by phase;
- cost by agent;
- cost by repository;
- cost per completed task;
- failure/escalation/abort reason.

At team level:

- median and percentile lead times;
- cost distributions;
- human-touch rate;
- regression/failure rate;
- common policy blockers;
- repeated review findings;
- memory reuse effectiveness;
- runner utilization.

---

## 19. Cost accounting

You already have a proxy that can return token/model cost by session ID.

Define:

```python
class CostProvider(Protocol):
    async def get_session_cost(session_id: str) -> SessionCost: ...
```

### 19.1 Cost strategy

Avoid querying on every UI refresh.

Query:

- after each agent session/wake ends;
- before entering another expensive retry loop;
- when approaching a configured budget;
- on explicit user request;
- periodically with a deterministic timer only if hard in-flight enforcement is required.

Persist:

```text
CostSample
- task_id
- agent_session_id
- provider_session_id
- sampled_at
- input_tokens
- output_tokens
- cached_tokens
- cost_usd
- model_breakdown
```

### 19.2 Important accounting question

Determine whether a parent OpenCode session's proxy total includes child/subagent sessions.

If yes, do not sum parent + child totals.

If no, aggregate the complete session tree.

Make the provider adapter responsible for returning non-double-counted task totals.

### 19.3 Budget policy

```yaml
cost:
  soft_limit_usd: 20
  hard_limit_usd: 50
  soft_action: ask_human
  hard_action: pause
```

Cost checks themselves spend no model tokens.

---

## 20. PostgreSQL data model

Suggested starting tables:

```text
organizations
projects
epics
work_items
work_item_links
runs

organizations
teams
users
external_identities

repositories
repository_integrations
repository_policies
repository_commands

tasks
task_specs
task_repositories
task_state_history

conversations
conversation_messages

questions
question_answers

workflows
workflow_runs
phase_runs

workers
worker_capabilities
worker_leases

agent_sessions
agent_session_edges
agent_wakes

tickets

pull_requests
pull_request_reviews
pull_request_threads

review_findings

test_runs
qa_runs
artifacts

events

cost_samples

context_sources
context_documents
context_chunks

memories
memory_provenance
memory_candidates

integration_installations
webhook_deliveries

policy_sets
policy_evaluations
```

### 20.1 Use JSONB selectively

Good JSONB candidates:

- raw webhook payloads;
- agent structured output;
- policy evaluation details;
- event payloads;
- provider-specific metadata.

Do not hide frequently queried domain fields only inside JSONB.

---

## 21. Backend services

Use a **TypeScript modular monolith running on Bun** for the initial control plane.

Suggested layout:

```text
factory/
  src/
    api/
    auth/
    projects/
    epics/
    work-items/
    runs/
    sessions/
    workflow/
    events/
    policies/
    context/
    memory/
    questions/
    costs/
    artifacts/
    workers/
    harnesses/
    steps/
    integrations/
      chat/
        slack/
      tickets/
        jira/
      scm/
        github/
    metrics/
```

Suggested technology choices:

- Bun runtime and package manager;
- TypeScript throughout the backend;
- `Bun.serve()` directly or a small Bun-compatible HTTP framework;
- Bun's PostgreSQL client or a thin query layer on top of PostgreSQL;
- PostgreSQL full-text search;
- optional pgvector;
- S3-compatible artifact storage;
- OpenTelemetry-compatible tracing/log correlation;
- Server-Sent Events or WebSockets for live UI updates;
- Python + pytest + Playwright/API clients for black-box E2E/system tests.

### Durable workflow runtime under Bun

There is an important runtime constraint: the Temporal TypeScript **client** may run under Bun, but Temporal currently does not officially recommend running TypeScript Workflow/Activity workers under Bun because the worker implementation relies on Node-specific runtime facilities.

Therefore v1 should choose one of:

#### Option A — preferred if Bun-only backend processes are a hard requirement

Implement `WorkflowRuntime` using PostgreSQL:

- workflow state rows;
- transactional outbox;
- signal inbox;
- durable timers;
- leased jobs using `FOR UPDATE SKIP LOCKED`;
- retries/backoff;
- idempotency keys;
- dead-letter/escalation state.

Keep this behind `WorkflowRuntime` so Temporal or another durable engine can replace it later.

#### Option B — preferred if one Node service is acceptable

Run:

```text
Bun control-plane services
        │
        ▼
Temporal client
        │
        ▼
small Node.js Temporal worker service
```

The rest of the application remains Bun. The Node process exists only because Temporal's worker runtime has a stricter compatibility requirement.

Do not run Temporal workers under Bun merely because they happen to work in a particular release unless that configuration becomes officially supported and is covered by our own compatibility tests.

### Why Bun fits well elsewhere

Bun provides native HTTP/WebSocket functionality and a built-in SQL client with PostgreSQL support, which makes it a good fit for the API, webhook handlers, event ingestion, live UI transport, integrations, and most background jobs.

Avoid adding Redis until there is a concrete need. PostgreSQL should initially provide persistence, job coordination, event storage, search, and advisory/row-level locking.
## 22. Frontend

TypeScript frontend focused on control and observability, not chat.

Primary screens:

### Task list

- status;
- age;
- requester;
- repositories;
- cost;
- current phase;
- blocking reason.

### Task detail

- task specification;
- ticket;
- PRs;
- current policy;
- current worker;
- current phase;
- unresolved questions;
- review findings;
- tests;
- QA artifacts;
- cost breakdown.

### Timeline

A chronological event stream with filters:

```text
human
workflow
agent
tool
git
review
test
QA
PR
cost
```

### Session tree

```text
orchestrator
├── investigator
├── implementer
├── reviewer
│   └── security reviewer
├── simplifier
└── QA
```

For every session:

- current status;
- model;
- cost;
- start/end time;
- messages;
- tool calls;
- child sessions;
- diff;
- last activity.

### Intervention controls

- Pause
- Resume
- Steer
- Abort
- Retry phase
- Answer question
- Approve policy exception

Use Server-Sent Events or WebSockets for live updates, backed by persisted events.

---

## 23. CLI and API

### 23.1 CLI

Example:

```bash
factory task start \
  --repo web \
  --repo api \
  --title "Add CSV export" \
  --description-file spec.md

factory task list
factory task status TASK-123
factory task watch TASK-123
factory task inspect TASK-123
factory task events TASK-123
factory task sessions TASK-123
factory task cost TASK-123

factory task pause TASK-123
factory task resume TASK-123
factory task steer TASK-123 "Do not change the public API"
factory task abort TASK-123

factory question list TASK-123
factory question answer QUESTION-9 "Use the existing export endpoint"

factory worker list
factory policy explain TASK-123 action=push
```

### 23.2 HTTP API

Suggested shape:

```text
POST   /v1/tasks
GET    /v1/tasks
GET    /v1/tasks/{id}
POST   /v1/tasks/{id}/confirm
POST   /v1/tasks/{id}/pause
POST   /v1/tasks/{id}/resume
POST   /v1/tasks/{id}/steer
POST   /v1/tasks/{id}/abort
POST   /v1/tasks/{id}/retry

GET    /v1/tasks/{id}/events
GET    /v1/tasks/{id}/events/stream
GET    /v1/tasks/{id}/sessions
GET    /v1/tasks/{id}/costs
GET    /v1/tasks/{id}/artifacts

GET    /v1/questions
POST   /v1/questions/{id}/answer

GET    /v1/workers

POST   /v1/webhooks/slack
POST   /v1/webhooks/discord
POST   /v1/webhooks/github
POST   /v1/webhooks/linear
POST   /v1/webhooks/jira
```

All mutation endpoints should accept or derive idempotency keys.

---

## 24. Pause, steer, abort, and retry semantics

### Pause

Two modes:

```text
graceful pause:
  finish current safe atomic action, then stop

hard pause:
  abort current OpenCode turn/process immediately
```

The UI should distinguish them.

On pause:

- stop starting new phases;
- abort/suspend active agent execution as requested;
- leave workspace intact;
- continue to receive and store external webhooks;
- do not act on them until resumed unless explicitly allowed.

### Resume

Resume from the durable macro state, not from an assumption that the prior process survived.

### Steer

A steering instruction becomes a versioned task directive:

```text
Directive
- id
- task_id
- text
- created_by
- created_at
- scope
- supersedes?
```

If the task is actively reasoning, the orchestrator can be interrupted/woken according to policy. If it is waiting, include the directive in the next wake instead of waking immediately.

### Abort

On abort:

- cancel workflow;
- abort active agent sessions;
- terminate test/browser services;
- revoke task credentials;
- release worker;
- preserve logs/events/artifacts;
- policy decides whether branches/PRs are left, closed, or marked draft.

### Retry

Retry should be phase-scoped when possible:

```text
retry testing
retry PR fix
retry worker provisioning
```

Do not automatically rerun the whole task.

---

## 25. Security model

### 25.1 Treat repository content and PR comments as untrusted

A repository can contain prompt-injection text.

PR comments can contain instructions from unauthorized users.

Therefore:

- distinguish data from authorized human commands;
- verify actor identity and permissions before treating a comment as steering;
- never let content grant itself more capabilities.

### 25.2 Credentials

Use a credential broker.

Prefer:

- short-lived GitHub App installation tokens;
- scoped Jira/Linear OAuth credentials;
- task-scoped QA tokens;
- expiring cloud credentials;
- secret references rather than raw secret values in prompts.

Agents should receive only the minimum credentials for the current phase.

### 25.3 Network

Default deny or allowlist for agent/task environments.

Common allowlist targets:

- package registries;
- source-control host;
- internal dev APIs required by task;
- model proxy;
- control-plane tool gateway.

### 25.4 Filesystem

Mount only:

- task workspace;
- explicit caches;
- task-local artifact path.

No host home directory or control-plane secrets.

### 25.5 Git

Agents may:

- edit task branches;
- create commits;
- push task branches when policy permits.

Agents may not:

- force push protected branches;
- push directly to default branch;
- alter repository settings;
- access unrelated repositories.

### 25.6 High-risk actions

Require human approval for configurable actions, such as:

- migrations;
- permission/authentication changes;
- infrastructure changes;
- destructive data operations;
- production interactions;
- unusually high spend;
- policy exceptions.

---

## 26. Webhook and integration reliability

Every inbound webhook should be handled as:

```text
verify signature
  ↓
persist raw delivery + unique delivery ID
  ↓
return success quickly
  ↓
process asynchronously
  ↓
emit normalized domain event
```

Requirements:

- deduplicate deliveries;
- preserve raw payload;
- store processing status;
- retry failed internal processing;
- use idempotent adapter operations;
- never rely on exactly-once external delivery.

This applies to Slack, ticket systems, SCM, and CI.

---

## 27. E2E testing the factory itself

Most factory tests should consume **zero model tokens**. The black-box/system E2E suite should be written in **Python**, even though the product backend is TypeScript/Bun.

### 27.1 Fake agent runtime

Build:

```text
FakeAgentRuntime
```

It can execute scripted scenarios:

```yaml
- output: asks_question
- input: human_answer
- output: implementation_complete
- child_agent: reviewer
- output: blocking_finding
- child_agent: fixer
- output: fixed
- output: tests_pass
```

Use this for backend E2E tests.

### 27.2 Fake integrations

Implement test doubles for:

- Slack;
- Discord;
- Linear;
- Jira;
- GitHub;
- CI;
- cost proxy;
- worker provisioner.

### 27.3 Critical E2E scenarios

Test:

1. mention → clarify → confirm → ticket → work → PR;
2. no clarification required;
3. agent asks a human during implementation;
4. pause during active model turn;
5. resume after process restart;
6. abort while waiting for human;
7. review finding causes fix loop;
8. test failure causes fix loop;
9. PR comment causes fix/push loop;
10. approval + green CI causes `READY_TO_MERGE`;
11. worker dies and task recovers;
12. duplicate webhook is harmless;
13. hard cost limit pauses task;
14. multi-repo task opens multiple PRs;
15. unauthorized Slack/PR user tries to steer;
16. browser QA creates video artifact.

### 27.4 Real-model smoke suite

Have a small opt-in suite that exercises a real OpenCode/model path against a tiny fixture repository.

Do not run it on every commit.

---

## 28. Recommended implementation phases

## Phase 0 — Architectural spike

Goal: validate the critical integration seams.

Build:

- Bun/TypeScript control-plane skeleton;
- PostgreSQL;
- event table;
- fake workflow;
- `AgentRuntime` interface;
- one OpenCode runner;
- event capture from OpenCode;
- fake `ask_user`;
- static local worker;
- one fixture repository.

Exit criteria:

- create task via API;
- run an OpenCode session;
- persist session events;
- abort it;
- inspect it afterward.

## Phase 1 — Single-path MVP

Choose one of each integration to avoid doubling work:

- Slack **or** Discord;
- Linear **or** Jira;
- GitHub;
- static worker pool;
- single repository.

Implement:

- mention intake;
- clarification;
- confirmation;
- ticket creation;
- task workflow;
- OpenCode orchestrator;
- child agents;
- question gateway;
- implementation;
- reviewer/fixer loop;
- simplifier;
- deterministic tests;
- PR opening;
- GitHub review/CI webhook loop;
- pause/resume/steer/abort;
- CLI;
- costs;
- full event ledger.

Exit criteria:

A real team member can request a modest change in chat and observe it progress to an approved, merge-ready PR without the control plane polling an LLM for status.

## Phase 2 — Inspection and QA

Add:

- web dashboard;
- session tree;
- live event timeline;
- diff/artifact viewer;
- Playwright QA service;
- screenshots/video/traces;
- better cost dashboards;
- review findings UI.

## Phase 3 — Multi-repo + policy maturity

Add:

- native multi-repo workspace;
- multiple PRs per task;
- repository metadata;
- layered policies;
- security/database specialist reviewers;
- task-level policy explain;
- better branch/dependency sequencing.

## Phase 4 — Memory and context maturity

Add:

- source indexing;
- context packs;
- repository/team/company scopes;
- memory candidates;
- provenance;
- memory curation;
- stale-memory revalidation;
- retrieval metrics.

## Phase 5 — Distributed/dynamic runners

Add:

- remote runner daemon;
- capacity scheduler;
- worker labels;
- dynamic provisioner plugins;
- task checkpoints;
- worker suspend/recreate;
- stronger per-task VM/microVM boundaries;
- autoscaling.

## Phase 6 — Hardening

Add:

- HA control plane;
- disaster recovery;
- audit export;
- SSO/RBAC;
- secret broker;
- policy exception approvals;
- penetration testing;
- retention controls;
- larger-scale metrics and SLOs.

---

## 29. Suggested repository layout

```text
software-factory/
  apps/
    control-plane/
      src/
        api/
        projects/
        epics/
        work-items/
        runs/
        sessions/
        workflow/
        events/
        policies/
        harnesses/
        steps/
        artifacts/
        workers/
        integrations/
      tests/

    web/
      src/

  packages/
    domain/
    schemas/
    plugin-sdk/
    harness-sdk/
    step-sdk/
    integration-sdk/

  runner/
    src/
    images/

  e2e/
    pyproject.toml
    tests/
      api/
      workflows/
      integrations/
      browser/
      resilience/

  agents/
    orchestrator.md
    investigator.md
    implementer.md
    reviewer.md
    simplifier.md
    qa-browser.md

  policies/
    defaults.yaml
    examples/

  schemas/
    events/
    work-item/
    agent-output/
    artifacts/

  infra/
    terraform/

  docs/
    architecture/
    operations/
    adr/
```

The **product/control-plane implementation is TypeScript on Bun**. Python exists as a deliberate external test client in `e2e/`, so the E2E suite exercises the deployed system rather than importing backend internals.
## 30. The first agents

### Orchestrator

Responsibilities:

- understand current task phase;
- delegate investigation and implementation;
- reconcile subagent results;
- decide what work is needed next within the active phase;
- ask humans only when required;
- never busy-wait for external state.

### Investigator

Responsibilities:

- locate relevant code;
- identify dependencies;
- propose implementation shape;
- identify risks;
- no edits by default.

### Implementer

Responsibilities:

- make scoped changes;
- run targeted deterministic checks;
- report changed files and unresolved issues.

### Reviewer

Responsibilities:

- inspect the diff;
- report structured findings;
- no edits.

### Simplifier

Responsibilities:

- reduce unnecessary complexity without changing behavior;
- modify only the relevant diff area.

### QA browser agent

Responsibilities:

- run the specified user journey;
- collect objective evidence;
- report defects;
- produce screenshots/video when policy asks.

---

## 31. Key interfaces to define early

These interfaces prevent lock-in and keep the architecture modular:

```text
ChatAdapter
TicketAdapter
ScmAdapter
CiAdapter
AgentRuntime
WorkerProvisioner
ArtifactStore
CostProvider
ContextProvider
MemoryStore
PolicyEvaluator
WorkflowRuntime
SecretBroker
BrowserQA
```

Every integration-specific ID should be stored alongside an internal stable ID.

---

## 32. Key failure modes to design for

### Agent process dies

- mark run failed;
- preserve events;
- restart only the affected phase/session.

### Worker disappears

- expire lease;
- mark workspace unavailable;
- retry on another worker if reconstructable;
- otherwise escalate with clear artifact/state information.

### Duplicate webhook

- deduplicate by provider delivery ID/idempotency key.

### Human never replies

- workflow remains cheaply waiting;
- deterministic reminders can be scheduled;
- policy may time out/escalate.

### Review/fix loop does not converge

- cap iterations;
- escalate to human with unresolved findings and cost.

### Test/fix loop does not converge

Same.

### Agent exceeds budget

- hard stop/pause at control-plane boundary.

### Agent tries forbidden operation

- tool/runner policy denies it;
- emit a policy-denied event;
- optionally escalate if the operation appears necessary.

### Context/memory is wrong

- show provenance;
- allow invalidation;
- do not mutate raw historical events.

---

## 33. Acceptance criteria for a credible v1

A v1 should demonstrate:

- A Slack/Discord mention becomes a persisted task.
- The system asks a clarification when a required field is missing.
- The user explicitly confirms.
- A Jira/Linear ticket is created exactly once.
- A worker is assigned.
- An OpenCode orchestrator starts.
- The orchestrator spawns at least one subagent.
- An agent can ask a human and wait without ongoing model usage.
- A human can pause and resume the task.
- A human can steer the task and the instruction is auditable.
- A human can abort the task.
- Reviewer findings are structured.
- Blocking findings cause a fix loop.
- A simplifier runs after review.
- Required tests run.
- A browser QA run can capture a video artifact.
- A PR is opened.
- PR review events are received through webhooks.
- Review comments cause a fix/push loop.
- Approval + required green checks produce `READY_TO_MERGE`.
- Every phase/action creates inspectable events.
- Session relationships are visible.
- Task cost can be calculated.
- Lead time, human wait time, review time, and agent active time can be calculated from events.
- No component polls an LLM merely to discover external state.

---

## 34. Architectural decisions I would make now

1. **Keep the control plane deterministic.**
2. **Use a durable workflow abstraction; strongly consider Temporal rather than hand-building every waiting/retry edge case.**
3. **Use PostgreSQL as the domain/event/search backbone.**
4. **Treat OpenCode as a pluggable execution runtime.**
5. **Persist normalized OpenCode events because live event streams alone are not a durable audit log.**
6. **Build `ask_user` as an asynchronous durable workflow primitive.**
7. **Use webhooks for PR/ticket/chat state changes.**
8. **Use static workers first, behind a provisioner interface.**
9. **Do not mount the host Docker socket into autonomous agent containers.**
10. **Use worker VM/node isolation as the long-term security boundary.**
11. **Use Playwright for browser QA and video before building generalized desktop computer-use infrastructure.**
12. **Make multi-repo tasks native in the schema from the beginning.**
13. **Keep immutable event history separate from curated memory.**
14. **Make policy evaluation deterministic and explainable.**
15. **Create a fake agent runtime so almost all platform E2E tests cost zero tokens.**

---

## 35. Implementation notes verified against current official documentation

As of this draft:

- OpenCode exposes programmatic sessions and session inspection, including child sessions, status, abort, messages, and diffs.
- OpenCode exposes an event stream and supports subagents.
- OpenCode supports custom tools/plugins and per-agent permissions.
- OpenCode has a user-question capability; this design should bridge/override that concept into the factory's asynchronous question service.
- OpenCode event subscriptions should be treated as live transport, not the factory's durable event store.
- Slack exposes an `app_mention` event suitable for an `@factory` entry point.
- Linear supports webhooks and recommends webhooks over polling for updates.
- Jira Cloud supports issue creation and webhooks.
- GitHub exposes separate webhook events for PR reviews, review comments, and review threads.
- Playwright supports test/browser-context video recording.
- Docker supports rootless Docker-in-Docker, but the documented DinD pattern still uses privileged execution.
- Docker explicitly warns that privileged containers are not securely sandboxed, and host Docker access is highly privileged.

These facts reinforce the architecture above rather than requiring custom polling or a model-driven scheduler.

---

## 36. Questions for the next design pass

These answers will materially change the detailed design. They do not block starting the core architecture.

### Highest priority

1. **Which SCM is first: GitHub, GitLab, or something else?**
2. **Which ticket system is first: Linear or Jira?**
3. **Which chat system is first: Slack or Discord?**
4. **Where will the control plane run: Kubernetes, plain VMs, an internal platform, or something else?**
5. **What is the trust model for repositories?** Only your private repos, or can the factory execute code from forks/external contributors?
6. **What autonomy level do you want at merge time?** Stop at `READY_TO_MERGE`, or allow policy-controlled automatic merge?

### Runner/security

7. What maximum concurrent tasks do you expect in the first 6 months?
8. Can one static runner handle several isolated tasks, or do you want one VM/node per task immediately?
9. Are tasks allowed outbound internet access by default?
10. Which secret systems already exist?
11. Do you need Linux only, or macOS/Windows/mobile QA later?

### Agent/cost

12. Does your cost proxy report child/subagent sessions separately or include them in the parent?
13. Do you have a preferred model-routing policy by agent role?
14. Should there be hard task budgets by default?
15. Should cost-limit escalation go only to the requester or to an engineering owner as well?

### Context/memory

16. Where is company context today—Git, Notion, Google Drive, Confluence, Slack, elsewhere?
17. Should agents be able to propose global/company memories automatically, or only repo/task memories?
18. Who may approve or invalidate a memory?

### QA

19. Is browser QA enough for the first version, or do you require native desktop computer use immediately?
20. Are your applications easy to boot locally with test data, or will QA often need shared staging environments?
21. Should successful UI changes always attach a video to the PR, or only when specifically requested/policy-triggered?

### Workflow behavior

22. Should a human steering message invalidate prior review/test results automatically?
23. When multiple repositories are changed, must every PR be approved before any can merge?
24. Should the factory create draft PRs early for visibility, or only open PRs after local review/testing completes?
25. Who is allowed to pause, steer, abort, approve exceptions, and answer agent questions?

---

## 37. Recommended next deliverables

After answering the highest-priority questions, turn this architecture into four more concrete artifacts:

1. **`domain-model.md`** — entities, state machines, invariants, event schemas.
2. **`api.md`** — HTTP/CLI/webhook contracts and idempotency rules.
3. **`runner.md`** — worker protocol, isolation model, workspace layout, provisioning interface.
4. **`mvp-backlog.md`** — milestones broken into implementable tickets with acceptance criteria.

The first coding spike should validate the end-to-end skeleton:

```text
API task
  -> durable workflow
  -> static worker
  -> OpenCode session
  -> persisted event stream
  -> agent asks question
  -> human answer signal
  -> resume
  -> abort/inspect
```

If that skeleton is solid, the rest of the factory can be added incrementally without changing the fundamental model.
---

## 38. Locked v0.2 integration decisions

The first production path is:

```yaml
scm: github
ticketing: jira
chat_entrypoint: slack
primary_ui: web
backend_runtime: bun
backend_language: typescript
database: postgres
worker_daemon: go_or_rust
tooling_language: python_3_14
e2e_language: python_3_14
auto_merge: policy_controlled
github_updates: webhook_driven
```

Slack is an **adapter**, not the product shell. Everything possible from Slack must also be possible through the web/API and, where appropriate, the CLI.

GitHub should be integrated as a **GitHub App** using short-lived installation credentials and minimum required permissions.

Normal GitHub state changes must be driven by webhooks, including:

- `pull_request`;
- `pull_request_review`;
- `pull_request_review_comment`;
- `pull_request_review_thread`;
- `issue_comment`;
- `check_run`;
- `check_suite`;
- `push`;
- merge/close lifecycle events.

A low-frequency reconciliation query is allowed only for recovery after missed webhooks, restarts, or detected inconsistency. It is not the normal control loop.

Jira and Slack should follow the same normalized-integration principle: provider payloads enter through adapters and become internal events.

---

## 39. Product hierarchy: Project → Epic → Work Item → Run → Session

Do **not** use agent sessions as the main user-facing unit. Sessions are execution details and can be restarted, forked, replaced by another harness, or multiplied by subagents.

Use this hierarchy:

```text
Organization
  └─ Project
      └─ Epic
          └─ Work Item
              ├─ Run
              │   ├─ Session
              │   │   └─ child Sessions
              │   └─ Artifacts
              ├─ Pull Requests
              ├─ Questions
              ├─ Findings
              └─ Artifacts
```

### Project

A durable product/codebase/workstream boundary.

Examples:

```text
Customer Portal
Payments Platform
Internal Developer Platform
```

A project may map to one or many repositories and has:

- repositories;
- default policies;
- default context sources;
- allowed harnesses;
- default worker requirements;
- Jira project mapping;
- Slack channel mapping;
- deployment environments.

### Epic

A business/engineering objective containing multiple work items.

Examples:

```text
Self-service billing
OAuth migration
Q4 performance initiative
```

An epic can map to a Jira Epic but should have its own stable internal ID.

### Work Item

This supersedes the user-facing meaning of `Task`.

A Work Item is one piece of requested work with:

- goal;
- acceptance criteria;
- repositories;
- requester;
- source conversation;
- Jira issue(s);
- policies;
- status;
- questions;
- PRs;
- artifacts;
- total cost;
- full event timeline.

For internal compatibility, existing `task_id` concepts can be renamed gradually to `work_item_id`.

### Run

A Run is one execution attempt of a Work Item.

Why this level matters:

- retrying a Work Item should not erase the prior attempt;
- a user may explicitly rerun with another harness/model;
- metrics need attempt-level cost and duration;
- failed/aborted runs remain inspectable.

Example:

```text
WI-123
  Run 1 — OpenCode — aborted after requirement changed
  Run 2 — OpenCode — completed
```

### Session

A Session is a harness-specific agent session.

Examples:

```text
orchestrator session
reviewer child session
QA session
fixer session
```

Sessions belong to a Run.

This prevents the domain model from assuming that one Work Item equals one LLM conversation.

---

## 40. Artifact architecture

Artifacts are first-class domain objects, not files hidden in a runner workspace.

Examples:

- Markdown documents;
- architecture plans;
- generated reports;
- patches;
- screenshots;
- videos;
- Playwright traces;
- test reports;
- Terraform plans;
- SBOMs;
- coverage reports;
- logs;
- exported data;
- build outputs intended for human consumption.

### Artifact lifecycle

```text
agent/tool writes file in task workspace
          ↓
runner detects/registers artifact
          ↓
ArtifactStore uploads immutable bytes
          ↓
control plane stores metadata
          ↓
artifact.created event
          ↓
available to:
  web UI
  API
  CLI
  Slack adapter
  PR/ticket adapter where policy permits
```

### Artifact record

```text
Artifact
- id
- organization_id
- project_id?
- epic_id?
- work_item_id?
- run_id?
- session_id?
- producer
- type
- filename
- media_type
- size_bytes
- sha256
- storage_key
- created_at
- retention_policy
- visibility
- metadata JSONB
- provenance_event_id
```

The bytes belong in S3-compatible object storage rather than PostgreSQL.

### Agent artifact tool

Agents should get a factory-native tool:

```text
publish_artifact(
  path,
  title?,
  type?,
  description?,
  visibility="work_item",
  share_to_origin=false
)
```

The tool:

1. verifies the path is inside the permitted workspace;
2. computes hash and size;
3. uploads it through the runner/control plane;
4. creates the artifact record;
5. returns an immutable artifact ID and API URL.

The agent should never need S3 credentials.

### Retrieving an agent-created `.md`

Example:

```text
orchestrator produces /workspace/artifacts/design.md
  ↓
publish_artifact(path="/workspace/artifacts/design.md")
  ↓
artifact = ART-8291
  ↓
GET /v1/artifacts/ART-8291
GET /v1/artifacts/ART-8291/download
```

The web UI can render Markdown inline and provide a download action.

CLI:

```bash
factory artifact list WI-123
factory artifact get ART-8291
factory artifact download ART-8291 --output design.md
```

### Slack delivery

Yes: the Slack adapter can make the same artifact downloadable in Slack.

Two modes:

#### A. Upload the file to Slack

Useful for small human-facing artifacts such as `.md`, `.txt`, screenshots, or short reports.

The Slack adapter obtains an external upload URL, uploads the bytes, finalizes the upload, and associates it with the originating task thread.

#### B. Send a signed factory download link

Prefer this for:

- large videos;
- traces;
- sensitive artifacts;
- artifacts whose access should remain governed by factory RBAC;
- short-lived download permissions.

Example message:

```text
Design document produced: design.md
[View] [Download] [Open in Work Item]
```

Default policy:

```yaml
artifacts:
  slack:
    upload_max_bytes: 10_000_000
    upload_types:
      - text/markdown
      - text/plain
      - image/png
      - image/jpeg
    otherwise: signed_link
```

Slack copies should be treated as distribution copies. The factory ArtifactStore remains the source of truth.

---

## 41. Web UI is the primary control surface

Slack is one plugin-like input/output channel.

All lifecycle operations must exist in the backend API first, then be exposed through:

```text
Web UI
CLI
Slack
future Discord
future IDE integration
future API clients
```

### Web UI navigation

Suggested information architecture:

```text
Projects
  Project
    Overview
    Epics
    Work Items
    Runs
    Repositories
    Deployments
    Policies
    Memory / Context
    Metrics

Epic
  Work Items
  Status rollup
  Cost rollup
  Artifacts

Work Item
  Overview
  Timeline
  Runs
  Sessions
  Questions
  Changes / PRs
  Review findings
  Tests / QA
  Deployments
  Artifacts
  Cost

Run
  State machine
  Sessions
  Worker
  Logs
  Events
  Costs
  Artifacts

Session
  Messages
  Tool calls
  Child sessions
  Diff
  Cost
  Current activity
```

The Work Item page is the operational center. Session pages are deep inspection views.

### Start work without Slack

Web flow:

```text
Project
  -> New Work Item
  -> describe request
  -> attach files / select repos
  -> clarification conversation
  -> review TaskSpec
  -> Confirm
```

The exact same backend command/event flow is used when the request originates in Slack.

---

## 42. Modular workflow steps

Make workflow steps first-class plugins. This is separate from agent tools.

A **Step** is a durable workflow unit such as:

```text
clarify
confirm
create_jira_ticket
investigate
implement
review
fix
simplify
test
browser_qa
open_pr
wait_for_review
deploy_preview
deploy_staging
terraform_plan
approval_gate
terraform_apply
smoke_test
notify
```

### Step interface

Conceptually:

```ts
interface StepPlugin {
  manifest: StepManifest;

  prepare(ctx: StepContext): Promise<PreparedStep>;
  execute(ctx: StepContext, input: unknown): Promise<StepResult>;
  cancel?(ctx: StepContext): Promise<void>;
  reconcile?(ctx: StepContext): Promise<ReconcileResult>;
}
```

A step declares:

```yaml
id: terraform.apply
version: 1
input_schema: ...
output_schema: ...
capabilities:
  - terraform
side_effects:
  - infrastructure_write
requires_approval: policy
supports_cancel: true
supports_reconcile: true
```

### Step types

Three useful classes:

#### DeterministicStep

No LLM.

Examples:

- create Jira issue;
- checkout repositories;
- run test command;
- open PR;
- upload artifact;
- Terraform plan/apply;
- check GitHub mergeability.

#### AgentStep

Starts/resumes a harness.

Examples:

- investigate;
- implement;
- review;
- simplify;
- exploratory QA.

#### WaitStep

Consumes no worker/model while waiting.

Examples:

- wait for human answer;
- wait for GitHub review;
- wait for CI;
- wait for approval gate;
- wait for deployment health event.

### Workflow definition

Workflows can then be data-driven:

```yaml
workflow: standard_feature
steps:
  - intake.clarify
  - intake.confirm
  - jira.create_issue
  - workspace.provision
  - agent.investigate
  - agent.implement

  - loop:
      step: agent.review
      while: findings.blocking > 0
      body:
        - agent.fix
        - test.targeted

  - agent.simplify
  - test.required
  - qa.browser_if_needed
  - github.open_pr
  - github.await_merge_readiness

  - if: policy.deploy_preview
    steps:
      - deploy.preview
      - qa.smoke

  - github.auto_merge_if_allowed
```

This makes adding deployment a workflow/configuration change rather than rewriting the orchestrator.

---

## 43. Tools vs. steps

Keep these distinct.

### Agent tool

A capability invoked by an agent during a reasoning turn.

Examples:

```text
search_code
run_command
ask_user
publish_artifact
query_memory
start_preview_environment
```

### Workflow step

A durable control-plane operation with lifecycle, events, policy, retries, and metrics.

Examples:

```text
deploy_staging
terraform_apply
wait_for_review
open_pull_request
```

A workflow step **may internally expose or invoke a tool**, but deployment should not exist only as an unconstrained orchestrator tool.

Recommended deployment pattern:

```text
orchestrator decides deployment is useful
        ↓
requests "deploy.preview"
        ↓
control plane evaluates policy
        ↓
DeployStep runs deterministically
        ↓
deployment result/artifacts returned
        ↓
orchestrator optionally resumes for QA/judgment
```

This keeps credentials and destructive side effects out of the model's direct control.

---

## 44. Terraform deployment

Yes, Terraform fits naturally as a deterministic deployment step.

Recommended flow:

```text
TerraformInitStep
  ↓
TerraformPlanStep
  ↓
publish plan artifact + parsed summary
  ↓
PolicyGate
  ↓
optional HumanApprovalStep
  ↓
TerraformApplyStep
  ↓
DeploymentHealthStep
  ↓
SmokeTestStep
```

The generated plan file must be treated as an immutable artifact tied to the apply operation.

Important invariants:

- use remote state;
- use state locking;
- no interactive prompts;
- scope credentials per environment;
- save/identify the exact plan that was approved;
- do not allow the agent to replace an approved plan before apply;
- parse machine-readable output for metrics/status;
- production auto-apply must be an explicit policy;
- destructive plans can require an additional approval even when normal deployments are automatic.

Example policy:

```yaml
deployment:
  preview:
    auto_apply: true

  staging:
    terraform:
      auto_apply: true

  production:
    terraform:
      auto_apply: false
      require_human_approval: true
      destructive_change_requires_approval: true
```

Terraform itself should run in a dedicated deployment execution context rather than inheriting all credentials from the coding worker.

---

## 45. Agent harness architecture

OpenCode should be the **first harness**, not the only harness.

Define:

```ts
interface AgentHarness {
  capabilities(): HarnessCapabilities;

  createSession(spec: SessionSpec): Promise<SessionHandle>;
  resumeSession(id: string, delta: ContextDelta): Promise<void>;
  steer(id: string, instruction: string): Promise<void>;
  abort(id: string): Promise<void>;

  status(id: string): Promise<HarnessStatus>;
  messages(id: string): Promise<HarnessMessage[]>;
  children(id: string): Promise<HarnessSession[]>;
  diff(id: string): Promise<DiffSnapshot>;

  events(id: string): AsyncIterable<HarnessEvent>;
}
```

Possible adapters:

```text
OpenCodeHarness
CodexHarness
ClaudeAgentSdkHarness
OpenHandsHarness
future internal harnesses
```

Normalize events into the factory's event schema.

### Harness capability negotiation

```yaml
name: opencode
capabilities:
  resumable_sessions: true
  subagents: true
  custom_tools: true
  mcp: true
  structured_output: true
  event_stream: true
  live_steering: true
```

A workflow/agent definition can require capabilities rather than naming a concrete harness:

```yaml
agent:
  role: reviewer
  requires:
    - structured_output
    - repository_read
```

Policy then selects a compatible harness/model.

### Why OpenCode remains a good first implementation

It aligns well with the requirements for:

- self-managed execution;
- inspectable sessions;
- subagents;
- custom tools;
- permissions;
- MCP/extensibility;
- event streaming.

But this factory will accumulate valuable metrics about harness/model performance. The architecture should allow later routing based on observed:

- cost;
- lead time;
- review quality;
- test pass rate;
- human-intervention rate;
- task category.

Do not hard-code workflow semantics into OpenCode-specific APIs.

---

## 46. Worker pools: Kubernetes and dedicated instances

Support both behind `WorkerProvisioner`.

### Kubernetes workers

Good for:

- controlled build/test jobs;
- known container workloads;
- high-density internal tasks;
- deterministic steps;
- browser QA pods;
- horizontally scalable runners.

Possible isolation improvements include dedicated node pools, runtime sandboxes, network policies, and stronger container sandboxes.

### Dedicated ephemeral VMs

Prefer for tasks requiring:

- arbitrary Docker Compose;
- Docker daemon access;
- nested container builds;
- broad local service orchestration;
- stronger tenant/task isolation;
- external-contribution execution.

VM flow:

```text
control plane
   ↓ provision
cloud provider / virtualization API
   ↓
cloud-init boots factory-runner
   ↓
runner registers outbound with control plane
   ↓
control plane leases task
```

### Do not use SSH as the primary worker protocol

SSH is useful for emergency debugging, but normal operation should use an outbound runner agent:

```text
factory-runner
  -> authenticated registration
  -> heartbeat
  -> task lease
  -> event/log stream
  -> artifact upload
  -> cleanup
```

Benefits:

- no inbound worker ports;
- easier dynamic provisioning;
- common protocol for Kubernetes and VMs;
- better auditability;
- less credential/key management.

Initial v1 can run both control plane and one runner on a single VM while preserving this logical boundary.

---

## 47. Trust classes for public repositories

The repositories are trusted, but code in external PRs is not automatically trusted.

Classify executions:

```text
TRUSTED_INTERNAL
UNTRUSTED_EXTERNAL
```

### TRUSTED_INTERNAL

Can receive policy-approved:

- repository write credentials;
- Jira access;
- QA credentials;
- deployment capabilities;
- auto-merge capability.

### UNTRUSTED_EXTERNAL

Run in a separate worker pool with:

- no deployment credentials;
- no production credentials;
- no GitHub write token initially;
- no organization-wide secrets;
- restricted outbound network;
- read-only or narrowly scoped SCM access;
- separate caches where poisoning matters;
- explicit promotion step before privileged actions.

Never decide trust merely from repository ownership. Derive it from the provenance of the commit/ref and authenticated actor.

---

## 48. Auto-merge

Auto-merge is a policy-controlled deterministic action.

Example:

```yaml
merge:
  enabled: true
  strategy: squash
  require:
    - required_ci_green
    - required_reviews
    - no_blocking_findings
    - no_open_blocking_questions
    - current_head_reviewed
    - deployment_gate_if_required
  deny_when:
    - external_untrusted
    - policy_exception_open
```

The LLM should not make the final merge authorization decision.

The orchestrator may report “I believe this is ready,” but the control plane computes `merge.allowed` from policy and observed facts.

---

## 49. Build-vs-buy / existing products

Before committing to building every layer, evaluate existing systems against this plan.

The closest current category is an **AI software factory / coding-agent orchestration platform**. Factory.ai is particularly close in positioning and should be evaluated as a reference implementation or possible partial solution.

Also evaluate narrower components rather than assuming one vendor must provide the whole factory:

- coding-agent harnesses;
- durable workflow engines;
- sandbox/runner products;
- browser QA services;
- deployment orchestration;
- artifact storage;
- observability.

The differentiators that may still justify a custom control plane are:

- harness independence;
- custom event/metrics model;
- your cost proxy;
- company-specific policies;
- custom memory;
- arbitrary multi-repository workflows;
- custom worker provisioning;
- explicit deterministic/model boundary;
- Terraform/deployment control;
- first-class Project/Epic/Work Item product model.

A sensible spike is to run the same 10–20 real engineering tasks through the leading existing product and through the proposed minimal control plane, then compare autonomy, inspectability, cost, intervention rate, and extensibility.

---

## 50. Updated v1 implementation slice

Build this vertical slice before broadening integrations:

```text
Web UI + Slack
      ↓
create Work Item
      ↓
clarify / confirm
      ↓
Jira issue
      ↓
Postgres durable workflow
      ↓
static factory-runner
      ↓
OpenCodeHarness
      ↓
implement / review / simplify / test
      ↓
publish Markdown + QA artifacts
      ↓
GitHub App opens PR
      ↓
GitHub webhooks
      ↓
review-fix loop
      ↓
policy computes merge readiness
      ↓
auto-merge if allowed
```

Then add:

```text
Terraform deployment step
Kubernetes worker provisioner
ephemeral VM provisioner
second agent harness
memory curation
additional chat/ticket adapters
```

Python E2E tests should drive the entire workflow through public APIs/webhooks and assert on PostgreSQL-visible outcomes only via supported APIs, not by importing the Bun implementation.

---

## 51. Worker node model: warm capacity, Docker-per-Run

Do **not** provision a VM for every Session or Run.

The worker architecture is:

```text
Node Provisioner
     │
     │ creates/removes capacity in the background
     ▼
Worker Node / VM
  ├─ Docker Engine
  ├─ factory-runner daemon
  ├─ Run container A
  ├─ Run container B
  └─ free capacity
```

The scheduler places a new Run on a node that already has sufficient free CPU, memory, disk, and capabilities. Provisioning new VMs is a pool-scaling operation rather than part of the normal Session startup path.

Typical path:

```text
Session confirmed
    ↓
Run created
    ↓
scheduler finds warm worker
    ↓
factory-runner starts isolated Docker runtime
    ↓
repositories + harness start
```

Pool scaling happens independently:

```text
capacity below threshold
    ↓
NodeProvisioner provisions new VM
    ↓
cloud-init installs/starts factory-runner
    ↓
runner registers
    ↓
capacity becomes schedulable
```

### Worker daemon language

The worker daemon should be a small compiled service written in **Go or Rust**, not Bun.

Recommended v1: **Go**.

Reasons:

- Docker publishes and documents an official Go SDK;
- the daemon's job is primarily systems integration, streaming, process/container supervision, and networking;
- deployment as a single static-ish binary is straightforward;
- API-version negotiation with Docker is already supported by the official Go client.

Rust remains a valid alternative if the team strongly prefers it. Keep the control-plane protocol language-neutral so the choice is reversible.

The worker daemon is intentionally small. It should not contain product/business workflow logic.

Responsibilities:

```text
register node
heartbeat
report capacity/capabilities
receive/renew Run lease
create workspace
start/stop/reconcile Docker containers
stream/spool logs and runtime events
inject scoped secrets
collect/publish artifacts
implement pause/abort primitives
garbage-collect orphaned resources
re-register/reconcile after restart
```

SSH remains break-glass/debug access only.

---

## 52. Python 3.14 tooling standard

Operational and repository tooling should standardize on **Python 3.14**.

As of this architecture revision, Python 3.14 is the current stable feature series.

Use Python 3.14 for:

- black-box E2E tests;
- deployment scripts;
- repository/bootstrap scripts;
- deterministic agent tools where a standalone script is appropriate;
- CI utility scripts;
- migration/maintenance utilities;
- fixture generators;
- QA orchestration helpers;
- local development tooling;
- administrative scripts.

Suggested layout:

```text
tooling/
  pyproject.toml
  src/factory_tools/
    deploy/
    github/
    qa/
    repos/
    maintenance/
    fixtures/

e2e/
  pyproject.toml
  tests/
```

The backend remains TypeScript/Bun because it is a long-running product service. The worker daemon remains Go/Rust because it is infrastructure software. Python is the default language for **tooling** rather than a requirement that every service be Python.

Pin the supported feature series explicitly in tooling CI so scripts do not silently drift across incompatible Python versions.

---

## 53. Multi-tenant organization model

The factory is multi-tenant from the first schema.

```text
Organization
  ├─ Members
  ├─ Integrations
  ├─ Organization settings
  └─ Projects
      ├─ Epics
      └─ Sessions
```

### Organization roles

Keep v1 deliberately simple:

```text
ORG_ADMIN
ORG_MEMBER
```

`ORG_ADMIN` can:

- manage organization members;
- install/remove integrations;
- configure organization-wide policies;
- create/configure projects;
- change project visibility;
- manage worker/deployment settings;
- access all projects in the organization.

`ORG_MEMBER` has access according to project visibility/membership.

### Project access modes

```text
ORGANIZATION
INVITE_ONLY
```

#### ORGANIZATION

Every active member of the organization is considered a member of the project.

They can read and write all project content.

#### INVITE_ONLY

Only explicitly invited organization members can access the project.

For v1 there are no project-specific read/write roles. If a user is a project member, they can read and write everything within that project.

### Project membership

```text
ProjectMembership
- organization_id
- project_id
- user_id
- created_at
- invited_by
```

For `ORGANIZATION` projects, explicit rows are not required for every organization member; membership may be derived.

For `INVITE_ONLY`, explicit membership is required.

### Tenant isolation in PostgreSQL

Every tenant-owned table carries `organization_id`.

Examples:

```text
projects
epics
sessions
runs
agent_executions
artifacts
events
questions
pull_requests
deployments
memories
integrations
policies
```

Use PostgreSQL Row Level Security as defense-in-depth in addition to application authorization.

Every request establishes an authenticated organization context before tenant data is accessed.

Important invariants:

```text
resource.organization_id == request.organization_id
project belongs to organization
epic belongs to project + organization
session belongs to project + organization
artifact/event/run cannot cross organization
```

Use composite tenant-aware foreign keys where practical for high-value relationships.

### Artifact isolation

Object storage keys are namespaced:

```text
org/{organization_id}/project/{project_id}/session/{session_id}/...
```

Signed download URLs are only created after an application authorization check.

### Worker isolation

Every Run lease includes:

```text
organization_id
project_id
session_id
run_id
```

Docker resources are labeled with these identifiers.

Do not share across organizations:

- writable workspaces;
- secret mounts;
- task credentials;
- artifact staging directories;
- caches that can leak source or credentials.

Safe global content-addressed caches may be introduced only after explicitly analyzing whether they can leak tenant data.

### Integrations

GitHub App installations, Jira credentials, and Slack installations belong to an Organization.

External identities map to an internal user within that organization.

A Slack user attempting to access an invite-only project they are not a member of must not gain access merely because the bot is present in a channel.

---

## 54. Project Kanban board

Each Project gets a board of Sessions.

The board is a **read model/projection** of durable Session/workflow state rather than an independent state machine.

Recommended columns:

```text
BRAINSTORM
QUEUED
RUNNING
REVIEW
READY_TO_MERGE
DONE
```

### BRAINSTORM

Includes:

```text
draft
clarifying
awaiting confirmation
```

This is where a user can create an empty Session, discuss it, attach files, refine scope, and leave it indefinitely without spending agent execution tokens.

### QUEUED

Confirmed work that has not yet begun active execution:

```text
confirmed
awaiting worker capacity
provisioning workspace
```

### RUNNING

Active engineering work:

```text
investigating
implementing
fixing
simplifying
testing
browser QA
deployment preview when part of implementation
```

### REVIEW

Work that has reached the external review lifecycle:

```text
PR opened
waiting for CI
waiting for review
addressing review comments
```

When actively fixing review comments, detailed state may be `PR_FIXING`, but the card remains visually in Review.

### READY_TO_MERGE

The deterministic policy engine says all merge requirements have been satisfied.

If auto-merge is enabled, Sessions may spend only a short period here before moving to Done.

### DONE

Merged/completed/closed work.

Keep aborted Sessions available through filters rather than placing them in the normal successful flow.

### Cross-cutting card badges

Use badges rather than extra columns for:

```text
WAITING ON HUMAN
PAUSED
FAILED / NEEDS ATTENTION
AUTO-MERGE ON
EXTERNAL / UNTRUSTED
cost
age
number of open questions
number of PRs
```

### Card example

```text
┌──────────────────────────────┐
│ CSV invoice export           │
│ EPIC: Self-service billing   │
│                              │
│ REVIEW                       │
│ Waiting on reviewer          │
│                              │
│ web #1832 · api #918         │
│ $4.82 · 38 min               │
│ AUTO-MERGE ON                │
└──────────────────────────────┘
```

### Filters

Support:

- Epic;
- requester;
- repository;
- labels;
- waiting-on-human;
- paused;
- assignee/owner if added later;
- age;
- cost;
- Session origin (web/Slack/API).

Dragging cards between columns should **not** directly rewrite workflow state.

If drag-and-drop is added, it maps to explicit commands. Example:

```text
Brainstorm → Queued = Confirm / Start
```

Dangerous transitions still require the appropriate policy/confirmation.

---

## 55. Deployment architecture: deployments must not disrupt active Runs

Platform deployment and Work execution are separate lifecycle domains.

Never implement a platform deployment as:

```text
docker compose down
docker compose up
```

on a Compose project that owns active Run containers.

### Three independently versioned things

Track:

```text
ControlPlaneVersion
RunnerDaemonVersion
WorkRuntimeVersion
```

A Run is pinned when it begins:

```text
run.runtime_image_digest
run.harness_version
run.tooling_version
run.runner_protocol_version
```

A later platform deployment does not mutate those values.

### Control-plane/web deployment

The control plane is stateless with respect to live execution state; durable state is in PostgreSQL/object storage.

Deploy it blue/green or rolling:

```text
old API/web
      │ serving
      │
start new API/web
      ↓
health check
      ↓
switch reverse proxy / service
      ↓
drain old HTTP connections
      ↓
stop old API/web
```

Active work containers keep running throughout.

Kubernetes Deployments can provide rolling updates if/when the product services run on Kubernetes.

### Database migration rule

Use expand/contract migrations.

A release must remain compatible with the prior application version during rollout.

Typical sequence:

```text
release N:
  add nullable/new schema
  deploy code that understands both forms

release N+1:
  backfill/transition

release N+2:
  remove old schema after compatibility window
```

Do not ship a migration that instantly makes already-running API or worker versions invalid.

### Runner daemon deployment

Updating the worker daemon must not terminate Run containers.

Docker owns the container processes independently of the daemon process, so a runner restart should:

1. stop accepting new leases;
2. persist/spool any local unsent telemetry;
3. restart/upgrade the daemon;
4. scan Docker resources carrying `factory.*` labels;
5. reconstruct active Run leases;
6. reattach log/event streams;
7. resume heartbeats.

For higher-risk runner upgrades:

```text
mark node DRAINING
no new Runs
wait for active Runs = 0
upgrade runner
re-register
mark READY
```

This is the safest default.

### Work runtime updates

Never update the image beneath an already-running Run.

New Runs use the new image digest. Existing Runs finish on the version with which they started.

---

## 56. Pull-based automatic deployment every 10 minutes

Use an intentionally simple **Python 3.14 deployment controller** for the first deployment system.

The deployment mechanism is separate from GitHub PR/review integration.

GitHub PR lifecycle remains webhook-driven.

Platform deployment can be pull-based:

```text
systemd timer / scheduler
       ↓ every 10 min
python3.14 deploy.py
       ↓
fetch target branch/ref from GitHub
       ↓
compare SHA with deployed SHA
       ↓
no change → exit
       ↓
change detected
       ↓
build/pull release
       ↓
run compatibility checks/migrations
       ↓
blue/green deploy
       ↓
health checks
       ↓
switch traffic
       ↓
record deployed SHA
```

A pull-based deployer has the advantage that the deployment host only needs outbound access to GitHub; GitHub does not need network access to the deployment environment.

### Deployment state

Store:

```text
deployment_id
target
git_repository
git_ref
git_sha
previous_git_sha
started_at
completed_at
status
healthcheck_results
migration_version
release_artifacts
```

### Single-VM v1

For one VM, use two separately named application stacks:

```text
factory-blue
factory-green
```

Work containers are **not** children of either stack.

Example:

```text
Docker Engine
  ├─ factory-blue-api
  ├─ factory-blue-web
  ├─ postgres / external postgres
  ├─ reverse-proxy
  │
  ├─ factory-run-SES123
  ├─ factory-run-SES124
  └─ factory-run-SES125
```

Deploying green must not stop the `factory-run-*` containers.

After green passes health checks, the reverse proxy switches traffic from blue to green.

On failure, leave blue serving and mark the deployment failed.

### Deployment script properties

`deploy.py` should be:

- idempotent;
- protected by a process/distributed lock;
- non-interactive;
- verbose and structured in logs;
- safe to rerun after partial failure;
- able to roll back application routing;
- explicit about the SHA being deployed;
- capable of posting a deployment event back to the factory.

Do not simply `git pull && docker compose down && docker compose up`.

### Worker-node updates

Worker nodes may use the same pull-based pattern, but daemon upgrades obey drain policy.

A node can check a release manifest every 10 minutes:

```text
new runner version?
  no → exit
  yes
    ↓
active Runs?
  yes → mark upgrade pending
  no  → upgrade + restart + reconcile
```

This lets platform releases proceed without sacrificing active work.

---

## 57. Updated product UI

Primary navigation:

```text
Organization switcher
  └─ Organization
      ├─ Projects
      ├─ Members
      ├─ Integrations
      ├─ Workers
      ├─ Policies
      └─ Settings
```

Project:

```text
Project
  ├─ Board
  ├─ Epics
  ├─ Sessions
  ├─ Repositories
  ├─ Deployments
  ├─ Artifacts
  ├─ Memory / Context
  ├─ Metrics
  ├─ Members       [invite-only projects]
  └─ Settings
```

### Default Project screen

The Kanban Board should be the default operational view.

Selecting a Session opens a full-page or side-panel detail view with:

```text
Conversation
Timeline
Current Run
Agent executions
Questions
Changes / PRs
Review findings
Tests / QA
Artifacts
Deployments
Cost
Policies
```

### Creating a Session

From Project or Epic:

```text
+ New Session
```

The initial Session can be empty.

A Session starts in `BRAINSTORM`. It may acquire compute immediately when the user asks it to inspect code, search repositories, run a deterministic tool, or otherwise needs a workspace.

```text
stage = BRAINSTORM
run = optional until first compute-requiring action
workspace = optional until first compute-requiring action
```

Once code-aware brainstorming begins, the Session can create a Run and a persistent Session Workspace even before implementation is confirmed.

Brainstorm execution has a more restrictive capability profile by default:

- repository/code access;
- search;
- read-only external context;
- local scratch changes if useful;
- artifact creation;
- human questions;

but normally no:

- Jira creation;
- GitHub push;
- PR creation;
- deployment;
- merge;
- privileged credentials.

`Start work` is therefore an **authorization/state transition**, not necessarily the moment compute starts. It confirms the implementation scope, creates Jira according to policy, and unlocks the implementation capabilities for the existing Run or a new Run.

This allows brainstorming to be code-aware while still keeping unconfirmed work from producing side effects.

---

## 58. Slack workflow under multi-tenancy

Slack remains an adapter into the same Session model.

### Slack installation

```text
Slack Workspace / Installation
           ↓
Organization
```

Channel mappings can provide defaults:

```text
#billing-engineering
  organization = Acme
  project = Billing
  default epic = optional
```

### Mention flow

Example:

```text
Alice:
@factory add CSV export to the invoice page
```

The Slack adapter:

1. verifies Slack signature;
2. resolves installation → Organization;
3. resolves Slack user → internal Organization member;
4. resolves channel → Project default;
5. checks Project access;
6. creates a Brainstorm Session;
7. binds the Slack thread to that Session;
8. posts a concise Session summary.

Example response:

```text
Created Session: CSV invoice export

Project: Billing
Epic: Self-service billing
Repos: web, billing-api

I have one question:
Should archived invoices be included?

[Include] [Exclude]

[Open Session]
```

After clarification:

```text
Ready to start:

Add CSV export to the invoice page.
- Respect active filters
- Include archived invoices
- web + billing-api

[Start work] [Edit] [Leave in brainstorm] [Cancel]
```

`Start work` creates the Run and, according to policy, creates the Jira issue.

### If no project can be inferred

Use a Slack modal or compact selection:

```text
Choose project:
[Billing]
[Customer Portal]
[Internal Tools]
```

Only show Projects the user can access.

### Invite-only behavior

For an invite-only Project, a Slack user without membership cannot create or inspect Sessions in that Project.

Do not leak Session details through bot responses.

### Slack as a view, not a second conversation model

Persist one logical Session conversation:

```text
Session
  ├─ Web messages
  └─ Slack thread binding
```

A reply in Slack becomes a Session message/event.

A reply in the web UI becomes the same kind of Session message/event.

The origin is metadata:

```text
origin = web | slack | api | cli
```

This allows a user to start work in Slack and continue it entirely in the web interface.

---

## 59. Revised technology boundaries

```text
Web UI
  TypeScript

Control plane
  TypeScript + Bun

Database
  PostgreSQL

Worker daemon
  Go recommended / Rust supported

Agent harnesses
  OpenCode first, pluggable

Work runtimes
  Docker

Operational tooling
  Python 3.14

E2E/system tests
  Python 3.14

Deployment controller
  Python 3.14

Artifacts
  S3-compatible object storage

SCM
  GitHub App + webhooks

Ticketing
  Jira

Chat
  Slack adapter

Infrastructure
  warm worker-node pool + modular NodeProvisioner
```

This is intentionally polyglot at service boundaries, while keeping each component in the language that best matches its role.

---

## 60. Local provisioner

Build a **Local Provisioner** alongside the warm VM worker-pool provisioner.

Its purpose is:

- local development of the factory;
- integration testing without cloud infrastructure;
- debugging runner/harness behavior;
- running small trusted Sessions directly on a developer machine;
- exercising the same container lifecycle used in production.

The Local Provisioner runs work containers on the same machine as the control plane.

```text
Developer machine
├─ control-plane (Bun)
├─ web
├─ postgres
├─ factory-runner (Go/Rust)
└─ Docker Engine
    ├─ factory-run-SES-101
    ├─ factory-run-SES-102
    └─ test services
```

### Do not create a completely separate local execution path

The preferred architecture is:

```text
Control plane
     │
     │ Run placement protocol
     ▼
factory-runner on localhost
     │
     ▼
local Docker Engine
```

rather than:

```text
Control plane
     │
     └── directly manipulates Docker only in dev
```

Running the same `factory-runner` daemon locally means development exercises:

- worker registration;
- worker heartbeats;
- lease acquisition;
- capacity reporting;
- Run startup;
- Docker lifecycle;
- logs;
- event delivery;
- artifact collection;
- cancellation;
- cleanup;
- runner restart/reconciliation.

This substantially reduces differences between development and production.

For convenience, the local runner may bind only to loopback and auto-register itself with the local control plane.

### Provisioner layers

Avoid using the word `Provisioner` for two different responsibilities.

Use two explicit abstractions:

```text
ExecutionProvisioner
    decides where/how a Run executes

NodeProvisioner
    creates/removes remote worker capacity
```

Suggested interfaces:

```ts
interface ExecutionProvisioner {
  readonly id: string;

  capabilities(): Promise<ExecutionCapabilities>;

  acquire(spec: RunExecutionSpec): Promise<ExecutionLease>;

  release(lease: ExecutionLease): Promise<void>;
}
```

Implementations:

```text
LocalDockerProvisioner
WorkerPoolProvisioner
```

The remote implementation uses worker nodes:

```text
WorkerPoolProvisioner
      │
      ▼
WorkerScheduler
      │
      ▼
factory-runner on selected node
      │
      ▼
Docker
```

The local implementation selects the locally registered runner:

```text
LocalDockerProvisioner
      │
      ▼
factory-runner on localhost
      │
      ▼
local Docker
```

### Remote capacity provisioning

`WorkerPoolProvisioner` may itself rely on a `NodeProvisioner` when more capacity is required.

```ts
interface NodeProvisioner {
  provision(spec: NodeSpec): Promise<ProvisionedNode>;
  terminate(nodeId: string): Promise<void>;
  list(): Promise<ProvisionedNode[]>;
}
```

Implementations may include:

```text
StaticNodeProvisioner
HetznerNodeProvisioner
AwsEc2NodeProvisioner
GcpNodeProvisioner
KubernetesNodeProvisioner
```

This produces a clean separation:

```text
Run needs somewhere to execute
        ↓
ExecutionProvisioner
        ↓
LocalDockerProvisioner
        OR
WorkerPoolProvisioner
        ↓
WorkerScheduler
        ↓
existing warm node
        OR
NodeProvisioner adds capacity
```

### Project / organization configuration

Organizations can define available execution backends.

Example:

```yaml
execution:
  provisioners:
    - id: local
      type: local_docker
      enabled: true

    - id: workers
      type: worker_pool
      enabled: true

  default: workers
```

A local development organization can instead use:

```yaml
execution:
  default: local
```

Projects may override the default when policy permits.

### Run execution selection

A Run stores the actual execution backend used:

```text
Run
- id
- execution_provisioner
- worker_id
- container_id
- runtime_image_digest
- started_at
- finished_at
```

This keeps Runs reproducible and makes metrics comparable between local and remote execution.

### CLI / local development

Suggested commands:

```bash
factory dev up
factory worker status

factory session start SES-123 --provisioner local
factory session start SES-124 --provisioner workers
```

`factory dev up` can start:

```text
PostgreSQL
control plane
web UI
local factory-runner
object storage emulator / local S3-compatible service
```

The local runner registers exactly like a remote runner, but with labels such as:

```yaml
worker:
  id: local-dev
  provisioner: local
  labels:
    - local
    - linux
    - docker
    - browser
```

### Local artifact storage

Development may use:

```text
MinIO
LocalStack/S3-compatible service
filesystem-backed ArtifactStore
```

behind the same `ArtifactStore` interface.

Do not special-case artifact semantics merely because a Run is local.

### Local secrets

Local development credentials should come from the normal SecretBroker interface.

For example:

```text
.env / local secret file
        ↓
LocalSecretBroker
        ↓
task-scoped injection
```

Do not have agents read the developer's full environment or home directory.

### Safety

The local provisioner is intended for **trusted internal work only** by default.

Policy:

```yaml
local_provisioner:
  allow_trust_classes:
    - TRUSTED_INTERNAL
```

Code from external contributors should normally use the fenced remote worker pool even during development, unless the developer explicitly overrides that safety policy.

The local Run container should still receive:

- an isolated workspace;
- only required mounts;
- scoped credentials;
- resource limits;
- explicit network policy where practical.

Do not mount the developer's home directory or arbitrary host paths into autonomous Run containers.

### Resource limits

Because the control plane and Runs share one machine locally, the Local Provisioner must enforce configurable limits.

Example:

```yaml
local:
  max_concurrent_runs: 2
  reserve_cpu: 2
  reserve_memory_gb: 4
  per_run:
    cpu: 4
    memory_gb: 8
```

The scheduler must refuse or queue a local Run when starting it would starve the control plane.

### Testing strategy

The Local Provisioner should be the default execution backend for system tests that require real Docker behavior.

Test matrix:

```text
unit tests
  fake provisioner

fast E2E
  fake runner / fake harness

container E2E
  LocalDockerProvisioner + real local factory-runner

production smoke
  WorkerPoolProvisioner + remote worker node
```

This gives high confidence that local development and remote production share the same execution semantics.

### Product UI

When starting a Session manually, advanced options may show:

```text
Execution
○ Automatic
○ Local
○ Worker pool
```

Most users should see only `Automatic`.

For local development, `Automatic` resolves to `LocalDockerProvisioner`.

For production organizations, `Automatic` normally resolves to `WorkerPoolProvisioner`.

The Session/Run page should show:

```text
Execution: Local
Worker: local-dev
Container: factory-run-01J...
```

or:

```text
Execution: Worker pool
Worker: worker-eu-03
Container: factory-run-01J...
```

The rest of the UI and workflow is identical.

---

## 61. Session Workspace, runtime instance, and fast repository materialization

The durable concepts should be separated:

```text
Session
  └─ Run
      ├─ Session Workspace
      ├─ Runtime Instance 1
      ├─ Runtime Instance 2   [after resume/replacement]
      └─ Agent Executions
```

### Session Workspace

A **Session Workspace** is the durable working directory for a Run.

It contains:

```text
workspace/
  repos/
    web/
    api/
  agent-state/
  scratch/
  artifacts-staging/
  runtime-manifest.json
```

The workspace lives outside the Docker container's writable layer and is mounted into the Run container.

This means:

```text
container dies
   ≠
workspace dies
```

A new Runtime Instance can mount the same workspace and continue.

### Runtime Instance

A Runtime Instance is one concrete Docker container used to execute the Run.

It is intentionally disposable.

Store:

```text
RuntimeInstance
- id
- organization_id
- session_id
- run_id
- worker_id
- container_id
- image_digest
- created_at
- started_at
- stopped_at
- destroyed_at
- generation
- status
```

If the container is replaced:

```text
Run 17
  Runtime generation 1 -> reaped
  Runtime generation 2 -> active
```

The Run and Session remain the same.

### Node-local repository cache

Each worker node maintains an organization-scoped repository cache.

Example:

```text
/var/lib/factory/
  repos/
    ORG-123/
      github.com/acme/web.git
      github.com/acme/api.git

  workspaces/
    ORG-123/
      SES-456/
        RUN-1/
          repos/
```

Repository caches must be tenant-scoped even when two organizations reference the same public repository. Do not use a cross-tenant writable Git cache.

### Keep mirrors warm

The runner maintains **bare mirrors** of repositories likely to be needed on that node.

Mirrors can be refreshed:

- when a Project is assigned to a node pool;
- periodically in the background;
- on Session creation;
- immediately before materializing a workspace if the cached ref is stale.

The scheduler should use **repository affinity** as part of worker placement:

```text
placement score =
  available CPU/RAM/disk
  + required capabilities
  + trust-class match
  + repository cache affinity
```

A node already caching all repositories for a Session should be preferred when capacity is otherwise comparable.

### Worktree vs. isolated checkout

Raw `git worktree` is extremely fast, but linked worktrees share important repository state with the common Git directory, including refs and the object store.

That is useful on a trusted developer machine, but it is not the isolation boundary we want between autonomous Runs.

Recommended production materialization:

```text
node-local bare mirror
        ↓
fast local per-Session clone
        ↓
isolated refs/config/index
        ↓
Run container
```

On Linux, the runner can use a local clone strategy that reuses local Git objects efficiently while giving the Session its own repository metadata.

The implementation should sit behind:

```ts
interface RepoMaterializer {
  ensureCached(repo: RepositoryRef): Promise<CachedRepository>;
  materialize(
    cached: CachedRepository,
    destination: string,
    revision: string
  ): Promise<MaterializedRepository>;
  checkpoint(...): Promise<RepoCheckpoint>;
  destroy(...): Promise<void>;
}
```

Implementations can experiment with:

```text
LocalCloneMaterializer
WorktreeMaterializer       [local trusted development]
ReflinkMaterializer        [filesystems supporting CoW clones]
```

The product does not need to know which optimization was used.

### Why not expose the mirror directly?

The agent must not be able to:

- corrupt the shared mirror;
- mutate another Session's refs;
- change shared Git config;
- race another Session's fetch;
- poison the cache.

The runner owns mirror updates.

Agents operate only on their Session materialization.

### Cache update locking

The runner serializes mirror mutation per repository:

```text
fetch/update mirror = exclusive lock
materialize checkout = read/snapshot operation
```

Avoid cloning while a mirror is being destructively maintained.

Do not run aggressive pruning in a way that can invalidate active materializations that depend on cached objects.

### GitHub access

Workspace materialization should normally require no GitHub network transfer when the requested commit already exists in the node cache.

GitHub credentials are only needed when:

- refreshing the mirror;
- fetching a missing ref;
- pushing a Session branch;
- opening/updating a PR through the GitHub integration.

The agent itself does not need the mirror's GitHub credential.

### Multiple repositories

A single Session Workspace can materialize all required repositories in parallel:

```text
ensure cache(web)
ensure cache(api)
ensure cache(schema)
       ↓
materialize all
       ↓
container starts
```

If the mirrors are warm, startup should mostly be local filesystem operations.

### Git LFS and submodules

Treat Git LFS and submodule materialization as explicit repository metadata.

Projects should declare whether they need:

```yaml
git:
  lfs: true
  submodules: recursive
```

LFS caches should also be organization-scoped.

Do not automatically download large LFS objects during every brainstorm Session if the current task does not need them.

---

## 62. Brainstorm Sessions are code-aware

`BRAINSTORM` is a workflow/product stage, not a "no compute" stage.

A typical flow becomes:

```text
New empty Session
      ↓
user types a request
      ↓
Session acquires worker/workspace
      ↓
repositories materialized from local cache
      ↓
brainstorm harness can:
  inspect code
  search
  read docs
  run safe local commands
  ask questions
  create design artifacts
      ↓
scope becomes clear
      ↓
user confirms Start work
      ↓
Jira issue + implementation capabilities
```

This means startup latency matters even before implementation.

### Brainstorm capability profile

Example:

```yaml
brainstorm:
  filesystem:
    workspace: read_write
  github:
    push: false
    open_pr: false
  jira:
    create_issue: false
  deployment:
    allowed: false
  shell:
    allowed: true
  artifacts:
    publish: true
  ask_user:
    allowed: true
```

The Session can make local exploratory edits if the harness finds that useful, but those edits are not pushed until the user confirms work and policy permits it.

If the user abandons the idea, the Session can simply be archived.

---

## 63. Runtime lifecycle, hibernation, and reaping

Do **not** make "Session not archived" mean "Docker container must exist forever."

Instead use a tiered runtime lifecycle.

```text
HOT
  container running

WARM
  container stopped but retained locally
  workspace retained locally

COLD
  container removed
  workspace retained locally

HIBERNATED
  container removed
  durable workspace checkpoint stored remotely
  local workspace may be removed

ARCHIVED
  no active runtime
  durable metadata/events/artifacts/checkpoint retained per policy
```

### HOT

Use when:

- an agent/tool is running;
- local services are active;
- the user is actively interacting;
- immediate resume is expected.

### WARM

When a Session becomes idle:

```text
docker stop
```

Retain the stopped container temporarily for extremely fast resume.

Stopped containers consume disk, not CPU.

### COLD

After a configurable idle period or under disk pressure:

```text
remove stopped container
retain Session Workspace
retain runtime manifest
retain pinned image digest
```

Resume by creating a new container and mounting the existing workspace.

For most Sessions, this should be almost as useful as keeping the old container.

### HIBERNATED

When local disk needs to be reclaimed:

1. ensure valuable Session state is externalized;
2. create a workspace checkpoint;
3. upload the checkpoint to durable object storage;
4. verify checksum;
5. remove local workspace;
6. retain only lightweight metadata.

Resume:

```text
scheduler chooses worker
      ↓
repo mirrors/materializations recreated
      ↓
checkpoint restored
      ↓
pinned runtime image started
      ↓
agent/harness resumes
```

A hibernated Session can therefore move to a different worker node.

### ARCHIVED

Archiving indicates that the Session is no longer expected to execute normally.

Policy decides retention for:

- workspace checkpoints;
- artifacts;
- logs;
- videos;
- agent transcripts;
- event history.

Archiving should immediately make the Session eligible for removal of all local runtime resources.

### Reaper policy

Example only; values are configurable:

```yaml
runtime_retention:
  stop_after_idle: 30m
  remove_container_after_idle: 6h
  hibernate_workspace_after_idle: 3d
  archive_after_idle: never

  disk_pressure:
    prefer_reap:
      - stopped_containers
      - reproducible_build_outputs
      - package_caches
      - cold_workspaces_after_checkpoint
```

Never reap a container while:

- an agent execution is active;
- a tool invocation is active;
- a test/deployment operation owned by that Runtime Instance is active.

### Reaper ownership

The control plane owns retention policy.

The worker daemon owns local execution.

Flow:

```text
worker reports:
  local disk
  containers
  last activity
  workspace sizes

control plane decides lifecycle transition
        ↓
worker executes stop/remove/checkpoint/cleanup
        ↓
events recorded
```

The worker may perform emergency disk-pressure cleanup using a pre-authorized deterministic policy, but must report every action.

---

## 64. What must survive a container

Anything valuable must not exist **only** in the Docker writable layer.

Persist or reconstruct:

### Always durable in control plane

- Session messages;
- questions/answers;
- directives/steering;
- event history;
- costs;
- policy evaluations;
- agent execution metadata;
- review findings;
- test results;
- PR/Jira references.

### Durable in ArtifactStore

- user-facing documents;
- screenshots;
- videos;
- traces;
- reports;
- generated plans;
- logs selected for retention.

### Durable/reconstructable repository state

For each repository record:

```text
remote
base SHA
Session branch
current HEAD
dirty-state metadata
untracked-file checkpoint when required
```

Prefer committing/pushing meaningful implementation state to the Session branch at safe checkpoints.

Do not depend on a local container layer as the only copy of code changes.

### Harness state

Harness session state must either:

- be stored by the harness in a mounted Session directory; or
- be exported/checkpointed into the Session Workspace/control plane.

Normalize important messages/tool events into the factory event ledger regardless.

### Local service state

Databases/test services used only as reproducible dev infrastructure can normally be recreated.

If exact state is valuable, put it in a Session-scoped persistent volume and add a checkpoint plugin for that volume.

Do not blindly snapshot every Docker volume.

---

## 65. Workspace checkpoint format

A checkpoint should be explicit and versioned.

Example manifest:

```json
{
  "version": 1,
  "session_id": "SES-123",
  "run_id": "RUN-4",
  "runtime_image": "sha256:...",
  "repos": [
    {
      "repo_id": "web",
      "base_sha": "...",
      "head_sha": "...",
      "branch": "factory/SES-123",
      "dirty": false
    }
  ],
  "harness": {
    "name": "opencode",
    "resume_metadata_path": "agent-state/opencode.json"
  },
  "files": [
    "scratch/...",
    "agent-state/..."
  ]
}
```

### Initial implementation

For v1, prioritize correctness over maximum storage efficiency.

A hibernation checkpoint may be:

```text
manifest.json
repo-state/
agent-state/
scratch/
selected session-local volumes/
```

compressed with a fast compressor and stored in object storage.

Repository source that can be deterministically reconstructed from Git does not need to be duplicated in full.

Later optimization can use:

- Git bundles for unpublished commits;
- patch/untracked-file archives;
- content-addressed chunks;
- deduplicated snapshots.

---

## 66. Artifact upload path: agents never receive S3 credentials

Use S3-compatible object storage as the durable ArtifactStore.

The agent should **not** get bucket credentials.

The preferred path is:

```text
agent
  ↓
publish_artifact("/workspace/artifacts/report.md")
  ↓
Session-scoped factory tool endpoint
  ↓
worker runner validates path + metadata
  ↓
control plane creates Artifact record / upload grant
  ↓
worker uploads bytes directly to S3
  ↓
control plane finalizes Artifact
  ↓
artifact.created event
```

### Why the runner uploads

The host runner already has trusted knowledge of:

- Organization;
- Project;
- Session;
- Run;
- allowed workspace root;
- artifact ID;
- expected object key.

It can verify that the requested file belongs to the current Run.

The agent never sees:

- S3 access keys;
- bucket-wide credentials;
- another organization's prefix.

### Prefer exact-object upload grants

The worker itself does not need broad long-lived S3 credentials either.

Recommended pattern:

1. Runner calls:
   ```text
   POST /artifacts/begin-upload
   ```
2. Control plane authorizes the Session and chooses an exact object key.
3. Control plane returns a short-lived upload grant, such as a presigned S3 PUT URL.
4. Runner uploads that exact file directly to object storage.
5. Runner reports SHA-256, size, and completion.
6. Control plane verifies/finalizes metadata.

S3 presigned upload URLs allow a specific object to be uploaded without giving the uploader general AWS credentials. citeturn805411search0turn805411search5

The presigned URL can remain inside the runner process; there is no reason to expose it to the agent.

### Large artifacts

For large videos/traces, the `ArtifactStore` API should support multipart uploads behind the same abstraction.

The caller still sees only:

```text
publish_artifact(path)
```

### Artifact staging directory

Each Runtime gets:

```text
/workspace/artifacts/
```

Agents/tools can write candidate artifacts there.

Publishing is explicit.

This prevents every temporary file from automatically becoming a permanent object.

### Artifact immutability

On publish:

```text
compute sha256
record size
detect/validate media type
choose immutable object key
upload
```

Example key:

```text
org/ORG-1/project/PROJ-2/session/SES-3/run/RUN-4/artifacts/ART-5/report.md
```

Do not overwrite an existing Artifact object.

A revised report creates a new Artifact with lineage metadata:

```text
supersedes_artifact_id
```

### Artifact downloads

The web/Slack/API layer performs authorization first, then generates a short-lived download URL.

Object storage itself stays private.

### Local development

Use the same `ArtifactStore` interface with:

```text
MinIO
```

or another S3-compatible local service.

This lets the entire upload/download path be tested locally without changing agent behavior.

---

## 67. Session-local factory tool endpoint

The Run container needs a narrow mechanism to call host/control-plane capabilities without receiving worker credentials.

Provide a Session-scoped tool endpoint.

Possible implementation:

```text
/container/run/factory.sock
```

mounted as a Unix socket, or a loopback network endpoint with a short-lived Session token.

Expose only capability-limited operations such as:

```text
ask_user
publish_artifact
emit_progress
read_session_metadata
request_step
```

Do **not** expose the entire worker daemon administration API.

The runner validates:

```text
container identity
Run lease
organization_id
session_id
operation
path/capability
```

This becomes the secure bridge between autonomous code running in Docker and trusted host-side operations.

---

## 68. Revised fast Session startup path

With repository affinity and warm images, the ideal startup path is:

```text
User creates/continues Session
        ↓
scheduler selects warm node
  - enough CPU/RAM
  - correct trust pool
  - required runtime image cached
  - project repositories cached
        ↓
create Session Workspace
        ↓
materialize repositories locally
        ↓
docker create
        ↓
mount workspace + factory tool socket
        ↓
start harness
```

The expensive network operations are moved off the critical path:

```text
Git fetches       -> background mirror refresh
container images  -> pre-pulled on workers
tool images       -> pre-pulled
repo dependencies -> cache where safe
```

Measure:

```text
session requested -> worker selected
worker selected   -> repos materialized
repos ready       -> container started
container started -> first harness action
```

These should be first-class latency metrics because "new Session feels instant" is a product requirement.

---

## 69. New worker responsibilities

The worker daemon now owns these additional modules:

```text
CapacityReporter
ContainerManager
RepoCacheManager
RepoMaterializer
WorkspaceManager
RuntimeReconciler
ArtifactUploader
LocalReaper
CheckpointManager
FactoryToolGateway
```

It still does **not** own workflow/business policy.

The control plane tells it what lifecycle transition is allowed; the runner makes that transition real on the node.

---

## 70. Memory subsystem: first-class tools and hybrid semantic search

Memory should be implemented early enough that the factory can use it while building itself.

Do not treat "memory" as one large conversation transcript.

Separate four concepts:

```text
Source Knowledge
  authoritative documents/code/ADRs/runbooks

Episodic Memory
  what happened in Sessions/Runs

Semantic Memory
  durable facts/lessons/conventions learned from experience

Procedural Memory
  reusable ways of doing things
  commands, workflows, setup quirks, debugging recipes
```

### Source Knowledge

Examples:

- architecture documents;
- `AGENTS.md`;
- repository READMEs;
- ADRs;
- API specifications;
- engineering handbooks;
- runbooks;
- code symbols/chunks.

This is retrieved from indexed source material and should preserve source identity/revision.

### Episodic Memory

Examples:

- Session SES-123 changed these repositories;
- a review discovered a race condition;
- a human instructed the agent not to modify a public endpoint;
- a deployment failed because of a missing migration.

The canonical source remains the event ledger, PRs, questions, and artifacts.

Episodic memory may provide a retrieval-friendly summary/index of those durable records.

### Semantic Memory

Examples:

```text
"The billing API uses cursor pagination; do not introduce offset pagination."

"Exports must use the user's effective timezone."

"Repository `api` uses `make test-fast` for the normal pre-commit suite."
```

These are compact reusable facts with provenance.

### Procedural Memory

Examples:

```text
"To run the complete billing stack locally:
1. start postgres
2. run ./tooling/bootstrap.py
3. start api
4. start web"
```

Procedural memory is especially valuable for the self-building factory because agents repeatedly need environment and repository knowledge.

---

## 71. Memory tools exposed to agents

Every harness should receive the same factory-native memory API.

Minimum tool set:

```text
memory.search
memory.read
memory.write
memory.supersede
memory.feedback
```

### `memory.search`

Conceptual schema:

```json
{
  "query": "How do we run backend integration tests?",
  "scopes": [
    "project:factory",
    "repo:control-plane"
  ],
  "types": [
    "semantic",
    "procedural",
    "source"
  ],
  "limit": 10
}
```

Returns compact ranked results:

```json
{
  "results": [
    {
      "id": "MEM-42",
      "type": "procedural",
      "scope": "repo:control-plane",
      "content": "Run integration tests with ...",
      "score": 0.91,
      "confidence": 0.96,
      "provenance": ["PR-183", "SES-88"],
      "last_verified_at": "..."
    }
  ]
}
```

The search tool is deterministic after the query text is supplied. It does not require waking another LLM.

### `memory.read`

Fetches full content/provenance for one or more known memory IDs.

This keeps `memory.search` results compact.

### `memory.write`

Agents may propose a new memory:

```json
{
  "scope": "repo:runner",
  "type": "procedural",
  "content": "The runner reconciliation test must use a real Docker daemon.",
  "reason": "Discovered while fixing SES-221.",
  "provenance": [
    "session:SES-221",
    "event:EVT-..."
  ]
}
```

Important: an agent write creates a **candidate/versioned memory**, not an untraceable mutation of organizational truth.

### `memory.supersede`

Used when an existing memory is now known to be obsolete:

```json
{
  "memory_id": "MEM-13",
  "replacement": {
    "content": "..."
  },
  "reason": "Command changed in PR #812"
}
```

The old record remains auditable.

### `memory.feedback`

Lets an agent or human report:

```text
helpful
not_helpful
incorrect
stale
duplicate
```

This becomes useful for retrieval evaluation and curation.

---

## 72. Memory scopes and tenant isolation

Every memory is owned by one Organization.

Scopes then narrow visibility:

```text
organization
project
repository
epic
session
```

Example:

```text
ORG-1
├─ org memory
└─ Project Factory
   ├─ project memory
   ├─ repo:control-plane memory
   ├─ repo:runner memory
   └─ Session-specific memory
```

A Session may search:

```text
its Session scope
+ its Epic scope
+ its Project scope
+ relevant repository scopes
+ Organization scope
```

It may **never** retrieve memory from another Organization.

Project access rules also apply to memory search. A user who cannot access an invite-only Project cannot use semantic search to infer its contents.

### Memory table

Suggested schema:

```text
memories
- id
- organization_id
- project_id?
- repository_id?
- epic_id?
- session_id?
- type
- status
- title?
- content
- confidence
- sensitivity
- created_by_type
- created_by_id
- created_at
- updated_at
- last_verified_at
- expires_at?
- supersedes_id?
- embedding_model
- embedding
- search_document
- metadata JSONB
```

Provenance remains normalized:

```text
memory_provenance
- memory_id
- source_type
- source_id
- source_revision?
- excerpt_hash?
```

Feedback:

```text
memory_feedback
- memory_id
- actor
- rating
- reason
- created_at
```

Use tenant-aware indexes and RLS on all of these tables.

---

## 73. Semantic + lexical hybrid search

Use PostgreSQL as the initial memory retrieval engine.

Recommended stack:

```text
PostgreSQL full-text search
        +
pgvector
        =
hybrid retrieval
```

PostgreSQL has built-in full-text search and relevance ranking, while pgvector provides exact vector search plus approximate HNSW/IVFFlat indexes. pgvector explicitly supports combining vector retrieval with PostgreSQL full-text search for hybrid search. citeturn540696search0turn540696search1turn540696search3

### Why hybrid instead of vector-only

Semantic search is good for:

```text
"How do we usually authenticate preview environments?"
```

Lexical search is better when the user/agent knows exact identifiers:

```text
factory-runner
BILL-812
PAYMENT_TIMEOUT_SECONDS
terraform.apply
```

Engineering memory contains many identifiers, filenames, commands, issue IDs, and exact error strings, so vector-only retrieval would be a poor fit.

### Initial ranking

Run both searches with tenant/scope filters:

```text
semantic candidates
lexical candidates
        ↓
Reciprocal Rank Fusion
        ↓
freshness/confidence adjustment
        ↓
top N
```

Example scoring inputs:

```text
semantic rank
lexical rank
scope specificity
confidence
last_verified_at
feedback quality
status
```

Do not let freshness automatically override highly specific authoritative memories; treat these as independent ranking features.

### HNSW

For larger memory sets, use pgvector HNSW indexes.

pgvector documents HNSW as providing a better speed/recall tradeoff than IVFFlat at the cost of slower index construction and higher memory use. citeturn540696search1

Start with exact vector search if the corpus is small. Add HNSW when measurements justify it.

---

## 74. Embedding provider

Do not couple the memory store to one embedding vendor.

```ts
interface EmbeddingProvider {
  modelId(): string;

  embedDocuments(texts: string[]): Promise<number[][]>;

  embedQuery(text: string): Promise<number[]>;
}
```

Implementations may be:

```text
LocalEmbeddingProvider
HostedEmbeddingProvider
```

### Default preference: local embeddings

Because memory search happens frequently and should not require expensive reasoning-model calls, prefer a small local embedding service when operationally practical.

The embedding service can run:

- on the control-plane host;
- as a small internal service;
- on worker infrastructure.

Advantages:

- no reasoning-agent wake;
- predictable marginal cost;
- low latency;
- organizational text does not need to leave infrastructure for embedding.

Keep the provider interface so quality can be benchmarked against hosted embedding models.

### Embedding versioning

Never silently change embedding models in place.

Store:

```text
embedding_model
embedding_dimensions
embedding_version
```

When changing model:

```text
dual-index/re-embed in background
      ↓
switch active retrieval version
      ↓
remove old embeddings later
```

Memory remains available while re-indexing.

---

## 75. Memory writes and curation

Do not run an LLM after every Session merely to ask, "What should I remember?"

Use multiple paths.

### Explicit agent memory write

When the agent discovers something obviously reusable:

```text
memory.write(...)
```

No additional model call is needed.

### Human corrections

Human answers/steering are high-value candidate sources.

Example:

```text
Human:
Never run `terraform apply` directly for this project.
Use deployment.py.
```

The system can deterministically mark this as a potential durable instruction and ask the active agent whether it should be stored, or expose a one-click:

```text
[Save as project memory]
```

### Structured outcome extraction

Many facts require no LLM:

```text
successful command
repository mapping
test command
service health URL
runtime requirement
```

They can be derived from structured Run events.

### Batch curation

When consolidation actually needs judgment, perform it in batches:

```text
many candidates
      ↓
deduplicate
      ↓
one curator agent wake
      ↓
promote / merge / supersede / reject
```

This is cheaper than one curation call per Session.

### High-impact memories

Organization-wide or policy-like memories can require human approval.

Example:

```yaml
memory_policy:
  session:
    auto_promote: true

  repository:
    auto_promote_below_risk: true

  project:
    require_confidence: 0.9

  organization:
    require_human_approval: true
```

---

## 76. Memory lifecycle

States:

```text
CANDIDATE
ACTIVE
STALE
SUPERSEDED
REJECTED
```

### Candidate

Proposed but not yet trusted enough for ordinary retrieval.

### Active

Eligible for normal retrieval.

### Stale

May still be returned, but explicitly labeled.

Useful when:

```text
source revision changed
repository config changed
TTL expired
contradictory evidence appeared
```

### Superseded

Replaced by another memory.

Keep for audit/provenance, but exclude from ordinary retrieval.

### Rejected

Candidate determined not useful/correct.

Useful for preventing repeated bad proposals.

---

## 77. Memory validation and provenance

Every durable semantic/procedural memory should answer:

```text
Where did this come from?
When was it last true?
Who/what wrote it?
What evidence supports it?
What superseded it?
```

Example UI:

```text
"Use `make integration-test` for API integration tests."

Scope: repo/api
Confidence: High
Last verified: 2 days ago

Evidence:
- PR #812
- Session SES-991
- human answer by Alice

[Mark stale] [Edit] [View sources]
```

Never make the memory system an opaque vector database whose results cannot be audited.

---

## 78. Source knowledge indexing

Semantic memory and source search can share retrieval infrastructure while remaining different logical record types.

Index:

```text
Markdown/docs
ADRs
runbooks
selected repository source chunks
API schemas
repository metadata
```

Each source chunk records:

```text
organization_id
project_id
repository_id
path/source URL
git SHA / source revision
chunk identity
content
tsvector
embedding
```

For source code, begin conservatively.

Do not embed every generated/vendor file.

Use repository rules:

```yaml
context:
  include:
    - src/**
    - docs/**
    - AGENTS.md
    - README.md

  exclude:
    - node_modules/**
    - vendor/**
    - dist/**
    - generated/**
```

Code search tools still remain primary for precise source navigation. Semantic source retrieval is a complement, not a replacement for grep/LSP/code intelligence.

---

## 79. Automatic memory context packs

Do not preload all memories into every agent prompt.

At Session/agent start, create a small **Memory Context Pack**.

Inputs:

```text
Session request
Project
selected repositories
current phase
changed paths when known
agent role
```

Perform hybrid retrieval and return only a bounded set.

Example:

```text
Memory Context Pack
- 5 project/repository memories
- 3 procedural memories
- relevant source docs
```

The agent can then call `memory.search` if it needs more.

This minimizes context cost and reduces stale/irrelevant instructions.

### Delta-based resume

On a resumed agent execution, do not resend the full memory pack unless necessary.

Send:

```text
previous memory IDs
new/changed relevant memories
human answers
workflow delta
```

---

## 80. Memory for self-building

For the software factory's own Project, prioritize memories such as:

```text
how to run each service locally
how to run Python E2E tests
runner protocol conventions
database migration rules
worker reconciliation invariants
GitHub webhook quirks encountered
artifact upload semantics
known Docker edge cases
review policies
deployment procedure
common failure signatures
```

After every successful fix to the factory itself, ask:

```text
Did this reveal reusable knowledge?
```

The active agent can write that memory directly when appropriate.

This creates a compounding loop:

```text
factory builds feature
      ↓
learns reusable detail
      ↓
stores memory with provenance
      ↓
future factory Session retrieves it
      ↓
less investigation / fewer agent wakes
      ↓
factory improves faster
```

Memory usefulness should itself be measured.

Metrics:

```text
memory searches per Session
retrieved memories used
memory feedback rate
human corrections after memory use
stale-memory rate
Sessions benefiting from prior memory
time/cost saved on repeated task categories
```

---

## 81. Bootstrap order optimized for self-hosting

To reach "the factory builds the factory" quickly, prioritize:

### Bootstrap 1 — execution kernel

```text
Project/Session API
local factory-runner
Docker runtime
warm repository cache
OpenCodeHarness
events/logs
basic web Session UI
```

Goal:

```text
Factory can modify its own repository.
```

### Bootstrap 2 — memory + artifacts

```text
ArtifactStore/S3-compatible local storage
publish_artifact
memory tables
hybrid search
memory.search/read/write
source context indexing
```

Goal:

```text
Factory can remember what it learns while modifying itself.
```

### Bootstrap 3 — human control

```text
ask_user
steer
pause
abort
resume
Session conversation
```

Goal:

```text
You can safely supervise self-building Sessions.
```

### Bootstrap 4 — GitHub loop

```text
GitHub App
push
open PR
webhooks
review/fix
CI state
auto-merge policy
```

Goal:

```text
Factory can take its own changes through review/merge.
```

### Bootstrap 5 — review/test quality

```text
reviewers
simplifier
Python 3.14 E2E
browser QA
cost accounting
```

### Bootstrap 6 — remote execution + Slack/Jira

```text
warm worker pool
VM NodeProvisioner
Slack
Jira
```

This ordering intentionally delays some integration work until the factory itself can assist in building those integrations.

---

## 82. Full agent introspection as a product invariant

Agent execution must be fully inspectable for debugging, auditing, performance analysis, and incident investigation.

The system should make it possible to answer:

```text
What was the agent asked to do?
What context did it receive?
Which memories/documents were retrieved?
Which tools did it call?
Which commands ran?
What did stdout/stderr contain?
Which files changed?
Which Git commits/diffs were produced?
Which subagents were started?
Which policy decisions allowed/denied actions?
Which human messages changed direction?
How long did each operation take?
How much did each model invocation cost?
What external events arrived while it was running?
Why did the workflow move to the next state?
```

Do not depend on hidden chain-of-thought for debuggability.

Instead, capture **observable execution facts** plus explicit structured reasoning artifacts at important boundaries:

```text
plan
decision summary
review findings
test diagnosis
next-action rationale
completion summary
```

These can be required outputs from agents without storing private model reasoning.

---

## 83. Execution trace model

Every meaningful operation belongs to a trace hierarchy.

```text
Organization
  └─ Project
      └─ Session
          └─ Run
              └─ Agent Execution
                  ├─ Model Turn
                  ├─ Tool Call
                  │   └─ Command / external API operation
                  └─ Child Agent Execution
```

Each record carries:

```text
trace_id
span_id
parent_span_id
correlation_id
causation_id
organization_id
project_id
session_id
run_id
agent_execution_id
started_at
completed_at
status
```

This makes it possible to build a full waterfall/timeline of one Session.

---

## 84. Agent execution record

Suggested model:

```text
AgentExecution
- id
- organization_id
- project_id
- session_id
- run_id

- role
- harness
- harness_session_id
- model
- parent_agent_execution_id?

- status
- started_at
- completed_at

- context_envelope_id
- agent_definition_version
- policy_version

- input_summary
- completion_summary
- error_summary?

- input_tokens
- output_tokens
- cached_tokens
- cost_usd

- worker_id
- runtime_instance_id
```

The harness-specific raw identifier is retained, but all UI/debug behavior uses the normalized internal execution ID.

---

## 85. Context snapshot introspection

Every agent execution must point to the exact immutable `ContextEnvelope` supplied to it.

Store:

```text
ContextEnvelope
- id
- organization_id
- project_id
- session_id
- run_id
- agent_execution_id

- agent_definition_version
- project_context_version
- policy_version
- session_spec_version

- repository_shas
- memory_refs
- source_document_refs
- directive_refs
- finding_refs
- question_refs

- rendered_system_prompt_hash
- rendered_initial_context_hash
- created_at
```

Retain the rendered prompt/context content according to tenant retention policy.

The UI should expose:

```text
Context
├─ System instructions
├─ Agent role instructions
├─ Session goal
├─ Human directives
├─ Policies
├─ Memories retrieved
├─ Documents retrieved
├─ Repository revisions
└─ Current findings/errors
```

This is critical for answering:

```text
"Why did the agent think X was true?"
```

---

## 86. Retrieval introspection

Every automatic or explicit retrieval call should record:

```text
query
scope
filters
candidate count
ranking method
result IDs
scores
selected results
latency
```

For hybrid memory/context search:

```text
semantic rank
lexical rank
fusion score
scope boost
confidence/freshness modifiers
final rank
```

The UI can show:

```text
memory.search("runner restart reconciliation")

1. MEM-812  0.91
2. MEM-109  0.83
3. ADR-014  0.78
```

This lets developers distinguish:

```text
bad agent judgment
vs.
bad retrieval
vs.
bad memory
vs.
missing context
```

---

## 87. Model-turn observability

Normalize harness events into:

```text
ModelTurnStarted
ModelTurnCompleted
ModelTurnFailed
```

Capture observable model I/O permitted by the harness:

```text
input messages
assistant-visible response
structured outputs
model
sampling/config metadata where exposed
token usage
latency
provider/session IDs
```

Do not make hidden internal chain-of-thought a platform dependency.

If a harness exposes a user-visible reasoning summary, treat it as an ordinary artifact/message and label it clearly as such.

---

## 88. Tool-call introspection

Every tool call is recorded before and after execution.

```text
ToolCall
- id
- agent_execution_id
- tool_name
- tool_version
- requested_at
- started_at
- completed_at
- status
- input
- output_summary
- error
- policy_evaluation_id
- side_effect_class
```

Sensitive arguments are redacted before durable storage.

Examples:

```text
repo.search
memory.search
context.read
ask_user
publish_artifact
shell.exec
github.push
github.open_pr
request_step
```

### Side-effect classification

Tools declare:

```text
READ_ONLY
WORKSPACE_WRITE
EXTERNAL_WRITE
PRIVILEGED
DESTRUCTIVE
```

This makes debugging and policy review significantly easier.

---

## 89. Shell and process introspection

Commands launched inside a Runtime Instance should be represented explicitly.

```text
ProcessExecution
- id
- tool_call_id
- argv
- working_directory
- environment_keys
- started_at
- completed_at
- exit_code
- timeout
- stdout_artifact_id?
- stderr_artifact_id?
```

Do not persist raw secret environment values.

For short output, keep an indexed preview.

For large output:

```text
preview in PostgreSQL
full stdout/stderr in ArtifactStore
```

The web UI should support live tailing while the command is running.

---

## 90. Filesystem and Git change introspection

Track meaningful workspace changes at agent/tool boundaries.

At minimum record:

```text
repository
base SHA
HEAD SHA
changed files
insertions/deletions
untracked files
commits produced
```

At important phase boundaries create a diff snapshot:

```text
after investigation      [usually no diff]
after implementation
after each fix loop
after simplifier
before PR
after PR review fixes
```

Diffs can be reconstructed from Git where possible rather than duplicating every version as an artifact.

The UI should allow:

```text
Execution A changed:
  src/runner/reconcile.go
  tests/test_runner.py

[View diff]
```

### File-change event stream

Optional high-detail mode:

```text
file.created
file.modified
file.deleted
```

Do not enable expensive filesystem watching globally until needed. Git snapshots are usually sufficient and cheaper.

---

## 91. Subagent tree

Subagent relationships must be first-class.

Example:

```text
Orchestrator
├─ Investigator
├─ Implementer
│  ├─ Backend specialist
│  └─ Frontend specialist
├─ Reviewer
│  ├─ Security reviewer
│  └─ Database reviewer
└─ QA browser agent
```

For each node display:

```text
status
duration
cost
model
current/last action
tool calls
files changed
children
```

Clicking a child opens its complete trace.

This is one of the most useful debugging views for agentic systems because many failures are delegation failures rather than coding failures.

---

## 92. Workflow introspection

Record every macro workflow transition explicitly:

```text
workflow.transitioned
```

Payload:

```json
{
  "from": "IMPLEMENTING",
  "to": "REVIEWING",
  "reason": "implementer completed and deterministic checks passed",
  "trigger_event_ids": ["EVT-...", "EVT-..."],
  "policy_evaluation_id": "POL-..."
}
```

The state machine should never appear to move "magically."

The UI should show:

```text
22:14 IMPLEMENTING → REVIEWING
Reason:
- implementer completed
- lint passed
- unit tests passed
```

---

## 93. Policy introspection

Every gated action stores its policy evaluation.

```text
PolicyEvaluation
- id
- organization_id
- project_id
- session_id
- action
- decision
- matched_rules
- inputs
- evaluated_at
```

Example UI:

```text
github.auto_merge

ALLOWED

Matched:
✓ project.merge.enabled = true
✓ required CI green
✓ 2/2 approvals
✓ 0 blocking findings
✓ trusted internal Session
```

Denied actions are equally important:

```text
terraform.apply

DENIED

Reason:
production requires human approval
```

---

## 94. Human-interaction introspection

Treat every human interaction as part of the trace.

Capture:

```text
question asked
where it was delivered
who was allowed to answer
who answered
answer
wait duration

steering message
origin: web/slack
actor
received_at
when incorporated by agent

pause
resume
abort
approval
policy exception
```

For steering, record the execution boundary:

```text
Directive received at 12:03
Applied to Agent Execution AE-83 at 12:04
```

This makes it possible to determine whether an agent acted on a human instruction.

---

## 95. External integration introspection

Inbound provider events are retained as normalized events with links to raw webhook deliveries.

Examples:

```text
Slack message
GitHub PR review
GitHub check completion
Jira issue event
```

UI:

```text
GitHub review received
  reviewer: Alice
  state: changes_requested
  comments: 3

→ caused PR_FIXING
→ woke orchestrator AE-92
```

This gives a complete causal chain from external event to agent behavior.

---

## 96. Artifact provenance

Every artifact shows:

```text
which Session
which Run
which Agent Execution
which tool
which source file
which Runtime Instance
which event
```

Example:

```text
qa-video.webm

Produced by:
QA Browser AE-119
Tool call TC-448
Run RUN-23
Worker worker-eu-03
```

For generated Markdown:

```text
architecture-plan.md

Produced by:
Orchestrator AE-91
publish_artifact TC-392
SHA-256 ...
```

---

## 97. Live inspection protocol

The web UI should update live from the persisted event stream.

Recommended pattern:

```text
runner / integrations / control plane
          ↓
     append event
          ↓
transaction commits
          ↓
event fanout
          ↓
SSE / WebSocket
          ↓
web UI
```

The database/event ledger remains authoritative.

The UI stream is not the source of truth.

If the browser reconnects:

```text
GET events after cursor X
```

then resumes live streaming.

---

## 98. Session debug UI

Every Session should expose a primary **Live Execution** view. This is not a hidden developer-only debug screen; it is the normal way to inspect what autonomous work is doing.

A secondary deep-debug/raw mode may expose lower-level details, but the same execution trace powers both.

Suggested layout:

```text
Session
├─ Conversation
├─ Live Execution
│  ├─ Timeline
│  ├─ Agent tree
│  ├─ Model requests
│  ├─ Tool calls
│  ├─ Processes
│  └─ Live metrics
├─ Context
├─ Git / changes
├─ Tests / QA
├─ PRs
├─ Artifacts
├─ Costs
└─ Debug / raw events
```

### Timeline

A zoomable chronological waterfall:

```text
22:01 Session message
22:01 Context assembled
22:01 Orchestrator started
22:02 memory.search
22:02 Investigator spawned
22:04 Investigator completed
22:04 Implementer started
22:06 pytest
22:07 human question
22:12 human answer
22:12 Implementer resumed
...
```

Filters:

```text
agents
tools
commands
git
human
workflow
github
slack
jira
cost
errors
```

---

## 99. Debug bundle export

Support:

```bash
factory session debug-bundle SES-123
```

or a web button:

```text
Download debug bundle
```

Bundle manifest:

```text
session.json
run.json
events.ndjson
agent-executions.json
context-envelopes/
retrievals.json
tool-calls.json
policy-evaluations.json
git-state.json
costs.json
artifact-manifest.json
logs/
```

Do not automatically include secret values or sensitive artifact contents.

This is useful for:

- reproducing bugs;
- filing factory issues;
- offline analysis;
- regression tests.

---

## 100. Redaction and observability security

Full introspection does **not** mean indiscriminately logging secrets.

Create a central redaction layer.

Redact:

```text
authorization headers
cookies
API keys
passwords
secret environment values
signed artifact URLs
private tokens
credential helper output
```

Tools declare sensitive input/output fields in their schema.

Where possible, store:

```text
secret reference ID
```

rather than the value.

Example:

```text
GITHUB_TOKEN = secret://run/RUN-123/github-installation
```

### Tenant boundary

All debugging/introspection tables are tenant-owned and protected by the same organization/project access rules as the Session itself.

Raw webhook payloads and command output are not exempt from tenant isolation.

---

## 101. Retention tiers for introspection

High-detail traces can become large.

Define retention classes.

Example:

```yaml
observability:
  events:
    retention: long

  agent_messages:
    retention: long

  context_envelopes:
    retention: long

  tool_call_metadata:
    retention: long

  command_output:
    retention: 30d
    promote_on_failure: 180d

  full_debug_artifacts:
    retention: 30d

  failed_sessions:
    extended_retention: true
```

Artifacts explicitly marked valuable remain under their normal artifact retention policy.

Keep metadata longer than large raw blobs.

---

## 102. Replay and reproduction

A long-term goal should be partial execution replay.

Because we store:

```text
ContextEnvelope
agent definition version
policy version
repo SHAs
runtime image digest
tool inputs/results
event sequence
```

we can reproduce many classes of failure.

Two modes:

### Read-only replay

Render the Session exactly as it happened from stored events.

No external actions.

### Fork from execution point

Create a new Session/Run from a historical checkpoint:

```text
Fork from AE-83
```

This should:

```text
restore repository state
restore Session workspace checkpoint
reuse context snapshot
apply optional new directive
start new Run
```

This is extremely useful for debugging agent behavior and testing prompt/policy changes.

---

## 103. Introspection-driven evaluation

Once execution is observable, the same data becomes an evaluation dataset.

Examples:

```text
Which agent roles generate most failed tool calls?
Which memories correlate with successful completions?
Which context documents are never useful?
Which reviewer findings recur?
Which harness/model has lower fix-loop counts?
Where do Sessions spend the most wall time?
Which commands commonly fail first?
How often does a human correct an agent after a particular memory is retrieved?
```

This is how the factory should improve itself.

The introspection system is therefore not only for debugging. It is the raw dataset for:

```text
harness routing
prompt improvements
memory quality
policy tuning
tool reliability
cost optimization
workflow optimization
```

---

## 104. Introspection acceptance criteria

A first self-hosting-capable version should satisfy:

- Every agent execution is visible in a parent/child tree.
- Every execution points to its exact context envelope.
- Every memory/source retrieval is inspectable.
- Every tool call has input metadata, status, duration, and result summary.
- Every shell command has working directory, exit status, and retained output.
- Git state can be inspected before/after implementation phases.
- Every workflow transition records its reason and causal events.
- Every policy gate records why it allowed or denied the action.
- Human questions and steering can be traced to the execution that consumed them.
- GitHub/Slack/Jira events can be traced to resulting workflow actions.
- Costs can be attributed to individual agent executions.
- Live activity appears in the UI without relying on ephemeral streams as the source of truth.
- A completed or failed Session remains debuggable after its container and worker resources are gone.
- A debug bundle can be exported without secrets.

---

## 105. Live Session execution UI

The Session UI must show agent activity **while it is happening**, not only after execution completes.

The primary live view is a chronological execution stream.

Example:

```text
Session: Add runner reconciliation                    RUNNING

Orchestrator · GPT-5.6 Sol                     $1.82
────────────────────────────────────────────────────────

23:04:01  MODEL REQUEST STARTED
          model: gpt-5.6-sol
          input: 18,421 tokens
          context: CTX-812

23:04:02  FIRST TOKEN
          TTFT: 842 ms

23:04:05  TOOL  memory.search                         RUNNING
          input:
            query: "runner reconciliation after daemon restart"
            scopes: [project:factory, repo:runner]

23:04:05  TOOL  memory.search                         DONE
          143 ms
          output: 5 results

23:04:06  TOOL  shell.exec                            RUNNING
          cwd: /workspace/repos/runner
          argv: ["go", "test", "./..."]

23:04:08  TOOL  shell.exec                            DONE
          2.14 s · exit 1
          stdout/stderr [expand]

23:04:09  SUBAGENT reviewer                           STARTED

23:04:12  MODEL REQUEST COMPLETED
          total: 10.9 s
          TTFT: 842 ms
          input: 18,421
          cached: 11,203
          output: 1,844
          output rate: 184 tok/s
          cost: $0.31
```

Rows update in place as operations complete.

### Expandable detail

Every execution row is expandable.

#### Model request

Show:

```text
agent execution
harness
provider
model
request ID
start/end timestamps
request latency
time to first token
generation duration
input tokens
cached input tokens
output tokens
total tokens
tokens/second
cost
context envelope
visible input messages
visible output
error/retry metadata
```

Do not attempt to expose hidden chain-of-thought. If the harness provides an explicit user-visible reasoning summary, show that separately and label it accurately.

#### Tool call

Show:

```text
tool name + version
agent
input arguments
policy decision
queued timestamp
started timestamp
completed timestamp
queue latency
execution duration
output
error
side-effect classification
related artifacts
child process executions
```

Sensitive fields are redacted by schema before reaching the browser.

#### Shell/process execution

Show:

```text
command
cwd
worker
runtime/container
start/end
wall time
exit code
stdout
stderr
resource usage where available
```

Long stdout/stderr streams are live-tailed and archived to object storage rather than stored entirely in PostgreSQL.

---

## 106. Real-time event transport

Activity should reach the browser through a durable event pipeline.

```text
Harness / Runner / Control Plane / Integrations
                     ↓
              normalized event
                     ↓
          append to durable event log
                     ↓
             live event fanout
                     ↓
              SSE / WebSocket
                     ↓
                 Session UI
```

Preferred initial transport: **Server-Sent Events** unless bidirectional socket semantics are needed for a specific feature.

User commands still use normal HTTP APIs:

```text
POST /sessions/{id}/pause
POST /sessions/{id}/steer
POST /questions/{id}/answer
```

and the resulting state is reflected back through the event stream.

### Reconnection

Every live event has a monotonic Session/event cursor.

On browser reconnect:

```text
GET /sessions/{id}/events?after=<cursor>
```

then reopen the live stream.

No activity is lost because the websocket/SSE connection dropped.

### High-volume streams

Do not write one PostgreSQL row per stdout byte or model token.

Use two channels:

```text
DURABLE SEMANTIC EVENTS
  model request started/completed
  tool started/completed
  subagent started/completed
  workflow transition
  Git changes
  human interaction
  cost sample

EPHEMERAL/CHUNKED STREAM DATA
  model visible-output deltas
  stdout/stderr chunks
  browser/video streams
```

For live display, stream deltas immediately.

For durable retention:

- store final model-visible messages;
- store tool inputs/final outputs;
- store bounded stdout/stderr previews;
- put large logs in ArtifactStore;
- optionally chunk high-volume logs in object storage.

This prevents the event table from becoming a token/log byte store.

---

## 107. Model request timing metrics

Every model request should expose a consistent timing breakdown.

Record:

```text
model_request_queued_at
model_request_started_at
provider_request_sent_at
first_output_at
provider_request_completed_at
model_request_completed_at
```

Derived metrics:

```text
model_queue_latency_ms
  = started - queued

model_request_overhead_ms
  = provider_request_sent - started

model_time_to_first_token_ms
  = first_output - provider_request_sent

model_generation_duration_ms
  = provider_request_completed - first_output

model_provider_duration_ms
  = provider_request_completed - provider_request_sent

model_total_duration_ms
  = model_request_completed - queued
```

When a provider/harness does not expose streaming/first-token timing, `time_to_first_token` is null rather than guessed.

### Token metrics

Record when available:

```text
model_input_tokens
model_cached_input_tokens
model_uncached_input_tokens
model_output_tokens
model_total_tokens
model_output_tokens_per_second
model_cost_usd
```

Also track:

```text
orchestrator_wake_count
model_request_count
model_retry_count
```

per Session/Run/phase.

---

## 108. Tool execution timing metrics

Tool timing is a first-class metric family.

Every tool invocation records:

```text
tool_requested_at
tool_queued_at
tool_started_at
tool_completed_at
```

Derived:

```text
tool_queue_duration_ms
tool_execution_duration_ms
tool_total_duration_ms
```

Dimensions:

```text
organization
project
session
run
phase
agent role
harness
tool name
tool version
worker
runtime type
status
```

Examples:

```text
memory.search                  41 ms
context.search                 73 ms
repo.search                    18 ms
shell.exec / go test         2.14 s
publish_artifact              381 ms
github.open_pr                612 ms
ask_user                  18m 42s   [human wait; separate category]
```

Human wait duration should **not** be mixed into ordinary tool execution percentiles.

If `ask_user` creates an asynchronous question:

```text
tool API execution: 12 ms
human wait: 18m 42s
```

record both independently.

---

## 109. Local execution timing

For commands/processes on workers, capture:

```text
process_spawn_latency_ms
process_execution_duration_ms
process_exit_code

container_create_duration_ms
container_start_duration_ms

workspace_materialize_duration_ms
repo_cache_refresh_duration_ms
repo_materialize_duration_ms

artifact_hash_duration_ms
artifact_upload_duration_ms
```

These are important because Session responsiveness depends on much more than LLM latency.

For the product-level startup target, decompose:

```text
Session asks for compute
   ↓
scheduler selection
   ↓
workspace acquisition
   ↓
repo materialization
   ↓
container creation/start
   ↓
harness initialization
   ↓
first model request
   ↓
first visible agent activity
```

Metrics:

```text
session_to_worker_assignment_ms
worker_to_workspace_ready_ms
workspace_to_container_ready_ms
container_to_harness_ready_ms
harness_to_first_model_request_ms
session_to_first_agent_activity_ms
```

This tells us exactly why a Session feels slow.

---

## 110. Metrics architecture

Use the event/span stream as the source for product metrics, with OpenTelemetry-compatible traces and metrics where useful.

The domain event log remains the durable audit record.

Operational metrics should be exported as histograms/counters/gauges for aggregation.

Important histogram families:

```text
factory_model_request_duration_seconds
factory_model_ttft_seconds
factory_tool_execution_duration_seconds
factory_tool_queue_duration_seconds
factory_process_execution_duration_seconds
factory_repo_materialization_duration_seconds
factory_container_start_duration_seconds
factory_artifact_upload_duration_seconds
factory_human_wait_duration_seconds
factory_phase_duration_seconds
factory_session_lead_time_seconds
```

Counters:

```text
factory_model_input_tokens_total
factory_model_cached_input_tokens_total
factory_model_output_tokens_total
factory_model_requests_total
factory_tool_calls_total
factory_tool_failures_total
factory_agent_executions_total
factory_orchestrator_wakes_total
```

Cost:

```text
factory_model_cost_usd_total
```

Do not put Session IDs into exported metrics labels because that creates unbounded cardinality.

Session-level metrics belong in PostgreSQL/analytics projections.

Use bounded metric labels such as:

```text
organization tier
project class if bounded
model
agent role
tool name
phase
status
harness
provisioner
```

For a specific Session, the UI queries its event/trace data directly.

---

## 111. Live metrics sidebar

While a Session runs, show a compact live summary:

```text
RUNNING

Elapsed                    18m 42s
Active model time           4m 13s
Tool execution time         8m 07s
Human wait                  3m 01s
External wait               1m 44s

Model requests                  9
Orchestrator wakes              3
Subagents                       4
Tool calls                     72

Input tokens               184,221
Cached input              133,902
Output tokens               18,442
Cost                         $6.18
```

These numbers update as events arrive.

Also show current activity:

```text
Current
Reviewer
  └─ shell.exec
     └─ python3.14 -m pytest e2e/
        14.2 s...
```

---

## 112. Agent tree + live current operation

The agent tree should be live, not a static post-run visualization.

Example:

```text
● Orchestrator · GPT-5.6 Sol                     $2.11
│  waiting for children
│
├─ ✓ Investigator · GPT-5.6 Sol                 $0.44
│
├─ ● Implementer · GPT-5.6 Sol                  $1.73
│   └─ shell.exec: go test ./...                 8.2s
│
└─ ● Reviewer · GPT-5.6 Sol                     $0.62
    └─ context.search                            42ms
```

Clicking an agent filters the Session timeline to that execution.

---

## 113. Tool input/output rendering

Tools should provide UI schemas in addition to execution schemas.

Example tool manifest:

```yaml
id: memory.search

input_schema: ...
output_schema: ...

ui:
  summary:
    input: "{{query}}"
    output: "{{results.length}} results"

  sensitive_fields:
    - auth_token
```

This allows the UI to render useful cards rather than raw JSON everywhere.

Users can switch to:

```text
Pretty
Raw JSON
```

For `shell.exec`, render a terminal-like panel.

For `memory.search`, render ranked results.

For `github.open_pr`, render the PR identity/link/status.

For `publish_artifact`, render the resulting Artifact.

---

## 114. Requests, retries, and failures

One logical model action may involve multiple provider attempts.

Represent both levels:

```text
ModelAction
  ├─ ProviderRequest attempt 1 → timeout
  └─ ProviderRequest attempt 2 → success
```

This avoids hiding retries inside a single misleading latency number.

Metrics include:

```text
logical_model_action_duration
provider_request_duration per attempt
retry_backoff_duration
attempt_count
```

Likewise, a tool may retry external APIs.

The UI should make retries visible:

```text
github.open_pr
  attempt 1 — 503 — 412 ms
  retry backoff — 1.0 s
  attempt 2 — success — 601 ms
```

---

## 115. Live request and tool-output retention

For each model request retain:

- model/provider;
- start/end;
- timing breakdown;
- usage/cost;
- visible request messages where retention policy permits;
- visible final response;
- explicit structured outputs.

For each tool invocation retain:

- tool/version;
- arguments after redaction;
- output after redaction;
- timings;
- error;
- policy evaluation;
- artifacts/processes created.

For large values:

```text
PostgreSQL:
  metadata + first/last bounded preview

ArtifactStore:
  full retained payload/log
```

The Session UI retrieves the full payload lazily when expanded.

This keeps the normal timeline fast even for very verbose commands.

---

## 116. Real-time introspection acceptance criteria

The first dogfoodable version must satisfy:

- Opening a running Session shows activity without refreshing.
- A model request appears as soon as it starts.
- The request shows its model and agent role.
- Time-to-first-token appears when measurable.
- Token counts and cost update when the request completes.
- Tool calls appear before execution completes.
- Tool inputs are inspectable immediately.
- Tool output appears/streams as it becomes available where supported.
- Local shell stdout/stderr can be tailed live.
- Tool execution duration appears on completion.
- Subagents appear in the tree immediately when spawned.
- Current agent/tool activity is visible.
- Human wait and external wait are distinguished from compute time.
- Model latency and tool latency are persisted as metrics.
- Session-level token/cost/timing totals update in real time.
- All live information remains inspectable after the Run/container is gone.
- Sensitive tool/request fields are redacted before browser delivery and durable persistence.

---

## 117. Web-first, desktop-ready UI architecture

The product should launch as a web application.

Do **not** build or require a desktop shell for v1.

However, structure the frontend so it can later be packaged as a distributable desktop application without forking the UI or rewriting product logic.

Target model:

```text
                         Factory UI
                            │
                 ┌──────────┴──────────┐
                 │                     │
             Web browser          Desktop shell
                                      │
                              Tauri or Electron
```

The same frontend application, routes, components, API client, Session views, Kanban, and live execution UI are used in both environments.

### Frontend must depend on product APIs, not its host

The UI talks to:

```text
Control Plane HTTP API
SSE/live event API
Artifact API
Authentication API
```

It must not assume that it has:

```text
filesystem access
Node.js access
Rust access
local Docker access
shell access
native notifications
```

Those are optional host capabilities.

This means the ordinary browser build remains fully functional.

---

## 118. Host capability bridge

Create a tiny abstraction from the beginning:

```ts
interface AppHost {
  kind: "web" | "desktop";

  notifications?: NotificationHost;
  filesystem?: FilesystemHost;
  shell?: ShellHost;
  updater?: UpdateHost;
  deepLinks?: DeepLinkHost;
  localRunner?: LocalRunnerHost;
}
```

Browser implementation:

```text
WebAppHost
```

Desktop implementation later:

```text
TauriAppHost
```

or:

```text
ElectronAppHost
```

Ordinary application components do not import Tauri/Electron APIs directly.

Example:

```ts
const host = useAppHost();

if (host.notifications) {
  await host.notifications.show(...);
}
```

This prevents desktop concerns from spreading through the frontend.

---

## 119. Desktop-enhanced capabilities

The desktop application can add capabilities that the browser version cannot provide as cleanly.

Potential features:

```text
native desktop notifications
system tray
global keyboard shortcuts
OS file dialogs
open artifact in local editor
drag local files into a Session
deep links:
  factory://session/SES-123
background connection / badge count
automatic app updates
credential storage in OS keychain
launch/control local factory-runner
detect local Docker availability
open repository in IDE
copy/download large artifacts directly to disk
```

None of these should be required for core Session functionality.

---

## 120. Local provisioner + desktop app

A future desktop app is particularly useful for the Local Provisioner.

Possible experience:

```text
Factory Desktop
      │
      ├─ Factory UI
      │
      └─ local integration
             ↓
       factory-runner
             ↓
          Docker
```

The desktop shell does **not** need to run work itself.

It can:

- discover whether `factory-runner` is installed/running;
- display its health;
- start/stop it where permitted;
- help install it;
- open local logs;
- direct Sessions to the local execution provisioner.

Keep the worker daemon an independent service/binary. Do not embed critical execution state exclusively inside the GUI process.

If the desktop UI crashes or upgrades, active Runs must continue.

---

## 121. Frontend packaging constraints

To preserve desktop portability, prefer a client-rendered application or static-buildable frontend.

The UI deployment should produce browser assets that can be:

```text
served by the web application
or
bundled into a desktop webview
```

Keep server-side business logic in the Bun control plane rather than relying on frontend-framework server actions that exist only in the web deployment environment.

The desktop shell should still talk to the normal remote control-plane API.

Authentication and routing must support both:

```text
https://factory.example.com/...
factory://...
```

---

## 122. Desktop shell choice

Do not lock this decision for v1.

Both leading approaches fit the architecture.

### Tauri

Strong candidate if priorities are:

```text
small application distribution
lower idle memory
native webview
Rust-based privileged bridge
tight capability permissions
```

Tauri 2 supports existing web frontends and treats the frontend essentially as static HTML/JavaScript/CSS served inside the system webview.

Because the project may already use Rust for infrastructure components, adding a small Rust desktop bridge later is reasonable.

### Electron

Strong candidate if priorities are:

```text
Chromium behavior identical across supported OSes
very mature desktop ecosystem
complex browser/devtools integrations
maximum consistency with a browser-like product
```

Electron bundles Chromium and Node.js and uses a separate main/renderer process model.

### Initial recommendation

Build the UI to support either.

When desktop packaging becomes a real milestone, prototype both shells around the production web frontend.

For this product, **Tauri would be the initial candidate** because the desktop process should be a thin native shell rather than another large application runtime.

Choose Electron instead if testing shows that system-webview differences affect the complex live execution/terminal UI enough to matter.

---

## 123. Design-system implications

The web UI should feel app-like even before desktop packaging.

Use:

```text
persistent left navigation
keyboard navigation
command palette
resizable panes
virtualized execution timelines
dockable/expandable inspectors
responsive side panels
local optimistic UI state
offline/reconnect indicators
desktop-density layouts
```

Avoid designing the product like a sequence of traditional independent web pages.

Session navigation should feel instantaneous.

Example shell:

```text
┌───────────────────────────────────────────────────────────────┐
│ Project / Epic / Session                       Search   User   │
├───────────────┬───────────────────────────────────────────────┤
│ Projects      │                                               │
│               │       active workspace                       │
│ Board         │                                               │
│ Sessions      │                                               │
│ Deployments   │                                               │
│ Workers       │                                               │
│               │                                               │
├───────────────┴───────────────────────────────────────────────┤
│ connection · running agents · notifications                   │
└───────────────────────────────────────────────────────────────┘
```

The desktop wrapper should mostly disappear visually; the web app itself already behaves like an application.

---

## 124. Live Session UI portability

The existing real-time introspection architecture is naturally desktop-ready.

```text
Control Plane
    ↓
SSE / HTTP APIs
    ↓
Factory frontend
    ↓
browser OR desktop webview
```

No desktop-specific transport is required.

The live Session UI continues to show:

```text
model requests
tokens
cost
tool calls
tool inputs/outputs
command streams
latencies
subagents
workflow transitions
Git changes
questions
```

in exactly the same way.

Desktop-only native notifications can subscribe to the same Session event/state store:

```text
Session needs human input
PR ready to merge
Run failed
Session completed
```

---

## 125. Web and desktop release independence

The control plane, web frontend, desktop client, worker daemon, and work runtime must have independent versions.

```text
ControlPlaneVersion
WebFrontendVersion
DesktopClientVersion
RunnerDaemonVersion
WorkRuntimeVersion
```

The API should be backward compatible across a reasonable desktop-client window.

A user may not upgrade their desktop application immediately when the hosted backend deploys.

Expose:

```text
GET /api/version
```

and capability negotiation where required.

The desktop client can then detect:

```text
update available
client too old
feature unsupported
```

without breaking active Sessions.

---

## 126. Desktop readiness acceptance criteria for the web v1

Even before a desktop application exists:

- Core UI contains no direct Node/Tauri/Electron dependency.
- All product functionality uses control-plane APIs.
- Host-specific capabilities sit behind `AppHost`.
- Frontend can be emitted as static/client-side assets suitable for a webview.
- Routing can support a future deep-link adapter.
- Authentication logic can support a future desktop OAuth callback.
- Live Session SSE logic is transport-independent from browser page lifecycle.
- Large timelines/terminal output are virtualized and performant.
- Active Session state survives UI reload/reconnect.
- Closing the future desktop app would never terminate worker Runs.

// Package protocol holds the string constants shared with the control plane.
//
// These values are a cross-language contract: they are written to Postgres
// enums, validated by zod at the control plane's edge, and asserted on by the
// Python E2E suite. Spelling them inline at each call site means a rename on
// the TypeScript side fails at runtime with a 400 rather than at build time.
//
// Keep in sync with:
//
//	packages/domain/src/hierarchy.ts        (run status)
//	packages/domain/src/interfaces/execution.ts (runtime status)
//	packages/domain/src/events/types.ts     (event types)
package protocol

// Run status, as accepted by POST /v1/runner/runs/{id}/status.
const (
	RunStarting  = "starting"
	RunRunning   = "running"
	RunCompleted = "completed"
	RunFailed    = "failed"
	RunAborted   = "aborted"
)

// Runtime (container) status, as accepted by POST /v1/runner/runs/{id}/runtime.
const (
	RuntimeCreating  = "creating"
	RuntimeRunning   = "running"
	RuntimeDestroyed = "destroyed"
	RuntimeFailed    = "failed"
)

// Event types emitted by the execution plane.
//
// Mirrors packages/domain/src/events/types.ts. Spelling these inline is what
// this package exists to prevent: a rename on the TypeScript side would
// otherwise surface as a runtime 400, or as an event that is silently never
// routed, rather than as a build failure here.
const (
	EventWorkspaceCreated = "workspace.created"
	EventGitCommitCreated = "git.commit_created"

	EventAgentMessage               = "agent.message"
	EventAgentToolCalled            = "agent.tool.called"
	EventAgentToolCompleted         = "agent.tool.completed"
	EventAgentModelRequestCompleted = "agent.model.request.completed"
	EventAgentSessionStopped        = "agent.session.stopped"
)

// Worker status reported in heartbeats.
const (
	WorkerReady    = "ready"
	WorkerDraining = "draining"
)

// Container naming and labelling.
//
// The E2E suite reaps containers by label and looks them up by name, so these
// are part of the contract too — a silent change here leaks a container per
// test run.
const (
	ContainerNamePrefix = "dude-run-"

	LabelManaged        = "dude.managed"
	LabelRunID          = "dude.run_id"
	LabelOrganizationID = "dude.organization_id"
)

// Run control — what a human has asked of a Run (plan §24).
//
// Mirrors the run_control enum in migrations/009_interventions.sql.
const (
	ControlNone          = "none"
	ControlPauseGraceful = "pause_graceful"
	ControlPauseHard     = "pause_hard"
	ControlAbort         = "abort"
)

// Run status for a paused Run. Separate from the control request above: the
// control says what was asked, the status says what the Run now is.
const RunPaused = "paused"

// Network modes for a Run container.
const (
	// NetworkIsolated is used for untrusted repositories, which must not
	// reach the network at all (plan §47).
	NetworkIsolated = "none"
	NetworkBridge   = "bridge"
)

// Repository trust classes.
const (
	TrustInternal = "trusted_internal"
	TrustExternal = "untrusted_external"
)

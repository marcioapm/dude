import type { AgentRole } from "../hierarchy.ts";

/**
 * AgentHarness — plan §45.
 *
 * OpenCode is the first harness, not the only one. Workflow semantics must
 * never be encoded in harness-specific APIs; everything a harness emits is
 * normalized into the factory's own event schema before it is persisted.
 */

export interface HarnessCapabilities {
  resumableSessions: boolean;
  subagents: boolean;
  customTools: boolean;
  mcp: boolean;
  structuredOutput: boolean;
  eventStream: boolean;
  liveSteering: boolean;
}

/** A workflow can require capabilities instead of naming a harness. */
export type CapabilityRequirement = keyof HarnessCapabilities;

export interface SessionSpec {
  sessionId: string;
  organizationId: string;
  runId: string;
  role: AgentRole;
  model: string;
  /** Absolute path, inside the runtime, of the Session Workspace. */
  workspacePath: string;
  /** The role's system prompt / agent definition. */
  instructions: string;
  /** The concrete task for this Session. */
  prompt: string;
  parentSessionId?: string | undefined;
  maxTokens?: number | undefined;
  temperature?: number | undefined;
}

export interface SessionHandle {
  sessionId: string;
  externalSessionId: string;
}

/** Compact delta used to resume without replaying the whole history (§10.2). */
export interface ContextDelta {
  summary: string;
  answeredQuestions?: Array<{ question: string; answer: string }>;
  newFindings?: Array<Record<string, unknown>>;
  steering?: string[];
}

export type HarnessSessionStatus =
  | "pending"
  | "running"
  | "idle"
  | "awaiting_input"
  | "completed"
  | "failed"
  | "aborted";

export interface HarnessStatus {
  status: HarnessSessionStatus;
  /** Populated once the harness reports terminal failure. */
  error?: string | undefined;
}

export interface HarnessMessage {
  id: string;
  role: "user" | "assistant" | "system" | "tool";
  content: string;
  createdAt: string;
}

export interface HarnessSession {
  externalSessionId: string;
  parentExternalSessionId: string | null;
  title: string;
}

export interface DiffSnapshot {
  /** Repo name → unified diff. */
  perRepo: Record<string, string>;
  filesChanged: number;
  insertions: number;
  deletions: number;
}

/**
 * A harness event, already normalized. `type` is one of EventTypes; the
 * adapter is responsible for the translation from provider-native shapes.
 */
export interface HarnessEvent {
  type: string;
  occurredAt: string;
  externalSessionId: string;
  payload: Record<string, unknown>;
}

export interface AgentHarness {
  readonly name: string;

  capabilities(): HarnessCapabilities;

  createSession(spec: SessionSpec): Promise<SessionHandle>;
  resumeSession(externalSessionId: string, delta: ContextDelta): Promise<void>;
  steer(externalSessionId: string, instruction: string): Promise<void>;
  abort(externalSessionId: string): Promise<void>;

  status(externalSessionId: string): Promise<HarnessStatus>;
  messages(externalSessionId: string): Promise<HarnessMessage[]>;
  children(externalSessionId: string): Promise<HarnessSession[]>;
  diff(externalSessionId: string): Promise<DiffSnapshot>;

  events(externalSessionId: string): AsyncIterable<HarnessEvent>;
}

/** Does this harness satisfy every required capability? */
export function satisfiesCapabilities(
  harness: AgentHarness,
  required: readonly CapabilityRequirement[],
): boolean {
  const caps = harness.capabilities();
  return required.every((r) => caps[r]);
}

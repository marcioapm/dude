/**
 * Fake agent harness — plan §27.1, §34.15.
 *
 * Almost every platform behaviour worth testing (workflow transitions,
 * question/answer, pause/resume/abort, the review→fix loop, event ordering,
 * cost accounting) is independent of whether a real model produced the text.
 * This harness makes those tests deterministic, fast and free.
 *
 * It is scripted rather than random: a test declares the sequence of actions
 * a session should take, and the harness replays it, emitting the same events
 * a real harness would.
 */

import type {
  AgentHarness,
  ContextDelta,
  DiffSnapshot,
  HarnessCapabilities,
  HarnessEvent,
  HarnessMessage,
  HarnessSession,
  HarnessStatus,
  SessionHandle,
  SessionSpec,
} from "@dude/domain";
import { EventTypes } from "@dude/domain";

/** One scripted action a fake session performs. */
export type ScriptedAction =
  | { kind: "message"; text: string }
  | { kind: "tool"; tool: string; args?: Record<string, unknown>; result?: string }
  | { kind: "ask"; question: string; blocking?: boolean }
  | { kind: "spawn"; role: string; title?: string }
  | { kind: "cost"; costUsd: number; inputTokens: number; outputTokens: number }
  | { kind: "fail"; error: string }
  | { kind: "complete" };

export interface FakeScript {
  actions: ScriptedAction[];
  /** Simulated delay between actions; 0 keeps tests fast. */
  stepDelayMs?: number;
}

interface FakeSessionState {
  spec: SessionSpec;
  externalSessionId: string;
  script: ScriptedAction[];
  cursor: number;
  status: HarnessStatus["status"];
  messages: HarnessMessage[];
  children: HarnessSession[];
  steering: string[];
  resumes: ContextDelta[];
  usage: { costUsd: number; inputTokens: number; outputTokens: number; cachedTokens: number };
  pendingEvents: HarnessEvent[];
  waiters: Array<() => void>;
}

const DEFAULT_SCRIPT: ScriptedAction[] = [
  { kind: "message", text: "working on it" },
  { kind: "complete" },
];

export class FakeHarness implements AgentHarness {
  readonly name = "fake";

  readonly #sessions = new Map<string, FakeSessionState>();
  /** Scripts keyed by role, so a test can vary behaviour per agent kind. */
  readonly #scripts = new Map<string, FakeScript>();
  #counter = 0;

  /** Script every session created with this role. */
  script(role: string, script: FakeScript): this {
    this.#scripts.set(role, script);
    return this;
  }

  capabilities(): HarnessCapabilities {
    return {
      resumableSessions: true,
      subagents: true,
      customTools: true,
      mcp: false,
      structuredOutput: true,
      eventStream: true,
      liveSteering: true,
    };
  }

  async createSession(spec: SessionSpec): Promise<SessionHandle> {
    const externalSessionId = `fake_ses_${++this.#counter}`;
    const script = this.#scripts.get(spec.role)?.actions ?? DEFAULT_SCRIPT;

    const state: FakeSessionState = {
      spec,
      externalSessionId,
      script: [...script],
      cursor: 0,
      status: "running",
      messages: [],
      children: [],
      steering: [],
      resumes: [],
      usage: { costUsd: 0, inputTokens: 0, outputTokens: 0, cachedTokens: 0 },
      pendingEvents: [],
      waiters: [],
    };
    this.#sessions.set(externalSessionId, state);

    this.#emit(state, {
      type: EventTypes.SessionStarted,
      occurredAt: new Date().toISOString(),
      externalSessionId,
      payload: { role: spec.role, model: spec.model },
    });

    // Advance asynchronously so createSession returns promptly, as a real
    // harness would.
    void this.#advance(state, this.#scripts.get(spec.role)?.stepDelayMs ?? 0);

    return { sessionId: spec.sessionId, externalSessionId };
  }

  async resumeSession(externalSessionId: string, delta: ContextDelta): Promise<void> {
    const state = this.#require(externalSessionId);
    state.resumes.push(delta);
    state.status = "running";
    void this.#advance(state, 0);
  }

  async steer(externalSessionId: string, instruction: string): Promise<void> {
    const state = this.#require(externalSessionId);
    state.steering.push(instruction);
  }

  async abort(externalSessionId: string): Promise<void> {
    const state = this.#require(externalSessionId);
    state.status = "aborted";
    this.#emit(state, {
      type: EventTypes.SessionStopped,
      occurredAt: new Date().toISOString(),
      externalSessionId,
      payload: { reason: "aborted" },
    });
    this.#wake(state);
  }

  async status(externalSessionId: string): Promise<HarnessStatus> {
    return { status: this.#require(externalSessionId).status };
  }

  async messages(externalSessionId: string): Promise<HarnessMessage[]> {
    return [...this.#require(externalSessionId).messages];
  }

  async children(externalSessionId: string): Promise<HarnessSession[]> {
    return [...this.#require(externalSessionId).children];
  }

  async diff(externalSessionId: string): Promise<DiffSnapshot> {
    this.#require(externalSessionId);
    return { perRepo: {}, filesChanged: 0, insertions: 0, deletions: 0 };
  }

  async *events(externalSessionId: string): AsyncIterable<HarnessEvent> {
    const state = this.#require(externalSessionId);

    while (true) {
      while (state.pendingEvents.length > 0) {
        yield state.pendingEvents.shift()!;
      }
      if (state.status === "completed" || state.status === "failed" || state.status === "aborted") {
        return;
      }
      // Park until the next event or a terminal transition.
      await new Promise<void>((resolve) => state.waiters.push(resolve));
    }
  }

  async usage(externalSessionId: string) {
    return { ...this.#require(externalSessionId).usage };
  }

  // -------------------------------------------------------------------------
  // Test inspection helpers
  // -------------------------------------------------------------------------

  /** Steering instructions a session received, for asserting auditability. */
  steeringFor(externalSessionId: string): string[] {
    return [...this.#require(externalSessionId).steering];
  }

  /** Resume deltas a session received, to assert that waking is delta-based. */
  resumesFor(externalSessionId: string): ContextDelta[] {
    return [...this.#require(externalSessionId).resumes];
  }

  sessionCount(): number {
    return this.#sessions.size;
  }

  #require(externalSessionId: string): FakeSessionState {
    const state = this.#sessions.get(externalSessionId);
    if (!state) throw new Error(`unknown fake session: ${externalSessionId}`);
    return state;
  }

  #emit(state: FakeSessionState, event: HarnessEvent): void {
    state.pendingEvents.push(event);
    this.#wake(state);
  }

  #wake(state: FakeSessionState): void {
    const waiters = state.waiters.splice(0);
    for (const resolve of waiters) resolve();
  }

  /** Replay the script until it blocks, completes, or is aborted. */
  async #advance(state: FakeSessionState, delayMs: number): Promise<void> {
    while (state.cursor < state.script.length) {
      if (state.status === "aborted" || state.status === "waiting_on_human") return;
      if (delayMs > 0) await Bun.sleep(delayMs);

      const action = state.script[state.cursor++]!;
      const now = new Date().toISOString();
      const externalSessionId = state.externalSessionId;

      switch (action.kind) {
        case "message":
          state.messages.push({
            id: `msg_${state.messages.length + 1}`,
            role: "assistant",
            content: action.text,
            createdAt: now,
          });
          this.#emit(state, {
            type: EventTypes.AgentMessage,
            occurredAt: now,
            externalSessionId,
            payload: { text: action.text },
          });
          break;

        case "tool":
          this.#emit(state, {
            type: EventTypes.ToolCalled,
            occurredAt: now,
            externalSessionId,
            payload: { tool: action.tool, args: action.args ?? {} },
          });
          this.#emit(state, {
            type: EventTypes.ToolCompleted,
            occurredAt: now,
            externalSessionId,
            payload: { tool: action.tool, result: action.result ?? "ok" },
          });
          break;

        case "ask":
          // Blocking questions park the session: no further script runs until
          // resumeSession delivers the answer.
          state.status = action.blocking === false ? "running" : "waiting_on_human";
          this.#emit(state, {
            type: EventTypes.QuestionAsked,
            occurredAt: now,
            externalSessionId,
            payload: { question: action.question, blocking: action.blocking ?? true },
          });
          if (state.status === "waiting_on_human") return;
          break;

        case "spawn": {
          const child: HarnessSession = {
            externalSessionId: `${externalSessionId}_child_${state.children.length + 1}`,
            parentExternalSessionId: externalSessionId,
            title: action.title ?? action.role,
          };
          state.children.push(child);
          this.#emit(state, {
            type: EventTypes.SubagentStarted,
            occurredAt: now,
            externalSessionId,
            payload: { role: action.role, childSessionId: child.externalSessionId },
          });
          break;
        }

        case "cost":
          state.usage.costUsd += action.costUsd;
          state.usage.inputTokens += action.inputTokens;
          state.usage.outputTokens += action.outputTokens;
          this.#emit(state, {
            type: EventTypes.CostSampled,
            occurredAt: now,
            externalSessionId,
            payload: {
              costUsd: action.costUsd,
              inputTokens: action.inputTokens,
              outputTokens: action.outputTokens,
            },
          });
          break;

        case "fail":
          state.status = "failed";
          this.#emit(state, {
            type: EventTypes.SessionStopped,
            occurredAt: now,
            externalSessionId,
            payload: { reason: "failed", error: action.error },
          });
          return;

        case "complete":
          state.status = "completed";
          this.#emit(state, {
            type: EventTypes.SessionStopped,
            occurredAt: now,
            externalSessionId,
            payload: { reason: "completed" },
          });
          return;
      }
    }

    // Script exhausted without an explicit terminal action.
    state.status = "completed";
    this.#emit(state, {
      type: EventTypes.SessionStopped,
      occurredAt: new Date().toISOString(),
      externalSessionId: state.externalSessionId,
      payload: { reason: "script_exhausted" },
    });
  }
}

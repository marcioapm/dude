/**
 * OpenCode harness adapter — plan §45.
 *
 * OpenCode is the first harness, not the only one, so nothing above this file
 * may depend on OpenCode's API shape. Two responsibilities:
 *
 *   1. implement AgentHarness against `opencode serve`'s HTTP API;
 *   2. normalize OpenCode's event stream into the factory's own event schema,
 *      because a live stream is transport, not a durable audit log (§18.3).
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

/** OpenCode wraps successful responses in a `data` envelope. */
interface Envelope<T> {
  data: T;
}

/** Provider assumed when a model is configured without one. */
const DEFAULT_PROVIDER = "anthropic";

interface OpenCodeSession {
  id: string;
  parentID?: string;
  title?: string;
  cost?: number;
  tokens?: {
    input: number;
    output: number;
    reasoning: number;
    cache: { read: number; write: number };
  };
  model?: { providerID: string; id: string };
  time?: { created: number; updated: number };
}

interface OpenCodeMessage {
  id: string;
  role: "user" | "assistant" | "system";
  time?: { created: number };
  parts?: Array<{ type: string; text?: string }>;
}

export interface OpenCodeHarnessOptions {
  /** Base URL of `opencode serve`, e.g. http://127.0.0.1:4096 */
  baseUrl: string;
  /** Set when the server runs with OPENCODE_SERVER_PASSWORD. */
  password?: string | undefined;
  fetchImpl?: typeof fetch;
}

export class OpenCodeHarness implements AgentHarness {
  readonly name = "opencode";

  readonly #baseUrl: string;
  readonly #password: string | undefined;
  readonly #fetch: typeof fetch;

  constructor(options: OpenCodeHarnessOptions) {
    this.#baseUrl = options.baseUrl.replace(/\/$/, "");
    this.#password = options.password;
    this.#fetch = options.fetchImpl ?? fetch;
  }

  capabilities(): HarnessCapabilities {
    return {
      resumableSessions: true,
      subagents: true,
      customTools: true,
      mcp: true,
      structuredOutput: true,
      eventStream: true,
      liveSteering: true,
    };
  }

  async #request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const headers: Record<string, string> = {
      "content-type": "application/json",
      ...((init.headers as Record<string, string>) ?? {}),
    };
    if (this.#password) headers.authorization = `Bearer ${this.#password}`;

    const res = await this.#fetch(`${this.#baseUrl}${path}`, { ...init, headers });
    if (!res.ok) {
      const body = await res.text().catch(() => "");
      throw new Error(`opencode ${init.method ?? "GET"} ${path} failed: ${res.status} ${body}`);
    }
    if (res.status === 204) return undefined as T;

    const payload = (await res.json()) as Envelope<T> | T;
    // Unwrap the envelope when present; some endpoints return bare values.
    return payload && typeof payload === "object" && "data" in payload
      ? (payload as Envelope<T>).data
      : (payload as T);
  }

  async createSession(spec: SessionSpec): Promise<SessionHandle> {
    const session = await this.#request<OpenCodeSession>("/api/session", {
      method: "POST",
      body: JSON.stringify({
        // The role maps to an OpenCode agent definition of the same name.
        agent: spec.role,
        ...(spec.model ? { model: parseModelRef(spec.model) } : {}),
        location: { directory: spec.workspacePath },
      }),
    });

    // Send the opening prompt separately: session creation does not take one,
    // and this keeps instructions and task text in one message.
    const opening = spec.instructions ? `${spec.instructions}\n\n---\n\n${spec.prompt}` : spec.prompt;
    await this.#prompt(session.id, opening, "queue");

    return { sessionId: spec.sessionId, externalSessionId: session.id };
  }

  /**
   * Resume a parked session with a compact delta rather than replaying
   * history — the point of waking an orchestrator is new information, not
   * re-reading what it already knows (plan §10.2).
   */
  async resumeSession(externalSessionId: string, delta: ContextDelta): Promise<void> {
    await this.#prompt(externalSessionId, renderDelta(delta), "queue");
  }

  /** Interrupt the current turn and redirect it. */
  async steer(externalSessionId: string, instruction: string): Promise<void> {
    await this.#prompt(externalSessionId, instruction, "steer");
  }

  async abort(externalSessionId: string): Promise<void> {
    await this.#request<unknown>(`/api/session/${externalSessionId}/interrupt`, { method: "POST" });
  }

  async #prompt(externalSessionId: string, text: string, delivery: "steer" | "queue"): Promise<void> {
    await this.#request<unknown>(`/api/session/${externalSessionId}/prompt`, {
      method: "POST",
      body: JSON.stringify({ prompt: { text }, delivery }),
    });
  }

  /**
   * Current session status.
   *
   * An open question outranks "running": a session blocked on a human is not
   * making progress, and the distinction drives both the UI and the decision
   * not to bill waiting time as agent-active.
   */
  async status(externalSessionId: string): Promise<HarnessStatus> {
    const questions = await this.#request<unknown[]>(
      `/api/session/${externalSessionId}/question`,
    ).catch(() => []);
    if (Array.isArray(questions) && questions.length > 0) {
      return { status: "waiting_on_human" };
    }

    const active = await this.#request<Array<{ sessionID?: string }>>("/api/session/active").catch(
      () => [],
    );
    const isActive =
      Array.isArray(active) && active.some((a) => a?.sessionID === externalSessionId);

    return { status: isActive ? "running" : "idle" };
  }

  async messages(externalSessionId: string): Promise<HarnessMessage[]> {
    const messages = await this.#request<OpenCodeMessage[]>(
      `/api/session/${externalSessionId}/message`,
    );

    return (messages ?? []).map((m) => ({
      id: m.id,
      role: m.role,
      content: (m.parts ?? [])
        .filter((p) => p.type === "text" && p.text)
        .map((p) => p.text)
        .join("\n"),
      createdAt: new Date(m.time?.created ?? Date.now()).toISOString(),
    }));
  }

  /** Child sessions — the subagents an orchestrator spawned. */
  async children(externalSessionId: string): Promise<HarnessSession[]> {
    const sessions = await this.#request<OpenCodeSession[]>("/api/session");
    return (sessions ?? [])
      .filter((s) => s.parentID === externalSessionId)
      .map((s) => ({
        externalSessionId: s.id,
        parentExternalSessionId: s.parentID ?? null,
        title: s.title ?? "",
      }));
  }

  /**
   * Diff of the session's working tree.
   *
   * OpenCode has no diff endpoint, so this is computed from git in the
   * workspace by the caller; returning an empty snapshot keeps the interface
   * honest rather than inventing data.
   */
  async diff(_externalSessionId: string): Promise<DiffSnapshot> {
    return { perRepo: {}, filesChanged: 0, insertions: 0, deletions: 0 };
  }

  /**
   * Normalized event stream.
   *
   * OpenCode's SSE feed is transport; each event is translated into the
   * factory's schema and persisted by the caller into the durable ledger.
   */
  async *events(externalSessionId: string): AsyncIterable<HarnessEvent> {
    const headers: Record<string, string> = { accept: "text/event-stream" };
    if (this.#password) headers.authorization = `Bearer ${this.#password}`;

    const res = await this.#fetch(`${this.#baseUrl}/api/session/${externalSessionId}/event`, {
      headers,
    });
    if (!res.ok || !res.body) {
      throw new Error(`opencode event stream failed: ${res.status}`);
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";

    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });

        const frames = buffer.split("\n\n");
        buffer = frames.pop() ?? "";

        for (const frame of frames) {
          const line = frame.split("\n").find((l) => l.startsWith("data:"));
          if (!line) continue;

          let payload: Record<string, unknown>;
          try {
            payload = JSON.parse(line.slice(5).trim()) as Record<string, unknown>;
          } catch {
            // A malformed frame must not tear down the stream.
            continue;
          }

          const normalized = normalizeEvent(payload, externalSessionId);
          if (normalized) yield normalized;
        }
      }
    } finally {
      reader.releaseLock();
    }
  }

  /** Cost and token usage, for the cost ledger (plan §19). */
  async usage(externalSessionId: string): Promise<{
    costUsd: number;
    inputTokens: number;
    outputTokens: number;
    cachedTokens: number;
  }> {
    const session = await this.#request<OpenCodeSession>(`/api/session/${externalSessionId}`);
    const tokens = session.tokens;
    return {
      costUsd: session.cost ?? 0,
      inputTokens: tokens?.input ?? 0,
      outputTokens: tokens?.output ?? 0,
      cachedTokens: (tokens?.cache.read ?? 0) + (tokens?.cache.write ?? 0),
    };
  }
}

/**
 * "anthropic/claude-opus-5" -> OpenCode's ModelRef shape.
 *
 * The field is `id`, not `modelID`; a bare name assumes the default provider
 * rather than failing, so project config can stay terse.
 */
export function parseModelRef(model: string): { id: string; providerID: string } {
  const slash = model.indexOf("/");
  if (slash === -1) return { providerID: DEFAULT_PROVIDER, id: model };
  return { providerID: model.slice(0, slash), id: model.slice(slash + 1) };
}

/** Render a resume delta as a compact prompt. */
export function renderDelta(delta: ContextDelta): string {
  const sections: string[] = [delta.summary];

  if (delta.answeredQuestions?.length) {
    sections.push(
      "Answers to your questions:\n" +
        delta.answeredQuestions.map((q) => `- ${q.question}\n  → ${q.answer}`).join("\n"),
    );
  }
  if (delta.newFindings?.length) {
    sections.push("New findings:\n" + delta.newFindings.map((f) => `- ${JSON.stringify(f)}`).join("\n"));
  }
  if (delta.steering?.length) {
    sections.push("Updated instructions:\n" + delta.steering.map((s) => `- ${s}`).join("\n"));
  }

  return sections.filter(Boolean).join("\n\n");
}

/**
 * Translate one OpenCode event into the factory's schema.
 *
 * Returns null for events with no factory meaning, so the ledger records
 * semantic milestones rather than every internal tick (plan §106).
 */
export function normalizeEvent(
  payload: Record<string, unknown>,
  externalSessionId: string,
): HarnessEvent | null {
  const type = typeof payload.type === "string" ? payload.type : "";
  const properties = (payload.properties ?? {}) as Record<string, unknown>;
  const occurredAt = new Date().toISOString();

  const event = (factoryType: string, extra: Record<string, unknown> = {}): HarnessEvent => ({
    type: factoryType,
    occurredAt,
    externalSessionId,
    payload: { opencodeType: type, ...extra },
  });

  switch (type) {
    case "session.created":
      return event(EventTypes.SessionStarted, { session: properties.info });
    case "session.idle":
      return event(EventTypes.SessionStopped, { reason: "idle" });
    case "session.error":
      return event(EventTypes.SessionStopped, { reason: "error", error: properties.error });

    case "message.updated":
      return event(EventTypes.AgentMessage, { message: properties.info });

    case "tool.execute.before":
      return event(EventTypes.ToolCalled, {
        tool: properties.tool,
        callId: properties.callID,
        args: properties.args,
      });
    case "tool.execute.after":
      return event(EventTypes.ToolCompleted, {
        tool: properties.tool,
        callId: properties.callID,
      });

    case "question.asked":
      return event(EventTypes.QuestionAsked, { question: properties });
    case "permission.requested":
      return event(EventTypes.QuestionAsked, { kind: "permission", permission: properties });

    default:
      return null;
  }
}

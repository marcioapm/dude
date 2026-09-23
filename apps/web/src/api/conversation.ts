/**
 * Project ledger events into a conversation.
 *
 * The ledger is an append-only record of *what happened*; the chat is a
 * narrative of *what the agent is doing*. Turning one into the other is this
 * module's whole job, and it is deliberately the only place that knows how.
 *
 * Two consequences worth stating:
 *
 *  - Events arrive in cursor order but describe overlapping things. A tool
 *    call and its completion are separate events; the projection folds them
 *    into one turn rather than showing the same call twice.
 *  - The ledger is the source of truth, so a reload rebuilds exactly the same
 *    conversation. Nothing lives only in component state.
 */

import type { PersistedEvent, RunStatus } from "@dude/domain";
import { EventTypes, TERMINAL_RUN_STATUSES } from "@dude/domain";
import type { HumanIntent, PlanItem } from "@dude/design-system/components";
import { TODO_STATUSES, type ActivityKind, type ToolCallStatus } from "@dude/design-system/tokens";

/**
 * One stream of a tool's output as the orchestrator keeps it: whole up to
 * 4 KB, otherwise its first and last 2 KB with how much was left out.
 */
export interface CappedOutput {
  head: string;
  tail?: string;
  omittedBytes?: number;
}

/** What a finished tool call produced. Agents report one merged stream or two. */
export interface ToolResult {
  output?: CappedOutput;
  stdout?: CappedOutput;
  stderr?: CappedOutput;
  exitCode?: number;
}

export interface ToolTurn {
  kind: "tool";
  id: string;
  tool: string;
  args: unknown;
  status: ToolCallStatus;
  startedAt: string;
  endedAt: string | null;
  result: ToolResult | null;
}

export interface MessageTurn {
  kind: "message";
  id: string;
  text: string;
  at: string;
  /** How full the conversation was when this message was written. */
  contextTokens: number | null;
  /** The output tokens of the turn this message ended, once it has ended. */
  outputTokens: number | null;
}

/** The model's reasoning between actions: secondary to what it says and does. */
export interface ThoughtTurn {
  kind: "thought";
  id: string;
  text: string;
  at: string;
}

/** The task, as the agent received it. Written by the factory, not a person. */
export interface PromptTurn {
  kind: "prompt";
  id: string;
  text: string;
  at: string;
}

/** A turn's token totals, as the agent reported them when it ended. */
export interface UsageTurn {
  kind: "usage";
  id: string;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
  contextTokens: number;
  at: string;
}

export interface HumanTurn {
  kind: "human";
  id: string;
  /** Steering interrupts; an answer unblocks. They read differently. */
  intent: Extract<HumanIntent, "steer" | "answer">;
  text: string;
  at: string;
  /** A steer is queued until the agent takes it; null until then. */
  deliveredAt: string | null;
}

export type Turn = ToolTurn | MessageTurn | HumanTurn | ThoughtTurn | PromptTurn | UsageTurn;

export interface Conversation {
  turns: Turn[];
  /** The agent's current plan, rebuilt from the latest todowrite. */
  plan: PlanItem[];
  /** Running totals, so the header needs no separate query. */
  costUsd: number;
  tokens: number;
  /** The latest context size, and the window it fills, when reported. */
  contextTokens: number;
  contextWindow: number;
  /** What the agent is doing right now, or null when it is not working. */
  activity: Extract<ActivityKind, "thinking" | "streaming" | "tool"> | null;
  /** The tool being waited on, when activity is "tool". */
  activeTool: { name: string; since: string } | null;
}

/** A todo as the harness emits it inside a `todowrite` call. */
interface HarnessTodo {
  content?: string;
  status?: string;
  priority?: string;
}

/**
 * The fold's in-progress state.
 *
 * Exposed as a type because the projection is incremental: a caller keeps one
 * of these across frames and applies each new event to it, rather than
 * re-folding the whole history every time an event arrives. Over a session of
 * N events that is the difference between N and N²/2 units of work.
 */
export interface Projection {
  turns: Turn[];
  /**
   * Tool calls keyed by the harness's call id, so a completion finds the turn
   * it belongs to rather than appending a second one.
   */
  toolsByCall: Map<string, ToolTurn>;
  /** Steers by directive id, so a delivery marks the turn it belongs to. */
  steersByDirective: Map<string, HumanTurn>;
  plan: PlanItem[];
  costUsd: number;
  tokens: number;
  contextTokens: number;
  contextWindow: number;
  activity: Conversation["activity"];
  activeTool: Conversation["activeTool"];
  /** Highest cursor folded in; lets a caller skip what it already applied. */
  cursor: number;
}

export function emptyProjection(): Projection {
  return {
    turns: [],
    toolsByCall: new Map(),
    steersByDirective: new Map(),
    plan: [],
    costUsd: 0,
    tokens: 0,
    contextTokens: 0,
    contextWindow: 0,
    activity: null,
    activeTool: null,
    cursor: 0,
  };
}

/**
 * Fold an event stream into a conversation.
 *
 * Total and order-dependent but not history-dependent: folding the same
 * events in the same order always produces the same conversation, which is
 * what lets the UI rebuild from a reconnect without special cases.
 */
export function project(events: readonly PersistedEvent[], runStatus?: RunStatus): Conversation {
  return snapshot(apply(emptyProjection(), events), runStatus);
}

/**
 * Apply events to a projection, mutating and returning it.
 *
 * Mutation is deliberate: a tool completion, a steer's delivery and a
 * turn's token totals update a turn already pushed, rather than replacing
 * it. Nothing memoizes turns today, so that re-renders correctly; a turn
 * component wrapped in `React.memo` would need these updates to replace the
 * turn instead.
 */
export function apply(state: Projection, events: readonly PersistedEvent[]): Projection {
  const { turns, toolsByCall } = state;

  for (const event of events) {
    if (event.cursor <= state.cursor) continue;
    state.cursor = event.cursor;

    const payload = event.payload ?? {};
    const isToolEvent =
      event.eventType === EventTypes.ToolCalled || event.eventType === EventTypes.ToolCompleted;
    const tool = isToolEvent ? String(payload.tool ?? "tool") : "";

    switch (event.eventType) {
      case EventTypes.PlanUpdated: {
        // The harness adapter already decided this was a plan rather than a
        // tool call, so nothing here needs to know what the tool was named.
        state.plan = planFrom(payload) ?? state.plan;
        break;
      }

      case EventTypes.AgentMessage: {
        const text = typeof payload.text === "string" ? payload.text : "";
        if (!text.trim()) break;
        const contextTokens = numberOf(payload.contextTokens);
        turns.push({
          kind: "message",
          id: event.eventId,
          text,
          at: event.occurredAt,
          contextTokens: contextTokens > 0 ? contextTokens : null,
          outputTokens: null,
        });
        // A message means the model produced output; it is no longer waiting
        // on a tool.
        state.activity = null;
        state.activeTool = null;
        break;
      }

      case EventTypes.ToolCalled: {
        const callId = String(payload.callId ?? event.eventId);
        const turn: ToolTurn = {
          kind: "tool",
          id: callId,
          tool,
          args: payload.input ?? null,
          status: "running",
          startedAt: event.occurredAt,
          endedAt: null,
          result: null,
        };
        toolsByCall.set(callId, turn);
        turns.push(turn);

        state.activity = "tool";
        state.activeTool = { name: tool, since: event.occurredAt };
        break;
      }

      case EventTypes.ToolCompleted: {
        const callId = String(payload.callId ?? "");
        const existing = toolsByCall.get(callId);
        // The harness reports "error"; the design system's vocabulary calls
        // it "failed".
        const status: ToolCallStatus = payload.status === "error" ? "failed" : "completed";

        const result = resultFrom(payload);
        if (existing) {
          existing.status = status;
          existing.endedAt = event.occurredAt;
          existing.result = result;
        } else {
          // The harness buffers some tools and only reports them once
          // finished, so a completion with no matching call is normal rather
          // than a gap in the record.
          const turn: ToolTurn = {
            kind: "tool",
            id: callId || event.eventId,
            tool,
            args: payload.input ?? null,
            status,
            startedAt: event.occurredAt,
            endedAt: event.occurredAt,
            result,
          };
          toolsByCall.set(turn.id, turn);
          turns.push(turn);
        }

        if (state.activeTool?.name === tool) {
          state.activity = "thinking";
          state.activeTool = null;
        }
        break;
      }

      case EventTypes.AgentThought: {
        const text = typeof payload.text === "string" ? payload.text : "";
        if (!text.trim()) break;
        turns.push({ kind: "thought", id: event.eventId, text, at: event.occurredAt });
        break;
      }

      case EventTypes.PromptDelivered: {
        const text = typeof payload.text === "string" ? payload.text : "";
        if (!text.trim()) break;
        turns.push({ kind: "prompt", id: event.eventId, text, at: event.occurredAt });
        break;
      }

      case EventTypes.DirectiveDelivered: {
        const steer = state.steersByDirective.get(String(payload.directiveId ?? ""));
        if (steer) steer.deliveredAt = event.occurredAt;
        break;
      }

      case EventTypes.ModelRequestCompleted: {
        state.costUsd += numberOf(payload.costUsd);
        const context = numberOf(payload.contextTokens);
        if (context > 0) state.contextTokens = context;
        const window = numberOf(payload.contextWindow);
        if (window > 0) state.contextWindow = window;
        const t = payload.tokens as Record<string, unknown> | undefined;
        if (t) {
          const [input, output] = [numberOf(t.input), numberOf(t.output)];
          state.tokens += input + output;
          // A turn's totals arrive with its end: shown where the turn ended —
          // on its closing message, or on their own after a tool.
          const last = turns[turns.length - 1];
          if (payload.turn === true && last?.kind === "message") {
            last.outputTokens = output;
            if (context > 0) last.contextTokens = context;
          } else if (payload.turn === true) {
            turns.push({
              kind: "usage",
              id: event.eventId,
              input,
              output,
              cacheRead: numberOf(t.cacheRead),
              cacheWrite: numberOf(t.cacheWrite),
              contextTokens: context,
              at: event.occurredAt,
            });
          }
        }
        // Between model requests the agent is thinking, unless a tool is
        // outstanding.
        if (!state.activeTool) state.activity = "thinking";
        break;
      }

      case EventTypes.RunSteered: {
        const turn: HumanTurn = {
          kind: "human",
          id: event.eventId,
          intent: "steer",
          text: String(payload.text ?? ""),
          at: event.occurredAt,
          deliveredAt: null,
        };
        if (typeof payload.directiveId === "string") state.steersByDirective.set(payload.directiveId, turn);
        turns.push(turn);
        break;
      }

      case EventTypes.QuestionAnswered: {
        turns.push({
          kind: "human",
          id: event.eventId,
          intent: "answer",
          text: String(payload.answer ?? ""),
          at: event.occurredAt,
          deliveredAt: event.occurredAt,
        });
        break;
      }

      default:
        break;
    }
  }

  return state;
}

/**
 * The read-only view of a projection that the UI renders.
 *
 * `runStatus` is the Run's actual status, not something re-derived from the
 * event types. The two disagree: `run.paused` is appended when a pause is
 * *requested*, while the Run keeps running until the runner confirms — so a
 * projection that read termination out of the ledger would blank the
 * activity indicator the moment an operator clicked Pause.
 */
export function snapshot(state: Projection, runStatus?: RunStatus): Conversation {
  const settled = runStatus !== undefined && TERMINAL_RUN_STATUSES.includes(runStatus);
  if (!settled) {
    return {
      turns: state.turns,
      plan: state.plan,
      costUsd: state.costUsd,
      tokens: state.tokens,
      contextTokens: state.contextTokens,
      contextWindow: state.contextWindow,
      activity: state.activity,
      activeTool: state.activeTool,
    };
  }

  /*
   * A Run that has ended is not doing anything, whatever the last activity
   * event suggested — without this a completed run shows a thinking
   * indicator forever. A tool still marked running never reported back:
   * `aborted` when the operator stopped the Run, `failed` otherwise, because
   * the design system reads the first as deliberate and the second as an
   * error.
   */
  const unfinished: ToolCallStatus = runStatus === "aborted" ? "aborted" : "failed";
  return {
    turns: state.turns.map((turn) =>
      turn.kind === "tool" && turn.status === "running"
        ? { ...turn, status: unfinished, endedAt: turn.endedAt ?? turn.startedAt }
        : turn,
    ),
    plan: state.plan,
    costUsd: state.costUsd,
    tokens: state.tokens,
    contextTokens: state.contextTokens,
    contextWindow: state.contextWindow,
    activity: null,
    activeTool: null,
  };
}

/** Extract a plan from an `agent.plan.updated` payload, or null if it has none. */
function planFrom(payload: Record<string, unknown>): PlanItem[] | null {
  const todos = payload.todos;
  if (!Array.isArray(todos)) return null;

  return todos.map((raw) => {
    const todo = (raw ?? {}) as HarnessTodo;
    const item: PlanItem = {
      content: todo.content ?? "",
      status: normalizeTodoStatus(todo.status),
    };
    return todo.priority ? { ...item, priority: todo.priority } : item;
  });
}

/**
 * Map the harness's todo status onto the design system's vocabulary.
 *
 * A membership check against the exported tuple rather than a switch, so a
 * status added to the design system flows through without a second edit here.
 */
function normalizeTodoStatus(status: unknown): PlanItem["status"] {
  return (TODO_STATUSES as readonly string[]).includes(status as string)
    ? (status as PlanItem["status"])
    : "pending";
}

/** A tool's outcome from an `agent.tool.completed` payload, or null if it reported none. */
function resultFrom(payload: Record<string, unknown>): ToolResult | null {
  const result: ToolResult = {};
  for (const key of ["output", "stdout", "stderr"] as const) {
    const stream = payload[key] as Record<string, unknown> | undefined;
    if (stream && typeof stream.head === "string") {
      result[key] = {
        head: stream.head,
        ...(typeof stream.tail === "string" ? { tail: stream.tail } : {}),
        ...(numberOf(stream.omittedBytes) > 0 ? { omittedBytes: numberOf(stream.omittedBytes) } : {}),
      };
    }
  }
  if (typeof payload.exitCode === "number") result.exitCode = payload.exitCode;
  return Object.keys(result).length > 0 ? result : null;
}

function numberOf(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

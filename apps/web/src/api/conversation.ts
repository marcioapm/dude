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

import type { PersistedEvent } from "@dude/domain";
import { EventTypes } from "@dude/domain";
import type { HumanIntent, PlanItem } from "@dude/design-system/components";
import { TODO_STATUSES, type ActivityKind, type ToolCallStatus } from "@dude/design-system/tokens";

export interface ToolTurn {
  kind: "tool";
  id: string;
  tool: string;
  args: unknown;
  status: ToolCallStatus;
  startedAt: string;
  endedAt: string | null;
}

export interface MessageTurn {
  kind: "message";
  id: string;
  text: string;
  at: string;
}

export interface HumanTurn {
  kind: "human";
  id: string;
  /** Steering interrupts; an answer unblocks. They read differently. */
  intent: Extract<HumanIntent, "steer" | "answer">;
  text: string;
  at: string;
}

export type Turn = ToolTurn | MessageTurn | HumanTurn;

export interface Conversation {
  turns: Turn[];
  /** The agent's current plan, rebuilt from the latest todowrite. */
  plan: PlanItem[];
  /** Running totals, so the header needs no separate query. */
  costUsd: number;
  tokens: number;
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
  plan: PlanItem[];
  costUsd: number;
  tokens: number;
  activity: Conversation["activity"];
  activeTool: Conversation["activeTool"];
  /** Highest cursor folded in; lets a caller skip what it already applied. */
  cursor: number;
}

export function emptyProjection(): Projection {
  return {
    turns: [],
    toolsByCall: new Map(),
    plan: [],
    costUsd: 0,
    tokens: 0,
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
export function project(events: readonly PersistedEvent[]): Conversation {
  return snapshot(apply(emptyProjection(), events));
}

/**
 * Apply events to a projection, mutating and returning it.
 *
 * Mutation is deliberate: a tool completion updates the turn its call
 * created, so turns were never immutable, and keeping their identity stable
 * across frames is what lets React skip re-rendering settled turns.
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

    /*
     * `todowrite` is the plan, not a tool call worth its own turn — the
     * agent rewrites the whole list each time, so rendering every call
     * would bury the work under near-identical lists. Handled before the
     * switch because the call and its completion both carry the list.
     */
    if (tool === "todowrite") {
      state.plan = planFrom(payload.input) ?? state.plan;
      continue;
    }

    switch (event.eventType) {
      case EventTypes.AgentMessage: {
        const text = typeof payload.text === "string" ? payload.text : "";
        if (!text.trim()) break;
        turns.push({ kind: "message", id: event.eventId, text, at: event.occurredAt });
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

        if (existing) {
          existing.status = status;
          existing.endedAt = event.occurredAt;
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

      case EventTypes.ModelRequestCompleted: {
        state.costUsd += numberOf(payload.costUsd);
        const t = payload.tokens as Record<string, unknown> | undefined;
        if (t) state.tokens += numberOf(t.input) + numberOf(t.output);
        // Between model requests the agent is thinking, unless a tool is
        // outstanding.
        if (!state.activeTool) state.activity = "thinking";
        break;
      }

      case EventTypes.RunSteered: {
        turns.push({
          kind: "human",
          id: event.eventId,
          intent: "steer",
          text: String(payload.text ?? ""),
          at: event.occurredAt,
        });
        break;
      }

      case EventTypes.QuestionAnswered: {
        turns.push({
          kind: "human",
          id: event.eventId,
          intent: "answer",
          text: String(payload.answer ?? ""),
          at: event.occurredAt,
        });
        break;
      }

      case EventTypes.RunCompleted:
      case EventTypes.RunFailed:
      case EventTypes.RunAborted:
      case EventTypes.RunPaused: {
        // A finished Run is not doing anything, whatever the last activity
        // event suggested; and a tool still marked running never completed.
        state.activity = null;
        state.activeTool = null;
        for (const turn of turns) {
          if (turn.kind === "tool" && turn.status === "running") {
            turn.status = "failed";
            turn.endedAt = turn.endedAt ?? turn.startedAt;
          }
        }
        break;
      }

      default:
        break;
    }
  }

  return state;
}

/** The read-only view of a projection that the UI renders. */
export function snapshot(state: Projection): Conversation {
  return {
    turns: state.turns,
    plan: state.plan,
    costUsd: state.costUsd,
    tokens: state.tokens,
    activity: state.activity,
    activeTool: state.activeTool,
  };
}

/** Extract a plan from a `todowrite` payload, or null if it has none. */
function planFrom(input: unknown): PlanItem[] | null {
  const todos = (input as { todos?: unknown } | null)?.todos;
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

function numberOf(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

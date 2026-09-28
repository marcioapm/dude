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

import type { PersistedEvent, Run, RunStatus } from "@dude/domain";
import { EventTypes, TERMINAL_RUN_STATUSES } from "@dude/domain";
import type { HumanIntent, PlanItem, ToolOutput } from "@dude/design-system/components";
import { TODO_STATUSES, type ActivityKind, type ToolCallStatus } from "@dude/design-system/tokens";

/**
 * What a finished tool call produced. Agents report one merged stream or
 * two; each is whole up to 4 KB, otherwise its first and last 2 KB with how
 * much was left out — the shape the tool card renders.
 */
export interface ToolResult {
  output?: ToolOutput;
  stdout?: ToolOutput;
  stderr?: ToolOutput;
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

/** A question the agent stopped on, for a person to answer. */
export interface QuestionTurn {
  kind: "question";
  id: string;
  questionId: string;
  text: string;
  options: string[];
  at: string;
  /** Null while it waits for an answer. */
  answeredAt: string | null;
}

/** The task, as the agent received it. Written by the factory, not a person. */
export interface PromptTurn {
  kind: "prompt";
  id: string;
  text: string;
  at: string;
}

/** A turn's token totals, when the turn ended on something other than a message. */
export interface UsageTurn {
  kind: "usage";
  id: string;
  outputTokens: number;
  contextTokens: number | null;
  at: string;
}

/**
 * Who a person's act came from: the ledger's actor id (an API key until
 * people are records), and their name when the event carried one
 * (`actor` as a PersonRef, once the people work lands). The screen names
 * the rest from the organisation's people.
 */
export interface ActorRef {
  id: string;
  name: string | null;
}

export interface HumanTurn {
  kind: "human";
  id: string;
  /** Steering interrupts; an answer unblocks. They read differently. */
  intent: Extract<HumanIntent, "steer" | "answer">;
  /** Who said it. */
  by: ActorRef | null;
  text: string;
  at: string;
  /** When the agent took it. A steer is queued until then (null); an answer is delivered as given. */
  deliveredAt: string | null;
}

/**
 * An event the agent (or a script it ran) recorded with `dude event`: a
 * milestone, a measurement. Progress is one that updates in place.
 */
export interface EventTurn {
  kind: "event";
  id: string;
  /** What the agent called it: "progress", "tests.finished". */
  type: string;
  data: unknown;
  at: string;
}

/** How far along the agent says it is: one per run, updated in place. */
export interface ProgressTurn {
  kind: "progress";
  id: string;
  done: number | null;
  of: number | null;
  step: string | null;
  at: string;
  /** When it was first reported, so the chat keeps it where it started. */
  startedAt: string;
}

/** The agent asked for a repository its work does not name; a person decides. */
export interface RepositoryRequestTurn {
  kind: "repositoryRequest";
  id: string;
  requestId: string;
  repository: string;
  access: "read" | "write";
  reason: string;
  at: string;
  /** Null while it waits; "approved" or "denied" once decided. */
  decision: "approved" | "denied" | null;
  decidedAt: string | null;
}

/**
 * Something dude did to the session: parked it while it waits on a person
 * (its container stopped, nothing held), took it back up, or nudged it
 * after it went quiet.
 */
export interface NoticeTurn {
  kind: "notice";
  id: string;
  notice: "parked" | "unparked" | "nudged";
  text: string;
  at: string;
}

/**
 * How the Run ended, when it did not simply finish: it failed (and why, as
 * the orchestrator recorded it), or a person aborted it. A finished Run
 * needs no line of its own — its header says so — but these explain the
 * silence after the last turn, which may otherwise say it was waiting.
 */
export interface EndedTurn {
  kind: "ended";
  id: string;
  outcome: "failed" | "aborted";
  text: string;
  at: string;
  /** Who stopped it, for an abort a person asked for. */
  by: ActorRef | null;
  /** The reason given, when there was one. */
  why: string | null;
}

export type Turn =
  | ToolTurn | MessageTurn | HumanTurn | ThoughtTurn | PromptTurn | UsageTurn | QuestionTurn | EventTurn | ProgressTurn
  | RepositoryRequestTurn | NoticeTurn | EndedTurn;

/** Why dude paused a Run itself (Run.dudePause), as the transcript and the composer say it. */
export type DudePause = NonNullable<Run["dudePause"]>;
export const PAUSE_WORDS: Record<DudePause, { parked: string; composer: string }> = {
  person: {
    parked: "Parked while it waits for you — nothing is held; your answer resumes it.",
    composer: "Parked while it waits for you: decide its request above, and that resumes it.",
  },
  idle: {
    parked: "Parked: it went quiet and did not answer a nudge. Resume it when you have looked.",
    composer: "Parked after going quiet. Resume it to steer.",
  },
  repository: { parked: "Paused to bring in a repository.", composer: "Bringing in a repository; it carries on in a moment." },
};

/** What the transcript says for what dude did to a Run, by event type. */
const NOTICES: Record<string, { notice: NoticeTurn["notice"]; text: (payload: Record<string, unknown>) => string }> = {
  "run.parked": {
    notice: "parked",
    text: (p) => PAUSE_WORDS[p.reason as DudePause]?.parked ?? "Parked.",
  },
  "run.unparked": { notice: "unparked", text: () => "Taken back up where it left off." },
  "run.idle_nudged": { notice: "nudged", text: () => "Quiet for a while: nudged to carry on or ask." },
};

/** Custom events live under this prefix in the ledger (agenttools.CustomPrefix). */
export const CUSTOM_EVENT_PREFIX = "agent.custom.";

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
  /** The question the agent is waiting on, if it is. */
  openQuestion: QuestionTurn | null;
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
  /** Steers and answers by directive id, so a delivery marks the turn it belongs to. */
  steersByDirective: Map<string, HumanTurn>;
  questionsById: Map<string, QuestionTurn>;
  repoRequestsById: Map<string, RepositoryRequestTurn>;
  /** Where the progress row is in `turns`, once there is one. */
  progressIndex: number | null;
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
    questionsById: new Map(),
    repoRequestsById: new Map(),
    progressIndex: null,
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
        // The question block an agent ended on (before questions were asked
        // with a tool; older transcripts) is shown by the question turn that
        // follows; left in the message it would be said twice.
        const text = typeof payload.text === "string" ? withoutQuestion(payload.text) : "";
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
          const output = numberOf(t.output);
          state.tokens += numberOf(t.input) + output;
          // A turn's totals arrive with its end: shown where the turn ended —
          // on its closing message, or on their own after a tool.
          if (payload.turn === true) {
            const last = turns[turns.length - 1];
            const contextTokens = context > 0 ? context : null;
            if (last?.kind === "message") {
              last.outputTokens = output;
              last.contextTokens = contextTokens ?? last.contextTokens;
            } else {
              turns.push({ kind: "usage", id: event.eventId, outputTokens: output, contextTokens, at: event.occurredAt });
            }
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
          by: humanActor(event),
          text: String(payload.text ?? ""),
          at: event.occurredAt,
          deliveredAt: null,
        };
        if (typeof payload.directiveId === "string") state.steersByDirective.set(payload.directiveId, turn);
        turns.push(turn);
        break;
      }

      case EventTypes.QuestionAsked: {
        // The workflow escalating to a person is a question too, but not one
        // this agent asked, and not one answered here.
        if (payload.kind !== "agent") break;
        const turn: QuestionTurn = {
          kind: "question",
          id: event.eventId,
          questionId: String(payload.questionId ?? ""),
          text: String(payload.prompt ?? ""),
          options: Array.isArray(payload.options) ? payload.options.map(String) : [],
          at: event.occurredAt,
          answeredAt: null,
        };
        state.questionsById.set(turn.questionId, turn);
        turns.push(turn);
        // The agent is not working: it is waiting on a person.
        state.activity = null;
        state.activeTool = null;
        break;
      }

      case EventTypes.QuestionAnswered: {
        const question = state.questionsById.get(String(payload.questionId ?? ""));
        if (question) question.answeredAt = event.occurredAt;
        // Delivered the way a steer is: queued until the agent takes it.
        const turn: HumanTurn = {
          kind: "human",
          id: event.eventId,
          intent: "answer",
          by: humanActor(event),
          text: String(payload.answer ?? ""),
          at: event.occurredAt,
          deliveredAt: typeof payload.directiveId === "string" ? null : event.occurredAt,
        };
        if (typeof payload.directiveId === "string") state.steersByDirective.set(payload.directiveId, turn);
        turns.push(turn);
        break;
      }

      case "run.parked":
      case "run.unparked":
      case "run.idle_nudged": {
        const { notice, text } = NOTICES[event.eventType]!;
        turns.push({ kind: "notice", id: event.eventId, notice, text: text(payload), at: event.occurredAt });
        if (notice === "parked") {
          state.activity = null;
          state.activeTool = null;
        }
        break;
      }

      case EventTypes.RunFailed:
      case EventTypes.RunAborted: {
        const failed = event.eventType === EventTypes.RunFailed;
        const why = String((failed ? payload.error : payload.reason) ?? "").trim();
        turns.push({
          kind: "ended", id: event.eventId, outcome: failed ? "failed" : "aborted",
          text: failed ? (why ? `Failed: ${why}` : "Failed.") : why ? `Aborted: ${why}` : "Aborted.",
          at: event.occurredAt,
          by: failed ? null : humanActor(event),
          why: why || null,
        });
        state.activity = null;
        state.activeTool = null;
        break;
      }

      case "repository.requested": {
        const turn: RepositoryRequestTurn = {
          kind: "repositoryRequest", id: event.eventId, requestId: String(payload.requestId ?? ""),
          repository: String(payload.repository ?? ""), access: payload.access === "write" ? "write" : "read",
          reason: String(payload.reason ?? ""), at: event.occurredAt, decision: null, decidedAt: null,
        };
        state.repoRequestsById.set(turn.requestId, turn);
        turns.push(turn);
        break;
      }

      case "repository.approved":
      case "repository.denied": {
        const turn = state.repoRequestsById.get(String(payload.requestId ?? ""));
        if (turn) {
          turn.decision = event.eventType === "repository.approved" ? "approved" : "denied";
          turn.decidedAt = event.occurredAt;
        }
        break;
      }

      default: {
        if (!event.eventType.startsWith(CUSTOM_EVENT_PREFIX)) break;
        const type = event.eventType.slice(CUSTOM_EVENT_PREFIX.length);
        const data = payload.data ?? {};
        if (type === "progress") {
          // Updated in place: one bar that moves, not a line per update.
          const d = (typeof data === "object" && data !== null ? data : {}) as Record<string, unknown>;
          const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? v : null);
          const existing = state.progressIndex === null ? undefined : (turns[state.progressIndex] as ProgressTurn);
          const next: ProgressTurn = {
            kind: "progress", id: existing?.id ?? event.eventId,
            done: num(d.done), of: num(d.of), step: typeof d.step === "string" ? d.step : null,
            at: event.occurredAt, startedAt: existing?.startedAt ?? event.occurredAt,
          };
          if (state.progressIndex !== null) turns[state.progressIndex] = next;
          else state.progressIndex = turns.push(next) - 1;
          break;
        }
        turns.push({ kind: "event", id: event.eventId, type, data, at: event.occurredAt });
        break;
      }
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
      openQuestion: openQuestion(state),
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
    // A finished Run asks nothing, whatever it asked before it ended.
    openQuestion: null,
    activity: null,
    activeTool: null,
  };
}

/** The latest question still waiting for an answer. */
function openQuestion(state: Projection): QuestionTurn | null {
  let open: QuestionTurn | null = null;
  for (const q of state.questionsById.values()) if (q.answeredAt === null) open = q;
  return open;
}

/** Extract a plan from an `agent.plan.updated` payload, or null if it has none. */
export function planFrom(payload: Record<string, unknown>): PlanItem[] | null {
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

/**
 * A message without the question block it ends a turn with: the question
 * turn says it. The last block only, as the orchestrator read it when
 * agents asked that way — an earlier one was not the question asked.
 */
function withoutQuestion(text: string): string {
  const blocks = [...text.matchAll(/```question[ \t]*\n[\s\S]*?\n?```/g)];
  const last = blocks[blocks.length - 1];
  if (!last || last.index === undefined) return text;
  return (text.slice(0, last.index) + text.slice(last.index + last[0].length)).trimEnd();
}

/**
 * The person behind an event, when a person did it: their id, and their
 * name if the event says it (a PersonRef `actor` from the API, or a
 * `name` on the envelope's actor).
 */
export function humanActor(event: PersistedEvent): ActorRef | null {
  const actor = event.actor as PersistedEvent["actor"] & { name?: unknown };
  if (actor?.type !== "human" || !actor.id || actor.id === "unknown") return null;
  return { id: actor.id, name: typeof actor.name === "string" && actor.name ? actor.name : null };
}

/** A person's name for an act: the one it came with, the organisation's, or null when unknown. */
export function actorName(by: ActorRef | null, people: ReadonlyMap<string, string>): string | null {
  if (!by) return null;
  return by.name ?? people.get(by.id) ?? null;
}

function numberOf(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

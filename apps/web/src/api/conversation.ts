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

import type { CostOrigin, PersistedEvent, Run, RunStatus } from "@dude/domain";
import { EventTypes, TERMINAL_RUN_STATUSES } from "@dude/domain";
import type { HumanIntent, PlanItem, ToolOutput } from "@dude/design-system/components";
import { TODO_STATUSES, type ActivityKind, type ToolCallStatus } from "@dude/design-system/tokens";
import { formatDuration } from "@dude/design-system";

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
  /** When the harness took it (lux's accepted receipt); null before, or from a lux that sends none. */
  acceptedAt: string | null;
  /** Where the harness said it lands: at the agent's next step, or only when its turn ends. */
  lands: SteerLands | null;
  /**
   * lux reported the agent's step reading it: `deliveredAt` is when it was
   * read, and the turn sits where it was read, not where it was typed.
   * False for a delivery that only says it was handed over (an older lux).
   */
  read: boolean;
  /** The tool the agent finished just before reading it ("read … after Bash"). */
  after: string | null;
  /** Why it will not reach the agent, once lux or dude says so. */
  failed: string | null;
  /** A person asked for it to be heard now: re-sent with interrupt. */
  interrupting: boolean;
  /** The directive it was sent as, to re-send it (Interrupt now, Retry); null for an answer given directly. */
  directiveId: string | null;
}

export type SteerLands = "next_step" | "next_turn";

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
 * after it went quiet. A resume, once timed, says how long it took, its
 * phases in `title`, one per line.
 */
export interface NoticeTurn {
  kind: "notice";
  id: string;
  notice: "parked" | "unparked" | "nudged";
  text: string;
  at: string;
  title?: string;
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
  unused: {
    parked: "Parked: nobody opened the preview for a while. Starting a server wakes it.",
    composer: "A parked branch preview: start a server to wake it.",
  },
  conductor: {
    parked: "Parked while nobody is writing — nothing is held; your next message resumes it.",
    composer: "Parked: your next message resumes it.",
  },
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

/** A resume's phases (`run.resume.timed`), in order, as its notice's hover names them. */
const RESUME_PHASES: ReadonlyArray<readonly [key: string, label: string]> = [
  ["react", "dude asked lux"],
  ["schedule", "lux placed it"],
  ["image", "image ready"],
  ["restore", "restored"],
  ["start", "started"],
  ["reload", "agent reloaded"],
  ["take", "took its input"],
  ["firstOutput", "first words"],
];

/**
 * How long a resume took, from its `run.resume.timed`: "in 6.4s", ", on
 * another host" when lux moved it, and the phases it knows, one per line.
 * Null when it carries no total.
 */
export function resumeTiming(payload: Record<string, unknown>): { took: string; title: string } | null {
  if (typeof payload.totalMs !== "number") return null;
  const phases = (payload.phases ?? {}) as Record<string, unknown>;
  const lines = RESUME_PHASES.flatMap(([key, label]) =>
    typeof phases[key] === "number" ? [`${label} ${formatDuration(phases[key] as number)}`] : []);
  return {
    took: `in ${formatDuration(payload.totalMs)}${payload.moved === true ? ", on another host" : ""}`,
    title: lines.join("\n"),
  };
}

/** Custom events live under this prefix in the ledger (agenttools.CustomPrefix). */
export const CUSTOM_EVENT_PREFIX = "agent.custom.";

/**
 * Why a queued steer has not been read, as its one line says: the Run's
 * state first (paused, starting), then what lux said the harness does,
 * then what the agent is running now. `tool` is the open tool call's name;
 * null when nothing is running. An older lux (lands unknown) holds a steer
 * until the turn ends, and is described so.
 */
export type SteerWait =
  | { kind: "paused" } | { kind: "starting" } | { kind: "next_turn" }
  | { kind: "tool"; tool: string } | { kind: "next_step" };

export function steerWait(turn: HumanTurn, runStatus: RunStatus, activeTool: string | null, lands: SteerLands | null): SteerWait {
  if (runStatus === "paused") return { kind: "paused" };
  if (runStatus === "pending" || runStatus === "scheduled" || runStatus === "starting") return { kind: "starting" };
  const where = turn.lands ?? lands;
  if (where !== "next_step") return { kind: "next_turn" };
  return activeTool ? { kind: "tool", tool: activeTool } : { kind: "next_step" };
}

/** The composer's hint for a steer sent now, following the same capability. */
export function landsHint(runStatus: RunStatus, activeTool: string | null, lands: SteerLands | null): string | null {
  if (runStatus !== "running") return null;
  if (lands !== "next_step") return "Lands when the turn ends";
  return activeTool ? "Lands after the current tool" : "Lands at the agent's next step";
}

/** A tool's name as a person reads it: "bash" → "Bash". */
export function toolLabel(name: string): string {
  return name.charAt(0).toUpperCase() + name.slice(1);
}

/**
 * Who priced a Run's model cost. lux's cost plugins meter every token and
 * settle the price over days (`settled` once its status is final); the
 * agent's harness's own running total is never settled.
 */
export type CostSource =
  | { readonly from: Extract<CostOrigin, "lux">; readonly settled: boolean }
  | { readonly from: Extract<CostOrigin, "agent">; readonly settled: false };

export interface Conversation {
  turns: Turn[];
  /** The agent's current plan, rebuilt from the latest todowrite. */
  plan: PlanItem[];
  /** Running totals, so the header needs no separate query. */
  costUsd: number;
  /**
   * Where `costUsd` is from: lux's cost plugins (`run.cost.reported`), with
   * whether lux has settled it, or the harness's own running total.
   */
  costSource: CostSource;
  tokens: number;
  /** The latest context size, and the window it fills, when reported. */
  contextTokens: number;
  contextWindow: number;
  /** The question the agent is waiting on, if it is. */
  openQuestion: QuestionTurn | null;
  /** The repository request waiting on a person's decision, if one is. */
  openRequest: RepositoryRequestTurn | null;
  /** What the agent is doing right now, or null when it is not working. */
  activity: Extract<ActivityKind, "thinking" | "streaming" | "tool"> | null;
  /** The tool being waited on, when activity is "tool". */
  activeTool: { name: string; since: string } | null;
  /** How often it called each tool, by the tool's name as the harness gives it. */
  toolCounts: ReadonlyMap<string, number>;
  /**
   * Where a steer sent now lands, as this Run's lux last said: `next_step`,
   * `next_turn`, or null when it has said nothing (an older lux, which
   * holds a steer until the turn ends).
   */
  lands: SteerLands | null;
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
  /** The harness's cost deltas, summed. */
  costUsd: number;
  /**
   * lux's latest AI cost for the Run and its status, once a
   * `run.cost.reported` carried one. It replaces `costUsd`, never adds to
   * it: both price the same tokens.
   */
  luxCost: { usd: number; status: string } | null;
  tokens: number;
  contextTokens: number;
  contextWindow: number;
  activity: Conversation["activity"];
  activeTool: Conversation["activeTool"];
  toolCounts: Map<string, number>;
  lands: SteerLands | null;
  /**
   * "Taken back up" notices whose resume is not timed yet, by the epoch
   * their `run.unparked` names: the `run.resume.timed` of that epoch says
   * how long it took there, whenever it arrives.
   */
  untimedUnparks: Map<number, NoticeTurn>;
  /**
   * The same for a `run.unparked` that names no epoch (written before
   * they did): the last one, until the next timing. A later park or pause
   * ends the wait, so a resume never timed is not given the next one's
   * numbers.
   */
  untimedUnpark: NoticeTurn | null;
  /**
   * "Resumed in …" notices said on their own, by the epoch their timing
   * names. Such a timing can be a park's return whose `run.unparked` is
   * still to come (lux streamed the first words before answering the
   * resume): when it comes, the notice becomes that return, not a second one.
   */
  standaloneTimings: Map<number, { turn: NoticeTurn; took: string }>;
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
    luxCost: null,
    tokens: 0,
    contextTokens: 0,
    contextWindow: 0,
    activity: null,
    activeTool: null,
    toolCounts: new Map(),
    lands: null,
    untimedUnparks: new Map(),
    untimedUnpark: null,
    standaloneTimings: new Map(),
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
 * turn instead. A steer the agent read moves to where it was read — the
 * same object, spliced out and pushed — so the turns after it shift up by
 * one; callers key on turn ids, not positions.
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
        state.toolCounts.set(tool, (state.toolCounts.get(tool) ?? 0) + 1);
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
        const lands = landsOf(payload.lands);
        if (lands) state.lands = lands;
        const text = typeof payload.text === "string" ? payload.text : "";
        if (!text.trim()) break;
        turns.push({ kind: "prompt", id: event.eventId, text, at: event.occurredAt });
        break;
      }

      case EventTypes.DirectiveAccepted: {
        const lands = landsOf(payload.lands);
        if (lands) state.lands = lands;
        const steer = state.steersByDirective.get(String(payload.directiveId ?? ""));
        if (steer && steer.acceptedAt === null) {
          steer.acceptedAt = event.occurredAt;
          steer.lands = lands;
        }
        break;
      }

      case EventTypes.DirectiveDelivered: {
        const steer = state.steersByDirective.get(String(payload.directiveId ?? ""));
        // Once: a re-sent steer (interrupt now) is the same turn under two ids.
        if (!steer || steer.deliveredAt !== null) break;
        steer.deliveredAt = event.occurredAt;
        steer.failed = null;
        if (payload.read === true) {
          steer.read = true;
          steer.after = toolBefore(turns, steer);
          moveToEnd(state, steer);
        }
        break;
      }

      case EventTypes.DirectiveFailed: {
        const id = String(payload.directiveId ?? "");
        const steer = state.steersByDirective.get(id);
        // Only its latest attempt's failure fails the turn: an Interrupt now
        // that carries the words still may deliver them.
        if (steer && steer.deliveredAt === null && steer.directiveId === id) {
          steer.failed = String(payload.error ?? "") || "lux could not deliver it";
        }
        break;
      }

      case EventTypes.RunCostReported: {
        // Mirrors lux_ai_usd: null (lux has priced no AI) puts the harness's
        // figure back, as run_model_usd does.
        const usd = payload.aiUsd;
        state.luxCost = typeof usd === "number" && Number.isFinite(usd) ? { usd, status: String(payload.status ?? "") } : null;
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
        const directiveId = typeof payload.directiveId === "string" ? payload.directiveId : null;
        // "Interrupt now" on a queued steer, or Retry on a failed one, sends
        // it again superseding it: one steer, not two turns saying the same thing.
        const earlier = typeof payload.supersedes === "string" ? state.steersByDirective.get(payload.supersedes) : undefined;
        const same = earlier !== undefined && earlier.text === String(payload.text ?? "");
        if (same && earlier.deliveredAt !== null && payload.interrupt === true) {
          // Interrupt now clicked on a steer already read (the page had not
          // caught up): it stays where and when it was read, and the
          // interrupt's own delivery, flagged interruptOnly, settles nothing new.
          if (directiveId) state.steersByDirective.set(directiveId, earlier);
          break;
        }
        if (same && earlier.deliveredAt === null) {
          // A retry replaces a failed attempt, interrupt and all: it is
          // interrupting only if this attempt is.
          earlier.interrupting = payload.interrupt === true || (earlier.failed === null && earlier.interrupting);
          earlier.failed = null;
          if (directiveId) {
            state.steersByDirective.set(directiveId, earlier);
            earlier.directiveId = directiveId;
          }
          break;
        }
        const turn: HumanTurn = {
          ...humanTurn(event, "steer", String(payload.text ?? ""), null),
          interrupting: payload.interrupt === true,
          directiveId,
        };
        if (directiveId) state.steersByDirective.set(directiveId, turn);
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
        const directiveId = typeof payload.directiveId === "string" ? payload.directiveId : null;
        const turn = { ...humanTurn(event, "answer", String(payload.answer ?? ""), directiveId ? null : event.occurredAt), directiveId };
        if (directiveId !== null) state.steersByDirective.set(directiveId, turn);
        turns.push(turn);
        break;
      }

      case "run.parked":
      case "run.unparked":
      case "run.idle_nudged": {
        const { notice, text } = NOTICES[event.eventType]!;
        const turn: NoticeTurn = { kind: "notice", id: event.eventId, notice, text: text(payload), at: event.occurredAt };
        if (notice === "unparked" && typeof payload.epoch === "number") {
          // Its timing came first and was said on its own: that notice is
          // this return.
          const said = state.standaloneTimings.get(payload.epoch);
          if (said) {
            state.standaloneTimings.delete(payload.epoch);
            said.turn.text = `Taken back up ${said.took}.`;
            break;
          }
        }
        turns.push(turn);
        if (notice === "parked") {
          state.activity = null;
          state.activeTool = null;
          state.untimedUnpark = null;
        }
        if (notice === "unparked") {
          if (typeof payload.epoch === "number") state.untimedUnparks.set(payload.epoch, turn);
          else state.untimedUnpark = turn;
        }
        break;
      }

      case EventTypes.RunPaused:
        state.untimedUnpark = null;
        break;

      case EventTypes.RunResumeTimed: {
        // How long the resume took: said on its own "taken back up" notice,
        // by epoch — a timing can arrive after the next park — or else on a
        // notice of its own, kept by epoch in case its park's return is
        // still to come.
        const timing = resumeTiming(payload);
        if (!timing) break;
        let unparked: NoticeTurn | null | undefined;
        if (typeof payload.epoch === "number" && state.untimedUnparks.has(payload.epoch)) {
          unparked = state.untimedUnparks.get(payload.epoch);
          state.untimedUnparks.delete(payload.epoch);
        } else {
          unparked = state.untimedUnpark;
          state.untimedUnpark = null;
        }
        if (unparked) {
          unparked.text = `Taken back up ${timing.took}.`;
          unparked.title = timing.title;
          break;
        }
        const own: NoticeTurn = { kind: "notice", id: event.eventId, notice: "unparked", text: `Resumed ${timing.took}.`,
          title: timing.title, at: event.occurredAt };
        turns.push(own);
        if (typeof payload.epoch === "number") state.standaloneTimings.set(payload.epoch, { turn: own, took: timing.took });
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
      ...modelCost(state),
      tokens: state.tokens,
      contextTokens: state.contextTokens,
      contextWindow: state.contextWindow,
      openQuestion: openQuestion(state),
      openRequest: openRequest(state),
      activity: state.activity,
      activeTool: state.activeTool,
      toolCounts: state.toolCounts,
      lands: state.lands,
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
    ...modelCost(state),
    tokens: state.tokens,
    contextTokens: state.contextTokens,
    contextWindow: state.contextWindow,
    // A finished Run asks nothing, whatever it asked before it ended.
    openQuestion: null,
    openRequest: null,
    activity: null,
    activeTool: null,
    toolCounts: state.toolCounts,
    lands: state.lands,
  };
}

/** The Run's model cost: lux's latest once it reported one, else the harness's sum. */
function modelCost(state: Projection): Pick<Conversation, "costUsd" | "costSource"> {
  if (state.luxCost) {
    return { costUsd: state.luxCost.usd, costSource: { from: "lux", settled: state.luxCost.status === "final" } };
  }
  return { costUsd: state.costUsd, costSource: { from: "agent", settled: false } };
}

/** The latest question still waiting for an answer. */
function openQuestion(state: Projection): QuestionTurn | null {
  let open: QuestionTurn | null = null;
  for (const q of state.questionsById.values()) if (q.answeredAt === null) open = q;
  return open;
}

function openRequest(state: Projection): RepositoryRequestTurn | null {
  let open: RepositoryRequestTurn | null = null;
  for (const r of state.repoRequestsById.values()) if (r.decision === null) open = r;
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
 * The tool the agent last finished before now, if nothing it said came
 * after it: what a steer read at this point waited for. Other people's
 * turns in between (another steer) say nothing about the agent's step.
 */
function toolBefore(turns: readonly Turn[], steer: HumanTurn): string | null {
  for (let i = turns.length - 1; i >= 0; i--) {
    const turn = turns[i]!;
    if (turn === steer || turn.kind === "human") continue;
    if (turn.kind === "tool") return turn.tool;
    if (turn.kind !== "thought" && turn.kind !== "usage" && turn.kind !== "progress" && turn.kind !== "event") return null;
  }
  return null;
}

/**
 * Move a turn already in the transcript to its end — where the event now
 * being folded sits — as the same object, so it keeps its identity (and
 * its React key). The progress row's index follows the shift.
 */
function moveToEnd(state: Projection, turn: Turn): void {
  const { turns } = state;
  const from = turns.indexOf(turn);
  if (from < 0 || from === turns.length - 1) return;
  turns.splice(from, 1);
  turns.push(turn);
  if (state.progressIndex !== null && state.progressIndex > from) state.progressIndex -= 1;
}

function landsOf(value: unknown): SteerLands | null {
  return value === "next_step" || value === "next_turn" ? value : null;
}

function humanTurn(event: PersistedEvent, intent: HumanTurn["intent"], text: string, deliveredAt: string | null): HumanTurn {
  return {
    kind: "human", id: event.eventId, intent, by: humanActor(event), text, at: event.occurredAt, deliveredAt,
    acceptedAt: null, lands: null, read: false, after: null, failed: null, interrupting: false, directiveId: null,
  };
}

/**
 * The person behind an event, when a person did it: their id, and their
 * name if the event says it (a PersonRef `actor` from the API, or a
 * `name` on the envelope's actor).
 */
export function humanActor(event: PersistedEvent): ActorRef | null {
  const actor = event.actor as PersistedEvent["actor"] & { name?: unknown };
  if ((actor?.type !== "human" && actor?.type !== "person") || !actor.id || actor.id === "unknown") return null;
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

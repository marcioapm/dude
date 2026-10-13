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

import type { AskItem, AttachmentInfo, CostOrigin, ItemAnswer, MachineRaisedPayload, MachineRefusedPayload, MachineRequestedPayload, MachineResetPayload, PersistedEvent, Run, RunStatus, SessionStatus, StartStage } from "@dude/domain";
import { EventTypes, START_STAGE_WORDS, TERMINAL_RUN_STATUSES, askItems, isStartStage, itemAnswers } from "@dude/domain";
import type { HumanIntent, PlanItem, ToolOutput } from "@dude/design-system/components";
import { TODO_STATUSES, type ActivityKind, type ToolCallStatus } from "@dude/design-system/tokens";
import { formatDuration } from "@dude/design-system";
import { moveWords, resetNotice, roleLabel } from "../machines.ts";
import { instant } from "../instant.ts";

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
  /**
   * A request_machine call: the sizes from its machine.requested (by name;
   * the pool only when the move changes it; `from` null when no event named
   * it, a refusal dude does not record), and the tool's answer, or its
   * refusal's exact words and code (`not_larger`, `not_live`, lux's).
   */
  machine?: { from: string | null; to: string; pool: string | null; answer?: string; error?: string; code?: string };
  /** The child sessions (subagents) this call started, in the order they started. */
  children: ChildTurn[];
}

/**
 * A child session a tool call started (a task or spawn call's subagent),
 * nested under that call: its own tool calls and messages, how it ended,
 * and a permission it waits on that nothing in the Run can answer.
 */
export interface ChildTurn {
  kind: "child";
  id: string;
  /** The parent's call that started it. */
  callId: string;
  /** `task` (OpenCode, Claude Code) or `spawn` (Codex). */
  childKind: string;
  title: string;
  agentType: string | null;
  model: string | null;
  status: SessionStatus;
  startedAt: string;
  endedAt: string | null;
  /** What its harness said it spent: already in the Run's cost, never added to it. */
  costUsd: number | null;
  /** How it ended, in its harness's words. */
  summary: string | null;
  turns: Array<ToolTurn | MessageTurn>;
  waitingOn: { permission: string; patterns: string[]; since: string } | null;
  /** The child whose call started this one; null under one of the Run's own calls. */
  parentChildId: string | null;
  /**
   * Bumped whenever this child or any child under it changes. The
   * projection mutates children in place, so a renderer compares this,
   * not the object, to skip a child that did not change.
   */
  revision: number;
  /**
   * A terminal Run's settled copy, which may show a status the live child
   * has not reached at the same revision: a renderer compares this too.
   */
  settled: boolean;
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

/** A question the agent stopped on, for a person to answer — or several, asked together. */
export interface QuestionTurn {
  kind: "question";
  id: string;
  questionId: string;
  text: string;
  /** What it asks: one to four questions, each with its choices. */
  items: AskItem[];
  at: string;
  /** Null while it waits for an answer. */
  answeredAt: string | null;
  /** The answer to each item, once answered: the record the turn becomes. */
  answers: ItemAnswer[] | null;
  /** Who answered. */
  answeredBy: ActorRef | null;
  /**
   * Closed unanswered, when: what it asked was decided elsewhere (an
   * escalation decided on the task's banner). Null while it may be answered.
   */
  closedAt: string | null;
  /** Why it closed unanswered: decided on the banner, or withdrawn (its one recipient left the session). */
  closedBy?: "decision" | "withdrawn";
  /** In a brainstorm session: the one member it is put to (ask_person `to`); null for anyone who can chat. */
  to: ActorRef | null;
}

/**
 * The task, as the agent received it. Written by the factory, not a
 * person. For a task's conductor, dude's briefing (`briefing`).
 */
export interface PromptTurn {
  kind: "prompt";
  id: string;
  text: string;
  at: string;
  /** dude's briefing of a task's conductor, not a phase's task. */
  briefing?: true;
  /** A briefing Try again repeated, word for word, to the fresh agent: shown folded, the first one already says it. */
  again?: true;
  /** Images a person gave with the task. */
  attachments: AttachmentInfo[];
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
  /**
   * Steering interrupts; an answer unblocks. They read differently. A
   * message is a person talking with a task's conductor, in its Chat.
   */
  intent: Extract<HumanIntent, "steer" | "answer" | "message">;
  /** Who said it. */
  by: ActorRef | null;
  /** The task's conductor wrote it (a steer of a Run it conducts, or its reply on a pull request), not a person. */
  conductor: boolean;
  /**
   * It came from, or went to, a pull request: a comment addressed to dude
   * (by its GitHub login), or the conductor's reply there. Where to read it.
   */
  github: GithubRef | null;
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
  /** The images sent with it, in order. */
  attachments: AttachmentInfo[];
  /**
   * In a brainstorm session: the question it waits behind (put to another
   * member), so it goes after their answer. Null otherwise.
   */
  heldFor: string | null;
}

export type SteerLands = "next_step" | "next_turn";

/** A Chat message's pull request: which, the comment, and its author's login (none for the conductor's reply). */
export interface GithubRef {
  login: string | null;
  repo: string;
  number: number;
  url: string | null;
}

function githubOf(value: unknown): GithubRef | null {
  if (!value || typeof value !== "object") return null;
  const g = value as Record<string, unknown>;
  if (typeof g.repo !== "string" || typeof g.number !== "number") return null;
  return { login: typeof g.login === "string" ? g.login : null, repo: g.repo, number: g.number,
    url: typeof g.url === "string" && g.url.startsWith("https://") ? g.url : null };
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
 * after it went quiet; or something that happened to its agent (`notice`):
 * it restarted without its conversation, or compacted it. A resume, once
 * timed, says how long it took, its phases in `title`, one per line.
 */
export interface NoticeTurn {
  kind: "notice";
  id: string;
  notice: "parked" | "unparked" | "nudged" | "notice";
  text: string;
  at: string;
  title?: string;
}

/**
 * How the Run ended, when it did not simply finish: it failed (and why, as
 * the orchestrator recorded it), or a person aborted it. A finished Run
 * needs no line of its own — its header says so — but these explain the
 * silence after the last turn, which may otherwise say it was waiting.
 * A conductor's or session agent's failed turn that parked it (`run.failed`
 * with `kept`) is one too, though its transcript goes on after it.
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
  /** A start that failed before the agent ran: where it was, as the row showed it. lux's reason is `why`. */
  start?: StartState;
  /** That start was tried again: a fresh agent took its message. */
  retried?: boolean;
  /** A later agent took what it left without Try again (the hand-over, a next message): nothing is left to try again. */
  taken?: boolean;
  /** A conductor dude started with its wake note, no person's message: dude wakes the next one for the same reasons. */
  woken?: boolean;
}

/**
 * Where an agent is while it starts, from its `run.stage` events: a fresh
 * agent, one taken back up, or one moved to another machine (led by
 * `park`). `stage` is null while lux has named none (an older lux).
 */
export interface StartState {
  kind: "start" | "resume" | "move";
  /**
   * A machine move (machine.requested, or a Reset of a live Run): the size it
   * goes to, by name; the pool only when the move changes it. `stopped` once
   * dude's park has stopped the Run: before that, the agent's own records
   * are its last turn ending, not its return.
   */
  machine?: { role: string; to: string; pool: string | null; moveId: string | null; stopped: boolean };
  stage: StartStage | null;
  stageSince: string | null;
  /** When this start began: the Run's creation, the resume's request, or the move's stop. */
  startedAt: string;
  /** Stages passed in this start, with how long each took (consecutive `since`s, both lux's). */
  done: Array<{ stage: StartStage; tookMs: number }>;
  /** lux's reason with its latest stage (why it waits for a machine), when it gave one. */
  luxReason: string | null;
  /** It failed in `stage`: lux's raw reason, or dude's own sentence (`run.failed`'s error) when lux gave none. */
  failed: { reason: string | null; source: "lux" | "dude" } | null;
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
  machine: { parked: "Paused to move to another machine.", composer: "Moving to another machine; it carries on in a moment." },
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
    // A conductor or session agent parked because its container stopped,
    // or its turn failed, rather than because nobody wrote.
    text: (p) => typeof p.stopped === "string" ? "Its container stopped: your next message resumes it."
      : typeof p.failedTurns === "number" ? "Parked after its turn failed: your next message resumes it."
      : PAUSE_WORDS[p.reason as DudePause]?.parked ?? "Parked.",
  },
  "run.unparked": { notice: "unparked", text: () => "Taken back up where it left off." },
  "run.idle_nudged": { notice: "nudged", text: () => "Quiet for a while: nudged to carry on or ask." },
  [EventTypes.SessionReplaced]: { notice: "notice", text: () => "The agent restarted without its earlier conversation." },
  // The summary it kept stays in the event, for the ledger; it can run to pages.
  [EventTypes.ContextCompacted]: { notice: "notice", text: () => "The agent compacted its context." },
  // A session's names the repository as members know it (KEY/name), a
  // task's by its checkout name; the reason is in plain words, and an event
  // from before it had one says nothing more.
  [EventTypes.RepositoryCloneFailed]: {
    notice: "notice",
    text: (p) => {
      const named = typeof p.key === "string" && typeof p.name === "string" ? `${p.key}/${p.name}` : String(p.repository ?? "A repository");
      return typeof p.reason === "string" && p.reason ? `${named} could not be checked out: ${p.reason}` : `${named} could not be checked out.`;
    },
  },
};

/** A resume's phases (`run.resume.timed`), in order, as its notice's hover names them: lux's in the stages' words, dude's in its own. */
const RESUME_PHASES: ReadonlyArray<readonly [key: string, label: string]> = [
  ["react", "dude asked lux"],
  ["schedule", START_STAGE_WORDS.waiting.done],
  ["image", START_STAGE_WORDS.image.done],
  ["restore", START_STAGE_WORDS.volumes.done],
  ["start", START_STAGE_WORDS.container.done],
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
  | { kind: "paused" } | { kind: "starting"; back?: boolean } | { kind: "next_turn" }
  | { kind: "tool"; tool: string } | { kind: "next_step" };

/** What a message waits on while its agent starts: one sentence for the queued turn and the composer. */
export const READ_WHEN_STARTED = "Will be read when the agent starts.";
export const READ_WHEN_BACK = "Will be read when the agent is back.";
/** A message whose agent never started: why it was not delivered, and what Try again does with it. */
export const NOT_STARTED = "the agent did not start. Try again starts it with this message.";
/** The same message once a later agent took over without Try again: nothing left to try. */
export const NOT_STARTED_TAKEN = "the agent did not start; a later agent took over.";
/** The same message on a failed conductor a later one retries (Try again): its own ledger cannot see that, its Chat can. */
export const NOT_STARTED_RETRIED = "the agent did not start; Try again started a new one.";

/** While its agent starts (not once that start failed): when a message sent now is read. */
export function readWhen(start: StartState | null): string | null {
  if (!start || start.failed) return null;
  return start.kind === "start" ? READ_WHEN_STARTED : READ_WHEN_BACK;
}

export function steerWait(turn: HumanTurn, runStatus: RunStatus, activeTool: string | null, lands: SteerLands | null,
  start: StartState | null = null): SteerWait {
  if (start && !start.failed) return { kind: "starting", back: start.kind !== "start" };
  if (runStatus === "paused") return { kind: "paused" };
  if (runStatus === "pending" || runStatus === "scheduled" || runStatus === "starting") return { kind: "starting" };
  const where = turn.lands ?? lands;
  if (where !== "next_step") return { kind: "next_turn" };
  return activeTool ? { kind: "tool", tool: activeTool } : { kind: "next_step" };
}

/** The composer's hint for a steer sent now, following the same capability. */
export function landsHint(runStatus: RunStatus, activeTool: string | null, lands: SteerLands | null, start: StartState | null = null): string | null {
  const starting = readWhen(start);
  if (starting) return starting.replace(/\.$/, "");
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
  /** Where its agent is while it starts, resumes or moves; null once it runs (or ended). */
  start: StartState | null;
  /** The current Run's start that failed, until a next Run (Try again, a next message) is created: the header says so. */
  startFailed: StartState | null;
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
  /** Child sessions by their id, and each child's tool calls by theirs. */
  childrenById: Map<string, ChildTurn>;
  childToolsByCall: Map<string, ToolTurn>;
  /**
   * Calls made for a child whose parent's call was not heard yet: that
   * call's own event fills one in rather than adding a second turn.
   */
  placeholderCalls: Set<ToolTurn>;
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
  /** A "Taken back up" notice whose start (a resume or a move) is still live: shown once the agent is up, never if it fails. */
  heldUnpark: NoticeTurn | null;
  /**
   * The question a dude ask tool (`agent.tool.dude`) just asked, by the
   * tool's name, until the next harness tool event: dude records the call
   * before the harness reports it, so that report's row goes before it.
   */
  pendingAsk: { tool: string; question: QuestionTurn } | null;
  /** dude briefed this Run (a conductor): the prompt its harness echoes is that briefing, already shown. */
  briefed: boolean;
  /** The last briefing's text as shown, and whether the current Run is Try again's (run.created with retries), which repeats it. */
  lastBriefing: string | null;
  retryRun: boolean;
  /** Where the current Run's agent is while it starts; null once it runs. */
  start: StartState | null;
  /** The current start has had a `run.stage`: a lux that reports stages, so its end gets a summary line. */
  startStaged: boolean;
  /** The current Run's agent has run: a later start is a resume. */
  ran: boolean;
  /** The current Run is a conductor dude started with its wake note (conductor.woken started). */
  startWoken: boolean;
  /** Messages its briefing carries, shown queued until the agent is up. */
  startMessages: HumanTurn[];
  /** The last start that failed, and the messages it never delivered: Try again hands them to a fresh agent. */
  failedStart: { turn: EndedTurn; messages: HumanTurn[] } | null;
  /**
   * After Try again (run.created with retries): the failed Run's unread messages. The new Run's copy of one
   * names its directive (`supersedes`) and becomes that same turn; one the backend skips or refuses stays as it is.
   */
  retrying: Set<HumanTurn>;
  /** Highest cursor folded in; lets a caller skip what it already applied. */
  cursor: number;
}

/**
 * `briefedBefore`: the briefing of the Run this ledger's first Run retries, when that Run's ledger is
 * another (a task's conductors): Try again's briefing repeating it is then one line, as within one ledger.
 */
export function emptyProjection(briefedBefore?: string): Projection {
  return {
    turns: [],
    toolsByCall: new Map(),
    childrenById: new Map(),
    childToolsByCall: new Map(),
    placeholderCalls: new Set(),
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
    heldUnpark: null,
    pendingAsk: null,
    briefed: false,
    lastBriefing: briefedBefore ? withoutMessage(briefedBefore) : null,
    retryRun: false,
    start: null,
    startStaged: false,
    ran: false,
    startWoken: false,
    startMessages: [],
    failedStart: null,
    retrying: new Set(),
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
    // Only the harness's next report of a tool can be the ask's own.
    const ask = state.pendingAsk;
    if (isToolEvent) state.pendingAsk = null;
    // The agent's own records (or lux reporting it running) end a start: it is up. A machine move's start ends
    // only once the Run has stopped for it: until then they are the turn the request ended.
    if (AGENT_UP.has(event.eventType) && !moveNotStopped(state)) endStart(state, event);

    switch (event.eventType) {
      case EventTypes.RunCreated:
        // A new Run of this ledger (a session's next agent, a task's next conductor): it starts afresh.
        state.ran = false;
        state.start = newStart("start", event.occurredAt);
        state.startStaged = false;
        state.startWoken = false;
        state.heldUnpark = null;
        state.retryRun = typeof payload.retries === "string";
        // The last Run's queued messages are not this one's to read: only Try again hands them on, below.
        state.startMessages = [];
        state.retrying = new Set();
        // Try again on a start that failed: the fresh agent has its messages, queued again.
        if (typeof payload.retries === "string" && state.failedStart) {
          state.failedStart.turn.retried = true;
          for (const m of state.failedStart.messages) m.failed = null;
          state.startMessages = state.failedStart.messages;
          state.failedStart = null;
        } else if (state.failedStart) {
          // Any other next agent: what the failed one left is that agent's now.
          state.failedStart.turn.taken = true;
          for (const m of state.failedStart.messages) m.failed = NOT_STARTED_TAKEN;
          state.failedStart = null;
        }
        // What the failed Run was sent and never read: Try again sends it to the fresh agent, the same turn again.
        if (typeof payload.retries === "string") {
          state.retrying = new Set(turns.filter((t): t is HumanTurn =>
            t.kind === "human" && t.directiveId !== null && t.deliveredAt === null && t.failed === null));
        }
        break;

      case EventTypes.BrainstormBriefed:
        // A briefing that is itself the handed-on directive (an answer, which has no Chat copy): its turn is the start's.
        handedOn(state, payload, null);
        break;

      case EventTypes.RunStage: {
        if (!isStartStage(payload.stage)) break;
        const stage = payload.stage;
        const since = typeof payload.since === "string" ? payload.since : event.occurredAt;
        const reason = typeof payload.reason === "string" && payload.reason ? payload.reason : null;
        let start = state.start;
        if (stage === "park") {
          // A move: stopped with its work kept, then started on another machine.
          start = state.start?.machine ? state.start : newStart("move", since);
        } else if (!start) {
          start = newStart(state.ran ? "resume" : "start", event.occurredAt);
        }
        enterStage(start, stage, since);
        // A stage past the park: the Run has stopped for its move.
        if (start.machine && stage !== "park") start.machine.stopped = true;
        start.luxReason = reason;
        state.start = start;
        state.startStaged = true;
        break;
      }

      case EventTypes.RunResumed:
      case "run.unparked": {
        // Taken back up: what follows is a resume (its stages may already have come).
        const at = typeof payload.requestedAt === "string" ? payload.requestedAt : event.occurredAt;
        if (state.start) {
          if (state.start.kind === "start" && state.ran) state.start.kind = "resume";
        } else {
          state.start = newStart("resume", at);
          state.startStaged = false;
        }
        break;
      }

      case EventTypes.RunStarted:
        endStart(state, event);
        break;

      case EventTypes.RunCompleted:
        // Ended without its agent coming up (its container stopped for good): the start is over, its messages unread.
        state.start = null;
        state.startMessages = [];
        state.heldUnpark = null;
        break;

      case EventTypes.ConductorWoken:
        // dude's wake started this conductor: its briefing is dude's note, not a person's message.
        if (payload.started === true && !state.ran) state.startWoken = true;
        break;
    }

    switch (event.eventType) {
      case EventTypes.MachineRequested: {
        const p = payload as unknown as MachineRequestedPayload;
        const m = moveWords(p);
        const requestCall = (t: Turn): t is ToolTurn => t.kind === "tool" && dudeToolName(t.tool) === "request_machine";
        const called = turns.findLast((t): t is ToolTurn => requestCall(t) && t.status === "running")
          ?? turns.findLast((t): t is ToolTurn => requestCall(t) && !t.machine?.from);
        if (called) called.machine = { ...called.machine, from: m.from, to: m.to, pool: m.pool };
        if (p.refused !== undefined) {
          // Refused before any park: the tool card says it in the exact words; asked through the CLI, a notice does.
          if (!called) turns.push({ kind: "notice", id: event.eventId, notice: "notice", at: event.occurredAt, text: p.message ?? p.refused });
          break;
        }
        state.start = moveStart(m, p.moveId ?? null, event.occurredAt);
        state.activity = null;
        break;
      }
      case EventTypes.MachineRaised: {
        const m = moveWords(payload as unknown as MachineRaisedPayload);
        const role = roleLabel(m.role).toLowerCase();
        turns.push({ kind: "notice", id: event.eventId, notice: "notice", at: event.occurredAt,
          text: `Moving the ${role} to ${m.to}${m.pool ? `, in the ${m.pool} pool` : ""}: it keeps ${m.role === "conductor" ? "this conversation and its" : "its conversation and"} checkout; running processes stop.${laterRuns(m.role, m.to, event.taskId !== null)}` });
        break;
      }
      case EventTypes.MachineRefused: {
        const p = payload as unknown as MachineRefusedPayload;
        if (state.start?.machine?.moveId === p.moveId) state.start = null;
        const verb = p.kind === "reset" ? "was to move back to" : "asked for";
        // The backend's words for lux's refusal (sizes and pools by name), made a sentence.
        const why = /[.!?]$/.test(p.message) ? p.message : `${p.message}.`;
        turns.push({ kind: "notice", id: event.eventId, notice: "notice", at: event.occurredAt,
          text: `The ${roleLabel(p.role).toLowerCase()} ${verb} ${p.to.name}; lux kept it on ${p.kept.name}: ${why}` });
        break;
      }
      case EventTypes.MachineReset: {
        const p = payload as unknown as MachineResetPayload;
        turns.push({ kind: "notice", id: event.eventId, notice: "notice", at: event.occurredAt, text: resetNotice(p) });
        // Only a reset that moves a live Run (it has a moveId) has a move to show.
        if (p.moveId) state.start = moveStart(moveWords(p), p.moveId, event.occurredAt);
        break;
      }
      case EventTypes.RunResized: {
        // lux applied the move's size: said once, with how long the move has taken so far. A size lux kept
        // instead is the refusal's to say (machine.refused follows); size names are unique per organization.
        const machine = (payload as { machine?: { name?: unknown } }).machine;
        const move = state.start?.machine;
        if (state.start && move && typeof machine?.name === "string" && machine.name === move.to) {
          turns.push({ kind: "notice", id: event.eventId, notice: "unparked", at: event.occurredAt,
            text: `The ${roleLabel(move.role).toLowerCase()} moved to ${machine.name} in ${formatDuration(msBetween(state.start.startedAt, event.occurredAt))}, with its conversation and work.` });
        }
        break;
      }
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
        // A replay after resume/move must preserve the completed call and its children. A replay is the same
        // call reported again; an id reused for another tool is a new call.
        const prior = toolsByCall.get(callId);
        if (prior?.endedAt && prior.tool === tool) break;
        state.toolCounts.set(tool, (state.toolCounts.get(tool) ?? 0) + 1);
        const placeholder = toolsByCall.get(callId);
        if (placeholder && state.placeholderCalls.has(placeholder)) {
          claimPlaceholder(state, placeholder, tool, payload, event.occurredAt, true);
        } else {
          const turn = toolTurn(callId, tool, payload, event.occurredAt);
          toolsByCall.set(callId, turn);
          addToolTurn(state, turn, ask);
        }

        state.activity = "tool";
        state.activeTool = { name: tool, since: event.occurredAt };
        break;
      }

      case EventTypes.ToolCompleted: {
        const callId = String(payload.callId ?? "");
        const existing = toolsByCall.get(callId);
        const result = resultFrom(payload);
        const machineCall = dudeToolName(existing?.tool ?? tool) === "request_machine";
        const outcome = machineCall ? machineOutcome(result, payload.status === "error") : null;
        if (existing && outcome) {
          const asked = (existing.args as { size?: unknown } | null)?.size;
          existing.machine = { from: existing.machine?.from ?? null, to: existing.machine?.to ?? (typeof asked === "string" ? asked : "a larger machine"),
            pool: existing.machine?.pool ?? outcome.pool, ...outcome.said };
        }
        if (existing) {
          // A repeated completion (a harness taken back up re-reporting it) never re-times a finished call.
          if (!existing.endedAt) {
            claimPlaceholder(state, existing, tool, payload, event.occurredAt, false);
            endTool(existing, payload, event.occurredAt);
            // request_machine's refusal fails its call even when the harness reported it completed.
            if (outcome?.said.error !== undefined) existing.status = "failed";
          }
        } else {
          // The harness buffers some tools and only reports them once
          // finished, so a completion with no matching call is normal rather
          // than a gap in the record.
          const turn = toolTurn(callId || event.eventId, tool, payload, event.occurredAt);
          endTool(turn, payload, event.occurredAt);
          toolsByCall.set(turn.id, turn);
          addToolTurn(state, turn, ask);
        }

        if (state.activeTool?.name === tool) {
          state.activity = "thinking";
          state.activeTool = null;
        }
        break;
      }

      case EventTypes.ChildStarted: {
        touch(state, childOf(state, payload, event));
        break;
      }

      case EventTypes.ChildToolCalled:
      case EventTypes.ChildToolCompleted: {
        const child = childOf(state, payload, event);
        child.waitingOn = null;
        const callId = String(payload.callId ?? event.eventId);
        const childTool = String(payload.tool ?? "tool");
        let turn = state.childToolsByCall.get(`${child.id}/${callId}`);
        if (!turn) {
          turn = toolTurn(callId, childTool, payload, event.occurredAt);
          state.childToolsByCall.set(`${child.id}/${callId}`, turn);
          child.turns.push(turn);
        } else {
          claimPlaceholder(state, turn, childTool, payload, event.occurredAt, event.eventType === EventTypes.ChildToolCalled);
        }
        if (event.eventType === EventTypes.ChildToolCompleted) endTool(turn, payload, event.occurredAt);
        touch(state, child);
        break;
      }

      case EventTypes.ChildMessage: {
        const child = childOf(state, payload, event);
        child.waitingOn = null;
        const text = typeof payload.text === "string" ? payload.text : "";
        if (text.trim()) child.turns.push({ kind: "message", id: event.eventId, text, at: event.occurredAt, contextTokens: null, outputTokens: null });
        touch(state, child);
        break;
      }

      case EventTypes.ChildWaiting: {
        const child = childOf(state, payload, event);
        // One answered for it, or for a call already over, holds nothing.
        if (payload.answered === "allow" || payload.held === false) break;
        child.waitingOn = {
          permission: String(payload.permission ?? payload.on ?? "a permission"),
          patterns: Array.isArray(payload.patterns) ? payload.patterns.map(String) : [],
          since: event.occurredAt,
        };
        touch(state, child);
        break;
      }

      case EventTypes.ChildEnded: {
        const child = childOf(state, payload, event);
        child.waitingOn = null;
        switch (payload.status) {
          case "failed":
            child.status = "failed";
            break;
          case "interrupted":
            child.status = "aborted";
            break;
          default:
            child.status = "completed";
        }
        child.endedAt = event.occurredAt;
        if (typeof payload.costUsd === "number") child.costUsd = payload.costUsd;
        if (typeof payload.summary === "string" && payload.summary.trim()) child.summary = payload.summary;
        for (const turn of child.turns) {
          if (turn.kind === "tool" && turn.status === "running") {
            turn.status = child.status;
            turn.endedAt = event.occurredAt;
          }
        }
        touch(state, child);
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
        const attachments = attachmentsOf(payload.attachments);
        if ((!text.trim() && attachments.length === 0) || state.briefed) break;
        turns.push({ kind: "prompt", id: event.eventId, text, at: event.occurredAt, attachments });
        break;
      }

      case EventTypes.ConductorBriefed:
      case EventTypes.BrainstormBriefed: {
        // dude's note to a new conductor, which ends with the message that
        // started it (shown just before, as its person's turn).
        const text = typeof payload.text === "string" ? payload.text : "";
        state.briefed = true;
        if (!text.trim()) break;
        const briefing: PromptTurn = { kind: "prompt", id: event.eventId, text: withoutMessage(text), at: event.occurredAt, briefing: true, attachments: [] };
        // Try again briefs the fresh agent with the failed one's briefing: the same words, said once in full.
        if (state.retryRun && state.lastBriefing === briefing.text) briefing.again = true;
        else state.lastBriefing = briefing.text;
        turns.push(briefing);
        break;
      }

      case EventTypes.BrainstormTold: {
        // dude's line to a session's agent (who owns it now): dude's words, to the agent.
        const text = typeof payload.text === "string" ? payload.text : "";
        if (text.trim()) turns.push({ kind: "prompt", id: event.eventId, text, at: event.occurredAt, attachments: [] });
        break;
      }

      case EventTypes.ChatMessage: {
        // A message to a task's conductor: the first is in its briefing;
        // each later one is delivered as a steer is (a directive). One from
        // a pull request is signed by its GitHub login; the conductor's
        // reply on a pull request is the conductor's, delivered as written.
        const directiveId = typeof payload.directiveId === "string" ? payload.directiveId : null;
        // Try again handing on a message the failed Run never read: the same turn, now the fresh agent's to read.
        if (handedOn(state, payload, directiveId)) break;
        const github = githubOf(payload.github);
        const turn: HumanTurn = {
          ...humanTurn(event, "message", String(payload.text ?? ""), directiveId ? null : event.occurredAt),
          directiveId,
          conductor: payload.by === "conductor",
          github,
          // A message handed on from an earlier conductor keeps its images.
          attachments: attachmentsOf(payload.attachments),
          heldFor: typeof payload.heldFor === "string" ? payload.heldFor : null,
        };
        if (github?.login) turn.by = { id: `github:${github.login}`, name: `${github.login} (GitHub)` };
        if (directiveId) state.steersByDirective.set(directiveId, turn);
        // The message a starting agent's briefing carries: read once the agent is up, not as it was written.
        if (!directiveId && state.start && !state.ran && !turn.conductor) {
          turn.deliveredAt = null;
          state.startMessages.push(turn);
        }
        turns.push(turn);
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
        // The hand-over settling a message on the old Run: it is no longer one a copy can take.
        if (steer) state.retrying.delete(steer);
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
        // outstanding. The turn's closing totals (`turn: true`) come after
        // its last message: the turn is over, nothing is thinking.
        if (payload.turn === true) {
          state.activity = null;
          state.activeTool = null;
        } else if (!state.activeTool) state.activity = "thinking";
        break;
      }

      case EventTypes.SessionStopped: {
        // The harness ended the turn (turn_complete, or any other reason):
        // the agent is idle until something starts its next one.
        state.activity = null;
        state.activeTool = null;
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
          conductor: payload.by === "conductor",
          interrupting: payload.interrupt === true,
          directiveId,
          attachments: attachmentsOf(payload.attachments),
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
          items: askItems(payload.items, payload.prompt, payload.options),
          at: event.occurredAt,
          answeredAt: null,
          answers: null,
          answeredBy: null,
          closedAt: null,
          to: typeof payload.to === "string" && payload.to ? { id: payload.to, name: typeof payload.toName === "string" ? payload.toName : null } : null,
        };
        state.questionsById.set(turn.questionId, turn);
        turns.push(turn);
        // The agent is not working: it is waiting on a person.
        state.activity = null;
        state.activeTool = null;
        break;
      }

      case EventTypes.QuestionClosed: {
        // Settled elsewhere: no answer will come, and none is asked for.
        const question = state.questionsById.get(String(payload.questionId ?? ""));
        if (question && question.answeredAt === null) {
          question.closedAt = event.occurredAt;
          question.closedBy = payload.by === "withdrawn" ? "withdrawn" : "decision";
        }
        break;
      }

      case EventTypes.QuestionAnswered: {
        const question = state.questionsById.get(String(payload.questionId ?? ""));
        const directiveId = typeof payload.directiveId === "string" ? payload.directiveId : null;
        const attachments = attachmentsOf(payload.attachments);
        const answer = String(payload.answer ?? "");
        if (question) {
          // The question's turn becomes the record: each question with its
          // answer under it. An answer from before answers were kept per
          // question is one question's: the choice it names, or its words.
          question.answeredAt = event.occurredAt;
          question.answeredBy = humanActor(event);
          question.answers = itemAnswers(payload.answers) ?? [answerOfText(question.items[0], answer)];
          // A note, or images, are the person's own words after it.
          const note = typeof payload.note === "string" ? payload.note : "";
          if (note || attachments.length > 0) {
            const turn = { ...humanTurn(event, "message", note, directiveId ? null : event.occurredAt), directiveId, attachments };
            if (directiveId !== null) state.steersByDirective.set(directiveId, turn);
            turns.push(turn);
          }
          break;
        }
        // Delivered the way a steer is: queued until the agent takes it.
        const turn = { ...humanTurn(event, "answer", answer, directiveId ? null : event.occurredAt), directiveId, attachments };
        if (directiveId !== null) state.steersByDirective.set(directiveId, turn);
        turns.push(turn);
        break;
      }

      case "run.parked":
      case "run.unparked":
      case "run.idle_nudged":
      case EventTypes.SessionReplaced:
      case EventTypes.ContextCompacted:
      case EventTypes.RepositoryCloneFailed: {
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
        // While its start is live the agent is not back yet: the notice waits for it (endStart).
        if (notice === "parked") placeHeldUnpark(state, event.occurredAt);
        if (notice === "unparked" && state.start && !state.start.failed) state.heldUnpark = turn;
        else turns.push(turn);
        if (notice === "parked") {
          state.activity = null;
          state.activeTool = null;
          state.untimedUnpark = null;
          state.start = null;
        }
        if (notice === "unparked") {
          if (typeof payload.epoch === "number") state.untimedUnparks.set(payload.epoch, turn);
          else state.untimedUnpark = turn;
        }
        break;
      }

      case EventTypes.RunPaused:
        placeHeldUnpark(state, event.occurredAt);
        state.untimedUnpark = null;
        // dude's stop for a machine move (run.paused, confirmed) is the move's park done; any other pause ends a start.
        if (state.start?.machine && payload.confirmed === true) {
          state.start.machine.stopped = true;
          state.activity = null;
          state.activeTool = null;
        } else if (state.start && !state.start.failed) state.start = null;
        break;

      case EventTypes.ServersChanged:
        // lux reports the Run running (after a wait for a host, a resume or a move): it is up. Not a move's Run before its stop.
        if (payload.change === "state" && payload.luxState === "running" && !moveNotStopped(state)) endStart(state, event);
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
        // A conductor's or session agent's failed turn that parked it (kept): its turn failed, not the Run.
        const failedText = payload.kept === true ? "Its turn failed" : "Failed";
        const ended: EndedTurn = {
          kind: "ended", id: event.eventId, outcome: failed ? "failed" : "aborted",
          text: failed ? (why ? `${failedText}: ${why}` : `${failedText}.`) : why ? `Aborted: ${why}` : "Aborted.",
          at: event.occurredAt,
          by: failed ? null : humanActor(event),
          why: why || null,
        };
        // A start that failed before the agent ran: the row's stages, failed in the one it was in, with lux's own reason.
        // A move whose Run failed at its stop (park: a resumePolicy that does not resume it) never began a start.
        const starting = state.start;
        const stage = isStartStage(payload.stage) && payload.stage !== "park" ? payload.stage : starting?.stage ?? null;
        if (failed && starting && payload.kept !== true && stage !== "park") {
          if (stage) enterStage(starting, stage, event.occurredAt);
          const luxReason = typeof payload.reason === "string" && payload.reason.trim() ? payload.reason : null;
          starting.failed = luxReason ? { reason: luxReason, source: "lux" } : { reason: ended.why, source: "dude" };
          ended.start = starting;
          // Only the wake's own first start, or a move of it before the agent ran: a woken conductor that ran and then
          // failed resuming for a person's message keeps Try again.
          if (state.startWoken && !state.ran) ended.woken = true;
          for (const m of state.startMessages) if (m.deliveredAt === null) m.failed = NOT_STARTED;
          state.failedStart = { turn: ended, messages: state.startMessages.filter((m) => m.deliveredAt === null) };
          state.startMessages = [];
        }
        state.start = null;
        // A failed resume or move shows its failure, never "Taken back up".
        state.heldUnpark = null;
        turns.push(ended);
        state.activity = null;
        state.activeTool = null;
        break;
      }

      case "agent.tool.dude": {
        // dude's record of one of its tools, written while handling the
        // call: an ask (ask_person, or a conductor's decide, which returns a
        // question only when it asks) names the open question it just
        // asked. decide_escalation names the answered one it acts on.
        const name = String(payload.tool ?? "");
        const asks = name === "ask_person" || name === "decide";
        const result = payload.result as Record<string, unknown> | null | undefined;
        const question = asks && typeof result?.questionId === "string" ? state.questionsById.get(result.questionId) : undefined;
        const open = question !== undefined && question.answeredAt === null && question.closedAt === null;
        state.pendingAsk = open ? { tool: name, question } : null;
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
      start: shownStart(state.start, runStatus),
      startFailed: state.failedStart?.turn.start ?? null,
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
  // Settles a call and every child under it, to any depth: an ended child
  // keeps its status but its descendants are settled too. Every call, child,
  // turns and children array is copied, because `apply` keeps writing the
  // live ones (a late event pushes into them or ends a call); a child's
  // message turn is never written after it is pushed, so it is shared (the
  // Run's own last message does take its tokens later, but that is not
  // under a call).
  const settleChild = (child: ChildTurn): ChildTurn => ({
    ...child,
    settled: true,
    status: child.status === "running" ? unfinished : child.status,
    waitingOn: null,
    endedAt: child.endedAt ?? child.startedAt,
    turns: child.turns.map((t) => (t.kind === "tool" ? settle(t) : t)),
  });
  const settle = (turn: ToolTurn): ToolTurn => ({
    ...turn,
    ...(turn.status === "running" ? { status: unfinished, endedAt: turn.endedAt ?? turn.startedAt } : {}),
    children: turn.children.map(settleChild),
  });
  return {
    turns: state.turns.map((turn) => (turn.kind === "tool" ? settle(turn) : turn)),
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
    start: null,
    startFailed: state.failedStart?.turn.start ?? null,
  };
}

/**
 * The start as the screens show it. A fresh Run is running to dude only
 * once lux has reported it running (the stream's state event), which can
 * come before any record of its agent: it has started. A resume or a move
 * is running to dude from its request, so only the ledger ends it.
 */
function shownStart(start: StartState | null, runStatus: RunStatus | undefined): StartState | null {
  if (!start || start.failed) return start;
  if (start.kind === "start" && runStatus === "running") return null;
  // A machine move is paused by dude between its park and its resume, once its park is done. A Run already
  // paused (a person's pause, or an idle park) moves at its next resume: nothing is shown until that resume's stages.
  if (runStatus === "paused" && !start.machine?.stopped) return null;
  return start;
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
  for (const q of state.questionsById.values()) if (q.answeredAt === null && q.closedAt === null) open = q;
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

/** The tool's answer, or its refusal's words and code: exactly one of `answer` and `error`. */
type MachineSaid = { answer?: string; error?: string; code?: string };

/**
 * What request_machine answered, from its output: `{size, pool?, answer}`
 * when it moved, `{error, code}` when refused (the JSON route's 422, MCP's
 * structured content), or a refusal's bare words when the harness kept only
 * the text. Null when it said nothing.
 */
function machineOutcome(result: ToolResult | null, failed: boolean): { said: MachineSaid; pool: string | null } | null {
  const raw = (result?.output?.head ?? result?.stdout?.head)?.trim();
  if (!raw) return null;
  let out: Record<string, unknown> = {};
  try {
    const parsed: unknown = JSON.parse(raw);
    if (parsed && typeof parsed === "object") out = parsed as Record<string, unknown>;
  } catch {
    // The refusal's words alone.
  }
  const pool = typeof out.pool === "string" && out.pool ? out.pool : null;
  if (typeof out.error === "string") {
    return { said: { error: out.error, ...(typeof out.code === "string" && out.code ? { code: out.code } : {}) }, pool };
  }
  if (typeof out.answer === "string" && !failed) return { said: { answer: out.answer }, pool };
  return { said: failed ? { error: raw } : { answer: raw }, pool };
}

/**
 * What else a raise changes, as the design's notice says it: later Runs of
 * the role in the task (the implementer's reach the fixer too), or nothing
 * more for a session, whose agent is the one that moved.
 */
function laterRuns(role: string, size: string, inTask: boolean): string {
  if (!inTask) return "";
  if (role === "conductor") return ` Later conductors in this task run on ${size} too.`;
  if (role === "implementer") return ` Later implementer and fixer Runs in this task run on ${size} too.`;
  return ` Later ${roleLabel(role).toLowerCase()} Runs in this task run on ${size} too.`;
}

/** A tool call's turn as its call reports it, running. */
function toolTurn(id: string, tool: string, payload: Record<string, unknown>, at: string): ToolTurn {
  return { kind: "tool", id, tool, args: payload.input ?? null, status: "running", startedAt: at, endedAt: null, result: null, children: [] };
}

/** A tool call's end as its completion reports it. The harness's "error" is the design system's "failed". */
function endTool(turn: ToolTurn, payload: Record<string, unknown>, at: string): void {
  turn.status = payload.status === "error" ? "failed" : "completed";
  turn.endedAt = at;
  turn.result = resultFrom(payload);
}

/**
 * A call made for a child before its own call event, filled in by that
 * call's first event — its call, or its completion from a harness that
 * reports a call only once finished — in place: the same object keeps its
 * id, its place and its children. A completion carries no start, so the
 * child's start stays the call's; args it omits keep what the call had.
 */
function claimPlaceholder(state: Projection, turn: ToolTurn, tool: string, payload: Record<string, unknown>, at: string, called: boolean): void {
  if (!state.placeholderCalls.delete(turn)) return;
  turn.tool = tool;
  turn.args = payload.input ?? turn.args;
  if (called) turn.startedAt = at;
}

/** Marks a child and every child above it changed, for a renderer that skips unchanged ones. */
function touch(state: Projection, child: ChildTurn): void {
  for (let c: ChildTurn | undefined = child; c; c = c.parentChildId ? state.childrenById.get(c.parentChildId) : undefined) c.revision += 1;
}

/**
 * The child an `agent.child.*` event is about, made from it when it is the
 * first heard of the child, and hung under the parent's call. A child of a
 * child hangs under its parent child's call; one whose call is unknown (a
 * harness that reports only once finished) under a call made for it, as a
 * completion with no matching call is.
 */
function childOf(state: Projection, payload: Record<string, unknown>, event: PersistedEvent): ChildTurn {
  const id = String(payload.childId ?? event.eventId);
  const known = state.childrenById.get(id);
  if (known) return known;
  const callId = String(payload.parentCallId ?? payload.callId ?? "");
  const child: ChildTurn = {
    kind: "child",
    id,
    callId,
    childKind: typeof payload.kind === "string" ? payload.kind : "task",
    title: typeof payload.title === "string" ? payload.title : "",
    agentType: typeof payload.agentType === "string" ? payload.agentType : null,
    model: typeof payload.model === "string" ? payload.model : null,
    status: "running",
    startedAt: event.occurredAt,
    endedAt: null,
    costUsd: null,
    summary: null,
    turns: [],
    waitingOn: null,
    parentChildId: null,
    revision: 0,
    settled: false,
  };
  state.childrenById.set(id, child);
  // A child never hangs under itself: that would loop every walk up the tree.
  const parentChild = typeof payload.parentChildId === "string" && payload.parentChildId !== id
    ? state.childrenById.get(payload.parentChildId) : undefined;
  child.parentChildId = parentChild?.id ?? null;
  let call = parentChild ? state.childToolsByCall.get(`${parentChild.id}/${callId}`) : state.toolsByCall.get(callId);
  if (!call) {
    call = toolTurn(callId || id, child.childKind, {}, event.occurredAt);
    state.placeholderCalls.add(call);
    if (parentChild) {
      state.childToolsByCall.set(`${parentChild.id}/${call.id}`, call);
      parentChild.turns.push(call);
    } else {
      state.toolsByCall.set(call.id, call);
      state.turns.push(call);
    }
  }
  call.children.push(child);
  return child;
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
 * dude's briefing without the message it ends with ("## Ana's message"),
 * which the transcript shows as Ana's own turn just before it.
 */
function withoutMessage(briefing: string): string {
  // A brainstorm's briefing ends "## The first message", already its writer's turn.
  const at = briefing.search(/\n## (?:[^\n]*'s message|The first message)\n/);
  return at < 0 ? briefing : briefing.slice(0, at).trimEnd();
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
 * Add a tool call's row; if it is the call that asked `ask.question`, that
 * question moves after the row. Only the agent's words may lie between
 * them (OpenCode writes them when the call's first update arrives); any
 * other turn after the question keeps it where it is.
 */
function addToolTurn(state: Projection, turn: ToolTurn, ask: Projection["pendingAsk"]): void {
  const { turns } = state;
  turns.push(turn);
  if (!ask || dudeToolName(turn.tool) !== ask.tool) return;
  const at = turns.lastIndexOf(ask.question);
  if (at < 0) return;
  for (let i = at + 1; i < turns.length - 1; i++) {
    const kind = turns[i]!.kind;
    if (kind !== "message" && kind !== "thought") return;
  }
  moveToEnd(state, ask.question);
}

// Translators record dude's tools by their bare name; a `mcp__dude__` or
// `dude_` prefix is tolerated for a harness that passes the server's name through.
function dudeToolName(name: string): string {
  return name.replace(/^(?:mcp__dude__|dude_)/, "");
}

/**
 * Move a turn already in the transcript to its end — where the event now
 * being folded sits — as the same object, so it keeps its identity (and
 * its React key). The progress row's index follows the shift.
 */
function moveToEnd(state: Projection, turn: Turn): void {
  const { turns } = state;
  const from = turns.lastIndexOf(turn);
  if (from < 0 || from === turns.length - 1) return;
  turns.splice(from, 1);
  turns.push(turn);
  if (state.progressIndex !== null && state.progressIndex > from) state.progressIndex -= 1;
}

/** The images an event carries (AttachmentInfo, as the orchestrator wrote them); anything malformed is left out. */
function attachmentsOf(value: unknown): AttachmentInfo[] {
  if (!Array.isArray(value)) return [];
  return value.filter((a): a is AttachmentInfo =>
    typeof a === "object" && a !== null && typeof a.id === "string" && typeof a.name === "string" &&
    typeof a.width === "number" && typeof a.height === "number" && typeof a.original === "object" && a.original !== null);
}

function landsOf(value: unknown): SteerLands | null {
  return value === "next_step" || value === "next_turn" ? value : null;
}

/** One question's answer given as text: the choice it names (case and spaces aside, as dude matches it), else the person's words. */
function answerOfText(item: AskItem | undefined, text: string): ItemAnswer {
  const i = item ? item.choices.findIndex((c) => c.label.trim().toLowerCase() === text.trim().toLowerCase()) : -1;
  return i >= 0 ? { choices: [i], text: "" } : { choices: [], text: text.trim() };
}

function humanTurn(event: PersistedEvent, intent: HumanTurn["intent"], text: string, deliveredAt: string | null): HumanTurn {
  return {
    kind: "human", id: event.eventId, intent, by: humanActor(event), conductor: false, github: null, text, at: event.occurredAt, deliveredAt,
    acceptedAt: null, lands: null, read: false, after: null, failed: null, interrupting: false, directiveId: null,
    attachments: [], heldFor: null,
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

/**
 * A copy of a message the failed Run never read, handed on by Try again: the event names the directive it
 * replaces (`supersedes`), and that directive's turn becomes the new copy's (`directiveId`, or with none the
 * fresh agent's start message). False when it names no such unread turn: it is a turn of its own. The turn takes
 * the new attempt's state: its hold (none when the copy names none) and no receipt from the dead agent's harness.
 */
function handedOn(state: Projection, payload: Record<string, unknown>, directiveId: string | null): boolean {
  const earlier = typeof payload.supersedes === "string" ? state.steersByDirective.get(payload.supersedes) : undefined;
  if (!earlier || !state.retrying.delete(earlier)) return false;
  earlier.directiveId = directiveId;
  earlier.heldFor = typeof payload.heldFor === "string" ? payload.heldFor : null;
  earlier.acceptedAt = null;
  earlier.lands = null;
  earlier.read = false;
  earlier.after = null;
  earlier.interrupting = false;
  if (directiveId) state.steersByDirective.set(directiveId, earlier);
  else if (state.start && !state.ran) state.startMessages.push(earlier);
  return true;
}

/** Events that say the agent is up: what it wrote or took, or the harness taking its input. */
const AGENT_UP: ReadonlySet<string> = new Set([
  EventTypes.AgentMessage, EventTypes.AgentThought, EventTypes.ToolCalled, EventTypes.ToolCompleted, EventTypes.PlanUpdated,
  EventTypes.ModelRequestCompleted, EventTypes.SessionStarted, EventTypes.PromptDelivered, EventTypes.DirectiveAccepted,
  EventTypes.DirectiveDelivered, EventTypes.RunResumeTimed,
]);

/**
 * The agent is up: its start is over. A fresh start that lux staged ends in
 * a line of its own ("Started in 31s.", its stages in the hover, the resume
 * line's words); a resume has its "Taken back up" line already. The
 * messages its briefing carried are read now.
 */
function endStart(state: Projection, event: PersistedEvent): void {
  const start = state.start;
  state.ran = true;
  if (!start || start.failed) return;
  state.start = null;
  for (const m of state.startMessages) if (m.deliveredAt === null && !m.failed) m.deliveredAt = event.occurredAt;
  state.startMessages = [];
  placeHeldUnpark(state, event.occurredAt);
  if (start.kind !== "start" || !state.startStaged) return;
  const lines = start.done.map((d) => `${START_STAGE_WORDS[d.stage].done} ${formatDuration(d.tookMs)}`);
  if (start.stage && start.stageSince) {
    lines.push(`${START_STAGE_WORDS[start.stage].done} ${formatDuration(msBetween(start.stageSince, event.occurredAt))}`);
  }
  state.turns.push({
    kind: "notice", id: `${event.eventId}:started`, notice: "notice", at: event.occurredAt, title: lines.join("\n"),
    text: `Started in ${formatDuration(msBetween(start.startedAt, event.occurredAt))}.`,
  });
}

/**
 * A held "Taken back up" goes into the transcript at `at`: the agent is up, or was parked or paused again (dude
 * parks only an agent that ran). A failed or ended start drops it instead.
 */
function placeHeldUnpark(state: Projection, at: string): void {
  if (!state.heldUnpark) return;
  state.heldUnpark.at = at;
  state.turns.push(state.heldUnpark);
  state.heldUnpark = null;
}

function newStart(kind: StartState["kind"], startedAt: string): StartState {
  return { kind, stage: null, stageSince: null, startedAt, done: [], luxReason: null, failed: null };
}

/** A machine move's start, parking from `at`. */
function moveStart(m: { role: string; to: string; pool: string | null }, moveId: string | null, at: string): StartState {
  const start = newStart("move", at);
  start.machine = { role: m.role, to: m.to, pool: m.pool, moveId, stopped: false };
  enterStage(start, "park", at);
  return start;
}

/** A machine move whose Run has not stopped yet: the agent's records are still the turn the request ended. */
function moveNotStopped(state: Projection): boolean {
  return !!state.start?.machine && !state.start.machine.stopped;
}

/** The start is in `stage` from `since`: the stage it was in, if another, is done, timed by the two `since`s. */
function enterStage(start: StartState, stage: StartStage, since: string): void {
  if (start.stage === stage) return;
  if (start.stage && start.stageSince) start.done.push({ stage: start.stage, tookMs: msBetween(start.stageSince, since) });
  start.stage = stage;
  start.stageSince = since;
}

function msBetween(from: string, to: string): number {
  return Math.max(0, instant(to) - instant(from));
}

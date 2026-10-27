/**
 * A transcript's turns as the Run page draws them, for every screen that
 * shows an agent's ledger: a task's Run, a session, an automation run's
 * activity. One renderer: `renderTurn` draws each turn, `asides` folds the
 * runs of tool calls and thoughts between messages, `interleaved` places a
 * screen's own lines among them by time.
 */

import { Fragment, useState, type ReactNode } from "react";
import { ChatEvent, ChatMessage, ChatNotice, ChatProgress, MentionChip, QuestionCard, ThinkingBlock, ToolCallCard } from "@dude/design-system/components";
import type { QuestionSubmission } from "@dude/design-system/components";
import { Button, Callout } from "@dude/design-system/primitives";
import { firstName, type IconName } from "@dude/design-system";
import type { AgentRole, AttachmentInfo } from "@dude/domain";
import {
  READ_WHEN_BACK, READ_WHEN_STARTED, actorName, toolLabel, type GithubRef, type HumanTurn, type PromptTurn, type QuestionTurn, type SteerWait, type ToolTurn, type Turn,
} from "../api/conversation.ts";
import { SetupOutput } from "../startCue.tsx";
import { ToolWithChildren } from "../childThreads.tsx";
import { useAnswerDraft } from "../answerDraft.ts";
import type { People } from "../people.tsx";
import { instant } from "../instant.ts";
import { DudeMark } from "../DudeMark.tsx";
import { TurnImages, limitsHint, type ImageTray, type SentImages } from "../hooks/useImages.tsx";
import { DigestView, NoteToldView, PeerTurnView, PeerWaitView, noPeers, peerTool, type PeerContext } from "../coordination.tsx";

/**
 * The conductor's turns (grouped into asides) and the Chat's other lines,
 * merged by time: a line goes before the first turn that came after it.
 * Stable: turns keep their order, and lines theirs.
 */
export function interleaved(groups: ReadonlyArray<Turn | Turn[]>, lines: ReadonlyArray<{ id: string; at: string; node: ReactNode }>):
  Array<{ group: Turn | Turn[] } | { id: string; at: string; node: ReactNode }> {
  const out: Array<{ group: Turn | Turn[] } | { id: string; at: string; node: ReactNode }> = [];
  let i = 0;
  for (const group of groups) {
    const turn = Array.isArray(group) ? group[0]! : group;
    let at: string | null = null;
    if ("at" in turn) at = turn.at;
    else if ("startedAt" in turn) at = turn.startedAt;
    while (i < lines.length && at !== null && instant(lines[i]!.at) < instant(at)) out.push(lines[i++]!);
    out.push({ group });
  }
  while (i < lines.length) out.push(lines[i++]!);
  return out;
}

/**
 * The turns, with each run of what an agent does between its messages —
 * tool calls, thoughts, the events and progress it records — gathered
 * into one group: a `ChatAside` puts them on the message column, stacked
 * close, with a turn's air above and below the run, as the design system
 * draws it. Never one of them alone at the transcript's edge.
 */
export function asides(turns: readonly Turn[]): Array<Turn | Turn[]> {
  const out: Array<Turn | Turn[]> = [];
  for (const turn of turns) {
    if (turn.kind !== "tool" && turn.kind !== "thought" && turn.kind !== "event" && turn.kind !== "progress") {
      out.push(turn);
      continue;
    }
    const last = out[out.length - 1];
    if (Array.isArray(last)) last.push(turn);
    else out.push([turn]);
  }
  return out;
}

/** A live Run's queued steers: what each waits for, and sending one again (interrupting, or after a failure). */
export interface SteerActions {
  wait: (turn: HumanTurn) => SteerWait;
  resend: (turn: HumanTurn, interrupt: boolean) => void;
}

/** The one line under a queued steer. */
export function pendingReason(wait: SteerWait) {
  switch (wait.kind) {
    case "tool": return <>Lands after <b>{toolLabel(wait.tool)}</b> finishes.</>;
    case "next_step": return "Lands at the agent's next step.";
    case "next_turn": return "This agent reads messages only between turns — lands when this turn ends.";
    case "paused": return "Lands when the run resumes.";
    case "starting": return wait.back ? READ_WHEN_BACK : READ_WHEN_STARTED;
  }
}

/** A turn whose images the viewer shows. */
export type ViewedTurn = HumanTurn | { kind: "prompt"; attachments: AttachmentInfo[]; at: string };

/** How a turn's images are shown, and opened. */
export interface ShownImages {
  images: Pick<SentImages, "sent" | "mounted" | "visible">;
  open: (turn: ViewedTurn, index: number) => void;
}

/** "15:52:40": a read time, in the viewer's line. */
export function clock(at: string): string {
  return new Date(at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
}

/** A briefing Try again repeated to the fresh agent: one quiet line that opens to the full card. */
function BriefedAgain({ turn, dude, shown }: { turn: PromptTurn; dude: string; shown: ShownImages | undefined }) {
  const [open, setOpen] = useState(false);
  return (
    <div data-testid="chat-briefed-again">
      <Button size="sm" variant="quiet" leadingIcon="retry" aria-expanded={open} onClick={() => setOpen(!open)} data-testid="briefed-again-toggle">
        {BRIEFED_AGAIN}
      </Button>
      {open ? (
        <ChatMessage role="system" name={dude} avatar={<DudeMark size="fill" />} intent="briefing" content={turn.text} startedAt={turn.at}
          data-testid="chat-briefing" {...turnImages(turn, shown)} />
      ) : null}
    </div>
  );
}

export const BRIEFED_AGAIN = "Briefed again with the same message";

/** A turn's images, as ChatMessage's `attachments` prop; none when it has none. */
function turnImages(turn: ViewedTurn, shown: ShownImages | undefined) {
  if (turn.attachments.length === 0 || !shown) return {};
  return { attachments: <TurnImages attachments={turn.attachments} images={shown.images} onOpen={(i) => shown.open(turn, i)} /> };
}

/** Where a Chat message was written on GitHub, or where the conductor's reply went: the pull request, linked to the comment. */
function GithubSource({ github, reply }: { github: GithubRef; reply: boolean }) {
  const pr = `${github.repo}#${github.number}`;
  const what = reply ? <>Replied on {pr}</> : <>On {pr}</>;
  return (
    <div className="githubSource" data-testid="github-source">
      {github.url ? <a href={github.url} target="_blank" rel="noreferrer noopener">{what} on GitHub</a> : <>{what} on GitHub</>}
    </div>
  );
}

/** "Márcio · steer to Implement · 15:52": who sent a message's images, to whom, when. */
export function viewedContext(turn: ViewedTurn, people: People, agent: string, dude: string): string {
  const at = new Date(turn.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  if (turn.kind === "prompt") return `${dude} · task to ${agent} · ${at}`;
  const who = actorName(turn.by, people.names);
  return `${who ? firstName(who) : "Someone"} · ${turn.intent} to ${agent} · ${at}`;
}

/**
 * The people a session's message mentioned: each drawn as its chip in place
 * of its "@Name" run in the text (`inText`), and any whose name is not in
 * the text as chips under it (`rest`), so a name is never shown twice. The
 * reader's own is marked.
 */
export function mentionChips(ids: readonly string[], text: string, people: People) {
  const inText: { name: string; chip: ReactNode }[] = [];
  const rest: ReactNode[] = [];
  for (const id of ids) {
    const person = people.byId.get(id) ?? { id, name: people.names.get(id) ?? "Someone" };
    const chip = <MentionChip key={id} person={person} you={id === people.you} />;
    if (text.includes(`@${person.name}`)) inText.push({ name: person.name, chip });
    else rest.push(chip);
  }
  return { inText, rest: rest.length > 0 ? <span data-testid="mention-chips">{rest}</span> : null };
}

/**
 * Who an agent's turns are signed by when it is no role of dude's (an
 * automation): its name, the muted word after it, and its face.
 */
export interface Speaker {
  name: string;
  roleLabel: string;
  avatar: ReactNode;
}

/**
 * A dude tool an automation's agent called, as its card shows it: post_to_chat
 * by the Slack glyph and its channel, create_task by "+" with its project and
 * title, and a refusal's words from the tool server's record (ToolTurn.dude).
 */
function dudeToolView(turn: ToolTurn): { icon?: IconName; summary?: ReactNode; error?: string } | null {
  if (!turn.dude) return null;
  const name = turn.tool.replace(/^dude_/, "");
  const args = (turn.args && typeof turn.args === "object" ? turn.args : {}) as Record<string, unknown>;
  const channel = turn.dude.posted?.channel ?? (typeof args.channel === "string" ? args.channel : "");
  const text = (k: string) => (typeof args[k] === "string" ? args[k] as string : "");
  // create_task takes no project: the key of the task it made names it (WC-231 → WC).
  const made = turn.dude.result && typeof turn.dude.result === "object" ? (turn.dude.result as Record<string, unknown>).key : undefined;
  const projectKey = typeof made === "string" ? made.replace(/-\d+$/, "") : "";
  return {
    ...(name === "post_to_chat" ? { icon: "slack" as const, summary: <span className="ds-mono">{channel}</span> } : {}),
    ...(name === "create_task" ? { icon: "plus" as const, summary: [projectKey, text("title")].filter(Boolean).join(" · ") } : {}),
    ...(turn.dude.error ? { error: turn.dude.error } : {}),
  };
}

export function renderTurn(turn: Turn, role: AgentRole, contextWindow: number, ended: boolean, people: People, dude: string,
  decide?: (requestId: string, approve: boolean) => void, waitingOn?: string, steer?: SteerActions, shown?: ShownImages,
  toolNote?: (turn: ToolTurn) => ReactNode, ask?: AskActions, peer?: PeerContext, now?: number, speaker?: Speaker) {
  switch (turn.kind) {
    case "peer":
      return <PeerTurnView key={turn.id} turn={turn} ctx={peer ?? noPeers(role, people)} />;
    case "peerWait":
      return <PeerWaitView key={turn.id} turn={turn} now={now} />;
    case "noteTold":
      return <NoteToldView key={turn.id} turn={turn} ctx={peer ?? noPeers(role, people)} />;
    case "digest":
      return <DigestView key={turn.id} turn={turn} ctx={peer ?? noPeers(role, people)} />;
    case "repositoryRequest": {
      // Asked of a person, like a question: approve brings it into the Run.
      const what = `${turn.access === "write" ? "Change" : "Read"} ${turn.repository}`;
      return (
        <QuestionCard
          key={turn.id}
          data-testid="repository-request"
          role={role}
          // The agent's reason, quoted: its words cannot pass for the card's.
          text={`**${what}?**\n\n${turn.reason.split("\n").map((line) => `> ${line}`).join("\n")}`}
          options={turn.decision === null && !ended ? ["Approve", "Decline"] : []}
          kind="request"
          askedAt={turn.at}
          answeredAt={turn.decidedAt}
          dismissed={ended && turn.decision === null}
          waitingOn={waitingOn}
          {...(decide && turn.decision === null && !ended
            ? { onChoose: (choice: string) => decide(turn.requestId, choice === "Approve") }
            : {})}
        />
      );
    }
    case "notice":
      return <ChatNotice key={turn.id} data-testid="chat-notice" kind={turn.notice} text={turn.text} at={turn.at} by={dude}
        {...(turn.title ? { title: turn.title } : {})} />;
    case "ended": {
      // Where the transcript stops, and why: a failure in its tone, not a
      // margin note (ChatNotice has no tones).
      const callout = (
        <Callout key={turn.id} data-testid="chat-ended" data-outcome={turn.outcome}
          tone={turn.outcome === "failed" ? "danger" : "neutral"}>
          {turn.outcome === "aborted" ? abortedBy(turn, people) : turn.text}
        </Callout>
      );
      return turn.setup ? <Fragment key={turn.id}>{callout}<SetupOutput setup={turn.setup} /></Fragment> : callout;
    }
    case "progress":
      // One row, updated in place as the agent reports.
      return (
        <ChatProgress key={turn.id} data-testid="chat-progress" role={role} done={turn.done} of={turn.of}
          step={turn.step} at={turn.at} startedAt={turn.startedAt} ended={ended} />
      );
    case "event":
      return <ChatEvent key={turn.id} data-testid="chat-event" role={role} type={turn.type} data={turn.data} at={turn.at} />;
    case "question":
      // A Run that ended on an unanswered question will never hear back.
      return <AskTurn key={turn.id} turn={turn} role={role} ended={ended} people={people} waitingOn={waitingOn} ask={ask} />;
    case "prompt":
      // Try again's fresh agent, briefed with the failed one's words: one line, the briefing a click away.
      if (turn.again) return <BriefedAgain key={turn.id} turn={turn} dude={dude} shown={shown} />;
      // Written by the factory, not a person: the avatar and name say so.
      // A conductor's is dude's briefing of it, tagged so.
      return (
        <ChatMessage key={turn.id} role="system" name={dude} avatar={<DudeMark size="fill" />} intent={turn.briefing ? "briefing" : "prompt"}
          content={turn.text} startedAt={turn.at} data-testid={turn.briefing ? "chat-briefing" : undefined} {...turnImages(turn, shown)} />
      );
    case "told":
      // What dude put in front of the agent's input that no one typed: dude's, in the briefing's frame.
      return (
        <ChatMessage key={turn.id} role="system" name={dude} avatar={<DudeMark size="fill" />} intent="told"
          content={turn.text} startedAt={turn.at} data-testid="chat-told" />
      );
    case "message":
      return (
        <ChatMessage
          key={turn.id}
          data-testid={role === "conductor" ? "conductor-turn" : speaker ? "agent-turn" : undefined}
          role={role}
          content={turn.text}
          startedAt={turn.at}
          {...(speaker ? { name: speaker.name, roleLabel: speaker.roleLabel, avatar: speaker.avatar } : {})}
          {...tokenFoot(turn, contextWindow)}
        />
      );
    case "thought":
      // Only when the thought ended is known, not how long it took.
      return <ThinkingBlock key={turn.id} text={turn.text} />;
    case "usage":
      // A turn's totals, at its end: a quiet foot, not a message.
      return (
        <ChatMessage
          key={turn.id}
          role={role}
          continued
          startedAt={turn.at}
          {...tokenFoot(turn, contextWindow)}
          costUsd={null}
        />
      );
    case "human": {
      // Signed: the person's face and name when known; "Someone" only when the ledger kept no one. A
      // conductor's steer is the conductor's: its face, colour and name, never a person's. One from a
      // pull request is signed by its GitHub login, and says where it was written.
      const name = turn.conductor ? null : actorName(turn.by, people.names);
      const person = name && turn.by && !turn.github ? { ...(people.byId.get(turn.by.id) ?? {}), id: turn.by.id, name } : undefined;
      const wait = steer && (turn.intent === "steer" || turn.intent === "message") && turn.deliveredAt === null && !turn.failed ? steer.wait(turn) : null;
      // Only where there is a turn to stop, and not twice; never a message in Chat, which starts a turn.
      const interruptible = turn.intent === "steer" && (wait?.kind === "tool" || wait?.kind === "next_step" || wait?.kind === "next_turn") && !turn.interrupting;
      const chips = turn.mentions?.length ? mentionChips(turn.mentions, turn.text, people) : null;
      const source = turn.github ? <GithubSource github={turn.github} reply={turn.conductor} /> : null;
      const under = source && chips?.rest ? <>{source}{chips.rest}</> : source ?? chips?.rest ?? null;
      return (
        <ChatMessage
          key={turn.id}
          data-testid="human-turn"
          data-by={turn.conductor ? "conductor" : turn.github ? "github" : undefined}
          role={turn.conductor ? "conductor" : turn.github ? "integration" : "human"}
          intent={turn.intent}
          content={turn.text}
          startedAt={turn.at}
          deliveredAt={turn.deliveredAt}
          read={turn.read}
          readAfter={turn.after ? toolLabel(turn.after) : undefined}
          failed={turn.failed ?? undefined}
          {...(turn.heldFor && turn.deliveredAt === null && !turn.failed
            ? { pendingReason: "Queued: goes after the answer the agent is waiting for." }
            : wait ? { pendingReason: pendingReason(wait) } : {})}
          {...(interruptible && steer ? { onInterrupt: () => steer.resend(turn, true) } : {})}
          {...(turn.failed && steer && turn.intent === "steer" ? { onRetry: () => steer.resend(turn, false) } : {})}
          person={person}
          name={turn.conductor ? "Conductor" : name ?? "Someone"}
          {...turnImages(turn, shown)}
          {...(chips?.inText.length ? { mentions: chips.inText } : {})}
          {...(under ? { attachments: under } : {})}
        />
      );
    }
    case "tool": {
      // A coordination tool, a dude tool of an automation's, or a refused message shows its own card; its child sessions, if it started any, nest under it.
      const coordination: { icon?: IconName; summary?: ReactNode; result?: ReactNode; error?: string } | null = peerTool(turn, peer) ?? dudeToolView(turn);
      const error = turn.refusal ?? coordination?.error;
      const card = coordination || turn.refusal !== undefined ? (
        <ToolCallCard
          name={turn.tool}
          args={turn.args}
          status={turn.status}
          startedAt={turn.startedAt}
          endedAt={turn.endedAt}
          output={coordination?.result ? undefined : turn.result?.output ?? turn.result?.stdout}
          stderr={turn.result?.stderr}
          exitCode={turn.refusal !== undefined ? undefined : turn.result?.exitCode}
          {...(coordination?.summary ? { summary: coordination.summary } : {})}
          {...(coordination?.icon ? { icon: coordination.icon } : {})}
          {...(coordination?.result ? { result: coordination.result } : {})}
          {...(error !== undefined ? { error } : {})}
          note={toolNote?.(turn)}
        />
      ) : undefined;
      return <ToolWithChildren key={turn.id} turn={turn} depth={1} note={toolNote?.(turn)} card={card} />;
    }
  }
}

/** The question's turn in a transcript (AskTurn marks it), for the transcript to keep in view. */
export const questionTurnSelector = (questionId: string) => `[data-question="${CSS.escape(questionId)}"]`;

/** Whom a session's question waits on: none when it is put to you, else their first name. */
function questionFor(to: { id: string; name: string | null }, people: People): string | undefined {
  if (to.id === people.you) return undefined;
  const name = actorName(to, people.names);
  return name ? firstName(name) : "someone else";
}

/** How a question waiting on the reader is answered: its form's Send, and the note's image tray. */
export interface AskActions {
  answer: (turn: QuestionTurn, submission: QuestionSubmission) => Promise<boolean>;
  tray?: Pick<ImageTray, "attachments" | "add" | "remove" | "disabledReason" | "limits"> | undefined;
}

/**
 * An agent's question as its turn: the answer form while it waits on the
 * reader (its draft kept in this browser until sent), the record once
 * answered, as it was otherwise.
 */
function AskTurn({ turn, role, ended, people, waitingOn, ask }: {
  turn: QuestionTurn; role: AgentRole; ended: boolean; people: People; waitingOn: string | undefined; ask: AskActions | undefined;
}) {
  const { key, draft, keep, forget } = useAnswerDraft(people.you, turn, ended);
  const whom = turn.to ? questionFor(turn.to, people) : waitingOn;
  const by = actorName(turn.answeredBy, people.names);
  const open = !ended && turn.answeredAt === null && turn.closedAt === null && whom === undefined && ask !== undefined;
  // The note's images are for several questions; one question has no note.
  const tray = open && turn.items.length > 1 ? ask.tray : undefined;
  return (
    <QuestionCard
      // The open form is keyed on the draft, so once the person is known it starts from their own; a settled card keeps its node.
      key={open ? key : turn.questionId}
      data-testid="question-turn"
      data-question={turn.questionId}
      role={role}
      text={turn.text}
      items={turn.items}
      askedAt={turn.at}
      answeredAt={turn.answeredAt}
      answers={turn.answers}
      answeredBy={by ? firstName(by) : undefined}
      dismissed={ended && turn.answeredAt === null}
      settledBy={turn.closedAt === null ? undefined : turn.closedBy === "withdrawn" ? "Withdrawn" : "Decided on the banner"}
      waitingOn={whom}
      onlyThey={turn.to !== null}
      {...(open ? {
        onSubmit: async (s: QuestionSubmission) => {
          const ok = await ask.answer(turn, s);
          if (ok) forget();
          return ok;
        },
        draft,
        onDraftChange: keep,
      } : {})}
      {...(tray ? {
        attachments: tray.attachments, onAttachFiles: tray.add, onRemoveAttachment: tray.remove,
        attachAccept: "image/png,image/jpeg,image/webp,image/gif", attachHint: limitsHint(tray.limits), attachDisabledReason: tray.disabledReason,
      } : {})}
    />
  );
}

/** "Aborted by Ana: wrong task" — who stopped it, and why, when known. */
function abortedBy(turn: Extract<Turn, { kind: "ended" }>, people: People): string {
  const name = actorName(turn.by, people.names);
  const who = name ? `Aborted by ${name}` : "Aborted";
  return turn.why ? `${who}: ${turn.why}` : `${who}.`;
}

/** A message's token foot: the context then, against the window when known, and the turn's output. */
function tokenFoot(turn: { contextTokens: number | null; outputTokens: number | null }, contextWindow: number) {
  return {
    contextTokens: turn.contextTokens ?? undefined,
    contextWindowTokens: turn.contextTokens !== null && contextWindow > 0 ? contextWindow : undefined,
    outputTokens: turn.outputTokens ?? undefined,
  };
}

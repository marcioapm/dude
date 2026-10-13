import { Children, useCallback, useEffect, useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import type { AgentRole } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { statusSpec, type Status } from "../tokens/status.ts";
import { Button } from "../primitives/Button.tsx";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { PersonAvatar, type Person } from "./PersonAvatar.tsx";
import { StatusBadge } from "./StatusBadge.tsx";
import { CostDisplay, Duration, TokenCount } from "./Numbers.tsx";
import type { CostOrigin } from "@dude/domain";
import { Cost } from "./Cost.tsx";
import styles from "./ChatTranscript.module.css";

export interface ChatTranscriptSession {
  readonly id: string;
  readonly role: AgentRole;
  /**
   * Any domain status. A Run and a Session are both shown here, and a Run
   * can be `paused` — a state the session vocabulary has no word for, so
   * narrowing to SessionStatus would force callers to mistranslate it.
   */
  readonly status: Status;
  readonly statusLabel?: string | undefined;
  readonly model?: string | undefined;
  /** "WI-2481 · Add retry with backoff…" — enough to know what you are looking at. */
  readonly title?: string | undefined;
  /** The task's raw id. Only in a tooltip; people read `taskKey`. */
  readonly taskId?: string | undefined;
  /** The task's human key ("TEXT-14"), shown before the title. */
  readonly taskKey?: string | undefined;
  readonly repo?: string | undefined;
  readonly branch?: string | undefined;
  readonly startedAt?: string | number | Date | undefined;
  readonly endedAt?: string | number | Date | null | undefined;
  /**
   * `null` when the harness reports no cost (a subscription seat, say).
   * The header then shows "—" for cost and still shows the tokens; it
   * never shows `$0.00` for a run that simply was not priced.
   */
  readonly costUsd?: number | null | undefined;
  /** Who priced `costUsd`, and whether lux has settled it (as on `Cost`). */
  readonly tokensFrom?: CostOrigin | undefined;
  readonly settled?: boolean | undefined;
  readonly budgetUsd?: number | undefined;
  readonly tokens?: number | undefined;
  /** Whom the agent works for: their face carries its tile, in place of the bare tile. */
  readonly owner?: Person | undefined;
  /** Under the title, in place of role · model: "for Ana · sonnet · started 2m ago". */
  readonly subtitle?: ReactNode;
  /** Beside the status, what qualifies it: "Waiting for a host". Keeps its width; the title gives way. */
  readonly statusNote?: ReactNode;
}

export interface ChatTranscriptProps extends Omit<HTMLAttributes<HTMLDivElement>, "children" | "title"> {
  /** Header context. Omit for a headerless transcript embedded elsewhere. */
  readonly session?: ChatTranscriptSession | undefined;
  readonly headerActions?: ReactNode;
  /** Pinned under the header: the agent's plan (`AgentPlan sticky`). */
  readonly pinned?: ReactNode;
  /** Rendered below the scroller, outside it: the composer. */
  readonly footer?: ReactNode;
  /** Monotonic counter of content changes; bump it when turns arrive or stream. */
  readonly revision?: number | undefined;
  /**
   * How many turns there are, for "N new turns". Defaults to the children
   * counted; give it when children group several turns (a `ChatAside`
   * holding a run of tool calls), so each still counts.
   */
  readonly turns?: number | undefined;
  /** Still receiving. Controls aria-live. */
  readonly live?: boolean | undefined;
  /** Fill the parent's height instead of `maxHeight`. */
  readonly fill?: boolean | undefined;
  /**
   * A selector for the turn that waits on the reader (a `QuestionCard`):
   * following the tail never scrolls its top out of view, so it is what a
   * transcript opens on. The person's own scroll, or Jump to latest,
   * lets it go until it names another.
   */
  readonly keepInView?: string | undefined;
  readonly maxHeight?: number | string | undefined;
  readonly emptyMessage?: ReactNode;
  readonly children?: ReactNode;
}

/** Room left above a turn kept in view, so it does not sit flush on the transcript's edge. */
const HELD_MARGIN = 12;
const SCROLL_KEYS: ReadonlySet<string> = new Set(["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " "]);
const DESCENDANT_SCROLL_KEYS: ReadonlySet<string> = new Set(["PageUp", "PageDown", "Home", "End"]);
const FIELD = 'input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"]';

/**
 * Whether an element between `from` and the viewport scrolls itself in the
 * key's direction (a focused code block or tool output): the browser moves
 * it, not the log, so the key is no reason to let the held turn go.
 */
function innerScroller(from: HTMLElement, viewport: HTMLElement, key: string): boolean {
  const down = key === "PageDown" || key === "End";
  for (let el: HTMLElement | null = from; el && el !== viewport; el = el.parentElement) {
    if (el.scrollHeight <= el.clientHeight) continue;
    const overflow = getComputedStyle(el).overflowY;
    if (overflow !== "auto" && overflow !== "scroll") continue;
    if (down ? el.scrollTop + el.clientHeight < el.scrollHeight - 1 : el.scrollTop > 0) return true;
  }
  return false;
}

/**
 * The transcript: the operator's day-to-day screen. A header states what
 * you are looking at (task, role, model, repo/branch, status, cost
 * against budget, elapsed), an optional pinned plan sits under it, the
 * turns scroll, and the composer sits below.
 *
 * Scrolling: follows the tail while you are at the bottom. The moment you
 * scroll up it stops, and a "Jump to latest" pill appears — with a count
 * of turns that arrived while you were reading. Streaming text that grows
 * the last turn also follows, via a ResizeObserver on the content, so a
 * long answer does not run off the bottom of the viewport. A turn waiting on
 * the reader (`keepInView`) bounds the following: its top stays in view, so
 * a question with replies after it still opens where it can be answered.
 */
export function ChatTranscript({
  session,
  headerActions,
  pinned,
  footer,
  revision = 0,
  turns,
  live,
  fill,
  keepInView,
  maxHeight = 560,
  emptyMessage = "No turns yet.",
  className,
  children,
  ...rest
}: ChatTranscriptProps) {
  const viewportRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const [following, setFollowing] = useState(true);
  const followingRef = useRef(true);
  /** Top-level turns that arrived while the operator was scrolled up. */
  const [missed, setMissed] = useState(0);
  /** Anything (including streamed text) changed while scrolled up. */
  const [stale, setStale] = useState(false);
  const lastRevision = useRef(revision);
  const turnCount = turns ?? Children.count(children);
  const lastCount = useRef(turnCount);
  /** The turn kept in view, until the person scrolls or jumps past it. */
  const held = useRef(keepInView);

  // Following stops at the held turn's top: never past it, so it stays read.
  const scrollToBottom = useCallback((smooth: boolean) => {
    const el = viewportRef.current;
    if (!el) return;
    let top = el.scrollHeight;
    const turn = held.current ? el.querySelector<HTMLElement>(held.current) : null;
    if (turn) top = Math.min(top, el.scrollTop + turn.getBoundingClientRect().top - el.getBoundingClientRect().top - HELD_MARGIN);
    el.scrollTo({ top, behavior: smooth ? "smooth" : "auto" });
  }, []);

  // A new turn to keep in view (or none now): follow again, to it.
  useLayoutEffect(() => {
    if (held.current === keepInView) return;
    held.current = keepInView;
    followingRef.current = true;
    setFollowing(true);
    scrollToBottom(false);
  }, [keepInView, scrollToBottom]);

  // The person moving the transcript themselves lets the held turn go.
  const release = () => {
    held.current = undefined;
  };

  // New revision or new turns: follow if following, else remember what was missed.
  useLayoutEffect(() => {
    const revChanged = revision !== lastRevision.current;
    const added = Math.max(0, turnCount - lastCount.current);
    lastRevision.current = revision;
    lastCount.current = turnCount;
    if (!revChanged && added === 0) return;
    if (followingRef.current) {
      scrollToBottom(false);
      return;
    }
    if (added > 0) setMissed((n) => n + added);
    setStale(true);
  }, [revision, turnCount, scrollToBottom]);

  // Content growing without a revision bump (streaming text) still follows.
  useEffect(() => {
    const el = contentRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      if (followingRef.current) scrollToBottom(false);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [scrollToBottom]);

  const onScroll = () => {
    const el = viewportRef.current;
    if (!el) return;
    // Held at the turn kept in view is following too: it is as far as following goes.
    const turn = held.current ? el.querySelector<HTMLElement>(held.current) : null;
    const atHeld = turn !== null && turn.getBoundingClientRect().top - el.getBoundingClientRect().top >= HELD_MARGIN - 2;
    const atBottom = atHeld || el.scrollHeight - el.scrollTop - el.clientHeight < 12;
    if (atBottom !== followingRef.current) {
      followingRef.current = atBottom;
      setFollowing(atBottom);
      if (atBottom) {
        setMissed(0);
        setStale(false);
      }
    }
  };

  const jump = () => {
    release();
    followingRef.current = true;
    setFollowing(true);
    setMissed(0);
    setStale(false);
    scrollToBottom(true);
  };

  const hasChildren = children !== undefined && children !== null && children !== false;

  return (
    <div className={cx(styles["root"], fill && styles["fill"], className)} style={fill ? undefined : { maxHeight }} data-following={following ? "true" : "false"} {...rest}>
      {session ? <SessionHeader session={session} actions={headerActions} /> : null}
      {pinned !== undefined ? <div className={styles["pinned"]}>{pinned}</div> : null}
      <div className={styles["scroller"]}>
        <div ref={viewportRef} className={styles["viewport"]} onScroll={onScroll} onWheel={release} onTouchMove={release}
          onKeyDown={(e) => {
            if (!e.currentTarget.contains(e.target as Node)) return;
            if (e.defaultPrevented || !SCROLL_KEYS.has(e.key)) return;
            const target = e.target as HTMLElement;
            const own = target === e.currentTarget;
            // Descendants own Space and arrows (activation, choice navigation); page/home/end bubble to the log.
            if (!own && !DESCENDANT_SCROLL_KEYS.has(e.key)) return;
            // Fields keep their keys (caret, value) whether or not they consume them; widgets that use Home/End preventDefault.
            if (target.closest(FIELD)) return;
            if (innerScroller(target, e.currentTarget, e.key)) return;
            release();
          }}
          // A drag of the scrollbar, which is the viewport's own: not a click inside a turn.
          onPointerDown={(e) => e.target === e.currentTarget && release()}
          role="log" aria-live={live ? "polite" : "off"} aria-relevant="additions text" tabIndex={0}>
          <div ref={contentRef} className={styles["content"]}>
            {hasChildren ? children : <div className={styles["empty"]}>{emptyMessage}</div>}
          </div>
        </div>
        {!following ? (
          <Button size="sm" variant="secondary" leadingIcon="arrow-down-to-line" className={cx(styles["jump"], stale && styles["jumpStale"])} onClick={jump}>
            {missed > 0 ? `${missed} new ${missed === 1 ? "turn" : "turns"} · Jump to latest` : stale ? "Still writing · Jump to latest" : "Jump to latest"}
          </Button>
        ) : null}
      </div>
      {footer !== undefined ? <div className={styles["footer"]}>{footer}</div> : null}
    </div>
  );
}

export interface SessionHeaderProps {
  readonly session: ChatTranscriptSession;
  readonly actions?: ReactNode;
}

/**
 * Whose agent this is and what it is doing: the transcript's own header,
 * and — above a session's Conversation / Changes tabs — the header both share.
 */
export function SessionHeader({ session, actions }: SessionHeaderProps) {
  // `live` and `needsHuman` are properties of the status itself, so the
  // header asks the spec rather than re-listing which statuses count.
  const spec = statusSpec(session.status);
  // Raw ids help whoever debugs, not whoever reads: they live in a tooltip.
  const ids = [session.taskId ? `task ${session.taskId}` : null, `session ${session.id}`].filter(Boolean).join(" · ");
  return (
    <header className={styles["header"]} data-status={session.status}>
      {session.owner ? (
        <PersonAvatar person={session.owner} size={40} agent={session.role} live={session.status === "running"} />
      ) : (
        <AgentAvatar role={session.role} size="lg" live={session.status === "running"} />
      )}
      <div className={styles["headerMain"]}>
        <div className={styles["headerTitle"]}>
          {session.taskKey ? (
            <code className={styles["headerId"]} title={ids}>
              {session.taskKey}
            </code>
          ) : null}
          <span className={styles["headerText"]}>{session.title ?? ROLE_LABEL[session.role]}</span>
          <StatusBadge status={session.status} label={session.statusLabel} size="sm" />
          {session.statusNote ? <span className={styles["headerStatusNote"]}>{session.statusNote}</span> : null}
        </div>
        <div className={styles["headerSub"]}>
          {session.subtitle ?? (
            <>
              <span title={ids}>{ROLE_LABEL[session.role]}</span>
              {session.model ? <code>{session.model}</code> : null}
              {session.repo ? (
                <code>
                  {session.repo}
                  {session.branch ? <span className={styles["headerBranch"]}> @ {session.branch}</span> : null}
                </code>
              ) : null}
            </>
          )}
        </div>
      </div>
      <div className={styles["headerStats"]}>
        {session.costUsd !== undefined ? (
          <span className={styles["stat"]}>
            <span className={styles["statLabel"]}>Cost</span>
            {session.budgetUsd !== undefined ? (
              <CostDisplay usd={session.costUsd} budgetUsd={session.budgetUsd} live={spec.live && session.costUsd !== null} />
            ) : (
              <Cost tokensUsd={session.costUsd} tokensFrom={session.tokensFrom} settled={session.settled} />
            )}
          </span>
        ) : null}
        {session.tokens !== undefined ? (
          <span className={styles["stat"]}>
            <span className={styles["statLabel"]}>Tokens</span>
            <TokenCount tokens={session.tokens} />
          </span>
        ) : null}
        {session.startedAt !== undefined ? (
          <span className={styles["stat"]}>
            <span className={styles["statLabel"]}>Elapsed</span>
            <Duration since={session.startedAt} until={session.endedAt} live={spec.live} />
          </span>
        ) : null}
      </div>
      {actions !== undefined ? <div className={styles["headerActions"]}>{actions}</div> : null}
    </header>
  );
}

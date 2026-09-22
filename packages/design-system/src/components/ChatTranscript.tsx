import { Children, useCallback, useEffect, useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import type { AgentRole, SessionStatus } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Button } from "../primitives/Button.tsx";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { StatusBadge } from "./StatusBadge.tsx";
import { CostDisplay, Duration, TokenCount } from "./Numbers.tsx";
import styles from "./ChatTranscript.module.css";

export interface ChatTranscriptSession {
  readonly id: string;
  readonly role: AgentRole;
  readonly status: SessionStatus;
  readonly model?: string | undefined;
  /** "WI-2481 · Add retry with backoff…" — enough to know what you are looking at. */
  readonly title?: string | undefined;
  readonly workItemId?: string | undefined;
  readonly repo?: string | undefined;
  readonly branch?: string | undefined;
  readonly startedAt?: string | number | Date | undefined;
  readonly endedAt?: string | number | Date | null | undefined;
  readonly costUsd?: number | undefined;
  readonly budgetUsd?: number | undefined;
  readonly tokens?: number | undefined;
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
  /** Still receiving. Controls aria-live. */
  readonly live?: boolean | undefined;
  /** Fill the parent's height instead of `maxHeight`. */
  readonly fill?: boolean | undefined;
  readonly maxHeight?: number | string | undefined;
  readonly emptyMessage?: ReactNode;
  readonly children?: ReactNode;
}

/**
 * The transcript: the operator's day-to-day screen. A header states what
 * you are looking at (work item, role, model, repo/branch, status, cost
 * against budget, elapsed), an optional pinned plan sits under it, the
 * turns scroll, and the composer sits below.
 *
 * Scrolling: follows the tail while you are at the bottom. The moment you
 * scroll up it stops, and a "Jump to latest" pill appears — with a count
 * of turns that arrived while you were reading. Streaming text that grows
 * the last turn also follows, via a ResizeObserver on the content, so a
 * long answer does not run off the bottom of the viewport.
 */
export function ChatTranscript({
  session,
  headerActions,
  pinned,
  footer,
  revision = 0,
  live,
  fill,
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
  const turnCount = Children.count(children);
  const lastCount = useRef(turnCount);

  const scrollToBottom = useCallback((smooth: boolean) => {
    const el = viewportRef.current;
    if (!el) return;
    el.scrollTo({ top: el.scrollHeight, behavior: smooth ? "smooth" : "auto" });
  }, []);

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
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 12;
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
    followingRef.current = true;
    setFollowing(true);
    setMissed(0);
    setStale(false);
    scrollToBottom(true);
  };

  const hasChildren = children !== undefined && children !== null && children !== false;

  return (
    <div className={cx(styles["root"], fill && styles["fill"], className)} style={fill ? undefined : { maxHeight }} data-following={following ? "true" : "false"} {...rest}>
      {session ? <TranscriptHeader session={session} actions={headerActions} /> : null}
      {pinned !== undefined ? <div className={styles["pinned"]}>{pinned}</div> : null}
      <div className={styles["scroller"]}>
        <div ref={viewportRef} className={styles["viewport"]} onScroll={onScroll} role="log" aria-live={live ? "polite" : "off"} aria-relevant="additions text" tabIndex={0}>
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

function TranscriptHeader({ session, actions }: { readonly session: ChatTranscriptSession; readonly actions: ReactNode }) {
  const live = session.status === "running" || session.status === "awaiting_input";
  return (
    <header className={cx(styles["header"], session.status === "awaiting_input" && styles["headerNeedsYou"])} data-status={session.status}>
      <AgentAvatar role={session.role} size="md" live={session.status === "running"} />
      <div className={styles["headerMain"]}>
        <div className={styles["headerTitle"]}>
          {session.workItemId ? <code className={styles["headerId"]}>{session.workItemId}</code> : null}
          <span className={styles["headerText"]}>{session.title ?? ROLE_LABEL[session.role]}</span>
          <StatusBadge status={session.status} size="sm" />
        </div>
        <div className={styles["headerSub"]}>
          <span>{ROLE_LABEL[session.role]}</span>
          {session.model ? <code>{session.model}</code> : null}
          {session.repo ? (
            <code>
              {session.repo}
              {session.branch ? <span className={styles["headerBranch"]}> @ {session.branch}</span> : null}
            </code>
          ) : null}
          <code className={styles["headerSession"]}>{session.id}</code>
        </div>
      </div>
      <div className={styles["headerStats"]}>
        {session.costUsd !== undefined ? (
          <span className={styles["stat"]}>
            <span className={styles["statLabel"]}>Cost</span>
            <CostDisplay usd={session.costUsd} budgetUsd={session.budgetUsd} live={live} />
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
            <Duration since={session.startedAt} until={session.endedAt ?? (live ? null : undefined)} />
          </span>
        ) : null}
      </div>
      {actions !== undefined ? <div className={styles["headerActions"]}>{actions}</div> : null}
    </header>
  );
}

/**
 * People's feedback on a task, drawn: an item's status, the lines or page
 * it is on, its thread, the card that holds them, the inline editor a
 * comment is written in, the tray of one's private drafts and the
 * confirmation of a submit. The app fetches and acts; these draw and ask.
 */

import { useEffect, useRef, useState, type HTMLAttributes, type KeyboardEvent, type ReactNode } from "react";
import type { FeedbackAnchor, FeedbackCodeAnchor, FeedbackPreviewAnchor, FeedbackStatus } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { plural } from "../util/format.ts";
import { openLinkWith } from "../util/openLink.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Badge } from "../primitives/Badge.tsx";
import { Button, IconButton } from "../primitives/Button.tsx";
import { Dialog } from "../primitives/Dialog.tsx";
import { EmptyState } from "../primitives/Feedback.tsx";
import { KeyHint } from "../primitives/Kbd.tsx";
import { Textarea } from "../primitives/Textarea.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import type { ToneName } from "../tokens/palette.ts";
import { Duration } from "./Numbers.tsx";
import { Markdown } from "./Markdown.tsx";
import { PersonAvatar, type Person } from "./PersonAvatar.tsx";
import diff from "./LiveDiff.module.css";
import styles from "./FeedbackCard.module.css";

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

export interface FeedbackStatusSpec {
  readonly tone: ToneName;
  readonly glyph: IconName;
  readonly label: string;
  readonly description: string;
}

/** What an open item holds: the merge, or, while a delivery is held before its pull request, its opening. */
export type FeedbackHolds = "merge" | "pull request";

const heldThing = (holds: FeedbackHolds) => (holds === "merge" ? "the merge" : "the pull request's opening");

/**
 * What an item is shown as: a tone, a glyph and words. `withWhom` names the
 * agent that has a delivered one ("Fix"); `holds` what an open one holds.
 */
export function feedbackStatusSpec(status: FeedbackStatus, pending = false, withWhom?: string, holds: FeedbackHolds = "merge"): FeedbackStatusSpec {
  const held = heldThing(holds);
  switch (status) {
    case "draft":
      return { tone: "neutral", glyph: "lock", label: "Draft · only you", description: "Nobody else sees it, and no agent, until you submit it." };
    case "open":
      return { tone: "neutral", glyph: "circle-dotted", label: "Open", description: `Submitted; no agent has it yet. It holds ${held}.` };
    case "delivered":
      return { tone: "info", glyph: "agent", label: withWhom ? `With ${withWhom}` : "With an agent",
        description: `An agent has it. It holds ${held} until it is resolved or answered.` };
    case "resolved":
      return pending
        ? { tone: "info", glyph: "clock", label: "Resolved · not yet published", description: `Resolved in a commit not yet pushed: it holds ${held} until it is.` }
        : { tone: "success", glyph: "check", label: "Resolved", description: "Done with. Anyone on the task can reopen it." };
    case "replied":
      return { tone: "neutral", glyph: "message", label: "Answered", description: "Answered rather than changed. It holds nothing; reopen it to ask again." };
  }
}

/** Whether an item holds the merge: submitted and not yet handled, or resolved in a commit not yet published. */
export const feedbackHolds = (status: FeedbackStatus, pending: boolean): boolean =>
  status === "open" || status === "delivered" || (status === "resolved" && pending);

export function FeedbackStatusBadge({ status, pending, withWhom, holds }: {
  readonly status: FeedbackStatus; readonly pending?: boolean; readonly withWhom?: string | undefined; readonly holds?: FeedbackHolds | undefined;
}) {
  const spec = feedbackStatusSpec(status, pending, withWhom, holds);
  return (
    <Tooltip content={spec.description}>
      <span tabIndex={0} className={styles["statusWrap"]} data-testid="fb-status" data-status={status}>
        <Badge tone={spec.tone} icon={spec.glyph} size="sm">{spec.label}</Badge>
      </span>
    </Tooltip>
  );
}

// ---------------------------------------------------------------------------
// Anchors
// ---------------------------------------------------------------------------

const baseName = (path: string) => path.slice(path.lastIndexOf("/") + 1);
/** "line 4" or "lines 4–9". */
export const lineWords = (start: number, end: number): string => (start === end ? `line ${start}` : `lines ${start}–${end}`);
const SIGN = { "+": "+", "-": "−", " ": "" } as const;

/** Where an item points, in one line: a file's lines, General, or a preview's page. */
export function FeedbackAnchorLine({ anchor, onJump, jumpLabel }: { readonly anchor: FeedbackAnchor; readonly onJump?: (() => void) | undefined; readonly jumpLabel?: string | undefined }) {
  if (anchor.kind === "general") {
    return <span className={styles["anchor"]}><Icon name="message" size={14} /><b>General</b><span className={styles["muted"]}>· the task as a whole</span></span>;
  }
  if (anchor.kind === "preview") {
    let where = anchor.url;
    try {
      where = new URL(anchor.url).pathname;
    } catch {
      // Stored as given: shown as given.
    }
    const inner = <><Icon name="globe" size={14} /><b>Preview</b><span className={cx(styles["mono"], styles["muted"], styles["clip"])} title={anchor.url}>{where}</span></>;
    return onJump ? (
      <button type="button" className={cx(styles["anchor"], styles["anchorLink"])} onClick={onJump} aria-label={jumpLabel ?? `Show it on the preview, ${where}`}>{inner}</button>
    ) : <span className={styles["anchor"]}>{inner}</span>;
  }
  const body = (
    <>
      <Icon name="file-diff" size={14} />
      <b className={styles["mono"]} title={anchor.path}>{baseName(anchor.path)}</b>
      <span className={styles["muted"]}>{lineWords(anchor.startLine, anchor.endLine)}{anchor.side === "old" ? " · removed side" : ""}</span>
    </>
  );
  return onJump ? (
    <button type="button" className={cx(styles["anchor"], styles["anchorLink"])} onClick={onJump} aria-label={jumpLabel ?? `Show ${anchor.path} ${lineWords(anchor.startLine, anchor.endLine)} in the diff`}>{body}</button>
  ) : <span className={styles["anchor"]}>{body}</span>;
}

/** The lines a code item was written on, as they were then: a note above says why they are shown. */
export function FeedbackExcerpt({ anchor, note }: { readonly anchor: Pick<FeedbackCodeAnchor, "excerpt">; readonly note?: ReactNode }) {
  return (
    <div className={styles["excerpt"]} data-testid="fb-excerpt">
      {note ? <div className={styles["excerptNote"]}><Icon name="file-diff" size={12} /> {note}</div> : null}
      {anchor.excerpt.map((l, i) => (
        <div key={i} className={cx(diff["line"], l.kind === "+" && diff["added"], l.kind === "-" && diff["removed"])}>
          <span className={diff["n"]}>{l.old ?? ""}</span>
          <span className={diff["n"]}>{l.new ?? ""}</span>
          <span className={diff["sign"]} aria-hidden>{SIGN[l.kind]}</span>
          <span className={diff["text"]}>{l.text || " "}</span>
        </div>
      ))}
    </div>
  );
}

/**
 * A preview item's page: where, which element, which component, and its
 * picture when the app has one. `onOpen` is the app's own way to open the
 * page, called on a plain left click of its link (false leaves it to the link).
 */
export function FeedbackPreview({ anchor, imageUrl, onOpen }: {
  readonly anchor: FeedbackPreviewAnchor; readonly imageUrl?: string | undefined; readonly onOpen?: ((url: string) => boolean) | undefined;
}) {
  const vw = anchor.viewport?.width;
  const vh = anchor.viewport?.height;
  return (
    <div className={cx(styles["preview"], !imageUrl && styles["previewNoShot"])} data-testid="fb-preview">
      {imageUrl ? <img className={styles["shot"]} src={imageUrl} alt="The page as it was, the element marked" /> : null}
      <dl className={styles["facts"]}>
        <dt>Page</dt>
        <dd><a href={anchor.url} target="_blank" rel="noreferrer" className={cx(styles["mono"], styles["clip"])} title={anchor.url}
          onClick={openLinkWith(onOpen, anchor.url)} data-testid="fb-preview-page">{anchor.url.replace(/^https?:\/\//, "")}</a>
          {vw && vh ? <span className={styles["muted"]}> · {vw}×{vh}</span> : null}</dd>
        {anchor.selector ? <><dt>Element</dt><dd className={cx(styles["mono"], styles["clip"])} title={anchor.selector}>{anchor.selector}</dd></> : null}
        {anchor.text ? <><dt>Text</dt><dd>“{anchor.text}”</dd></> : null}
        {anchor.component?.name || anchor.component?.file ? (
          <><dt>Component</dt><dd>{anchor.component.name ? <b className={styles["mono"]}>{anchor.component.name}</b> : null}{" "}
            <span className={cx(styles["mono"], styles["muted"])}>{anchor.component.file ?? ""}{anchor.component.line ? `:${anchor.component.line}` : ""}</span></dd></>
        ) : null}
      </dl>
    </div>
  );
}

// ---------------------------------------------------------------------------
// The thread
// ---------------------------------------------------------------------------

export interface FeedbackThreadEntry {
  readonly id: string;
  readonly kind: "reply" | "resolve" | "reopen";
  /** Who: a person's or an agent's name, and their face. */
  readonly who: string;
  readonly face: ReactNode;
  readonly body: string;
  readonly commit: string | null;
  readonly at: string;
}

const ageOf = (at: string, now: number) => <Duration ms={Math.max(0, now - Date.parse(at))} format="age" tone="muted" />;

function entryVerb(entry: FeedbackThreadEntry): ReactNode {
  switch (entry.kind) {
    case "reply": return "answered";
    case "reopen": return "reopened it";
    case "resolve": return entry.commit ? <>resolved it in <code className={styles["sha"]}>{entry.commit.slice(0, 7)}</code></> : "resolved it";
  }
}

export function FeedbackThread({ entries, now = Date.now() }: { readonly entries: ReadonlyArray<FeedbackThreadEntry>; readonly now?: number }) {
  if (entries.length === 0) return null;
  return (
    <ol className={styles["thread"]} aria-label="Thread">
      {entries.map((h) => (
        <li key={h.id} className={styles["entry"]} data-kind={h.kind} data-testid="fb-entry">
          <span className={styles["entryFace"]}>{h.face}</span>
          <div className={styles["entryMain"]}>
            <div className={styles["entryHead"]}>
              <b>{h.who}</b>{" "}
              {entryVerb(h)}
              <span className={styles["muted"]}> · {ageOf(h.at, now)} ago</span>
            </div>
            {h.body ? <div className={styles["entryBody"]}><Markdown source={h.body} unmeasured /></div> : null}
          </div>
        </li>
      ))}
    </ol>
  );
}

// ---------------------------------------------------------------------------
// The editor
// ---------------------------------------------------------------------------

export interface CommentEditorProps {
  /** What it is on: "Comment on lines 58–61", "General". */
  readonly where: ReactNode;
  readonly value: string;
  readonly onChange: (value: string) => void;
  readonly onSave: () => void;
  readonly onCancel: () => void;
  readonly saving?: boolean | undefined;
  readonly saveLabel?: string | undefined;
  readonly placeholder?: string | undefined;
  /** Why the last save failed, in the API's words. */
  readonly problem?: ReactNode;
  /** No more drafts are kept: the field is off and says why. */
  readonly full?: ReactNode;
  /** At most this many characters. */
  readonly maxLength?: number | undefined;
  readonly autoFocus?: boolean | undefined;
}

/** Writing a comment: ⌘/Ctrl+Enter saves, Escape cancels; a private draft until submitted. */
export function CommentEditor({ where, value, onChange, onSave, onCancel, saving, saveLabel = "Save draft", placeholder, problem, full, maxLength, autoFocus = true }: CommentEditorProps) {
  const canSave = !full && !saving && value.trim() !== "" && (maxLength === undefined || value.length <= maxLength);
  const keys = (e: KeyboardEvent) => {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      if (canSave) onSave();
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      onCancel();
    }
  };
  return (
    <div className={styles["editor"]} data-testid="fb-editor" onKeyDown={keys}>
      <div className={styles["editorHead"]}>
        <span className={styles["editorWhere"]}>{where}</span>
        <span className={styles["spacer"]} />
        <span className={styles["private"]}><Icon name="lock" size={12} /> Private draft until you submit</span>
      </div>
      <Textarea aria-label="Comment" rows={3} maxRows={12} value={value} onChange={(e) => onChange(e.target.value)} disabled={Boolean(full)}
        placeholder={placeholder ?? "What should change…"} autoFocus={autoFocus && !full} />
      {problem ? <p className={styles["problem"]} role="alert">{problem}</p> : null}
      <div className={styles["editorFoot"]}>
        {full ? <span className={styles["limit"]} role="note">{full}</span> : (
          <>
            <KeyHint keys={["mod", "Enter"]}>save</KeyHint>
            <KeyHint keys={["Esc"]}>cancel</KeyHint>
            {maxLength !== undefined && value.length > maxLength * 0.9 ? <span className={styles["limit"]}>{value.length} / {maxLength}</span> : null}
          </>
        )}
        <span className={styles["spacer"]} />
        <Button size="sm" variant="quiet" onClick={onCancel}>Cancel</Button>
        <Button size="sm" variant="primary" disabled={!canSave} onClick={onSave} data-testid="fb-save">{saving ? "Saving…" : saveLabel}</Button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// The card
// ---------------------------------------------------------------------------

export interface FeedbackCardItem {
  readonly id: string;
  readonly status: FeedbackStatus;
  readonly resolutionPending: boolean;
  readonly resolvedCommit: string | null;
  readonly body: string;
  readonly anchor: FeedbackAnchor;
  readonly author: Person | null;
  /** Submitted, or for a draft written. */
  readonly at: string;
  /** Where its batch went, in words: "to the fixer at work". */
  readonly routed?: ReactNode;
  /** The agent that has a delivered one: "Fix". */
  readonly withWhom?: string | undefined;
}

export interface FeedbackCardProps extends Omit<HTMLAttributes<HTMLElement>, "children"> {
  readonly item: FeedbackCardItem;
  readonly thread?: ReadonlyArray<FeedbackThreadEntry> | undefined;
  /** In a diff, under its lines: the anchor is above it, not repeated. */
  readonly inline?: boolean | undefined;
  /**
   * Where its lines are now, when it is not on them: `outdated` (they
   * changed) or `unchanged` (the diff shown does not show them). The
   * original excerpt is shown with the words.
   */
  readonly placement?: "outdated" | "unchanged" | undefined;
  /** To read only: an earlier attempt's, a done task's. */
  readonly readOnly?: boolean | undefined;
  /** The page an item points at, when the app has its picture. */
  readonly imageUrl?: string | undefined;
  /** A preview item's Page link opened the app's way (FeedbackPreview's `onOpen`). */
  readonly onOpenPage?: ((url: string) => boolean) | undefined;
  /**
   * Back to where it is: a code item's lines in the diff (Show in diff), a
   * preview item's element on the page (Show on preview, drafts too).
   */
  readonly onJump?: (() => void) | undefined;
  /** Each resolves true when done, false (or throws) to keep what was written. */
  readonly onReply?: ((body: string) => Promise<unknown>) | undefined;
  readonly onResolve?: (() => Promise<unknown>) | undefined;
  readonly onReopen?: (() => Promise<unknown>) | undefined;
  readonly onSaveDraft?: ((body: string) => Promise<unknown>) | undefined;
  readonly onDelete?: (() => Promise<unknown>) | undefined;
  /** A draft opens in its editor (the tray's Edit). */
  readonly editing?: boolean | undefined;
  readonly onEditingChange?: ((editing: boolean) => void) | undefined;
  /**
   * Whether the card holds words not yet saved: a draft's editor changed
   * from its saved text, or a reply typed and not sent. Told when it turns,
   * so a page can refuse what would lose them (a refresh).
   */
  readonly onDirtyChange?: ((dirty: boolean) => void) | undefined;
  readonly maxLength?: number | undefined;
  readonly now?: number | undefined;
  /** What an open item holds: the merge, or a held delivery's pull request opening. */
  readonly holds?: FeedbackHolds | undefined;
}

const PLACEMENT_WORDS = {
  outdated: "The lines it was on have changed. As they were when it was written:",
  unchanged: "These lines are unchanged in this diff. As they were when it was written:",
} as const;

/** One item: who wrote it and when, where it went, its status, what it is on, its words and thread, and what can be done with it. */
export function FeedbackCard({ item, thread = [], inline, placement, readOnly, imageUrl, onOpenPage, onJump, onReply, onResolve, onReopen, onSaveDraft, onDelete,
  editing: editingGiven, onEditingChange, onDirtyChange, maxLength, now = Date.now(), holds = "merge", className, ...rest }: FeedbackCardProps) {
  const [ownEditing, setOwnEditing] = useState(false);
  const editing = editingGiven ?? ownEditing;
  const setEditing = (on: boolean) => {
    if (editingGiven === undefined) setOwnEditing(on);
    onEditingChange?.(on);
  };
  const [text, setText] = useState(item.body);
  const [replying, setReplying] = useState(false);
  const [reply, setReply] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const draft = item.status === "draft";
  const pending = item.status === "resolved" && item.resolutionPending;
  const dirty = (draft && editing && text !== item.body) || (replying && reply.trim() !== "");
  const latestDirty = useRef(onDirtyChange);
  latestDirty.current = onDirtyChange;
  const told = useRef(false);
  useEffect(() => {
    if (told.current === dirty) return;
    told.current = dirty;
    latestDirty.current?.(dirty);
  }, [dirty]);
  // Gone while holding words: they went with it, so the page is told it holds none.
  useEffect(() => () => {
    if (told.current) latestDirty.current?.(false);
  }, []);
  const a = item.anchor;
  const showOnPreview = onJump && a.kind === "preview"
    ? <Button size="sm" variant="quiet" leadingIcon="eye" onClick={onJump} data-testid="fb-show-on-preview">Show on preview</Button> : null;
  const act = async (what: string, run: () => Promise<unknown>, after?: () => void) => {
    setBusy(what);
    setProblem(null);
    try {
      if ((await run()) !== false) after?.();
    } catch (err) {
      setProblem(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  };
  const sendReply = () => onReply && reply.trim() && void act("reply", () => onReply(reply.trim()), () => {
    setReply("");
    setReplying(false);
  });

  return (
    <article className={cx(styles["card"], inline && styles["inline"], draft && styles["draft"], className)}
      data-testid={draft ? "fb-draft" : "fb-item"} data-id={item.id} data-status={item.status} data-placement={placement}
      aria-label={draft ? "Your draft" : `Feedback from ${item.author?.name ?? "someone"}`} {...rest}>
      <header className={styles["head"]}>
        {item.author ? <PersonAvatar person={item.author} size={24} /> : null}
        <b className={styles["who"]}>{item.author?.name ?? "Someone"}</b>
        <span className={styles["muted"]}>
          {draft ? <>drafted {ageOf(item.at, now)} ago</> : <>submitted {ageOf(item.at, now)} ago{item.routed ? <> · {item.routed}</> : null}</>}
        </span>
        <span className={styles["spacer"]} />
        {placement === "outdated" ? <Badge tone="neutral" icon="file-diff" size="sm">Lines changed since</Badge> : null}
        <FeedbackStatusBadge status={item.status} pending={item.resolutionPending} withWhom={item.withWhom} holds={holds} />
      </header>
      {inline ? null : <div className={styles["anchorRow"]}><FeedbackAnchorLine anchor={a} onJump={a.kind === "general" ? undefined : onJump} /></div>}
      {a.kind === "code" && (!inline || placement) ? <FeedbackExcerpt anchor={a} note={placement ? PLACEMENT_WORDS[placement] : undefined} /> : null}
      {a.kind === "preview" ? <FeedbackPreview anchor={a} imageUrl={imageUrl} onOpen={onOpenPage} /> : null}
      {draft && editing && onSaveDraft ? (
        <CommentEditor where={<>Edit your draft</>} value={text} onChange={setText} saveLabel="Save" saving={busy === "save"} maxLength={maxLength}
          problem={problem} onCancel={() => { setText(item.body); setProblem(null); setEditing(false); }}
          onSave={() => void act("save", () => onSaveDraft(text.trim()), () => setEditing(false))} />
      ) : (
        <div className={styles["body"]}><Markdown source={item.body} unmeasured breaks /></div>
      )}
      <FeedbackThread entries={thread} now={now} />
      {pending ? (
        <p className={styles["pendingLine"]} data-testid="fb-pending">
          <Icon name="clock" size={12} /> Counts as resolved once <code className={styles["sha"]}>{item.resolvedCommit?.slice(0, 7)}</code> is published; until then it holds {heldThing(holds)}.
          If its agent stops without publishing it, the item opens again by itself.
        </p>
      ) : null}
      {replying && !readOnly ? (
        <div className={styles["replyBox"]} onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); sendReply(); }
          if (e.key === "Escape") { e.preventDefault(); setReplying(false); }
        }}>
          <Textarea aria-label="Reply" rows={2} maxRows={8} value={reply} onChange={(e) => setReply(e.target.value)} autoFocus
            placeholder="Reply… The agent that has it reads it with its next work." />
          <div className={styles["editorFoot"]}>
            <KeyHint keys={["mod", "Enter"]}>reply</KeyHint>
            <span className={styles["spacer"]} />
            <Button size="sm" variant="quiet" onClick={() => setReplying(false)}>Cancel</Button>
            <Button size="sm" variant="secondary" disabled={reply.trim() === "" || busy !== null} onClick={sendReply} data-testid="fb-reply-send">
              {busy === "reply" ? "Replying…" : "Reply"}
            </Button>
          </div>
        </div>
      ) : null}
      {problem && !(draft && editing) ? <p className={styles["problem"]} role="alert">{problem}</p> : null}
      {readOnly || (draft && editing) ? null : (
        <footer className={styles["actions"]}>
          {draft ? (
            <>
              <span className={styles["muted"]}><Icon name="lock" size={12} /> Only you see this until you submit.</span>
              <span className={styles["spacer"]} />
              {showOnPreview}
              {onSaveDraft ? <Button size="sm" variant="quiet" leadingIcon="edit" onClick={() => { setText(item.body); setEditing(true); }}>Edit</Button> : null}
              {onDelete ? <Button size="sm" variant="danger" leadingIcon="close" disabled={busy !== null} onClick={() => void act("delete", onDelete)}>Delete</Button> : null}
            </>
          ) : (
            <>
              {inline || !onJump || a.kind !== "code" ? null : <Button size="sm" variant="quiet" leadingIcon="file-diff" onClick={onJump}>Show in diff</Button>}
              {showOnPreview}
              <span className={styles["spacer"]} />
              {onReply && !replying ? (
                pending ? (
                  <Tooltip content="A reply would let it go before its commit is published. Reopen it to ask for more, or confirm it resolved.">
                    <span tabIndex={0}><Button size="sm" variant="quiet" leadingIcon="message" disabled>Reply</Button></span>
                  </Tooltip>
                ) : <Button size="sm" variant="quiet" leadingIcon="message" onClick={() => setReplying(true)} data-testid="fb-reply">Reply</Button>
              ) : null}
              {onResolve && (item.status !== "resolved" || pending) && item.status !== "replied" ? (
                <Button size="sm" variant="quiet" leadingIcon="check" disabled={busy !== null} onClick={() => void act("resolve", onResolve)} data-testid="fb-resolve">
                  {busy === "resolve" ? "Resolving…" : pending ? "Confirm resolved" : "Resolve"}
                </Button>
              ) : null}
              {onReopen && (item.status === "replied" || (item.status === "resolved" && !pending)) ? (
                <Tooltip content={`Open again: it goes to whoever drives the task now, and holds ${heldThing(holds)}.`}>
                  <Button size="sm" variant="quiet" leadingIcon="retry" disabled={busy !== null} onClick={() => void act("reopen", onReopen)} data-testid="fb-reopen">
                    {busy === "reopen" ? "Reopening…" : "Reopen"}
                  </Button>
                </Tooltip>
              ) : null}
            </>
          )}
        </footer>
      )}
    </article>
  );
}

// ---------------------------------------------------------------------------
// The tray
// ---------------------------------------------------------------------------

export interface DraftsTrayRow {
  readonly id: string;
  readonly anchor: FeedbackAnchor;
  readonly body: string;
}

export interface DraftsTrayProps extends Omit<HTMLAttributes<HTMLElement>, "children"> {
  readonly drafts: ReadonlyArray<DraftsTrayRow>;
  /** The most drafts kept on a task. */
  readonly max: number;
  /** Where a submit now goes: "the fixer at work". */
  readonly destination?: ReactNode;
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  readonly onSubmit: () => void;
  readonly onJump?: ((id: string) => void) | undefined;
  readonly onEdit?: ((id: string) => void) | undefined;
  readonly onDelete?: ((id: string) => void) | undefined;
  /** Submitting is refused now (a done or stopped task): why, in the API's words. */
  readonly submitRefused?: string | undefined;
}

const oneLine = (body: string) => body.replace(/[*_`#>]/g, "").replace(/\s+/g, " ").trim();

/** One's drafts on a task, at the foot of its pages: how many, that they are private, where they go, and Submit. */
export function DraftsTray({ drafts, max, destination, open, onOpenChange, onSubmit, onJump, onEdit, onDelete, submitRefused, className, ...rest }: DraftsTrayProps) {
  const n = drafts.length;
  if (n === 0) return null;
  const full = n >= max;
  return (
    <section className={cx(styles["tray"], className)} aria-label="Your drafts" data-testid="fb-tray" data-count={n} {...rest}>
      {open ? (
        <div className={styles["trayPanel"]} id="fb-tray-panel" data-testid="fb-tray-panel">
          {full ? <p className={styles["limit"]} role="note">{max} drafts is the most kept on a task. Submit them (one batch takes them all) or delete some to write more.</p> : null}
          <ul className={styles["trayList"]}>
            {drafts.map((d) => (
              <li key={d.id} className={styles["trayRow"]} data-testid="fb-tray-row" data-id={d.id}>
                <span className={styles["trayAnchor"]}><FeedbackAnchorLine anchor={d.anchor} /></span>
                <span className={styles["trayText"]}>{oneLine(d.body)}</span>
                <span className={styles["trayActions"]}>
                  {onJump ? <IconButton icon="arrow-right" size="sm" label={d.anchor.kind === "code" ? "Show it in the diff" : "Show it on the Feedback tab"} onClick={() => onJump(d.id)} /> : null}
                  {onEdit ? <IconButton icon="edit" size="sm" label="Edit" onClick={() => onEdit(d.id)} /> : null}
                  {onDelete ? <IconButton icon="close" size="sm" label="Delete" onClick={() => onDelete(d.id)} /> : null}
                </span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      <div className={styles["trayBar"]}>
        <Icon name="comments" size={16} className={styles["trayIcon"]} />
        <span className={styles["trayCount"]}>
          <b data-testid="fb-tray-count">{n}</b>{full ? ` of ${max} drafts` : ` ${n === 1 ? "draft" : "drafts"}`}
          <span className={styles["trayNote"]}>{full ? " · the most you can keep" : " · only you see them until you submit"}</span>
        </span>
        <span className={styles["spacer"]} />
        {destination && !submitRefused ? <span className={styles["trayRoute"]} data-testid="fb-tray-route">Goes to {destination}</span> : null}
        <Button size="sm" variant="quiet" trailingIcon={open ? "chevron-down" : "chevron-up"} aria-expanded={open} aria-controls="fb-tray-panel"
          onClick={() => onOpenChange(!open)} data-testid="fb-tray-toggle">
          {open ? "Hide" : "Review"}
        </Button>
        {submitRefused ? (
          <Tooltip content={submitRefused}>
            <span tabIndex={0} data-testid="fb-submit-refused"><Button size="sm" variant="primary" disabled>Submit {n}</Button></span>
          </Tooltip>
        ) : <Button size="sm" variant="primary" onClick={onSubmit} data-testid="fb-submit">Submit {n}…</Button>}
      </div>
    </section>
  );
}

// ---------------------------------------------------------------------------
// Submit
// ---------------------------------------------------------------------------

export interface SubmitFeedbackDialogProps {
  readonly drafts: ReadonlyArray<DraftsTrayRow>;
  /** Where they go, in a sentence, then what follows from it. */
  readonly destination: { readonly line: ReactNode; readonly detail?: ReactNode };
  /** Until each is handled, what it holds: "Pull request #482 can't merge…". */
  readonly holds: ReactNode;
  readonly onConfirm: () => void;
  readonly onCancel: () => void;
  readonly busy?: boolean | undefined;
  readonly problem?: ReactNode;
}

/** The confirmation of a submit: what goes, where, and what it holds until handled. */
export function SubmitFeedbackDialog({ drafts, destination, holds, onConfirm, onCancel, busy, problem }: SubmitFeedbackDialogProps) {
  const n = drafts.length;
  const files = [...new Set(drafts.flatMap((d) => (d.anchor.kind === "code" ? [baseName(d.anchor.path)] : [])))];
  const onCode = drafts.filter((d) => d.anchor.kind === "code").length;
  const general = drafts.filter((d) => d.anchor.kind === "general").length;
  const preview = drafts.filter((d) => d.anchor.kind === "preview").length;
  const parts: ReactNode[] = [];
  if (onCode > 0) parts.push(<span key="c">{onCode} on lines of {files.map((f, i) => <span key={f}>{i > 0 ? ", " : ""}<code>{f}</code></span>)}</span>);
  if (general > 0) parts.push(<span key="g">{general} general</span>);
  if (preview > 0) parts.push(<span key="p">{preview} on a preview</span>);
  return (
    // On the dialog itself: focus opens on its Close button, outside the body.
    <Dialog open onOpenChange={(o) => !o && onCancel()} size="md" title={`Submit ${plural(n, "feedback item")}`}
      onKeyDown={(e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !busy) { e.preventDefault(); onConfirm(); }
      }}
      footerStart={problem ? <span role="alert" data-testid="fb-submit-problem">{problem}</span> : <span className={styles["keys"]} data-testid="fb-confirm-keys"><KeyHint keys={["mod", "Enter"]}>submit</KeyHint></span>}
      footer={<>
        <Button variant="quiet" onClick={onCancel}>Keep as drafts</Button>
        <Button variant="primary" onClick={onConfirm} disabled={busy} data-testid="fb-confirm">{busy ? "Submitting…" : `Submit ${n}`}</Button>
      </>}>
      <div className={styles["confirm"]} data-testid="fb-confirm-body">
        <p>{parts.map((p, i) => <span key={i}>{i > 0 ? " · " : ""}{p}</span>)} — as one batch.</p>
        <section className={styles["confirmBlock"]} aria-label="Where they go">
          <h3 className="ds-label">Where they go</h3>
          <p className={styles["confirmLine"]} data-testid="fb-confirm-route"><b>{destination.line}</b>{destination.detail ? <> {destination.detail}</> : null}</p>
        </section>
        <section className={styles["confirmBlock"]} aria-label="What they hold">
          <h3 className="ds-label">Until each is handled</h3>
          <p className={styles["confirmLine"]}>{holds}</p>
        </section>
        <p className={styles["muted"]}>Once submitted, everyone on the task sees them and they can't be edited: reply on them instead.</p>
      </div>
    </Dialog>
  );
}

/** Nothing on this attempt yet, in the Feedback tab's words. */
export function FeedbackEmpty({ description }: { readonly description: ReactNode }) {
  return <EmptyState icon="comments" title="No feedback on this attempt yet" description={description} />;
}

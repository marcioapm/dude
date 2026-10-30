/**
 * A server's feedback, folded under its row on the project's Servers page:
 * everyone's, read-only, All / Open / Resolved with Refresh.
 *
 * - A branch preview's server: its saved feedback (`GET
 *   /v1/branch-previews/:id/servers/:name/feedback?order=desc`), newest
 *   first, older pages with Load more; the resolution is the thread's resolve entry.
 * - A task's server: that task's submitted items on this server's pages,
 *   with the task's statuses, and the reader's own drafts first ("Draft ·
 *   only you"); the API lists nobody else's. Acting on them is the task's
 *   Feedback tab's.
 *
 * The project's stream moves it: a submit, resolve or reopen on this
 * server reads it again, and what arrived since it opened is marked New.
 */

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { EventTypes, type FeedbackItem, type PersistedEvent, type PreviewFeedbackItem, type RunningRun, type RunningServer } from "@dude/domain";
import { FeedbackCard, PersonAvatar, Segmented, type FeedbackCardItem, type FeedbackThreadEntry } from "@dude/design-system/components";
import { Badge, Button, Callout, Spinner } from "@dude/design-system/primitives";
import { firstName } from "@dude/design-system";
import type { ApiClient } from "../api/client.ts";
import { usePeople, type People } from "../people.tsx";
import { errorText } from "../hooks/useSave.tsx";
import { ROUTED_WORDS } from "../feedback.tsx";
import { httpsOriginOf } from "../previewComment.ts";

export type FoldFilter = "all" | "open" | "resolved";

/** A task item's place in Open (open, delivered, resolved-not-yet-published); every other submitted status is Resolved. */
export const taskOpen = (i: Pick<FeedbackItem, "status" | "resolutionPending">) =>
  i.status === "open" || i.status === "delivered" || (i.status === "resolved" && i.resolutionPending);

const PAGE = 20;
// The list route's largest `limit`.
const ROUTE_MAX = 200;

type Shown = { id: string; open: boolean; card: FeedbackCardItem; thread: FeedbackThreadEntry[]; draft: boolean };

const face = (people: People, id: string, name: string) =>
  <PersonAvatar person={{ ...(people.byId.get(id) ?? {}), id, name }} size={20} />;

function fromPreview(i: PreviewFeedbackItem, people: People): Shown {
  const known = people.byId.get(i.author.id);
  return {
    id: i.id, open: i.status === "open", draft: false,
    card: { id: i.id, status: i.status, resolutionPending: false, resolvedCommit: null, body: i.body, anchor: i.anchor,
      author: { ...(known ?? {}), id: i.author.id, name: i.author.name }, at: i.createdAt },
    thread: i.history.map((h, k) => {
      const name = people.names.get(h.personId) ?? "Someone";
      return { id: `${i.id}-${k}`, kind: h.status === "resolved" ? "resolve" : "reopen", who: firstName(name), face: face(people, h.personId, name),
        body: h.note, commit: null, at: h.at };
    }),
  };
}

function fromTask(i: FeedbackItem, people: People): Shown {
  const known = i.author ? people.byId.get(i.author.id) : undefined;
  return {
    id: i.id, open: taskOpen(i), draft: i.status === "draft",
    card: { id: i.id, status: i.status, resolutionPending: i.resolutionPending, resolvedCommit: i.resolvedCommit, body: i.body, anchor: i.anchor,
      author: i.author ? { ...(known ?? {}), id: i.author.id, name: i.author.name } : null, at: i.submittedAt ?? i.createdAt,
      routed: i.routedTo ? ROUTED_WORDS[i.routedTo] : undefined, withWhom: i.status === "delivered" ? "an agent" : undefined },
    thread: i.history.map((h) => {
      const name = h.personId ? people.names.get(h.personId) ?? "Someone" : "The agent";
      return { id: h.id, kind: h.kind, who: h.personId ? firstName(name) : name, body: h.body, commit: h.commit, at: h.createdAt,
        face: h.personId ? face(people, h.personId, name) : <Badge size="sm" tone="neutral" icon="agent">agent</Badge> };
    }),
  };
}

type Read = { state: "loading" } | { state: "failed"; why: string } | { state: "ok"; items: Shown[]; next: string | null; more: boolean };

/** The events that change a server's feedback: a branch preview's, or a task's. */
export const FEEDBACK_EVENTS: ReadonlySet<string> = new Set([EventTypes.PreviewFeedbackSubmitted, EventTypes.PreviewFeedbackResolved, EventTypes.PreviewFeedbackReopened,
  EventTypes.FeedbackSubmitted, EventTypes.FeedbackResolved, EventTypes.FeedbackReopened, EventTypes.FeedbackReplied, EventTypes.FeedbackDelivered]);

/** How long a fold waits after a matching event for the rest of its burst before reading again. */
export const FOLD_READ_MS = 300;

export function ServerFeedback({ client, run, server, events, reconnects = 0, onTaskFeedback }: {
  client: ApiClient;
  run: RunningRun;
  server: RunningServer;
  events: ReadonlyArray<PersistedEvent>;
  /** The project stream's reconnect count: a change reads the fold again (what it missed while away). */
  reconnects?: number;
  onTaskFeedback?: (() => void) | undefined;
}) {
  const people = usePeople();
  const peopleRef = useRef(people);
  peopleRef.current = people;
  const [filter, setFilter] = useState<FoldFilter>("all");
  const [read, setRead] = useState<Read>({ state: "loading" });
  const loaded = useRef(0);
  loaded.current = read.state === "ok" ? read.items.length : 0;
  // Ids seen at the first read: anything after them arrived while the fold was open.
  const known = useRef<Set<string> | null>(null);
  const [fresh, setFresh] = useState<ReadonlySet<string>>(new Set());
  const [updated, setUpdated] = useState(false);
  const reads = useRef(0);
  const task = run.kind === "task" ? run.task : null;
  // By id: the list re-read gives a new Run object each time, which must not read the fold again.
  const taskId = task?.id ?? null;
  const origin = httpsOriginOf(server.url);

  const load = useCallback(async (live = false) => {
    const n = ++reads.current;
    try {
      let shown: Shown[];
      let next: string | null = null;
      let gap = false;
      if (taskId) {
        if (!origin) throw new Error("this server has no https URL to match its feedback by");
        const { feedback } = await client.serverFeedback(taskId, origin);
        const items = feedback.map((i) => fromTask(i, peopleRef.current));
        // Your drafts first, then everyone's submitted, newest first.
        shown = [...items.filter((i) => i.draft), ...items.filter((i) => !i.draft).sort((a, b) => b.card.at.localeCompare(a.card.at))];
      } else {
        // Newest first: the first page is the latest feedback, Load more goes back in time. A live
        // read covers every loaded card, so a resolve or reopen on an older page lands too.
        const depth = live ? Math.max(PAGE, loaded.current) : PAGE;
        const got: PreviewFeedbackItem[] = [];
        let before: string | undefined;
        do {
          const page = await client.previewFeedback(run.id, server.name,
            { status: "all", order: "desc", limit: Math.min(ROUTE_MAX, depth - got.length), ...(before ? { before } : {}) });
          got.push(...page.feedback);
          next = page.nextCursor;
          before = next ?? undefined;
        } while (next !== null && got.length < depth);
        shown = got.map((i) => fromPreview(i, peopleRef.current));
        gap = next !== null && shown.every((s) => !known.current?.has(s.id));
      }
      if (n !== reads.current) return;
      if (known.current === null) known.current = new Set(shown.map((s) => s.id));
      else if (live) {
        const arrived = shown.filter((s) => !known.current!.has(s.id)).map((s) => s.id);
        if (arrived.length) setFresh((f) => new Set([...f, ...arrived]));
        for (const id of arrived) known.current.add(id);
        setUpdated(true);
      }
      setRead((prev) => {
        // A branch fold's re-read is merged into the pages already loaded, by id; a whole page of new items may hide a gap, so it starts over.
        if (taskId || !live || prev.state !== "ok" || gap) return { state: "ok", items: shown, next, more: next !== null };
        const byId = new Map(shown.map((s) => [s.id, s]));
        const kept = prev.items.map((s) => byId.get(s.id) ?? s);
        const added = shown.filter((s) => !prev.items.some((p) => p.id === s.id));
        return { state: "ok", items: [...added, ...kept], next: prev.next, more: prev.more };
      });
    } catch (err) {
      if (n === reads.current) setRead({ state: "failed", why: errorText(err) });
    }
  }, [client, taskId, origin, run.id, server.name]);

  useEffect(() => {
    void load();
  }, [load]);

  // The project's stream: this server's feedback changed. One re-read per burst (FOLD_READ_MS), and one when the stream comes back.
  const heard = useRef(events.at(-1)?.cursor ?? 0);
  const pending = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const soon = useCallback(() => {
    if (pending.current !== undefined) return;
    pending.current = setTimeout(() => { pending.current = undefined; void load(true); }, FOLD_READ_MS);
  }, [load]);
  useEffect(() => () => clearTimeout(pending.current), []);
  useEffect(() => {
    const mine = events.filter((e) => e.cursor > heard.current && FEEDBACK_EVENTS.has(e.eventType) && (taskId
      ? e.taskId === taskId
      : (e.payload as { previewRunId?: string; server?: string }).previewRunId === run.id && (e.payload as { server?: string }).server === server.name));
    if (events.length) heard.current = Math.max(heard.current, events.at(-1)!.cursor);
    if (mine.length) soon();
  }, [events, taskId, run.id, server.name, soon]);
  const reconnected = useRef(reconnects);
  useEffect(() => {
    if (reconnects === reconnected.current) return;
    reconnected.current = reconnects;
    soon();
  }, [reconnects, soon]);

  const more = async () => {
    if (read.state !== "ok" || !read.next) return;
    try {
      const got = await client.previewFeedback(run.id, server.name, { status: "all", order: "desc", limit: PAGE, before: read.next });
      const added = got.feedback.map((i) => fromPreview(i, peopleRef.current)).filter((a) => !read.items.some((s) => s.id === a.id));
      for (const a of added) known.current?.add(a.id);
      setRead({ state: "ok", items: [...read.items, ...added], next: got.nextCursor, more: got.nextCursor !== null });
    } catch (err) {
      setRead({ state: "failed", why: errorText(err) });
    }
  };

  const items = read.state === "ok" ? read.items : [];
  const submitted = items.filter((i) => !i.draft);
  const drafts = items.filter((i) => i.draft);
  const openN = submitted.filter((i) => i.open).length;
  const doneN = submitted.length - openN;
  const listed = submitted.filter((i) => filter === "all" || (filter === "open" ? i.open : !i.open));
  const card = (s: Shown): ReactNode => <FeedbackCard item={s.card} thread={s.thread} readOnly />;

  return (
    <div className="serverFold" data-testid="server-fold" data-kind={task ? "task" : "preview"}>
      <div className="serverFoldBar">
        <Segmented size="sm" label="Show" value={filter} onChange={setFilter} data-testid="fold-filter"
          options={[{ value: "all", label: `All ${submitted.length}` }, { value: "open", label: `Open ${openN}` }, { value: "resolved", label: `Resolved ${doneN}` }]} />
        <span className="serverFoldRight">
          {updated ? <span className="serverFoldNote" data-testid="live-note">Updated just now</span> : null}
          {task && onTaskFeedback ? (
            <Button size="sm" variant="quiet" trailingIcon="arrow-right" data-testid="task-feedback-link" onClick={onTaskFeedback}>{`${task.key} Feedback tab`}</Button>
          ) : null}
          <Button size="sm" variant="quiet" leadingIcon="retry" data-testid="fold-refresh" onClick={() => void load(true)}>Refresh</Button>
        </span>
      </div>
      {task ? (
        <p className="serverFoldNote">
          This server's pages, from {task.key}'s feedback. Reply, resolve and reopen there.
          {drafts.length ? " Your drafts show only to you until you submit them on the task." : ""}
        </p>
      ) : null}
      {read.state === "loading" ? <Spinner size={16} label="Reading the feedback…" /> : null}
      {read.state === "failed" ? (
        <Callout tone="danger" fill data-testid="fold-error">
          <span className="projectServersFailed">
            <span>Couldn't read this server's feedback: {read.why}</span>
            <Button size="sm" variant="secondary" leadingIcon="retry" onClick={() => { setRead({ state: "loading" }); void load(); }}>Try again</Button>
          </span>
        </Callout>
      ) : null}
      {drafts.map((s) => <div key={s.id} data-testid="my-draft">{card(s)}</div>)}
      {read.state === "ok" && submitted.length === 0 ? (
        <p className="serverFoldEmpty">No feedback yet. Open it with the comment tool, pick an element and submit: it shows here with its status.</p>
      ) : null}
      {listed.map((s) => fresh.has(s.id) ? (
        <div key={s.id} className="serverFoldNew" data-testid="new-item">
          <Badge size="sm" tone="info" dot>New</Badge>
          {card(s)}
        </div>
      ) : <div key={s.id} data-testid="fold-item" data-status={s.card.status}>{card(s)}</div>)}
      {read.state === "ok" && read.more ? (
        <Button size="sm" variant="secondary" data-testid="fold-more" onClick={() => void more()}>Load more</Button>
      ) : null}
    </div>
  );
}

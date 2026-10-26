/**
 * Commenting on the branch preview, on the task page: the hook that holds
 * the task's commenting window (PreviewCommenting), its ways in — every
 * link dude shows to a commentable server (its URL and Preview on the
 * Servers tab and the Overview, a feedback card's Page) and the Feedback
 * tab's line — what is said when commenting is off, the notice while a
 * window is open, and Restart for a server that needs one.
 */

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Button, Callout, Tooltip } from "@dude/design-system/primitives";
import { Icon, plural, useResolvedTheme } from "@dude/design-system";
import type { FeedbackItem } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import type { ServersState } from "../hooks/useServers.ts";
import type { People } from "../people.tsx";
import {
  COMMENTING_UNAVAILABLE, PreviewCommenting, arrivedFromPreview, commentingOffer, previewOrigins, serverOf, showable,
  type CommentingOffer, type CommentingView,
} from "../previewComment.ts";
import type { TaskFeedbackController } from "./FeedbackSection.tsx";

export interface PreviewCommentingControl {
  readonly offer: CommentingOffer;
  readonly view: CommentingView;
  /** Opens a server's page with the overlay, in the task's window. */
  readonly open: (server: { name: string; url: string }) => void;
  /**
   * A link's `onOpen` for server `name`: present only while comments can be
   * made on it, opening its URL in the task's window, or bringing that
   * window forward when it is already open on the server (true when it did).
   * Absent, the link opens the server in a new tab as any link does.
   */
  readonly linkFor: (name: string) => ((url: string) => boolean) | undefined;
  /** A feedback card's Page link on a commentable server: the task's window brought forward when already on that page, else navigated there; false otherwise. */
  readonly openPage: (url: string) => boolean;
  /** Show on preview: the item's page, marked. */
  readonly show: (item: FeedbackItem) => void;
  /** Whether Show on preview can work for the item now. */
  readonly canShow: (item: FeedbackItem) => boolean;
  readonly focus: () => void;
  readonly stop: () => void;
  readonly dismiss: () => void;
  readonly restart: (name: string) => void;
  readonly restarting: boolean;
}

const IDLE: CommentingView = { state: "idle", server: null, saved: 0 };

export function usePreviewCommenting({ client, taskId, taskKey, servers, fb, people, onOpenItem }: {
  client: ApiClient; taskId: string; taskKey: string; servers: ServersState; fb: TaskFeedbackController; people: People;
  onOpenItem: (id: string) => void;
}): PreviewCommentingControl {
  const offer = useMemo(() => commentingOffer(servers.data), [servers.data]);
  const [view, setView] = useState<CommentingView>(IDLE);
  const theme = useResolvedTheme();
  // What each message is checked and answered against: the latest render's, read when it arrives.
  const latest = useRef({ servers, fb, people, taskKey, onOpenItem, theme });
  latest.current = { servers, fb, people, taskKey, onOpenItem, theme };
  const commenting = useMemo(() => new PreviewCommenting({
    win: window,
    taskId,
    read: () => {
      const { servers: s, fb: f, people: p, taskKey: key, theme: t } = latest.current;
      return {
        origins: previewOrigins(s.data), items: f.items, drafts: f.drafts.length, taskKey: key, you: p.you, theme: t,
        name: (id) => (id ? (p.names.get(id) ?? "Someone").split(" ")[0]! : "Someone"),
      };
    },
    upload: async ({ bytes, type, name }) => {
      const blob = new Blob([bytes as BlobPart], { type });
      return (await client.uploadAttachment(taskId, { name, original: blob, delivered: blob })).id;
    },
    createDraft: async (anchor, body) => {
      const item = await client.createFeedbackDraft(taskId, { body, anchor });
      latest.current.fb.reload();
      return { id: item.id, drafts: latest.current.fb.drafts.length + 1 };
    },
    onSaved: (id) => latest.current.fb.flash(id),
    onOpenItem: (id) => latest.current.onOpenItem(id),
    onChange: setView,
    openerArrived: () => arrivedFromPreview(window, taskId),
  }), [client, taskId]);
  useEffect(() => commenting.listen(), [commenting]);
  // The open page's pins and theme follow dude: a save, a submit, an agent's reply, the person switching light or dark.
  useEffect(() => commenting.refresh(), [commenting, fb.items, theme]);
  const { restart } = servers;
  const restarting = servers.busy !== null;
  return useMemo(() => ({
    offer, view,
    open: (s: { name: string; url: string }) => void commenting.comment(s.url, s.name),
    linkFor: (name: string) => offer.kind === "available" && offer.servers.some((s) => s.name === name)
      ? (url: string) => commenting.comment(url, name) : undefined,
    openPage: (url: string) => {
      const s = serverOf(offer, url);
      return s !== null && commenting.visit(url, s.name);
    },
    show: (item: FeedbackItem) => void commenting.show(item),
    canShow: (item: FeedbackItem) => item.anchor.kind === "preview" && showable(offer, item.anchor.url),
    focus: () => commenting.focus(),
    stop: () => commenting.stop(),
    dismiss: () => commenting.dismiss(),
    restart: (name: string) => void restart(name),
    restarting,
  }), [offer, view, commenting, restart, restarting]);
}

/**
 * Preview items' pictures, read through the API with the signed-in client
 * (an <img> can't send the key) once each is first asked for, and let go
 * when the page unmounts. A picture that can't be read is drawn as none.
 */
export function usePictures(client: ApiClient): (attachmentId: string) => string | undefined {
  const [urls, setUrls] = useState<ReadonlyMap<string, string>>(new Map());
  const asked = useRef(new Set<string>());
  const made = useRef<string[]>([]);
  useEffect(() => () => {
    for (const u of made.current) URL.revokeObjectURL(u);
  }, []);
  return (id: string) => {
    if (!asked.current.has(id)) {
      asked.current.add(id);
      client.attachment(id).then((blob) => {
        const url = URL.createObjectURL(blob);
        made.current.push(url);
        setUrls((m) => new Map(m).set(id, url));
      }, () => undefined);
    }
    return urls.get(id);
  };
}

/** Why a preview's links open it without the comment tool. */
export function CommentingUnavailable({ reason }: { reason: "public_url" | "lux" }) {
  const words = COMMENTING_UNAVAILABLE[reason];
  const why = reason === "public_url"
    ? "dude's preview overlay is served from dude's own address, which this deployment doesn't set (auth.public_url)."
    : "lux adds the overlay to a preview's pages from v0.1.32; this lux is older.";
  return (
    <Tooltip content={why}>
      <span className="commentOff" tabIndex={0} data-testid="commenting-unavailable" data-reason={reason}>
        {reason === "public_url" ? <>Commenting on previews needs <code>auth.public_url</code></> : words}
      </span>
    </Tooltip>
  );
}

/**
 * Under the Servers tab's list: that the preview's links open it with the
 * comment tool, or why they don't. Nothing for a run commenting is not about.
 */
export function CommentingNote({ control }: { control: PreviewCommentingControl }): ReactNode {
  const { offer } = control;
  if (offer.kind === "unavailable") return <CommentingUnavailable reason={offer.reason} />;
  if (offer.kind !== "available" || offer.servers.length === 0) return null;
  return (
    <span data-testid="commenting-links">
      Opening a ready preview server's link here starts dude's comment tool; what you save lands in your drafts. Ctrl- or ⌘-click opens a plain tab.
    </span>
  );
}

/** The Feedback tab's way in, drawn as its general line is. */
export function PreviewWayIn({ control }: { control: PreviewCommentingControl }): ReactNode {
  const { offer } = control;
  if (offer.kind === "none") return null;
  if (offer.kind === "unavailable") {
    return (
      <div className="feedbackGeneral feedbackGeneralOff" data-testid="preview-way-in" data-off="">
        <Icon name="globe" size={16} />
        <span>Comment on the branch preview…</span>
        <CommentingUnavailable reason={offer.reason} />
      </div>
    );
  }
  const s = offer.servers[0];
  return (
    <button type="button" className="feedbackGeneral" disabled={!s} onClick={() => s && control.open(s)} data-testid="preview-way-in">
      <Icon name="globe" size={16} />
      <span>Comment on the branch preview…</span>
      <span className="muted">{s ? <>opens <span className="ds-mono">{s.name}</span> with the comment tool · picks land here as drafts</> : "it opens once a server of the preview is ready"}</span>
    </button>
  );
}

/** Whether CommentingNotices has anything to say. */
export const commentingNoticeShown = ({ view, offer }: Pick<PreviewCommentingControl, "view" | "offer">): boolean =>
  view.state !== "idle" || (offer.kind === "available" && offer.restart.length > 0);

/**
 * The task's notices about commenting: a server serving its pages without
 * the overlay (Restart), and the window — open, closed, or not answering.
 */
export function CommentingNotices({ control }: { control: PreviewCommentingControl }): ReactNode {
  const { view, offer } = control;
  const restart = offer.kind === "available" ? offer.restart : [];
  const out: ReactNode[] = [];
  if (view.state === "open" || view.state === "waiting") {
    out.push(
      <Callout key="open" tone="info" data-testid="commenting-notice" data-state={view.state}>
        <span className="feedbackLine">
          <span>
            <span className="ds-live-dot" aria-hidden /> <b>Commenting on <span className="ds-mono">{view.server ?? "the preview"}</span></b> in another window — what you pick there lands in your drafts here.
            <span className="muted"> {view.state === "waiting" ? "Waiting for the page to load…" : `${plural(view.saved, "draft")} saved from it so far.`}</span>
          </span>
          <span className="noticeActions">
            <Button size="sm" variant="secondary" leadingIcon="external" onClick={control.focus} data-testid="commenting-go">Go to the preview</Button>
            <Tooltip content="Takes the comment tool off the preview's page. Your drafts stay here.">
              <Button size="sm" variant="quiet" leadingIcon="stop" onClick={control.stop} data-testid="commenting-stop">Stop commenting</Button>
            </Tooltip>
          </span>
        </span>
      </Callout>,
    );
  } else if (view.state === "closed") {
    const s = offer.kind === "available" ? offer.servers.find((x) => x.name === view.server) ?? offer.servers[0] : undefined;
    out.push(
      <Callout key="closed" tone="neutral" data-testid="commenting-notice" data-state="closed">
        <span className="feedbackLine">
          <span><b>The preview window was closed.</b> {view.saved > 0 ? `The ${plural(view.saved, "draft")} you saved from it are in your drafts.` : "Nothing was saved from it."}</span>
          <span className="noticeActions">
            {s ? <Button size="sm" variant="secondary" leadingIcon="comments" onClick={() => control.open(s)}>Comment on preview</Button> : null}
            <Button size="sm" variant="quiet" onClick={control.dismiss}>Dismiss</Button>
          </span>
        </span>
      </Callout>,
    );
  }
  const silent = view.state === "silent";
  if (silent || restart.length > 0) {
    const names = restart.length > 0 ? restart : view.server ? [view.server] : [];
    out.push(
      <Callout key="restart" tone="attention" data-testid="commenting-restart" data-state={silent ? "silent" : "restart"}>
        <span className="feedbackLine">
          <span>
            {silent ? <b>The preview hasn't answered. </b> : null}
            {restart.length > 0
              ? <>{names.map((n, i) => <span key={n}>{i > 0 ? ", " : ""}<span className="ds-mono">{n}</span></span>)} {names.length === 1 ? "serves its" : "serve their"} pages without the comment tool yet: a preview started before commenting was on gets it once {names.length === 1 ? "it restarts" : "they restart"}.</>
              : <>Its page doesn't carry the comment tool, or is still waking: give it a moment, or open it again.</>}
          </span>
          <span className="noticeActions">
            {restart.map((n) => (
              <Button key={n} size="sm" variant="secondary" leadingIcon="retry" disabled={control.restarting} onClick={() => control.restart(n)} data-testid="commenting-restart-server" data-server={n}>
                Restart {n}
              </Button>
            ))}
            {silent ? <Button size="sm" variant="quiet" onClick={control.dismiss}>Dismiss</Button> : null}
          </span>
        </span>
      </Callout>,
    );
  }
  return out.length > 0 ? <>{out}</> : null;
}

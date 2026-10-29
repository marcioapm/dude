/** Submission-only bridge. Never reads saved feedback or establishes a task controller. */
import { OVERLAY_PROTOCOL, type DudeToOverlay } from "@dude/domain";
import { ApiError, type ApiClient, type BranchPreviewServerAt } from "./api/client.ts";
import { checkMessage, nameTab, pictureBytes } from "./previewComment.ts";

export function branchPreviewRelay(win: Window, opener: Window, origin: string, owner: BranchPreviewServerAt,
  client: ApiClient, onStatus: (status: "connected" | "saving" | "saved" | "failed", reason?: string) => void): () => void {
  const scoped = client.inOrganization(owner.organizationId);
  const tab = nameTab(win);
  let alive = true;
  let pending: string | null = null;
  const answered = new Map<string, DudeToOverlay>();
  const uploads = new Map<string, { picture: string; id: string }>();
  // Bound checks by elapsed time, including failures, rather than hello frequency.
  let nextCheckAt = 0;
  let valid = false;
  let checking: Promise<void> | null = null;
  let page = origin;
  const post = (message: DudeToOverlay) => { if (alive && !opener.closed) opener.postMessage(message, origin); };
  const listener = (e: MessageEvent) => {
    const checked = checkMessage(e, e.source === opener, new Set([origin]));
    if (!checked.ok) return;
    const msg = checked.msg;
    if (msg.type === "hello" || msg.type === "navigated") {
      page = msg.url;
      const session = () => {
        post({ type: "session", v: OVERLAY_PROTOCOL, mode: "branch", task: owner.branch.slice(0, 100), url: page,
          pins: [], drafts: 0, showPins: false, ...(tab ? { dudeWindow: tab } : {}) });
        if (alive) onStatus("connected");
      };
      if (checking) return;
      if (Date.now() < nextCheckAt) { if (valid) session(); return; }
      nextCheckAt = Date.now() + 30_000;
      checking = scoped.validatePreviewServer(owner.previewRunId, owner.server, origin).then((found) => {
        if (found.kind !== "branch" || found.previewRunId !== owner.previewRunId || found.server !== owner.server) {
          throw new ApiError(404, "not_found", "This preview ended. Open the new preview and connect again.");
        }
        valid = true;
        session();
      }).catch((err: unknown) => {
        valid = false;
        // A transport failure must not discard an in-flight submission's nonce.
        if (err instanceof ApiError && err.status >= 400 && err.status < 500) {
          post({ type: "refused", v: OVERLAY_PROTOCOL, reason: err.message });
        }
        if (alive) onStatus("failed", err instanceof Error ? err.message : "Preview unavailable");
      }).finally(() => { checking = null; });
      return;
    }
    if (msg.type !== "draft") return; // No task open/reply/resolve routing.
    const done = answered.get(msg.nonce);
    if (done) { post(done); return; }
    if (pending !== null) return;
    pending = msg.nonce;
    onStatus("saving");
    void (async () => {
      let attachmentId: string | undefined;
      if (msg.picture) {
        const image = pictureBytes(msg.picture);
        if (!image) throw new Error("The screenshot is not a PNG or JPEG image.");
        // A requested screenshot is never silently dropped from a submission.
        const uploaded = uploads.get(msg.nonce);
        attachmentId = uploaded?.picture === msg.picture ? uploaded.id
          : await scoped.uploadPreviewAttachment(owner.previewRunId, owner.server, image);
        uploads.set(msg.nonce, { picture: msg.picture, id: attachmentId });
        if (uploads.size > 50) uploads.delete(uploads.keys().next().value!);
      }
      const item = await scoped.submitPreviewFeedback(owner.previewRunId, owner.server, {
        body: msg.body, anchor: { ...msg.anchor, ...(attachmentId ? { attachmentId } : {}) }, submissionKey: msg.nonce,
      });
      const saved: DudeToOverlay = { type: "saved", v: OVERLAY_PROTOCOL, nonce: msg.nonce, id: item.id,
        drafts: 0, picture: attachmentId ? "attached" : "none" };
      answered.set(msg.nonce, saved);
      if (answered.size > 50) answered.delete(answered.keys().next().value!);
      post(saved);
      if (alive) onStatus("saved");
    })().catch((err: unknown) => {
      const reason = err instanceof Error ? err.message : "Feedback submission failed";
      const definite = err instanceof ApiError && err.status >= 400 && err.status < 500;
      post({ type: definite ? "refused" : "retryable", v: OVERLAY_PROTOCOL, nonce: msg.nonce, reason });
      if (alive) onStatus("failed", reason);
    }).finally(() => { pending = null; });
  };
  win.addEventListener("message", listener);
  return () => { alive = false; win.removeEventListener("message", listener); };
}

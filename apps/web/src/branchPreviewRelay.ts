/** Submission-only bridge. Never reads saved feedback or establishes a task controller. */
import { OVERLAY_PROTOCOL, type DudeToOverlay } from "@dude/domain";
import type { ApiClient, BranchPreviewServerAt } from "./api/client.ts";
import { checkMessage, nameTab, pictureBytes } from "./previewComment.ts";

export function branchPreviewRelay(win: Window, opener: Window, origin: string, owner: BranchPreviewServerAt,
  client: ApiClient, onStatus: (status: "connected" | "saving" | "saved" | "failed", reason?: string) => void): () => void {
  const scoped = client.inOrganization(owner.organizationId);
  const tab = nameTab(win);
  let alive = true;
  let pending: string | null = null;
  const answered = new Map<string, DudeToOverlay>();
  const post = (message: DudeToOverlay) => { if (alive && !opener.closed) opener.postMessage(message, origin); };
  const listener = (e: MessageEvent) => {
    const checked = checkMessage(e, e.source === opener, new Set([origin]));
    if (!checked.ok) return;
    const msg = checked.msg;
    if (msg.type === "hello" || msg.type === "navigated") {
      // Revalidate the live logical owner on every heartbeat. An origin reused
      // by another incarnation never silently retargets this relay.
      void scoped.previewServerAt(origin).then((found) => {
        if (found.kind !== "branch" || found.previewRunId !== owner.previewRunId || found.server !== owner.server) {
          throw new Error("This preview ended. Open the new preview and connect again.");
        }
        post({ type: "session", v: OVERLAY_PROTOCOL, mode: "branch", task: owner.branch.slice(0, 100), url: msg.url,
          pins: [], drafts: 0, showPins: false, ...(tab ? { dudeWindow: tab } : {}) });
        if (alive) onStatus("connected");
      }).catch((err: unknown) => {
        post({ type: "refused", v: OVERLAY_PROTOCOL, reason: err instanceof Error ? err.message : "Preview unavailable" });
        if (alive) onStatus("failed", err instanceof Error ? err.message : "Preview unavailable");
      });
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
        attachmentId = await scoped.uploadPreviewAttachment(owner.previewRunId, owner.server, image);
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
      post({ type: "refused", v: OVERLAY_PROTOCOL, nonce: msg.nonce, reason });
      if (alive) onStatus("failed", reason);
    }).finally(() => { pending = null; });
  };
  win.addEventListener("message", listener);
  return () => { alive = false; win.removeEventListener("message", listener); };
}

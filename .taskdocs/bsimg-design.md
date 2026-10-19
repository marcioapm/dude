# Session chat images — design

Premise check: a task's Chat (`POST /internal/tasks/{id}/chat`, RunScreen `chat` mode) takes NO images
today (chat.go has no attachmentIds; RunScreen's chat branch has no tray). Images reach agents through
steer (`delivery.Steer` → `Attach` → `run.steered.attachments`) and answers (`api.go answer`). The
session follows that machinery: `Attach` + directive + `deliverDirectives`/`DirectiveAttachments`.

1. Storage: `migrations/157_session_attachments.sql` — session_id FK cascade, task_id nullable,
   CHECK one owner, index. `apps/control-plane/test/migrate.test.ts` lists gain 157.
2. Upload: `apps/control-plane/src/api/routes/attachments.ts` — `upload` split into owner lookup +
   shared `store()`; `POST /v1/brainstorms/:id/attachments` checks `session_role` (null→404, read→403).
3. Serve/remove: same file — a row with session_id requires `session_role(session, person)`; else 404.
   Test: `apps/control-plane/test/attachments.test.ts`.
4. Sending: `orchestrator/internal/delivery/attachments.go` `Attach(owner AttachOwner{TaskID|SessionID})`,
   COALESCE on nullable task_id (also `SyncTaskImagesTx`); `orchestrator/internal/api/sessions.go`
   `sessionChat` takes `attachmentIds`; text or image required (steer's rule); backend `text` schema in
   `apps/control-plane/src/api/routes/sessions.ts`.
   sessionChat branches:
   - live agent, QueueDirective → Attach to directive; chat.message payload `attachments`.
   - held for another member → same directive, held_for set; images ride it.
   - no live agent (startSessionAgent) → 409 `images_need_agent`: a briefing carries no images and the
     brainstorm spec has no prompt-image source (would need a per-Run prompt-image record = new
     mechanism). Refused, reported.
   - model swap (ReplaceBrainstorm) → same 409 refusal.
   - answers single question, same agent → Attach to the answer directive; question.answered payload
     `attachments` (matches `api.go answer`).
   - answers via fresh agent (answerByFreshAgent) → 409 refusal.
   - sessionHandOver to a live agent: images ride the supersedes chain already; ChatEvent payload now
     carries `attachments` (as conductor handTo). To a new agent: still fails as before.
5. Delivery: `phases/sync.go deliverDirectives` → `DirectiveAttachments` → `readAttachments` →
   `Lux.Input`; no task_id use. Proved by Go test with fakelux in
   `orchestrator/internal/phases` (or api) session test.
6. Web: `apps/web/src/hooks/useImages.tsx` useImageTray gains an owner kind; `api/client.ts`
   `uploadSessionAttachment`, `sessionChat(..., attachmentIds)`; `screens/SessionScreen.tsx` tray,
   paperclip, paste, AttachDropZone, sent images + ImageViewer. No design-system change.
7. Docs: `docs/design/session-welcome.md`, migration comment.

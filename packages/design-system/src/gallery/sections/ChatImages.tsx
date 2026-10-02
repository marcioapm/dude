import { useState } from "react";
import { Block, Col, Label, Panes, type PaneMode } from "../Frame.tsx";
import { ChatComposer } from "../../components/ChatComposer.tsx";
import { ChatMessage } from "../../components/ChatMessage.tsx";
import { AttachDropZone, ImageViewer, MessageImages, type ComposerAttachment, type SentImage } from "../../components/ImageAttachments.tsx";
import { at } from "../fixtures.tsx";

/** A screenshot stand-in, drawn: the gallery loads nothing. */
function shot(w: number, h: number, label: string, ink: string): string {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" font-family="Helvetica,Arial">
<rect width="${w}" height="${h}" fill="#f6f7f9"/><rect width="${w}" height="${Math.round(h / 14)}" fill="#ffffff"/>
<rect x="${w * 0.05}" y="${h * 0.16}" width="${w * 0.5}" height="${h * 0.72}" rx="12" fill="#ffffff" stroke="#e5e7eb"/>
<rect x="${w * 0.6}" y="${h * 0.16}" width="${w * 0.35}" height="${h * 0.45}" rx="12" fill="#ffffff" stroke="#e5e7eb"/>
<rect x="${w * 0.09}" y="${h * 0.76}" width="${w * 0.42}" height="${h * 0.07}" rx="8" fill="#2563eb"/>
<text x="${w * 0.05}" y="${h * 0.12}" font-size="${Math.round(h / 22)}" font-weight="700" fill="${ink}">${label}</text></svg>`;
  return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;
}

const CHECKOUT = shot(1200, 760, "Payment", "#111111");
const SUMMARY = shot(900, 900, "Summary v3", "#4c1d95");
const CONSOLE = shot(1000, 560, "Console", "#b91c1c");
const PHONE = shot(390, 844, "9:41", "#111111");

const SENT: SentImage[] = [
  { id: "att_1", name: "checkout-yearly.png", src: CHECKOUT,
    delivered: { width: 1200, height: 760, contentType: "image/png", bytes: 412 * 1024 },
    original: { width: 2400, height: 1520, contentType: "image/png", bytes: 1.9 * 1024 * 1024 } },
  { id: "att_2", name: "Summary v3.png", src: SUMMARY,
    delivered: { width: 900, height: 900, contentType: "image/png", bytes: 188 * 1024 },
    original: { width: 900, height: 900, contentType: "image/png", bytes: 188 * 1024 } },
];

const TRAY: ComposerAttachment[] = [
  { id: "c1", name: "checkout-yearly.png", previewUrl: CHECKOUT, state: "ready", badge: "412 KB", attachmentId: "att_1" },
  { id: "c2", name: "Summary v3.png", previewUrl: SUMMARY, state: "uploading", progress: 0.62 },
];

const LIMITS = (
  <span style={{ display: "flex", flexDirection: "column", gap: 2, maxWidth: 260 }}>
    <b>Attach images</b>
    <span>Or paste a screenshot, or drop files on the conversation.</span>
    <span>PNG, JPEG, WebP, GIF · up to 6 · up to 10 MB each</span>
    <span>Large images are scaled down to 2000 px before the agent gets them. The original is kept.</span>
  </span>
);

const frame = { border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, overflow: "hidden" } as const;

/** Images a person sends an agent: every state of the tray, the drop target, the transcript and the viewer. */
export function ImagesBlock({ mode }: { readonly mode: PaneMode }) {
  const [viewer, setViewer] = useState<number | null>(null);
  const [tray, setTray] = useState<ComposerAttachment[]>(TRAY);
  return (
    <Block
      id="ch-images"
      title="Images (composer, transcript, viewer)"
      note="A person can send an agent images with a steer, an answer or a task's prompt: same tray, same rules. The paperclip sits first in the action row with the limits in its tooltip; paste and drop work too, and the drop target is the whole conversation, saying who gets them and when in the steer's own words. Chips upload in the background with a ring; Send waits for them, and Enter during an upload sends once they are up. A chip that cannot go says why in place, the warning line under the field says how many and why, and Send stays off until they are removed. Sent, a turn shows its images under its words — one large, several in a row, name · size on hover — and its receipt covers them. The viewer shows what the agent got, and from what when it was scaled. Nothing here reads, scales or uploads a file: the app does."
    >
      <Panes mode={mode} surface>
        <Col>
          <Label>tray — one uploaded, one uploading; Send waits</Label>
          <div style={frame}>
            <ChatComposer running canInterrupt landsHint="Lands after the current tool" defaultValue="VAT stays at €0 when I switch to yearly — see the first one."
              attachments={tray} onAttachFiles={() => undefined} attachHint={LIMITS} onRemoveAttachment={(id) => setTray((t) => t.filter((a) => a.id !== id))}
              onSubmit={() => undefined} />
          </div>
          <Label>uploaded — Send is on; a message may be images alone</Label>
          <div style={frame}>
            <ChatComposer running canInterrupt landsHint="Lands after the current tool" attachments={[TRAY[0]!]} onAttachFiles={() => undefined}
              onRemoveAttachment={() => undefined} onSubmit={() => undefined} />
          </div>
          <Label>cannot be sent — in place on the chip, a warning line, Send off</Label>
          <div style={frame}>
            <ChatComposer running canInterrupt landsHint="Lands after the current tool" defaultValue="Same thing on Safari:"
              attachments={[
                { id: "e0", name: "console.png", previewUrl: CONSOLE, state: "ready", badge: "74 KB", attachmentId: "att_3" },
                { id: "e1", name: "huge.png", previewUrl: CHECKOUT, state: "error", error: "38 MB · max 10", errorDetail: "one is over 10 MB" },
                { id: "e2", name: "spec.pdf", state: "error", error: "Not an image", errorDetail: "only PNG, JPEG, WebP and GIF can be sent" },
              ]}
              onAttachFiles={() => undefined} onRemoveAttachment={() => undefined} onSubmit={() => undefined} />
          </div>
          <Label>answer with a screenshot</Label>
          <div style={frame}>
            <ChatComposer question={{ id: "q1", askedBy: "Orchestrator", text: "Does the summary card overflow on a phone?" }}
              defaultValue="It overflows — VAT amount is cut off on the right."
              attachments={[{ id: "p1", name: "phone.png", previewUrl: PHONE, state: "ready", badge: "96 KB", attachmentId: "att_4" }]}
              onAttachFiles={() => undefined} onRemoveAttachment={() => undefined} onSubmit={() => undefined} />
          </div>
          <Label>no storage — the paperclip is off and says why</Label>
          <div style={frame}>
            <ChatComposer running onAttachFiles={() => undefined} attachDisabledReason="Image storage isn't set up" onSubmit={() => undefined} />
          </div>
          <Label>drop target — the whole conversation; drag a file over it</Label>
          <AttachDropZone onFiles={() => undefined}
            detail={<>They go with your next steer to <b>Implement</b>. It reads them after the current tool.</>}>
            <div style={{ height: 140, display: "grid", placeItems: "center", color: "var(--ds-color-text-muted)", background: "var(--ds-color-surface)" }}>
              Drag an image from your desktop over here
            </div>
          </AttachDropZone>
          <Label>in the transcript — several in a row, read; one large</Label>
          <ChatMessage role="human" name="Márcio Martins" intent="steer" content="VAT stays at €0 when I switch to yearly — see the first one. It should match the design in the second."
            startedAt={at(1_000_000)} deliveredAt={at(1_040_000)} read readAfter="Bash"
            attachments={<MessageImages images={SENT} onOpen={setViewer} />} />
          <ChatMessage role="human" name="Márcio Martins" intent="answer" inReplyTo="Does the summary card overflow on a phone?" content="It overflows."
            startedAt={at(1_100_000)} deliveredAt={at(1_101_000)}
            attachments={<MessageImages images={[{ id: "att_4", name: "phone.png", src: PHONE,
              delivered: { width: 390, height: 844, contentType: "image/png", bytes: 96 * 1024 },
              original: { width: 390, height: 844, contentType: "image/png", bytes: 96 * 1024 } }]} />} />
          <Label>not delivered — the images stay with it, with Retry</Label>
          <ChatMessage role="human" name="Márcio Martins" intent="steer" content="And here's the console error from Spain." startedAt={at(1_200_000)}
            deliveredAt={null} failed="this agent cannot take images" onRetry={() => undefined}
            attachments={<MessageImages images={[{ id: "att_3", name: "console.png", src: CONSOLE,
              delivered: { width: 1000, height: 560, contentType: "image/png", bytes: 74 * 1024 },
              original: { width: 1000, height: 560, contentType: "image/png", bytes: 74 * 1024 } }]} />} />
        </Col>
      </Panes>
      {/* Once, outside the panes: it covers the page, whichever pane opened it. */}
      <ImageViewer images={SENT.map((s) => ({ ...s, originalSrc: s.src }))} index={viewer} onIndexChange={setViewer} onClose={() => setViewer(null)}
        context="Márcio · steer to Implement · 15:52" readAt="15:52:40" onDownload={() => undefined} />
    </Block>
  );
}

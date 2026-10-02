/**
 * The composer's image tray, mounted: Send waits for uploads and refuses an
 * image that cannot go, a message may be images alone, Enter during an
 * upload sends once it is done, and pasting an image attaches it. The drop
 * zone claims pastes of files and refused drops.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ChatComposer, type ComposerSubmission } from "../src/components/ChatComposer.tsx";
import { AttachDropZone, ImageViewer, MessageImages, type ComposerAttachment, type SentImage } from "../src/components/ImageAttachments.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;

afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function render(el: React.ReactElement) {
  if (!host) {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
  }
  await act(async () => root!.render(<TooltipProvider>{el}</TooltipProvider>));
}

const q = <T extends Element>(sel: string) => document.querySelector<T>(sel);
const send = () => q<HTMLButtonElement>('button[type="submit"]')!;

const ready: ComposerAttachment = { id: "c1", name: "a.png", state: "ready", badge: "4 KB", attachmentId: "att_1" };
const uploading: ComposerAttachment = { id: "c2", name: "b.png", state: "uploading", progress: 0.4 };
const broken: ComposerAttachment = { id: "c3", name: "big.png", state: "error", error: "38 MB · max 10", errorDetail: "one is over 10 MB" };

test("Send is off while an image uploads, and on once it is up — images alone are a message", async () => {
  const sent: ComposerSubmission[] = [];
  const el = (attachments: ComposerAttachment[]) => (
    <ChatComposer mode="steer" attachments={attachments} onAttachFiles={() => undefined} onSubmit={(s) => void sent.push(s)} />
  );
  await render(el([ready, uploading]));
  expect(send().disabled).toBe(true);
  expect(q('[data-testid="upload-hint"]')?.textContent).toBe("Uploading 1 of 2…");
  await render(el([ready]));
  expect(send().disabled).toBe(false);
  await act(async () => send().click());
  expect(sent).toEqual([{ mode: "steer", text: "", interrupt: false, attachmentIds: ["att_1"] }]);
});

test("an image that cannot be sent keeps Send off and says why, on the chip and under the field", async () => {
  await render(<ChatComposer mode="steer" defaultValue="see" attachments={[ready, broken]} onAttachFiles={() => undefined} onSubmit={() => undefined} />);
  expect(send().disabled).toBe(true);
  expect(q('[data-state="error"]')?.textContent).toContain("38 MB · max 10");
  expect(q('[data-testid="attachment-warning"]')?.textContent).toBe("1 can't be sent: one is over 10 MB. Remove it to send the rest.");
});

test("Enter during an upload sends once the images are up, with them", async () => {
  const sent: ComposerSubmission[] = [];
  const el = (attachments: ComposerAttachment[]) => (
    <ChatComposer mode="answer" question={{ id: "q1", text: "?" }} defaultValue="it overflows" attachments={attachments}
      onAttachFiles={() => undefined} onSubmit={(s) => void sent.push(s)} />
  );
  await render(el([uploading]));
  const field = q<HTMLTextAreaElement>("textarea")!;
  await act(async () => void field.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
  expect(sent).toEqual([]);
  await render(el([{ ...uploading, state: "ready", attachmentId: "att_2" }]));
  expect(sent).toEqual([{ mode: "answer", questionId: "q1", text: "it overflows", attachmentIds: ["att_2"] }]);
});

test("pasting an image attaches it; pasting text does not", async () => {
  const got: File[][] = [];
  await render(<ChatComposer mode="steer" onAttachFiles={(f) => void got.push(f)} onSubmit={() => undefined} />);
  const field = q<HTMLTextAreaElement>("textarea")!;
  const image = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "shot.png", { type: "image/png" });
  const paste = (files: File[]) => {
    const e = new Event("paste", { bubbles: true, cancelable: true }) as Event & { clipboardData: unknown };
    e.clipboardData = { files, getData: () => "" };
    field.dispatchEvent(e);
    return e;
  };
  await act(async () => void paste([image]));
  expect(got.map((f) => f.map((x) => x.name))).toEqual([["shot.png"]]);
  const text = await act(async () => paste([]));
  expect(text.defaultPrevented).toBe(false);
  expect(got.length).toBe(1);
});

test("a zone that takes pastes claims files alone or with their own names, and leaves real text to the field", async () => {
  const got: File[][] = [];
  const zone = (reason?: string) => (
    <AttachDropZone takePaste onFiles={(f) => void got.push(f)} disabledReason={reason}><input data-testid="field" /></AttachDropZone>
  );
  await render(zone());
  const field = q<HTMLInputElement>('[data-testid="field"]')!;
  const image = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "design.png", { type: "image/png" });
  const fire = (type: string, prop: "clipboardData" | "dataTransfer", data: object) => {
    const e = new Event(type, { bubbles: true, cancelable: true }) as Event & Record<string, unknown>;
    e[prop] = data;
    field.dispatchEvent(e);
    return e;
  };
  const paste = (types: string[], files: File[], text = "") =>
    act(async () => fire("paste", "clipboardData", { types, files, getData: () => text }));

  expect((await paste(["Files"], [image])).defaultPrevented).toBe(true);
  expect((await paste(["Files", "text/plain"], [image], "see the header")).defaultPrevented).toBe(false);
  expect((await paste(["Files", "text/plain"], [image], " design.png \n")).defaultPrevented).toBe(true);
  expect((await paste(["text/plain"], [], "just words")).defaultPrevented).toBe(false);
  expect(got.length).toBe(3);

  await render(zone("Image storage isn't set up"));
  const drag = { types: ["Files"], items: [image], files: [image], dropEffect: "" };
  await act(async () => void fire("dragenter", "dataTransfer", drag));
  expect(q('[data-testid="drop-overlay"]')?.textContent).toContain("Image storage isn't set up");
  const drop = await act(async () => fire("drop", "dataTransfer", drag));
  expect(drop.defaultPrevented).toBe(true);
  expect(got.length).toBe(3);
});

test("a message the app could not send keeps its words, and the refusal goes no further", async () => {
  const unhandled: unknown[] = [];
  const onUnhandled = (reason: unknown) => void unhandled.push(reason);
  process.on("unhandledRejection", onUnhandled);
  try {
    let tries = 0;
    await render(<ChatComposer mode="steer" defaultValue="look again" onSubmit={async () => {
      tries++;
      throw new Error("not sent");
    }} />);
    await act(async () => send().click());
    // Let a rejection that escaped surface before looking.
    await act(async () => void (await new Promise((r) => setTimeout(r, 10))));
    expect(tries).toBe(1);
    expect(q<HTMLTextAreaElement>("textarea")!.value).toBe("look again");
    expect(send().disabled).toBe(false);
    expect(unhandled).toEqual([]);
  } finally {
    process.off("unhandledRejection", onUnhandled);
  }
});

test("without storage the paperclip is off and says why", async () => {
  await render(<ChatComposer mode="steer" onAttachFiles={() => undefined} attachDisabledReason="Image storage isn't set up" onSubmit={() => undefined} />);
  const clip = q<HTMLButtonElement>('[data-testid="attach-button"]')!;
  expect(clip.disabled).toBe(true);
  expect(clip.getAttribute("aria-label")).toBe("Image storage isn't set up");
});

const IMAGES: SentImage[] = [
  { id: "a", name: "checkout.png", src: "data:,a", delivered: { width: 1200, height: 760, contentType: "image/png", bytes: 412 * 1024 },
    original: { width: 2400, height: 1520, contentType: "image/png", bytes: 1.9 * 1024 * 1024 } },
  { id: "b", name: "summary.webp", src: "data:,b", delivered: { width: 900, height: 900, contentType: "image/webp", bytes: 188 * 1024 },
    original: { width: 900, height: 900, contentType: "image/webp", bytes: 188 * 1024 } },
];

test("the viewer says what the agent got, and from what only when it was scaled; arrows move between images", async () => {
  let index: number | null = 0;
  const el = () => <ImageViewer images={IMAGES} index={index} onIndexChange={(i) => (index = i)} onClose={() => (index = null)} readAt="15:52:40" />;
  await render(el());
  expect(q('[data-testid="viewer-meta"]')?.textContent).toBe("The agent got 1200×760 PNG · 412 KB, scaled from 2400×1520 · 1.9 MB · read at 15:52:40");
  expect(q('[data-testid="viewer-original"]')?.textContent).toBe("Original 2400×1520");
  await act(async () => void q('[data-testid="image-viewer"]')!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true })));
  expect(index).toBe(1);
  await render(el());
  expect(q('[data-testid="viewer-meta"]')?.textContent).toBe("The agent got 900×900 WEBP · 188 KB · read at 15:52:40");
  expect(q('[data-testid="viewer-original"]')).toBeNull();
});

test("a turn's images open the viewer on the one clicked", async () => {
  let opened = -1;
  await render(<MessageImages images={IMAGES} onOpen={(i) => (opened = i)} />);
  const shown = document.querySelectorAll<HTMLButtonElement>('[data-testid="message-image"]');
  expect(shown.length).toBe(2);
  expect(shown[1]!.textContent).toBe("summary.webp900×900 · 188 KB");
  await act(async () => shown[1]!.click());
  expect(opened).toBe(1);
});

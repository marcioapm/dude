/**
 * Images in a task's goal and criteria (`![name](attachment:att_…)`): drawn
 * in place wherever the task is read, and put there by the task dialog.
 *
 * A task's images are read through the API with the signed-in client, as
 * sent turn images are (useSentImages): an <img> cannot send the key, and
 * nothing is ever fetched from another host.
 */

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { ImageViewer, MarkdownImage } from "@dude/design-system";
import { attachmentReferences } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";

/** A local stand-in id for an image held in the browser until its task exists. */
export const LOCAL_PREFIX = "att_local";

interface Shown {
  src: string;
  width: number;
  height: number;
  type: string;
  bytes: number;
}

/**
 * The resolver `Markdown` takes as `attachmentImage`, and the viewer it
 * opens. `local` answers a blob URL for an image not uploaded yet (New
 * task holds them until Create); anything else is read from the API once,
 * when first drawn, and let go when the screen unmounts.
 */
export function useTaskImages(client: ApiClient, local?: (id: string) => string | undefined) {
  const [shown, setShown] = useState<ReadonlyMap<string, Shown | "missing">>(new Map());
  const asked = useRef(new Set<string>());
  const urls = useRef<string[]>([]);
  const [viewing, setViewing] = useState<{ id: string; alt: string } | null>(null);
  useEffect(() => () => {
    for (const url of urls.current) URL.revokeObjectURL(url);
  }, []);

  const load = useCallback((id: string) => {
    if (asked.current.has(id) || id.startsWith(LOCAL_PREFIX)) return;
    asked.current.add(id);
    client.attachment(id).then((blob) => {
      const src = URL.createObjectURL(blob);
      urls.current.push(src);
      setShown((m) => new Map(m).set(id, { src, width: 0, height: 0, type: blob.type, bytes: blob.size }));
    }, () => setShown((m) => new Map(m).set(id, "missing")));
  }, [client]);

  const sized = useCallback((id: string, width: number, height: number) => {
    setShown((m) => {
      const s = m.get(id);
      return s && s !== "missing" && s.width !== width ? new Map(m).set(id, { ...s, width, height }) : m;
    });
  }, []);

  const attachmentImage = useCallback((id: string, alt: string): ReactNode => (
    <TaskImage key={id} id={id} alt={alt} load={load} shown={shown.get(id)} localSrc={local?.(id)}
      onSize={sized} onOpen={() => setViewing({ id, alt })} />
  ), [load, shown, local, sized]);

  const open = viewing ? shown.get(viewing.id) : undefined;
  const localSrc = viewing ? local?.(viewing.id) : undefined;
  const facts = open && open !== "missing" ? { width: open.width || 1, height: open.height || 1, contentType: open.type, bytes: open.bytes }
    : { width: 1, height: 1, contentType: "image/png", bytes: 0 };
  const viewer = viewing ? (
    <ImageViewer
      images={[{ id: viewing.id, name: viewing.alt, src: localSrc ?? (open && open !== "missing" ? open.src : undefined), delivered: facts, original: facts }]}
      index={0}
      onIndexChange={() => {}}
      onClose={() => setViewing(null)}
      context="In the task"
    />
  ) : null;
  return { attachmentImage, viewer };
}

function TaskImage({ id, alt, load, shown, localSrc, onSize, onOpen }: {
  id: string;
  alt: string;
  load: (id: string) => void;
  shown: Shown | "missing" | undefined;
  localSrc: string | undefined;
  onSize: (id: string, width: number, height: number) => void;
  onOpen: () => void;
}) {
  useEffect(() => {
    if (!localSrc) load(id);
  }, [id, localSrc, load]);
  if (!localSrc && shown === "missing") return <MarkdownImage alt={alt} unavailable />;
  const src = localSrc ?? (shown && shown !== "missing" ? shown.src : undefined);
  return (
    <span onLoadCapture={(e) => {
      const img = e.target as HTMLImageElement;
      if (img.naturalWidth) onSize(id, img.naturalWidth, img.naturalHeight);
    }}>
      <MarkdownImage src={src} alt={alt} onOpen={onOpen} />
    </span>
  );
}

/** The placeholder an image sits as while it is made and uploaded. */
export const uploadingMarkdown = (name: string) => `![Uploading ${name.replace(/[[\]]/g, "")}…]()`;

/**
 * The text with each reference to a local stand-in id rewritten to the
 * attachment it was uploaded as (`ids`). Only the id changes: the alt as
 * escaped, angle brackets and the title (the image's layout) stay byte for
 * byte. Other references are left as they are.
 */
export function withUploadedIds(text: string, ids: ReadonlyMap<string, string>): string {
  let out = "";
  let at = 0;
  for (const r of attachmentReferences(text)) {
    const real = ids.get(r.id);
    if (!real) continue;
    const idAt = r.from + urlStart(text.slice(r.from, r.to)) + "attachment:".length;
    out += text.slice(at, idAt) + real;
    at = idAt + r.id.length;
  }
  return out + text.slice(at);
}

/** Where `attachment:` starts in one reference: past the alt's closing `]`, which an escape never is. */
function urlStart(span: string): number {
  let i = 2;
  while (span[i] !== "]") i += span[i] === "\\" ? 2 : 1;
  return span.indexOf("attachment:", i);
}

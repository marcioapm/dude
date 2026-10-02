/**
 * The images a person is attaching to one message, and the images shown
 * with sent ones: everything the app does around the design system's tray
 * and viewer — reading files, making the delivered variant, uploading with
 * progress, removing, and loading sent images for display.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { MessageImages, type ComposerAttachment, type SentImage } from "@dude/design-system/components";
import { formatBytes } from "@dude/design-system";
import { ATTACHMENT_LIMITS, type AttachmentInfo } from "@dude/domain";
import { ApiError, type ApiClient, type AttachmentLimits } from "../api/client.ts";
import { BUDGET_SPENT, ShrinkError, deliveredName, makeChip, sentChips, type Limits, type Prepared } from "../images.ts";

interface Chip extends ComposerAttachment {
  /** What the delivered variant weighs, once made: the message's budget counts it. */
  deliveredBytes?: number;
  /** Made, and kept here until there is a task to upload it to (a task not created yet). */
  prepared?: Prepared;
}

export interface ImageTray {
  attachments: ReadonlyArray<ComposerAttachment>;
  /** Files picked, pasted or dropped. */
  add: (files: File[]) => void;
  remove: (id: string) => void;
  /** After a message is sent: its images' chips go (they are the message's now); any added meanwhile stay. */
  clear: (sentAttachmentIds: ReadonlyArray<string>) => void;
  /** Whether images can be attached; why not when they cannot. */
  disabledReason: string | undefined;
  limits: Limits;
  /**
   * For a tray opened before its task exists: upload what it holds to the
   * task now, and answer the attachment ids, in order. Throws if one fails.
   */
  uploadTo: (taskId: string) => Promise<string[]>;
}

let seq = 0;

/** What may be attached here: the backend's answer, the shared limits until it comes. */
export function useAttachmentLimits(client: ApiClient): AttachmentLimits | null {
  const [limits, setLimits] = useState<AttachmentLimits | null>(null);
  useEffect(() => {
    let live = true;
    client.attachmentLimits().then((l) => live && setLimits(l), () => live && setLimits(null));
    return () => {
      live = false;
    };
  }, [client]);
  return limits;
}

/**
 * The tray for one message to a task. Each file is checked, its delivered
 * variant made within what the message has left of its budget, and both
 * uploaded; the chip shows each step. With no task yet (a task being
 * written), images are made and kept, and `uploadTo` sends them once it exists.
 */
export function useImageTray(client: ApiClient, taskId: string | undefined, limitsAnswer: AttachmentLimits | null): ImageTray {
  const [chips, setChips] = useState<Chip[]>([]);
  const chipsRef = useRef<Chip[]>([]);
  chipsRef.current = chips;
  const previews = useRef(new Map<string, string>());
  const limits: Limits = limitsAnswer ?? ATTACHMENT_LIMITS;

  const update = useCallback((id: string, change: Partial<Chip>) => {
    setChips((cs) => cs.map((c) => (c.id === id ? { ...c, ...change } : c)));
  }, []);
  const uploadFailed = useCallback((id: string, err: unknown) => {
    update(id, { state: "error", error: "Upload failed", errorDetail: err instanceof ApiError ? err.message : "the upload failed" });
  }, [update]);
  const dropPreview = useCallback((id: string) => {
    const url = previews.current.get(id);
    if (url) URL.revokeObjectURL(url);
    previews.current.delete(id);
  }, []);

  useEffect(() => () => {
    for (const url of previews.current.values()) URL.revokeObjectURL(url);
  }, []);

  const add = useCallback((files: File[]) => {
    const room = limits.perMessage - chipsRef.current.length;
    const taken = files.slice(0, Math.max(0, room));
    const over = files.slice(taken.length);
    const fresh: Chip[] = taken.map((f) => {
      const id = `chip_${++seq}`;
      const previewUrl = URL.createObjectURL(f);
      previews.current.set(id, previewUrl);
      return { id, name: f.name || "pasted image", previewUrl, state: "uploading" };
    });
    const refused: Chip[] = over.map((f) => ({ id: `chip_${++seq}`, name: f.name, state: "error",
      error: `max ${limits.perMessage}`, errorDetail: `a message carries at most ${limits.perMessage} images` }));
    setChips((cs) => [...cs, ...fresh, ...refused]);
    // One after another: each one's budget is what the earlier ones left.
    void (async () => {
      for (const [i, file] of taken.entries()) {
        const chip = fresh[i]!;
        let made;
        try {
          const result = await makeChip(chipsRef.current, chip.id, file, limits);
          if ("refused" in result) {
            // Not an image, or one too big to draw: no thumbnail, the file glyph.
            dropPreview(chip.id);
            update(chip.id, { state: "error", error: result.refused.short, errorDetail: result.refused.detail, previewUrl: undefined });
            continue;
          }
          made = result.made;
        } catch (err) {
          const full = err instanceof ShrinkError && err.message === BUDGET_SPENT;
          update(chip.id, full
            ? { state: "error", error: "No room left", errorDetail: BUDGET_SPENT.toLowerCase() }
            : { state: "error", error: "Can't read it", errorDetail: "an image could not be read or made small enough" });
          continue;
        }
        if (!chipsRef.current.some((c) => c.id === chip.id)) continue; // removed meanwhile
        if (!taskId) {
          update(chip.id, { state: "ready", name: made.name, deliveredBytes: made.delivered.size, prepared: made,
            badge: formatBytes(made.delivered.size) });
          continue;
        }
        update(chip.id, { name: made.name, deliveredBytes: made.delivered.size, progress: 0 });
        try {
          const uploaded = await client.uploadAttachment(taskId, made, (p) => update(chip.id, { progress: p }));
          if (!chipsRef.current.some((c) => c.id === chip.id)) {
            void client.removeAttachment(uploaded.id).catch(() => {});
            continue;
          }
          update(chip.id, { state: "ready", attachmentId: uploaded.id, badge: formatBytes(uploaded.bytes), progress: 1 });
        } catch (err) {
          uploadFailed(chip.id, err);
        }
      }
    })();
  }, [client, taskId, limits, update, uploadFailed, dropPreview]);

  const remove = useCallback((id: string) => {
    const chip = chipsRef.current.find((c) => c.id === id);
    if (chip?.attachmentId) void client.removeAttachment(chip.attachmentId).catch(() => {});
    dropPreview(id);
    setChips((cs) => cs.filter((c) => c.id !== id));
  }, [client, dropPreview]);

  const clear = useCallback((sent: ReadonlyArray<string>) => {
    const gone = new Set(sentChips(chipsRef.current, sent).map((c) => c.id));
    for (const id of gone) dropPreview(id);
    setChips((cs) => cs.filter((c) => !gone.has(c.id)));
  }, [dropPreview]);

  const uploadTo = useCallback(async (task: string) => {
    const ids: string[] = [];
    for (const chip of chipsRef.current) {
      if (chip.attachmentId) {
        ids.push(chip.attachmentId);
        continue;
      }
      if (!chip.prepared) continue;
      update(chip.id, { state: "uploading", progress: 0 });
      try {
        const uploaded = await client.uploadAttachment(task, chip.prepared, (p) => update(chip.id, { progress: p }));
        update(chip.id, { state: "ready", attachmentId: uploaded.id, progress: 1 });
        ids.push(uploaded.id);
      } catch (err) {
        uploadFailed(chip.id, err);
        throw err;
      }
    }
    return ids;
  }, [client, update, uploadFailed]);

  const disabledReason = limitsAnswer && !limitsAnswer.enabled ? "Image storage isn't set up" : undefined;
  return { attachments: chips, add, remove, clear, disabledReason, limits, uploadTo };
}

/** The paperclip's tooltip: what may be attached, and how large. */
export function limitsHint(limits: Limits) {
  return (
    <span className="attachLimitsHint">
      <b>Attach images</b>
      <span>Or paste a screenshot, or drop files on the conversation.</span>
      <span>PNG, JPEG, WebP, GIF · up to {limits.perMessage} · up to {limits.originalBytes / 1e6} MB each</span>
      <span>Large images are scaled down to {limits.maxSide} px before the agent gets them. The original is kept.</span>
    </span>
  );
}

/**
 * Sent images, with blob URLs to show them: read through the API (it needs
 * the key, which an <img> cannot send) once a turn showing them scrolls
 * into view, and let go when every turn showing them has unmounted.
 */
export function useSentImages(client: ApiClient) {
  const urls = useRef(new Map<string, string>());
  const asked = useRef(new Set<string>());
  // How many mounted turns show each image id.
  const shownBy = useRef(new Map<string, number>());
  const [, bump] = useState(0);
  useEffect(() => () => {
    for (const url of urls.current.values()) URL.revokeObjectURL(url);
  }, []);
  const load = useCallback((id: string, variant: "delivered" | "original") => {
    const key = `${id}:${variant}`;
    if (asked.current.has(key)) return;
    asked.current.add(key);
    client.attachment(id, variant).then((blob) => {
      // Its turn unmounted while the bytes were on their way.
      if (!asked.current.has(key)) return;
      urls.current.set(key, URL.createObjectURL(blob));
      bump((n) => n + 1);
    }, () => asked.current.delete(key));
  }, [client]);
  const forget = useCallback((id: string) => {
    for (const variant of ["delivered", "original"]) {
      const key = `${id}:${variant}`;
      const url = urls.current.get(key);
      if (url) URL.revokeObjectURL(url);
      urls.current.delete(key);
      asked.current.delete(key);
    }
  }, []);
  /** A turn showing `ids` mounted; returns its unmount. */
  const mounted = useCallback((ids: ReadonlyArray<string>) => {
    for (const id of ids) shownBy.current.set(id, (shownBy.current.get(id) ?? 0) + 1);
    return () => {
      for (const id of ids) {
        const n = (shownBy.current.get(id) ?? 1) - 1;
        if (n > 0) {
          shownBy.current.set(id, n);
          continue;
        }
        shownBy.current.delete(id);
        forget(id);
      }
    };
  }, [forget]);
  /** A turn's images came into view: read the bytes of those not read yet. */
  const visible = useCallback((ids: ReadonlyArray<string>) => {
    for (const id of ids) load(id, "delivered");
  }, [load]);
  /** A sent image for display: its URL once read. */
  const sent = useCallback((a: AttachmentInfo): SentImage & { originalSrc?: string } => {
    const src = urls.current.get(`${a.id}:delivered`);
    const originalSrc = urls.current.get(`${a.id}:original`);
    return {
      id: a.id, name: a.name,
      delivered: { width: a.width, height: a.height, contentType: a.contentType, bytes: a.bytes },
      original: a.original,
      ...(src ? { src } : {}),
      ...(originalSrc ? { originalSrc } : {}),
    };
  }, []);
  const download = useCallback(async (a: AttachmentInfo, variant: "delivered" | "original") => {
    const blob = await client.attachment(a.id, variant);
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = variant === "original" ? deliveredName(a.name, a.original.contentType) : a.name;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }, [client]);
  return { sent, mounted, visible, wantOriginal: (id: string) => load(id, "original"), download };
}

export type SentImages = ReturnType<typeof useSentImages>;

/** A sent turn's images: their bytes read when they scroll into view, let go when the turn unmounts. */
export function TurnImages({ attachments, images, onOpen }: {
  attachments: ReadonlyArray<AttachmentInfo>;
  images: Pick<SentImages, "sent" | "mounted" | "visible">;
  onOpen: (index: number) => void;
}) {
  const ids = attachments.map((a) => a.id);
  const key = ids.join(",");
  const { mounted, visible } = images;
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => mounted(ids), [mounted, key]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const onVisible = useCallback(() => visible(ids), [visible, key]);
  return <MessageImages images={attachments.map(images.sent)} onOpen={onOpen} onVisible={onVisible} />;
}

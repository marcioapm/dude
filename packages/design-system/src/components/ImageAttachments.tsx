import * as RadixDialog from "@radix-ui/react-dialog";
import { useCallback, useEffect, useRef, useState, type ClipboardEvent as ReactClipboardEvent, type DragEvent, type ReactNode, type RefObject } from "react";
import { cx } from "../util/cx.ts";
import { formatBytes } from "../util/format.ts";
import { Icon } from "../icons/index.tsx";
import styles from "./ImageAttachments.module.css";

/**
 * Images a person sends an agent: the composer's tray of chips, the drop
 * target over a conversation, the images under a sent turn, and the viewer
 * they open in. Nothing here reads a file, scales an image or uploads: the
 * app does, and says how far along each chip is.
 */

/** One chip in the composer's tray. */
export interface ComposerAttachment {
  /** The app's key for the chip, stable while it lives. */
  readonly id: string;
  readonly name: string;
  /** A URL to draw the thumbnail from (a blob URL the app made). */
  readonly previewUrl?: string | undefined;
  /** uploading: the ring shows `progress`; error: it cannot be sent and says why. */
  readonly state: "uploading" | "ready" | "error";
  /** 0..1 while uploading; absent is indeterminate. */
  readonly progress?: number | undefined;
  /** The size badge ("412 KB"), once known. */
  readonly badge?: string | undefined;
  /** Why it cannot be sent, short, on the chip ("38 MB · max 10"). */
  readonly error?: string | undefined;
  /** The longer reason, for the warning line ("one is over 10 MB"). */
  readonly errorDetail?: string | undefined;
  /** The uploaded attachment's id, sent with the message once ready. */
  readonly attachmentId?: string | undefined;
}

/** A progress ring over an uploading chip: determinate from `progress`, a sweep without. */
function ProgressRing({ progress }: { readonly progress: number | undefined }) {
  const r = 14;
  const length = 2 * Math.PI * r;
  const done = progress === undefined ? 0.25 : Math.max(0, Math.min(1, progress));
  return (
    <svg className={cx(styles["ring"], progress === undefined && styles["ringSweep"])} viewBox="0 0 36 36" width={30} height={30}
      role="progressbar" aria-label="Uploading"
      {...(progress !== undefined ? { "aria-valuemin": 0, "aria-valuemax": 100, "aria-valuenow": Math.round(done * 100) } : {})}>
      <circle className={styles["ringTrack"]} cx={18} cy={18} r={r} />
      <circle className={styles["ringFill"]} cx={18} cy={18} r={r} strokeDasharray={`${done * length} ${length}`} transform="rotate(-90 18 18)" />
    </svg>
  );
}

export interface AttachmentChipProps {
  readonly attachment: ComposerAttachment;
  readonly onRemove?: ((id: string) => void) | undefined;
}

/** One image waiting to be sent: its thumbnail, a ring while it uploads, why it cannot go, and ✕. */
export function AttachmentChip({ attachment: a, onRemove }: AttachmentChipProps) {
  return (
    <div className={cx(styles["chip"], a.state === "uploading" && styles["chipUploading"], a.state === "error" && styles["chipError"])}
      data-testid="attachment-chip" data-state={a.state} title={a.error ? `${a.name}: ${a.errorDetail ?? a.error}` : a.name}>
      {a.previewUrl ? <img className={styles["chipImage"]} src={a.previewUrl} alt={a.name} draggable={false} /> : (
        <span className={styles["chipFile"]}><Icon name="file" size={20} /><span className={styles["chipFileName"]}>{a.name}</span></span>
      )}
      {a.state === "uploading" ? <span className={styles["chipRing"]}><ProgressRing progress={a.progress} /></span> : null}
      {a.state === "ready" && a.badge ? <span className={styles["chipBadge"]}>{a.badge}</span> : null}
      {a.state === "error" && a.error ? <span className={styles["chipErrorLabel"]} role="status">{a.error}</span> : null}
      {onRemove ? (
        <button type="button" className={styles["chipRemove"]} aria-label={`Remove ${a.name}`} onClick={() => onRemove(a.id)}>
          <Icon name="close" size={12} strokeWidth={2} />
        </button>
      ) : null}
    </div>
  );
}

/** The warning under a tray holding images that cannot be sent: how many and why. */
export function attachmentWarning(attachments: ReadonlyArray<ComposerAttachment>): ReactNode {
  const bad = attachments.filter((a) => a.state === "error");
  if (bad.length === 0) return null;
  const reasons = [...new Set(bad.map((a) => a.errorDetail ?? a.error ?? "it cannot be sent"))];
  const it = bad.length === 1 ? "it" : "them";
  return (
    <>
      <b>{bad.length} can't be sent:</b> {reasons.join("; ")}. Remove {it} to send the rest.
    </>
  );
}

// -------------------------------------------------------------------------

export interface AttachDropZoneProps {
  /** Files dropped or pasted on it. */
  readonly onFiles: (files: File[]) => void;
  /** Off: no overlay, nothing taken, drags left to the browser (a finished session, another view). */
  readonly disabled?: boolean | undefined;
  /**
   * Why nothing can be attached ("Image storage isn't set up"). Unlike
   * `disabled`, a file dragged over still shows the overlay, saying this,
   * and its drop is swallowed so the browser does not open the file.
   */
  readonly disabledReason?: ReactNode;
  /** Who gets them and when: "They go with your next steer to Implement. It reads them after the current tool." */
  readonly detail?: ReactNode;
  /** Also take images pasted anywhere inside it. */
  readonly takePaste?: boolean | undefined;
  readonly className?: string | undefined;
  readonly children: ReactNode;
}

const hasFiles = (e: DragEvent) => Array.from(e.dataTransfer?.types ?? []).includes("Files");

/**
 * Takes a paste's files for `onFiles`. The paste's text, when it has some
 * (a caption, a URL), is left to go into the field; a paste of files alone
 * is claimed. True when it took any.
 */
export function takePastedFiles(e: ReactClipboardEvent, onFiles: (files: File[]) => void): boolean {
  const files = Array.from(e.clipboardData?.files ?? []);
  if (files.length === 0) return false;
  if (!pasteHasText(e.clipboardData, files)) e.preventDefault();
  onFiles(files);
  return true;
}

// A file copied in a file manager also puts its name in text/plain, one line per file; that is not text.
function pasteHasText(data: DataTransfer, files: File[]): boolean {
  if (!Array.from(data.types ?? []).includes("text/plain")) return false;
  const names = new Set(files.map((f) => f.name));
  const lines = data.getData("text/plain").split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  return lines.some((line) => !names.has(line));
}

/**
 * The drop target for images, put around everything a person may drop on
 * (a whole conversation, a whole dialog) so a dropped image is never
 * missed. While files are dragged over it an overlay says how many, who
 * gets them and when — the steer's own "lands" words.
 */
export function AttachDropZone({ onFiles, disabled, disabledReason, detail, takePaste, className, children }: AttachDropZoneProps) {
  const [count, setCount] = useState<number | null>(null);
  // dragenter/dragleave fire for every child crossed; count the depth.
  const depth = useRef(0);
  const reset = () => {
    depth.current = 0;
    setCount(null);
  };
  const refused = disabledReason !== undefined && disabledReason !== null && disabledReason !== false;
  return (
    <div
      className={cx(styles["dropZone"], className)}
      onDragEnter={(e) => {
        if (disabled || !hasFiles(e)) return;
        e.preventDefault();
        depth.current++;
        setCount(e.dataTransfer.items?.length ?? 0);
      }}
      onDragOver={(e) => {
        if (disabled || !hasFiles(e)) return;
        e.preventDefault();
        e.dataTransfer.dropEffect = refused ? "none" : "copy";
      }}
      onDragLeave={(e) => {
        if (disabled || !hasFiles(e)) return;
        depth.current = Math.max(0, depth.current - 1);
        if (depth.current === 0) setCount(null);
      }}
      onDrop={(e) => {
        if (disabled || !hasFiles(e)) return;
        // Claimed even when refused: unclaimed, the browser opens the file in place of the page.
        e.preventDefault();
        reset();
        const files = Array.from(e.dataTransfer.files);
        if (!refused && files.length > 0) onFiles(files);
      }}
      onPaste={takePaste ? (e) => {
        if (disabled || refused) return;
        takePastedFiles(e, onFiles);
      } : undefined}
    >
      {children}
      {count !== null ? (
        <div className={styles["dropOverlay"]} data-testid="drop-overlay" data-refused={refused ? "true" : undefined} aria-live="polite">
          <div className={styles["dropMessage"]}>
            <Icon name={refused ? "warning" : "upload"} size={28} />
            <span className={styles["dropTitle"]}>
              {refused ? "Can't attach images here"
                : count > 1 ? `Drop to attach ${count} images` : count === 1 ? "Drop to attach the image" : "Drop to attach"}
            </span>
            {refused ? <small className={styles["dropDetail"]}>{disabledReason}</small>
              : detail ? <small className={styles["dropDetail"]}>{detail}</small> : null}
          </div>
        </div>
      ) : null}
    </div>
  );
}

// -------------------------------------------------------------------------

/** What one variant of a sent image is. */
export interface ImageFacts {
  readonly width: number;
  readonly height: number;
  /** MIME type: "image/png". */
  readonly contentType: string;
  readonly bytes: number;
}

/** An image sent with a turn. */
export interface SentImage {
  readonly id: string;
  readonly name: string;
  /** A URL for what the agent got (a blob URL the app made); absent while it loads. */
  readonly src?: string | undefined;
  /** What the agent got. */
  readonly delivered: ImageFacts;
  /** The image as picked. */
  readonly original: ImageFacts;
}

const typeLabel = (type: string) => (type.split("/")[1] ?? type).toUpperCase();
const dims = (f: { width: number; height: number }) => `${f.width}×${f.height}`;

/** "1200×760 · 412 KB": an image's caption. */
export function imageCaption(f: ImageFacts): string {
  return `${dims(f)} · ${formatBytes(f.bytes)}`;
}

/** Whether the agent got a scaled-down copy rather than the image as picked. */
export function wasScaled(image: SentImage): boolean {
  return image.original.width !== image.delivered.width || image.original.height !== image.delivered.height;
}

export interface MessageImagesProps {
  readonly images: ReadonlyArray<SentImage>;
  /** Open the viewer on one. */
  readonly onOpen?: ((index: number) => void) | undefined;
  /**
   * Called once, when the images first scroll into view: the app reads
   * their bytes then, not for every turn of a long conversation at once.
   */
  readonly onVisible?: (() => void) | undefined;
}

/** Calls `onVisible` once, when `ref`'s element first comes into view (at once without an IntersectionObserver). */
function useFirstVisible(ref: RefObject<HTMLElement | null>, onVisible: (() => void) | undefined) {
  const latest = useRef(onVisible);
  latest.current = onVisible;
  const wanted = onVisible !== undefined;
  useEffect(() => {
    const el = ref.current;
    if (!wanted || !el) return;
    if (typeof IntersectionObserver === "undefined") {
      latest.current?.();
      return;
    }
    const seen = new IntersectionObserver((entries) => {
      if (!entries.some((e) => e.isIntersecting)) return;
      seen.disconnect();
      latest.current?.();
    }, { rootMargin: "200px" });
    seen.observe(el);
    return () => seen.disconnect();
  }, [ref, wanted]);
}

/** A turn's images, under its words: one shows large, several as a row. Hover gives name · size. */
export function MessageImages({ images, onOpen, onVisible }: MessageImagesProps) {
  const ref = useRef<HTMLDivElement>(null);
  useFirstVisible(ref, onVisible);
  if (images.length === 0) return null;
  const one = images.length === 1;
  return (
    <div ref={ref} className={cx(styles["images"], one && styles["imagesOne"])} data-testid="message-images">
      {images.map((image, i) => (
        <button key={image.id} type="button" className={styles["image"]} onClick={() => onOpen?.(i)}
          aria-label={`Open ${image.name}`} data-testid="message-image">
          {image.src ? (
            <img src={image.src} alt={image.name} draggable={false} decoding="async"
              style={{ aspectRatio: `${image.delivered.width} / ${image.delivered.height}` }} />
          ) : (
            <span className={styles["imagePending"]} style={{ aspectRatio: `${image.delivered.width} / ${image.delivered.height}` }}>
              <Icon name="image" size={20} />
            </span>
          )}
          <span className={styles["caption"]}>
            <span className={styles["captionName"]}>{image.name}</span>
            <span>{imageCaption(image.delivered)}</span>
          </span>
        </button>
      ))}
    </div>
  );
}

// -------------------------------------------------------------------------

export interface ImageViewerProps {
  readonly images: ReadonlyArray<SentImage & {
    /** A URL for the original, once the app has it (onWantOriginal). */
    readonly originalSrc?: string | undefined;
  }>;
  /** The one shown; null closes the viewer. */
  readonly index: number | null;
  readonly onIndexChange: (index: number) => void;
  readonly onClose: () => void;
  /** Who sent it, to whom, when: "Márcio · steer to Implement · 15:52". */
  readonly context?: ReactNode;
  /** When the agent read it, as a time ("15:52:40"); absent until it has. */
  readonly readAt?: string | undefined;
  /** Asked once when a person switches to the original and it has no URL yet. */
  readonly onWantOriginal?: ((index: number) => void) | undefined;
  /** Save the image shown, as the variant shown. */
  readonly onDownload?: ((index: number, variant: "delivered" | "original") => void) | undefined;
}

/**
 * A message's images, full size, over everything: ← → between them, Esc
 * closes. It says what the agent got, and when the browser scaled an image
 * down, from what — "Original W×H" shows that instead.
 */
export function ImageViewer({ images, index, onIndexChange, onClose, context, readAt, onWantOriginal, onDownload }: ImageViewerProps) {
  const open = index !== null && images[index] !== undefined;
  const [original, setOriginal] = useState(false);
  useEffect(() => setOriginal(false), [index]);
  const image = open ? images[index!]! : null;
  const step = useCallback((by: number) => {
    if (index === null || images.length < 2) return;
    onIndexChange((index + by + images.length) % images.length);
  }, [index, images.length, onIndexChange]);
  const scaled = image ? wasScaled(image) : false;
  const showOriginal = original && scaled;
  const src = image ? (showOriginal ? image.originalSrc : image.src) : undefined;
  const meta = image ? (
    <>
      The agent got <code>{dims(image.delivered)} {typeLabel(image.delivered.contentType)} · {formatBytes(image.delivered.bytes)}</code>
      {scaled ? <>, scaled from <code>{dims(image.original)} · {formatBytes(image.original.bytes)}</code></> : null}
      {readAt ? <> · read at {readAt}</> : null}
    </>
  ) : null;
  return (
    <RadixDialog.Root open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <RadixDialog.Portal>
        <RadixDialog.Content className={styles["viewer"]} data-testid="image-viewer"
          onKeyDown={(e) => {
            if (e.key === "ArrowRight") { e.preventDefault(); step(1); }
            if (e.key === "ArrowLeft") { e.preventDefault(); step(-1); }
          }}
          aria-describedby={undefined}>
          {image ? (
            <>
              <div className={styles["viewerTop"]}>
                <RadixDialog.Title className={styles["viewerTitle"]}>{image.name}</RadixDialog.Title>
                {context ? <span className={styles["viewerContext"]}>{context}</span> : null}
                <span className={styles["spacer"]} />
                {scaled ? (
                  <button type="button" className={styles["viewerButton"]} aria-pressed={showOriginal} data-testid="viewer-original"
                    onClick={() => {
                      const next = !original;
                      setOriginal(next);
                      if (next && !image.originalSrc) onWantOriginal?.(index!);
                    }}>
                    {showOriginal ? `Sent ${dims(image.delivered)}` : `Original ${dims(image.original)}`}
                  </button>
                ) : null}
                {onDownload ? (
                  <button type="button" className={styles["viewerButton"]} onClick={() => onDownload(index!, showOriginal ? "original" : "delivered")}>
                    <Icon name="download" size={14} /> Download
                  </button>
                ) : null}
                <RadixDialog.Close className={styles["viewerButton"]} aria-label="Close">
                  <Icon name="close" size={14} />
                </RadixDialog.Close>
              </div>
              <div className={styles["viewerStage"]}>
                {images.length > 1 ? (
                  <button type="button" className={cx(styles["viewerStep"], styles["viewerPrev"])} aria-label="Previous image" onClick={() => step(-1)}>
                    <Icon name="chevron-left" size={20} />
                  </button>
                ) : null}
                {src ? <img className={styles["viewerImage"]} src={src} alt={image.name} data-testid="viewer-image" /> : (
                  <span className={styles["viewerLoading"]}><Icon name="spinner" size={20} /></span>
                )}
                {images.length > 1 ? (
                  <button type="button" className={cx(styles["viewerStep"], styles["viewerNext"])} aria-label="Next image" onClick={() => step(1)}>
                    <Icon name="chevron-right" size={20} />
                  </button>
                ) : null}
              </div>
              <div className={styles["viewerMeta"]} data-testid="viewer-meta">{meta}</div>
              {images.length > 1 ? (
                <div className={styles["viewerStrip"]}>
                  {images.map((im, i) => (
                    <button key={im.id} type="button" className={cx(styles["viewerThumb"], i === index && styles["viewerThumbOn"])}
                      aria-label={im.name} aria-current={i === index ? "true" : undefined} onClick={() => onIndexChange(i)}>
                      {im.src ? <img src={im.src} alt="" /> : <Icon name="image" size={16} />}
                    </button>
                  ))}
                </div>
              ) : null}
            </>
          ) : null}
        </RadixDialog.Content>
      </RadixDialog.Portal>
    </RadixDialog.Root>
  );
}

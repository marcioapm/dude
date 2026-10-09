import { useEffect, useMemo, useState, type HTMLAttributes, type ReactNode } from "react";
import * as RadixDialog from "@radix-ui/react-dialog";
import type { AgentRole } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { formatBytes } from "../util/format.ts";
import { toMs, useNow } from "../util/useNow.ts";
import { Icon } from "../icons/index.tsx";
import { Button, IconButton } from "../primitives/Button.tsx";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { ARTIFACT_KIND_SPECS, artifactKind, type ArtifactKind } from "./ArtifactRow.tsx";
import { Duration } from "./Numbers.tsx";
import styles from "./FileGallery.module.css";

/** One version of a file an agent saved. */
export interface FileVersion {
  readonly id: string;
  readonly name: string;
  readonly contentType: string;
  readonly sizeBytes: number;
  readonly createdAt: string | number | Date;
  /** Who made it: the agent's role, and the session it was in. */
  readonly role: AgentRole;
  readonly session: string;
  /** 1 for the first of its name. */
  readonly version: number;
  /** What its maker said it is for, one short line; shown under the name. */
  readonly description?: string | undefined;
}

/** A file: its versions, newest first — the first is the file. */
export interface GalleryFile {
  readonly name: string;
  readonly versions: ReadonlyArray<FileVersion>;
}

/** Media shows as a picture; everything else as a row. */
const isMedia = (k: ArtifactKind) => k === "image" || k === "video";

const mediaFirst = (f: GalleryFile) => (isMedia(artifactKind(f.versions[0]!.contentType, f.name)) ? 0 : 1);

/**
 * Files in the order the gallery shows them — pictures, then documents —
 * which is the order the viewer walks them.
 */
export function galleryOrder(files: ReadonlyArray<GalleryFile>): GalleryFile[] {
  return [...files].sort((a, b) => mediaFirst(a) - mediaFirst(b));
}

type Filter = "all" | "documents" | "media";

export interface FileGalleryProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  readonly files: ReadonlyArray<GalleryFile>;
  /** A thumbnail's bytes, for images: the app makes the URL (it holds the key). */
  readonly thumbnail?: ((v: FileVersion) => string | undefined) | undefined;
  readonly onOpen: (file: GalleryFile) => void;
  readonly onDownload: (v: FileVersion) => void;
  /** Download every file's latest version, as a zip. */
  readonly onDownloadAll?: (() => void) | undefined;
  /** Beside the total: how long they are kept. */
  readonly note?: ReactNode;
}

/**
 * Everything a task's sessions saved: images and video as a gallery,
 * documents as a list, each opening in the viewer. A file saved again is
 * one file with versions ("v3"), not three. Filters by kind; the total and
 * Download all on the right.
 */
export function FileGallery({ files, thumbnail, onOpen, onDownload, onDownloadAll, note, className, ...rest }: FileGalleryProps) {
  const [filter, setFilter] = useState<Filter>("all");
  const kinds = useMemo(() => new Map(files.map((f) => [f.name, artifactKind(f.versions[0]!.contentType, f.name)])), [files]);
  const media = files.filter((f) => isMedia(kinds.get(f.name)!));
  const documents = files.filter((f) => !isMedia(kinds.get(f.name)!));
  const total = files.reduce((n, f) => n + f.versions[0]!.sizeBytes, 0);
  const segments: Array<[Filter, string, number]> = [
    ["all", "All", files.length],
    ["documents", "Documents", documents.length],
    ["media", "Images & video", media.length],
  ];
  return (
    <div className={cx(styles["root"], className)} {...rest}>
      <div className={styles["bar"]}>
        <div className={styles["segments"]} role="tablist" aria-label="Show">
          {segments.map(([k, label, n]) => (
            <button key={k} type="button" role="tab" aria-selected={filter === k}
              className={cx(styles["segment"], filter === k && styles["on"])} onClick={() => setFilter(k)}>
              {label} <span className={styles["count"]}>{n}</span>
            </button>
          ))}
        </div>
        <span className={styles["spacer"]} />
        <span className={styles["muted"]}>
          {formatBytes(total)}
          {note ? <> · {note}</> : null}
        </span>
        {onDownloadAll ? (
          <Button variant="secondary" size="sm" leadingIcon="archive" onClick={onDownloadAll} data-testid="download-all">
            Download all
          </Button>
        ) : null}
      </div>
      {filter !== "documents" && media.length > 0 ? (
        <section>
          <h3 className={styles["label"]}>Images &amp; video</h3>
          <div className={styles["grid"]}>
            {media.map((f) => {
              const latest = f.versions[0]!;
              const kind = kinds.get(f.name)!;
              const src = kind === "image" ? thumbnail?.(latest) : undefined;
              return (
                <button key={f.name} type="button" className={styles["card"]} onClick={() => onOpen(f)} data-testid="file-card" data-name={f.name}>
                  <span className={cx(styles["picture"], kind === "video" && styles["dark"])}>
                    {src ? <img src={src} alt="" /> : <Icon name={kind === "video" ? "play" : "image"} size={32} />}
                  </span>
                  <span className={styles["caption"]}>
                    <span className={styles["name"]}>
                      <b>{f.name}</b>
                      <Description of={latest} />
                      <small>
                        {ARTIFACT_KIND_SPECS[kind].label} · {formatBytes(latest.sizeBytes)}
                        {f.versions.length > 1 ? <> · <span className={styles["version"]}>v{latest.version}</span></> : null}
                      </small>
                    </span>
                    <span className={styles["who"]}>
                      <AgentAvatar role={latest.role} size="xs" />
                      {ROLE_LABEL[latest.role]}
                    </span>
                  </span>
                </button>
              );
            })}
          </div>
        </section>
      ) : null}
      {filter !== "media" && documents.length > 0 ? (
        <section>
          <h3 className={styles["label"]}>Documents</h3>
          <ul className={styles["list"]}>
            {documents.map((f) => (
              <FileRow key={f.name} file={f} kind={kinds.get(f.name)!} onOpen={() => onOpen(f)} onDownload={() => onDownload(f.versions[0]!)} />
            ))}
          </ul>
        </section>
      ) : null}
    </div>
  );
}

function FileRow({ file, kind, onOpen, onDownload }: { file: GalleryFile; kind: ArtifactKind; onOpen: () => void; onDownload: () => void }) {
  const latest = file.versions[0]!;
  const now = useNow(true, 60_000);
  const at = toMs(latest.createdAt);
  return (
    <li className={styles["row"]} data-testid="file-row" data-name={file.name}>
      <button type="button" className={styles["rowOpen"]} onClick={onOpen}>
        <span className={styles["thumb"]} aria-hidden>
          <Icon name={ARTIFACT_KIND_SPECS[kind].glyph} size={16} />
          <em>{file.name.split(".").pop()}</em>
        </span>
        <span className={styles["name"]}>
          <b>{file.name}</b>
          <Description of={latest} />
          <small>
            {ARTIFACT_KIND_SPECS[kind].label} · {formatBytes(latest.sizeBytes)}
            {file.versions.length > 1 ? <> · <span className={styles["version"]}>v{latest.version}</span></> : null}
          </small>
        </span>
        <span className={styles["who"]}>
          <AgentAvatar role={latest.role} size="xs" />
          {latest.session}
        </span>
        {at !== null ? <Duration ms={Math.max(0, now - at)} format="age" tone="muted" className={styles["age"]} /> : null}
      </button>
      <IconButton icon="download" size="sm" label={`Download ${file.name}`} onClick={onDownload} />
    </li>
  );
}

/** A version's description under its name, in full on hover; nothing without one. */
function Description({ of }: { of: FileVersion }) {
  return of.description ? <span className={styles["description"]} title={of.description} data-testid="file-description">{of.description}</span> : null;
}

// ---------------------------------------------------------------------------
// The viewer
// ---------------------------------------------------------------------------

export interface FileViewerProps {
  /** Every file; prev/next walks them as the gallery shows them. */
  readonly files: ReadonlyArray<GalleryFile>;
  /** The file open, by name; null closes the viewer. */
  readonly open: string | null;
  readonly onOpenChange: (name: string | null) => void;
  /** The version shown; the latest when absent. */
  readonly version?: string | undefined;
  readonly onVersionChange?: ((id: string) => void) | undefined;
  /** The content of the version shown (an ArtifactPreview). */
  readonly children: ReactNode;
  readonly onDownload: (v: FileVersion) => void;
  /** Copy its text; offered for text kinds. */
  readonly onCopy?: ((v: FileVersion) => void) | undefined;
  /** Go to the session that made it. */
  readonly onOpenSession?: ((v: FileVersion) => void) | undefined;
}

/**
 * One file at a time, big, over the page: what it is and who made it, its
 * neighbours a keystroke away (← →), its versions down the side (newest
 * first), and copy, open in session and download. Escape closes it.
 */
export function FileViewer({ files: given, open, onOpenChange, version, onVersionChange, children, onDownload, onCopy, onOpenSession }: FileViewerProps) {
  const files = useMemo(() => galleryOrder(given), [given]);
  const index = files.findIndex((f) => f.name === open);
  const file = index >= 0 ? files[index] : undefined;
  const shown = file ? (file.versions.find((v) => v.id === version) ?? file.versions[0]!) : undefined;
  const step = (by: number) => {
    if (files.length > 0) onOpenChange(files[(index + by + files.length) % files.length]!.name);
  };
  useEffect(() => {
    if (!file) return;
    const key = (e: KeyboardEvent) => {
      if (e.target instanceof HTMLElement && e.target.closest("input, textarea, [contenteditable]")) return;
      if (e.key === "ArrowLeft") step(-1);
      if (e.key === "ArrowRight") step(1);
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  });
  const kind = shown ? artifactKind(shown.contentType, shown.name) : "other";
  const textual = kind === "markdown" || kind === "text" || kind === "json";
  const now = useNow(!!file, 60_000);
  const at = shown ? toMs(shown.createdAt) : null;
  return (
    <RadixDialog.Root open={!!file} onOpenChange={(o) => !o && onOpenChange(null)}>
      <RadixDialog.Portal>
        <RadixDialog.Overlay className={styles["scrim"]} />
        <RadixDialog.Content className={styles["viewer"]} aria-describedby={undefined} data-testid="file-viewer">
          {file && shown ? (
            <>
              <div className={styles["viewerHead"]}>
                <span className={styles["thumb"]} aria-hidden>
                  <Icon name={ARTIFACT_KIND_SPECS[kind].glyph} size={16} />
                </span>
                <span className={styles["name"]}>
                  <RadixDialog.Title className={styles["viewerTitle"]}>{file.name}</RadixDialog.Title>
                  <small>
                    {ARTIFACT_KIND_SPECS[kind].label} · {formatBytes(shown.sizeBytes)} · <AgentAvatar role={shown.role} size="xs" /> {shown.session}
                    {at !== null ? <> · <Duration ms={Math.max(0, now - at)} format="age" tone="muted" /></> : null}
                  </small>
                </span>
                <span className={styles["muted"]}>
                  {index + 1} of {files.length}
                </span>
                <IconButton icon="chevron-right" className={styles["flip"]} size="sm" label="Previous file (←)" onClick={() => step(-1)} />
                <IconButton icon="chevron-right" size="sm" label="Next file (→)" onClick={() => step(1)} />
                {textual && onCopy ? (
                  <Button variant="quiet" size="sm" leadingIcon="copy" onClick={() => onCopy(shown)}>
                    Copy
                  </Button>
                ) : null}
                {onOpenSession ? (
                  <Button variant="quiet" size="sm" onClick={() => onOpenSession(shown)}>
                    Open in session
                  </Button>
                ) : null}
                <Button variant="secondary" size="sm" leadingIcon="download" onClick={() => onDownload(shown)} data-testid="viewer-download">
                  Download
                </Button>
                <RadixDialog.Close asChild>
                  <IconButton icon="close" size="sm" label="Close (Esc)" />
                </RadixDialog.Close>
              </div>
              <div className={styles["viewerMain"]}>
                <div className={cx(styles["stage"], isMedia(kind) && styles["mediaStage"])}>{children}</div>
                {file.versions.length > 1 ? (
                  <nav className={styles["versions"]} aria-label="Versions">
                    <h3 className={styles["label"]}>Versions</h3>
                    {file.versions.map((v, i) => {
                      const vAt = toMs(v.createdAt);
                      return (
                        <button key={v.id} type="button" aria-current={v.id === shown.id}
                          className={cx(styles["versionRow"], v.id === shown.id && styles["current"])}
                          onClick={() => onVersionChange?.(v.id)} data-testid="viewer-version">
                          <span>v{v.version}{i === 0 ? " · latest" : ""}</span>
                          {vAt !== null ? <Duration ms={Math.max(0, now - vAt)} format="age" tone="muted" /> : null}
                        </button>
                      );
                    })}
                  </nav>
                ) : null}
              </div>
            </>
          ) : null}
        </RadixDialog.Content>
      </RadixDialog.Portal>
    </RadixDialog.Root>
  );
}

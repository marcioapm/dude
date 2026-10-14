/**
 * A task's files: everything its sessions saved — notes, a design, a
 * report, screenshots, a recording — as a gallery of pictures and a list
 * of documents, each opening in a viewer with its versions, and all of
 * them downloadable at once as a zip.
 *
 * Bytes are read only when something is shown or downloaded, with the key
 * in a header: what the page shows is a blob URL it made, so no link
 * carries a credential. A page an agent wrote (HTML) is shown only in a
 * sandboxed frame — no scripts, an opaque origin — and the server serves
 * it sandboxed as well.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { ArtifactPreview, FileGallery, FileViewer, artifactKind, PageWithRail, PageRail, PageRailBlock, FacetList } from "@dude/design-system/components";
import { formatBytes } from "@dude/design-system";
import { zipSync } from "fflate";
import type { FileVersion, GalleryFile } from "@dude/design-system/components";
import { Button, EmptyState, useToast } from "@dude/design-system/primitives";
import { DEFAULT_RUN_ROLE, runLabel } from "@dude/domain";
import type { ApiClient, Artifact } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

/** Past this, a text file is downloaded rather than shown: the tab would not survive it. */
const PREVIEW_LIMIT = 2 * 1024 * 1024;
/**
 * Video and pages are shown from a blob — the content needs the key, which
 * a <video src> cannot send — so the whole file is read first. Past this
 * it is downloaded instead of held in the tab.
 */
const MEDIA_LIMIT = 64 * 1024 * 1024;

/** The API lists every version, newest first: one file per name, its versions in that order. */
export function filesOf(artifacts: readonly Artifact[]): GalleryFile[] {
  const byName = new Map<string, FileVersion[]>();
  for (const a of artifacts) {
    const v: FileVersion = {
      id: a.id, name: a.name, contentType: a.contentType, sizeBytes: a.sizeBytes, createdAt: a.createdAt,
      role: a.role ?? DEFAULT_RUN_ROLE, session: runLabel(a), version: a.version, description: a.description,
    };
    byName.set(a.name, [...(byName.get(a.name) ?? []), v]);
  }
  return [...byName.entries()].map(([name, versions]) => ({ name, versions }));
}

/** Save a blob as a file, from a URL the page made. */
export function save(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export const fileKindFacet = (f: GalleryFile) => ["image", "video"].includes(artifactKind(f.versions[0]!.contentType, f.name)) ? "media" : "documents";

export function filterFiles(files: readonly GalleryFile[], kinds: readonly string[], sources: readonly string[]) {
  return files.filter((f) => (!kinds.length || kinds.includes(fileKindFacet(f))) &&
    (!sources.length || sources.includes(f.versions[0]!.session)));
}

export async function displayedFilesZip(client: Pick<ApiClient, "artifactContent">, files: readonly GalleryFile[]): Promise<Blob> {
  const entries = await Promise.all(files.map(async (f) => [f.name, new Uint8Array(await (await client.artifactContent(f.versions[0]!.id)).arrayBuffer())] as const));
  return new Blob([zipSync(Object.fromEntries(entries), { level: 0 })], { type: "application/zip" });
}

export function FilesSection({ client, taskId, taskKey, artifacts, onOpenRun }: {
  client: ApiClient;
  taskId: string;
  /** Names the zip. */
  taskKey?: string | undefined;
  artifacts: readonly Artifact[];
  onOpenRun: (runId: string) => void;
}) {
  const { toast } = useToast();
  const files = useMemo(() => filesOf(artifacts), [artifacts]);
  const [open, setOpen] = useState<string | null>(null);
  const fail = (err: unknown) => toast({ title: errorText(err), tone: "danger" });
  const download = useDownload(client);
  const [kinds, setKinds] = useState<string[]>([]);
  const [sources, setSources] = useState<string[]>([]);
  const [zipping, setZipping] = useState(false);
  const displayed = filterFiles(files, kinds, sources);
  const total = displayed.reduce((n, f) => n + f.versions[0]!.sizeBytes, 0);
  const thumbnails = useThumbnails(client, displayed);
  return (
    <PageWithRail data-testid="files" rail={<PageRail aria-label="File filters and downloads">
      <PageRailBlock label="Kind"><FacetList label="File kind" value={kinds} onValueChange={setKinds}
        options={[{ value: "documents", label: "Documents" }, { value: "media", label: "Images & video" }].map((o) => ({ ...o, count: files.filter((f) => fileKindFacet(f) === o.value).length }))} /></PageRailBlock>
      <PageRailBlock label="Published by"><FacetList label="File source" value={sources} onValueChange={setSources}
        options={[...new Set(files.map((f) => f.versions[0]!.session))].map((s) => ({ value: s, label: s, count: files.filter((f) => f.versions[0]!.session === s).length }))} /></PageRailBlock>
      <PageRailBlock label="Scope"><span className="ds-muted">Files and counts belong to the attempt selected in the task header. No selections means all files in that attempt.</span></PageRailBlock>
      <PageRailBlock label="Download"><Button size="sm" variant="secondary" data-testid="download-all" disabled={zipping || !displayed.length}
        onClick={() => { setZipping(true); void displayedFilesZip(client, displayed).then((b) => save(b, `${taskKey ?? taskId}-files.zip`), fail).finally(() => setZipping(false)); }}>
        {zipping ? "Preparing…" : `Download all · ${formatBytes(total)}`}</Button><p className="ds-muted">One ZIP of every file shown, latest versions only.</p></PageRailBlock>
    </PageRail>}>
      <div className="taskPane">
      {displayed.length ? <FileGallery
        controls={false}
        files={displayed}
        thumbnail={(v) => thumbnails.get(v.id)}
        onOpen={(f) => setOpen(f.name)}
        onDownload={download}
      /> : <EmptyState compact icon="file" title="No matching files" description="Clear a kind or publisher filter to show more files." />}
      <ArtifactViewer client={client} files={displayed} open={open} onOpenChange={setOpen} onOpenSession={(v) => {
        const a = artifacts.find((x) => x.id === v.id);
        if (a?.runId) onOpenRun(a.runId);
      }} />
      </div>
    </PageWithRail>
  );
}

/** Downloading one version: its bytes read with the key, saved under its own name. */
function useDownload(client: ApiClient): (v: FileVersion) => void {
  const { toast } = useToast();
  return (v) => void client.artifactContent(v.id).then((b) => save(b, v.name.split("/").pop() ?? v.name),
    (err: unknown) => toast({ title: errorText(err), tone: "danger" }));
}

/**
 * The viewer over a set of files, open on one by name: its content read
 * when shown, its versions, copy and download. A task's Files and a
 * brainstorm session's rail open the same one.
 */
export function ArtifactViewer({ client, files, open, onOpenChange, onOpenSession }: {
  client: ApiClient;
  files: readonly GalleryFile[];
  open: string | null;
  onOpenChange: (name: string | null) => void;
  /** Go to the agent session that made it; absent where there is none to go to. */
  onOpenSession?: ((v: FileVersion) => void) | undefined;
}) {
  const { toast } = useToast();
  const [version, setVersion] = useState<string | undefined>(undefined);
  const fail = (err: unknown) => toast({ title: errorText(err), tone: "danger" });
  const download = useDownload(client);
  const file = files.find((f) => f.name === open);
  const shown = file ? (file.versions.find((v) => v.id === version) ?? file.versions[0]!) : undefined;
  return (
    <FileViewer
      files={files}
      open={open}
      onOpenChange={(name) => {
        setVersion(undefined);
        onOpenChange(name);
      }}
      version={version}
      onVersionChange={setVersion}
      onDownload={download}
      onCopy={(v) => void client.artifactContent(v.id).then((b) => b.text()).then((t) => navigator.clipboard.writeText(t))
        .then(() => toast({ title: `Copied ${v.name}` }), fail)}
      onOpenSession={onOpenSession}
    >
      {shown ? <Content key={shown.id} client={client} version={shown} /> : null}
    </FileViewer>
  );
}

/**
 * Thumbnails for the gallery's images: each read once, when it appears, and
 * freed when it goes — a new screenshot does not read the others again.
 */
function useThumbnails(client: ApiClient, files: readonly GalleryFile[]): Map<string, string> {
  const [urls, setUrls] = useState(new Map<string, string>());
  const ids = files.map((f) => f.versions[0]!).filter((v) => artifactKind(v.contentType, v.name) === "image" && v.sizeBytes <= PREVIEW_LIMIT)
    .map((v) => v.id).join(",");
  const made = useRef(new Map<string, string>());
  useEffect(() => {
    const wanted = new Set(ids ? ids.split(",") : []);
    for (const [id, url] of made.current) {
      if (!wanted.has(id)) {
        URL.revokeObjectURL(url);
        made.current.delete(id);
      }
    }
    setUrls(new Map(made.current));
    let current = true;
    for (const id of wanted) {
      if (made.current.has(id)) continue;
      void client.artifactContent(id).then((b) => {
        if (!current) return;
        made.current.set(id, URL.createObjectURL(b));
        setUrls(new Map(made.current));
      }, () => undefined);
    }
    return () => {
      current = false;
    };
  }, [client, ids]);
  // Freed with the gallery.
  useEffect(() => () => made.current.forEach((u) => URL.revokeObjectURL(u)), []);
  return urls;
}

/** One version's content, read when it is shown. */
function Content({ client, version }: { client: ApiClient; version: FileVersion }) {
  const kind = artifactKind(version.contentType, version.name);
  const byUrl = kind === "image" || kind === "video" || kind === "html";
  const previewable = kind !== "other" && version.sizeBytes <= (byUrl ? MEDIA_LIMIT : PREVIEW_LIMIT);
  const [content, setContent] = useState<{ text?: string; url?: string; error?: string } | null>(null);
  useEffect(() => {
    if (!previewable) return;
    let current = true;
    let url: string | undefined;
    void client.artifactContent(version.id, kind === "html").then(
      async (blob) => {
        if (!current) return;
        if (byUrl) {
          url = URL.createObjectURL(blob);
          setContent({ url });
        } else {
          const text = await blob.text();
          if (current) setContent({ text });
        }
      },
      (err: unknown) => current && setContent({ error: errorText(err) }),
    );
    return () => {
      current = false;
      if (url) URL.revokeObjectURL(url);
    };
  }, [client, version.id, kind, byUrl, previewable]);
  return (
    <ArtifactPreview
      data-testid="file-content"
      contentType={version.contentType}
      name={version.name}
      maxHeight={100_000}
      tooLarge={kind !== "other" && !previewable}
      loading={previewable && content === null}
      {...(content?.text !== undefined ? { text: content.text } : {})}
      {...(content?.url ? { url: content.url } : {})}
      {...(content?.error ? { error: content.error } : {})}
    />
  );
}

/**
 * The files agents published for a work item — notes, a design, a report, a
 * screenshot — each opening in place.
 *
 * Their bytes are read only when a row is opened, with the key in a header:
 * text is shown as it is, and an image or a download gets a blob URL the
 * page made, so no link carries a credential.
 */

import { useEffect, useState } from "react";
import { ArtifactGroup, ArtifactPreview, ArtifactRow, artifactKind } from "@dude/design-system/components";
import { IconButton, useToast } from "@dude/design-system/primitives";
import { DEFAULT_RUN_ROLE } from "@dude/domain";
import type { ApiClient, Artifact } from "../api/client.ts";

/** Past this, a text file is downloaded rather than shown: the tab would not survive it. */
const PREVIEW_LIMIT = 2 * 1024 * 1024;

/**
 * Newest version of each name — the API lists newest first — so an older
 * one is history, not another file.
 */
function latest(artifacts: readonly Artifact[]): Artifact[] {
  const byName = new Map<string, Artifact>();
  for (const a of artifacts) if (!byName.has(a.name)) byName.set(a.name, a);
  return [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
}

export function ArtifactsSection({ client, artifacts }: { client: ApiClient; artifacts: readonly Artifact[] }) {
  const shown = latest(artifacts);
  const versions = (name: string) => artifacts.filter((a) => a.name === name).length;
  return (
    <ArtifactGroup
      data-testid="artifacts"
      artifacts={shown}
      renderRow={(a) => (
        <ArtifactItem key={a.id} client={client} artifact={a} updated={versions(a.name) > 1} />
      )}
    />
  );
}

function ArtifactItem({ client, artifact, updated }: { client: ApiClient; artifact: Artifact; updated: boolean }) {
  const [open, setOpen] = useState(false);
  const [content, setContent] = useState<{ text?: string; url?: string; error?: string } | null>(null);
  const { toast } = useToast();
  const kind = artifactKind(artifact.contentType, artifact.name);
  // Nothing to show, or too much: the preview offers the download instead.
  const previewable = kind === "image" || (kind !== "other" && artifact.sizeBytes <= PREVIEW_LIMIT);

  // Read once it is opened; a new version (another id) is read afresh.
  useEffect(() => {
    if (!open || !previewable) return;
    let current = true;
    let url: string | undefined;
    void client.artifactContent(artifact.id).then(
      async (blob) => {
        if (!current) return;
        if (kind === "image") {
          url = URL.createObjectURL(blob);
          setContent({ url });
        } else {
          const text = await blob.text();
          if (current) setContent({ text });
        }
      },
      (err: unknown) => current && setContent({ error: err instanceof Error ? err.message : String(err) }),
    );
    return () => {
      current = false;
      if (url) URL.revokeObjectURL(url);
      setContent(null);
    };
  }, [open, previewable, client, artifact.id, kind]);

  // A button, not a link: the bytes need the key in a header, so there is no
  // URL a new tab could open.
  const download = (
    <IconButton
      icon="download"
      size="sm"
      label={`Download ${artifact.name}`}
      onClick={() => {
        void client.artifactContent(artifact.id).then(
          (blob) => {
            const url = URL.createObjectURL(blob);
            const a = document.createElement("a");
            a.href = url;
            a.download = artifact.name.split("/").pop() ?? artifact.name;
            a.click();
            setTimeout(() => URL.revokeObjectURL(url), 1000);
          },
          (err: unknown) => toast({ title: err instanceof Error ? err.message : String(err), tone: "danger" }),
        );
      }}
    />
  );

  return (
    <ArtifactRow
      data-testid="artifact"
      name={artifact.name}
      contentType={artifact.contentType}
      sizeBytes={artifact.sizeBytes}
      sha256={artifact.sha256}
      producer={{ role: artifact.role ?? DEFAULT_RUN_ROLE, ...(artifact.phase ? { phase: artifact.phase } : {}) }}
      publishedAt={artifact.createdAt}
      change={updated ? "updated" : null}
      download={download}
      expanded={open}
      onExpandedChange={setOpen}
      preview={
        <ArtifactPreview
          contentType={artifact.contentType}
          name={artifact.name}
          tooLarge={kind !== "other" && !previewable}
          loading={previewable && open && content === null}
          download={download}
          {...(content?.text !== undefined ? { text: content.text } : {})}
          {...(content?.url ? { url: content.url } : {})}
          {...(content?.error ? { error: content.error } : {})}
        />
      }
    />
  );
}

/**
 * Start a project: a name, and optionally the repository its work goes to.
 *
 * Deliberately short. A project needs nothing else to exist, and every
 * other setting — more repositories, models, reviewers — has its place in
 * the project's settings, which is where this lands you.
 */

import { useState } from "react";
import { Input } from "@dude/design-system/primitives";
import type { ApiClient } from "../api/client.ts";
import { FormDialog, useSave } from "../hooks/useSave.tsx";

/** A slug from a name: lowercase words joined by dashes. */
export function slugify(name: string): string {
  return name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 100);
}

/** A repository's name from its URL: the last path segment, without .git. */
export function repositoryName(url: string): string {
  const last = url.trim().replace(/\/+$/, "").split(/[/:]/).pop() ?? "";
  return last.replace(/\.git$/, "");
}

export interface NewProjectDialogProps {
  client: ApiClient;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: (projectId: string) => void;
}

/** Mounted by its opener only while open, so each opening starts empty. */
export function NewProjectDialog({ client, open, onOpenChange, onCreated }: NewProjectDialogProps) {
  const [name, setName] = useState("");
  const [slug, setSlug] = useState<string | null>(null);
  const [url, setUrl] = useState("");
  const { busy, problem, save } = useSave();
  const effectiveSlug = slug ?? slugify(name);

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="New project"
      description="Everything else — more repositories, models, reviewers — is in its settings."
      submitLabel="Create project"
      submitTestId="project-create"
      canSubmit={!busy && Boolean(name.trim()) && Boolean(effectiveSlug)}
      problem={problem}
      onSubmit={() => {
        let id = "";
        void save(
          async () => {
            id = (
              await client.createProject({
                name: name.trim(),
                slug: effectiveSlug,
                repositories: url.trim() ? [{ name: repositoryName(url), url: url.trim() }] : [],
              })
            ).id;
          },
          () => {
            onOpenChange(false);
            onCreated(id);
          },
        );
      }}
    >
      <Input label="Name" autoFocus value={name} maxLength={200} data-testid="project-name"
        onChange={(e) => setName(e.target.value)} />
      <Input label="Slug" mono value={effectiveSlug}
        hint="Names it in paths; its first letters start its tasks' keys."
        onChange={(e) => setSlug(e.target.value)} />
      <Input label="Repository" mono value={url} placeholder="https://github.com/acme/api.git"
        hint="Optional: where its work goes. You can add more later." data-testid="project-repository"
        onChange={(e) => setUrl(e.target.value)} />
    </FormDialog>
  );
}

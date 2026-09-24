/**
 * Start a project: a name, and optionally the repository its work goes to.
 *
 * Deliberately short. A project needs nothing else to exist, and every
 * other setting — more repositories, models, reviewers — has its place in
 * the project's settings, which is where this lands you.
 */

import { useEffect, useId, useState } from "react";
import { Button, Dialog, Input } from "@dude/design-system/primitives";
import type { ApiClient } from "../api/client.ts";
import { ApiError } from "../api/client.ts";

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

export function NewProjectDialog({ client, open, onOpenChange, onCreated }: NewProjectDialogProps) {
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const formId = useId();

  useEffect(() => {
    if (!open) return;
    setName("");
    setSlug("");
    setSlugTouched(false);
    setUrl("");
    setProblem(null);
  }, [open]);

  const effectiveSlug = slugTouched ? slug : slugify(name);
  const canCreate = !busy && Boolean(name.trim()) && Boolean(effectiveSlug);

  async function create() {
    if (!canCreate) return;
    setBusy(true);
    setProblem(null);
    try {
      const project = await client.createProject({
        name: name.trim(),
        slug: effectiveSlug,
        repositories: url.trim() ? [{ name: repositoryName(url), url: url.trim() }] : [],
      });
      onOpenChange(false);
      onCreated(project.id);
    } catch (err) {
      setProblem(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      size="sm"
      title="New project"
      description="Everything else — more repositories, models, reviewers — is in its settings."
      footer={
        <>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="submit" form={formId} variant="primary" disabled={!canCreate} data-testid="project-create">
            Create project
          </Button>
        </>
      }
    >
      <form
        id={formId}
        className="dialogForm"
        onSubmit={(e) => {
          e.preventDefault();
          void create();
        }}
      >
        <Input
          label="Name"
          autoFocus
          value={name}
          maxLength={200}
          onChange={(e) => setName(e.target.value)}
          data-testid="project-name"
        />
        <Input
          label="Slug"
          mono
          value={effectiveSlug}
          hint="Names it in paths; its first letters start its work items' keys."
          onChange={(e) => {
            setSlugTouched(true);
            setSlug(e.target.value);
          }}
        />
        <Input
          label="Repository"
          mono
          value={url}
          placeholder="https://github.com/acme/api.git"
          hint="Optional: where its work goes. You can add more later."
          onChange={(e) => setUrl(e.target.value)}
          data-testid="project-repository"
        />
        {problem ? <p className="problem" role="alert">{problem}</p> : null}
      </form>
    </Dialog>
  );
}

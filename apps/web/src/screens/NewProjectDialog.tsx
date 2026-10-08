/**
 * Start a project: a name, its key, and optionally the repository its work
 * goes to.
 *
 * Deliberately short. A project needs nothing else to exist, and every
 * other setting — more repositories, models, reviewers — has its place in
 * the project's settings, which is where this lands you.
 */

import { useEffect, useState } from "react";
import { Input } from "@dude/design-system/primitives";
import { deriveProjectKey, PROJECT_KEY } from "@dude/domain";
import { ApiError, type ApiClient } from "../api/client.ts";
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

const KEY_HINT = "Starts its tasks' keys (BILL-12). Unique in the organisation; fixed once made.";
const KEY_FORMAT = "2 to 6 letters or digits, starting with a letter";

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
  const [key, setKey] = useState<string | null>(null);
  const [url, setUrl] = useState("");
  // The organisation's keys, so the prefill is the key the API would derive.
  const [taken, setTaken] = useState<string[]>([]);
  // The API's refusal of the key, shown under it, and the free key it offered.
  const [refused, setRefused] = useState<{ key: string; message: string; suggestion: string | null } | null>(null);
  const { busy, problem, save } = useSave();
  const effectiveSlug = slug ?? slugify(name);
  const effectiveKey = key ?? (effectiveSlug ? deriveProjectKey(effectiveSlug, taken) ?? "" : "");
  const keyError = refused?.key === effectiveKey ? refused.message
    : effectiveKey && !PROJECT_KEY.test(effectiveKey) ? KEY_FORMAT : undefined;

  useEffect(() => {
    void client.listProjects().then(({ projects }) => setTaken(projects.map((p) => p.key)), () => undefined);
  }, [client]);

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title="New project"
      description="Everything else — more repositories, models, reviewers — is in its settings."
      submitLabel="Create project"
      submitTestId="project-create"
      canSubmit={!busy && Boolean(name.trim()) && Boolean(effectiveSlug) && PROJECT_KEY.test(effectiveKey)}
      problem={problem}
      onSubmit={() => {
        let id = "";
        const sent = effectiveKey;
        void save(
          async () => {
            try {
              id = (
                await client.createProject({
                  name: name.trim(),
                  slug: effectiveSlug,
                  key: sent,
                  repositories: url.trim() ? [{ name: repositoryName(url), url: url.trim() }] : [],
                })
              ).id;
            } catch (err) {
              // The key taken: said under it, not in the footer.
              if (!(err instanceof ApiError) || err.status !== 409 || !err.message.startsWith(`${sent} is already the key of`)) throw err;
              const suggestion = (err.details as { suggestion?: unknown } | undefined)?.suggestion;
              setRefused({ key: sent, message: err.message, suggestion: typeof suggestion === "string" ? suggestion : null });
            }
          },
          () => {
            if (!id) return;
            onOpenChange(false);
            onCreated(id);
          },
        );
      }}
    >
      <Input label="Name" autoFocus value={name} maxLength={200} data-testid="project-name"
        onChange={(e) => setName(e.target.value)} />
      <Input label="Slug" mono value={effectiveSlug}
        hint="Names it in paths."
        onChange={(e) => setSlug(e.target.value)} />
      <Input label="Key" mono value={effectiveKey} maxLength={6} data-testid="project-key"
        hint={KEY_HINT} error={keyError}
        errorAction={refused?.key === effectiveKey && refused.suggestion
          ? { label: `Use ${refused.suggestion}`, onClick: () => setKey(refused.suggestion), "data-testid": "project-key-suggestion" }
          : undefined}
        onChange={(e) => setKey(e.target.value.toUpperCase())} />
      <Input label="Repository" mono value={url} placeholder="https://github.com/acme/api.git"
        hint="Optional: where its work goes. You can add more later." data-testid="project-repository"
        onChange={(e) => setUrl(e.target.value)} />
    </FormDialog>
  );
}

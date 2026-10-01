/**
 * Project settings → Servers: the project's server definitions (a table,
 * with a dialog to add or edit one), and how its branch previews run —
 * the image, the egress allowlist, the idle timeout, who may open one.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { HostChips, ServerRecipeDialog, ServerRecipeTable, SettingRow, SettingSource, SettingsHeader, SettingsMeta, SettingsNote, SettingsSection } from "@dude/design-system/components";
import { formatTimestamp, Icon, PREVIEW_IDLE_TIMEOUT_DEFAULT_MINUTES } from "@dude/design-system";
import { Button, Callout, Dialog, EmptyState, FormActions, Input, RowMenu, Select, Spinner } from "@dude/design-system/primitives";
import { egressProblem, type MachineSize, type PreviewSettings, type Recipe, type RecipeInput } from "@dude/domain";
import type { ApiClient, ProjectDetail } from "../api/client.ts";
import { errorText, useSave } from "../hooks/useSave.tsx";
import { MachineSelect } from "./MachinesSettings.tsx";

const IDLE_TIMEOUTS = [5, 10, 15, 30, 60, 120, 240];

export function ServersSettingsPage({ client, project, canEdit, orgName, sizes, onCount }: {
  client: ApiClient;
  project: ProjectDetail;
  canEdit: boolean;
  orgName: string;
  /** The organisation's sizes, as the settings screen loaded them; null while loading. */
  sizes: readonly MachineSize[] | null;
  onCount?: ((n: number) => void) | undefined;
}) {
  const [recipes, setRecipes] = useState<Recipe[] | null>(null);
  const [previews, setPreviews] = useState<PreviewSettings | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [editing, setEditing] = useState<Recipe | "new" | null>(null);
  const [removing, setRemoving] = useState<Recipe | null>(null);
  const { busy, problem: saveProblem, save, clear } = useSave();
  // The preview settings save apart from the definitions, so a refusal shows by them.
  const previewSave = useSave();
  const [previewRound, setPreviewRound] = useState(0);
  const addButton = useRef<HTMLButtonElement>(null);
  const defaultSize = sizes?.find((s) => s.isDefault) ?? null;

  const latest = useRef(0);
  const load = useCallback(async () => {
    const mine = ++latest.current;
    try {
      const fresh = await client.projectServers(project.id);
      if (mine !== latest.current) return;
      setRecipes(fresh.servers);
      setPreviews(fresh.previews);
      setProblem(null);
      onCount?.(fresh.servers.length);
    } catch (err) {
      if (mine === latest.current) setProblem(errorText(err));
    }
  }, [client, project.id, onCount]);
  useEffect(() => {
    void load();
  }, [load]);

  if (!recipes || !previews) return <div className="centered">{problem ?? <Spinner label="Loading…" />}</div>;

  const repository = project.repositories[0]?.name ?? null;
  const last = [...recipes].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))[0];
  const saveRecipe = (name: string, recipe: RecipeInput) =>
    void save(() => client.putProjectServer(project.id, name, recipe), () => {
      setEditing(null);
      void load();
    }, `Server ${recipe.name} saved`);
  const savePreviews = (patch: Partial<PreviewSettings>, done: string) => {
    // Shown at once; put back from the server if it refuses, and the
    // fields start over from what it says.
    const next = { ...previews, ...patch };
    setPreviews(next);
    void previewSave.save(() => client.updatePreviewSettings(project.id, next), undefined, done)
      .then(() => load())
      .then(() => setPreviewRound((n) => n + 1));
  };

  return (
    <>
      <SettingsHeader title="Servers" description="What a run can serve. Each server is a port lux exposes at its own URL, with the command that starts it. People start them in a task, and a branch preview starts the ones marked to." />
      <SettingsNote icon="globe">
        Every server gets <span className="ds-mono">https://&lt;name&gt;-&lt;run&gt;.&lt;preview domain&gt;</span>, behind your sign-in. Servers stop when a run pauses or moves host; a person starts them again.
      </SettingsNote>
      {problem ? <Callout tone="danger">{problem}</Callout> : null}

      <SettingsSection title="Definitions" data-testid="server-recipes"
        actions={last?.updatedBy ? <SettingsMeta>Last changed by {last.updatedBy.name} · {formatTimestamp(last.updatedAt, "relative")}</SettingsMeta> : undefined}>
        {recipes.length === 0 ? (
          <EmptyState compact icon="globe" title="No servers yet"
            description="A server is a port and the command that starts it. Once defined, anyone can start it in a task, or preview a branch with it."
            action={canEdit ? <Button variant="secondary" leadingIcon="plus" onClick={() => setEditing("new")} data-testid="add-server-recipe">Add server</Button> : undefined} />
        ) : (
          <>
            <ServerRecipeTable recipes={recipes} menu={canEdit ? (r) => (
              <RowMenu size="sm" label={`Actions for ${r.name}`} items={[
                { id: "edit", label: "Edit", icon: "edit", onSelect: () => setEditing(r) },
                { kind: "separator" },
                { id: "remove", label: "Remove", tone: "danger", onSelect: () => setRemoving(r) },
              ]} />
            ) : undefined} />
            {canEdit ? (
              <FormActions>
                <Button ref={addButton} variant="secondary" leadingIcon="plus" onClick={() => setEditing("new")} data-testid="add-server-recipe">Add server</Button>
              </FormActions>
            ) : null}
          </>
        )}
        {saveProblem && !editing && !removing ? <Callout tone="danger">{saveProblem}</Callout> : null}
      </SettingsSection>

      <SettingsSection title="Branch previews" data-testid="preview-settings">
        {recipes.length === 0 ? <Callout tone="neutral">Previewing a branch needs at least one server. Add one above.</Callout> : null}
        {previewSave.problem ? <Callout tone="danger">{previewSave.problem}</Callout> : null}
        <SettingRow label="Machine" help="The size a preview runs on: one run with every server that starts in previews." htmlFor="preview-machine"
          source={previews.machineSize && sizes?.some((s) => s.id === previews.machineSize)
            ? <SettingSource source="project" from={orgName} inherited={defaultSize ? `default size, ${defaultSize.name}` : "default size"}
                onReset={canEdit ? () => savePreviews({ machineSize: null }, "Machine reset") : undefined} />
            : <SettingSource source="organization" from={`${orgName}’s default size`} />}>
          {sizes ? (
            <MachineSelect id="preview-machine" testId="preview-machine" sizes={sizes} value={previews.machineSize} inherited={defaultSize}
              inheritLabel={`${orgName}’s default`} inheritDescription="Follows whichever size is the default"
              footer={`Sizes are ${orgName}’s.`} disabled={!canEdit || previewSave.busy}
              onChange={(machineSize) => savePreviews({ machineSize }, machineSize ? "Machine saved" : "Machine reset")} />
          ) : null}
        </SettingRow>
        <SettingsNote icon="info">
          A preview is one run with every server that starts in previews. Servers someone starts from a session run inside that agent’s machine instead, sharing its CPUs and memory.
        </SettingsNote>
        <SettingRow label="Image" help="The container a preview run starts in. The project’s runner image unless changed here."
          source={previews.image === null ? <SettingSource source="organization" from="the project’s runner" /> : <SettingSource source="project" from="the project’s runner" onReset={canEdit ? () => savePreviews({ image: null }, "Image reset") : undefined} />}>
          <ImageField key={previewRound} value={previews.image} fallback={project.runtimeImage} disabled={!canEdit || previewSave.busy} onSave={(image) => savePreviews({ image }, "Image saved")} />
        </SettingRow>
        <SettingRow label="Egress allowlist" help="Hosts a preview run may reach, beyond the repository. Everything else is refused; * allows anywhere.">
          <HostChips hosts={previews.egress} validate={egressProblem} disabled={!canEdit || previewSave.busy} onChange={(egress) => savePreviews({ egress }, "Allowlist saved")} data-testid="preview-egress" />
        </SettingRow>
        <SettingRow label="Idle timeout" help="With no request for this long, the preview run is parked. Starting a server wakes it."
          source={previews.idleTimeoutMinutes !== PREVIEW_IDLE_TIMEOUT_DEFAULT_MINUTES
            ? <SettingSource source="project" from={orgName} inherited={`${PREVIEW_IDLE_TIMEOUT_DEFAULT_MINUTES} minutes`} onReset={canEdit ? () => savePreviews({ idleTimeoutMinutes: PREVIEW_IDLE_TIMEOUT_DEFAULT_MINUTES }, "Idle timeout reset") : undefined} />
            : undefined}>
          <Select aria-label="Idle timeout" value={String(previews.idleTimeoutMinutes)} disabled={!canEdit || previewSave.busy}
            onValueChange={(v) => savePreviews({ idleTimeoutMinutes: Number(v) }, "Idle timeout saved")}
            options={[...new Set([...IDLE_TIMEOUTS, previews.idleTimeoutMinutes])].sort((a, b) => a - b).map((m) => ({ value: String(m), label: m < 60 ? `${m} minutes` : m === 60 ? "1 hour" : `${m / 60} hours` }))} />
        </SettingRow>
        <SettingRow label="Access" help="Who can open a preview.">
          <span className="previewAccess"><Icon name="human" size={14} />Visitors sign in the way they sign in to lux. Nothing is public.</span>
        </SettingRow>
      </SettingsSection>

      {editing ? (
        <ServerRecipeDialog
          open
          onOpenChange={(open) => {
            if (!open) {
              setEditing(null);
              clear();
            }
          }}
          existing={editing === "new" ? null : editing}
          repository={repository}
          busy={busy}
          problem={saveProblem}
          onSubmit={(recipe) => saveRecipe(editing === "new" ? recipe.name : editing.name, recipe)}
        />
      ) : null}
      <Dialog
        open={removing !== null}
        onOpenChange={(open) => !open && setRemoving(null)}
        tone="danger"
        size="sm"
        title={`Remove ${removing?.name ?? ""}?`}
        description="Runs that already have it keep it; new previews will not start it."
        footer={
          <>
            <Button variant="quiet" onClick={() => setRemoving(null)}>Cancel</Button>
            <Button variant="danger" solid disabled={busy} data-testid="remove-server-recipe" onClick={() => {
              const r = removing!;
              setRemoving(null);
              void save(() => client.removeProjectServer(project.id, r.name), () => {
                void load();
                addButton.current?.focus();
              }, `${r.name} removed`);
            }}>
              Remove
            </Button>
          </>
        }
      />
    </>
  );
}

/** The preview image: typed in place, saved on blur or Enter; empty means the runner's. Keyed by its parent per save, so a refused value goes. */
function ImageField({ value, fallback, disabled, onSave }: { value: string | null; fallback: string | null; disabled: boolean; onSave: (image: string | null) => void }) {
  const [draft, setDraft] = useState(value ?? "");
  const commit = () => {
    const next = draft.trim() || null;
    if (next !== value) onSave(next);
  };
  return (
    <Input aria-label="Image" mono value={draft} disabled={disabled} placeholder={fallback ?? "The runner’s image"} style={{ minWidth: 340 }} data-testid="preview-image"
      onChange={(e) => setDraft(e.target.value)} onBlur={commit} onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()} />
  );
}

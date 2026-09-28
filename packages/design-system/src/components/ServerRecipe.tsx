import { useId, useState, type HTMLAttributes, type ReactNode } from "react";
import { serverNameProblem, serverPortProblem, type ServerEnvVar, type ServerRecipe, type ServerRecipeInput } from "@dude/domain";
import { Icon } from "../icons/index.tsx";
import { Button, IconButton } from "../primitives/Button.tsx";
import { Checkbox } from "../primitives/Checkbox.tsx";
import { Dialog } from "../primitives/Dialog.tsx";
import { Input } from "../primitives/Input.tsx";
import { Callout, FormActions, FormRow, FormStack } from "../primitives/Layout.tsx";
import { Table, TBody, Td, Th, THead, Tr } from "../primitives/Table.tsx";
import { AutostartMark } from "./ServerRow.tsx";
import styles from "./ServerRecipe.module.css";

export interface ServerRecipeTableProps extends HTMLAttributes<HTMLTableElement> {
  readonly recipes: ReadonlyArray<ServerRecipe>;
  /** The row's overflow menu (the app's RowMenu), when the person may change it. */
  readonly menu?: ((recipe: ServerRecipe) => ReactNode) | undefined;
}

/** A project's server definitions: name, port, command, directory, whether a preview starts it. */
export function ServerRecipeTable({ recipes, menu, ...rest }: ServerRecipeTableProps) {
  return (
    <Table density="compact" aria-label="Servers" {...rest}>
      <THead>
        <Tr>
          <Th width="14%">Name</Th>
          <Th width="8%">Port</Th>
          <Th>Command</Th>
          <Th width="14%">Directory</Th>
          <Th width="16%">Branch previews</Th>
          {menu ? (
            <Th align="right" width="48px">
              <span className="ds-sr-only">Actions</span>
            </Th>
          ) : null}
        </Tr>
      </THead>
      <TBody>
        {recipes.map((r) => (
          <Tr key={r.name} data-testid="server-recipe" data-server={r.name}>
            <Td mono>{r.name}</Td>
            <Td mono>{r.port}</Td>
            <Td mono muted title={r.command}>{r.command}</Td>
            <Td mono>{r.workdir}</Td>
            <Td><AutostartMark autostart={r.autostartInPreviews} /></Td>
            {menu ? <Td align="right">{menu(r)}</Td> : null}
          </Tr>
        ))}
      </TBody>
    </Table>
  );
}

export interface ServerUrlPreviewProps {
  /** The name typed so far; "…" until it is one. */
  readonly name: string;
  /** The preview domain, when previews are configured. */
  readonly domain?: string | null | undefined;
}

/** What URL the name becomes: `https://<name>-<run>.<domain>`. */
export function ServerUrlPreview({ name, domain }: ServerUrlPreviewProps) {
  const ok = serverNameProblem(name) === null;
  return (
    <span className={styles["urlPreview"]} data-testid="server-url-preview">
      <Icon name="globe" size={12} />
      <span>
        https://<b>{ok ? name : "…"}</b>-<i>&lt;run&gt;</i>.{domain ?? "<preview domain>"}
      </span>
    </span>
  );
}

export interface EnvVarRowsProps {
  readonly vars: ReadonlyArray<ServerEnvVar>;
  readonly onChange: (vars: ServerEnvVar[]) => void;
  readonly disabled?: boolean | undefined;
}

/** Environment variables as rows of name and value, with add and remove. */
export function EnvVarRows({ vars, onChange, disabled }: EnvVarRowsProps) {
  const rows = vars.length === 0 ? [{ name: "", value: "" }] : vars;
  const set = (i: number, patch: Partial<ServerEnvVar>) => onChange(rows.map((v, j) => (j === i ? { ...v, ...patch } : v)));
  return (
    <div className={styles["field"]}>
      <span className={styles["fieldLabel"]}>Environment variables</span>
      <div className={styles["envs"]}>
        {rows.map((v, i) => (
          <div key={i} className={styles["env"]}>
            <Input aria-label="Variable name" mono size="sm" value={v.name} placeholder="NAME" disabled={disabled} onChange={(e) => set(i, { name: e.target.value })} />
            <Input aria-label="Value" mono size="sm" value={v.value} placeholder="value" disabled={disabled} onChange={(e) => set(i, { value: e.target.value })} />
            <IconButton size="sm" icon="close" label="Remove variable" disabled={disabled} onClick={() => onChange(rows.filter((_, j) => j !== i))} />
          </div>
        ))}
      </div>
      <FormActions className={styles["envActions"]} note="Not for secrets: values are visible to anyone who can read the project.">
        <Button size="sm" variant="quiet" leadingIcon="plus" disabled={disabled} onClick={() => onChange([...rows, { name: "", value: "" }])}>
          Add variable
        </Button>
      </FormActions>
    </div>
  );
}

/** A recipe as the form holds it: the port as typed, so a blank stays blank. */
export interface ServerRecipeDraft {
  name: string;
  port: string;
  command: string;
  workdir: string;
  setup: string;
  env: ServerEnvVar[];
  autostartInPreviews: boolean;
}

export function draftOf(recipe: ServerRecipe | null): ServerRecipeDraft {
  return recipe
    ? { name: recipe.name, port: String(recipe.port), command: recipe.command, workdir: recipe.workdir, setup: recipe.setup ?? "", env: [...recipe.env], autostartInPreviews: recipe.autostartInPreviews }
    : { name: "", port: "", command: "", workdir: "", setup: "", env: [], autostartInPreviews: true };
}

/** The draft's problems, by field; empty when it can be saved. */
export function draftProblems(d: ServerRecipeDraft, touched: boolean): { name?: string; port?: string; command?: string } {
  const out: { name?: string; port?: string; command?: string } = {};
  const name = serverNameProblem(d.name);
  if (name && (touched || d.name)) out.name = name;
  const port = serverPortProblem(Number(d.port));
  if (port && (touched || d.port)) out.port = port;
  if (touched && !d.command.trim()) out.command = "What starts the server.";
  return out;
}

export function recipeOf(d: ServerRecipeDraft): ServerRecipeInput {
  return {
    name: d.name.trim(),
    port: Number(d.port),
    command: d.command.trim(),
    workdir: d.workdir.trim().replace(/^\/+|\/+$/g, ""),
    setup: d.setup.trim() || null,
    env: d.env.filter((v) => v.name.trim()).map((v) => ({ name: v.name.trim(), value: v.value })),
    autostartInPreviews: d.autostartInPreviews,
  };
}

export interface ServerRecipeDialogProps {
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
  /** The recipe being edited; null for a new one. */
  readonly existing: ServerRecipe | null;
  /** The repository the working directory is under: "web-console/". */
  readonly repository?: string | null | undefined;
  readonly domain?: string | null | undefined;
  readonly busy?: boolean | undefined;
  readonly problem?: string | null | undefined;
  readonly onSubmit: (recipe: ServerRecipeInput) => void;
  /** For the gallery: start with these values rather than the recipe's. */
  readonly initial?: Partial<ServerRecipeDraft> | undefined;
}

/**
 * Add or edit a server definition: name and port with the URL they make,
 * the command, the working directory under the repository, a setup
 * command, environment variables, and whether a branch preview starts it.
 * Validation says what to change; Add waits until nothing is wrong.
 */
export function ServerRecipeDialog({ open, onOpenChange, existing, repository, domain, busy, problem, onSubmit, initial }: ServerRecipeDialogProps) {
  const formId = useId();
  const [draft, setDraft] = useState<ServerRecipeDraft>(() => ({ ...draftOf(existing), ...initial }));
  const [touched, setTouched] = useState(Boolean(initial));
  const problems = draftProblems(draft, touched);
  const complete = draft.name.trim() !== "" && draft.port !== "" && draft.command.trim() !== "";
  const canSubmit = !busy && complete && Object.keys(draftProblems(draft, true)).length === 0;
  const set = <K extends keyof ServerRecipeDraft>(key: K, value: ServerRecipeDraft[K]) => setDraft((d) => ({ ...d, [key]: value }));
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      size="lg"
      title={existing ? `Edit ${existing.name}` : "Add a server"}
      description={existing ? `Changes apply to the next start; a running ${existing.name} keeps its command.` : "A port and the command that serves it, run in the checkout."}
      footer={
        <>
          <Button variant="quiet" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button type="submit" form={formId} variant="primary" disabled={!canSubmit} data-testid="server-recipe-save">
            {existing ? "Save" : "Add"}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={(e) => {
          e.preventDefault();
          setTouched(true);
          if (canSubmit) onSubmit(recipeOf(draft));
        }}
      >
        <FormStack className={styles["form"]}>
          <FormRow className={styles["namePort"]}>
            <Input label="Name" mono autoFocus={!existing} value={draft.name} error={problems.name} data-testid="server-recipe-name"
              hint="Lowercase letters, digits and dashes: the first label of the URL." onChange={(e) => set("name", e.target.value)} />
            <Input label="Port" mono type="number" inputMode="numeric" value={draft.port} error={problems.port} className={styles["port"]} data-testid="server-recipe-port"
              hint="1024–65535. Health is a TCP check on it." onChange={(e) => set("port", e.target.value)} />
          </FormRow>
          <ServerUrlPreview name={draft.name.trim()} domain={domain} />
          <Input label="Command" mono value={draft.command} error={problems.command} placeholder="npm run dev -- --host 0.0.0.0 --port 3000" data-testid="server-recipe-command"
            hint="Started in the working directory. Bind 0.0.0.0, not localhost: the URL reaches it from outside the container." onChange={(e) => set("command", e.target.value)} />
          <FormRow>
            <Input label="Working directory" mono value={draft.workdir} placeholder="apps/web" leading={repository ? `${repository}/` : undefined}
              hint="Relative to the repository." onChange={(e) => set("workdir", e.target.value)} />
            <Input label="Setup command" mono value={draft.setup} placeholder="npm ci"
              hint="Optional. Run once before the command, in the same directory." onChange={(e) => set("setup", e.target.value)} />
          </FormRow>
          <EnvVarRows vars={draft.env} onChange={(env) => set("env", env)} />
          <Checkbox
            checked={draft.autostartInPreviews}
            onCheckedChange={(c) => set("autostartInPreviews", c === true)}
            label="Start automatically in branch previews"
            description="Otherwise it waits in the preview run, stopped, until someone starts it."
          />
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
        </FormStack>
      </form>
    </Dialog>
  );
}

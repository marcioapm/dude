/**
 * Adding a server to a run: one of the project's recipes, or a port and a
 * command kept only on this run — the one-off `python -m http.server`.
 * Recipes already on the run are shown, ticked and disabled, so the list
 * says what is there without letting it be added twice.
 */

import { useState } from "react";
import { Segmented } from "@dude/design-system/components";
import { Badge, Checkbox, Input } from "@dude/design-system/primitives";
import { serverNameProblem, serverPortProblem, type RunServerInput, type ServerRecipe } from "@dude/domain";
import { FormDialog } from "../hooks/useSave.tsx";

export interface AddServerDialogProps {
  recipes: readonly ServerRecipe[];
  /** Names already on the run. */
  present: ReadonlySet<string>;
  busy: boolean;
  problem: string | null;
  onClose: () => void;
  /** One call per server added; resolves whether it landed. */
  onAdd: (input: RunServerInput) => Promise<boolean>;
}

export function AddServerDialog({ recipes, present, busy, problem, onClose, onAdd }: AddServerDialogProps) {
  const [kind, setKind] = useState<"recipe" | "adhoc">(recipes.length > 0 ? "recipe" : "adhoc");
  const [chosen, setChosen] = useState<Set<string>>(() => new Set(recipes.filter((r) => !present.has(r.name)).map((r) => r.name)));
  const [name, setName] = useState("");
  const [port, setPort] = useState("");
  const [command, setCommand] = useState("");
  const [workdir, setWorkdir] = useState("");
  const [touched, setTouched] = useState(false);

  const nameProblem = touched || name ? serverNameProblem(name) : null;
  const portProblem = touched || port ? serverPortProblem(Number(port)) : null;
  const adhocOk = name !== "" && port !== "" && !serverNameProblem(name) && !serverPortProblem(Number(port)) && !present.has(name);
  const canSubmit = !busy && (kind === "recipe" ? chosen.size > 0 : adhocOk);

  const submit = async () => {
    setTouched(true);
    if (kind === "recipe") {
      for (const r of recipes) if (chosen.has(r.name) && !(await onAdd({ recipe: r.name }))) return;
    } else if (!(await onAdd({ name: name.trim(), port: Number(port), ...(command.trim() ? { command: command.trim() } : {}), ...(workdir.trim() ? { workdir: workdir.trim() } : {}) }))) {
      return;
    }
    onClose();
  };

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      size="md"
      title="Add a server to this run"
      description="One of the project’s, or a port and command just for this run."
      submitLabel={command.trim() || kind === "recipe" ? "Add and start" : "Add"}
      submitTestId="add-server-submit"
      canSubmit={canSubmit}
      problem={problem}
      onSubmit={() => void submit()}
    >
      <Segmented label="Kind" value={kind} onChange={setKind}
        options={[{ value: "recipe", label: "From the project" }, { value: "adhoc", label: "Ad hoc" }]} />
      {kind === "recipe" ? (
        <>
          <ul className="recipeChoices" aria-label="The project’s servers">
            {recipes.map((r) => {
              const on = present.has(r.name);
              return (
                <li key={r.name} className="recipeChoice" data-server={r.name} aria-disabled={on || undefined}>
                  <Checkbox
                    checked={on || chosen.has(r.name)}
                    disabled={on}
                    aria-label={r.name}
                    onCheckedChange={(c) => setChosen((s) => {
                      const next = new Set(s);
                      if (c === true) next.add(r.name);
                      else next.delete(r.name);
                      return next;
                    })}
                  />
                  <span className="recipeChoiceText">
                    <span className="recipeChoiceTitle ds-mono">{r.name} <span className="muted">:{r.port}</span></span>
                    <span className="recipeChoiceDetail ds-mono">{r.command}</span>
                  </span>
                  {on ? <Badge size="sm">On this run</Badge> : null}
                </li>
              );
            })}
            {recipes.length === 0 ? <li className="recipeChoiceNone">The project defines no servers yet.</li> : null}
          </ul>
          <p className="formNote">Ad hoc: a name, a port and a command, kept only on this run. Handy for a one-off <span className="ds-mono">python -m http.server 8000</span>.</p>
        </>
      ) : (
        <>
          <Input label="Name" mono autoFocus value={name} error={nameProblem ?? (present.has(name) ? "Already on this run." : undefined)} data-testid="adhoc-name"
            hint="Lowercase letters, digits and dashes: the first label of the URL." onChange={(e) => setName(e.target.value)} />
          <Input label="Port" mono type="number" inputMode="numeric" value={port} error={portProblem ?? undefined} data-testid="adhoc-port"
            hint="1024–65535." onChange={(e) => setPort(e.target.value)} />
          <Input label="Command" mono value={command} placeholder="python -m http.server 8000" data-testid="adhoc-command"
            hint="Optional: without one, the port is only watched — something else starts the server." onChange={(e) => setCommand(e.target.value)} />
          <Input label="Working directory" mono value={workdir} placeholder="apps/web" hint="Relative to the checkout." onChange={(e) => setWorkdir(e.target.value)} />
        </>
      )}
    </FormDialog>
  );
}

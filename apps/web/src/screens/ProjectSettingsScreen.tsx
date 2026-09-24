/**
 * A project's settings: where its work goes, who does it, and how it is
 * delivered. Tabs, because the four are read separately — the person
 * adding a repository is not also retuning reviewers — and each saves on
 * its own.
 */

import { useCallback, useEffect, useId, useRef, useState } from "react";
import { ROLE_LABEL } from "@dude/design-system/components";
import { ALL_AGENT_ROLES as AGENT_ROLES, findingSeveritySchema, REVIEWER_CATEGORIES } from "@dude/domain";
import type { AgentRole, DeliveryPolicy, FullDeliveryPolicy } from "@dude/domain";
import {
  Badge,
  Button,
  Checkbox,
  Dialog,
  EmptyState,
  Input,
  Spinner,
  Tab,
  TabList,
  TabPanel,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
  Tabs,
  useToast,
} from "@dude/design-system/primitives";
import type { ApiClient, ProjectDetail, Repository } from "../api/client.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";

const SEVERITIES = findingSeveritySchema.options;
type FullPolicy = FullDeliveryPolicy;

export interface ProjectSettingsScreenProps {
  client: ApiClient;
  projectId: string;
  onChanged: () => void;
  onBack: () => void;
}

export function ProjectSettingsScreen({ client, projectId, onChanged, onBack }: ProjectSettingsScreenProps) {
  const [project, setProject] = useState<ProjectDetail | null>(null);
  const [problem, setProblem] = useState<string | null>(null);

  const [defaults, setDefaults] = useState<FullPolicy | null>(null);
  // Only the latest load may land: two saves in a row start two.
  const latest = useRef(0);
  const load = useCallback(async () => {
    const mine = ++latest.current;
    try {
      const [fresh, factory] = await Promise.all([client.getProject(projectId), client.deliveryDefaults()]);
      if (mine !== latest.current) return;
      setProject(fresh);
      setDefaults(factory);
      setProblem(null);
    } catch (err) {
      if (mine === latest.current) setProblem(errorText(err));
    }
  }, [client, projectId]);

  useEffect(() => {
    void load();
  }, [load]);

  if (!project || !defaults) return <div className="centered">{problem ?? <Spinner label="Loading…" />}</div>;

  const saved = () => {
    void load();
    onChanged();
  };

  return (
    <div className="settingsScreen" data-testid="project-settings">
      <header className="settingsHeader">
        <h1 className="wiTitle">{project.name}</h1>
        <span className="muted">Settings</span>
        <Button size="sm" variant="ghost" className="settingsBack" onClick={onBack}>
          Back to the board
        </Button>
      </header>
      {problem ? <p className="problem" role="alert">{problem}</p> : null}
      <Tabs defaultValue="repositories">
        <TabList>
          <Tab value="general">General</Tab>
          <Tab value="repositories" count={project.repositories.length}>Repositories</Tab>
          <Tab value="agents">Agents</Tab>
          <Tab value="delivery">Delivery</Tab>
        </TabList>
        <TabPanel value="general">
          <GeneralTab client={client} project={project} onSaved={saved} />
        </TabPanel>
        <TabPanel value="repositories">
          <RepositoriesTab client={client} project={project} onSaved={saved} />
        </TabPanel>
        <TabPanel value="agents">
          <AgentsTab client={client} project={project} onSaved={saved} />
        </TabPanel>
        <TabPanel value="delivery">
          <DeliveryTab client={client} project={project} defaults={defaults} onSaved={saved} />
        </TabPanel>
      </Tabs>
    </div>
  );
}

interface TabProps {
  client: ApiClient;
  project: ProjectDetail;
  onSaved: () => void;
}

function GeneralTab({ client, project, onSaved }: TabProps) {
  const [name, setName] = useState(project.name);
  const [image, setImage] = useState(project.runtimeImage ?? "");
  const { busy, problem, save } = useSave();
  const dirty = name.trim() !== project.name || image.trim() !== (project.runtimeImage ?? "");
  return (
    <form
      className="settingsForm"
      onSubmit={(e) => {
        e.preventDefault();
        void save(() => client.updateProject(project.id, {
          name: name.trim(),
          runtimeImage: image.trim() || null,
        }), onSaved, "General settings saved");
      }}
    >
      <Input label="Name" value={name} required maxLength={200} onChange={(e) => setName(e.target.value)} />
      <Input label="Slug" value={project.slug} mono disabled hint="Fixed: it names the project in paths and keys." />
      <Input
        label="Runtime image"
        mono
        value={image}
        placeholder="The system default"
        hint="The container image agents work in. Empty uses the system default."
        onChange={(e) => setImage(e.target.value)}
      />
      {problem ? <p className="problem" role="alert">{problem}</p> : null}
      <div className="settingsActions">
        <Button type="submit" variant="primary" disabled={!dirty || busy || !name.trim()}>
          Save
        </Button>
      </div>
    </form>
  );
}

function RepositoriesTab({ client, project, onSaved }: TabProps) {
  const [editing, setEditing] = useState<Repository | "new" | null>(null);
  const [removing, setRemoving] = useState<Repository | null>(null);
  const { busy, problem, save } = useSave();
  const addButton = useRef<HTMLButtonElement>(null);
  return (
    <div className="settingsSection">
      {project.repositories.length === 0 ? (
        <EmptyState
          icon="git-branch"
          title="No repository yet"
          description="Work items can't open pull requests until one is added."
        />
      ) : (
        <Table density="compact">
          <THead>
            <Tr>
              <Th width="18%">Name</Th>
              <Th>URL</Th>
              <Th width="12%">Branch</Th>
              <Th width="12%">Trust</Th>
              <Th align="right" width="140px">
                <span className="ds-sr-only">Actions</span>
              </Th>
            </Tr>
          </THead>
          <TBody>
            {project.repositories.map((r) => (
              <Tr key={r.id}>
                <Td mono>{r.name}</Td>
                <Td mono muted title={r.url}>{r.url}</Td>
                <Td mono>{r.defaultBranch}</Td>
                <Td>{r.trust === "untrusted_external" ? "External" : "Internal"}</Td>
                <Td align="right">
                  <div className="rowActions">
                    <Button size="sm" variant="ghost" onClick={() => setEditing(r)} aria-label={`Edit ${r.name}`}>
                      Edit
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setRemoving(r)} aria-label={`Remove ${r.name}`}>
                      Remove
                    </Button>
                  </div>
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}
      <div className="settingsActions">
        <Button ref={addButton} variant="secondary" leadingIcon="plus" onClick={() => setEditing("new")} data-testid="add-repository">
          Add repository
        </Button>
      </div>
      {problem ? <p className="problem" role="alert">{problem}</p> : null}
      {editing ? (
        <RepositoryDialog
          client={client}
          projectId={project.id}
          existing={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={onSaved}
        />
      ) : null}
      <Dialog
        open={removing !== null}
        onOpenChange={(open) => !open && setRemoving(null)}
        tone="danger"
        size="sm"
        title={`Remove ${removing?.name ?? ""}?`}
        description="Work items pointing at it will need another. Refused while work is being delivered to it, or if it has pull requests from past work."
        footer={
          <>
            <Button variant="ghost" onClick={() => setRemoving(null)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={busy}
              onClick={() => {
                const r = removing!;
                setRemoving(null);
                // The row goes; focus goes to what is left to do.
                void save(() => client.removeRepository(r.id), () => {
                  onSaved();
                  addButton.current?.focus();
                }, `${r.name} removed`);
              }}
            >
              Remove
            </Button>
          </>
        }
      />
    </div>
  );
}

/** Mounted only while open, so each opening starts from the repository (or empty). */
function RepositoryDialog(props: {
  client: ApiClient;
  projectId: string;
  existing: Repository | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { existing } = props;
  const [name, setName] = useState(existing?.name ?? "");
  const [url, setUrl] = useState(existing?.url ?? "");
  const [branch, setBranch] = useState(existing?.defaultBranch ?? "main");
  const [external, setExternal] = useState(existing?.trust === "untrusted_external");
  const { busy, problem, save } = useSave();

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && props.onClose()}
      size="md"
      title={existing ? `Edit ${existing.name}` : "Add a repository"}
      submitLabel={existing ? "Save" : "Add"}
      submitTestId="repository-save"
      canSubmit={!busy && Boolean(name.trim()) && Boolean(url.trim())}
      problem={problem}
      onSubmit={() => {
        const fields = {
          name: name.trim(),
          url: url.trim(),
          defaultBranch: branch.trim() || "main",
          trust: (external ? "untrusted_external" : "trusted_internal") as Repository["trust"],
        };
        void save(
          () => (existing ? props.client.updateRepository(existing.id, fields) : props.client.addRepository(props.projectId, fields)),
          () => {
            props.onClose();
            props.onSaved();
          },
          `Repository ${fields.name} saved`,
        );
      }}
    >
      <Input label="Name" mono autoFocus value={name} data-testid="repository-name"
        hint="A directory name: where agents find it in their workspace." onChange={(e) => setName(e.target.value)} />
      <Input label="Clone URL" mono value={url} placeholder="https://github.com/acme/api.git"
        onChange={(e) => setUrl(e.target.value)} data-testid="repository-url" />
      <Input label="Default branch" mono value={branch} onChange={(e) => setBranch(e.target.value)} />
      <Checkbox
        checked={external}
        onCheckedChange={(c) => setExternal(c === true)}
        label="External (untrusted)"
        description="Code from outside the organisation: agents get no credentials and restricted network."
      />
    </FormDialog>
  );
}

function AgentsTab({ client, project, onSaved }: TabProps) {
  const initial = () =>
    Object.fromEntries(AGENT_ROLES.map((role) => [role, project.agentModels[role]?.model ?? ""])) as Record<AgentRole, string>;
  const [models, setModels] = useState(initial);
  const { busy, problem, save } = useSave();
  const dirty = AGENT_ROLES.some((role) => (project.agentModels[role]?.model ?? "") !== models[role]);
  // A role's settings beyond its model (context, limits) need a model to
  // hang on: clearing the model would silently drop them.
  const losing = AGENT_ROLES.filter((role) => {
    const { model: _model, ...rest } = project.agentModels[role] ?? { model: "" };
    return !models[role].trim() && Object.keys(rest).length > 0;
  });
  return (
    <form
      className="settingsForm"
      onSubmit={(e) => {
        e.preventDefault();
        const agentModels = Object.fromEntries(
          AGENT_ROLES.filter((role) => models[role].trim()).map((role) => [
            role,
            { ...project.agentModels[role], model: models[role].trim() },
          ]),
        );
        void save(() => client.updateProject(project.id, { agentModels }), onSaved, "Agents saved");
      }}
    >
      <p className="muted">
        The model each role runs, as <code>provider/model</code>. A role left empty uses the organisation's default.
      </p>
      {AGENT_ROLES.map((role) => (
        <Input
          key={role}
          label={ROLE_LABEL[role]}
          mono
          value={models[role]}
          placeholder="Organisation default"
          error={losing.includes(role) ? "This role has other settings (such as its context) that need a model." : undefined}
          onChange={(e) => setModels((m) => ({ ...m, [role]: e.target.value }))}
        />
      ))}
      {problem ? <p className="problem" role="alert">{problem}</p> : null}
      <div className="settingsActions">
        <Button type="submit" variant="primary" disabled={!dirty || busy || losing.length > 0}>
          Save
        </Button>
      </div>
    </form>
  );
}

function DeliveryTab({ client, project, defaults, onSaved }: TabProps & { defaults: FullPolicy }) {
  const stored = project.deliveryPolicy;
  // Stored fields are present or absent, never undefined; drop any that are.
  const effective = { ...defaults, ...Object.fromEntries(Object.entries(stored).filter(([, v]) => v !== undefined)) } as FullPolicy;
  const [reviewers, setReviewers] = useState<string[]>(effective.requiredReviewers);
  const [blocking, setBlocking] = useState<string[]>(effective.blockingSeverities);
  const [rounds, setRounds] = useState(String(effective.maxReviewIterations));
  const [prRounds, setPrRounds] = useState(String(effective.maxPrFixIterations));
  const [simplify, setSimplify] = useState(effective.simplify);
  const { busy, problem, save } = useSave();
  const reviewersHint = useId();
  const blockingHint = useId();

  const toggle = (list: string[], set: (l: string[]) => void, value: string, on: boolean) =>
    set(on ? [...list, value] : list.filter((v) => v !== value));
  const count = (text: string, min: number) => {
    const n = Number(text);
    return text.trim() !== "" && Number.isInteger(n) && n >= min && n <= 20 ? n : null;
  };
  const roundsValue = count(rounds, 1);
  const prRoundsValue = count(prRounds, 0);

  // What the form says, as a policy; only what differs from the factory's
  // defaults is stored, so the project keeps following them where it has
  // not chosen otherwise.
  const chosen: FullPolicy = {
    requiredReviewers: reviewers as FullPolicy["requiredReviewers"],
    blockingSeverities: blocking as FullPolicy["blockingSeverities"],
    maxReviewIterations: roundsValue ?? effective.maxReviewIterations,
    maxPrFixIterations: prRoundsValue ?? effective.maxPrFixIterations,
    simplify,
  };
  const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
  const differs = (Object.keys(chosen) as Array<keyof FullPolicy>).filter((k) => !same(chosen[k], defaults[k]));
  const toStore = Object.fromEntries(differs.map((k) => [k, chosen[k]])) as DeliveryPolicy;
  const dirty = !same(toStore, stored);
  const valid = reviewers.length > 0 && blocking.length > 0 && roundsValue !== null && prRoundsValue !== null;

  return (
    <form
      className="settingsForm"
      onSubmit={(e) => {
        e.preventDefault();
        void save(() => client.updateProject(project.id, { deliveryPolicy: toStore }), onSaved, "Delivery saved");
      }}
    >
      <fieldset className="choices" aria-describedby={reviewersHint}>
        <legend>Reviewers every delivery runs</legend>
        <p id={reviewersHint} className="muted">Others join when a change touches their area.</p>
        {REVIEWER_CATEGORIES.map((c) => (
          <Checkbox
            key={c}
            label={c}
            checked={reviewers.includes(c)}
            onCheckedChange={(on) => toggle(reviewers, setReviewers, c, on === true)}
          />
        ))}
      </fieldset>
      <fieldset className="choices" aria-describedby={blockingHint}>
        <legend>Findings that send the change back for a fix</legend>
        <p id={blockingHint} className="muted">Lower severities go into the pull request for a person to weigh.</p>
        {SEVERITIES.map((s) => (
          <Checkbox
            key={s}
            label={s}
            checked={blocking.includes(s)}
            onCheckedChange={(on) => toggle(blocking, setBlocking, s, on === true)}
          />
        ))}
      </fieldset>
      <div className="fieldRow">
        <Input label="Review rounds" type="number" min={1} max={20} value={rounds} onChange={(e) => setRounds(e.target.value)}
          hint="Review → fix cycles before a person is asked."
          error={roundsValue === null ? "A whole number from 1 to 20." : undefined} />
        <Input label="Pull request fix rounds" type="number" min={0} max={20} value={prRounds}
          onChange={(e) => setPrRounds(e.target.value)} hint="Fixes for PR comments before a person is asked."
          error={prRoundsValue === null ? "A whole number from 0 to 20." : undefined} />
      </div>
      <Checkbox
        label="Run the simplifier"
        description="A last pass that removes needless complexity without changing behaviour."
        checked={simplify}
        onCheckedChange={(on) => setSimplify(on === true)}
      />
      {reviewers.length === 0 ? <p className="problem">Choose at least one reviewer.</p> : null}
      {blocking.length === 0 ? <p className="problem">Choose at least one severity that blocks.</p> : null}
      {problem ? <p className="problem" role="alert">{problem}</p> : null}
      <div className="settingsActions">
        <Button type="submit" variant="primary" disabled={busy || !valid || !dirty} data-testid="delivery-save">
          Save
        </Button>
        <span className="muted">Settings left at the factory's defaults follow them if they change.</span>
      </div>
    </form>
  );
}

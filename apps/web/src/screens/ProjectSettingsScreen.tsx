/**
 * A project's settings, for its admins (and the organization's): its name
 * and image, its repositories, and — over the organization's defaults — its
 * agents and delivery. Every inherited value says "From <organization>";
 * changing one overrides it here, and Reset puts the organization's back.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { SettingsHeader, SettingsNote, TextButton } from "@dude/design-system/components";
import {
  Button,
  Callout,
  Checkbox,
  Dialog,
  EmptyState,
  FormActions,
  FormStack,
  Input,
  RowMenu,
  Section,
  Spinner,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
} from "@dude/design-system/primitives";
import type { ApiClient, ProjectDetail, Repository } from "../api/client.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";
import { settingsPage } from "../settings.ts";
import { agentsNav, deliveryNav, isRole, SettingsFrame, useSettings } from "./SettingsFrame.tsx";
import { DeliveryPage, RolePage } from "./settingsPages.tsx";

// The first is where the screen opens: a new project needs its repositories first.
const PAGES = ["repositories", "general", "implementer", "reviewer", "fixer", "simplifier", "qa_browser", "delivery"] as const;

export interface ProjectSettingsScreenProps {
  client: ApiClient;
  projectId: string;
  page?: string | undefined;
  onPage: (page: string) => void;
  onChanged: () => void;
  onBack: () => void;
  /** The organization's settings, where what is not changed here comes from. */
  onOrganization: (page: string) => void;
  onOpenRun?: ((runId: string) => void) | undefined;
}

export function ProjectSettingsScreen({ client, projectId, page: given, onPage, onChanged, onBack, onOrganization, onOpenRun }: ProjectSettingsScreenProps) {
  const page = settingsPage(given, PAGES);
  const [project, setProject] = useState<ProjectDetail | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const { scope, problem: settingsProblem } = useSettings(
    client,
    () => client.projectSettings(projectId),
    (p) => client.updateProjectSettings(projectId, p),
  );

  // Only the latest load may land: two saves in a row start two.
  const latest = useRef(0);
  const load = useCallback(async () => {
    const mine = ++latest.current;
    try {
      const fresh = await client.getProject(projectId);
      if (mine !== latest.current) return;
      setProject(fresh);
      setProblem(null);
    } catch (err) {
      if (mine === latest.current) setProblem(errorText(err));
    }
  }, [client, projectId]);

  useEffect(() => {
    void load();
  }, [load]);

  const saved = () => {
    void load();
    onChanged();
  };
  const settings = scope?.settings;
  const orgName = settings?.organization.name ?? "the organisation";

  return (
    <SettingsFrame
      testId="project-settings"
      loading={!project || !scope}
      problem={problem ?? settingsProblem}
      page={page}
      onPage={onPage}
      scope={{
        title: project?.name ?? "",
        subtitle: (
          <TextButton onClick={onBack} data-testid="project-settings-back">
            Project settings
          </TextButton>
        ),
        leading: null,
      }}
      items={
        settings && project
          ? [
              { id: "general", label: "General", icon: "settings" },
              { id: "repositories", label: "Repositories", icon: "git-branch", note: project.repositories.length || undefined },
              agentsNav(settings),
              deliveryNav(settings),
            ]
          : []
      }
      footer={
        <>
          Anything not changed here follows{" "}
          <TextButton onClick={() => onOrganization(isRole(page) || page === "delivery" ? page : "implementer")}>{orgName}’s settings</TextButton>.
        </>
      }
    >
      {project && scope ? (
        <>
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
          {isRole(page) || page === "delivery" ? (
            <SettingsNote icon="layers">
              Values marked “From {orgName}” follow the organisation; change one to override it here.
            </SettingsNote>
          ) : null}
          {page === "general" ? (
            <>
              <SettingsHeader title="General" />
              <GeneralTab client={client} project={project} onSaved={saved} />
            </>
          ) : page === "repositories" ? (
            <>
              <SettingsHeader title="Repositories" description="What agents check out. The first is where tasks start; the others can be requested." />
              <RepositoriesTab client={client} project={project} onSaved={saved} />
            </>
          ) : page === "delivery" ? (
            <DeliveryPage scope={scope} />
          ) : isRole(page) ? (
            <RolePage key={page} scope={scope} role={page} onOpenRun={onOpenRun} />
          ) : null}
        </>
      ) : null}
    </SettingsFrame>
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
      onSubmit={(e) => {
        e.preventDefault();
        void save(() => client.updateProject(project.id, {
          name: name.trim(),
          runtimeImage: image.trim() || null,
        }), onSaved, "General settings saved");
      }}
    >
      <FormStack>
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
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
        <FormActions>
          <Button type="submit" variant="primary" disabled={!dirty || busy || !name.trim()}>
            Save
          </Button>
        </FormActions>
      </FormStack>
    </form>
  );
}

function RepositoriesTab({ client, project, onSaved }: TabProps) {
  const [editing, setEditing] = useState<Repository | "new" | null>(null);
  const [removing, setRemoving] = useState<Repository | null>(null);
  const { busy, problem, save } = useSave();
  const addButton = useRef<HTMLButtonElement>(null);
  // A table uses the width it has; a form keeps to one readable column.
  return (
    <Section>
      {project.repositories.length === 0 ? (
        <EmptyState
          icon="git-branch"
          title="No repository yet"
          description="Tasks can't open pull requests until one is added."
        />
      ) : (
        <Table density="compact">
          <THead>
            <Tr>
              <Th width="18%">Name</Th>
              <Th>URL</Th>
              <Th width="12%">Branch</Th>
              <Th width="12%">Trust</Th>
              <Th align="right" width="48px">
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
                  <RowMenu label={`Actions for ${r.name}`} items={[
                    { id: "edit", label: "Edit", icon: "edit", onSelect: () => setEditing(r) },
                    { kind: "separator" },
                    { id: "remove", label: "Remove", tone: "danger", onSelect: () => setRemoving(r) },
                  ]} />
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}
      <FormActions>
        <Button ref={addButton} variant="secondary" leadingIcon="plus" onClick={() => setEditing("new")} data-testid="add-repository">
          Add repository
        </Button>
      </FormActions>
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
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
        description="Tasks pointing at it will need another. Refused while work is being delivered to it, or if it has pull requests from past work."
        footer={
          <>
            <Button variant="quiet" onClick={() => setRemoving(null)}>
              Cancel
            </Button>
            <Button
              variant="danger" solid
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
    </Section>
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
        description="Code from outside the organization: agents get no credentials and restricted network."
      />
    </FormDialog>
  );
}

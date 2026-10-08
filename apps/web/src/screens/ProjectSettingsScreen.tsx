/**
 * A project's settings, for its admins (and the organization's): its name
 * and image, its repositories, and — over the organization's defaults — its
 * agents and delivery. Every inherited value says "From <organization>";
 * changing one overrides it here, and Reset puts the organization's back.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { ProjectAvatar, SettingRow, SettingsHeader, SettingsNote, SettingsSection, TextButton } from "@dude/design-system/components";
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
import { SETTINGS_ROLES } from "@dude/domain";
import { agentsNav, deliveryNav, isRole, SettingsFrame, useSettings } from "./SettingsFrame.tsx";
import { DeliveryPage, RolePage } from "./settingsPages.tsx";
import { FacePicker } from "./FacePicker.tsx";
import { ServersSettingsPage } from "./ServersSettings.tsx";
import { useMachineSizes } from "./MachinesSettings.tsx";
import { useModelTiers } from "./ModelsSettings.tsx";
import { ImageField, imageWords, useImageChoices, type ImageChoices } from "../images.tsx";
import { isMemoryPage, MEMORY_PAGES, MemoryPages, memoryNav, useIndexSummary, type ProjectChoice } from "./MemorySettings.tsx";

// The first is where the screen opens: a new project needs its repositories first.
const PAGES = ["repositories", "general", "servers", ...SETTINGS_ROLES, "delivery", ...MEMORY_PAGES] as const;

export interface ProjectSettingsScreenProps {
  client: ApiClient;
  projectId: string;
  /** For Memory's "Applies to". */
  projects: readonly ProjectChoice[];
  /** An organisation admin: changes anyone's memory, reindexes. */
  admin: boolean;
  page?: string | undefined;
  onPage: (page: string) => void;
  onChanged: () => void;
  onBack: () => void;
  /** The organization's settings, where what is not changed here comes from. */
  onOrganization: (page: string) => void;
  onOpenRun?: ((runId: string) => void) | undefined;
}

export function ProjectSettingsScreen({ client, projectId, projects, admin, page: given, onPage, onChanged, onBack, onOrganization, onOpenRun }: ProjectSettingsScreenProps) {
  const page = settingsPage(given, PAGES);
  const index = useIndexSummary(client, projectId);
  const [project, setProject] = useState<ProjectDetail | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  // How many servers the project defines, for the menu. The Servers page
  // reads them and says; any other page reads them once for the count.
  const [serverCount, setServerCount] = useState<number | null>(null);
  useEffect(() => {
    if (page === "servers") return;
    void client.projectServers(projectId).then((s) => setServerCount(s.servers.length), () => undefined);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once per project, not per page
  }, [client, projectId]);
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
  // The organisation's sizes, once for the screen: each Machine field's choices.
  const sizes = useMachineSizes(client).sizes?.sizes ?? null;
  // And its tiers: each role's Model field's choices.
  const models = useModelTiers(client);
  const tiers = models.tiers?.tiers ?? null;
  // The organisation's images, once for the screen: every image field's choices.
  const images = useImageChoices(client);

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
              { id: "servers", label: "Servers", icon: "globe", note: serverCount || undefined },
              agentsNav(settings),
              deliveryNav(settings),
              memoryNav(index.status?.failed),
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
              <GeneralTab client={client} project={project} canEdit={scope.settings.canEdit} onSaved={saved} images={images} orgName={orgName}
                onManageImages={admin ? () => onOrganization("images") : undefined} />
            </>
          ) : page === "repositories" ? (
            <>
              <SettingsHeader title="Repositories" description="What agents check out. The first is where tasks start; the others can be requested." />
              <RepositoriesTab client={client} project={project} canEdit={scope.settings.canEdit} onSaved={saved} />
            </>
          ) : page === "servers" ? (
            <ServersSettingsPage client={client} project={project} canEdit={scope.settings.canEdit} orgName={orgName} sizes={sizes} onCount={setServerCount}
              images={images} onManageImages={admin ? () => onOrganization("images") : undefined} />
          ) : page === "delivery" ? (
            <DeliveryPage scope={scope} />
          ) : isRole(page) ? (
            <RolePage key={page} scope={scope} role={page} onOpenRun={onOpenRun} sizes={sizes} tiers={tiers}
              tiersProblem={models.problem} onManageTiers={admin ? () => onOrganization("models") : undefined} images={images}
              onManageImages={admin ? () => onOrganization("images") : undefined} />
          ) : isMemoryPage(page) ? (
            <MemoryPages client={client} page={page} projects={projects} admin={admin} index={index} onPage={onPage}
              scope={{ kind: "project", id: project.id, name: project.name, organization: orgName }} />
          ) : null}
        </>
      ) : null}
    </SettingsFrame>
  );
}

interface TabProps {
  client: ApiClient;
  project: ProjectDetail;
  /** An admin's: others see the project as it is, without the controls. */
  canEdit: boolean;
  onSaved: () => void;
}

function GeneralTab({ client, project, canEdit, onSaved, images, orgName, onManageImages }: TabProps & { images: ImageChoices; orgName: string; onManageImages?: (() => void) | undefined }) {
  const [name, setName] = useState(project.name);
  const { busy, problem, save } = useSave();
  const face = useSave();
  const imageSave = useSave();
  const dirty = name.trim() !== project.name;
  const base = imageWords(images.images, images.defaultImageId);
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void save(() => client.updateProject(project.id, { name: name.trim() }), onSaved, "General settings saved");
      }}
    >
      <fieldset disabled={!canEdit} className="plainFieldset">
      <FormStack>
        <FacePicker
          testId="project-image"
          face={<ProjectAvatar project={project} size={56} data-testid="project-face" />}
          hasImage={Boolean(project.imageUrl)}
          busy={face.busy}
          disabled={!canEdit}
          onPick={(picked) => void face.save(async () => client.setProjectImage(project.id, await picked), onSaved, "Image updated")}
          onRemove={() => void face.save(() => client.setProjectImage(project.id, null), onSaved, "Image removed")}
        />
        {face.problem ? <Callout tone="danger">{face.problem}</Callout> : null}
        <Input label="Name" value={name} required maxLength={200} onChange={(e) => setName(e.target.value)} />
        <Input label="Slug" value={project.slug} mono disabled hint="Fixed: it names the project in paths." />
        <Input label="Key" value={project.key} mono disabled data-testid="project-settings-key"
          hint={`Fixed: its tasks are ${project.key}-1, ${project.key}-2… No other project in the organisation has it.`} />
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
        {canEdit ? (
          <FormActions>
            <Button type="submit" variant="primary" disabled={!dirty || busy || !name.trim()}>
              Save
            </Button>
          </FormActions>
        ) : null}
      </FormStack>
      </fieldset>
      <SettingsSection title="Images">
        <SettingRow label="Runtime image" help={`What this project’s agents work in. Without one, ${orgName}’s default base.`} data-testid="runtime-image-row">
          <ImageField
            testId="runtime-image"
            label="Runtime image"
            images={images.images}
            value={project.runtimeImageId}
            orgName={orgName}
            allowNone={`Use ${orgName}’s default base`}
            noneLabel={base ? `${orgName}’s default base · ${base}` : "dude’s own image"}
            legacy={project.runtimeImage}
            shadowedBy={base}
            disabled={!canEdit || imageSave.busy}
            onManage={onManageImages}
            onChange={(runtimeImageId) => void imageSave.save(() => client.updateProject(project.id, { runtimeImageId }), onSaved, "Runtime image saved")}
            onClearLegacy={() => void imageSave.save(() => client.updateProject(project.id, { runtimeImage: null }), onSaved, "Typed image cleared")}
          />
        </SettingRow>
        {imageSave.problem ? <Callout tone="danger">{imageSave.problem}</Callout> : null}
      </SettingsSection>
    </form>
  );
}

function RepositoriesTab({ client, project, canEdit, onSaved }: TabProps) {
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
                  {canEdit ? <RowMenu label={`Actions for ${r.name}`} items={[
                    { id: "edit", label: "Edit", icon: "edit", onSelect: () => setEditing(r) },
                    { kind: "separator" },
                    { id: "remove", label: "Remove", tone: "danger", onSelect: () => setRemoving(r) },
                  ]} /> : null}
                </Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}
      {canEdit ? <FormActions>
        <Button ref={addButton} variant="secondary" leadingIcon="plus" onClick={() => setEditing("new")} data-testid="add-repository">
          Add repository
        </Button>
      </FormActions> : null}
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

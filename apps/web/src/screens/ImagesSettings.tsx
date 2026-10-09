/**
 * Organisation settings → Images: the organisation's image library.
 *
 * The list (with the builder's queue over it), an image's page — its
 * Containerfile in the editor with the dude layer under it, its history
 * with diffs and Publish again, its builds — and a build's page with its
 * stages and log. Everyone reads; admins add, edit, build and publish.
 * Where an image is chosen (a project, a preview, a role) is the
 * ImagePicker on those pages; this is where images are made.
 *
 * Its sub-page is in the URL after "images/": "<image id>", then
 * "/history" or "/builds", or "builds/<build id>".
 */

import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactElement } from "react";
import {
  Breadcrumb,
  BuildQueueStrip,
  BuildStages,
  CanRunContainersBadge,
  CodeEditor,
  ImageHistory,
  ImageMark,
  ImageState,
  LogStream,
  SettingsHeader,
  SettingsNote,
  type ImageHistoryVersion,
  type LogLine,
} from "@dude/design-system/components";
import { formatDuration, formatTimestamp, Icon } from "@dude/design-system";
import {
  Badge,
  Button,
  Callout,
  Checkbox,
  Dialog,
  Input,
  RowMenu,
  Spinner,
  Tab,
  TabList,
  TabPanel,
  Table,
  Tabs,
  TBody,
  Td,
  Th,
  THead,
  Tr,
} from "@dude/design-system/primitives";
import {
  BUILDER_GIVE_UP_MINUTES,
  builderOffline,
  IMAGE_NAME,
  IMAGE_NAME_MESSAGE,
  imageReferences,
  lacksContainerEngine,
  lintContainerfile,
  shortDigest,
  type ImageBuild,
  type ImageBuilderInfo,
  type ImageBuildWithLog,
  type ImageDetail,
  type ImagesResponse,
  type ImageSummary,
  type ImageVersion,
  type PersistedEvent,
} from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";
import { useReloadOnEvents } from "../hooks/useEventStream.ts";
import { buildStages, builderLimits, containerfileCompletions, draftCounts, imageState, mergeBuildLog, queuePlace, usedByWords } from "../imageWords.ts";

const ago = (iso: string) => formatTimestamp(iso, "relative");

/** Only the library's own events change what these pages show; the rest of the organisation's are skipped. */
const notAnImageEvent = (e: PersistedEvent) => !e.eventType.startsWith("image.");

/** How often an idle page re-reads, to notice the builder going offline (its heartbeat is every 30 s). */
const LIVENESS_EVERY = 60_000;

/**
 * The organisation's images and queue, read again on an image.* event,
 * every few seconds while something builds, and every minute otherwise.
 */
export function useImages(client: ApiClient) {
  const [data, setData] = useState<ImagesResponse | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const load = useCallback(() => {
    client.images().then((d) => {
      setData(d);
      setProblem(null);
    }, (err: unknown) => setProblem(errorText(err)));
  }, [client]);
  useEffect(load, [load]);
  useReloadOnEvents({ client, all: true }, load, 1000, notAnImageEvent);
  const busy = Boolean(data?.queue.length);
  useEffect(() => {
    const t = setInterval(load, busy ? 4000 : LIVENESS_EVERY);
    return () => clearInterval(t);
  }, [busy, load]);
  return { data, problem, load, setData };
}

/** The builder not heard from: since when, and that builds wait for it. */
function BuilderOffline({ builder }: { builder: ImageBuilderInfo }) {
  if (!builder.offline) return null;
  return (
    <Callout tone="attention" data-testid="builder-offline">
      <b>{capitalise(builderOffline(builder.lastSeenAt, (iso) => formatTimestamp(iso, "datetime")))}.</b>{" "}
      Builds wait until it is back; a Run waiting for its image fails after {BUILDER_GIVE_UP_MINUTES} minutes of this.
    </Callout>
  );
}

const capitalise = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** How many builds run or wait, for the menu. */
export const queueCount = (d: ImagesResponse | null) => d?.queue.length ?? 0;

export interface ImagesPageProps {
  client: ApiClient;
  orgName: string;
  images: ReturnType<typeof useImages>;
  sub?: string | undefined;
  onSub: (sub: string | undefined) => void;
}

export function ImagesPage({ client, orgName, images, sub, onSub }: ImagesPageProps) {
  const parts = (sub ?? "").split("/").filter(Boolean);
  if (parts[0] === "builds" && parts[1]) return <BuildPage client={client} buildId={parts[1]} onSub={onSub} />;
  if (parts[0]) {
    return <ImagePage key={parts[0]} client={client} orgName={orgName} id={parts[0]} tab={parts[1] ?? "containerfile"} onSub={onSub}
      library={images.data} onChanged={images.load} />;
  }
  return <ImagesList client={client} orgName={orgName} images={images} onSub={onSub} />;
}

// ---------------------------------------------------------------------------
// The list
// ---------------------------------------------------------------------------

function ImagesList({ client, orgName, images, onSub }: { client: ApiClient; orgName: string; images: ReturnType<typeof useImages>; onSub: (sub: string | undefined) => void }) {
  const [adding, setAdding] = useState(false);
  const { data, problem } = images;
  if (!data) return <div className="centered">{problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />}</div>;
  const running = data.queue.find((b) => b.state === "running");
  const waiting = data.queue.filter((b) => b.state === "queued");
  // Children under their parent, as the mockup's tree has them: an image
  // built FROM another of the library's sits after it, indented.
  const ordered = treeOrder(data.images);
  return (
    <>
      <SettingsHeader
        title="Images"
        description="What agents, servers and previews run in. Edit one here and everything that uses it gets the new version once it builds."
        actions={data.canEdit ? <Button variant="primary" leadingIcon="plus" onClick={() => setAdding(true)} data-testid="new-image">New image</Button> : undefined}
      />
      {!data.canEdit ? <SettingsNote icon="info">Only {orgName}’s admins change images. Everyone can read them and their builds.</SettingsNote> : null}
      <BuilderOffline builder={data.builder} />
      <BuildQueueStrip
        building={running ? { label: `${running.imageName} v${running.version ?? ""}${running.kind === "finish" ? " (dude layer)" : ""}`, elapsed: running.startedAt ? formatDuration(Date.now() - Date.parse(running.startedAt)) : undefined } : null}
        waiting={waiting.map((b) => `${b.imageName} v${b.version ?? ""}`)}
        onOpen={running ? () => onSub(`builds/${running.id}`) : undefined}
        limits={builderLimits(data.builder)}
        unavailable={data.builder.available ? undefined : "Builds are off: this dude has no dude layer configured (DUDE_LAYER_IMAGE), so images can be edited but not built or run."}
      />
      {data.images.length === 0 ? (
        <Callout tone="neutral">No images yet. An image is a Containerfile dude builds and adds its own tools to; projects, previews and agent roles then pick it.</Callout>
      ) : (
        <Table density="default" data-testid="images-table">
          <THead>
            <Tr>
              <Th>Image</Th>
              <Th hideWhenNarrow>Published</Th>
              <Th hideWhenNarrow>Used by</Th>
              <Th>Status</Th>
              <Th hideWhenNarrow>Changed</Th>
            </Tr>
          </THead>
          <TBody>
            {ordered.map(({ image, depth }) => {
              const state = imageState(image, data.queue, ago);
              return (
                <Tr key={image.id} interactive onClick={() => onSub(image.id)} data-testid="image-row" data-image={image.name}>
                  <Td wrap>
                    <span className="imageRow" style={{ paddingLeft: depth ? 20 : 0 }}>
                      <ImageMark isDefault={image.isDefault} />
                      <span className="imageRowText">
                        <span className="imageRowName">
                          <span className="ds-mono">{image.name}</span>
                          {image.isDefault ? <Badge size="sm" tone="info" emphasis="subtle">Default base</Badge> : null}
                          {image.published?.canRunContainers ? <CanRunContainersBadge /> : null}
                          {image.archivedAt ? <Badge size="sm" icon="archive">Archived</Badge> : null}
                        </span>
                        {image.description ? <span className="imageRowDesc">{image.description}</span> : null}
                        {image.from ? <span className="imageRowFrom">FROM {image.from}</span> : null}
                      </span>
                    </span>
                  </Td>
                  <Td mono fit hideWhenNarrow>
                    {image.published ? `v${image.published.number}` : "—"}
                    {image.pending ? <div className="imageRowSub">v{image.pending.number} {image.pending.state === "queued" ? "waiting" : image.pending.state === "failed" ? "failed" : "building"}</div> : null}
                  </Td>
                  <Td hideWhenNarrow wrap muted>{usedByWords(image.usedBy, orgName)}</Td>
                  <Td wrap><ImageState kind={state.kind}>{state.words}</ImageState></Td>
                  <Td hideWhenNarrow wrap muted>{image.lastChange.source === "base_rebuild" ? "dude" : (image.lastChange.by?.name ?? "—")} · {ago(image.lastChange.at)}</Td>
                </Tr>
              );
            })}
          </TBody>
        </Table>
      )}
      {adding ? <NewImageDialog client={client} onClose={() => setAdding(false)} onMade={(id) => {
        setAdding(false);
        images.load();
        onSub(id);
      }} /> : null}
    </>
  );
}

/** Each image, then the images built FROM it, one level in. */
function treeOrder(images: readonly ImageSummary[]): Array<{ image: ImageSummary; depth: number }> {
  const ids = new Set(images.map((i) => i.id));
  const childOf = new Map<string, ImageSummary[]>();
  const roots: ImageSummary[] = [];
  for (const i of images) {
    const parent = i.parents.find((p) => ids.has(p.id) && p.id !== i.id);
    if (parent) childOf.set(parent.id, [...(childOf.get(parent.id) ?? []), i]);
    else roots.push(i);
  }
  const out: Array<{ image: ImageSummary; depth: number }> = [];
  const seen = new Set<string>();
  const walk = (i: ImageSummary, depth: number) => {
    if (seen.has(i.id)) return;
    seen.add(i.id);
    out.push({ image: i, depth });
    for (const c of childOf.get(i.id) ?? []) walk(c, Math.min(depth + 1, 1));
  };
  for (const r of roots.sort((a, b) => Number(b.isDefault) - Number(a.isDefault))) walk(r, 0);
  for (const i of images) walk(i, 0);
  return out;
}

interface NewImageDialogProps {
  client: ApiClient;
  onClose: () => void;
  onMade: (id: string) => void;
}

/** A new image: its name (fixed once made), what it is, and the base it starts FROM. */
function NewImageDialog({ client, onClose, onMade }: NewImageDialogProps): ReactElement {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [base, setBase] = useState("");
  const { busy, problem, save } = useSave();
  const nameProblem = name && !IMAGE_NAME.test(name) ? IMAGE_NAME_MESSAGE : undefined;
  return (
    <FormDialog open onOpenChange={(o) => !o && onClose()} title="New image" size="md"
      description="It starts as a draft: a one-line Containerfile you can edit, then build and publish."
      submitLabel="Create" submitTestId="new-image-create" canSubmit={!busy && Boolean(name) && !nameProblem} problem={problem}
      onSubmit={() => {
        let made = "";
        void save(async () => {
          const out = await client.createImage({
            name, description: description.trim(),
            containerfile: `FROM ${base.trim() || "debian:bookworm-slim"}\n`, note: "First version",
          });
          made = out.image.id;
        }, () => onMade(made), `${name} created`);
      }}>
      <Input label="Name" mono autoFocus value={name} onChange={(e) => setName(e.target.value.toLowerCase())} error={nameProblem}
        hint="What FROM image:<name> calls it. Fixed once made." data-testid="new-image-name" />
      <Input label="Description" value={description} onChange={(e) => setDescription(e.target.value)} maxLength={500} />
      <Input label="FROM" mono value={base} placeholder="debian:bookworm-slim" onChange={(e) => setBase(e.target.value)}
        hint="A registry image, or image:<name> for another of the library's." data-testid="new-image-from" />
    </FormDialog>
  );
}

// ---------------------------------------------------------------------------
// An image
// ---------------------------------------------------------------------------

function useImage(client: ApiClient, id: string) {
  const [detail, setDetail] = useState<ImageDetail | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const load = useCallback(() => {
    client.image(id).then((d) => {
      setDetail(d);
      setProblem(null);
    }, (err: unknown) => setProblem(errorText(err)));
  }, [client, id]);
  useEffect(load, [load]);
  useReloadOnEvents({ client, all: true }, load, 1000, notAnImageEvent);
  const live = detail?.builds.some((b) => b.state === "queued" || b.state === "running");
  useEffect(() => {
    if (!live) return;
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [live, load]);
  return { detail, problem, load, setDetail };
}

function ImagePage({ client, orgName, id, tab, onSub, library, onChanged }: {
  client: ApiClient;
  orgName: string;
  id: string;
  tab: string;
  onSub: (sub: string | undefined) => void;
  library: ImagesResponse | null;
  onChanged: () => void;
}) {
  const { detail, problem, load, setDetail } = useImage(client, id);
  const [describing, setDescribing] = useState(false);
  const action = useSave();
  if (!detail) return <div className="centered">{problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />}</div>;
  const { image, versions, builds } = detail;
  const state = imageState(image, library?.queue ?? builds.filter((b) => b.state === "queued" || b.state === "running"), ago);
  const changed = (d: ImageDetail) => {
    setDetail(d);
    onChanged();
  };
  const crumbs = [
    { id: "images", label: "Images", onSelect: () => onSub(undefined) },
    { id: image.id, label: image.name, mono: true, onSelect: () => onSub(image.id) },
    ...(tab === "history" ? [{ id: "history", label: "History" }] : tab === "builds" ? [{ id: "builds", label: "Builds" }] : []),
  ];
  return (
    <>
      <Breadcrumb items={crumbs} size="sm" />
      <SettingsHeader
        leading={<ImageMark isDefault={image.isDefault} size={44} />}
        title={<span className="ds-mono" data-testid="image-name">{image.name}</span>}
        description={image.description || "No description."}
        actions={
          <span className="imageHeadActions">
            <ImageState kind={state.kind}>{image.published ? `v${image.published.number} published` : state.words}</ImageState>
            {image.isDefault ? <Badge tone="info" emphasis="subtle">Default base</Badge> : null}
            {image.published?.canRunContainers ? <CanRunContainersBadge size="md" /> : null}
            {detail.canEdit ? (
              <RowMenu label={`Actions for ${image.name}`} items={[
                { id: "describe", label: "Edit description", icon: "edit", onSelect: () => setDescribing(true) },
                image.isDefault
                  ? { id: "undefault", label: "Stop being the default base", onSelect: () => void action.save(() => client.setDefaultImage(null), () => { load(); onChanged(); }, "No default base") }
                  : { id: "default", label: "Make it the default base", disabled: !image.published, disabledReason: "Publish a version first", onSelect: () => void action.save(() => client.setDefaultImage(image.id), () => { load(); onChanged(); }, `${image.name} is the default base`) },
                { kind: "separator" },
                image.archivedAt
                  ? { id: "unarchive", label: "Unarchive", onSelect: () => void action.save(() => client.updateImage(image.id, { archived: false }).then(changed), undefined, "Unarchived") }
                  : { id: "archive", label: "Archive", tone: "danger", onSelect: () => void action.save(() => client.updateImage(image.id, { archived: true }).then(changed), undefined, `${image.name} archived`) },
              ]} />
            ) : null}
          </span>
        }
      />
      {image.archivedAt ? <Callout tone="attention">Archived: pickers no longer offer it. {image.usedBy.length ? "What still names it keeps working." : ""}</Callout> : null}
      {action.problem ? <Callout tone="danger">{action.problem}</Callout> : null}
      <BuilderOffline builder={detail.builder} />
      <Tabs value={tab} onValueChange={(t) => onSub(t === "containerfile" ? image.id : `${image.id}/${t}`)}>
        <TabList aria-label="Image">
          <Tab value="containerfile">Containerfile</Tab>
          <Tab value="history" count={versions.length}>History</Tab>
          <Tab value="builds" count={builds.length}>Builds</Tab>
        </TabList>
        <TabPanel value="containerfile">
          <ContainerfileTab key={versions[0]?.id ?? "none"} client={client} detail={detail} orgName={orgName} library={library}
            onChanged={changed} onOpenBuild={(b) => onSub(`builds/${b}`)} />
        </TabPanel>
        <TabPanel value="history">
          <HistoryTab client={client} detail={detail} onChanged={changed} onOpenBuild={(b) => onSub(`builds/${b}`)} />
        </TabPanel>
        <TabPanel value="builds">
          <BuildsTable builds={builds} onOpen={(b) => onSub(`builds/${b}`)} />
        </TabPanel>
      </Tabs>
      {describing ? (
        <DescribeDialog image={image} onClose={() => setDescribing(false)}
          onSave={(description) => action.save(() => client.updateImage(image.id, { description }).then(changed), () => setDescribing(false), "Description saved")} />
      ) : null}
    </>
  );
}

function DescribeDialog({ image, onClose, onSave }: { image: ImageSummary; onClose: () => void; onSave: (d: string) => Promise<boolean> }) {
  const [text, setText] = useState(image.description);
  return (
    <FormDialog open onOpenChange={(o) => !o && onClose()} title={`Describe ${image.name}`} submitLabel="Save" canSubmit problem={null}
      onSubmit={() => void onSave(text.trim())}>
      <Input label="Description" autoFocus value={text} maxLength={500} onChange={(e) => setText(e.target.value)} />
    </FormDialog>
  );
}

/** The latest version that is not the draft: what the draft is "from". */
const latestNumbered = (versions: readonly ImageVersion[]) => versions.find((v) => v.number !== null && v.state !== "cancelled") ?? null;

function ContainerfileTab({ client, detail, orgName, library, onChanged, onOpenBuild }: {
  client: ApiClient;
  detail: ImageDetail;
  orgName: string;
  library: ImagesResponse | null;
  onChanged: (d: ImageDetail) => void;
  onOpenBuild: (buildId: string) => void;
}) {
  const { image, versions, builds, builder, canEdit } = detail;
  const draft = versions.find((v) => v.state === "draft") ?? null;
  const base = latestNumbered(versions);
  const published = versions.find((v) => v.id === image.published?.versionId) ?? null;
  const [text, setText] = useState(draft?.containerfile ?? base?.containerfile ?? "FROM debian:bookworm-slim\n");
  const [note, setNote] = useState(draft?.note ?? "");
  // Saved with the draft, like its Containerfile; a new draft starts from the published version's.
  const savedContainers = draft?.canRunContainers ?? published?.canRunContainers ?? base?.canRunContainers ?? false;
  const [containers, setContainers] = useState(savedContainers);
  const fieldId = useId();
  const [refused, setRefused] = useState<string | null>(null);
  const { busy, problem, save } = useSave();
  const names = useMemo(() => (library?.images ?? []).map((i) => i.name), [library]);
  // Until the list has loaded, an image: name is not called unknown.
  const lintNames = useMemo(() => (library ? names : [...names, ...imageReferences(text)]), [library, names, text]);
  const diagnostics = useMemo(() => lintContainerfile(text, { images: lintNames, self: image.name }), [text, lintNames, image.name]);
  const errors = diagnostics.filter((d) => d.severity === "error").length;
  const dirty = text !== (draft?.containerfile ?? base?.containerfile ?? "") || note !== (draft?.note ?? "") || containers !== savedContainers;
  // The draft against the version it is from: "Can run containers turned on".
  const flipped = (draft || dirty) && containers !== (base?.canRunContainers ?? false) ? (containers ? "on" : "off") : null;
  const noEngine = containers && lacksContainerEngine(text, (library?.images ?? []).map((i) => ({ name: i.name, canRunContainers: Boolean(i.published?.canRunContainers) })));
  // Turning it off where previews run it: a sleeping preview resumes what it was
  // submitted with, so only a fresh run gets the new version.
  const previewsLose = !containers && Boolean(image.published?.canRunContainers) && detail.previewedBy.length > 0;
  const next = (versions.reduce((n, v) => Math.max(n, v.number ?? 0), 0) || 0) + 1;
  const ahead = (library?.queue ?? []).length;
  const counts = base ? draftCounts(base.containerfile, text) : null;
  const choices = (library?.images ?? []).map((i) => ({ name: i.name, version: i.published?.number ?? null, isDefault: i.isDefault }));
  const running = builds.find((b) => b.state === "running" || b.state === "queued");
  const failed = (e: unknown) => {
    setRefused(errorText(e));
    throw e;
  };
  return (
    <div className="imageEdit">
      <div className="imageEditMain">
        {running ? (
          <Callout tone="info">
            v{running.version} is {running.state === "running" ? "building" : `waiting (${queuePlace(running.ahead)})`}.{" "}
            <Button size="sm" variant="quiet" onClick={() => onOpenBuild(running.id)}>Open its build</Button>
          </Callout>
        ) : null}
        <CodeEditor
          data-testid="containerfile-editor"
          aria-label="Containerfile"
          language="dockerfile"
          value={text}
          readOnly={!canEdit}
          onChange={(v) => {
            setText(v);
            setRefused(null);
          }}
          diagnostics={diagnostics}
          complete={(ctx) => containerfileCompletions(ctx, choices, orgName, image.name)}
          minLines={12}
          header={
            <>
              <span className="ds-mono">Containerfile</span>
              {draft || dirty ? <Badge size="sm" tone="attention">draft v{next}</Badge> : base ? <Badge size="sm">v{base.number}</Badge> : null}
              {base && (draft || dirty) ? <span>from v{base.number}</span> : null}
              <span className="imageEditSpacer" />
              <span className="hideNarrow">⌘F search · ⌘Z undo · Ctrl-Space complete</span>
            </>
          }
          after={
            <>
              <span className="layerLabel">DUDE LAYER</span>
              <span className="layerNote"> · added last, from the running release · read-only</span>
              {`\nCOPY --from=${builder.layer ? `dude-layer@${shortDigest(builder.layer)}` : "dude-layer (not configured)"} /rootfs/ /   # dude CLI, OpenCode, its config\nRUN ["/bin/sh", "/usr/local/share/dude/setup.sh"]\nUSER agent`}
            </>
          }
          footer={
            <>
              <span>{text.split("\n").length} lines</span>
              {counts ? <span>+{counts.add} −{counts.del} against v{base!.number}</span> : null}
              {flipped ? (
                <span className="imageFlagChange" data-testid="containers-changed">
                  <Icon name="cube" size={12} />Can run containers turned {flipped}
                </span>
              ) : null}
              <span className="imageEditSpacer" />
              {errors ? <span className="lintBad" data-testid="lint-errors">✕ {errors} won’t build</span> : <span className="lintOk">Builds</span>}
            </>
          }
        />
        <div className="imageContainers" data-testid="can-run-containers-field">
          <Checkbox checked={containers} disabled={!canEdit} onCheckedChange={(c) => setContainers(c === true)}
            label="Can run containers"
            description="Runs in this image can start containers inside with podman or Docker. dude checks the build can, and fails it if not."
            aria-describedby={[noEngine ? `${fieldId}-hint` : null, previewsLose ? `${fieldId}-warning` : null].filter(Boolean).join(" ")} />
          {noEngine ? (
            <p className="imageContainersHint" id={`${fieldId}-hint`} data-testid="containers-hint">
              <Icon name="warning" size={12} />
              This Containerfile doesn’t install podman or Docker. The build will fail its container check unless the base has them.
            </p>
          ) : null}
          {previewsLose ? (
            <p className="imageContainersHint" id={`${fieldId}-warning`} data-testid="containers-off-warning">
              <Icon name="warning" size={12} />
              New previews of this image can’t run containers. Existing ones keep theirs until they start a fresh run.
            </p>
          ) : null}
        </div>
        {refused ?? problem ? <Callout tone="danger" data-testid="image-save-problem">{refused ?? problem}</Callout> : null}
        {canEdit ? (
          <>
            <div className="imageSaveBar">
              {draft || dirty ? <span className="draftMark">● Draft not built</span> : null}
              <Input aria-label="What changed" placeholder="What changed, and why" value={note} onChange={(e) => setNote(e.target.value)} maxLength={500}
                data-testid="image-note" className="imageNote" />
              {draft ? (
                <Button variant="quiet" disabled={busy} onClick={() => void save(() => client.discardImageDraft(image.id).then(onChanged), undefined, "Draft discarded")}>
                  Discard draft
                </Button>
              ) : null}
              <Button variant="secondary" disabled={busy || !dirty} data-testid="save-draft"
                onClick={() => void save(() => client.saveImageDraft(image.id, { containerfile: text, note, canRunContainers: containers }).then(onChanged, failed), undefined, "Draft saved")}>
                Save draft
              </Button>
              <Button variant="primary" disabled={busy || errors > 0 || !builder.available || (!draft && !dirty)} data-testid="build-publish"
                onClick={() => void save(() => client.buildImage(image.id, { containerfile: text, note, canRunContainers: containers }).then((r) => {
                  onChanged(r.image);
                  onOpenBuild(r.buildId);
                }, failed), undefined, `v${next} queued`)}>
                Build & publish v{next}
              </Button>
            </div>
            <p className="imageSaveHint">
              {builder.available
                ? `v${next} joins the queue${ahead ? ` (${ahead} ahead)` : ""}. If it fails, ${image.published ? `v${image.published.number} stays published` : "nothing changes"} and the draft keeps its build log.`
                : "Builds are off on this dude (no DUDE_LAYER_IMAGE): drafts save, nothing builds."}
              {" "}Build arguments and the Containerfile are not secret: the history shows them.
            </p>
          </>
        ) : null}
      </div>
      <aside className="imageEditAside">
        <h3 className="ds-label">Used by · all on the latest</h3>
        <p className="imageAsideText" data-testid="image-used-by">{usedByWords(image.usedBy, orgName)}</p>
        {published ? (
          <>
            <h3 className="ds-label">Published · v{published.number}</h3>
            <dl className="imageFacts">
              <dt>Built</dt><dd>{published.builtAt ? formatTimestamp(published.builtAt, "datetime") : "—"}</dd>
              {published.parents.length ? <><dt>Base</dt><dd className="ds-mono">{published.parents.map((p) => `${p.name} v${p.version ?? "?"}`).join(", ")}</dd></> : null}
              <dt>Containers</dt><dd data-testid="image-fact-containers">{published.canRunContainers ? "Can run them" : "No"}</dd>
              <dt>Image</dt><dd className="ds-mono">{shortDigest(published.userRef)}</dd>
            </dl>
          </>
        ) : null}
        <h3 className="ds-label">Builds run</h3>
        <ul className="imageAsideList">
          <li>on the dude host, rootless, {builder.cpus} CPU and {(builder.memoryMiB / 1024).toFixed(1).replace(/\.0$/, "")} GB, one at a time;</li>
          <li>with no build files: COPY only from a stage or an image;</li>
          <li>with the internet, but not the host’s AWS credentials or dude’s own services;</li>
          <li>and, for a version that can run containers, checked that it can before it’s pushed.</li>
        </ul>
      </aside>
    </div>
  );
}

const asHistory = (v: ImageVersion): ImageHistoryVersion => ({
  id: v.id,
  number: v.number,
  state: v.state,
  containerfile: v.containerfile,
  note: v.note,
  author: v.createdBy,
  when: formatTimestamp(v.createdAt, "relative"),
  builtOn: v.parents.filter((p) => p.version).map((p) => `${p.name} v${p.version}`).join(", ") || undefined,
  error: v.error,
  canRunContainers: v.canRunContainers,
});

function HistoryTab({ client, detail, onChanged, onOpenBuild }: { client: ApiClient; detail: ImageDetail; onChanged: (d: ImageDetail) => void; onOpenBuild: (b: string) => void }) {
  const { image, versions, builds, canEdit } = detail;
  const [asking, setAsking] = useState<ImageHistoryVersion | null>(null);
  const { busy, problem, save } = useSave();
  const running = builds.find((b) => b.state === "running" && b.kind === "build");
  const users = image.usedBy.filter((u) => u.kind !== "child");
  const children = image.usedBy.filter((u) => u.kind === "child");
  const original = versions.find((v) => v.id === asking?.id);
  return (
    <>
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      <div data-testid="image-history">
        <ImageHistory versions={versions.map(asHistory)} publishedId={image.published?.versionId ?? null}
          onRepublish={canEdit ? setAsking : undefined}
          onOpenBuild={(v) => {
            const b = builds.find((x) => x.versionId === v.id && x.kind === "build");
            if (b) onOpenBuild(b.id);
          }} />
      </div>
      <Dialog open={asking !== null} onOpenChange={(o) => !o && setAsking(null)} size="md" title={`Publish v${asking?.number} again?`}
        description={`v${asking?.number}’s image is still in the registry, so there’s nothing to build. It becomes the published version straight away.`}
        footer={
          <>
            <Button variant="quiet" onClick={() => setAsking(null)}>Cancel</Button>
            <Button variant="primary" disabled={busy} data-testid="republish-confirm" onClick={() => {
              const v = asking!;
              void save(() => client.republishImage(image.id, v.id).then(onChanged), () => setAsking(null), `v${v.number} published again`);
            }}>Publish v{asking?.number}</Button>
          </>
        }>
        <ul className="republishList">
          <li>{users.length ? "Everything that uses it" : "Whatever picks it later"} runs v{asking?.number} from its next Run; Runs already going keep theirs.</li>
          {children.length ? <li>{children.map((c) => c.image!.name).join(", ")}, built FROM {image.name}, {children.length === 1 ? "is" : "are"} queued to rebuild on v{asking?.number}.</li> : null}
          {original?.parents.length ? <li>v{asking?.number} was built on {original.parents.map((p) => `${p.name} v${p.version ?? "?"}`).join(", ")}. To move it onto today’s, build it again instead.</li> : null}
          {running ? <li>The v{running.version} build that is running carries on. If it passes, it publishes over this.</li> : null}
        </ul>
      </Dialog>
    </>
  );
}

function BuildsTable({ builds, onOpen }: { builds: readonly ImageBuild[]; onOpen: (id: string) => void }) {
  if (builds.length === 0) return <Callout tone="neutral">No builds yet. Build & publish queues the draft.</Callout>;
  return (
    <Table density="compact" data-testid="image-builds">
      <THead>
        <Tr>
          <Th>Version</Th>
          <Th>Kind</Th>
          <Th>State</Th>
          <Th hideWhenNarrow>Asked</Th>
          <Th align="right" hideWhenNarrow>Took</Th>
        </Tr>
      </THead>
      <TBody>
        {builds.map((b) => (
          <Tr key={b.id} interactive onClick={() => onOpen(b.id)} data-testid="image-build-row">
            <Td mono>v{b.version ?? "?"}</Td>
            <Td>{b.kind === "finish" ? `dude layer ${shortDigest(b.layerRef).replace("sha256:", "").slice(0, 12)}` : "build"}</Td>
            <Td>
              <ImageState kind={b.state === "succeeded" ? "published" : b.state === "running" ? "building" : b.state === "queued" ? "waiting" : b.state === "failed" ? "failed" : "none"}>
                {b.state === "queued" ? `waiting · ${queuePlace(b.ahead)}` : b.state === "failed" ? `failed: ${b.error ?? ""}` : b.state}
              </ImageState>
            </Td>
            <Td hideWhenNarrow muted>{b.requestedBy?.name ?? "dude"} · {ago(b.requestedAt)}</Td>
            <Td align="right" hideWhenNarrow mono>{b.startedAt && b.finishedAt ? formatDuration(Date.parse(b.finishedAt) - Date.parse(b.startedAt)) : "—"}</Td>
          </Tr>
        ))}
      </TBody>
    </Table>
  );
}

// ---------------------------------------------------------------------------
// A build
// ---------------------------------------------------------------------------

/** The container check's own lines: what dude says italic, the failure as an error. */
function logLevel(text: string): LogLine["level"] {
  if (text.startsWith("Can't run containers:")) return "error";
  if (/^(Check containers: |Check passed: |Not pushed\.)/.test(text)) return "system";
  return undefined;
}

/** A library image that can run containers, other than this one, for the failed check's way out. */
function useAbleBase(client: ApiClient, imageId: string | undefined): string | null {
  const [name, setName] = useState<string | null>(null);
  useEffect(() => {
    if (!imageId) return;
    client.imageChoices().then((r) => setName(r.images.find((i) => i.canRunContainers && !i.archived && i.id !== imageId)?.name ?? null), () => undefined);
  }, [client, imageId]);
  return name;
}

function BuildPage({ client, buildId, onSub }: { client: ApiClient; buildId: string; onSub: (sub: string | undefined) => void }) {
  const [build, setBuild] = useState<ImageBuildWithLog | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  // While it runs, each read asks only for the log after the bytes the page has.
  const have = useRef<{ id: string; total: number } | null>(null);
  const shown = useRef(buildId);
  shown.current = buildId;
  useEffect(() => {
    have.current = build ? { id: build.id, total: build.logTotal } : null;
  }, [build]);
  const load = useCallback(() => {
    const after = have.current?.id === buildId ? have.current.total : undefined;
    client.imageBuild(buildId, after).then((b) => {
      setBuild((prev) => mergeBuildLog(prev, b, shown.current));
      setProblem(null);
    }, (err: unknown) => setProblem(errorText(err)));
  }, [client, buildId]);
  useEffect(load, [load]);
  const live = build?.state === "queued" || build?.state === "running";
  useEffect(() => {
    if (!live) return;
    const t = setInterval(load, 2000);
    return () => clearInterval(t);
  }, [live, load]);
  const lines = useMemo<LogLine[]>(() => (build?.log ?? "").replace(/\n$/, "").split("\n").filter((l, i, a) => l || i < a.length - 1).map((text, seq) => ({ seq, text, level: logLevel(text) })), [build?.log]);
  const ableBase = useAbleBase(client, build?.imageId);
  if (!build) return <div className="centered">{problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />}</div>;
  const elapsed = build.startedAt ? (build.finishedAt ? Date.parse(build.finishedAt) : Date.now()) - Date.parse(build.startedAt) : null;
  return (
    <>
      <Breadcrumb size="sm" items={[
        { id: "images", label: "Images", onSelect: () => onSub(undefined) },
        { id: build.imageId, label: build.imageName, mono: true, onSelect: () => onSub(build.imageId) },
        { id: build.id, label: build.kind === "finish" ? `Dude layer for v${build.version}` : `Build of v${build.version}` },
      ]} />
      <SettingsHeader leading={<ImageMark size={44} />} title={<span className="ds-mono" data-testid="build-title">{build.imageName} v{build.version}</span>}
        description={<>{build.note ? `“${build.note}” · ` : ""}{build.requestedBy?.name ?? "dude"}</>} />
      <div data-testid="build-stages" data-state={build.state}>
        <BuildStages stages={buildStages(build, build.builder, build.published?.number ?? null)} />
      </div>
      <dl className="buildFacts">
        <div><dt className="ds-tnum">{elapsed !== null ? formatDuration(elapsed) : build.state === "queued" ? queuePlace(build.ahead) : "—"}</dt><dd>{elapsed !== null ? (live ? "elapsed" : "took") : "in the line"}</dd></div>
        {build.buildSeconds ? <div><dt className="ds-tnum">{formatDuration(build.buildSeconds * 1000)}</dt><dd>building</dd></div> : null}
        {build.checkSeconds ? <div><dt className="ds-tnum">{formatDuration(build.checkSeconds * 1000)}</dt><dd>checking containers</dd></div> : null}
        {build.pushSeconds ? <div><dt className="ds-tnum">{formatDuration(build.pushSeconds * 1000)}</dt><dd>pushing</dd></div> : null}
        <div><dt className="ds-tnum">{build.published ? `v${build.published.number}` : "none"}</dt><dd>{build.state === "succeeded" && build.kind === "build" ? "published now" : "published"}</dd></div>
      </dl>
      {live ? <BuilderOffline builder={build.builder} /> : null}
      {build.state === "failed" ? <Callout tone="danger" data-testid="build-error">{build.error}</Callout> : null}
      {build.state === "failed" && build.containersCheck && !build.containersCheck.passed ? (
        <p className="buildFix" data-testid="build-fix">
          Install them in the Containerfile, or build <code>FROM image:{ableBase ?? "<an image that can>"}</code>, which can run containers. Or untick Can run containers.
        </p>
      ) : null}
      {build.state === "succeeded" && build.kind === "build" ? (
        <Callout tone="success">Published. Everything that uses {build.imageName} gets v{build.version} on its next Run.</Callout>
      ) : null}
      <LogStream lines={lines} title="Build log" live={live} maxHeight={520} emptyMessage={build.state === "queued" ? "Waiting for the builder." : "No output."} data-testid="build-log" />
    </>
  );
}

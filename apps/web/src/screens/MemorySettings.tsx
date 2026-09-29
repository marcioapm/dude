/**
 * Memory, in the organisation's and a project's settings: what people, dude
 * and agents remember, and the index they search (docs/design/memory.md).
 * Three pages under Memory in the settings menu — Search (what an agent
 * gets, and why each result ranked where it did), Memories (the records:
 * add, edit, archive) and Index (the embedder, what is indexed, what failed).
 * A project's shows its own and the organisation's, marked "From <org>"
 * and changed only in the organisation's settings.
 */

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import {
  AuthorLine,
  EntityLine,
  Markdown,
  RefLead,
  RemovableList,
  SearchPicker,
  SearchResultList,
  SearchResultRow,
  Segmented,
  SettingSource,
  SettingsHeader,
  SettingsNote,
  SettingsSection,
  ROLE_LABEL,
  type AuthorLineProps,
  type SettingsNavItem,
} from "@dude/design-system/components";
import {
  Badge,
  Button,
  Callout,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  Checkbox,
  Dialog,
  EmptyState,
  FormRow,
  FormStack,
  Input,
  KeyValueList,
  RowMenu,
  Select,
  Spinner,
  Table,
  TBody,
  Td,
  Textarea,
  Th,
  THead,
  Tr,
} from "@dude/design-system/primitives";
import { formatTimestamp, Icon } from "@dude/design-system";
import {
  MEMORY_KINDS,
  SEARCH_TYPES,
  type AgentRole,
  type IndexStatus,
  type Memory,
  type MemoryKind,
  type MemoryRef,
  type SearchOutcome,
  type SearchResult,
  type SearchType,
  type TaskStatus,
} from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { errorText, useSave } from "../hooks/useSave.tsx";
import { usePeople } from "../people.tsx";

export const MEMORY_PAGES = ["memory-search", "memory-list", "memory-index"] as const;
export type MemoryPage = (typeof MEMORY_PAGES)[number];
export const isMemoryPage = (page: string): page is MemoryPage => (MEMORY_PAGES as readonly string[]).includes(page);

/** Where the pages are: the organisation's settings, or one project's. */
export type MemoryScope = { readonly kind: "organization"; readonly name: string } | { readonly kind: "project"; readonly id: string; readonly name: string; readonly organization: string };

export interface ProjectChoice {
  readonly id: string;
  readonly name: string;
}

/** Memory in the settings menu, with its pages; a failure count on Index. */
export function memoryNav(failed?: number): SettingsNavItem {
  return {
    id: "memory",
    label: "Memory",
    icon: "memory",
    items: [
      { id: "memory-search", label: "Search" },
      { id: "memory-list", label: "Memories" },
      { id: "memory-index", label: "Index", note: failed ? `${failed} failed` : undefined },
    ],
  };
}

const ago = (iso: string) => formatTimestamp(iso, "relative");

/**
 * The index, read by the settings screen and kept current by the Index page:
 * the menu's failure note, and whether there is an embedder at all —
 * without one everything is found by its words, and "Text only" on every
 * row would say nothing.
 */
export interface IndexSummary {
  readonly status: IndexStatus | null;
  readonly problem: string | null;
  readonly reload: () => Promise<void>;
}

export function useIndexSummary(client: ApiClient, project?: string): IndexSummary {
  const [status, setStatus] = useState<IndexStatus | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const latest = useRef(0);
  const reload = useCallback(async () => {
    const mine = ++latest.current;
    try {
      const s = await client.memoryIndex(project);
      if (mine === latest.current) {
        setStatus(s);
        setProblem(null);
      }
    } catch (err) {
      if (mine === latest.current) setProblem(errorText(err));
    }
  }, [client, project]);
  useEffect(() => {
    void reload();
  }, [reload]);
  return { status, problem, reload };
}

/** The note a memory page sits under: who may do what here. */
function MemoryNote({ scope }: { scope: MemoryScope }) {
  return (
    <SettingsNote icon="info">
      {scope.kind === "organization"
        ? "Everyone here searches and adds memories; a person changes their own, and organisation admins anyone’s."
        : `Memories added here apply to ${scope.name} only. ${scope.organization}’s are shown too, marked “From ${scope.organization}”, and are changed in ${scope.organization}’s settings.`}
    </SettingsNote>
  );
}

export function MemoryPages({ client, scope, page, projects, admin, index, onPage }: {
  client: ApiClient;
  scope: MemoryScope;
  page: MemoryPage;
  projects: readonly ProjectChoice[];
  index: IndexSummary;
  /** An organisation admin: changes anyone's memory, reindexes. */
  admin: boolean;
  onPage: (page: MemoryPage) => void;
}) {
  const [editing, setEditing] = useState<Memory | "new" | null>(null);
  // A save bumps it: the open page reads again, keeping its words and filters.
  const [version, setVersion] = useState(0);
  const open = useCallback(async (id: string) => setEditing(await client.getMemory(id)), [client]);
  const embedder = Boolean(index.status?.model);
  return (
    <>
      <MemoryNote scope={scope} />
      {page === "memory-search" ? (
        <SearchPage client={client} scope={scope} projects={projects} version={version} onOpen={(id) => void open(id)} />
      ) : page === "memory-list" ? (
        <MemoriesPage version={version} client={client} scope={scope} projects={projects} embedder={embedder} onOpen={setEditing}
          add={<Button variant="primary" leadingIcon="plus" onClick={() => setEditing("new")} data-testid="memory-add">Add memory</Button>} />
      ) : (
        <IndexPage client={client} scope={scope} admin={admin} index={index} onMemories={() => onPage("memory-list")} />
      )}
      {editing ? (
        <MemoryDialog client={client} scope={scope} projects={projects} admin={admin}
          memory={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            setVersion((v) => v + 1);
            void index.reload();
          }} />
      ) : null}
    </>
  );
}

// ---------------------------------------------------------------------------
// Shared pieces
// ---------------------------------------------------------------------------

/** A ref as the design system names things: a task's key and status, an epic's or project's name. */
function refLead(r: MemoryRef) {
  return r.type === "task"
    ? { type: "task" as const, taskKey: r.label ?? r.id, status: r.status as TaskStatus | undefined }
    : { type: r.type, id: r.id, name: r.label ?? r.id };
}

function isRole(role: string | undefined): role is AgentRole {
  return role !== undefined && role in ROLE_LABEL && role !== "human" && role !== "system" && role !== "integration";
}

/** Who wrote it: a person; an agent on the face of the person it worked for; dude, and why. */
function authorOf(memory: Memory, people: ReturnType<typeof usePeople>): AuthorLineProps["author"] {
  const a = memory.author;
  if (a.kind === "system") return { kind: "system", reason: a.reason };
  const person = (a.personId ? people.byId.get(a.personId) : undefined) ?? { id: a.personId ?? "unknown", name: a.personName || "Someone" };
  if (a.kind === "agent" && isRole(a.role)) return { kind: "agent", person, role: a.role, task: a.taskKey };
  return { kind: "person", person };
}

function Writer({ memory }: { memory: Memory }) {
  const people = usePeople();
  return <AuthorLine author={authorOf(memory, people)} />;
}

const KIND_LABEL: Record<MemoryKind, string> = { fact: "Fact", procedure: "Procedure", note: "Note" };

function Scope({ memory, scope, projects }: { memory: Memory; scope: MemoryScope; projects: readonly ProjectChoice[] }) {
  if (memory.projectId) {
    const name = projects.find((p) => p.id === memory.projectId)?.name ?? memory.projectId;
    return <RefLead type="project" id={memory.projectId} name={name} named />;
  }
  if (scope.kind === "project") return <SettingSource source="organization" from={scope.organization} />;
  return <EntityLine size="sm" lead={<Icon name="building" size={14} />} name={`All of ${scope.name}`} />;
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

const TYPE_LABEL: Record<SearchType, string> = { memory: "Memories", task: "Tasks", epic: "Epics", project: "Projects" };
const TYPE_OPTIONS = [{ value: "all", label: "Everything" }, ...SEARCH_TYPES.map((t) => ({ value: t, label: TYPE_LABEL[t] }))];

function SearchPage({ client, scope, projects, version, onOpen }: {
  client: ApiClient;
  scope: MemoryScope;
  projects: readonly ProjectChoice[];
  /** Bumped by a save: the same search runs again, keeping its words. */
  version: number;
  onOpen: (id: string) => void;
}) {
  const [query, setQuery] = useState("");
  const [types, setTypes] = useState("all");
  const [project, setProject] = useState(scope.kind === "project" ? scope.id : "all");
  const [view, setView] = useState<"ranked" | "agent">("ranked");
  const [outcome, setOutcome] = useState<SearchOutcome | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const latest = useRef(0);
  const pending = useRef<ReturnType<typeof setTimeout>>(undefined);

  const run = useCallback(async () => {
    const q = query.trim();
    if (!q) {
      setOutcome(null);
      return;
    }
    const mine = ++latest.current;
    setBusy(true);
    try {
      const out = await client.searchMemory({
        q,
        ...(project !== "all" ? { project } : {}),
        ...(types !== "all" ? { types } : {}),
        limit: 20,
      });
      if (mine === latest.current) {
        setOutcome(out);
        setProblem(null);
      }
    } catch (err) {
      if (mine === latest.current) setProblem(errorText(err));
    } finally {
      if (mine === latest.current) setBusy(false);
    }
  }, [client, query, project, types]);

  // Types and project change the search at once; the words, on Enter or a pause.
  useEffect(() => {
    pending.current = setTimeout(() => void run(), 300);
    return () => clearTimeout(pending.current);
  }, [run, version]);

  return (
    <>
      <SettingsHeader title="Search"
        description="What an agent finds when it calls search_memory: memories, tasks, epics and projects, ranked by their words and their meaning together." />
      <form className="memoryToolbar" onSubmit={(e) => {
        e.preventDefault();
        // Enter searches now; the pause's search is not needed too.
        clearTimeout(pending.current);
        void run();
      }}>
        <Input className="memoryQuery" leading={<Icon name="search" size={14} />} value={query} autoFocus
          placeholder="Ask as an agent would: webhook retries, how to run the tests…" aria-label="Search memory"
          onChange={(e) => setQuery(e.target.value)} data-testid="memory-query" />
        <Select aria-label="What to search" value={types} onValueChange={setTypes} options={TYPE_OPTIONS} />
        {scope.kind === "organization" ? (
          <Select aria-label="Project" value={project} onValueChange={setProject}
            options={[{ value: "all", label: "All projects" }, ...projects.map((p) => ({ value: p.id, label: p.name }))]} />
        ) : null}
      </form>
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {outcome ? (
        <SettingsSection
          title={`${outcome.results.length} ${outcome.results.length === 1 ? "result" : "results"}`}
          actions={<Segmented size="sm" label="How to show the results" value={view} onChange={setView}
            options={[{ value: "ranked", label: "Ranked" }, { value: "agent", label: "As the agent sees it" }]} />}
        >
          {outcome.mode === "words" ? (
            <Callout tone="neutral">
              {outcome.degraded ? `Searched by words only: the embedder failed (${outcome.degraded}).` : "Searched by words only: no embedder is configured for this deployment."}
            </Callout>
          ) : null}
          {outcome.results.length === 0 ? (
            <EmptyState compact icon="search" title="Nothing found" description="Try other words: search matches any of them, and their meaning." />
          ) : view === "agent" ? (
            <Markdown source={"```text\n" + agentView(query, outcome.results) + "\n```"} />
          ) : (
            <SearchResultList data-testid="memory-results">
              {outcome.results.map((r, i) => (
                <ResultRow key={`${r.type}/${r.id}`} r={r} rank={i + 1} open={i === 0} projects={projects}
                  model={outcome.model} onOpen={onOpen} />
              ))}
            </SearchResultList>
          )}
        </SettingsSection>
      ) : busy ? (
        <Spinner label="Searching…" />
      ) : null}
    </>
  );
}

function found(r: SearchResult): string {
  return r.textRank && r.vectorRank ? "words and meaning" : r.textRank ? "words only" : "meaning only";
}

function resultLead(r: SearchResult) {
  if (r.type === "task") return { type: "task" as const, taskKey: r.key ?? r.id, status: r.status as TaskStatus | undefined };
  return { type: r.type, id: r.id, name: r.title };
}

function ResultRow({ r, rank, open, projects, model, onOpen }: {
  r: SearchResult;
  rank: number;
  open: boolean;
  projects: readonly ProjectChoice[];
  model: string | undefined;
  onOpen: (id: string) => void;
}) {
  const project = r.type !== "project" && r.projectId ? projects.find((p) => p.id === r.projectId)?.name : undefined;
  const terms = [r.textRank, r.vectorRank].filter((rank) => rank > 0);
  return (
    <SearchResultRow
      rank={rank}
      data-result={`${r.type}/${r.id}`}
      defaultExpanded={open}
      lead={resultLead(r)}
      title={r.title}
      badge={!r.embedded && model ? <Badge size="sm" emphasis="subtle" icon="clock">Text only</Badge> : undefined}
      facts={[found(r), ...(project ? [project] : [])]}
    >
      {r.snippet ? <span>{r.snippet}</span> : null}
      <KeyValueList items={[
        { label: "Words", value: r.textRank ? `#${r.textRank} · ts_rank_cd ${(r.textScore ?? 0).toFixed(3)}` : "not matched", mono: true },
        ...(model ? [
          { label: "Meaning", value: r.vectorRank ? `#${r.vectorRank} · cosine distance ${(r.distance ?? 0).toFixed(3)}` : r.embedded ? "not among the nearest" : "not embedded yet", mono: true },
        ] : []),
        { label: "Score", value: `${r.score.toFixed(4)} = ${terms.map((t) => `1/(60+${t})`).join(" + ")}`, mono: true },
        ...(model ? [{ label: "Indexed", value: r.embedded ? model : "by words; meaning queued", mono: true }] : []),
      ]} />
      {r.type === "memory" ? (
        <div className="memoryActions">
          <Button size="sm" variant="secondary" leadingIcon="edit" onClick={() => onOpen(r.id)}>Open memory</Button>
        </div>
      ) : null}
    </SearchResultRow>
  );
}

/** What search_memory returns, as text: the same ranking the agent reads. */
function agentView(query: string, results: readonly SearchResult[]): string {
  const lines = [`search_memory(query: ${JSON.stringify(query.trim())})`, ""];
  results.forEach((r, i) => {
    const head = r.type === "task" ? `task ${r.key ?? r.id}${r.status ? ` · ${r.status.replace(/_/g, " ")}` : ""} · ${r.title}`
      : r.type === "memory" ? `memory · ${r.id} · ${r.title}` : `${r.type} · ${r.title}`;
    lines.push(`${i + 1}. ${head}`);
    const snip = r.snippet.replace(/\s+/g, " ").trim();
    if (snip) lines.push(`   ${snip.length > 160 ? snip.slice(0, 159) + "…" : snip}`);
  });
  return lines.join("\n");
}

// ---------------------------------------------------------------------------
// Memories
// ---------------------------------------------------------------------------

function MemoriesPage({ version, client, scope, projects, embedder, onOpen, add }: {
  /** Bumped by a save: the list reads again, keeping its filters. */
  version: number;
  client: ApiClient;
  scope: MemoryScope;
  projects: readonly ProjectChoice[];
  embedder: boolean;
  onOpen: (m: Memory) => void;
  add: ReactNode;
}) {
  const [text, setText] = useState("");
  const [where, setWhere] = useState("all");
  const [author, setAuthor] = useState("any");
  const [archived, setArchived] = useState(false);
  const [memories, setMemories] = useState<Memory[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const latest = useRef(0);

  const load = useCallback(async () => {
    const mine = ++latest.current;
    try {
      const { memories } = await client.listMemories({
        ...(scope.kind === "project" ? { project: scope.id } : where !== "all" ? { scope: where } : {}),
        ...(author !== "any" ? { author } : {}),
        ...(text.trim() ? { q: text.trim() } : {}),
        ...(archived ? { archived: true } : {}),
      });
      if (mine === latest.current) {
        setMemories(memories);
        setProblem(null);
      }
    } catch (err) {
      if (mine === latest.current) setProblem(errorText(err));
    }
  }, [client, scope, where, author, text, archived]);

  useEffect(() => {
    const t = setTimeout(() => void load(), 200);
    return () => clearTimeout(t);
  }, [load, version]);

  const act = useSave();
  const toggleArchive = (m: Memory) =>
    void act.save(() => client.archiveMemory(m.id, !m.archivedAt), () => void load(), m.archivedAt ? "Memory restored" : "Memory archived");

  const org = scope.kind === "project" ? scope.organization : scope.name;
  return (
    <>
      <SettingsHeader title="Memories" actions={add}
        description={scope.kind === "organization"
          ? "What people, dude and agents chose to remember. Live as soon as they are added; archive one to take it out of every search."
          : `What ${scope.name} remembers, and ${org}’s memories every project shares.`} />
      <div className="memoryToolbar">
        <Input size="sm" className="memoryFilter" leading={<Icon name="search" size={14} />} placeholder="Filter" aria-label="Filter memories"
          value={text} onChange={(e) => setText(e.target.value)} />
        {scope.kind === "organization" ? (
          <Select size="sm" aria-label="Where" value={where} onValueChange={setWhere}
            options={[{ value: "all", label: "Everywhere" }, { value: "organization", label: `All of ${org}` }, ...projects.map((p) => ({ value: p.id, label: p.name }))]} />
        ) : null}
        <Select size="sm" aria-label="Written by" value={author} onValueChange={setAuthor}
          options={[{ value: "any", label: "Written by anyone" }, { value: "person", label: "People" }, { value: "agent", label: "Agents" }]} />
        <span className="memorySpacer" />
        <Checkbox checked={archived} onCheckedChange={(c) => setArchived(c === true)} label="Show archived" />
      </div>
      {problem || act.problem ? <Callout tone="danger">{problem ?? act.problem}</Callout> : null}
      {!memories ? (
        <Spinner label="Loading…" />
      ) : memories.length === 0 ? (
        <EmptyState icon="memory" title="Nothing remembered yet"
          description="Agents save what they learn with remember; add what every agent here should know." />
      ) : (
        <Table data-testid="memories">
          <THead>
            <Tr>
              <Th>Memory</Th>
              <Th width="16%">{scope.kind === "project" ? "From" : "Applies to"}</Th>
              <Th width="24%">Written by</Th>
              <Th align="right" width="48px"><span className="ds-sr-only">Actions</span></Th>
            </Tr>
          </THead>
          <TBody>
            {memories.map((m) => {
              const inherited = scope.kind === "project" && !m.projectId;
              return (
                <Tr key={m.id} interactive onClick={() => onOpen(m)} data-memory={m.id}>
                  <Td>
                    <EntityLine
                      lead={<Icon name="memory" size={16} />}
                      name={m.title}
                      detail={[KIND_LABEL[m.kind], m.source?.label ? `learned on ${m.source.label}` : null, ago(m.createdAt)].filter(Boolean).join(" · ")}
                      trailing={
                        <>
                          {m.archivedAt ? <Badge size="sm" icon="archive">Archived</Badge> : null}
                          {m.index === "waiting" && embedder && !m.archivedAt ? <Badge size="sm" emphasis="subtle" icon="clock">Text only</Badge> : null}
                          {m.index === "failed" && !m.archivedAt ? <Badge size="sm" tone="danger" icon="alert" title={m.indexNote}>Not embedded</Badge> : null}
                        </>
                      }
                    />
                  </Td>
                  <Td><Scope memory={m} scope={scope} projects={projects} /></Td>
                  <Td><Writer memory={m} /></Td>
                  <Td align="right" fit onClick={(e) => e.stopPropagation()}>
                    <RowMenu label={`Actions for ${m.title}`} items={[
                      { id: "edit", label: inherited ? "Open" : "Edit", icon: "edit", onSelect: () => onOpen(m) },
                      { id: "copy", label: "Copy id", icon: "copy", onSelect: () => void navigator.clipboard?.writeText(m.id) },
                      { kind: "separator" },
                      inherited
                        ? { id: "archive", label: "Archive", icon: "archive", disabled: true, disabledReason: `${org}’s memories are archived in ${org}’s settings` }
                        : { id: m.archivedAt ? "restore" : "archive", label: m.archivedAt ? "Restore" : "Archive", icon: m.archivedAt ? "retry" : "archive", onSelect: () => toggleArchive(m) },
                    ]} />
                  </Td>
                </Tr>
              );
            })}
          </TBody>
        </Table>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// Add / edit
// ---------------------------------------------------------------------------

function MemoryDialog({ client, scope, projects, admin, memory, onClose, onSaved }: {
  client: ApiClient;
  scope: MemoryScope;
  projects: readonly ProjectChoice[];
  admin: boolean;
  memory: Memory | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const people = usePeople();
  const [title, setTitle] = useState(memory?.title ?? "");
  const [content, setContent] = useState(memory?.content ?? "");
  const [kind, setKind] = useState<MemoryKind>(memory?.kind ?? "fact");
  const [appliesTo, setAppliesTo] = useState(memory ? (memory.projectId ?? "organization") : scope.kind === "project" ? scope.id : "organization");
  const [about, setAbout] = useState<readonly MemoryRef[]>(memory?.about ?? []);
  const [adding, setAdding] = useState(false);
  const { busy, problem, save } = useSave();

  const org = scope.kind === "project" ? scope.organization : scope.name;
  const inherited = Boolean(memory && scope.kind === "project" && !memory.projectId);
  const mine = memory?.author.kind === "person" && memory.author.personId === people.you;
  const canChange = !memory || ((admin || mine) && !inherited);
  const input = {
    title: title.trim(),
    content: content.trim(),
    kind,
    projectId: appliesTo === "organization" ? "" : appliesTo,
    about: about.map(({ type, id }) => ({ type, id })),
  };
  // A lookup by name (a task's key, an epic's title): words, not meaning.
  const find = useCallback(
    async (q: string) => (await client.searchMemory({ q, types: "task,epic,project", limit: 8, mode: "words",
      ...(appliesTo !== "organization" ? { project: appliesTo } : {}) })).results,
    [client, appliesTo],
  );

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="lg"
      title={memory ? (canChange ? "Edit memory" : "Memory") : "Add a memory"}
      description={memory ? undefined : "Live as soon as you add it: agents find it the next time they search."}
      footer={
        <>
          {memory && canChange ? (
            <Button variant="quiet" leadingIcon={memory.archivedAt ? "retry" : "archive"} disabled={busy}
              onClick={() => void save(() => client.archiveMemory(memory.id, !memory.archivedAt), onSaved, memory.archivedAt ? "Memory restored" : "Memory archived")}>
              {memory.archivedAt ? "Restore" : "Archive"}
            </Button>
          ) : null}
          <span className="memorySpacer" />
          <Button variant="secondary" onClick={onClose}>{canChange ? "Cancel" : "Close"}</Button>
          {canChange ? (
            <Button variant="primary" disabled={busy || !title.trim()} data-testid="memory-save"
              onClick={() => void save(() => (memory ? client.updateMemory(memory.id, input) : client.createMemory(input)), onSaved,
                memory ? "Memory saved" : "Memory added")}>
              {memory ? "Save" : "Add memory"}
            </Button>
          ) : null}
        </>
      }
    >
      <FormStack>
        {memory ? (
          <KeyValueList items={[
            { label: "Written by", value: <Writer memory={memory} /> },
            ...(memory.source?.label ? [{ label: "Learned on", value: <RefLead {...refLead(memory.source)} named /> }] : []),
            { label: "Id", value: memory.id, mono: true },
          ]} />
        ) : null}
        {inherited ? (
          <Callout tone="neutral">{org}’s memory: every project searches it. Change it in {org}’s settings.</Callout>
        ) : !canChange ? (
          <Callout tone="neutral">Only the person who wrote it, or an organisation admin, changes it.</Callout>
        ) : null}
        <fieldset disabled={!canChange} className="plainFieldset">
          <FormStack>
            <Input label="Title" value={title} maxLength={200} autoFocus={!memory} data-testid="memory-title"
              placeholder="One line an agent can scan in a list of results" onChange={(e) => setTitle(e.target.value)} />
            <Textarea label="What to remember" rows={5} value={content} maxLength={20000} data-testid="memory-content"
              placeholder="Markdown. Say it the way you would tell a new colleague." hint="One fact, procedure or note per memory."
              onChange={(e) => setContent(e.target.value)} />
            <FormRow>
              <Select label="Kind" value={kind} onValueChange={(v) => setKind(v as MemoryKind)}
                options={MEMORY_KINDS.map((k) => ({ value: k, label: KIND_LABEL[k] }))} />
              <Select label="Applies to" value={appliesTo} onValueChange={setAppliesTo}
                options={[{ value: "organization", label: `All of ${org}` }, ...projects.map((p) => ({ value: p.id, label: p.name }))]} />
            </FormRow>
          </FormStack>
        </fieldset>
        <SettingsSection title="About" actions={canChange ? <Button size="sm" variant="quiet" leadingIcon="plus" onClick={() => setAdding(true)}>Add</Button> : undefined}>
          {about.length ? (
            <RemovableList
              items={about.map((r) => ({ id: `${r.type}/${r.id}`, label: r.label ?? r.id, content: <RefLead {...refLead(r)} named /> }))}
              onRemove={canChange ? (key) => setAbout(about.filter((r) => `${r.type}/${r.id}` !== key)) : undefined} />
          ) : (
            <p className="muted">Nothing yet: the tasks, epics and projects this memory is about.</p>
          )}
          {adding ? (
            <SearchPicker<SearchResult>
              label="Find a task, epic or project"
              placeholder="A task key, an epic, a project"
              autoFocus
              find={find}
              optionKey={(r) => `${r.type}/${r.id}`}
              renderOption={(r) => <><RefLead {...resultLead(r)} /><span>{r.title}</span></>}
              onPick={(r) => {
                if (r.type !== "memory" && !about.some((x) => x.type === r.type && x.id === r.id)) {
                  setAbout([...about, { type: r.type, id: r.id, label: r.type === "task" ? r.key : r.title, status: r.status }]);
                }
                setAdding(false);
              }}
              onCancel={() => setAdding(false)} />
          ) : null}
        </SettingsSection>
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
      </FormStack>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Index
// ---------------------------------------------------------------------------

const KIND_TEXT: Record<SearchType, [string, string]> = {
  memory: ["Memories", "title, content"],
  task: ["Tasks", "key, title, goal, acceptance criteria"],
  epic: ["Epics", "title, description"],
  project: ["Projects", "name, description"],
};

function IndexPage({ client, scope, admin, index, onMemories }: {
  client: ApiClient;
  scope: MemoryScope;
  admin: boolean;
  index: IndexSummary;
  onMemories: () => void;
}) {
  const org = scope.kind === "project" ? scope.organization : scope.name;
  const { status, problem, reload } = index;
  const [confirm, setConfirm] = useState(false);
  const act = useSave();
  const busy = Boolean(status && (status.waiting > 0 || status.health.error));

  // Embedding happens in the background: watch it land while there is
  // something to watch, and not while the page is hidden.
  useEffect(() => {
    void reload();
  }, [reload]);
  useEffect(() => {
    if (!busy) return;
    const t = setInterval(() => {
      if (!document.hidden) void reload();
    }, 10_000);
    return () => clearInterval(t);
  }, [busy, reload]);

  // Reindex and Retry act on the organisation's whole index, whatever page
  // they are pressed on: the count they name is the organisation's.
  const [orgTotal, setOrgTotal] = useState<number | null>(null);
  useEffect(() => {
    if (!confirm) return;
    let live = true;
    client.memoryIndex().then((s) => live && setOrgTotal(s.total), () => undefined);
    return () => {
      live = false;
    };
  }, [client, confirm]);

  if (!status) return <div className="centered">{problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />}</div>;
  const broken = status.health.error;

  return (
    <>
      <SettingsHeader title="Index"
        description="Everything search_memory can find, and the embeddings that let it search by meaning. What is not embedded yet is still found by its words." />
      {problem || act.problem ? <Callout tone="danger">{problem ?? act.problem}</Callout> : null}
      <Card data-testid="memory-embedder">
        <CardHeader title="Embeddings" actions={!status.model ? <Badge>Words only</Badge>
          : broken ? <Badge tone="danger" icon="alert">Failing</Badge>
          : status.failed ? <Badge tone="danger" icon="alert">{status.failed} not embedded</Badge>
          : status.waiting ? <Badge icon="clock">Embedding {status.waiting.toLocaleString("en-US")}</Badge>
          : <Badge tone="success" icon="check">Up to date</Badge>} />
        <CardBody>
          {broken ? (
            <Callout tone="danger" data-testid="memory-embedder-failing">
              The embedder has failed since {status.health.since ? ago(status.health.since) : "a moment ago"}: {broken}. Nothing is embedded
              until it works; search is by words meanwhile. Tried again {status.health.retry ? `at ${formatTimestamp(status.health.retry, "time-short")}` : "soon"}.
            </Callout>
          ) : null}
          {status.model ? (
            <KeyValueList items={[
              { label: "Model", value: `${status.model} · ${status.dimensions} dimensions`, mono: true },
              ...(status.endpoint ? [{ label: "Endpoint", value: status.endpoint, mono: true }] : []),
              { label: "Set by", value: "the deployment (DUDE_EMBEDDINGS_*)" },
            ]} />
          ) : (
            <p className="muted">No embedder is configured for this deployment (DUDE_EMBEDDINGS_URL), so memory is searched by words alone.</p>
          )}
        </CardBody>
        {status.model && admin ? (
          <CardFooter>
            <Button variant="secondary" onClick={() => setConfirm(true)}>Reindex all of {org}…</Button>
          </CardFooter>
        ) : null}
      </Card>

      <SettingsSection title="What is indexed">
        <Table density="compact" data-testid="memory-kinds">
          <THead>
            <Tr>
              <Th>Kind</Th>
              <Th>Text</Th>
              <Th align="right" width="12%">Documents</Th>
              <Th align="right" width="12%">Embedded</Th>
              <Th align="right" width="10%">Waiting</Th>
              <Th align="right" width="10%">Failed</Th>
            </Tr>
          </THead>
          <TBody>
            {status.kinds.map((k) => (
              <Tr key={k.type} interactive={k.type === "memory"} onClick={k.type === "memory" ? onMemories : undefined}>
                <Td>{KIND_TEXT[k.type][0]}</Td>
                <Td muted>{KIND_TEXT[k.type][1]}</Td>
                <Td align="right" mono>{k.total.toLocaleString("en-US")}</Td>
                <Td align="right" mono>{k.embedded.toLocaleString("en-US")}</Td>
                <Td align="right" mono muted={!k.waiting}>{k.waiting}</Td>
                <Td align="right" mono muted={!k.failed}>{k.failed}</Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      </SettingsSection>

      {status.failed ? (
        <SettingsSection title="Not embedded"
          actions={<Button size="sm" variant="secondary" leadingIcon="retry" disabled={act.busy}
            onClick={() => void act.save(() => client.retryIndex(), () => void reload(), "Retrying now")}>
            {scope.kind === "project" ? `Retry all of ${org}’s` : "Retry all"}</Button>}>
          <Table density="compact" data-testid="memory-failures">
            <THead>
              <Tr>
                <Th width="40%">Document</Th>
                <Th>Last error</Th>
                <Th align="right" width="10%">Attempts</Th>
                <Th align="right" width="10%">Last try</Th>
                <Th align="right" width="48px"><span className="ds-sr-only">Actions</span></Th>
              </Tr>
            </THead>
            <TBody>
              {status.failures.map((f) => (
                <Tr key={`${f.type}/${f.id}`}>
                  <Td title={f.title}>
                    <EntityLine size="sm" lead={<RefLead type={f.type} />} name={f.title} />
                  </Td>
                  <Td mono muted title={f.error}>{f.error}</Td>
                  <Td align="right" mono>{f.attempts}</Td>
                  <Td align="right" muted>{ago(f.lastTry)}</Td>
                  <Td align="right" fit>
                    <Button size="sm" variant="quiet" leadingIcon="retry" aria-label={`Retry ${f.title}`} disabled={act.busy}
                      onClick={() => void act.save(() => client.retryIndex({ type: f.type, id: f.id }), () => void reload(), "Retrying now")}>
                      Retry
                    </Button>
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        </SettingsSection>
      ) : null}

      <Dialog open={confirm} onOpenChange={setConfirm} tone="attention" title={`Reindex all of ${org}?`}
        description={`Embeds ${orgTotal === null ? "every" : `all ${orgTotal.toLocaleString("en-US")}`} document${orgTotal === 1 ? "" : "s"} in ${org} again${status.model ? ` with ${status.model}` : ""}, every project's. It takes a while; until each is done, search finds it by its words.`}
        footer={
          <>
            <Button variant="secondary" onClick={() => setConfirm(false)}>Cancel</Button>
            <Button variant="primary" disabled={act.busy}
              onClick={() => void act.save(() => client.reindexMemory(), () => { setConfirm(false); void reload(); }, "Reindexing")}>
              Reindex
            </Button>
          </>
        } />
    </>
  );
}

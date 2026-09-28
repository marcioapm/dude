/**
 * Memory, in the organisation's and a project's settings: what people, dude
 * and agents remember, and the index they search (docs/design/memory.md).
 * Three pages under Memory in the settings menu — Search (what an agent
 * gets, and why each result ranked where it did), Memories (the records:
 * add, edit, archive) and Index (the embedder, what is indexed, what failed).
 * A project's shows its own and the organisation's, marked "From <org>"
 * and changed only in the organisation's settings.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  AgentAvatar,
  Markdown,
  PersonLine,
  ProjectAvatar,
  SearchResultList,
  SearchResultRow,
  Segmented,
  SettingsHeader,
  SettingsSection,
  StatusMark,
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
  IconButton,
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
import { Icon, cx } from "@dude/design-system";
import { MEMORY_KINDS, type IndexStatus, type Memory, type MemoryKind, type MemoryRef, type SearchOutcome, type SearchResult, type TaskStatus } from "@dude/domain";
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

/**
 * The index, read once by the settings screen: how many documents failed
 * (the menu's note), and whether there is an embedder at all — without
 * one everything is found by its words, and "Text only" on every row
 * would say nothing.
 */
export interface IndexSummary {
  readonly failed?: number;
  readonly embedder: boolean;
}

export function useIndexSummary(client: ApiClient, project?: string): IndexSummary {
  const [summary, setSummary] = useState<IndexSummary>({ embedder: false });
  useEffect(() => {
    let live = true;
    client.memoryIndex(project).then(
      (s) => live && setSummary({ failed: s.kinds.reduce((n, k) => n + k.failed, 0), embedder: Boolean(s.model) }),
      () => undefined,
    );
    return () => {
      live = false;
    };
  }, [client, project]);
  return summary;
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
  // A save bumps it: the open page reads again.
  const [version, setVersion] = useState(0);
  const project = scope.kind === "project" ? scope.id : undefined;
  const open = useCallback(async (id: string) => setEditing(await client.getMemory(id)), [client]);
  return (
    <>
      {page === "memory-search" ? (
        <SearchPage client={client} scope={scope} projects={projects} version={version} onOpen={(id) => void open(id)} />
      ) : page === "memory-list" ? (
        <MemoriesPage key={version} client={client} scope={scope} projects={projects} embedder={index.embedder} onOpen={setEditing}
          add={<Button variant="primary" leadingIcon="plus" onClick={() => setEditing("new")} data-testid="memory-add">Add memory</Button>} />
      ) : (
        <IndexPage client={client} project={project} admin={admin} onMemories={() => onPage("memory-list")} />
      )}
      {editing ? (
        <MemoryDialog client={client} scope={scope} projects={projects} admin={admin}
          memory={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            setVersion((v) => v + 1);
          }} />
      ) : null}
    </>
  );
}

// ---------------------------------------------------------------------------
// Shared pieces
// ---------------------------------------------------------------------------

/** What a thing is, in the sidebar's grammar: a task's status mark and key, an epic's layers, a project's face. */
function Lead({ type, id, label, status }: { type: string; id: string; label?: string | undefined; status?: string | undefined }) {
  if (type === "task" || type === "run") {
    return (
      <span className="memoryLead">
        {status ? <StatusMark status={status as TaskStatus} iconOnly size="sm" /> : null}
        <span className="ds-mono">{label ?? id}</span>
      </span>
    );
  }
  if (type === "epic") return <Icon name="layers" size={14} />;
  if (type === "project") return <ProjectAvatar project={{ id, name: label ?? id }} size={16} />;
  return <Icon name="memory" size={14} />;
}

function RefText({ r }: { r: MemoryRef }) {
  return (
    <span className="memoryRef">
      <Lead type={r.type} id={r.id} label={r.label} status={r.status} />
      {r.type === "task" || r.type === "run" ? null : <span>{r.label ?? r.id}</span>}
    </span>
  );
}

/** Who wrote it: a person; an agent on the face of the person it worked for; dude, and why. */
function Writer({ memory }: { memory: Memory }) {
  const people = usePeople();
  const a = memory.author;
  if (a.kind === "system") {
    return (
      <span className="memoryDude">
        <AgentAvatar role="system" size="md" />
        <span className="memoryText">
          <span>dude</span>
          {a.reason ? <span className="memoryDetail">{a.reason}</span> : null}
        </span>
      </span>
    );
  }
  const who = (a.personId ? people.byId.get(a.personId) : undefined) ?? { id: a.personId ?? "unknown", name: a.personName || "Someone" };
  if (a.kind === "agent") {
    const role = (a.role || "implementer") as "implementer";
    return <PersonLine person={who} size={24} agent={role} detail={`${roleWord(a.role)}${a.taskKey ? ` on ${a.taskKey}` : ""}`} />;
  }
  return <PersonLine person={who} size={24} />;
}

const roleWord = (r?: string) => (r ? r[0]!.toUpperCase() + r.slice(1).replace("_", " ") : "An agent");

const KIND_LABEL: Record<MemoryKind, string> = { fact: "Fact", procedure: "Procedure", note: "Note" };

function ago(iso: string): string {
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

function Scope({ memory, scope, projects }: { memory: Memory; scope: MemoryScope; projects: readonly ProjectChoice[] }) {
  if (memory.projectId) {
    const name = projects.find((p) => p.id === memory.projectId)?.name ?? memory.projectId;
    return <span className="memoryRef"><ProjectAvatar project={{ id: memory.projectId, name }} size={16} /><span className="memoryClip">{name}</span></span>;
  }
  const org = scope.kind === "project" ? scope.organization : scope.name;
  if (scope.kind === "project") return <span className="memoryInherited"><Icon name="layers" size={12} />From {org}</span>;
  return <span className="memoryRef"><Icon name="building" size={14} /><span className="memoryClip" title={`All of ${org}`}>All of {org}</span></span>;
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

const TYPE_OPTIONS = [
  { value: "all", label: "Everything" },
  { value: "memory", label: "Memories" },
  { value: "task", label: "Tasks" },
  { value: "epic", label: "Epics" },
  { value: "project", label: "Projects" },
];

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
    const t = setTimeout(() => void run(), 300);
    return () => clearTimeout(t);
  }, [run, version]);

  return (
    <>
      <SettingsHeader title="Search"
        description="What an agent finds when it calls search_memory: memories, tasks, epics and projects, ranked by their words and their meaning together." />
      <form className="memoryToolbar" onSubmit={(e) => { e.preventDefault(); void run(); }}>
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

/** A snippet as text: no match marks, no Markdown emphasis or code ticks. */
function plain(snippet: string): string {
  return snippet.replace(/[⟦⟧]/g, "").replace(/(\*\*|__|`)/g, "");
}

function found(r: SearchResult): string {
  return r.textRank && r.vectorRank ? "words and meaning" : r.textRank ? "words only" : "meaning only";
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
  const terms = [r.textRank ? { rank: r.textRank } : null, r.vectorRank ? { rank: r.vectorRank } : null].filter((x) => x !== null);
  return (
    <SearchResultRow
      rank={rank}
      data-result={`${r.type}/${r.id}`}
      defaultExpanded={open}
      lead={<Lead type={r.type} id={r.id} label={r.type === "task" ? r.key : r.title} status={r.status} />}
      title={r.title}
      badge={!r.embedded && model ? <Badge size="sm" emphasis="subtle" icon="clock">Text only</Badge> : undefined}
      facts={[found(r), ...(project ? [project] : [])]}
    >
      {r.snippet ? <p className="memorySnippet">{plain(r.snippet)}</p> : null}
      <KeyValueList items={[
        { label: "Words", value: r.textRank ? `#${r.textRank} · ts_rank_cd ${(r.textScore ?? 0).toFixed(3)}` : "not matched", mono: true },
        ...(model ? [
          { label: "Meaning", value: r.vectorRank ? `#${r.vectorRank} · cosine distance ${(r.distance ?? 0).toFixed(3)}` : r.embedded ? "not among the nearest" : "not embedded yet", mono: true },
        ] : []),
        { label: "Score", value: `${r.score.toFixed(4)} = ${terms.map((t) => `1/(60+${t.rank})`).join(" + ")}`, mono: true },
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
    const snip = plain(r.snippet).replace(/\s+/g, " ").trim();
    if (snip) lines.push(`   ${snip.length > 160 ? snip.slice(0, 159) + "…" : snip}`);
  });
  return lines.join("\n");
}

// ---------------------------------------------------------------------------
// Memories
// ---------------------------------------------------------------------------

function MemoriesPage({ client, scope, projects, embedder, onOpen, add }: {
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
  }, [load]);

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
          options={[{ value: "any", label: "Written by anyone" }, { value: "person", label: "People" }, { value: "agent", label: "Agents" }, { value: "system", label: "dude" }]} />
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
                <Tr key={m.id} interactive className={cx(m.archivedAt && "memoryArchived")} onClick={() => onOpen(m)} data-memory={m.id}>
                  <Td>
                    <span className="memoryCell">
                      <span className="memoryGlyph"><Icon name="memory" size={16} /></span>
                      <span className="memoryText">
                        <span className="memoryName">{m.title}</span>
                        <span className="memoryDetail">
                          {[KIND_LABEL[m.kind], m.source?.label ? `learned on ${m.source.label}` : null, ago(m.createdAt)].filter(Boolean).join(" · ")}
                        </span>
                      </span>
                      <span className="memorySpacer" />
                      {m.archivedAt ? <Badge size="sm" icon="archive">Archived</Badge> : null}
                      {m.index === "waiting" && embedder ? <Badge size="sm" emphasis="subtle" icon="clock">Text only</Badge> : null}
                      {m.index === "failed" ? <Badge size="sm" tone="danger" icon="alert" title={m.indexNote}>Not embedded</Badge> : null}
                    </span>
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
      <div className="memoryForm">
        {memory ? (
          <KeyValueList items={[
            { label: "Written by", value: <Writer memory={memory} /> },
            ...(memory.source?.label ? [{ label: "Learned on", value: <RefText r={memory.source} /> }] : []),
            { label: "Id", value: memory.id, mono: true },
          ]} />
        ) : null}
        {inherited ? (
          <Callout tone="neutral">{org}’s memory: every project searches it. Change it in {org}’s settings.</Callout>
        ) : !canChange ? (
          <Callout tone="neutral">Only the person who wrote it, or an organisation admin, changes it.</Callout>
        ) : null}
        <fieldset disabled={!canChange} className="plainFieldset memoryForm">
          <Input label="Title" value={title} maxLength={200} autoFocus={!memory} data-testid="memory-title"
            placeholder="One line an agent can scan in a list of results" onChange={(e) => setTitle(e.target.value)} />
          <Textarea label="What to remember" rows={5} value={content} maxLength={20000} data-testid="memory-content"
            placeholder="Markdown. Say it the way you would tell a new colleague." hint="One fact, procedure or note per memory."
            onChange={(e) => setContent(e.target.value)} />
          <div className="memoryFormRow">
            <Select label="Kind" value={kind} onValueChange={(v) => setKind(v as MemoryKind)}
              options={MEMORY_KINDS.map((k) => ({ value: k, label: KIND_LABEL[k] }))} />
            <Select label="Applies to" value={appliesTo} onValueChange={setAppliesTo}
              options={[{ value: "organization", label: `All of ${org}` }, ...projects.map((p) => ({ value: p.id, label: p.name }))]} />
          </div>
        </fieldset>
        <SettingsSection title="About" actions={canChange ? <Button size="sm" variant="quiet" leadingIcon="plus" onClick={() => setAdding(true)}>Add</Button> : undefined}>
          {about.length ? (
            <ul className="memoryAbout">
              {about.map((r) => (
                <li key={`${r.type}/${r.id}`}>
                  <RefText r={r} />
                  {canChange ? <IconButton size="sm" icon="close" label={`Remove ${r.label ?? r.id}`} onClick={() => setAbout(about.filter((x) => x !== r))} /> : null}
                </li>
              ))}
            </ul>
          ) : (
            <p className="muted">Nothing yet. Search can be narrowed to the tasks, epics and projects a memory is about.</p>
          )}
          {adding ? (
            <AboutPicker client={client} project={appliesTo === "organization" ? undefined : appliesTo}
              onPick={(r) => {
                if (!about.some((x) => x.type === r.type && x.id === r.id)) setAbout([...about, r]);
                setAdding(false);
              }}
              onCancel={() => setAdding(false)} />
          ) : null}
        </SettingsSection>
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
      </div>
    </Dialog>
  );
}

/** Find a task, epic or project to add to "About", by the same search. */
function AboutPicker({ client, project, onPick, onCancel }: {
  client: ApiClient;
  project: string | undefined;
  onPick: (r: MemoryRef) => void;
  onCancel: () => void;
}) {
  const [q, setQ] = useState("");
  const [found, setFound] = useState<readonly SearchResult[]>([]);
  useEffect(() => {
    if (!q.trim()) {
      setFound([]);
      return;
    }
    let live = true;
    const t = setTimeout(() => {
      client.searchMemory({ q, types: "task,epic,project", limit: 8, ...(project ? { project } : {}) }).then(
        (out) => live && setFound(out.results),
        () => undefined,
      );
    }, 200);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [client, q, project]);
  return (
    <div className="memoryPicker">
      <Input size="sm" autoFocus leading={<Icon name="search" size={14} />} placeholder="A task key, an epic, a project"
        aria-label="Find a task, epic or project" value={q} onChange={(e) => setQ(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && (e.stopPropagation(), onCancel())} />
      {found.length ? (
        <ul className="memoryAbout">
          {found.map((r) => (
            <li key={`${r.type}/${r.id}`}>
              <button type="button" className="memoryPick" onClick={() => onPick({ type: r.type as MemoryRef["type"], id: r.id, label: r.type === "task" ? r.key : r.title, status: r.status })}>
                <Lead type={r.type} id={r.id} label={r.type === "task" ? r.key : r.title} status={r.status} />
                <span>{r.title}</span>
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Index
// ---------------------------------------------------------------------------

const KIND_TEXT: Record<string, [string, string]> = {
  memory: ["Memories", "title, content"],
  task: ["Tasks", "key, title, goal, acceptance criteria"],
  epic: ["Epics", "title, description"],
  project: ["Projects", "name, description"],
};

function IndexPage({ client, project, admin, onMemories }: {
  client: ApiClient;
  project: string | undefined;
  admin: boolean;
  onMemories: () => void;
}) {
  const [status, setStatus] = useState<IndexStatus | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const act = useSave();
  const load = useCallback(async () => {
    try {
      setStatus(await client.memoryIndex(project));
      setProblem(null);
    } catch (err) {
      setProblem(errorText(err));
    }
  }, [client, project]);
  useEffect(() => {
    void load();
    // Embedding happens in the background: watch it land.
    const t = setInterval(() => void load(), 10_000);
    return () => clearInterval(t);
  }, [load]);

  const total = useMemo(() => status?.kinds.reduce((n, k) => n + k.total, 0) ?? 0, [status]);
  if (!status) return <div className="centered">{problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />}</div>;
  const failed = status.failures.length;

  return (
    <>
      <SettingsHeader title="Index"
        description="Everything search_memory can find, and the embeddings that let it search by meaning. What is not embedded yet is still found by its words." />
      {problem || act.problem ? <Callout tone="danger">{problem ?? act.problem}</Callout> : null}
      <Card data-testid="memory-embedder">
        <CardHeader title="Embeddings" actions={!status.model ? <Badge>Words only</Badge>
          : failed ? <Badge tone="danger" icon="alert">{failed} not embedded</Badge> : <Badge tone="success" icon="check">Up to date</Badge>} />
        <CardBody>
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
            <Button variant="secondary" onClick={() => setConfirm(true)}>Reindex everything…</Button>
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
                <Td>{KIND_TEXT[k.type]?.[0] ?? k.type}</Td>
                <Td muted>{KIND_TEXT[k.type]?.[1]}</Td>
                <Td align="right" mono>{k.total.toLocaleString("en-US")}</Td>
                <Td align="right" mono>{k.embedded.toLocaleString("en-US")}</Td>
                <Td align="right" mono muted={!k.waiting}>{k.waiting}</Td>
                <Td align="right" mono muted={!k.failed}>{k.failed}</Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      </SettingsSection>

      {failed ? (
        <SettingsSection title="Not embedded"
          actions={<Button size="sm" variant="secondary" leadingIcon="retry" disabled={act.busy}
            onClick={() => void act.save(() => client.retryIndex(), () => void load(), "Retrying now")}>Retry all</Button>}>
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
                    <span className="memoryRef">
                      <Lead type={f.type} id={f.id} />
                      <span className="memoryName">{f.title}</span>
                    </span>
                  </Td>
                  <Td mono muted title={f.error}>{f.error}</Td>
                  <Td align="right" mono>{f.attempts}</Td>
                  <Td align="right" muted>{ago(f.lastTry)}</Td>
                  <Td align="right" fit>
                    <IconButton size="sm" icon="retry" label={`Retry ${f.title}`} disabled={act.busy}
                      onClick={() => void act.save(() => client.retryIndex({ type: f.type, id: f.id }), () => void load(), "Retrying now")} />
                  </Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        </SettingsSection>
      ) : null}

      <Dialog open={confirm} onOpenChange={setConfirm} tone="attention" title="Reindex everything?"
        description={`Embeds all ${total.toLocaleString("en-US")} documents again${status.model ? ` with ${status.model}` : ""}. It takes a few minutes; until each is done, search finds it by its words.`}
        footer={
          <>
            <Button variant="secondary" onClick={() => setConfirm(false)}>Cancel</Button>
            <Button variant="primary" disabled={act.busy}
              onClick={() => void act.save(() => client.reindexMemory(), () => { setConfirm(false); void load(); }, "Reindexing")}>
              Reindex
            </Button>
          </>
        } />
    </>
  );
}

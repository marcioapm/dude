/*
 * The Memory settings page, as proposed. Everything is the design system's
 * own pieces; the few things it does not have yet (a memory glyph, the
 * result row, the author line) are drawn here in its tokens and are the
 * proposal for what it gains once this is approved.
 */

import { useState, type ReactNode } from "react";
import {
  AgentAvatar,
  PersonAvatar,
  ProjectAvatar,
  Segmented,
  SettingRow,
  SettingsHeader,
  SettingsMeta,
  SettingsSection,
  StatusMark,
  MetricGroup,
  MetricTile,
  Markdown,
} from "@dude/design-system/components";
import {
  Badge,
  Button,
  Checkbox,
  Dialog,
  EmptyState,
  Input,
  KeyValueList,
  RowMenu,
  Select,
  Tab,
  TabList,
  TabPanel,
  Tabs,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
  Textarea,
} from "@dude/design-system/primitives";
import { Icon, cx } from "@dude/design-system";
import { AGENT_VIEW, INDEX, MEMORIES, ORG, QUERY, RESULTS, type Author, type DocType, type Memory, type Ref, type Result } from "./data.ts";
import styles from "./memory.module.css";

export type Where = { readonly kind: "org" } | { readonly kind: "project"; readonly project: string };

const PROJECTS = ["control-plane", "web", "runner"];
const project = (name: string) => ({ id: `p_${name}`, name });

/** The proposed `memory` glyph: a page with a folded corner and a bookmark — something kept. */
export function MemoryGlyph({ size = 16 }: { readonly size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round" aria-hidden style={{ flexShrink: 0 }}>
      <path d="M4 2.5h8a.5.5 0 0 1 .5.5v10.5L8 11l-4.5 2.5V3a.5.5 0 0 1 .5-.5ZM6 5.5h4M6 7.5h2.5" />
    </svg>
  );
}

const TYPE_LABEL: Record<DocType, string> = { memory: "Memory", task: "Task", epic: "Epic", project: "Project" };

/** What a search result is, by the grammar the sidebar already uses: a task leads with its status and key, an epic with layers, a project with its face. */
function TypeLead({ r }: { readonly r: Result }) {
  if (r.type === "task" && r.status) {
    return (
      <span className={styles["lead"]}>
        <StatusMark status={r.status} iconOnly size="sm" />
        <span className={styles["key"]}>{r.key}</span>
      </span>
    );
  }
  if (r.type === "epic") return <span className={styles["lead"]}><Icon name="layers" size={14} className={styles["glyph"]} /><span className={styles["typeWord"]}>Epic</span></span>;
  if (r.type === "project") return <span className={styles["lead"]}><ProjectAvatar project={project(r.title)} size={16} /><span className={styles["typeWord"]}>Project</span></span>;
  return <span className={styles["lead"]}><span className={styles["glyph"]}><MemoryGlyph size={14} /></span><span className={styles["typeWord"]}>Memory</span></span>;
}

/** Matched words: stronger ink and weight, never a hue. */
function Snippet({ text }: { readonly text: string }) {
  const parts = text.split(/⟦|⟧/);
  return (
    <span className={styles["snippet"]}>
      {parts.map((p, i) => (i % 2 ? <b key={i}>{p}</b> : <span key={i}>{p}</span>))}
    </span>
  );
}

/** Who wrote it. An agent sits on the face of the person it worked for, with the task it was on. */
export function AuthorLine({ author, size = 20 }: { readonly author: Author; readonly size?: 20 | 24 }) {
  if (author.kind === "person") {
    return (
      <span className={styles["author"]}>
        <PersonAvatar person={author.person} size={size} ring={false} />
        <span className={styles["authorName"]}>{author.person.name.split(" ")[0]}</span>
      </span>
    );
  }
  if (author.kind === "system") {
    return (
      <span className={styles["author"]}>
        <AgentAvatar role="system" size="sm" />
        <span className={styles["authorName"]}>dude</span>
        <span className={styles["authorDetail"]}>{author.what}</span>
      </span>
    );
  }
  return (
    <span className={styles["author"]}>
      <PersonAvatar person={author.for} size={size} ring={false} agent={author.role} />
      <span className={styles["authorName"]}>{roleWord(author.role)}</span>
      <span className={styles["authorDetail"]}>for {author.for.name.split(" ")[0]}</span>
    </span>
  );
}
const roleWord = (r: string) => r.charAt(0).toUpperCase() + r.slice(1).replace("_", " ");

function RefChip({ r }: { readonly r: Ref }) {
  return (
    <span className={styles["ref"]}>
      {r.type === "task" && r.status ? <StatusMark status={r.status} iconOnly size="sm" /> : r.type === "epic" ? <Icon name="layers" size={12} /> : <ProjectAvatar project={project(r.label)} size={14} />}
      <span className={r.type === "task" ? styles["key"] : undefined}>{r.label}</span>
    </span>
  );
}

function Scope({ memory, where }: { readonly memory: Memory; readonly where: Where }) {
  if (memory.project === null) {
    return where.kind === "project" ? (
      <span className={styles["inherited"]}><Icon name="layers" size={12} />From {ORG}</span>
    ) : (
      <span className={styles["scope"]}><Icon name="building" size={14} className={styles["glyph"]} />{ORG}</span>
    );
  }
  return <span className={styles["scope"]}><ProjectAvatar project={project(memory.project)} size={16} />{memory.project}</span>;
}

function IndexState({ state }: { readonly state: Memory["embedded"] }) {
  if (state === "yes") return null;
  return state === "pending" ? (
    <Badge size="sm" icon="clock">Text only</Badge>
  ) : (
    <Badge size="sm" tone="danger" icon="alert">Not embedded</Badge>
  );
}

// ---------------------------------------------------------------------------

export function MemoryPage({ where }: { readonly where: Where }) {
  const [tab, setTab] = useState("search");
  const [editing, setEditing] = useState<Memory | "new" | null>(null);
  const failed = INDEX.kinds.reduce((n, k) => n + k.failed, 0);
  const inScope = MEMORIES.filter((m) => where.kind === "org" || m.project === where.project || m.project === null);
  return (
    <>
      <SettingsHeader
        title="Memory"
        description={
          where.kind === "org"
            ? "What dude and its agents remember, and the index they search. Agents search it with search_memory and add to it with remember; what you see here is what they get."
            : "What this project remembers, and Acme’s memories every project shares. Agents on this project search both."
        }
        actions={<Button variant="primary" leadingIcon="plus" onClick={() => setEditing("new")}>Add memory</Button>}
      />
      <Tabs value={tab} onValueChange={setTab}>
        <TabList aria-label="Memory">
          <Tab value="search" icon="search">Search</Tab>
          <Tab value="memories" count={inScope.filter((m) => !m.archived).length}>Memories</Tab>
          <Tab value="index" count={failed || undefined}>Index</Tab>
        </TabList>
        <TabPanel value="search"><SearchTab where={where} onOpen={(m) => setEditing(m)} /></TabPanel>
        <TabPanel value="memories"><MemoriesTab where={where} memories={inScope} onOpen={(m) => setEditing(m)} /></TabPanel>
        <TabPanel value="index"><IndexTab /></TabPanel>
      </Tabs>
      {editing ? <MemoryDialog memory={editing === "new" ? null : editing} where={where} onClose={() => setEditing(null)} /> : null}
    </>
  );
}

// --------------------------------------------------------------------------- Search

function SearchTab({ where, onOpen }: { readonly where: Where; readonly onOpen: (m: Memory) => void }) {
  const [view, setView] = useState<"ranked" | "agent">("ranked");
  const [types, setTypes] = useState("all");
  const [open, setOpen] = useState<number | null>(0);
  const results = RESULTS.filter((r) => types === "all" || r.type === types);
  return (
    <div className={styles["tab"]}>
      <div className={styles["toolbar"]}>
        <Input className={styles["query"]} leading={<Icon name="search" size={14} />} defaultValue={QUERY} aria-label="Search memory" />
        <Select
          aria-label="What to search"
          value={types}
          onValueChange={setTypes}
          options={[
            { value: "all", label: "Everything" },
            { value: "memory", label: "Memories" },
            { value: "task", label: "Tasks" },
            { value: "epic", label: "Epics" },
            { value: "project", label: "Projects" },
          ]}
        />
        {where.kind === "org" ? (
          <Select aria-label="Project" defaultValue="all" options={[{ value: "all", label: "All projects" }, ...PROJECTS.map((p) => ({ value: p, label: p }))]} />
        ) : (
          <Checkbox defaultChecked label={`Include ${ORG}’s`} />
        )}
      </div>
      <div className={styles["resultsHead"]}>
        <span className={styles["resultsMeta"]}>
          {results.length} results · 184ms · full text and <span className={styles["mono"]}>{INDEX.model}</span>, fused by rank
        </span>
        <Segmented size="sm" label="How to show the results" value={view} onChange={setView} options={[{ value: "ranked", label: "Ranked" }, { value: "agent", label: "As the agent sees it" }]} />
      </div>
      {view === "agent" ? (
        <div className={styles["agentView"]}>
          <p className={styles["agentNote"]}>
            <AgentAvatar role="implementer" size="xs" /> <span className={styles["mono"]}>search_memory(query: "{QUERY}", limit: 5)</span>
          </p>
          <pre className={styles["agentText"]}>{AGENT_VIEW}</pre>
        </div>
      ) : (
        <ol className={styles["results"]}>
          {results.map((r, i) => (
            <ResultRow key={i} r={r} rank={i + 1} top={RESULTS[0]!.fused} open={open === i} onToggle={() => setOpen(open === i ? null : i)} onOpen={onOpen} />
          ))}
        </ol>
      )}
    </div>
  );
}

function ResultRow({ r, rank, top, open, onToggle, onOpen }: { readonly r: Result; readonly rank: number; readonly top: number; readonly open: boolean; readonly onToggle: () => void; readonly onOpen: (m: Memory) => void }) {
  return (
    <li className={cx(styles["result"], open && styles["resultOpen"])}>
      <button type="button" className={styles["resultHead"]} aria-expanded={open} onClick={onToggle}>
        <span className={styles["rank"]}>{rank}</span>
        <span className={styles["resultMain"]}>
          <span className={styles["resultLine"]}>
            <TypeLead r={r} />
            <span className={styles["resultTitle"]}>{r.title}</span>
            {r.project && r.type !== "project" ? <span className={styles["resultProject"]}>{r.project}</span> : null}
          </span>
          <Snippet text={r.snippet} />
        </span>
        <span className={styles["scores"]}>
          <span className={styles["scoreChips"]}>
            <span className={cx(styles["scoreChip"], !r.text && styles["scoreMissing"])} title={r.text ? `Full-text rank ${r.text.rank}` : "Not found by full text"}>
              text {r.text ? `#${r.text.rank}` : "—"}
            </span>
            <span className={cx(styles["scoreChip"], !r.vector && styles["scoreMissing"])} title={r.vector ? `Vector rank ${r.vector.rank}` : r.embedded === "pending" ? "Not embedded yet" : "Not among the nearest"}>
              vector {r.vector ? `#${r.vector.rank}` : "—"}
            </span>
          </span>
          <span className={styles["fused"]} title="Reciprocal-rank fusion: Σ 1/(60 + rank)">
            <span className={styles["fusedValue"]}>{r.fused.toFixed(4)}</span>
            <i className={styles["fusedBar"]}><b style={{ width: `${Math.round((r.fused / top) * 100)}%` }} /></i>
          </span>
        </span>
        <Icon name="chevron-right" size={12} className={styles["chevron"]} />
      </button>
      {open ? (
        <div className={styles["resultBody"]}>
          {r.memory ? (
            <div className={styles["memoryBody"]}>
              <Markdown source={r.memory.content} />
              <div className={styles["memoryFoot"]}>
                <AuthorLine author={r.memory.author} />
                {r.memory.from ? <span className={styles["footItem"]}>learned on <RefChip r={r.memory.from} /></span> : null}
                <span className={styles["footItem"]}>{r.memory.added} ago</span>
                <span className={styles["spacer"]} />
                <Button size="sm" variant="quiet" leadingIcon="edit" onClick={() => onOpen(r.memory!)}>Edit</Button>
              </div>
            </div>
          ) : null}
          <KeyValueList
            className={styles["why"]}
            items={[
              { label: "Full text", value: r.text ? `rank ${r.text.rank} · ts_rank_cd ${r.text.score.toFixed(3)}` : "not matched", mono: true },
              { label: "Vector", value: r.vector ? `rank ${r.vector.rank} · cosine distance ${r.vector.distance.toFixed(3)}` : r.embedded === "pending" ? "not embedded yet — found by full text only" : "not among the 50 nearest", mono: true },
              { label: "Fused", value: `${r.fused.toFixed(4)} = ${[r.text, r.vector].filter(Boolean).map((x) => `1/(60+${x!.rank})`).join(" + ")}`, mono: true },
              { label: "Indexed", value: r.embedded === "yes" ? `${INDEX.model} · ${INDEX.dimensions} dims · 2h ago` : "text now; embedding queued 3m ago", mono: true },
              { label: "Source", value: r.memory ? r.memory.id : r.type === "task" ? `${r.key} · title, goal, acceptance criteria` : r.type === "epic" ? "epic · title, description" : "project · name, description", mono: true },
            ]}
          />
          {!r.memory ? (
            <div className={styles["bodyActions"]}>
              <Button size="sm" variant="secondary" trailingIcon="arrow-right">Open {r.type === "task" ? r.key : TYPE_LABEL[r.type].toLowerCase()}</Button>
            </div>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}

// --------------------------------------------------------------------------- Memories

function MemoriesTab({ where, memories, onOpen }: { readonly where: Where; readonly memories: readonly Memory[]; readonly onOpen: (m: Memory) => void }) {
  const [archived, setArchived] = useState(false);
  const rows = memories.filter((m) => archived || !m.archived);
  return (
    <div className={styles["tab"]}>
      <div className={styles["toolbar"]}>
        <Input size="sm" className={styles["filter"]} leading={<Icon name="search" size={14} />} placeholder="Filter by words" aria-label="Filter memories" />
        {where.kind === "org" ? (
          <Select size="sm" aria-label="Where" defaultValue="all" options={[{ value: "all", label: "Everywhere" }, { value: "org", label: `${ORG}-wide` }, ...PROJECTS.map((p) => ({ value: p, label: p }))]} />
        ) : null}
        <Select size="sm" aria-label="Written by" defaultValue="any" options={[{ value: "any", label: "Anyone" }, { value: "people", label: "People" }, { value: "agents", label: "Agents" }, { value: "dude", label: "dude" }]} />
        <span className={styles["spacer"]} />
        <Checkbox checked={archived} onCheckedChange={(c) => setArchived(c === true)} label="Show archived" />
      </div>
      <Table>
        <THead>
          <Tr>
            <Th>Memory</Th>
            <Th width={160}>{where.kind === "org" ? "Where" : "From"}</Th>
            <Th width={210}>Written by</Th>
            <Th width={120}>Learned on</Th>
            <Th width={64} align="right">Added</Th>
            <Th width={52} />
          </Tr>
        </THead>
        <TBody>
          {rows.map((m) => (
            <Tr key={m.id} interactive className={cx(m.archived && styles["archived"])} onClick={() => onOpen(m)}>
              <Td>
                <span className={styles["memoryCell"]}>
                  <span className={styles["glyph"]}><MemoryGlyph size={14} /></span>
                  <span className={styles["memoryTitle"]}>{m.title}</span>
                  <span className={styles["kind"]}>{m.kind}</span>
                  {m.archived ? <Badge size="sm" icon="archive">Archived</Badge> : null}
                  <IndexState state={m.embedded} />
                </span>
              </Td>
              <Td><Scope memory={m} where={where} /></Td>
              <Td><AuthorLine author={m.author} /></Td>
              <Td>{m.from ? <RefChip r={m.from} /> : <span className={styles["none"]}>—</span>}</Td>
              <Td align="right" muted><span className="ds-tnum">{m.added}</span></Td>
              <Td fit onClick={(e) => e.stopPropagation()}>
                <RowMenu
                  items={[
                    { id: "edit", label: "Edit", icon: "edit" },
                    { id: "copy", label: "Copy id", icon: "copy", shortcut: m.id.slice(0, 8) + "…" },
                    { kind: "separator" },
                    m.archived
                      ? { id: "restore", label: "Restore", icon: "retry" }
                      : where.kind === "project" && m.project === null
                        ? { id: "archive", label: "Archive", icon: "archive", disabled: true, disabledReason: `${ORG}’s memories are archived in ${ORG}’s settings` }
                        : { id: "archive", label: "Archive", icon: "archive" },
                  ]}
                />
              </Td>
            </Tr>
          ))}
        </TBody>
      </Table>
    </div>
  );
}

function MemoryDialog({ memory, where, onClose }: { readonly memory: Memory | null; readonly where: Where; readonly onClose: () => void }) {
  const scopeDefault = memory ? (memory.project ?? "org") : where.kind === "project" ? where.project : "org";
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      size="lg"
      title={memory ? "Memory" : "Add a memory"}
      description={memory ? undefined : "Live as soon as you save it: agents find it the next time they search."}
      footer={
        <>
          {memory ? <Button variant="quiet" leadingIcon="archive">Archive</Button> : null}
          <span style={{ flex: 1 }} />
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={onClose}>{memory ? "Save" : "Add memory"}</Button>
        </>
      }
    >
      <div className={styles["form"]}>
        {memory ? (
          <div className={styles["formMeta"]}>
            <AuthorLine author={memory.author} />
            {memory.from ? <span className={styles["footItem"]}>learned on <RefChip r={memory.from} /></span> : null}
            <span className={styles["footItem"]}>{memory.added} ago</span>
            <span className={styles["spacer"]} />
            <span className={styles["mono"]}>{memory.id}</span>
          </div>
        ) : null}
        <Input label="Title" defaultValue={memory?.title ?? ""} placeholder="One line an agent can scan in a list of results" />
        <Textarea label="What to remember" rows={6} defaultValue={memory?.content ?? ""} placeholder="Markdown. Say it the way you would tell a new colleague." hint="Searched by its words and its meaning. Keep one fact, procedure or note per memory." />
        <div className={styles["formRow"]}>
          <Select label="Kind" defaultValue={memory?.kind ?? "fact"} options={[{ value: "fact", label: "Fact" }, { value: "procedure", label: "Procedure" }, { value: "note", label: "Note" }]} />
          <Select
            label="Where it applies"
            defaultValue={scopeDefault}
            disabled={where.kind === "project" && memory?.project === null}
            options={[{ value: "org", label: `All of ${ORG}` }, ...PROJECTS.map((p) => ({ value: p, label: p }))]}
          />
        </div>
        <div className={styles["aboutField"]}>
          <span className={styles["fieldLabel"]}>About</span>
          <div className={styles["aboutChips"]}>
            {(memory?.about ?? []).map((r, i) => (
              <span key={i} className={styles["aboutChip"]}>
                <RefChip r={r} />
                <button type="button" className={styles["chipRemove"]} aria-label={`Remove ${r.label}`}><Icon name="close" size={12} /></button>
              </span>
            ))}
            <Button size="sm" variant="quiet" leadingIcon="plus">Task, epic or project</Button>
          </div>
          <span className={styles["fieldHint"]}>Search can be narrowed to what a memory is about.</span>
        </div>
      </div>
    </Dialog>
  );
}

// --------------------------------------------------------------------------- Index

function IndexTab() {
  const [confirm, setConfirm] = useState(false);
  const t = INDEX.kinds.reduce((a, k) => ({ total: a.total + k.total, embedded: a.embedded + k.embedded, pending: a.pending + k.pending, failed: a.failed + k.failed }), { total: 0, embedded: 0, pending: 0, failed: 0 });
  return (
    <div className={cx(styles["tab"], styles["tabSections"])}>
      <MetricGroup joined>
        <MetricTile label="Documents" value={t.total} unit="count" sub="memories, tasks, epics, projects" />
        <MetricTile label="Embedded" value={t.embedded} unit="count" sub={`${((t.embedded / t.total) * 100).toFixed(1)}%`} />
        <MetricTile label="Waiting" value={t.pending} unit="count" sub="searchable by text meanwhile" />
        <MetricTile label="Failed" value={t.failed} unit="count" sub="retried with backoff" />
      </MetricGroup>

      <SettingsSection title="Embeddings">
        <SettingRow label="Provider" help="Set where dude is deployed, for every organisation. It becomes an organisation setting later.">
          <span className={styles["provider"]}>{INDEX.provider}<SettingsMeta>DUDE_EMBEDDINGS_PROVIDER</SettingsMeta></span>
        </SettingRow>
        <SettingRow label="Model"><span className={styles["mono"]}>{INDEX.model} · {INDEX.dimensions} dimensions</span></SettingRow>
        <SettingRow label="Endpoint"><span className={styles["mono"]}>{INDEX.endpoint}</span></SettingRow>
      </SettingsSection>

      <SettingsSection title="What is indexed">
        <Table>
          <THead>
            <Tr>
              <Th>Kind</Th>
              <Th>Text</Th>
              <Th align="right" width={110}>Documents</Th>
              <Th align="right" width={110}>Embedded</Th>
              <Th align="right" width={90}>Waiting</Th>
              <Th align="right" width={90}>Failed</Th>
            </Tr>
          </THead>
          <TBody>
            {INDEX.kinds.map((k) => (
              <Tr key={k.type}>
                <Td><span className={styles["scope"]}>{k.type === "memory" ? <span className={styles["glyph"]}><MemoryGlyph size={14} /></span> : k.type === "epic" ? <Icon name="layers" size={14} className={styles["glyph"]} /> : k.type === "task" ? <Icon name="list-check" size={14} className={styles["glyph"]} /> : <Icon name="folder" size={14} className={styles["glyph"]} />}{k.label}</span></Td>
                <Td muted>{k.type === "memory" ? "title, content" : k.type === "task" ? "title, goal, acceptance criteria" : k.type === "epic" ? "title, description" : "name, description"}</Td>
                <Td align="right" mono>{k.total.toLocaleString("en-US")}</Td>
                <Td align="right" mono>{k.embedded.toLocaleString("en-US")}</Td>
                <Td align="right" mono muted={!k.pending}>{k.pending}</Td>
                <Td align="right" mono muted={!k.failed}>{k.failed}</Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      </SettingsSection>

      <SettingsSection title="Not embedded" actions={<Button size="sm" variant="secondary" leadingIcon="retry">Retry all</Button>}>
        {INDEX.failures.length ? (
          <ul className={styles["failures"]}>
            {INDEX.failures.map((f) => (
              <li key={f.key} className={styles["failure"]}>
                <Icon name="alert" size={14} className={styles["failGlyph"]} />
                <span className={styles["failWhat"]}>
                  <span className={styles["failLine"]}>
                    {f.type === "task" ? <span className={styles["key"]}>{f.key}</span> : <span className={styles["glyph"]}><MemoryGlyph size={14} /></span>}
                    <span className={styles["memoryTitle"]}>{f.label}</span>
                  </span>
                  <span className={styles["failError"]}>{f.error}</span>
                </span>
                <span className={styles["failWhen"]}>{f.attempts} {f.attempts === 1 ? "attempt" : "attempts"} · {f.last} ago</span>
                <Button size="sm" variant="quiet" leadingIcon="retry">Retry</Button>
              </li>
            ))}
          </ul>
        ) : (
          <EmptyState compact icon="check" title="Everything is embedded" />
        )}
      </SettingsSection>

      <SettingsSection title="Rebuild">
        <SettingRow label="Reindex everything" help="Embeds every document again: after changing the model, or if search looks wrong. Search keeps working by text while it runs.">
          <Button variant="secondary" onClick={() => setConfirm(true)}>Reindex…</Button>
        </SettingRow>
      </SettingsSection>
      <Dialog
        open={confirm}
        onOpenChange={setConfirm}
        tone="attention"
        title="Reindex everything?"
        description={`Embeds ${t.total.toLocaleString("en-US")} documents again through ${INDEX.provider}. It takes a few minutes; search falls back to text for what is not done yet.`}
        footer={<><Button variant="secondary" onClick={() => setConfirm(false)}>Cancel</Button><Button variant="primary" onClick={() => setConfirm(false)}>Reindex</Button></>}
      />
    </div>
  );
}

export function Spacer({ children }: { readonly children?: ReactNode }) {
  return <span className={styles["spacer"]}>{children}</span>;
}

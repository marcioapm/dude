/*
 * The Memory settings pages, as proposed, composed the way the app's other
 * settings pages are: a page per sub-item in the settings menu, sections
 * and tables like Members and Repositories, a Card of facts like GitHub,
 * and search results that are rows in the FindingRow / ArtifactRow grammar.
 * The one new thing is the memory glyph.
 */

import { useState, type ReactNode } from "react";
import {
  AgentAvatar,
  Markdown,
  PersonLine,
  ProjectAvatar,
  Segmented,
  SettingsHeader,
  SettingsSection,
  StatusMark,
} from "@dude/design-system/components";
import {
  Badge,
  Button,
  Card,
  CardBody,
  CardFooter,
  CardHeader,
  Checkbox,
  Dialog,
  IconButton,
  Input,
  KeyValueList,
  RowMenu,
  Select,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
  Textarea,
} from "@dude/design-system/primitives";
import { Icon, cx } from "@dude/design-system";
import { AGENT_VIEW, INDEX, MEMORIES, ORG, QUERY, RESULTS, type Author, type Memory, type Ref, type Result } from "./data.ts";
import styles from "./memory.module.css";

export type Where = { readonly kind: "org" } | { readonly kind: "project"; readonly project: string };
export type MemoryPageId = "memory-search" | "memory-list" | "memory-index";

const PROJECTS = ["control-plane", "web", "runner"];
const face = (name: string) => ({ id: `p_${name}`, name });

/** The proposed `memory` glyph for the icon set: a page with a bookmark — something kept. */
export function MemoryGlyph({ size = 16, className }: { readonly size?: number; readonly className?: string }) {
  return (
    <svg className={className} width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round" aria-hidden style={{ flexShrink: 0 }}>
      <path d="M4 2.5h8a.5.5 0 0 1 .5.5v10.5L8 11l-4.5 2.5V3a.5.5 0 0 1 .5-.5ZM6 5.5h4M6 7.5h2.5" />
    </svg>
  );
}

export function MemoryPages({ page, where, onPage }: { readonly page: MemoryPageId; readonly where: Where; readonly onPage: (p: MemoryPageId) => void }) {
  const [editing, setEditing] = useState<Memory | "new" | null>(null);
  const add = <Button variant="primary" leadingIcon="plus" onClick={() => setEditing("new")}>Add memory</Button>;
  return (
    <>
      {page === "memory-search" ? <SearchPage where={where} onOpen={setEditing} /> : null}
      {page === "memory-list" ? <MemoriesPage where={where} onOpen={setEditing} add={add} /> : null}
      {page === "memory-index" ? <IndexPage onMemories={() => onPage("memory-list")} /> : null}
      {editing ? <MemoryDialog memory={editing === "new" ? null : editing} where={where} onClose={() => setEditing(null)} /> : null}
    </>
  );
}

// --------------------------------------------------------------------------- shared bits

/** What a thing is, in the sidebar's grammar: a task is its status mark and key, an epic the layers glyph, a project its face. */
function Lead({ type, status, label }: { readonly type: Ref["type"] | "memory"; readonly status?: Ref["status"] | undefined; readonly label?: string | undefined }) {
  if (type === "task" && status) return <span className={styles["lead"]}><StatusMark status={status} iconOnly size="sm" /><span className="ds-mono">{label}</span></span>;
  if (type === "epic") return <span className={styles["glyph"]}><Icon name="layers" size={14} /></span>;
  if (type === "project") return <ProjectAvatar project={face(label ?? "")} size={16} />;
  return <span className={styles["glyph"]}><MemoryGlyph size={14} /></span>;
}

function RefText({ r }: { readonly r: Ref }) {
  return (
    <span className={styles["ref"]}>
      <Lead type={r.type} status={r.status} label={r.label} />
      {r.type === "task" ? null : <span>{r.label}</span>}
    </span>
  );
}

/** A person, or dude, or an agent on the face of the person it worked for — `PersonLine`, as the task header draws people. */
function Writer({ author }: { readonly author: Author }) {
  if (author.kind === "person") return <PersonLine person={author.person} size={24} />;
  if (author.kind === "agent") return <PersonLine person={author.for} size={24} agent={author.role} detail={`${role(author.role)} on ${author.task}`} />;
  return (
    <span className={styles["dude"]}>
      <AgentAvatar role="system" size="md" />
      <span className={styles["dudeText"]}>
        <span className={styles["dudeName"]}>dude</span>
        <span className={styles["dudeDetail"]}>{author.what}</span>
      </span>
    </span>
  );
}
const role = (r: string) => r.charAt(0).toUpperCase() + r.slice(1).replace("_", " ");

function Scope({ memory, where }: { readonly memory: Memory; readonly where: Where }) {
  if (memory.project !== null) return <span className={styles["ref"]}><ProjectAvatar project={face(memory.project)} size={16} />{memory.project}</span>;
  if (where.kind === "project") return <span className={styles["inherited"]}><Icon name="layers" size={12} />From {ORG}</span>;
  return <span className={styles["ref"]}><span className={styles["glyph"]}><Icon name="building" size={14} /></span>All of {ORG}</span>;
}

// --------------------------------------------------------------------------- Search

function SearchPage({ where, onOpen }: { readonly where: Where; readonly onOpen: (m: Memory) => void }) {
  const [view, setView] = useState<"ranked" | "agent">("ranked");
  const [types, setTypes] = useState("all");
  const results = RESULTS.filter((r) => types === "all" || r.type === types);
  return (
    <>
      <SettingsHeader
        title="Search"
        description="What an agent finds when it calls search_memory: memories, tasks, epics and projects, ranked by their words and their meaning together."
      />
      <div className={styles["toolbar"]}>
        <Input className={styles["query"]} leading={<Icon name="search" size={14} />} defaultValue={QUERY} aria-label="Search" />
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
      <SettingsSection
        title={`${results.length} results`}
        actions={<Segmented size="sm" label="How to show the results" value={view} onChange={setView} options={[{ value: "ranked", label: "Ranked" }, { value: "agent", label: "As the agent sees it" }]} />}
      >
        {view === "agent" ? (
          <Markdown source={"```text\n" + AGENT_VIEW + "\n```"} />
        ) : (
          <ol className={styles["list"]}>
            {results.map((r, i) => (
              <ResultRow key={i} r={r} rank={i + 1} defaultOpen={i === 0} onOpen={onOpen} />
            ))}
          </ol>
        )}
      </SettingsSection>
    </>
  );
}

/**
 * One result, as an ArtifactRow is one file: chevron, what it is, its
 * title, and quiet facts at the end — where it lives and which of the two
 * searches found it. Why it ranked where it did is behind the click.
 */
function ResultRow({ r, rank, defaultOpen, onOpen }: { readonly r: Result; readonly rank: number; readonly defaultOpen: boolean; readonly onOpen: (m: Memory) => void }) {
  const [open, setOpen] = useState(defaultOpen);
  const found = r.text && r.vector ? "words and meaning" : r.text ? "words only" : "meaning only";
  return (
    <li className={cx(styles["row"], open && styles["open"])}>
      <div className={styles["rowLine"]}>
        <button type="button" className={styles["head"]} aria-expanded={open} onClick={() => setOpen(!open)}>
          <span className={styles["chevron"]} aria-hidden><Icon name="chevron-right" size={12} className={styles["chevronIcon"]} /></span>
          <span className={styles["rank"]}>{rank}</span>
          <Lead type={r.type} status={r.status} label={r.type === "task" ? r.key : r.title} />
          <span className={styles["title"]}>{r.title}</span>
        </button>
        <span className={styles["trailing"]}>
          {r.embedded === "pending" ? <Badge size="sm" emphasis="subtle" icon="clock">Text only</Badge> : null}
          <span className={styles["fact"]}>{found}</span>
          {r.type !== "project" && r.project ? <span className={styles["fact"]}>{r.project}</span> : null}
        </span>
      </div>
      {open ? (
        <div className={styles["body"]}>
          <p className={styles["snippet"]}>{r.snippet.replace(/⟦|⟧/g, "")}</p>
          <KeyValueList
            items={[
              { label: "Words", value: r.text ? `#${r.text.rank} · ts_rank_cd ${r.text.score.toFixed(3)}` : "not matched", mono: true },
              { label: "Meaning", value: r.vector ? `#${r.vector.rank} · cosine distance ${r.vector.distance.toFixed(3)}` : r.embedded === "pending" ? "not embedded yet" : "not among the 50 nearest", mono: true },
              { label: "Score", value: `${r.fused.toFixed(4)} = ${[r.text, r.vector].filter(Boolean).map((x) => `1/(60+${x!.rank})`).join(" + ")}`, mono: true },
              { label: "Indexed", value: r.embedded === "yes" ? `${INDEX.model} · 2h ago` : "words now · meaning queued 3m ago", mono: true },
            ]}
          />
          <div className={styles["bodyActions"]}>
            {r.memory ? (
              <Button size="sm" variant="secondary" leadingIcon="edit" onClick={() => onOpen(r.memory!)}>Open memory</Button>
            ) : (
              <Button size="sm" variant="secondary" trailingIcon="arrow-right">Open {r.type === "task" ? r.key : r.type}</Button>
            )}
          </div>
        </div>
      ) : null}
    </li>
  );
}

// --------------------------------------------------------------------------- Memories

function MemoriesPage({ where, onOpen, add }: { readonly where: Where; readonly onOpen: (m: Memory) => void; readonly add: ReactNode }) {
  const [archived, setArchived] = useState(false);
  const rows = MEMORIES.filter((m) => (where.kind === "org" || m.project === where.project || m.project === null) && (archived || !m.archived));
  return (
    <>
      <SettingsHeader
        title="Memories"
        description={
          where.kind === "org"
            ? "What people, dude and agents chose to remember. Live as soon as they are added; archive one to take it out of every search."
            : `What control-plane remembers, and ${ORG}’s memories every project shares.`
        }
        actions={add}
      />
      <div className={styles["toolbar"]}>
        <Input size="sm" className={styles["filter"]} leading={<Icon name="search" size={14} />} placeholder="Filter" aria-label="Filter memories" />
        {where.kind === "org" ? (
          <Select size="sm" aria-label="Where" defaultValue="all" options={[{ value: "all", label: "Everywhere" }, { value: "org", label: `All of ${ORG}` }, ...PROJECTS.map((p) => ({ value: p, label: p }))]} />
        ) : null}
        <Select size="sm" aria-label="Written by" defaultValue="any" options={[{ value: "any", label: "Written by anyone" }, { value: "people", label: "People" }, { value: "agents", label: "Agents" }, { value: "dude", label: "dude" }]} />
        <span className={styles["spacer"]} />
        <Checkbox checked={archived} onCheckedChange={(c) => setArchived(c === true)} label="Show archived" />
      </div>
      <Table>
        <THead>
          <Tr>
            <Th>Memory</Th>
            <Th width="16%">Applies to</Th>
            <Th width="22%">Written by</Th>
            <Th align="right" width="48px"><span className="ds-sr-only">Actions</span></Th>
          </Tr>
        </THead>
        <TBody>
          {rows.map((m) => (
            <Tr key={m.id} interactive className={cx(m.archived && styles["archived"])} onClick={() => onOpen(m)}>
              <Td>
                <span className={styles["memory"]}>
                  <span className={styles["glyph"]}><MemoryGlyph size={16} /></span>
                  <span className={styles["memoryText"]}>
                    <span className={styles["memoryName"]}>{m.title}</span>
                    <span className={styles["memoryDetail"]}>
                      {[m.kind === "procedure" ? "Procedure" : m.kind === "fact" ? "Fact" : "Note", m.from ? `learned on ${m.from.label}` : null, `${m.added} ago`].filter(Boolean).join(" · ")}
                    </span>
                  </span>
                  <span className={styles["spacer"]} />
                  {m.archived ? <Badge size="sm" icon="archive">Archived</Badge> : null}
                  {m.embedded === "pending" ? <Badge size="sm" emphasis="subtle" icon="clock">Text only</Badge> : null}
                  {m.embedded === "failed" ? <Badge size="sm" tone="danger" icon="alert">Not embedded</Badge> : null}
                </span>
              </Td>
              <Td><Scope memory={m} where={where} /></Td>
              <Td><Writer author={m.author} /></Td>
              <Td align="right" fit onClick={(e) => e.stopPropagation()}>
                <RowMenu
                  label={`Actions for ${m.title}`}
                  items={[
                    { id: "edit", label: "Edit", icon: "edit", onSelect: () => onOpen(m) },
                    { id: "copy", label: "Copy id", icon: "copy" },
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
    </>
  );
}

function MemoryDialog({ memory, where, onClose }: { readonly memory: Memory | null; readonly where: Where; readonly onClose: () => void }) {
  const scope = memory ? (memory.project ?? "org") : where.kind === "project" ? where.project : "org";
  const [about, setAbout] = useState<readonly Ref[]>(memory?.about ?? []);
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      size="lg"
      title={memory ? "Edit memory" : "Add a memory"}
      description={memory ? undefined : "Live as soon as you add it: agents find it the next time they search."}
      footer={
        <>
          {memory ? <Button variant="quiet" leadingIcon="archive">Archive</Button> : null}
          <span className={styles["spacer"]} />
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={onClose}>{memory ? "Save" : "Add memory"}</Button>
        </>
      }
    >
      <div className={styles["form"]}>
        {memory ? (
          <KeyValueList
            items={[
              { label: "Written by", value: <Writer author={memory.author} /> },
              ...(memory.from ? [{ label: "Learned on", value: <RefText r={memory.from} /> }] : []),
              { label: "Id", value: memory.id, mono: true },
            ]}
          />
        ) : null}
        <Input label="Title" defaultValue={memory?.title ?? ""} placeholder="One line an agent can scan in a list of results" />
        <Textarea label="What to remember" rows={5} defaultValue={memory?.content ?? ""} placeholder="Markdown. Say it the way you would tell a new colleague." hint="One fact, procedure or note per memory." />
        <div className={styles["formRow"]}>
          <Select label="Kind" defaultValue={memory?.kind ?? "fact"} options={[{ value: "fact", label: "Fact" }, { value: "procedure", label: "Procedure" }, { value: "note", label: "Note" }]} />
          <Select
            label="Applies to"
            defaultValue={scope}
            disabled={where.kind === "project" && memory?.project === null}
            options={[{ value: "org", label: `All of ${ORG}` }, ...PROJECTS.map((p) => ({ value: p, label: p }))]}
          />
        </div>
        <SettingsSection title="About" actions={<Button size="sm" variant="quiet" leadingIcon="plus">Add</Button>}>
          {about.length ? (
            <ul className={styles["about"]}>
              {about.map((r, i) => (
                <li key={i}>
                  <RefText r={r} />
                  <IconButton size="sm" icon="close" label={`Remove ${r.label}`} onClick={() => setAbout(about.filter((_, j) => j !== i))} />
                </li>
              ))}
            </ul>
          ) : (
            <p className="muted">Nothing yet. Search can be narrowed to the tasks, epics and projects a memory is about.</p>
          )}
        </SettingsSection>
      </div>
    </Dialog>
  );
}

// --------------------------------------------------------------------------- Index

function IndexPage({ onMemories }: { readonly onMemories: () => void }) {
  const [confirm, setConfirm] = useState(false);
  const total = INDEX.kinds.reduce((n, k) => n + k.total, 0);
  const failed = INDEX.failures.length;
  return (
    <>
      <SettingsHeader title="Index" description="Everything search_memory can find, and the embeddings that let it search by meaning. What is not embedded yet is still found by its words." />
      <Card>
        <CardHeader title="Embeddings" actions={failed ? <Badge tone="danger" icon="alert">{failed} not embedded</Badge> : <Badge tone="success" icon="check">Up to date</Badge>} />
        <CardBody>
          <KeyValueList
            items={[
              { label: "Provider", value: INDEX.provider },
              { label: "Model", value: `${INDEX.model} · ${INDEX.dimensions} dimensions`, mono: true },
              { label: "Endpoint", value: INDEX.endpoint, mono: true },
              { label: "Set by", value: "the deployment (DUDE_EMBEDDINGS_*); an organisation setting later" },
            ]}
          />
        </CardBody>
        <CardFooter>
          <Button variant="secondary" onClick={() => setConfirm(true)}>Reindex everything…</Button>
        </CardFooter>
      </Card>

      <SettingsSection title="What is indexed">
        <Table density="compact">
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
            {INDEX.kinds.map((k) => (
              <Tr key={k.type} interactive={k.type === "memory"} onClick={k.type === "memory" ? onMemories : undefined}>
                <Td>{k.label}</Td>
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

      <SettingsSection title="Not embedded" actions={failed ? <Button size="sm" variant="secondary" leadingIcon="retry">Retry all</Button> : undefined}>
        <Table density="compact">
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
            {INDEX.failures.map((f) => (
              <Tr key={f.key}>
                <Td title={f.label}><span className={styles["docCell"]}>{f.type === "task" ? <span className="ds-mono">{f.key}</span> : <span className={styles["glyph"]}><MemoryGlyph size={14} /></span>}<span className={styles["title"]}>{f.label}</span></span></Td>
                <Td mono muted title={f.error}>{f.error}</Td>
                <Td align="right" mono>{f.attempts}</Td>
                <Td align="right" muted>{f.last} ago</Td>
                <Td align="right" fit><IconButton size="sm" icon="retry" label={`Retry ${f.label}`} /></Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      </SettingsSection>

      <Dialog
        open={confirm}
        onOpenChange={setConfirm}
        tone="attention"
        title="Reindex everything?"
        description={`Embeds all ${total.toLocaleString("en-US")} documents again through ${INDEX.provider}. It takes a few minutes; until each is done, search finds it by its words.`}
        footer={<><Button variant="secondary" onClick={() => setConfirm(false)}>Cancel</Button><Button variant="primary" onClick={() => setConfirm(false)}>Reindex</Button></>}
      />
    </>
  );
}

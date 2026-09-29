# Memory: what dude and its agents remember, and one search over it all

Status: design, 2026-09-28. Mockup: `mockups/memory/index.html` (approved).

## What

People, dude itself and agents store **memories**: short facts, procedures
and notes, as rows. Memories, and every project, epic and task, are indexed
for **search by words and by meaning** together. Agents search with a tool
and get a ranked list; people search, read and curate in the settings, where
the same search explains why each result ranked where it did.

Decided (2026-09-28):

- A memory is **live when it is saved**. No candidates and no approval: a
  bad one is archived. Approval comes later, for what goes into a prompt
  (process, skills), not for memories.
- **Scope is the organization or one project.** No per-person memories.
- **Agents reach memory only through tools.** Nothing is put into a prompt
  on its own.
- **Embeddings are the deployment's**, configured by the operator
  (`DUDE_EMBEDDINGS_*`); an organization setting later. Acme's deployment
  goes through llm-proxy: `gemini-embedding-2` at 768 dimensions.

## The records

`memories` (`mem_…`), a tenant table like any other:

| column | |
|---|---|
| `project_id` | null: the whole organization |
| `title`, `content` | one line to scan in results; Markdown |
| `kind` | `fact` \| `procedure` \| `note` |
| `author_kind` | `person` \| `system` \| `agent` |
| `author_person_id`, `created_by_run_id` | who, for a person or an agent; an agent's run gives the person it worked for and its task |
| `system_reason` | for dude's own: "from an answer" |
| `source_type`, `source_id` | where it was learned: a task, epic, project or run; the run's task by default |
| `archived_at`, `archived_by` | out of every search; restorable |

`memory_refs(memory_id, ref_type, ref_id)` is what a memory is **about**:
any number of tasks, epics and projects. Search can be narrowed to it.
Provenance says where it came from; refs say what it concerns.

## The index

`search_documents`: one row per memory, task, epic and project, derived
from them and rebuildable. Triggers on the four tables keep it in step:

- the text: a memory's title and content; a task's key, title, goal and
  criteria; an epic's title and description; a project's name and
  description;
- `tsv` (Postgres `simple` + `english`), set by the trigger, so a new row is
  found by its words at once;
- `embedding halfvec(768)`, `embedding_model`, `embedded_at`,
  `content_hash`. A change of text clears the embedding; an unchanged one
  keeps it;
- `attempts`, `last_error`, `next_attempt_at` for the indexer.

Archiving a memory deletes its document; restoring puts it back. pgvector
is the first extension beyond `citext`: Postgres is now the
`pgvector/pgvector:pg17-trixie` image (same Debian as `postgres:17`, so no
collation change).

## The indexer

A loop in the orchestrator (`indexer`, like the others in
`dude-orchestrator`): take up to 100 documents without an embedding, across
organizations (`InSystem`), embed them in one call, write them back. A
memory saved wakes it. Without `DUDE_EMBEDDINGS_URL` it does nothing and
search is by words alone.

One rule for failures: **a document is blamed only when its neighbours
embed.** A refusal that may be one text's (a 4xx that is not the key, the
address or a rate limit) is embedded one by one to find it; that document
backs off (1m, 5m, 30m, 2h, then daily) and keeps its error. Anything else
— a bad key, a wrong model, a rate limit, the endpoint down, or every text
refused alike — is the embedder's: the indexer waits as a whole (15s
doubling to at most 10 minutes, so a fix is picked up soon), the Index page
says it is failing and why, and no document's backoff moves.

`Embedder` is one interface with two implementations: OpenAI-compatible
`/v1/embeddings` (what llm-proxy serves) and a deterministic fake for tests.
A model change is a reindex, not a migration: at start, and hourly after,
the indexer clears what another model embedded (hourly, not every sweep,
so an old orchestrator in a rolling deploy cannot undo it for long).

## Search

One function in Go, used by the tool and the settings alike:

1. words: `websearch_to_tsquery`, top 50 by `ts_rank_cd`;
2. meaning: embed the query, top 50 by cosine distance (skipped with no
   embedder, or when the query cannot be embedded);
3. fuse by rank: score = Σ 1 / (60 + rank). A result found both ways
   outranks one found by either alone.

Filters: types, project (a project's search includes its organization's
memories), and `about`. Each result carries both ranks, the text score, the
distance and the fused score, which the settings show and the tool does
not.

## Agent tools

For every role:

| tool | |
|---|---|
| `search_memory(query, types?, limit?)` | the ranked list: type, key or id, title, a snippet. The run's project and its organization |
| `get_memory(id)` | one memory in full, with where it was learned and what it is about |
| `remember(title, content, kind?, about?, scope?)` | a new memory, live at once, by this run: the run's project unless `scope: "organization"`, learned on the run's task |

`remember` has its own budget (20 per run) so a looping agent cannot flood
it. Each call is an `agent.tool.dude` event like every tool; saving also
records `memory.created`.

## API and screens

The orchestrator serves `/internal/memory/…` (search, list, get, create,
update, archive, restore, index status, retry, reindex); the backend
proxies `/v1/memory/…` to it with the principal as the actor, as it does
GitHub settings. Anyone in the organization searches and adds; a person
edits and archives their own; admins any.

Settings, organization's and each project's: **Memory** in the menu, with
three pages: **Search** (ranked, "As the agent sees it"), **Memories** (the
table, add and edit), **Index** (the embedder, what is indexed, what failed,
reindex). A project's shows its own and the organization's, marked "From
Acme", changed only in the organization's settings.

## Configuration

| | |
|---|---|
| `DUDE_EMBEDDINGS_URL` | e.g. `https://llm.absmartly.dev/v1` (…`/embeddings` is appended). Unset: search by words only |
| `DUDE_EMBEDDINGS_KEY` | **Secret.** A virtual key for the deployment, never a person's |
| `DUDE_EMBEDDINGS_MODEL` | default `gemini-embedding-2` |
| `DUDE_EMBEDDINGS_DIMENSIONS` | default `768`; must match the column. Changing it is a migration |

## Order of work

Migration and index → embedder, indexer, search → tools → API → settings
pages and the design system (memory glyph, `SearchResultRow`, the rule that
a page's sub-pages live in the settings menu) → tests at each step.

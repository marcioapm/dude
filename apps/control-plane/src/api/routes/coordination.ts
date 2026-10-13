/**
 * Agents coordinating on an epic (docs/design/agent-coordination.md): its
 * notes, the threads between its tasks' agents, and each task's state as
 * its peers see it.
 *
 * Reading stays here; pinning and retiring a note tell every live agent on
 * the epic, so the orchestrator carries them out. People read every thread
 * and never write in one: there is no route for it. An epic of another
 * organisation is not found (row-level security), as every route here.
 */

import { z } from "zod";
import { EPIC_NOTE_TEXT_MAX, EPIC_NOTES_MAX, type EpicNote, type PeerMessageView, type PeerThread, type PeerWaitView,
  type TaskRef } from "@dude/domain";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { json, notFound, parseBody } from "../http.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import type { RequestContext, Router } from "../router.ts";

/** Live, as delivery counts a Run that can still hear a directive. */
const LIVE = `('pending', 'scheduled', 'starting', 'running', 'paused')`;

/** The most messages a threads read returns, newest first. */
const THREADS_MESSAGES = 1000;

/** The epic, or 404: another organisation's is not found. */
async function epicOr404(scope: OrgScope, epicId: string): Promise<void> {
  if ((await scope.sql`SELECT 1 FROM epics WHERE id = ${epicId}`).length === 0) throw notFound(`epic ${epicId} not found`);
}

const TASK_REF = (alias: string, project: string) =>
  `jsonb_build_object('taskId', ${alias}.id, 'key', ${project}.key_prefix || '-' || ${alias}.number, 'title', ${alias}.title)`;

async function listNotes(ctx: RequestContext): Promise<Response> {
  const epicId = ctx.params.id!;
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    await epicOr404(scope, epicId);
    const rows = (await scope.sql`
      SELECT n.id, n.n, n.text, n.by_label AS "label", n.by_run_id AS "runId", n.by_person AS "personId",
        n.from_session_id AS "fromSessionId", n.created_at AS "createdAt", n.replaced_at AS "replacedAt",
        n.retired_at AS "retiredAt", n.retired_by AS "retiredBy",
        (SELECT p.name FROM people p WHERE p.id = n.retired_by) AS "retiredName",
        (SELECT o.n FROM epic_notes o WHERE o.id = n.replaces_id) AS "replaces",
        (SELECT jsonb_build_object('n', s.n, 'at', s.created_at) FROM epic_notes s WHERE s.replaces_id = n.id) AS "replacedBy",
        (SELECT ${scope.sql.unsafe(TASK_REF("t", "pr"))} FROM runs r JOIN tasks t ON t.id = r.task_id
           JOIN projects pr ON pr.id = t.project_id WHERE r.id = n.by_run_id) AS "task",
        (SELECT count(DISTINCT nr.run_id)::int FROM epic_note_reads nr JOIN runs r ON r.id = nr.run_id
           WHERE nr.note_id = n.id AND nr.read_at IS NOT NULL AND r.status IN ${scope.sql.unsafe(LIVE)}) AS "readBy"
      FROM epic_notes n WHERE n.epic_id = ${epicId} ORDER BY n.n`) as Array<Record<string, any>>;
    const [{ live }] = (await scope.sql`
      SELECT count(*)::int AS live FROM runs r JOIN tasks t ON t.id = r.task_id
      WHERE t.epic_id = ${epicId} AND r.phase IS NOT NULL AND r.kind = 'agent' AND r.status IN ${scope.sql.unsafe(LIVE)}`) as [{ live: number }];
    return { rows, live };
  });
  const notes: EpicNote[] = out.rows.map((r) => ({
    id: r.id, n: r.n, text: r.text, createdAt: r.createdAt,
    by: { kind: r.personId || r.fromSessionId ? "person" : "agent", label: r.label, runId: r.runId, task: (r.task as TaskRef) ?? null,
      personId: r.personId, fromSessionId: r.fromSessionId },
    replaces: r.replaces ?? null,
    replacedBy: r.replacedBy ?? null,
    retired: r.retiredAt ? { by: { personId: r.retiredBy, name: r.retiredName }, at: r.retiredAt } : null,
    readBy: r.readBy,
  }));
  const current = notes.filter((n) => !n.replacedBy && !n.retired);
  const history = notes.filter((n) => n.replacedBy || n.retired).reverse();
  return json({ epicId, limit: EPIC_NOTES_MAX, current, history, live: out.live });
}

const pinInput = z.object({
  text: z.string().trim().min(1).max(EPIC_NOTE_TEXT_MAX),
  replaces: z.number().int().positive().optional(),
}).strict();

/** Pin a note, or replace one: the orchestrator tells the epic's agents. */
async function pinNote(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, pinInput);
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/epics/${encodeURIComponent(ctx.params.id!)}/notes`,
    JSON.stringify(input), ctx.principal);
}

/** Retire a note, with no replacement: a person's alone. */
async function retireNote(ctx: RequestContext): Promise<Response> {
  const n = Number(ctx.params.n);
  if (!Number.isInteger(n) || n < 1) throw notFound(`no note ${ctx.params.n}`);
  return orchestrator(ctx.principal.organizationId, "POST",
    `/internal/epics/${encodeURIComponent(ctx.params.id!)}/notes/${n}/retire`, "{}", ctx.principal);
}

type MessageRow = PeerMessageView & { root: string };

/**
 * The epic's threads: each question and its reply chain, between the two
 * tasks of its first message, newest activity first. A thread waits while
 * a Run's wait on one of its messages is open; it is answered once the
 * task asked has replied in it.
 */
async function listThreads(ctx: RequestContext): Promise<Response> {
  const epicId = ctx.params.id!;
  const { messages, waits } = await withOrg(ctx.principal.organizationId, async (scope) => {
    await epicOr404(scope, epicId);
    const messages = (await scope.sql`
      WITH RECURSIVE onEpic AS (
        SELECT m.* FROM peer_messages m WHERE m.from_task_id IN (SELECT id FROM tasks WHERE epic_id = ${epicId})
        UNION
        SELECT m.* FROM peer_messages m WHERE m.to_task_id IN (SELECT id FROM tasks WHERE epic_id = ${epicId})
        ORDER BY created_at DESC LIMIT ${THREADS_MESSAGES}),
      chain (id, root, depth) AS (
        SELECT id, id, 0 FROM peer_messages WHERE id IN (SELECT id FROM onEpic) AND reply_to IS NULL
        UNION ALL
        SELECT m.id, c.root, c.depth + 1 FROM peer_messages m JOIN chain c ON m.reply_to = c.id WHERE c.depth < 1000)
      SELECT m.id, COALESCE(c.root, m.id) AS root,
        ${scope.sql.unsafe(TASK_REF("f", "fp"))} AS "from", ${scope.sql.unsafe(TASK_REF("t", "tp"))} AS "to",
        m.from_run_id AS "fromRunId", r.role::text AS "fromRole", r.phase::text AS "fromPhase", m.reply_to AS "replyTo",
        m.text, m.created_at AS "sentAt", m.read_at AS "readAt", m.delivered_run_id AS "deliveredRunId",
        m.briefed_run_id AS "briefedRunId", CASE WHEN m.directive_id IS NULL AND m.delivered_run_id IS NULL THEN 'next_run'
          ELSE 'next_step' END AS lands
      FROM onEpic m LEFT JOIN chain c ON c.id = m.id
      JOIN tasks f ON f.id = m.from_task_id JOIN projects fp ON fp.id = f.project_id
      JOIN tasks t ON t.id = m.to_task_id JOIN projects tp ON tp.id = t.project_id
      LEFT JOIN runs r ON r.id = m.from_run_id
      ORDER BY m.created_at`) as MessageRow[];
    const ids = messages.map((m) => m.id);
    const waits = ids.length === 0 ? [] : (await scope.sql`
      SELECT w.run_id AS "runId", w.message_id AS "messageId", ${scope.sql.unsafe(TASK_REF("t", "tp"))} AS "on",
        w.how, w.since, w.parks_at AS "parksAt", w.closed_at AS "closedAt", w.closed_by AS "closedBy"
      FROM peer_waits w JOIN peer_messages m ON m.id = w.message_id JOIN tasks t ON t.id = m.to_task_id
      JOIN projects tp ON tp.id = t.project_id
      WHERE w.message_id = ANY(${scope.sql.array(ids, "text")}::text[]) ORDER BY w.since`) as PeerWaitView[];
    return { messages, waits };
  });
  const byRoot = new Map<string, MessageRow[]>();
  for (const m of messages) byRoot.set(m.root, [...(byRoot.get(m.root) ?? []), m]);
  const rootOf = new Map(messages.map((m) => [m.id, m.root]));
  const threads: PeerThread[] = [];
  for (const [root, chain] of byRoot) {
    const first = chain[0]!;
    const own = waits.filter((w) => rootOf.get(w.messageId) === root);
    const open = own.find((w) => w.closedAt === null) ?? null;
    const answered = chain.some((m) => m.replyTo !== null && m.from.taskId === first.to.taskId);
    threads.push({
      id: root,
      pair: [first.from, first.to],
      state: open ? "waiting" : answered ? "answered" : "open",
      wait: open ?? own.at(-1) ?? null,
      messages: chain.map(({ root: _root, ...m }) => m),
      lastAt: chain.at(-1)!.sentAt,
    });
  }
  threads.sort((a, b) => new Date(b.lastAt).getTime() - new Date(a.lastAt).getTime());
  return json({ epicId, threads, waiting: threads.filter((t) => t.state === "waiting").length });
}

/** Each task on the epic as its peers see it: its phase, live or parked, whom it waits on, its unread messages. */
async function listPeers(ctx: RequestContext): Promise<Response> {
  const epicId = ctx.params.id!;
  const tasks = await withOrg(ctx.principal.organizationId, async (scope) => {
    await epicOr404(scope, epicId);
    return (await scope.sql`
      SELECT t.id AS "taskId", p.key_prefix || '-' || t.number AS key, t.title, t.status::text AS status,
        r.phase, r.role, CASE WHEN r.status = 'paused' THEN 'parked' WHEN r.id IS NOT NULL AND r.status IN ${scope.sql.unsafe(LIVE)}
          THEN 'live' ELSE 'none' END AS run,
        CASE WHEN r.status IN ${scope.sql.unsafe(LIVE)} THEN r.id END AS "runId",
        (SELECT jsonb_build_object('task', ${scope.sql.unsafe(TASK_REF("wt", "wp"))}, 'messageId', w.message_id, 'how', w.how,
            'since', w.since, 'parksAt', w.parks_at)
          FROM peer_waits w JOIN peer_messages m ON m.id = w.message_id JOIN tasks wt ON wt.id = m.to_task_id
          JOIN projects wp ON wp.id = wt.project_id
          WHERE w.run_id = r.id AND w.closed_at IS NULL AND r.status IN ${scope.sql.unsafe(LIVE)}) AS "waitingOn",
          heads.branch, heads.sha AS head,
         COALESCE((SELECT jsonb_agg(f.file ORDER BY f.churn DESC, f.path) FROM (
           SELECT jsonb_build_object('path', f->>'path', 'additions', COALESCE((f->>'additions')::int, 0),
             'deletions', COALESCE((f->>'deletions')::int, 0)) AS file, f->>'path' AS path,
             COALESCE((f->>'additions')::int, 0) + COALESCE((f->>'deletions')::int, 0) AS churn
           FROM run_diffs d CROSS JOIN LATERAL jsonb_array_elements(d.files) f
           WHERE d.run_id = (SELECT dr.id FROM runs dr WHERE dr.task_id = t.id AND dr.phase IS NOT NULL AND dr.kind = 'agent'
             ORDER BY dr.phase IN ('implement', 'fix', 'simplify') DESC,
               dr.status IN ${scope.sql.unsafe(LIVE)} DESC, dr.created_at DESC LIMIT 1)
           ORDER BY churn DESC, path LIMIT 5) f), '[]'::jsonb) AS files,
         (SELECT last.text FROM runs sr CROSS JOIN LATERAL (
           SELECT e.payload->>'text' AS text, e.cursor FROM events e WHERE e.run_id = sr.id AND e.event_type = 'agent.message'
           ORDER BY e.cursor DESC LIMIT 1) last
           WHERE sr.task_id = t.id AND sr.phase IS NOT NULL ORDER BY last.cursor DESC LIMIT 1) AS "lastSaid",
         (SELECT count(*)::int FROM peer_messages m WHERE m.to_task_id = t.id AND m.read_at IS NULL AND m.briefed_run_id IS NULL) AS inbox
      FROM tasks t JOIN projects p ON p.id = t.project_id
      LEFT JOIN LATERAL (SELECT r.id, r.heads, r.phase::text AS phase,
          -- delivery.PromptRoleForPhase in Go; repeated here because the role is read in this one statement
          -- and neither process can import the other's constants.
          (CASE r.phase WHEN 'implement' THEN 'implementer' WHEN 'review' THEN 'reviewer' WHEN 'fix' THEN 'fixer'
            WHEN 'simplify' THEN 'simplifier' WHEN 'test' THEN 'qa_browser' WHEN 'investigate' THEN 'investigator' END) AS role,
          r.status FROM runs r
        WHERE r.task_id = t.id AND r.phase IS NOT NULL AND r.kind = 'agent'
        ORDER BY r.status IN ${scope.sql.unsafe(LIVE)} DESC, r.created_at DESC LIMIT 1) r ON true
             LEFT JOIN LATERAL (
         SELECT COALESCE(
           (SELECT pr.head_sha FROM pull_requests pr WHERE pr.task_id = t.id AND pr.repository_id = repo.id
             AND pr.head_sha IS NOT NULL ORDER BY pr.updated_at DESC LIMIT 1),
           (SELECT h.sha FROM (
             SELECT rr.heads->repo.name->>'sha' AS sha, rr.ended_at AS at FROM runs rr WHERE rr.task_id = t.id AND rr.heads ? repo.name
             UNION ALL
             SELECT cp.heads->repo.name->>'sha', cp.settled_at FROM conductor_publishes cp
               WHERE cp.task_id = t.id AND cp.status = 'published' AND cp.heads ? repo.name) h
             ORDER BY h.at DESC NULLS LAST LIMIT 1), '') AS sha,
           -- delivery.TaskHeads' branch: the newest Run that has heads here, not the latest phase Run.
           (SELECT rr.branch FROM runs rr WHERE rr.task_id = t.id AND rr.heads ? repo.name AND rr.branch IS NOT NULL
             ORDER BY rr.ended_at DESC NULLS LAST LIMIT 1) AS branch
         FROM task_repositories tr JOIN repositories repo ON repo.id = tr.repository_id
         WHERE tr.task_id = t.id ORDER BY repo.name LIMIT 1) heads ON true
       WHERE t.epic_id = ${epicId} ORDER BY p.key_prefix, t.number`) as Array<Record<string, unknown>>;
  });
  for (const task of tasks) {
    const text = String(task.lastSaid ?? "").trim().split("\n\n")[0]!.replace(/\s+/g, " ");
    const chars = Array.from(text);
    task.lastSaid = chars.length > 600 ? `${chars.slice(0, 599).join("")}…` : text || null;
  }
  return json({ epicId, tasks });
}

async function countWaits(ctx: RequestContext): Promise<Response> {
  const epicId = ctx.params.id!;
  const waiting = await withOrg(ctx.principal.organizationId, async (scope) => {
    await epicOr404(scope, epicId);
    const [row] = await scope.sql`SELECT count(*)::int AS waiting FROM tasks t JOIN runs r ON r.task_id = t.id
      JOIN peer_waits w ON w.run_id = r.id AND w.closed_at IS NULL
      WHERE t.epic_id = ${epicId} AND r.status IN ${scope.sql.unsafe(LIVE)}`;
    return row.waiting;
  });
  return json({ epicId, waiting });
}

export function registerCoordinationRoutes(router: Router): void {
  router.get("/v1/epics/:id/notes", listNotes);
  router.post("/v1/epics/:id/notes", pinNote);
  router.post("/v1/epics/:id/notes/:n/retire", retireNote);
  router.get("/v1/epics/:id/threads", listThreads);
  router.get("/v1/epics/:id/waiting", countWaits);
  router.get("/v1/epics/:id/peers", listPeers);
}

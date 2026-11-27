/**
 * `dude.fixtures.run` = proposals-waiting (two tasks to file), proposals-one
 * (one filed), proposals-filed, proposals-other (the second item is Ana's
 * edit), proposals-theirs (the first filed, Ana's edit left) or proposals-reader: `#/sessions/ssn_nightlies` with a brainstorm's
 * card in that state, and Waiting on you with its row while it has items
 * you can file. In the shapes the API returns (orchestrator/internal/api/sessions.go).
 */

import type { PersistedEvent, Proposal, SessionDetail, SessionsList } from "@dude/domain";
import { ORG, PEOPLE, PROJECT, YOU } from "./data.ts";

export const PROPOSALS_SESSION = "ssn_nightlies";
const MIN = 60_000;
const iso = (ago: number) => new Date(Date.now() - ago).toISOString();
const ref = (id: string) => {
  const p = PEOPLE.find((x) => x.id === id)!;
  return { id: p.id, name: p.name, photoUrl: p.photoUrl, online: p.online };
};

/** The case asked for, from `dude.fixtures.run`; null for any other run. */
export function proposalsCase(run: string | null): "waiting" | "one" | "filed" | "other" | "theirs" | "reader" | null {
  const m = /^proposals-(waiting|one|filed|other|theirs|reader)$/.exec(run ?? "");
  return m ? (m[1] as "waiting") : null;
}

const GOAL_1 = "The warehouse nightly's coverage step fails before any test runs: setUpClass inserts a goal into the external analytics table, "
  + "and the warehouse refuses it with \"Cannot insert a NULL value into column copy_index\". The table is cloned with LIKE, which keeps NOT NULL "
  + "but drops the source's DEFAULT 0, so a row inserted without copy_index is refused. Clone it with the source table's defaults, then dispatch "
  + "the nightly on the fix branch to show the step gets past setup.";
const GOAL_2 = "The cache nightly's unsegmented query wrote a fresh cache file after the segmented one instead of reusing it. Find which "
  + "fallback rejected the covering file and fix it where it lives.";

function proposal(c: NonNullable<ReturnType<typeof proposalsCase>>): Proposal {
  const key = PROJECT.key;
  const items: Proposal["items"] = [
    { kind: "task", project: key, title: "Warehouse nightly: clone the goals table with its defaults", goal: GOAL_1,
      acceptanceCriteria: ["The test creates analytics_unit_goals with the source table's column defaults (LIKE … INCLUDING DEFAULTS).",
        "The nightly, dispatched on the fix branch, gets past setUpClass with no copy_index error; the run link is in the pull request.",
        "No change to the production unit_goals schema."] },
    c === "other" || c === "theirs"
      ? { kind: "edit", task: "WEBC-212", after: { goal: GOAL_2 } }
      : { kind: "task", project: key, title: "Cache nightly: find why the unsegmented query re-exported", goal: GOAL_2,
        acceptanceCriteria: ["Re-run only that test and read the engine log to see which fallback ran.",
          "The cause is fixed where it lives, with a regression test; the assertion is not relaxed."] },
  ];
  const filed = (k: string) => ({ filed: true, filedBy: "Márcio Martins", key: k });
  const status: Proposal["status"] = c === "reader"
    ? [{ canFile: false, blockedBy: "reader", why: "readers can't file" }, { canFile: false, blockedBy: "reader", why: "readers can't file" }]
    : c === "filed" ? [filed(`${key}-301`), filed(`${key}-302`)]
    : c === "one" ? [filed(`${key}-301`), { canFile: true }]
    : c === "other" || c === "theirs" ? [c === "theirs" ? filed(`${key}-301`) : { canFile: true }, { canFile: false, blockedBy: "owner", owner: ref("u_ana"), why: "Only Ana Ribeiro can file this: it's their task" }]
    : [{ canFile: true }, { canFile: true }];
  return { id: "prp_nightlies", runId: "run_nightlies", createdAt: iso(4 * MIN), items, status };
}

export function proposalsSession(c: NonNullable<ReturnType<typeof proposalsCase>>): SessionDetail {
  const reader = c === "reader";
  const owner = reader ? "u_ana" : YOU;
  return {
    session: { id: PROPOSALS_SESSION, title: "Warehouse nightlies", titledBy: "agent", createdAt: iso(40 * MIN),
      people: [{ person: ref(owner), role: "owner", accepted: true, becomesOwner: false, open: true },
        ...(reader ? [{ person: ref(YOU), role: "read" as const, accepted: true, becomesOwner: false, open: true }] : [])],
      projects: [{ id: PROJECT.id, key: PROJECT.key, name: PROJECT.name, repositories: [] }],
      run: { id: "run_nightlies", status: "completed", dudePause: null, model: "claude-opus-5-5", modelTier: "Thinker", machine: null, waiting: false },
      runs: ["run_nightlies"], costUsd: 0.42, messages: 2, access: "private", ownedByAdmins: false },
    you: { id: YOU, role: reader ? "read" : "owner", archived: false, member: true },
    proposals: [proposal(c)], question: null, agentMachines: [],
  };
}

export function proposalsList(c: NonNullable<ReturnType<typeof proposalsCase>>): SessionsList {
  const d = proposalsSession(c);
  const p = d.proposals![0]!;
  const kinds = p.items.flatMap((item, i) => (p.status[i]?.canFile ? [item.kind] : []));
  return {
    sessions: [{ id: d.session.id, title: d.session.title, role: d.you.role, createdAt: d.session.createdAt, owner: d.session.people[0]!.person,
      shared: false, projects: d.session.projects, runStatus: "completed", dudePause: null, filed: 0, lastActivityAt: p.createdAt, archived: false }],
    invitations: [], questions: [],
    proposals: kinds.length ? [{ sessionId: d.session.id, title: d.session.title, proposalId: p.id, count: kinds.length, kinds, proposedAt: p.createdAt }] : [],
  };
}

export function proposalsEvents(): PersistedEvent[] {
  const ev = (cursor: number, eventType: string, payload: Record<string, unknown>, actor: PersistedEvent["actor"]): PersistedEvent => ({
    eventId: `evt_prp${cursor}`, cursor: 950_000 + cursor, eventType, occurredAt: iso((10 - cursor) * MIN), organizationId: ORG.id, projectId: null as unknown as string,
    taskId: null as unknown as string, runId: "run_nightlies", sessionId: PROPOSALS_SESSION, workflowRunId: null, actor, source: "orchestrator",
    correlationId: null, causationId: null, payload });
  return [
    ev(1, "chat.message", { text: "The warehouse nightlies are red again. Can you find out why and propose the fixes?" }, { type: "human", id: YOU }),
    ev(2, "agent.message", { text: "I read the last nightly runs and their logs. The warehouse fails before any test runs, on a table cloned without its defaults; the cache nightly re-exports a file it should have reused.\n\nI've put two tasks on the card below for you to file." },
      { type: "agent", id: "brainstorm" }),
  ];
}

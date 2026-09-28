/**
 * View model for the navigation tree — pure functions, no React.
 *
 * The domain hierarchy is Organization → Project → Epic → Task → Run →
 * Session. The tree shows four of those levels and folds Runs into their
 * task: the sessions of the *current* run sit directly under the work
 * item, and earlier attempts fold into one "Attempt n" row each. Retrying
 * is rare enough that it must not cost every task a level.
 *
 * Everything here is keyed on the domain status unions via `triage.ts`, so
 * a new status is a compile error before it can be a blank row.
 */

import type { AgentRole, RunStatus, SessionStatus, TaskStatus } from "@dude/domain";
import type { Person } from "../components/PersonAvatar.tsx";
import type { PrChipPullRequest } from "../components/PrChip.tsx";
import { toMs } from "./useNow.ts";
import {
  EMPTY_TRIAGE_COUNTS,
  TRIAGE_SPECS,
  addTriage,
  sumTriage,
  triageOf,
  type TriageCounts,
  type TriageKind,
} from "../tokens/triage.ts";

// ---------------------------------------------------------------------------
// Input shapes. Deliberately a view model, not the domain records: the app
// joins people, activity and titles before handing this over.
// ---------------------------------------------------------------------------

export interface NavSession {
  readonly id: string;
  readonly role: AgentRole;
  readonly status: SessionStatus;
  /** Short title; defaults to the role label. */
  readonly title?: string | undefined;
  /** What the agent is doing right now (live sessions only). */
  readonly activity?: string | undefined;
  /** Its plan, when known: how far along, and the step it is on. */
  readonly plan?: { readonly done: number; readonly total: number; readonly current?: string | undefined } | undefined;
  readonly children?: ReadonlyArray<NavSession> | undefined;
}

export interface NavRun {
  readonly id: string;
  readonly attempt: number;
  readonly status: RunStatus;
  readonly sessions: ReadonlyArray<NavSession>;
}

export interface NavTask {
  readonly id: string;
  /** Human-facing key ("WI-2481"). Shown muted before the title when present. */
  readonly key?: string | undefined;
  readonly title: string;
  readonly status: TaskStatus;
  /** Humans involved, most relevant first (the one it waits on, then the requester). */
  readonly people?: ReadonlyArray<Person> | undefined;
  /** Attempts in order; the last one is current. */
  readonly runs?: ReadonlyArray<NavRun> | undefined;
  /** When it entered its current status. The board shows time in column. */
  readonly statusSince?: string | number | Date | undefined;
  /** Spend so far across every run, in USD. */
  readonly costUsd?: number | undefined;
  /**
   * Why it waits on a person when no agent is asking ("Review stuck on 2
   * findings"). Shown where it would otherwise say only "waiting for you".
   */
  readonly waitingFor?: string | undefined;
  /** Its pull requests, one per repository it changed, in the order they were opened. */
  readonly pullRequests?: ReadonlyArray<PrChipPullRequest> | undefined;
}

/** What a task that needs you, with no agent asking, says it waits for. */
export function waitingWords(task: NavTask): string {
  return task.waitingFor ?? (task.status === "awaiting_confirmation" ? "plan needs your confirmation" : "waiting for you");
}

export interface NavEpic {
  readonly id: string;
  readonly title: string;
  readonly tasks: ReadonlyArray<NavTask>;
}

export interface NavProject {
  readonly id: string;
  readonly name: string;
  /** Its face: an uploaded image, or initials on `colorSlot` (a hash of the id by default). */
  readonly imageUrl?: string | null | undefined;
  readonly colorSlot?: number | undefined;
  readonly epics?: ReadonlyArray<NavEpic> | undefined;
  /** Tasks with no epic. Listed after the epics. */
  readonly tasks?: ReadonlyArray<NavTask> | undefined;
}

export type NavKind = "project" | "epic" | "task" | "run" | "session";

export interface NavRef {
  readonly kind: NavKind;
  readonly id: string;
}

/** Row key: kind-qualified so a session and a run may share an id space. */
export function navKey(ref: NavRef): string {
  return `${ref.kind}:${ref.id}`;
}

// ---------------------------------------------------------------------------
// Triage roll-up
// ---------------------------------------------------------------------------

function moreUrgent(a: TriageKind, b: TriageKind): TriageKind {
  return TRIAGE_SPECS[a].rank <= TRIAGE_SPECS[b].rank ? a : b;
}

function sessionTriage(s: NavSession): TriageKind {
  let t = triageOf(s.status);
  for (const c of s.children ?? []) t = moreUrgent(t, sessionTriage(c));
  return t;
}

export function currentRun(wi: NavTask): NavRun | null {
  const runs = wi.runs ?? [];
  return runs[runs.length - 1] ?? null;
}

/**
 * A task's bucket is the most urgent of its own status and the
 * sessions of its current run: a `running` task whose reviewer is
 * `awaiting_input` needs you, whatever the macro state says.
 */
export function taskTriage(wi: NavTask): TriageKind {
  let t = triageOf(wi.status);
  const run = currentRun(wi);
  if (run) for (const s of run.sessions) t = moreUrgent(t, sessionTriage(s));
  return t;
}

/** Does any session in this subtree need a person? */
export function sessionSubtreeNeedsYou(s: NavSession): boolean {
  return sessionTriage(s) === "needs_you";
}

export function countTasks(items: ReadonlyArray<NavTask>): TriageCounts {
  let c = EMPTY_TRIAGE_COUNTS;
  for (const wi of items) c = addTriage(c, taskTriage(wi));
  return c;
}

export function epicCounts(e: NavEpic): TriageCounts {
  return countTasks(e.tasks);
}

export function projectCounts(p: NavProject): TriageCounts {
  return sumTriage([...(p.epics ?? []).map(epicCounts), countTasks(p.tasks ?? [])]);
}

export function totalCount(c: TriageCounts): number {
  let n = 0;
  for (const k of Object.keys(c) as TriageKind[]) n += c[k];
  return n;
}

/**
 * The agents of a task's current run that are working now or asking a
 * person — the deepest such in each branch, since an orchestrator
 * "waiting for the implementer" says less than the implementer does. The
 * tree shows only these under a task: the phases that ran live on the
 * task's page.
 */
export function liveSessions(wi: NavTask): NavSession[] {
  const out: NavSession[] = [];
  const walk = (s: NavSession): boolean => {
    let deeper = false;
    for (const c of s.children ?? []) deeper = walk(c) || deeper;
    const live = s.status === "running" || s.status === "awaiting_input";
    if (live && !deeper) out.push(s);
    return live || deeper;
  };
  const run = currentRun(wi);
  if (run) for (const s of run.sessions) walk(s);
  return out;
}

/** Live roles working on a task right now, for the collapsed row. */
export function workingRoles(wi: NavTask): ReadonlyArray<AgentRole> {
  const out: AgentRole[] = [];
  const walk = (s: NavSession) => {
    if (s.status === "running" && !out.includes(s.role)) out.push(s.role);
    for (const c of s.children ?? []) walk(c);
  };
  const run = currentRun(wi);
  if (run) for (const s of run.sessions) walk(s);
  return out;
}

// ---------------------------------------------------------------------------
// Flattening
// ---------------------------------------------------------------------------

export interface NavFilter {
  /** Show only tasks in this bucket (ancestors kept). */
  readonly triage?: TriageKind | null | undefined;
  /** Case-insensitive substring over titles, keys, people, epic and project names. */
  readonly query?: string | undefined;
  /** Only tasks this person is on ("Mine"). */
  readonly person?: string | null | undefined;
}

/** User overrides of the default open state, by row key. */
export type NavOverrides = ReadonlyMap<string, boolean>;

export interface NavRow {
  readonly key: string;
  readonly ref: NavRef;
  /** Indent level for aria-level and the guide. */
  readonly depth: number;
  readonly parentKey: string | null;
  readonly expandable: boolean;
  readonly expanded: boolean;
  /** Set when the filter forced this row open; the chevron is inert. */
  readonly forced: boolean;
  readonly node: NavProject | NavEpic | NavTask | NavRun | NavSession;
  /** Project / epic / task: bucket counts of the tasks inside. */
  readonly counts: TriageCounts | null;
  /** Task / session: its own bucket. */
  readonly triage: TriageKind | null;
  readonly projectId: string;
}

interface Ctx {
  readonly overrides: NavOverrides;
  readonly filter: NavFilter;
  readonly rows: NavRow[];
}

function matches(q: string, ...texts: ReadonlyArray<string | undefined>): boolean {
  if (!q) return true;
  return texts.some((t) => t !== undefined && t.toLowerCase().includes(q));
}

function taskMatches(q: string, wi: NavTask): boolean {
  return matches(q, wi.title, wi.key, ...(wi.people ?? []).map((p) => p.name));
}

function isOpen(ctx: Ctx, key: string, byDefault: boolean, forced: boolean): boolean {
  if (forced) return true;
  return ctx.overrides.get(key) ?? byDefault;
}

/**
 * Default open state — the operator should never have to expand three
 * levels to reach the thing that needs them:
 *   project    open if anything inside is counted (needs you / active / ready / failed)
 *   epic       open if anything inside needs you or is active
 *   task  open only if it needs you, so the asking session is visible
 *   session    open only if a descendant needs you
 *   run        (earlier attempt) closed
 * Anything the user toggles overrides these; the override is per row and
 * survives data refreshes, so a newly arriving urgent item still opens its
 * ancestors unless the user explicitly folded them.
 */
function projectDefaultOpen(c: TriageCounts): boolean {
  return c.needs_you > 0 || c.active > 0 || c.ready > 0 || c.failed > 0;
}
function epicDefaultOpen(c: TriageCounts): boolean {
  return c.needs_you > 0 || c.active > 0;
}

function pushTask(ctx: Ctx, wi: NavTask, depth: number, parentKey: string, projectId: string): void {
  const ref: NavRef = { kind: "task", id: wi.id };
  const key = navKey(ref);
  const triage = taskTriage(wi);
  const live = liveSessions(wi);
  const expandable = live.length > 0;
  const filterOpen = ctx.filter.triage === "needs_you" || ctx.filter.triage === "active";
  const forced = expandable && filterOpen && ctx.filter.triage === triage;
  // What is working now is the point of the row: open unless folded by hand.
  const expanded = expandable && isOpen(ctx, key, true, forced);
  ctx.rows.push({ key, ref, depth, parentKey, expandable, expanded, forced, node: wi, counts: null, triage, projectId });
  if (!expanded) return;
  for (const s of live) {
    const sref: NavRef = { kind: "session", id: s.id };
    ctx.rows.push({ key: navKey(sref), ref: sref, depth: depth + 1, parentKey: key, expandable: false, expanded: false, forced: false, node: s, counts: null, triage: sessionTriage(s), projectId });
  }
}

function visibleTasks(ctx: Ctx, items: ReadonlyArray<NavTask>, q: string): NavTask[] {
  const t = ctx.filter.triage ?? null;
  const person = ctx.filter.person ?? null;
  return items.filter(
    (wi) => (t === null || taskTriage(wi) === t) && (person === null || (wi.people ?? []).some((p) => p.id === person)) && taskMatches(q, wi),
  );
}

/**
 * Flatten the projects into the rows currently visible, applying defaults,
 * user overrides and the filter. Rendering from a flat list keeps keyboard
 * navigation trivial (index arithmetic) and leaves the door open to
 * virtualisation.
 */
export function flattenNav(projects: ReadonlyArray<NavProject>, overrides: NavOverrides, filter: NavFilter = {}): NavRow[] {
  const ctx: Ctx = { overrides, filter, rows: [] };
  const q = (filter.query ?? "").trim().toLowerCase();
  const filtering = q.length > 0 || (filter.triage ?? null) !== null || (filter.person ?? null) !== null;

  for (const p of projects) {
    const pref: NavRef = { kind: "project", id: p.id };
    const pkey = navKey(pref);
    const projectHit = matches(q, p.name);
    // A query that hits a project or epic name keeps everything inside it;
    // otherwise it has to hit the task itself. The triage filter always
    // applies to tasks.
    const epics = (p.epics ?? [])
      .map((e) => ({ epic: e, items: visibleTasks(ctx, e.tasks, projectHit || matches(q, e.title) ? "" : q) }))
      .filter((x) => !filtering || x.items.length > 0);
    const loose = visibleTasks(ctx, p.tasks ?? [], projectHit ? "" : q);
    if (filtering && epics.length === 0 && loose.length === 0) continue;

    const counts = projectCounts(p);
    const forced = filtering;
    const expanded = isOpen(ctx, pkey, projectDefaultOpen(counts), forced);
    ctx.rows.push({ key: pkey, ref: pref, depth: 0, parentKey: null, expandable: true, expanded, forced, node: p, counts, triage: null, projectId: p.id });
    if (!expanded) continue;

    for (const { epic, items } of epics) {
      const eref: NavRef = { kind: "epic", id: epic.id };
      const ekey = navKey(eref);
      const ec = epicCounts(epic);
      const eexp = isOpen(ctx, ekey, epicDefaultOpen(ec), forced);
      ctx.rows.push({ key: ekey, ref: eref, depth: 1, parentKey: pkey, expandable: epic.tasks.length > 0, expanded: eexp, forced, node: epic, counts: ec, triage: null, projectId: p.id });
      if (eexp) for (const wi of items) pushTask(ctx, wi, 2, ekey, p.id);
    }
    for (const wi of loose) pushTask(ctx, wi, 1, pkey, p.id);
  }
  return ctx.rows;
}

/** Keys of every ancestor of `ref`, outermost first; empty if not found. */
export function ancestorKeys(projects: ReadonlyArray<NavProject>, ref: NavRef): string[] {
  const target = navKey(ref);
  const path: string[] = [];
  const inSessions = (list: ReadonlyArray<NavSession>): boolean => {
    for (const s of list) {
      const k = navKey({ kind: "session", id: s.id });
      if (k === target) return true;
      path.push(k);
      if (inSessions(s.children ?? [])) return true;
      path.pop();
    }
    return false;
  };
  const inTasks = (list: ReadonlyArray<NavTask>): boolean => {
    for (const wi of list) {
      const k = navKey({ kind: "task", id: wi.id });
      if (k === target) return true;
      path.push(k);
      const runs = wi.runs ?? [];
      for (const [i, r] of runs.entries()) {
        const isCurrent = i === runs.length - 1;
        if (isCurrent) {
          if (inSessions(r.sessions)) return true;
        } else {
          const rk = navKey({ kind: "run", id: r.id });
          if (rk === target) return true;
          path.push(rk);
          if (inSessions(r.sessions)) return true;
          path.pop();
        }
      }
      path.pop();
    }
    return false;
  };
  for (const p of projects) {
    const pk = navKey({ kind: "project", id: p.id });
    if (pk === target) return [];
    path.push(pk);
    for (const e of p.epics ?? []) {
      const ek = navKey({ kind: "epic", id: e.id });
      if (ek === target) return [...path];
      path.push(ek);
      if (inTasks(e.tasks)) return [...path];
      path.pop();
    }
    if (inTasks(p.tasks ?? [])) return [...path];
    path.pop();
  }
  return [];
}

/** Bucket counts across every project — the sidebar's filter chips. */
export function globalCounts(projects: ReadonlyArray<NavProject>): TriageCounts {
  return sumTriage(projects.map(projectCounts));
}

export interface AttentionItem {
  readonly task: NavTask;
  readonly project: NavProject;
  readonly epic: NavEpic | null;
  /** The session that is asking, if the block is at session level. */
  readonly session: NavSession | null;
}

/** The deepest session waiting on a person, if any. */
export function askingSession(list: ReadonlyArray<NavSession>): NavSession | null {
  for (const s of list) {
    const deeper = askingSession(s.children ?? []);
    if (deeper) return deeper;
    if (s.status === "awaiting_input") return s;
  }
  return null;
}

/**
 * Every task across all projects that needs a person, with enough
 * context to act on it without opening the tree. This is what makes
 * "what needs me" answerable without expanding anything. Oldest wait
 * first (by `statusSince`; unknown last, tree order among equals), so a
 * short list shows the longest-waiting ask, not the one highest in the tree.
 */
export function attentionItems(projects: ReadonlyArray<NavProject>): AttentionItem[] {
  const out: AttentionItem[] = [];
  const consider = (wi: NavTask, project: NavProject, epic: NavEpic | null) => {
    if (taskTriage(wi) !== "needs_you") return;
    const run = currentRun(wi);
    out.push({ task: wi, project, epic, session: run ? askingSession(run.sessions) : null });
  };
  for (const p of projects) {
    for (const e of p.epics ?? []) for (const wi of e.tasks) consider(wi, p, e);
    for (const wi of p.tasks ?? []) consider(wi, p, null);
  }
  const waitingSince = (it: AttentionItem) => toMs(it.task.statusSince) ?? Number.POSITIVE_INFINITY;
  // Stable: equal waits keep tree order. Infinity - Infinity is NaN, hence the guard.
  return out.sort((a, b) => {
    const x = waitingSince(a);
    const y = waitingSince(b);
    return x === y ? 0 : x - y;
  });
}

/** Who a task waits on: its owner, the first of its people. */
export function taskOwner(wi: NavTask): Person | null {
  return wi.people?.[0] ?? null;
}

/**
 * What needs a person, split by whose it is: yours (you own the task, or
 * nobody does, so anyone may act) and others'. With no `you`, all of it
 * is yours.
 */
export function waitingSplit(items: ReadonlyArray<AttentionItem>, you: string | null | undefined): { yours: AttentionItem[]; others: AttentionItem[] } {
  const yours: AttentionItem[] = [];
  const others: AttentionItem[] = [];
  for (const it of items) {
    const owner = taskOwner(it.task);
    if (!you || !owner?.id || owner.id === you) yours.push(it);
    else others.push(it);
  }
  return { yours, others };
}

/** Everyone on a project's tasks, most involved first, each once. */
export function projectPeople(p: NavProject): Person[] {
  const seen = new Map<string, { person: Person; n: number; first: number }>();
  let i = 0;
  const all = [...(p.epics ?? []).flatMap((e) => e.tasks), ...(p.tasks ?? [])];
  for (const wi of all) {
    for (const person of wi.people ?? []) {
      const k = person.id ?? person.name;
      const had = seen.get(k);
      if (had) had.n++;
      else seen.set(k, { person, n: 1, first: i++ });
    }
  }
  return [...seen.values()].sort((a, b) => b.n - a.n || a.first - b.first).map((x) => x.person);
}

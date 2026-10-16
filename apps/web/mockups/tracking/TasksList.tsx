/*
 * The tasks list (tasks-list, tasks-list-view-bugs, tasks-list-archived,
 * archived-view): a project's tasks as dense rows, beside the board, at
 * #/project/:id/tasks?view=<view>. The views are fixed; the filter bar
 * narrows any of them. Search is GET /v1/tasks?q=… with keyset paging.
 */

import { useMemo, useState } from "react";
import { Segmented } from "@dude/design-system/components";
import { Button, EmptyState, Input, RowMenu } from "@dude/design-system/primitives";
import { FINISHED, KIND, PRIORITY, ROLE, TASKS, YOU, laterOf, onShelf, type Kind, type Priority, type Task } from "./data.ts";
import { TaskFilterBar, TaskRow } from "./Marks.tsx";
import { lifecycleMenu } from "./TaskPage.tsx";
import styles from "./pages.module.css";

export type View = "all" | "mine" | "bugs" | "needs" | "later" | "archived" | "closed";

export const VIEWS: Record<View, { readonly label: string; readonly means: string; readonly keep: (t: Task) => boolean; readonly query: string }> = {
  all: { label: "Open", means: "Everything not ended, not archived and not on Later.", keep: (t) => !t.archived && !onShelf(t) && !FINISHED.has(t.status), query: "status=open" },
  mine: { label: "Mine", means: "Open tasks you own.", keep: (t) => !t.archived && !FINISHED.has(t.status) && t.owner.id === YOU.id, query: "status=open&mine=true" },
  bugs: { label: "Bugs", means: "Open bugs, most urgent first.", keep: (t) => !t.archived && t.kind === "bug" && !FINISHED.has(t.status), query: "kind=bug&status=open&sort=priority" },
  needs: { label: "Needs you", means: "An agent of yours is asking, or a plan waits for you.", keep: (t) => !t.archived && (t.status === "awaiting_input" || t.status === "awaiting_confirmation") && t.owner.id === YOU.id, query: "needs=you" },
  later: { label: "Later", means: "On the shelf, its own or its epic's, by the date it comes back.", keep: (t) => onShelf(t), query: "deferred=true&sort=deferred_until" },
  closed: { label: "Closed", means: "Ended: merged, marked done, failed, stopped or closed. Archived ones are under Archived.", keep: (t) => !t.archived && FINISHED.has(t.status), query: "status=done,failed,aborted" },
  archived: { label: "Archived", means: "Out of every other view and count. Unarchive puts one back.", keep: (t) => Boolean(t.archived), query: "archived=only" },
};

const RANK: Record<Priority, number> = { P0: 0, P1: 1, P2: 2, P3: 3 };

export function TasksList({ view, onView, onOpen, kindPreset = null }: { readonly view: View; readonly onView: (v: View) => void; readonly onOpen: (k: string) => void; readonly kindPreset?: Kind | null }) {
  const [kind, setKind] = useState<Kind | null>(kindPreset);
  const [priority, setPriority] = useState<Priority | null>(null);
  const [q, setQ] = useState("");
  const v = VIEWS[view];
  const counts = useMemo(() => Object.fromEntries((Object.keys(VIEWS) as View[]).map((k) => [k, TASKS.filter(VIEWS[k].keep).length])) as Record<View, number>, []);
  const rows = TASKS.filter(v.keep)
    .filter((t) => (kind === null || t.kind === kind) && (priority === null || t.priority === priority))
    .filter((t) => !q || `${t.key} ${t.title}`.toLowerCase().includes(q.toLowerCase()))
    .sort((a, b) => RANK[a.priority] - RANK[b.priority] || a.hoursInStatus - b.hoursInStatus);
  const chips = [
    { id: "kind", label: "Kind", value: kind ? KIND[kind].label : undefined, onClear: () => setKind(null),
      items: [{ kind: "radio" as const, id: "k", label: "Kind", value: kind ?? "any", onValueChange: (x: string) => setKind(x === "any" ? null : (x as Kind)),
        items: [{ id: "any", label: "Any kind" }, ...(Object.keys(KIND) as Kind[]).map((k) => ({ id: k, label: KIND[k].plural }))] }] },
    { id: "priority", label: "Priority", value: priority ?? undefined, onClear: () => setPriority(null),
      items: [{ kind: "radio" as const, id: "p", label: "Priority", value: priority ?? "any", onValueChange: (x: string) => setPriority(x === "any" ? null : (x as Priority)),
        items: [{ id: "any", label: "Any priority" }, ...(Object.keys(PRIORITY) as Priority[]).map((p) => ({ id: p, label: p, description: PRIORITY[p].line }))] }] },
    { id: "owner", label: "Owner", items: [{ id: "ana", label: "Ana Ribeiro" }, { id: "marcio", label: "Márcio Martins" }, { id: "tom", label: "Tom Okafor" }] },
    { id: "epic", label: "Epic", items: [{ id: "b", label: "Billing v2" }, { id: "w", label: "Webhook reliability" }, { id: "a", label: "Usage alerts" }, { id: "n", label: "No epic" }] },
    { id: "repo", label: "Repository", items: [{ id: "cp", label: "acme/control-plane", mono: true }] },
  ];
  const age = (t: Task) => (t.hoursInStatus >= 48 ? `${Math.round(t.hoursInStatus / 24)}d` : `${t.hoursInStatus}h`);
  return (
    <div className={styles["listPage"]} data-testid="tasks-list" data-view={view}>
      <div className={styles["listHead"]}>
        <h1 className={styles["listTitle"]}>control-plane</h1>
        <Segmented size="sm" label="Show as" value="list" onChange={() => undefined}
          options={[{ value: "board", label: "Board" }, { value: "list", label: "List" }]} />
        <span className={styles["grow"]} />
        <Button variant="primary" size="sm" leadingIcon="plus">New task</Button>
      </div>
      <div className={styles["listBar"]}>
        <Segmented size="sm" label="View" value={view} onChange={onView} data-testid="views"
          options={(Object.keys(VIEWS) as View[]).map((k) => ({ value: k, label: `${VIEWS[k].label} ${counts[k]}` }))} />
      </div>
      <div className={styles["listBar"]} style={{ paddingTop: 0 }}>
        <Input className={styles["listSearch"]} size="sm" aria-label="Search tasks" placeholder="Search keys, titles and goals" leading={null} value={q} onChange={(e) => setQ(e.target.value)} />
        <TaskFilterBar chips={chips} />
      </div>
      <p className={styles["listNote"]}>{v.means} <span className="ds-mono">GET /v1/tasks?project=cp&amp;{v.query}{kind ? `&kind=${kind}` : ""}</span></p>
      <div className={styles["listBody"]}>
        {rows.length === 0 ? <EmptyState compact icon="search" title="Nothing here" description={view === "needs" ? "No agent of yours is asking anything." : "No task matches."} /> : (
          <ul className={styles["listRows"]} aria-label={`${v.label} tasks`}>
            {rows.map((t) => {
              const l = laterOf(t);
              return (
                <TaskRow key={t.key} onOpen={onOpen}
                  row={{ key: t.key, title: t.title, status: t.status, kind: t.kind, priority: t.priority, epic: t.epic ? (t.epic === "billing" ? "Billing v2" : t.epic === "webhooks" ? "Webhook reliability" : "Usage alerts") : undefined,
                    owner: t.owner, age: age(t), later: l?.until ?? null, onShelf: onShelf(t), archived: Boolean(t.archived), resolution: t.resolution, duplicateOf: t.duplicateOf }}
                  menu={<RowMenu size="sm" label={`Actions for ${t.key}`} items={lifecycleMenu(t, () => undefined)} />} />
              );
            })}
          </ul>
        )}
        <div className={styles["listFoot"]}>
          <span>{rows.length} shown · by priority, then newest · the next page loads on scroll (cursor on created_at, id)</span>
          {ROLE === "member" ? <span>Close, Later and Archive are the owner's: each menu says whose.</span> : null}
        </div>
      </div>
    </div>
  );
}

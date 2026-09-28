import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Cost } from "./Cost.tsx";
import { PersonAvatarStack, type Person } from "./PersonAvatar.tsx";
import styles from "./EpicCard.module.css";

/** A project's epics by how far along they are: done, in review, in progress, not started. */
export interface EpicLanes {
  readonly done: number;
  readonly review: number;
  readonly progress: number;
  readonly backlog: number;
}

const LANES: ReadonlyArray<{ key: keyof EpicLanes; label: string; className: string }> = [
  { key: "done", label: "done", className: "done" },
  { key: "review", label: "in review", className: "review" },
  { key: "progress", label: "in progress", className: "working" },
  { key: "backlog", label: "backlog", className: "backlog" },
];

export interface EpicSummary {
  readonly id: string;
  readonly title: string;
  readonly description?: string | undefined;
  readonly lanes: EpicLanes;
  readonly tasks: number;
  /** Pull requests by state: open, merged, closed, draft. */
  readonly prs: Readonly<Record<string, number>>;
  readonly people: ReadonlyArray<Person>;
  readonly costUsd: number | null;
  /** "updated 12m ago", "finished Sep 14". */
  readonly when?: ReactNode;
  /** Tasks waiting on a person. */
  readonly needsYou?: number | undefined;
}

/** How far an epic's tasks are, as a bar by lane with a legend: every lane has a word, not only a colour. */
export function EpicProgress({ lanes }: { readonly lanes: EpicLanes }) {
  const total = LANES.reduce((n, l) => n + lanes[l.key], 0);
  return (
    <div className={styles["progress"]}>
      <div className={styles["bar"]} role="img" aria-label={LANES.map((l) => `${lanes[l.key]} ${l.label}`).join(", ")}>
        {total === 0 ? <i className={styles["backlog"]} style={{ flex: 1 }} /> : null}
        {LANES.map((l) => (lanes[l.key] ? <i key={l.key} className={styles[l.className]} style={{ flex: lanes[l.key] }} /> : null))}
      </div>
      <div className={styles["legend"]}>
        {LANES.map((l) => (
          <span key={l.key}>
            <i className={styles[l.className]} />
            {lanes[l.key]} {l.label}
          </span>
        ))}
      </div>
    </div>
  );
}

/** Pull requests, by the states that matter here: merged and still open. */
export function EpicPullRequests({ prs }: { readonly prs: Readonly<Record<string, number>> }) {
  const open = (prs["open"] ?? 0) + (prs["draft"] ?? 0);
  const merged = prs["merged"] ?? 0;
  if (!open && !merged) return null;
  return (
    <span className={styles["prs"]}>
      {merged ? (
        <span className={styles["merged"]}>
          <Icon name="merge" size={12} /> {merged} merged
        </span>
      ) : null}
      {open ? (
        <span className={styles["open"]}>
          <Icon name="git-pr" size={12} /> {open} open
        </span>
      ) : null}
    </span>
  );
}

export interface EpicCardProps extends Omit<HTMLAttributes<HTMLDivElement>, "title"> {
  readonly epic: EpicSummary;
  readonly onOpen?: (() => void) | undefined;
  /** In the corner: its state control, a menu. */
  readonly actions?: ReactNode;
}

/** An epic being worked on: how far along, its pull requests, its people and cost. */
export function EpicCard({ epic, onOpen, actions, className, ...rest }: EpicCardProps) {
  return (
    <article className={cx(styles["card"], className)} data-epic={epic.id} {...rest}>
      <div className={styles["head"]}>
        <button type="button" className={styles["title"]} onClick={onOpen}>
          {epic.title}
        </button>
        {epic.needsYou ? (
          <span className={styles["needs"]}>
            <Icon name="hand" size={12} /> {epic.needsYou} {epic.needsYou === 1 ? "needs you" : "need you"}
          </span>
        ) : null}
        <span className={styles["spacer"]} />
        {actions}
      </div>
      {epic.description ? <p className={styles["description"]}>{epic.description}</p> : null}
      <EpicProgress lanes={epic.lanes} />
      <EpicPullRequests prs={epic.prs} />
      <div className={styles["foot"]}>
        <span>
          {epic.tasks} {epic.tasks === 1 ? "task" : "tasks"}
        </span>
        <Cost tokensUsd={epic.costUsd} size="sm" tone="secondary" />
        {epic.when ? <span>{epic.when}</span> : null}
        <span className={styles["spacer"]} />
        <PersonAvatarStack people={epic.people} size={24} max={5} />
      </div>
    </article>
  );
}

export interface EpicRowProps extends Omit<HTMLAttributes<HTMLDivElement>, "title"> {
  readonly epic: EpicSummary;
  readonly onOpen?: (() => void) | undefined;
  /** Under the title: "4 tasks · $2.70", "no tasks yet". */
  readonly detail?: ReactNode;
  readonly actions?: ReactNode;
}

/** An epic in a quiet list: planned ones, and done ones. */
export function EpicRow({ epic, onOpen, detail, actions, className, ...rest }: EpicRowProps) {
  return (
    <div className={cx(styles["row"], className)} data-epic={epic.id} {...rest}>
      <div className={styles["rowText"]}>
        <span className={styles["rowTitleLine"]}>
          <button type="button" className={styles["rowTitle"]} onClick={onOpen}>
            {epic.title}
          </button>
          <EpicPullRequests prs={epic.prs} />
        </span>
        {detail ? <small className={styles["rowDetail"]}>{detail}</small> : null}
      </div>
      <span className={styles["rowWhen"]}>{epic.when}</span>
      {epic.people.length ? <PersonAvatarStack people={epic.people} size={20} max={4} /> : <span />}
      {actions}
    </div>
  );
}

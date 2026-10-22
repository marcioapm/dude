/**
 * What a stop would stop, at the top of its confirmation (Stop, Close…):
 * one sentence, then a face per live Run with who it is and what it is
 * doing. Those at work wear the live ring; one queued, or a conductor
 * between turns, does not.
 */

import type { ReactNode } from "react";
import type { AgentRole } from "@dude/domain";
import { AgentAvatar } from "./AgentAvatar.tsx";
import { cx } from "../util/cx.ts";
import styles from "./LiveWork.module.css";

export type LiveState = "working" | "queued" | "waiting";

export interface LiveItem {
  readonly id: string;
  readonly role: AgentRole;
  /** "Conductor", "Reviewer", "Fixer". */
  readonly who: string;
  /** "deciding after review", "security", "between turns". */
  readonly doing: string;
  /** Default "working"; only "working" gets the live ring. */
  readonly state?: LiveState | undefined;
}

export interface LiveWorkProps {
  readonly summary: ReactNode;
  readonly items: readonly LiveItem[];
  readonly className?: string | undefined;
}

export function LiveWork({ summary, items, className }: LiveWorkProps) {
  return (
    <div className={cx(styles["root"], className)} data-testid="live-work">
      <p className={styles["summary"]}>{summary}</p>
      <ul className={styles["list"]} aria-label="Live now">
        {items.map((i) => {
          const state = i.state ?? "working";
          return (
            <li key={i.id} className={styles["item"]} data-state={state}>
              <AgentAvatar role={i.role} size="sm" live={state === "working"} />
              <span className={styles["who"]}>{i.who}</span>
              <span className={styles["doing"]}>{i.doing}</span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/** One group's phrase: "the conductor", "a reviewer", "2 reviewers", "a queued fixer", "an implementer". */
function phrase(role: AgentRole, who: string, n: number, queued: boolean): string {
  if (role === "conductor") return "the conductor";
  const noun = `${queued ? "queued " : ""}${who.toLowerCase()}`;
  if (n > 1) return `${n} ${noun}s`;
  return `${/^[aeiou]/.test(noun) ? "an" : "a"} ${noun}`;
}

/** "the conductor, 2 reviewers and a queued fixer" — the sentence a stop or a close names. */
export function liveWords(items: readonly LiveItem[]): string {
  const groups = new Map<string, { role: AgentRole; who: string; n: number; queued: boolean }>();
  for (const i of items) {
    const queued = i.state === "queued";
    const key = `${i.role}:${i.who}:${queued}`;
    const g = groups.get(key);
    if (g) g.n++;
    else groups.set(key, { role: i.role, who: i.who, n: 1, queued });
  }
  const parts = [...groups.values()].sort((a, b) => Number(b.role === "conductor") - Number(a.role === "conductor") || Number(a.queued) - Number(b.queued))
    .map((g) => phrase(g.role, g.who, g.n, g.queued));
  if (parts.length <= 1) return parts[0] ?? "nothing";
  return `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}`;
}

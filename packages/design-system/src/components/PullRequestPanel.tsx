import type { HTMLAttributes, ReactNode } from "react";
import { prCheckFailed, prChecksSummary, prReviewSummary } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { formatDuration } from "../util/format.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { PrChip, type PrChipPullRequest } from "./PrChip.tsx";
import styles from "./PullRequestPanel.module.css";

export interface PullRequestPanelProps extends Omit<HTMLAttributes<HTMLElement>, "title"> {
  readonly pr: PrChipPullRequest & { readonly title: string; readonly headBranch?: string | undefined };
  /** Lines added and removed, when known. */
  readonly additions?: number | undefined;
  readonly deletions?: number | undefined;
  /** At the foot: what can be done (Open on GitHub; merge, once dude can). */
  readonly actions?: ReactNode;
  /** A face for a reviewer's login, when the app knows who that is. */
  readonly face?: ((login: string) => ReactNode) | undefined;
  /** Under the actions, muted: why it waits, in words. */
  readonly note?: ReactNode;
  /**
   * An action at the end of a fact's line, as GitHub's page puts one: "Re-run
   * failed" by the checks, "Update branch" by how it stands against its
   * base, "show" by the threads, "Request review" after the reviewers.
   */
  readonly factActions?: Partial<Record<FactKind, ReactNode>> | undefined;
}

/** The facts a panel lists, by what they are about. */
export type FactKind = "checks" | "reviews" | "base" | "threads";

type Tone = "ok" | "bad" | "attention" | "neutral";

interface Fact {
  readonly kind?: FactKind;
  readonly tone: Tone;
  readonly glyph: IconName;
  readonly text: ReactNode;
  readonly trailing?: ReactNode;
  readonly children?: ReactNode;
}

/**
 * A pull request as a panel: its number and title linking to GitHub, its
 * one state, where it goes, and what is true of it — checks (by name when
 * the forge reports them), reviews (by person when it does), how it stands
 * against its base, open threads. What is not known is not said.
 */
export function PullRequestPanel({ pr, additions, deletions, actions, face, note, factActions, className, ...rest }: PullRequestPanelProps) {
  const facts: Fact[] = [];
  if (pr.state === "merged") facts.push({ tone: "ok", glyph: "merge", text: "Merged" });

  // Checks: by name when the forge sent them, else the one word.
  if (typeof pr.checks !== "string") {
    const failing = pr.checks.filter(prCheckFailed);
    const done = pr.checks.filter((c) => c.status.toLowerCase() === "completed").length;
    // None yet on this head (a fix just pushed): CI has not reported, which
    // is not passing.
    const none = pr.checks.length === 0;
    const summary = none
      ? "No checks reported on this commit yet"
      : failing.length > 0
        ? `${failing.length} of ${pr.checks.length} checks failing`
        : done < pr.checks.length
          ? `Checks running · ${done} of ${pr.checks.length} done`
          : `All ${pr.checks.length} checks passing`;
    facts.push({
      kind: "checks",
      tone: none ? "neutral" : failing.length > 0 ? "bad" : done < pr.checks.length ? "attention" : "ok",
      glyph: none ? "circle" : failing.length > 0 ? "circle-x" : done < pr.checks.length ? "circle-dotted" : "circle-check",
      text: <b>{summary}</b>,
      children:
        pr.checks.length > 0 ? (
          <ul className={styles["checks"]}>
            {pr.checks.map((c) => {
              const bad = prCheckFailed(c);
              const running = c.status.toLowerCase() !== "completed";
              return (
                <li key={c.name}>
                  <Icon name={bad ? "cross" : running ? "circle-dotted" : "check"} size={12} className={cx(styles["glyph"], styles[bad ? "bad" : running ? "attention" : "ok"])} />
                  <span className={styles["checkName"]}>{c.name}</span>
                  {c.durationMs ? <span className={styles["muted"]}>{formatDuration(c.durationMs)}</span> : null}
                  {c.url ? (
                    <a href={c.url} target="_blank" rel="noreferrer">
                      log
                    </a>
                  ) : null}
                </li>
              );
            })}
          </ul>
        ) : undefined,
    });
  } else {
    const words = { failing: ["bad", "circle-x", "Checks failing"], pending: ["attention", "circle-dotted", "Checks running"], passing: ["ok", "circle-check", "Checks passing"], unknown: ["neutral", "circle", "No checks reported yet"] } as const;
    const [tone, glyph, text] = words[prChecksSummary(pr.checks)];
    if (pr.state !== "merged" || tone !== "neutral") facts.push({ kind: "checks", tone, glyph, text });
  }

  // Reviews: by person when the forge sent them, else the verdict.
  const latest = new Map<string, string>();
  for (const r of [...(pr.reviews ?? [])].sort((a, b) => (a.submittedAt ?? "").localeCompare(b.submittedAt ?? ""))) {
    if (r.state.toUpperCase() !== "COMMENTED") latest.set(r.login, r.state.toUpperCase());
  }
  if (latest.size > 0) {
    for (const [login, state] of latest) {
      const changes = state === "CHANGES_REQUESTED";
      const approved = state === "APPROVED";
      facts.push({
        kind: "reviews",
        tone: changes ? "bad" : approved ? "ok" : "neutral",
        glyph: changes ? "file-diff" : approved ? "check" : "eye",
        text: (
          <span className={styles["who"]}>
            {face?.(login)}
            <b>{login}</b> {changes ? "requested changes" : approved ? "approved" : state === "REQUESTED" ? "· review requested" : "dismissed their review"}
          </span>
        ),
      });
    }
  } else if (pr.state === "open" || pr.state === "draft") {
    const review = prReviewSummary(pr);
    facts.push(
      review === "approved"
        ? { kind: "reviews", tone: "ok", glyph: "check", text: "Approved" }
        : review === "changes_requested"
          ? { kind: "reviews", tone: "bad", glyph: "file-diff", text: "Changes requested" }
          : { kind: "reviews", tone: "neutral", glyph: "eye", text: "No review yet" },
    );
  }

  if (pr.state === "open" || pr.state === "draft") {
    const base = pr.baseBranch ?? "its base";
    if (pr.unresolvedThreads) facts.push({ kind: "threads", tone: "attention", glyph: "comments", text: `${pr.unresolvedThreads} unresolved comment${pr.unresolvedThreads === 1 ? "" : "s"}` });
    if (pr.mergeable === "conflicting") facts.push({ kind: "base", tone: "bad", glyph: "git-conflict", text: `Conflicts with ${base}` });
    else if (pr.behindBy) facts.push({ kind: "base", tone: "neutral", glyph: "arrow-up", text: `${pr.behindBy} commit${pr.behindBy === 1 ? "" : "s"} behind ${base} · no conflicts` });
    else if (pr.mergeable === "clean") facts.push({ kind: "base", tone: "ok", glyph: "arrow-up", text: `Up to date with ${base}` });
  }

  return (
    <section className={cx(styles["root"], className)} aria-label={`Pull request #${pr.number}`} {...rest}>
      <header className={styles["head"]}>
        <Icon name="github" size={16} className={styles["gh"]} />
        <a className={styles["title"]} href={pr.url} target="_blank" rel="noreferrer">
          #{pr.number} {pr.title}
        </a>
        <PrChip pr={pr} showNumber={false} />
      </header>
      <div className={cx(styles["where"], "ds-mono")}>
        {[pr.repositoryName, pr.headBranch && pr.baseBranch ? `${pr.headBranch} → ${pr.baseBranch}` : pr.headBranch].filter(Boolean).join(" · ")}
        {additions !== undefined && deletions !== undefined ? (
          <>
            {" · "}
            <span className={styles["add"]}>+{additions}</span> <span className={styles["del"]}>−{deletions}</span>
          </>
        ) : null}
      </div>
      <ul className={styles["facts"]}>
        {facts.map((f, i) => (
          <li key={i} data-fact={f.kind}>
            <span className={styles["fact"]}>
              <Icon name={f.glyph} size={14} className={cx(styles["glyph"], styles[f.tone])} />
              <span className={styles["factText"]}>{f.text}</span>
              {f.trailing}
              {/* A kind's action sits on its last line: after every reviewer, not each. */}
              {f.kind && facts.findLastIndex((g) => g.kind === f.kind) === i ? factActions?.[f.kind] : null}
            </span>
            {f.children}
          </li>
        ))}
      </ul>
      {actions ? <div className={styles["actions"]}>{actions}</div> : null}
      {note ? <p className={styles["note"]}>{note}</p> : null}
    </section>
  );
}

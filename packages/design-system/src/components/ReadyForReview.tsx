import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Button } from "../primitives/Button.tsx";
import { RefusedButton } from "../primitives/RefusedButton.tsx";
import { Callout } from "../primitives/Layout.tsx";
import { DiffStat } from "./DiffStat.tsx";
import { Duration } from "./Numbers.tsx";
import styles from "./ReadyForReview.module.css";

/** One repository a held delivery would open a pull request in. */
export interface HeldRepository {
  readonly name: string;
  readonly head: string;
  readonly base: string;
  readonly additions: number;
  readonly deletions: number;
}

/**
 * Whose move it is at the hold. `on` is the owner's first name; `items` the
 * feedback items a fixer still has (0: handled, the round is being reviewed
 * and tested again). `waiting` with `sentBack`: the owner's feedback is with
 * a fixer, as anyone else reads it.
 */
export type ReadyForReviewState =
  | { readonly kind: "yours" }
  | { readonly kind: "waiting"; readonly on: string; readonly sentBack?: boolean | undefined }
  | { readonly kind: "sent-back"; readonly items: number; readonly openRefused: string };

/** A round after the owner's feedback: commits since the hold was last shown, and what it resolved. */
export interface ReadyForReviewRound {
  readonly n: number;
  /** Null when GitHub could not count them. */
  readonly commitsSince: number | null;
  readonly onRange?: (() => void) | undefined;
  readonly settled: ReadonlyArray<{ readonly id: string; readonly text: ReactNode; readonly commit: string | null }>;
}

export interface ReadyForReviewProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  /** After the bold title: what passed, and that no pull request is open. */
  readonly line: ReactNode;
  readonly repositories: ReadonlyArray<HeldRepository>;
  /** A ReviewEvidence. */
  readonly evidence?: ReactNode;
  readonly state: ReadyForReviewState;
  readonly round?: ReadyForReviewRound | undefined;
  readonly heldMs?: number | undefined;
  readonly onChanges?: ((repository: string) => void) | undefined;
  readonly onOpen?: (() => void) | undefined;
  readonly onDraft?: (() => void) | undefined;
  /** The owner's Send feedback (Show feedback while sent back); anyone else's Write feedback. */
  readonly onFeedback?: (() => void) | undefined;
  /** The quiet Done without a pull request, only while `yours`. The app confirms first. */
  readonly onDoneWithout?: (() => void) | undefined;
  /** Which of Open and Draft is in flight. */
  readonly busy?: "open" | "draft" | null | undefined;
}

const commits = (n: number) => `+${n} ${n === 1 ? "commit" : "commits"} since you last looked`;

/**
 * A delivery held before its pull request, in the task's notices where an
 * escalation would be, in its grammar: what would be opened, what was
 * checked, and the owner's answers. Attention while the owner can act,
 * info while a fixer works on their feedback, neutral for anyone else.
 */
export function ReadyForReview({
  line, repositories, evidence, state, round, heldMs, onChanges, onOpen, onDraft, onFeedback, onDoneWithout, busy, className, ...rest
}: ReadyForReviewProps) {
  const sentBack = state.kind === "sent-back";
  const withFixer = sentBack || (state.kind === "waiting" && state.sentBack === true);
  const tone = state.kind === "yours" ? "attention" : sentBack ? "info" : "neutral";
  return (
    <Callout tone={tone} className={cx(styles["root"], className)} data-state={state.kind} {...rest}>
      <div className={styles["body"]}>
        <div className={styles["head"]}>
          <p className={styles["lead"]}>
            {state.kind === "sent-back" ? (
              state.items > 0 ? (
                <>
                  <strong>Sent back:</strong> a fixer is working on your {state.items} {state.items === 1 ? "item" : "items"}. The hold comes
                  back at its new heads, reviewed and tested again.
                </>
              ) : (
                <>
                  <strong>Sent back:</strong> your feedback is handled, and the round is being reviewed and tested again. The hold comes
                  back at its new heads.
                </>
              )
            ) : state.kind === "waiting" && state.sentBack ? (
              <>
                <strong>{state.on}'s feedback is with a fixer.</strong> The hold comes back at its new heads, reviewed and tested again.
              </>
            ) : (
              <>
                <strong>{state.kind === "waiting" ? `Ready for ${state.on}'s review.` : "Ready for your review."}</strong> {line}
              </>
            )}
          </p>
          {heldMs !== undefined && !withFixer ? (
            <span className={styles["age"]}>held <Duration ms={heldMs} format="age" tone="muted" dot={false} /></span>
          ) : null}
        </div>
        {round ? (
          <div className={styles["round"]} data-round={round.n}>
            <span className={styles["roundTag"]}><Icon name="retry" size={12} /> Round {round.n} · after your feedback</span>
            {round.commitsSince === null ? null : round.onRange ? (
              <button type="button" className={styles["link"]} onClick={round.onRange}>{commits(round.commitsSince)}</button>
            ) : (
              <span className={styles["muted"]}>{commits(round.commitsSince)}</span>
            )}
          </div>
        ) : null}
        {round && round.settled.length > 0 ? (
          <ul className={styles["settled"]} aria-label="Your feedback, resolved">
            {round.settled.map((s) => (
              <li key={s.id} className={styles["settledRow"]}>
                <Icon name="check" size={12} className={styles["ok"]} />
                <span className={styles["settledText"]}>{s.text}</span>
                <span className={styles["muted"]}>
                  {s.commit ? <>resolved in <code className={styles["sha"]}>{s.commit}</code></> : "resolved"}
                </span>
              </li>
            ))}
          </ul>
        ) : null}
        <ul className={styles["repos"]} aria-label="What would be opened">
          {repositories.map((r) => (
            <li key={r.name} className={styles["repo"]}>
              <Icon name="git-branch" size={14} className={styles["repoIcon"]} />
              <b className={styles["repoName"]}>{r.name}</b>
              <span className={styles["refs"]} title={`${r.head} into ${r.base}`}>
                <span className={styles["ref"]}>{r.head}</span>
                <span aria-label="into" className={styles["arrow"]}>→</span>
                <span className={styles["ref"]}>{r.base}</span>
              </span>
              <DiffStat additions={r.additions} deletions={r.deletions} className={styles["stat"]} />
              {onChanges ? (
                <button type="button" className={styles["link"]} onClick={() => onChanges(r.name)} aria-label={`Changes in ${r.name}`}>Changes</button>
              ) : null}
            </li>
          ))}
        </ul>
        {evidence}
        {state.kind === "waiting" ? (
          <div className={styles["actions"]}>
            <p className={styles["waiting"]}>Waiting for {state.on}…</p>
            {onFeedback ? <Button size="sm" variant="quiet" leadingIcon="comments" onClick={onFeedback}>Write feedback</Button> : null}
          </div>
        ) : (
          <div className={styles["actions"]}>
            {state.kind === "sent-back" ? (
              <>
                <RefusedButton size="sm" variant="primary" leadingIcon="git-pr" label="Open pull request" reason={state.openRefused} />
                <RefusedButton size="sm" variant="secondary" label="Open as draft" reason={state.openRefused} />
              </>
            ) : (
              <>
                <Button size="sm" variant="primary" leadingIcon="git-pr" onClick={onOpen} disabled={Boolean(busy)} loading={busy === "open"}>
                  Open pull request
                </Button>
                <Button size="sm" variant="secondary" onClick={onDraft} disabled={Boolean(busy)} loading={busy === "draft"}>Open as draft</Button>
              </>
            )}
            {onFeedback ? (
              <Button size="sm" variant="quiet" leadingIcon="comments" onClick={onFeedback}>{sentBack ? "Show feedback" : "Send feedback"}</Button>
            ) : null}
            {state.kind === "sent-back" ? <span className={styles["reason"]} aria-hidden="true">{state.openRefused}</span> : null}
            {state.kind === "yours" && onDoneWithout ? (
              <span className={styles["end"]}>
                <Button size="sm" variant="quiet" leadingIcon="check" onClick={onDoneWithout} disabled={Boolean(busy)}>Done without a pull request</Button>
              </span>
            ) : null}
          </div>
        )}
      </div>
    </Callout>
  );
}

/** One piece of what was checked: a button to a tab, a link out, or plain words. */
export interface EvidenceChip {
  readonly id: string;
  readonly icon: IconName;
  /** A passed check: its glyph in success ink; the words say so too. */
  readonly ok?: boolean | undefined;
  readonly label: ReactNode;
  /** The accessible name when the label is terse. */
  readonly name?: string | undefined;
  readonly onOpen?: (() => void) | undefined;
  readonly href?: string | undefined;
}

export interface ReviewEvidenceProps extends Omit<HTMLAttributes<HTMLUListElement>, "children"> {
  readonly chips: ReadonlyArray<EvidenceChip>;
  /** The list's name. */
  readonly label?: string | undefined;
}

/**
 * What was checked before a person decides, one chip each on the active
 * wash, each leading to where it is read: a tab (a button) or the preview
 * (a link, new tab). Never a status: a failed check is an escalation.
 */
export function ReviewEvidence({ chips, label = "What was checked", className, ...rest }: ReviewEvidenceProps) {
  return (
    <ul className={cx(styles["chips"], className)} aria-label={label} {...rest}>
      {chips.map((c) => {
        const inner = (
          <>
            <Icon name={c.icon} size={12} className={c.ok ? styles["ok"] : styles["chipIcon"]} />
            <span>{c.label}</span>
          </>
        );
        return (
          <li key={c.id}>
            {c.href ? (
              <a className={styles["chip"]} href={c.href} target="_blank" rel="noreferrer" aria-label={c.name}>
                {inner}
                <Icon name="external" size={12} className={styles["chipIcon"]} />
              </a>
            ) : c.onOpen ? (
              <button type="button" className={styles["chip"]} onClick={c.onOpen} aria-label={c.name}>{inner}</button>
            ) : (
              <span className={styles["chip"]} data-static="">{inner}</span>
            )}
          </li>
        );
      })}
    </ul>
  );
}

import { useId, useState, type HTMLAttributes, type ReactElement, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { firstName } from "../util/format.ts";
import { toMs } from "../util/useNow.ts";
import { Icon } from "../icons/index.tsx";
import { Duration } from "./Numbers.tsx";
import { Button } from "../primitives/Button.tsx";
import { Callout } from "../primitives/Layout.tsx";
import styles from "./CredentialCard.module.css";

/**
 * A pull request waiting on a person's credential (Connect GitHub): dude
 * opens it as the task's owner, who has not connected. One of the
 * QuestionCard family: a turn on the chat grid, loud (attention wash and
 * bar) while it waits, with a glyph and a label as well as its tone.
 *
 *   owner   "Connect GitHub to open the pull request", the branch that is
 *           pushed, a primary Connect GitHub, and a quiet "Why as me?".
 *   others  "Waiting for <Name> to connect GitHub", when they were told,
 *           and Take over and open it as me only for a reader who has
 *           connected (`onTakeOver`); never a way that would wait again.
 */
export interface CredentialCardProps extends Omit<HTMLAttributes<HTMLElement>, "children" | "title"> {
  /** The person it waits on: their full name; null for a person not known (the task's owner). */
  readonly person: string | null;
  /** The reader is that person: the card is theirs to act on. */
  readonly yours: boolean;
  /** The task branch, pushed and ready. */
  readonly branch: string;
  /** They had connected and must connect again. */
  readonly reconnect?: boolean | undefined;
  /** When they were told. */
  readonly askedAt?: string | number | Date | undefined;
  /** Starts Connect GitHub, back to this task (the owner's view). */
  readonly onConnect?: (() => void) | undefined;
  readonly connecting?: boolean | undefined;
  /** Makes the task the reader's and opens it as them: only for a reader who has connected. */
  readonly onTakeOver?: (() => void) | undefined;
  readonly takingOver?: boolean | undefined;
}

/**
 * The words a credential wait says, by whose it is: kept here so the card
 * and its tests read the same. `person` is their full name, or null when
 * the wait names nobody known: then it is the task's owner.
 */
export function credentialWords(person: string | null, yours: boolean, reconnect = false): { title: string; label: string } {
  const verb = reconnect ? "reconnect" : "connect";
  const Verb = reconnect ? "Reconnect" : "Connect";
  const first = firstName(person);
  if (yours) return { title: `${Verb} GitHub to open the pull request`, label: `Needs you · ${Verb} GitHub` };
  return first
    ? { title: `Waiting for ${person} to ${verb} GitHub`, label: `Needs ${first} · ${Verb} GitHub` }
    : { title: `Waiting for the task's owner to ${verb} GitHub`, label: `Needs the owner · ${Verb} GitHub` };
}

export function CredentialCard({ person, yours, branch, reconnect = false, askedAt, onConnect, connecting, onTakeOver, takingOver,
  className, ...rest }: CredentialCardProps): ReactElement {
  const [why, setWhy] = useState(false);
  const whyId = useId();
  const words = credentialWords(person, yours, reconnect);
  const asked = toMs(askedAt);
  const who = person?.trim() || "The task's owner";
  const body: ReactNode = yours ? (
    <p className={styles["body"]}>
      dude opens pull requests as the task's owner, and {reconnect ? "your GitHub connection no longer works" : "you haven't connected your GitHub account"}.
      The branch <code className={styles["branch"]}>{branch}</code> is pushed and ready. Once you {reconnect ? "reconnect" : "connect"}, the PR opens on its own. Nothing is redone.
    </p>
  ) : (
    <p className={styles["body"]}>
      The pull request opens as its owner.{" "}
      {asked !== null ? <>{who} was notified <Duration ms={Math.max(0, Date.now() - asked)} format="age" /> ago.</> : <>{who} was notified.</>}
    </p>
  );
  return (
    <article className={cx(styles["root"], className)} data-testid="credential-wait" data-yours={yours || undefined}
      aria-label={words.title} {...rest}>
      <div className={styles["gutter"]} aria-hidden>
        <span className={styles["glyph"]}><Icon name="github" size={16} /></span>
      </div>
      <div className={styles["main"]}>
        <header className={styles["header"]}>
          <span className={styles["tag"]} data-testid="credential-wait-label">
            <Icon name="hand" size={10} strokeWidth={2} />
            <span className="ds-cap">{words.label}</span>
          </span>
        </header>
        <h3 className={styles["title"]} data-testid="credential-wait-title">{words.title}</h3>
        {body}
        {why ? (
          <p id={whyId} className={styles["why"]} data-testid="credential-wait-why">
            GitHub shows a pull request as opened by a person, so dude opens it as the person who owns the task, never as itself.
          </p>
        ) : null}
        <div className={styles["actions"]}>
          {yours && onConnect ? (
            <Button size="sm" variant="primary" leadingIcon="github" loading={connecting} onClick={onConnect} data-testid="credential-wait-connect">
              {reconnect ? "Reconnect GitHub" : "Connect GitHub"}
            </Button>
          ) : null}
          {yours ? (
            <Button size="sm" variant="quiet" aria-expanded={why} aria-controls={why ? whyId : undefined} onClick={() => setWhy((w) => !w)}
              data-testid="credential-wait-why-toggle">Why as me?</Button>
          ) : null}
          {!yours && onTakeOver ? (
            <Button size="sm" variant="secondary" loading={takingOver} onClick={onTakeOver} data-testid="credential-wait-take-over">
              Take over and open it as me
            </Button>
          ) : null}
        </div>
      </div>
    </article>
  );
}

export interface CredentialWarningProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  /** The effective owner's full name. */
  readonly person: string;
  /** The reader is that owner: offer Connect GitHub. */
  readonly yours: boolean;
  readonly onConnect?: (() => void) | undefined;
}

/**
 * Before work starts (filing, starting, a task's Overview): the owner has no
 * live GitHub connection, so the task will stop before its pull request.
 * An attention Callout under the owner field; never a block.
 */
export function CredentialWarning({ person, yours, onConnect, className, ...rest }: CredentialWarningProps): ReactElement {
  return (
    <Callout tone="attention" className={className} data-testid="credential-warning" {...rest}>
      {person} hasn't connected GitHub. The task will do its work and then stop before its pull request.{" "}
      {yours && onConnect ? (
        <Button size="sm" variant="quiet" leadingIcon="github" onClick={onConnect} data-testid="credential-warning-connect">Connect GitHub</Button>
      ) : null}
      {!yours ? <>Pick an owner who has connected GitHub, or ask {firstName(person, person)} to connect.</> : null}
    </Callout>
  );
}

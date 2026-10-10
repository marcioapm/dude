import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Button } from "../primitives/Button.tsx";
import { RowMenu } from "../primitives/RowMenu.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import { SharedMark } from "./Brainstorm.tsx";
import type { Person } from "./PersonAvatar.tsx";
import { ProjectAvatar, type ProjectFace } from "./ProjectAvatar.tsx";
import type { Density } from "../tokens/density.ts";
import styles from "./Welcome.module.css";

export interface WelcomeProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  /** The app's face, sized by the stage (64, 48 compact): pass it as `size="fill"`. */
  readonly mark?: ReactNode;
  /** "Afternoon, Márcio". */
  readonly greeting: ReactNode;
  /** One line under it: "What are we working out today?". */
  readonly line?: ReactNode;
  /** A `ChatComposer`: the stage raises it and makes its field taller. */
  readonly composer: ReactNode;
  /** `StarterPills`. */
  readonly starters?: ReactNode;
  /** `RecentSessions`, or the first-time line. */
  readonly footer?: ReactNode;
}

/**
 * The first screen: a 720px column a little above the middle of the page —
 * the face, the greeting and its line, the composer raised off the page,
 * the starters, then what came before. It scrolls when the window is short.
 * Compact takes the air in (less space above, a smaller face, a shorter
 * field), never the greeting's size.
 */
export function Welcome({ mark, greeting, line, composer, starters, footer, className, ...rest }: WelcomeProps) {
  return (
    <div className={cx(styles["stage"], className)} {...rest}>
      <div className={styles["column"]}>
        <header className={styles["greeting"]}>
          {mark ? <span className={styles["mark"]}>{mark}</span> : null}
          <h1 className={styles["hello"]}>{greeting}</h1>
          {line ? <p className={styles["line"]}>{line}</p> : null}
        </header>
        <div className={styles["composer"]}>{composer}</div>
        {starters}
        {footer}
      </div>
    </div>
  );
}

/** A muted line under the starters for someone with no sessions yet: what a session does. */
export function WelcomeNote({ children }: { readonly children: ReactNode }) {
  return <p className={styles["note"]} data-testid="welcome-note">{children}</p>;
}

export interface Starter {
  readonly id: string;
  readonly icon: IconName;
  readonly title: string;
  /** What it is for, in its tooltip. */
  readonly detail: string;
  /** The words it writes in the composer for the person to finish; never sent by itself. */
  readonly prompt: string;
}

export interface StarterPillsProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  readonly starters: ReadonlyArray<Starter>;
  readonly onPick: (starter: Starter) => void;
}

/** Ways to start, as pills under the composer: picking one fills it, never sends. */
export function StarterPills({ starters, onPick, className, ...rest }: StarterPillsProps) {
  return (
    <div className={cx(styles["pills"], className)} role="group" aria-label="Ways to start" {...rest}>
      {starters.map((s) => (
        <Tooltip key={s.id} content={s.detail}>
          <button type="button" className={styles["pill"]} onClick={() => onPick(s)} data-starter={s.id}>
            <Icon name={s.icon} size={14} />
            <span className="ds-cap">{s.title}</span>
          </button>
        </Tooltip>
      ))}
    </div>
  );
}

export interface LinkableProject extends ProjectFace {
  readonly id: string;
  readonly name: string;
}

export interface ComposerLinksProps {
  /** What the session will read, in the order linked. */
  readonly linked: ReadonlyArray<LinkableProject>;
  /** Every project that could be linked; those linked already are left out of the menu. */
  readonly projects?: ReadonlyArray<LinkableProject> | undefined;
  /** Without `onLink` and `onUnlink` the chips are read only. */
  readonly onLink?: ((project: LinkableProject) => void) | undefined;
  readonly onUnlink?: ((project: LinkableProject) => void) | undefined;
  /** Draws the menu open, portaled into this element: a gallery preview, never the product. */
  readonly previewMenu?: HTMLElement | null | undefined;
}

/**
 * What a session will read, said where you write: `ChatComposer`'s
 * `leading`. Each linked project's face and name with a close, then a quiet
 * "+ Link" opening a menu of the rest; "Reads memory only" with none.
 */
export function ComposerLinks({ linked, projects = [], onLink, onUnlink, previewMenu }: ComposerLinksProps) {
  const left = projects.filter((p) => !linked.some((l) => l.id === p.id));
  return (
    <span className={styles["links"]} data-testid="composer-links">
      <span className={styles["linksLabel"]}>{linked.length === 0 ? "Reads memory only" : "Reads"}</span>
      {linked.map((p) => (
        <span key={p.id} className={cx(styles["chip"], onUnlink && styles["chipRemovable"])} data-project={p.id}>
          <ProjectAvatar project={p} size={14} />
          <span className="ds-cap">{p.name}</span>
          {onUnlink ? (
            <button type="button" className={styles["chipClose"]} aria-label={`Stop reading ${p.name}`} onClick={() => onUnlink(p)}>
              <Icon name="close" size={12} />
            </button>
          ) : null}
        </span>
      ))}
      {onLink && left.length > 0 ? (
        <RowMenu
          align="start"
          className={styles["menu"]}
          {...(previewMenu ? { forceMount: true as const, container: previewMenu } : {})}
          items={left.map((p) => ({ id: p.id, label: p.name, leading: <ProjectAvatar project={p} size={16} />, onSelect: () => onLink(p) }))}
          trigger={
            <button type="button" className={styles["add"]} data-testid="composer-link">
              <Icon name="plus" size={12} />
              <span className="ds-cap">{linked.length === 0 ? "Link a project" : "Link"}</span>
            </button>
          }
        />
      ) : null}
    </span>
  );
}

/** How many recent sessions the welcome lists: compact has room for two more. */
export const RECENT_SESSIONS_SHOWN: Readonly<Record<Density, number>> = { comfortable: 4, compact: 6 };

export interface RecentSession {
  readonly id: string;
  readonly title: string;
  /** What came of it: "Filed 1 epic, 4 tasks", "Nothing filed yet". */
  readonly summary?: ReactNode;
  /** "yesterday", "3d". */
  readonly age?: ReactNode;
  /** Someone else is in it too; with the owner's face when it is not yours. */
  readonly shared?: { readonly owner?: Person | null | undefined } | undefined;
}

export interface RecentSessionsProps extends Omit<HTMLAttributes<HTMLElement>, "children"> {
  /** Newest first; all of them are drawn (the app takes `RECENT_SESSIONS_SHOWN`). */
  readonly sessions: ReadonlyArray<RecentSession>;
  readonly onOpen: (id: string) => void;
  /** "All sessions": the list of them all. */
  readonly onAll?: (() => void) | undefined;
}

/**
 * A short list of the sessions you were in last: one line each, told
 * apart by space, a wash on hover. The bulb, the title, the shared mark,
 * what came of it in muted ink (dropped on a phone) and its age.
 */
export function RecentSessions({ sessions, onOpen, onAll, className, ...rest }: RecentSessionsProps) {
  return (
    <section className={cx(styles["recent"], className)} aria-label="Recent sessions" {...rest}>
      <div className={styles["recentHead"]}>
        <h2 className="ds-label">Recent sessions</h2>
        {onAll ? <Button size="sm" variant="quiet" onClick={onAll} data-testid="all-sessions">All sessions</Button> : null}
      </div>
      <ul className={styles["recentList"]}>
        {sessions.map((s) => (
          <li key={s.id}>
            <button type="button" className={styles["recentRow"]} onClick={() => onOpen(s.id)} data-session={s.id} data-testid="recent-session">
              <Icon name="brainstorm" size={14} className={styles["recentIcon"]} />
              <span className={styles["recentTitle"]}>{s.title}</span>
              {s.shared ? <SharedMark owner={s.shared.owner ?? undefined} /> : null}
              {s.summary ? <span className={styles["recentSummary"]}>{s.summary}</span> : <span className={styles["recentSummary"]} />}
              {s.age ? <span className={cx(styles["recentAge"], "ds-tnum")}>{s.age}</span> : null}
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

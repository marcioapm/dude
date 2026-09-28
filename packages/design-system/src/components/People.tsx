import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Tooltip } from "../primitives/Tooltip.tsx";
import { HumanAvatar, type Person } from "./HumanAvatar.tsx";
import styles from "./People.module.css";

export interface PresencePerson extends Person {
  readonly id: string;
  /** Where and when, for the tooltip: "on TEXT-14 · 2m ago". */
  readonly where?: string | undefined;
}

export interface OnlineRowProps extends HTMLAttributes<HTMLDivElement> {
  /** Who is online now, the viewer included. */
  readonly people: ReadonlyArray<PresencePerson>;
  /** Faces shown before the count says the rest. */
  readonly max?: number | undefined;
}

/**
 * The sidebar's "Online" row: a face per person here in the last few
 * minutes, each with a tooltip saying who and where. Quiet by rule —
 * nobody's presence is a status, so no dot, no colour, no motion.
 */
export function OnlineRow({ people, max = 6, className, ...rest }: OnlineRowProps) {
  if (people.length === 0) return null;
  const shown = people.slice(0, max);
  return (
    <div className={cx(styles["online"], className)} role="group" aria-label={`Online: ${people.map((p) => p.name).join(", ")}`} {...rest}>
      <span className={cx(styles["label"], "ds-cap")}>Online</span>
      <span className={styles["faces"]}>
        {shown.map((p) => (
          <Tooltip key={p.id} content={p.where ? `${p.name} — ${p.where}` : p.name}>
            <span className={styles["face"]} tabIndex={0} data-person={p.id}>
              <HumanAvatar person={p} size="md" aria-hidden />
            </span>
          </Tooltip>
        ))}
      </span>
      <span className={styles["count"]}>{people.length}</span>
    </div>
  );
}

export interface ProfileBandProps extends Omit<HTMLAttributes<HTMLElement>, "onClick"> {
  readonly person: Person;
  /** Under the name: an email, or a role. */
  readonly detail?: string | undefined;
  /** Opens your settings. */
  readonly onOpen?: (() => void) | undefined;
  /** Above you, the organisation's settings (admins), or anything else. */
  readonly children?: ReactNode;
  /** At the end of your row: sign out. */
  readonly actions?: ReactNode;
  /** For tests: the open button's `data-testid`. */
  readonly openTestId?: string | undefined;
}

/**
 * The foot of the sidebar: you, by face and name — a way to your settings
 * — on a shade of its own, apart from the tree above it.
 */
export function ProfileBand({ person, detail, onOpen, children, actions, openTestId, className, ...rest }: ProfileBandProps) {
  return (
    <footer className={cx(styles["band"], className)} {...rest}>
      {children}
      <div className={styles["me"]}>
        <button type="button" className={styles["meButton"]} onClick={onOpen} aria-label={`Your settings — ${person.name}`} data-testid={openTestId}>
          <HumanAvatar person={person} size="lg" aria-hidden />
          <span className={styles["who"]}>
            <span className={styles["name"]}>{person.name}</span>
            {detail ? <span className={styles["detail"]}>{detail}</span> : null}
          </span>
        </button>
        {actions}
      </div>
    </footer>
  );
}

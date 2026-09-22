import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { IDENTITY_SLOTS } from "../tokens/palette.ts";
import styles from "./HumanAvatar.module.css";

export interface Person {
  /** Stable identity for colour and keys; falls back to `name`. */
  readonly id?: string | undefined;
  readonly name: string;
  /** Not yet available in the product; supported so the upgrade is a data change. */
  readonly imageUrl?: string | undefined;
}

export type HumanAvatarSize = "xs" | "sm" | "md" | "lg";

/**
 * Deterministic identity slot for a person: the same name is the same
 * colour on every screen, with no profile record. djb2 over the id or name.
 */
export function identitySlot(person: Person): number {
  const key = (person.id ?? person.name).trim().toLowerCase();
  let h = 5381;
  for (let i = 0; i < key.length; i++) h = ((h << 5) + h + key.charCodeAt(i)) | 0;
  return Math.abs(h) % IDENTITY_SLOTS;
}

/**
 * Up to two initials. "Márcio Martins" → "MM"; "marcio" → "MA";
 * "marcio@example.com" → "MA"; "m.martins" → "MM".
 */
export function initialsOf(name: string): string {
  const local = name.includes("@") ? (name.split("@")[0] ?? name) : name;
  const parts = local
    .split(/[\s._\-]+/)
    .map((p) => p.trim())
    .filter((p) => p.length > 0);
  if (parts.length === 0) return "?";
  if (parts.length === 1) {
    const p = parts[0] ?? "";
    return p.slice(0, 2).toUpperCase();
  }
  const a = parts[0]?.charAt(0) ?? "";
  const b = parts[parts.length - 1]?.charAt(0) ?? "";
  return `${a}${b}`.toUpperCase();
}

export interface HumanAvatarProps extends HTMLAttributes<HTMLSpanElement> {
  readonly person: Person;
  readonly size?: HumanAvatarSize | undefined;
  /** Render the name next to the avatar. */
  readonly showName?: boolean | undefined;
  /** Secondary text under/next to the name ("requested", "answered 2m ago"). */
  readonly detail?: string | undefined;
}

/**
 * A person. Distinct from `AgentAvatar` on three channels at once so the two
 * are never confused: a full circle with a hairline ring (agents are
 * squares, or a round *glyph* for the orchestrator), letters rather than an
 * icon, and a muted identity colour rather than a vivid role colour. The
 * colour only tells two people apart in a stack; it never means anything.
 *
 * Profile images are not in the product yet. When they arrive, `imageUrl`
 * replaces the initials and nothing else changes.
 */
export function HumanAvatar({ person, size = "sm", showName, detail, className, ...rest }: HumanAvatarProps) {
  const slot = identitySlot(person);
  const avatar = (
    <span
      className={cx(styles["root"], styles[size], !showName && className)}
      data-identity={slot}
      role="img"
      aria-label={person.name}
      title={showName ? undefined : person.name}
      {...(showName ? {} : rest)}
    >
      {person.imageUrl ? <img className={styles["image"]} src={person.imageUrl} alt="" /> : <span className={styles["initials"]}>{initialsOf(person.name)}</span>}
    </span>
  );
  if (!showName) return avatar;
  return (
    <span className={cx(styles["withName"], className)} {...rest}>
      {avatar}
      <span className={styles["text"]}>
        <span className={styles["name"]}>{person.name}</span>
        {detail ? <span className={styles["detail"]}>{detail}</span> : null}
      </span>
    </span>
  );
}

export interface HumanAvatarStackProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly people: ReadonlyArray<Person>;
  readonly size?: HumanAvatarSize | undefined;
  /** Avatars shown before collapsing the rest into "+N". */
  readonly max?: number | undefined;
}

/**
 * Overlapping avatars for a group. Order is the caller's (put the most
 * relevant person first — the one it is waiting on, or the requester). Past
 * `max`, a "+N" chip in the same shape stands for the rest; the full list is
 * in the title and the accessible name, so nothing is lost to the overflow.
 */
export function HumanAvatarStack({ people, size = "xs", max = 3, className, ...rest }: HumanAvatarStackProps) {
  if (people.length === 0) return null;
  const shown = people.length > max ? people.slice(0, Math.max(1, max - 1)) : people;
  const rest_ = people.length - shown.length;
  const names = people.map((p) => p.name).join(", ");
  return (
    <span className={cx(styles["stack"], styles[`stack-${size}`], className)} role="group" aria-label={names} title={names} {...rest}>
      {shown.map((p, i) => (
        <HumanAvatar key={p.id ?? `${p.name}-${i}`} person={p} size={size} aria-hidden />
      ))}
      {rest_ > 0 ? (
        <span className={cx(styles["root"], styles[size], styles["overflow"])} aria-hidden>
          <span className={styles["initials"]}>+{rest_}</span>
        </span>
      ) : null}
    </span>
  );
}

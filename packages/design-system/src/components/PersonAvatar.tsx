import type { HTMLAttributes, ReactNode } from "react";
import type { AgentRole } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { IDENTITY_SLOTS } from "../tokens/palette.ts";
import { Icon } from "../icons/index.tsx";
import { ROLE_ICON, ROLE_LABEL } from "./AgentAvatar.tsx";
import styles from "./PersonAvatar.module.css";

/** Anyone with a face: a name, and a photo when there is one. */
export interface Person {
  /** Stable identity for colour and keys; falls back to `name`. */
  readonly id?: string | undefined;
  readonly name: string;
  /** A photo. Without one, initials on the person's identity colour. */
  readonly photoUrl?: string | null | undefined;
  /** @deprecated the old name for `photoUrl`. */
  readonly imageUrl?: string | undefined;
  /** Acted in the last five minutes. Unknown (undefined) draws no ring. */
  readonly online?: boolean | undefined;
}

/** Face sizes in px, 16–56. The named ones match the avatar tokens. */
export type PersonAvatarSize = 16 | 20 | 24 | 28 | 32 | 40 | 56;

/**
 * Deterministic identity slot: the same person (or project) is the same
 * colour on every screen, with no profile record. djb2 over the id or name.
 */
export function identitySlot(who: { readonly id?: string | undefined; readonly name: string }): number {
  const key = (who.id ?? who.name).trim().toLowerCase();
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
  if (parts.length === 1) return (parts[0] ?? "").slice(0, 2).toUpperCase();
  const a = parts[0]?.charAt(0) ?? "";
  const b = parts[parts.length - 1]?.charAt(0) ?? "";
  return `${a}${b}`.toUpperCase();
}

export interface PersonAvatarProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly person: Person;
  readonly size?: PersonAvatarSize | undefined;
  /**
   * The agent working for this person: its role tile sits on the face,
   * bottom right. With `live` it pulses — it is working now.
   */
  readonly agent?: AgentRole | undefined;
  readonly live?: boolean | undefined;
  /** Replaces the default tooltip ("Ana Ribeiro · online"). */
  readonly title?: string | undefined;
  /** Draw the online ring when the person is online. Off in dense lists of the same person. */
  readonly ring?: boolean | undefined;
}

/** What a face says when hovered: who, whether they are here, and their agent. */
export function personTitle(person: Person, agent?: AgentRole, live?: boolean): string {
  const lines = [person.online ? `${person.name} · online` : person.name];
  if (agent) lines.push(`${ROLE_LABEL[agent]} ${live ? "working for them now" : "working for them"}`);
  return lines.join("\n");
}

/**
 * A person. Always a circle: a photo, or initials on a muted identity
 * colour — never a role colour or a tone, which belong to agents and
 * states. A green ring means online. When an agent works for the person
 * its role tile sits on the face, pulsing while it works, so "who is this
 * for" and "is anything happening" are one glance.
 */
export function PersonAvatar({ person, size = 24, agent, live, title, ring = true, className, style, ...rest }: PersonAvatarProps) {
  const photo = person.photoUrl ?? person.imageUrl ?? null;
  return (
    <span
      className={cx(styles["root"], ring && person.online && styles["online"], className)}
      data-identity={identitySlot(person)}
      data-size={size}
      role="img"
      aria-label={title ?? personTitle(person, agent, live).replace("\n", " · ")}
      title={title ?? personTitle(person, agent, live)}
      style={{ ["--av-size" as string]: `${size}px`, ...style }}
      {...rest}
    >
      {photo ? <img className={styles["photo"]} src={photo} alt="" /> : <span className={styles["initials"]}>{initialsOf(person.name)}</span>}
      {agent ? (
        <span className={cx(styles["agent"], live && styles["agentLive"])} data-role={agent} aria-hidden>
          <Icon name={ROLE_ICON[agent]} size="70%" strokeWidth={2} />
        </span>
      ) : null}
    </span>
  );
}

export interface PersonAvatarStackProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly people: ReadonlyArray<Person>;
  readonly size?: PersonAvatarSize | undefined;
  /** Faces shown before the rest collapse into "+N". */
  readonly max?: number | undefined;
  /** Per person, the agent working for them (the first person is usually the owner). */
  readonly agents?: ReadonlyMap<string, { readonly role: AgentRole; readonly live: boolean }> | undefined;
  /** The first face one size up: the owner leads. */
  readonly leadLarger?: boolean | undefined;
  readonly children?: ReactNode;
}

const STEP_UP: Partial<Record<PersonAvatarSize, PersonAvatarSize>> = { 16: 20, 20: 24, 24: 28, 28: 32, 32: 40, 40: 56 };

/**
 * Faces overlapping by a quarter, the first on top: order by relevance
 * (the owner, then everyone else on the task). Past `max`, a "+N" in the
 * same shape stands for the rest; every name is in the group's label.
 */
export function PersonAvatarStack({ people, size = 20, max = 4, agents, leadLarger, className, ...rest }: PersonAvatarStackProps) {
  if (people.length === 0) return null;
  const shown = people.length > max ? people.slice(0, Math.max(1, max - 1)) : people;
  const more = people.length - shown.length;
  const names = people.map((p) => p.name).join(", ");
  return (
    <span className={cx(styles["stack"], className)} role="group" aria-label={names} {...rest}>
      {shown.map((p, i) => {
        const a = agents?.get(p.id ?? p.name);
        return (
          <PersonAvatar
            key={p.id ?? `${p.name}-${i}`}
            person={p}
            size={i === 0 && leadLarger ? (STEP_UP[size] ?? size) : size}
            {...(a ? { agent: a.role, live: a.live } : {})}
          />
        );
      })}
      {more > 0 ? (
        <span className={cx(styles["root"], styles["overflow"])} style={{ ["--av-size" as string]: `${size}px` }} title={names} aria-hidden>
          <span className={styles["initials"]}>+{more}</span>
        </span>
      ) : null}
    </span>
  );
}

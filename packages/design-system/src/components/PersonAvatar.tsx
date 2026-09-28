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
  return stringSlot((who.id ?? who.name).trim().toLowerCase(), IDENTITY_SLOTS);
}

/** A string's steady place among `slots` (djb2): the same string, the same place. */
export function stringSlot(key: string, slots: number): number {
  let h = 5381;
  for (let i = 0; i < key.length; i++) h = ((h << 5) + h + key.charCodeAt(i)) | 0;
  return Math.abs(h) % slots;
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
  const photo = person.photoUrl ?? null;
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
  readonly children?: ReactNode;
}

/**
 * Faces overlapping by a quarter, the first on top: order by relevance
 * (the owner, then everyone else on the task). Past `max`, a "+N" in the
 * same shape stands for the rest; every name is in the group's label.
 */
export function PersonAvatarStack({ people, size = 20, max = 4, agents, className, ...rest }: PersonAvatarStackProps) {
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
            size={size}
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

export interface PersonLineProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly person: Person;
  readonly size?: PersonAvatarSize | undefined;
  /** Under the name: their part ("Owner", "On this task"), or what their agent is doing. */
  readonly detail?: ReactNode;
  readonly agent?: AgentRole | undefined;
  readonly live?: boolean | undefined;
}

/** A face with a name and a line under it: the people on a task, the owner of a session. */
export function PersonLine({ person, size = 40, detail, agent, live, className, ...rest }: PersonLineProps) {
  return (
    <span className={cx(styles["line"], className)} {...rest}>
      <PersonAvatar person={person} size={size} {...(agent ? { agent, live } : {})} />
      <span className={styles["lineText"]}>
        <span className={styles["lineName"]}>{person.name}</span>
        {detail ? <span className={styles["lineDetail"]}>{detail}</span> : null}
      </span>
    </span>
  );
}

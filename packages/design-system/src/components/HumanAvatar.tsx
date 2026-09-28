import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { PersonAvatar, PersonAvatarStack, type Person, type PersonAvatarSize } from "./PersonAvatar.tsx";
import styles from "./HumanAvatar.module.css";

export { identitySlot, initialsOf } from "./PersonAvatar.tsx";
export type { Person } from "./PersonAvatar.tsx";

export type HumanAvatarSize = "xs" | "sm" | "md" | "lg";

/** The named sizes, as faces: the avatar tokens' 16/20/24/32. */
export const HUMAN_AVATAR_PX: Record<HumanAvatarSize, PersonAvatarSize> = { xs: 16, sm: 20, md: 24, lg: 32 };

export interface HumanAvatarProps extends HTMLAttributes<HTMLSpanElement> {
  readonly person: Person;
  readonly size?: HumanAvatarSize | undefined;
  /** Render the name next to the avatar. */
  readonly showName?: boolean | undefined;
  /** Secondary text under/next to the name ("requested", "answered 2m ago"). */
  readonly detail?: string | undefined;
}

/**
 * A person by a named size, optionally with their name beside the face.
 * The face is `PersonAvatar`; this keeps the older, named-size API.
 */
export function HumanAvatar({ person, size = "sm", showName, detail, className, ...rest }: HumanAvatarProps) {
  if (!showName) return <PersonAvatar person={person} size={HUMAN_AVATAR_PX[size]} className={className} {...rest} />;
  return (
    <span className={cx(styles["withName"], className)} {...rest}>
      <PersonAvatar person={person} size={HUMAN_AVATAR_PX[size]} aria-hidden title={person.name} />
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

/** `PersonAvatarStack` by a named size. */
export function HumanAvatarStack({ people, size = "xs", max = 3, ...rest }: HumanAvatarStackProps) {
  return <PersonAvatarStack people={people} size={HUMAN_AVATAR_PX[size]} max={max} title={people.map((p) => p.name).join(", ")} {...rest} />;
}

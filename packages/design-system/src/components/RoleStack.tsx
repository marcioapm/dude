import type { HTMLAttributes } from "react";
import type { AgentRole } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import styles from "./RoleStack.module.css";

export interface RoleStackProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  /** Roles working right now, in the order to show them. */
  readonly roles: ReadonlyArray<AgentRole>;
  /** Avatars shown before collapsing the rest into "+N". */
  readonly max?: number | undefined;
}

/**
 * Who is working on something right now, as xs role avatars. Distinct from
 * `HumanAvatarStack` on every channel — square, glyph, vivid — and not
 * overlapped, because three roles side by side must each be readable. The
 * full list is the accessible name, so nothing is lost past `max`.
 */
export function RoleStack({ roles, max = 3, className, ...rest }: RoleStackProps) {
  if (roles.length === 0) return null;
  const label = roles.map((r) => ROLE_LABEL[r]).join(", ");
  return (
    <span className={cx(styles["root"], className)} role="group" aria-label={`Working: ${label}`} title={`Working: ${label}`} {...rest}>
      {roles.slice(0, max).map((r) => (
        <AgentAvatar key={r} role={r} size="xs" aria-hidden />
      ))}
      {roles.length > max ? <span className={styles["more"]}>+{roles.length - max}</span> : null}
    </span>
  );
}

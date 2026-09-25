import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import type { AgentRoleName } from "../tokens/palette.ts";
import type { ActorType } from "../tokens/status.ts";
import styles from "./AgentAvatar.module.css";

export type AvatarKind = AgentRoleName | Exclude<ActorType, "agent">;

export const ROLE_LABEL: Record<AvatarKind, string> = {
  orchestrator: "Orchestrator",
  investigator: "Investigator",
  implementer: "Implementer",
  reviewer: "Reviewer",
  simplifier: "Simplifier",
  qa_browser: "QA browser",
  human: "Human",
  system: "System",
  integration: "Integration",
};

const ROLE_ICON: Record<AvatarKind, IconName> = {
  orchestrator: "orchestrator",
  investigator: "investigator",
  implementer: "implementer",
  reviewer: "reviewer",
  simplifier: "simplifier",
  qa_browser: "qa_browser",
  human: "human",
  system: "system",
  integration: "integration",
};

export interface AgentAvatarProps extends HTMLAttributes<HTMLSpanElement> {
  readonly role: AvatarKind;
  /** `chat` is the transcript's own avatar: sized by `--ds-size-avatar-chat`, which follows the density (`tokens/density.ts`). */
  readonly size?: "xs" | "sm" | "md" | "lg" | "chat" | undefined;
  /** Vivid fill; use sparingly (headers, the selected session). */
  readonly solid?: boolean | undefined;
  /** Small green dot: this agent is currently running. */
  readonly live?: boolean | undefined;
  /** Show a glyph (default) or the role's initial letter. */
  readonly mark?: "glyph" | "initial" | undefined;
  /** Render a name next to the avatar. */
  readonly name?: string | undefined;
  /** Show the role name under/next to `name`. */
  readonly showRole?: boolean | undefined;
}

/**
 * Identifies *who* did something. Roles are distinguished by glyph shape,
 * hue, and — for the orchestrator — a round mask, so identity survives
 * colorblindness and monochrome. Humans are round with a ring; system is
 * hollow; integrations are outlined squares.
 */
export function AgentAvatar({
  role,
  size = "sm",
  solid,
  live,
  mark = "glyph",
  name,
  showRole,
  className,
  ...rest
}: AgentAvatarProps) {
  const label = ROLE_LABEL[role];
  const iconSize = size === "xs" ? 10 : size === "sm" ? 12 : size === "md" ? 14 : size === "lg" ? 18 : 20;
  const avatar = (
    <span
      className={cx(styles["root"], styles[role], styles[size], solid && styles["solid"], !name && className)}
      role="img"
      aria-label={label}
      title={name ? undefined : label}
      data-role={role}
      {...(name ? {} : rest)}
    >
      {mark === "initial" ? label.charAt(0) : <Icon name={ROLE_ICON[role]} size={iconSize} strokeWidth={1.75} />}
      {live ? <span className={styles["liveDot"]} aria-hidden /> : null}
    </span>
  );
  if (!name) return avatar;
  return (
    <span className={cx(styles["withName"], className)} {...rest}>
      {avatar}
      <span className={styles["name"]}>{name}</span>
      {showRole ? <span className={styles["roleName"]}>{label}</span> : null}
    </span>
  );
}

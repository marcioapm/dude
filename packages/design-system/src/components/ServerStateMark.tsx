import type { HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { serverStateSpec, type ServerDisplayState } from "../tokens/servers.ts";
import marks from "./StatusMark.module.css";
import dots from "./StatusBadge.module.css";

export interface ServerStateMarkProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly state: ServerDisplayState;
  readonly size?: "sm" | "md" | undefined;
  /** Keep the glyph, hide the word; the word moves to `title` and the accessible name. */
  readonly iconOnly?: boolean | undefined;
  /** Replace the vocabulary's word ("Exited 1"). */
  readonly label?: string | undefined;
}

/**
 * A server's state in StatusMark's grammar — a glyph and a word in its
 * tone, no fill: stopped is a filled square, starting a half circle that
 * breathes, ready a check, unreachable a warning, exited a cross with its
 * code. The vocabulary is `tokens/servers.ts`.
 */
export function ServerStateMark({ state, size = "md", iconOnly, label, className, ...rest }: ServerStateMarkProps) {
  const spec = serverStateSpec(state);
  const text = label ?? spec.label;
  return (
    <span
      className={cx(marks["root"], marks[spec.tone], size === "sm" && marks["sm"], spec.live && marks["live"], className)}
      data-server-state={state}
      title={iconOnly ? text : undefined}
      {...rest}
    >
      <Icon name={spec.glyph as IconName} size={size === "sm" ? 12 : 14} strokeWidth={1.75} className={marks["glyph"]} />
      {iconOnly ? <span className="ds-sr-only">{text}</span> : <span className={cx(marks["word"], "ds-cap")}>{text}</span>}
    </span>
  );
}

export interface ServerStateDotProps extends Omit<HTMLAttributes<HTMLSpanElement>, "children"> {
  readonly state: ServerDisplayState;
  /** What the dot stands for, when it is not the state's own word ("api exited"). */
  readonly label?: string | undefined;
}

/**
 * The state as StatusBadge's 8px dot, for a tab or a toggle that has no
 * room for a word: round, in the tone; a live state breathes. The word is
 * its title.
 */
export function ServerStateDot({ state, label, className, ...rest }: ServerStateDotProps) {
  const spec = serverStateSpec(state);
  const text = label ?? spec.label;
  return (
    <span className={cx(dots["dot"], dots[spec.tone], spec.live && dots["live"], className)} data-server-state={state} title={text} {...rest}>
      <span className={dots["dotMark"]} aria-hidden />
      <span className="ds-sr-only">{text}</span>
    </span>
  );
}

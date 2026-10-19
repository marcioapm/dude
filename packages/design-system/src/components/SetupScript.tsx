import { forwardRef, type ButtonHTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { formatBytes, plural } from "../util/format.ts";
import { Icon } from "../icons/index.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import styles from "./SetupScript.module.css";

/**
 * A repository's setup script, as its settings row and its editor show it:
 * SetupScriptMark after the repository's name, and SizeMeter in a code
 * document's footer.
 */

/** Lines in a text as a person counts them: a trailing newline starts no line of its own. */
export function textLines(text: string): number {
  if (text === "") return 0;
  return text.replace(/\n$/, "").split("\n").length;
}

export interface SetupScriptMarkProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "children"> {
  /** The repository's name. */
  readonly repository: string;
  readonly lines: number;
  /** `edit` for someone who may change it; `view` reads it. */
  readonly action: "edit" | "view";
}

/**
 * The repository has a setup script: a terminal glyph and `setup` on the
 * raised shade. A button, because it opens the script for editors and
 * readers alike; never a status colour.
 */
export const SetupScriptMark = forwardRef<HTMLButtonElement, SetupScriptMarkProps>(function SetupScriptMark(
  { repository, lines, action, className, ...rest }, ref) {
  const count = plural(lines, "line");
  const label = `${action === "edit" ? "Edit" : "Read"} ${repository}’s setup script, ${count}`;
  return (
    <Tooltip content={`Setup script · ${count} · runs once per new checkout`}>
      <button ref={ref} type="button" className={cx(styles["mark"], className)} aria-label={label} data-testid="setup-script-mark" {...rest}>
        <Icon name="terminal" size={12} />
        <span aria-hidden>setup</span>
      </button>
    </Tooltip>
  );
});

/** How full a document is against its limit: under 90%, near it, or over. */
export function sizeLevel(bytes: number, limit: number): "neutral" | "attention" | "danger" {
  if (bytes > limit) return "danger";
  return bytes > limit * 0.9 ? "attention" : "neutral";
}

/**
 * A document's size against a caller-supplied limit, tabular: "284 B of
 * 16 KB". Attention past 90% of it, danger over it. The caller validates
 * and owns the save.
 */
export function SizeMeter({ bytes, limit, className }: { readonly bytes: number; readonly limit: number; readonly className?: string | undefined }) {
  const level = sizeLevel(bytes, limit);
  return (
    <span className={cx(styles["meter"], "ds-tnum", className)} data-level={level} data-testid="size-meter">
      {formatBytes(bytes)} of {formatBytes(limit)}
    </span>
  );
}

import { useId, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Button, type ButtonProps } from "./Button.tsx";
import { Tooltip } from "./Tooltip.tsx";
import styles from "./RefusedButton.module.css";

export interface RefusedButtonProps extends Omit<ButtonProps, "disabled" | "onClick" | "children" | "aria-label"> {
  /** Why it can't be used now: its tooltip and its accessible description. */
  readonly reason: string;
  /** Its words, also its accessible name. */
  readonly label: string;
  readonly children?: ReactNode;
  readonly side?: "top" | "bottom" | undefined;
  readonly "data-testid"?: string | undefined;
}

/**
 * A control that can't be used now and still says why: the Button drawn
 * disabled inside a focusable wrapper that takes the pointer and the
 * focus, so its tooltip opens, and that assistive tech meets as a disabled
 * button named by its label and described by the reason. A plain disabled
 * button takes neither, and its reason would never be read.
 */
export function RefusedButton({ reason, label, children, side = "bottom", className, "data-testid": testId, ...button }: RefusedButtonProps) {
  const reasonId = useId();
  return (
    <Tooltip content={reason} side={side}>
      <span className={cx(styles["root"], className)} tabIndex={0} role="button" aria-disabled="true" aria-label={label}
        aria-describedby={reasonId} data-testid={testId} data-refused="">
        <span id={reasonId} className="ds-sr-only">{reason}</span>
        <Button {...button} disabled tabIndex={-1}>{children ?? label}</Button>
      </span>
    </Tooltip>
  );
}

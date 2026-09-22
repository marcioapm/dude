import * as RadixTooltip from "@radix-ui/react-tooltip";
import type { ReactNode } from "react";
import { cx } from "../util/cx.ts";
import styles from "./Tooltip.module.css";

export interface TooltipProps {
  readonly content: ReactNode;
  /** Keyboard shortcut hint shown after the content. */
  readonly shortcut?: string | undefined;
  readonly side?: "top" | "right" | "bottom" | "left" | undefined;
  readonly mono?: boolean | undefined;
  readonly delay?: number | undefined;
  /** The trigger. Must accept a ref and forward props (asChild). */
  readonly children: ReactNode;
}

/**
 * Tooltip. Wrap the app once in `TooltipProvider`. Content is supplementary
 * — never the only place a label lives. Icon buttons already carry a title.
 */
export function Tooltip({ content, shortcut, side = "top", mono, delay, children }: TooltipProps) {
  return (
    <RadixTooltip.Root {...(delay !== undefined ? { delayDuration: delay } : {})}>
      <RadixTooltip.Trigger asChild>{children}</RadixTooltip.Trigger>
      <RadixTooltip.Portal>
        <RadixTooltip.Content className={cx(styles["content"], mono && styles["mono"])} side={side} sideOffset={4}>
          {content}
          {shortcut ? <kbd className={styles["kbd"]}>{shortcut}</kbd> : null}
          <RadixTooltip.Arrow className={styles["arrow"]} width={8} height={4} />
        </RadixTooltip.Content>
      </RadixTooltip.Portal>
    </RadixTooltip.Root>
  );
}

export function TooltipProvider({ children }: { readonly children?: ReactNode }) {
  return (
    <RadixTooltip.Provider delayDuration={400} skipDelayDuration={200}>
      {children}
    </RadixTooltip.Provider>
  );
}

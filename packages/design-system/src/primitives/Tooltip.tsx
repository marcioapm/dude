import * as RadixTooltip from "@radix-ui/react-tooltip";
import { createContext, useContext, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import styles from "./Tooltip.module.css";

export interface TooltipProps {
  readonly content: ReactNode;
  /** Keyboard shortcut hint shown after the content. */
  readonly shortcut?: string | undefined;
  readonly side?: "top" | "right" | "bottom" | "left" | undefined;
  readonly mono?: boolean | undefined;
  readonly delay?: number | undefined;
  /**
   * Stay open when the trigger is pressed. For a trigger that does nothing on
   * a press (things shown but not yours to use): the press is when the
   * person most needs the tooltip's words, so it must not close them.
   */
  readonly keepOnPress?: boolean | undefined;
  /** The trigger. Must accept a ref and forward props (asChild). */
  readonly children: ReactNode;
}

/**
 * Tooltip. Wrap the app once in `TooltipProvider` (one shared delay). Content is supplementary
 * — never the only place a label lives. Icon buttons already carry a title.
 */
export function Tooltip({ content, shortcut, side = "top", mono, delay, keepOnPress, children }: TooltipProps) {
  const root = (
    <RadixTooltip.Root {...(delay !== undefined ? { delayDuration: delay } : {})}>
      <RadixTooltip.Trigger asChild {...(keepOnPress ? { onPointerDown: handled, onClick: handled } : {})}>{children}</RadixTooltip.Trigger>
      <RadixTooltip.Portal>
        <RadixTooltip.Content className={cx(styles["content"], mono && styles["mono"])} side={side} sideOffset={4}
          {...(keepOnPress ? { onPointerDownOutside: handled } : {})}>
          {content}
          {shortcut ? <kbd className={styles["kbd"]}>{shortcut}</kbd> : null}
          <RadixTooltip.Arrow className={styles["arrow"]} width={8} height={4} />
        </RadixTooltip.Content>
      </RadixTooltip.Portal>
    </RadixTooltip.Root>
  );
  // Radix refuses a tooltip outside a provider. A component that carries
  // one (a PR chip, a cost) must still render where nobody set one up — a
  // test, a static page — so it brings its own there.
  return useContext(HasProvider) ? root : <Provider>{root}</Provider>;
}

/** Radix skips its own close for an event already handled. */
const handled = (e: { preventDefault(): void }) => e.preventDefault();

const HasProvider = createContext(false);

function Provider({ children }: { readonly children?: ReactNode }) {
  return (
    <RadixTooltip.Provider delayDuration={400} skipDelayDuration={200}>
      <HasProvider.Provider value>{children}</HasProvider.Provider>
    </RadixTooltip.Provider>
  );
}

export function TooltipProvider({ children }: { readonly children?: ReactNode }) {
  return <Provider>{children}</Provider>;
}

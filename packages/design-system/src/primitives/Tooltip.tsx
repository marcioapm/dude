import * as RadixTooltip from "@radix-ui/react-tooltip";
import { createContext, useContext, useRef, useState, type ReactNode } from "react";
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
  // keepOnPress: the tooltip's state is ours, so a close asked for while the
  // trigger is pressed is simply not taken. No event is cancelled: the press
  // reaches every listener, and the trigger's own behaviour is untouched.
  const [open, setOpen] = useState(false);
  const pressed = useRef(false);
  const onOpenChange = (next: boolean) => {
    if (next || !pressed.current) setOpen(next);
  };
  const pressKeeper = {
    onPointerDown: () => {
      pressed.current = true;
      // Whichever ends the press, up or cancel, takes both listeners with it.
      const ends = new AbortController();
      const release = () => {
        ends.abort();
        // After the click that follows the release, so its close is not taken either.
        setTimeout(() => (pressed.current = false));
      };
      document.addEventListener("pointerup", release, { signal: ends.signal });
      document.addEventListener("pointercancel", release, { signal: ends.signal });
    },
  };
  const root = (
    <RadixTooltip.Root {...(delay !== undefined ? { delayDuration: delay } : {})} {...(keepOnPress ? { open, onOpenChange } : {})}>
      <RadixTooltip.Trigger asChild {...(keepOnPress ? pressKeeper : {})}>{children}</RadixTooltip.Trigger>
      <RadixTooltip.Portal>
        <RadixTooltip.Content className={cx(styles["content"], mono && styles["mono"])} side={side} sideOffset={4}>
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

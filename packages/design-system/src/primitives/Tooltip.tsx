import * as RadixTooltip from "@radix-ui/react-tooltip";
import { createContext, useContext, useRef, useState, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { modKey } from "../util/keys.ts";
import styles from "./Tooltip.module.css";

export interface TooltipProps {
  readonly content: ReactNode;
  /**
   * Keyboard shortcut hint shown after the content. Keys as a list are
   * drawn as caps, as `KeyHint` draws them (`["mod", "B"]`: ⌘ or Ctrl, B).
   */
  readonly shortcut?: string | ReadonlyArray<string> | undefined;
  readonly side?: "top" | "right" | "bottom" | "left" | undefined;
  readonly mono?: boolean | undefined;
  readonly delay?: number | undefined;
  /**
   * Stay open when the trigger is pressed. For a trigger that does nothing on
   * a press (things shown but not yours to use): the press is when the
   * person most needs the tooltip's words, so it must not close them.
   */
  readonly keepOnPress?: boolean | undefined;
  /**
   * Never open. For a trigger whose tooltip comes and goes with its data:
   * the tree stays the same, so the trigger keeps focus across the change.
   * Disabling closes it; re-enabling waits for a fresh focus or hover.
   */
  readonly disabled?: boolean | undefined;
  /** The trigger. Must accept a ref and forward props (asChild). */
  readonly children: ReactNode;
}

/**
 * Tooltip. Wrap the app once in `TooltipProvider` (one shared delay). Content is supplementary
 * — never the only place a label lives. Icon buttons already carry a title.
 */
export function Tooltip({ content, shortcut, side = "top", mono, delay, keepOnPress, disabled, children }: TooltipProps) {
  // The open state is always ours. Radix, left uncontrolled, would keep an
  // open from before `disabled` and show it again on re-enable with no
  // focus or hover on the trigger. keepOnPress: a close asked for while the
  // trigger is pressed is not taken; no event is cancelled.
  const [open, setOpen] = useState(false);
  const pressed = useRef(false);
  if (disabled && open) setOpen(false);
  const onOpenChange = (next: boolean) => {
    if (disabled) return;
    if (next || !(keepOnPress && pressed.current)) setOpen(next);
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
    <RadixTooltip.Root {...(delay !== undefined ? { delayDuration: delay } : {})} open={open && !disabled} onOpenChange={onOpenChange}>
      <RadixTooltip.Trigger asChild {...(keepOnPress && !disabled ? pressKeeper : {})}>{children}</RadixTooltip.Trigger>
      <RadixTooltip.Portal>
        <RadixTooltip.Content className={cx(styles["content"], mono && styles["mono"])} side={side} sideOffset={4}>
          {content}
          {typeof shortcut === "string" ? (
            <kbd className={styles["kbd"]}>{shortcut}</kbd>
          ) : shortcut ? (
            <span className={styles["keys"]}>
              {shortcut.map((k, i) => (
                <kbd key={i} className={styles["cap"]}>{k === "mod" ? modKey() : k}</kbd>
              ))}
            </span>
          ) : null}
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

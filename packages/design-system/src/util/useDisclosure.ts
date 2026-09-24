import { useCallback, useMemo, useRef, useState, type KeyboardEvent } from "react";

export interface DisclosureOptions {
  /** Controlled open state. When given, the hook never holds state of its own. */
  readonly expanded?: boolean | undefined;
  readonly defaultExpanded?: boolean | undefined;
  readonly onExpandedChange?: ((open: boolean) => void) | undefined;
}

export interface Disclosure {
  readonly open: boolean;
  readonly controlled: boolean;
  /** The user has toggled it at least once; programmatic reveals should then leave it alone. */
  readonly touched: boolean;
  /** Flip it, as a click would. */
  readonly toggle: () => void;
  /**
   * Set it programmatically (a card that opens itself when its call fails).
   * A no-op when controlled, or once the user has chosen for themselves.
   */
  readonly reveal: (open: boolean) => void;
  /** Enter / Space activate, like a native button. */
  readonly onKeyDown: (e: KeyboardEvent<HTMLElement>) => void;
}

/** A keydown handler that runs `fn` on Enter or Space, like a native button. */
export function activateOnKey(fn: () => void): (e: KeyboardEvent<HTMLElement>) => void {
  return (e) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      fn();
    }
  };
}

/**
 * Open/closed state for a row that toggles a body: controlled when
 * `expanded` is given, uncontrolled otherwise, with the change callback
 * fired either way. Shared by ThinkingBlock, ToolCallCard and ChatThread
 * so they cannot drift on how a header answers a click or a key.
 */
export function useDisclosure({ expanded, defaultExpanded, onExpandedChange }: DisclosureOptions): Disclosure {
  const controlled = expanded !== undefined;
  const [internal, setInternal] = useState(defaultExpanded ?? false);
  const [touched, setTouched] = useState(false);
  const open = expanded ?? internal;
  // Read through a ref so `toggle` can be stable yet see the latest state.
  const latest = useRef({ open, controlled, touched, onExpandedChange });
  latest.current = { open, controlled, touched, onExpandedChange };

  const toggle = useCallback(() => {
    const next = !latest.current.open;
    if (!latest.current.controlled) setInternal(next);
    setTouched(true);
    latest.current.onExpandedChange?.(next);
  }, []);

  const reveal = useCallback((next: boolean) => {
    if (latest.current.controlled || latest.current.touched || latest.current.open === next) return;
    setInternal(next);
    latest.current.onExpandedChange?.(next);
  }, []);

  const onKeyDown = useMemo(() => activateOnKey(toggle), [toggle]);

  return { open, controlled, touched, toggle, reveal, onKeyDown };
}

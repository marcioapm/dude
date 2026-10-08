import { useEffect, useRef } from "react";

/**
 * Calls `fn` now, then every `ms` while the page is visible: never while it
 * is hidden (a background tab polls nothing), and once as soon as it is
 * shown again, so what it reads is fresh when someone looks.
 */
export function useVisibleInterval(fn: () => void, ms: number): void {
  const latest = useRef(fn);
  latest.current = fn;
  useEffect(() => {
    const run = () => latest.current();
    if (!document.hidden) run();
    const t = setInterval(() => {
      if (!document.hidden) run();
    }, ms);
    const shown = () => {
      if (!document.hidden) run();
    };
    document.addEventListener("visibilitychange", shown);
    return () => {
      clearInterval(t);
      document.removeEventListener("visibilitychange", shown);
    };
  }, [ms]);
}

import { useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import styles from "./ClampedBody.module.css";

/**
 * Clamps its child to `lines` and offers "Show all" only when the content
 * actually overflows — measured after layout, so a four-line prompt gets
 * no control and a forty-line one gets exactly one. A prompt in the Chat
 * and a proposed task's goal on its card use it alike.
 */
export function ClampedBody({ lines, className, children }: { readonly lines: number | null; readonly className?: string | undefined; readonly children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [overflows, setOverflows] = useState(false);
  const [limit, setLimit] = useState<number | null>(null);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || lines === null) return;
    const measure = () => {
      // The body's own line box: it differs by density and when Markdown
      // switches a multi-block reply to prose leading. The body is the
      // child, so a `1lh` on this wrapper would read the wrong leading.
      const line = Number.parseFloat(getComputedStyle(el.firstElementChild ?? el).lineHeight);
      if (!Number.isFinite(line)) return;
      setLimit(lines * line);
      setOverflows(el.scrollHeight > lines * line + line / 2);
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    // Content that changes size (a prompt that finishes arriving) re-measures
    // through the observer, not through a dependency on `children`, which
    // is a new node every render.
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [lines]);

  if (lines === null) return <>{children}</>;
  const clamped = overflows && !open && limit !== null;
  return (
    <div className={cx(styles["clamp"], clamped && styles["clamped"], className)} data-clamped={clamped ? "true" : undefined}>
      <div ref={ref} className={styles["clampInner"]} style={clamped ? { maxHeight: limit } : undefined}>
        {children}
      </div>
      {overflows ? (
        <button type="button" className={styles["clampToggle"]} onClick={() => setOpen((o) => !o)} aria-expanded={open}>
          <Icon name={open ? "chevron-up" : "chevron-down"} size={11} strokeWidth={2} />
          {open ? "Show less" : "Show all"}
        </button>
      ) : null}
    </div>
  );
}

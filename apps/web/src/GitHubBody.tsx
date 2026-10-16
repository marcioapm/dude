import { useLayoutEffect, useMemo, useRef, useState } from "react";
import { Markdown } from "@dude/design-system/components";
import { Button } from "@dude/design-system/primitives";
import { githubBody, type GitHubSection } from "./githubBody.ts";

// githubBody bounds how deep disclosures nest, so this recursion is bounded too.
function Sections({ sections }: { sections: readonly GitHubSection[] }) {
  return sections.map((section, i) => section.kind === "markdown"
    ? <Markdown key={i} source={section.source} variant="message" breaks unmeasured />
    : <details key={i}>
      <summary>{section.summary}</summary>
      <div className="githubSections"><Sections sections={section.children} /></div>
    </details>);
}

export function GitHubBody({ source }: { source: string }) {
  const sections = useMemo(() => githubBody(source), [source]);
  const ref = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [overflows, setOverflows] = useState(false);
  const [limit, setLimit] = useState<number>();
  const clipped = overflows && !open;

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setOpen(false);
    const measure = () => {
      const line = Number.parseFloat(getComputedStyle(el).lineHeight);
      if (!Number.isFinite(line)) return;
      setLimit(12 * line);
      setOverflows(el.scrollHeight > 12 * line + 1);
    };
    measure();
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(el);
    // Opening a disclosure can make a previously short quote overflow.
    el.addEventListener("toggle", measure, true);
    return () => {
      observer?.disconnect();
      el.removeEventListener("toggle", measure, true);
    };
  }, [sections]);

  return <div className="githubBody">
    {/* Tabbing into the clipped tail shows it all, so focus never lands out of sight. */}
    <div ref={ref} className="githubSections" onFocus={clipped ? (event) => {
      if (event.target.getBoundingClientRect().bottom > event.currentTarget.getBoundingClientRect().bottom) setOpen(true);
    } : undefined}
      style={{ maxHeight: open ? undefined : limit, overflow: open ? undefined : "hidden" }}>
      <Sections sections={sections} />
    </div>
    {overflows ? <Button size="sm" variant="quiet" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
      {open ? "Show less" : "Show all"}
    </Button> : null}
  </div>;
}

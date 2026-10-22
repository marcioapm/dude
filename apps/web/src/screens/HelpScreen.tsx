/**
 * Help: dude's own guide, the pages the orchestrator embeds — the same
 * text agents find with search_memory and link people to. A list of the
 * pages, and the page picked; a section named in the URL is scrolled to.
 */

import { useEffect, useState } from "react";
import { Markdown, ScreenHeader } from "@dude/design-system/components";
import { Callout, Spinner } from "@dude/design-system/primitives";
import type { ApiClient, HelpPage } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { formatPlace } from "../place.ts";

/** A section's element id: the guide's anchor, apart from the Markdown's own heading ids. */
export const helpSectionId = (anchor: string) => `help-${anchor || "intro"}`;

export function HelpScreen({ client, page, section, onPage }: {
  client: ApiClient;
  page?: string | undefined;
  section?: string | undefined;
  onPage: (page: string) => void;
}) {
  const [pages, setPages] = useState<HelpPage[] | null>(null);
  const [shown, setShown] = useState<HelpPage | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const slug = page ?? (pages?.some((p) => p.slug === "overview") ? "overview" : pages?.[0]?.slug);

  useEffect(() => {
    let current = true;
    client.helpPages().then(
      (out) => current && setPages(out.pages),
      (err: unknown) => current && setProblem(`Could not read the guide: ${errorText(err)}`),
    );
    return () => {
      current = false;
    };
  }, [client]);

  useEffect(() => {
    if (!slug) return;
    let current = true;
    setShown(null);
    client.helpPage(slug).then(
      (p) => {
        if (!current) return;
        setShown(p);
        setProblem(null);
      },
      (err: unknown) => current && setProblem(`Could not read this page: ${errorText(err)}`),
    );
    return () => {
      current = false;
    };
  }, [client, slug]);

  useEffect(() => {
    if (shown && section) document.getElementById(helpSectionId(section))?.scrollIntoView({ block: "start" });
  }, [shown, section]);

  return (
    <div className="helpScreen" data-testid="help">
      <ScreenHeader title="Help" />
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      <div className="helpLayout">
        <nav aria-label="Guide pages" className="helpPages">
          {pages === null ? <Spinner label="Loading…" /> : (
            <ul>
              {pages.map((p) => (
                <li key={p.slug}>
                  <a href={formatPlace({ view: "help", page: p.slug })} aria-current={p.slug === slug ? "page" : undefined}
                    data-testid="help-page-link" onClick={(e) => {
                      e.preventDefault();
                      onPage(p.slug);
                    }}>
                    {p.title}
                  </a>
                </li>
              ))}
            </ul>
          )}
        </nav>
        <article className="helpPage" data-testid="help-page" data-page={shown?.slug}>
          {shown ? (
            <>
              <h1>{shown.title}</h1>
              {shown.sections.map((s) => (
                <section key={s.anchor} id={helpSectionId(s.anchor)} data-anchor={s.anchor}>
                  {s.heading ? <h2>{s.heading}</h2> : null}
                  <Markdown variant="document" linkTarget="_self" source={(s.text ?? "").replace(
                    /\]\(help:([a-z0-9]+(?:-[a-z0-9]+)*)(?:#([a-z0-9]+(?:-[a-z0-9]+)*)?)?\)/g,
                    (_, page: string, section?: string) => `](${formatPlace({ view: "help", page, ...(section ? { section } : {}) })})`,
                  )} />
                </section>
              ))}
            </>
          ) : slug && !problem ? <Spinner label="Loading…" /> : null}
        </article>
      </div>
    </div>
  );
}

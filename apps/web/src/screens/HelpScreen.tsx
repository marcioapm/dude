/**
 * Help: dude's own guide, the pages the orchestrator embeds — the same
 * text agents find with search_memory and link people to. Beside the page,
 * kept in view: a search over the guide, and the pages with the current
 * one's sections, the one in view in bold. A section named in the URL is
 * scrolled to; picking one names it in the URL.
 */

import { useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type ReactElement } from "react";
import { DocOutline, DocSearchResults, Markdown, ScreenHeader, docResultId, queryWords, type DocSearchResult } from "@dude/design-system/components";
import { Callout, SearchField, Spinner } from "@dude/design-system/primitives";
import type { ApiClient, HelpPage } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { formatPlace } from "../place.ts";

// Keep section anchors separate from Markdown's own heading ids.
export function helpSectionId(anchor: string): string {
  return `help-${anchor || "intro"}`;
}

function resolveHelpLinks(text: string): string {
  return text.replace(
    /\]\(help:([a-z0-9]+(?:-[a-z0-9]+)*)(?:#([a-z0-9]+(?:-[a-z0-9]+)*)?)?\)/g,
    (_, page: string, section?: string) => `](${formatPlace({ view: "help", page, ...(section ? { section } : {}) })})`,
  );
}

/** The bounds the search route holds to: at most this many words read, this many sections found. */
export const HELP_QUERY_MAX = 200;
export const HELP_RESULTS_MAX = 20;
/** Milliseconds after the last key before the guide is searched. */
export const HELP_SEARCH_DELAY = 200;

/**
 * The section in view: the last whose top has reached `line` (the list's
 * top, plus a little), else the first. `tops` are in document order.
 * `bottom` is given when the pane is scrolled to its end, where a short last
 * section can never reach `line`: then the last whose top is above `bottom`.
 */
export function sectionInView(tops: ReadonlyArray<{ readonly anchor: string; readonly top: number }>, line: number, bottom?: number): string | undefined {
  let seen = tops[0]?.anchor;
  for (const t of tops) if (t.top <= line || (bottom !== undefined && t.top < bottom)) seen = t.anchor;
  return seen;
}

// The box that scrolls the page: the nearest ancestor that scrolls (the app's main pane), else the document.
function scrollerOf(el: HTMLElement): HTMLElement {
  for (let p = el.parentElement; p; p = p.parentElement) if (/auto|scroll/.test(getComputedStyle(p).overflowY)) return p;
  return (document.scrollingElement as HTMLElement | null) ?? document.documentElement;
}

/** A guide section's id (`<page>#<anchor>`) and title (`<page> — <heading>`) as Help shows them. */
export function docResult(r: { readonly id: string; readonly title: string; readonly snippet: string }, pages: ReadonlyArray<HelpPage>): DocSearchResult {
  const hash = r.id.indexOf("#");
  const page = hash < 0 ? r.id : r.id.slice(0, hash);
  const anchor = hash < 0 ? "" : r.id.slice(hash + 1);
  const pageTitle = pages.find((p) => p.slug === page)?.title ?? r.title.split(" — ")[0] ?? page;
  const heading = r.title.startsWith(`${pageTitle} — `) ? r.title.slice(pageTitle.length + 3) : anchor ? r.title : "";
  return { page, anchor, heading, pageTitle, snippet: r.snippet };
}

type HelpScreenProps = {
  client: ApiClient;
  page?: string | undefined;
  section?: string | undefined;
  onPage: (page: string) => void;
  /** A section picked from the list or a search: the URL names it. */
  onSection?: ((page: string, section: string) => void) | undefined;
};

export function HelpScreen({ client, page, section, onPage, onSection }: HelpScreenProps): ReactElement {
  const [pages, setPages] = useState<HelpPage[] | null>(null);
  const [shown, setShown] = useState<HelpPage | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [inView, setInView] = useState<string | undefined>(section);
  const slug = page ?? (pages?.some((p) => p.slug === "overview") ? "overview" : pages?.[0]?.slug);
  const articleRef = useRef<HTMLElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

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

  // The section in view follows the scroll of whatever scrolls the page (the app's main pane).
  useEffect(() => {
    const article = articleRef.current;
    if (!shown || !article) return;
    if (section) document.getElementById(helpSectionId(section))?.scrollIntoView({ block: "start" });
    const scroller = scrollerOf(article);
    const follow = () => {
      const line = (listRef.current?.getBoundingClientRect().top ?? 0) + 24;
      const tops = [...article.querySelectorAll<HTMLElement>("section[data-anchor]")]
        .filter((s) => s.dataset["anchor"])
        .map((s) => ({ anchor: s.dataset["anchor"]!, top: s.getBoundingClientRect().top }));
      const atEnd = scroller.scrollHeight > scroller.clientHeight && scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 1;
      const bottom = scroller === document.scrollingElement ? window.innerHeight : scroller.getBoundingClientRect().bottom;
      setInView(sectionInView(tops, line, atEnd ? bottom : undefined));
    };
    let frame: number | null = null;
    const schedule = () => {
      if (frame !== null) return;
      frame = requestAnimationFrame(() => {
        frame = null;
        follow();
      });
    };
    follow();
    const target = scroller === document.scrollingElement ? document : scroller;
    target.addEventListener("scroll", schedule, { passive: true });
    return () => {
      target.removeEventListener("scroll", schedule);
      if (frame !== null) cancelAnimationFrame(frame);
    };
  }, [shown, section]);

  const openSection = useCallback((p: string, anchor: string) => {
    if (!anchor) return onPage(p);
    if (onSection) onSection(p, anchor);
    else onPage(p);
    // The same section again moves no URL: scroll to it here too.
    if (p === shown?.slug) document.getElementById(helpSectionId(anchor))?.scrollIntoView({ block: "start" });
  }, [onPage, onSection, shown?.slug]);

  // -- search --
  const listId = useId();
  const [query, setQuery] = useState("");
  const [found, setFound] = useState<Awaited<ReturnType<ApiClient["searchMemory"]>>["results"] | null>(null);
  const [searchProblem, setSearchProblem] = useState<string | null>(null);
  const [active, setActive] = useState(0);
  const latest = useRef(0);
  const words = query.trim().slice(0, HELP_QUERY_MAX);
  useEffect(() => {
    const mine = ++latest.current;
    setSearchProblem(null);
    setFound(null);
    if (!queryWords(words).length) return;
    const t = setTimeout(() => {
      client.searchMemory({ q: words, types: "doc", limit: HELP_RESULTS_MAX }).then(
        (out) => {
          if (mine !== latest.current) return;
          setSearchProblem(null);
          setFound(out.results.filter((r) => r.type === ("doc" as string)));
          setActive(0);
        },
        (err: unknown) => {
          if (mine !== latest.current) return;
          setFound(null);
          setSearchProblem(`Could not search the guide: ${errorText(err)}`);
        },
      );
    }, HELP_SEARCH_DELAY);
    return () => clearTimeout(t);
  }, [client, words]);
  const searching = queryWords(words).length > 0;
  const results = searching && found ? found.map((r) => docResult(r, pages ?? [])) : null;
  const onSearchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    const n = results?.length ?? 0;
    if (e.key === "Escape" && query) {
      e.preventDefault();
      setQuery("");
    } else if ((e.key === "ArrowDown" || e.key === "ArrowUp") && n) {
      e.preventDefault();
      const next = (active + (e.key === "ArrowDown" ? 1 : -1) + n) % n;
      setActive(next);
      document.getElementById(docResultId(listId, next))?.scrollIntoView?.({ block: "nearest" });
    } else if (e.key === "Enter") {
      e.preventDefault();
      const r = results?.[active];
      if (r) openSection(r.page, r.anchor);
    }
  };

  const articleSections = useMemo(() => shown?.sections.map((s) => (
    <section key={s.anchor} id={helpSectionId(s.anchor)} data-anchor={s.anchor}>
      {s.heading ? <h2>{s.heading}</h2> : null}
      <Markdown variant="document" unmeasured linkTarget="_self" source={resolveHelpLinks(s.text ?? "")} />
    </section>
  )), [shown]);

  return (
    <div className="helpScreen" data-testid="help">
      <ScreenHeader title="Help" />
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      <div className="helpLayout">
        <div className="helpPages" ref={listRef} data-testid="help-nav">
          <SearchField value={query} onValueChange={setQuery} onKeyDown={onSearchKey} label="Search the guide" placeholder="Search the guide"
            maxLength={HELP_QUERY_MAX} role="combobox" aria-expanded={Boolean(results?.length)} aria-controls={listId} aria-autocomplete="list"
            aria-activedescendant={results?.length ? docResultId(listId, active) : undefined} data-testid="help-search" />
          {searching && searchProblem ? (
            <Callout tone="danger" data-testid="help-search-error">{searchProblem}</Callout>
          ) : searching ? (
            <DocSearchResults results={results} query={words} active={active} onActiveChange={setActive} listId={listId}
              onOpen={(r) => openSection(r.page, r.anchor)} />
          ) : pages === null ? <Spinner label="Loading…" /> : (
            <DocOutline pages={pages} current={slug} currentSection={slug === shown?.slug ? inView : undefined}
              href={(p, s) => formatPlace({ view: "help", page: p, ...(s ? { section: s } : {}) })}
              onPage={onPage} onSection={openSection} />
          )}
        </div>
        <article className="helpPage" ref={articleRef} data-testid="help-page" data-page={shown?.slug}>
          {shown ? (
            <>
              <h1>{shown.title}</h1>
              {articleSections}
            </>
          ) : slug && !problem ? <Spinner label="Loading…" /> : null}
        </article>
      </div>
    </div>
  );
}

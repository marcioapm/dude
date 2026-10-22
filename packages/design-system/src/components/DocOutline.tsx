import { Fragment, type HTMLAttributes, type MouseEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import styles from "./DocOutline.module.css";

/*
 * A guide's navigation, beside the page it shows: the pages, and under the
 * current one its sections, the one in view in bold. Each is a link (its
 * `href`), so it opens in a new tab as a link does; a plain click is the
 * caller's (`onPage`, `onSection`) so it can scroll rather than reload.
 */

export interface DocOutlineSection {
  readonly anchor: string;
  readonly heading: string;
}

export interface DocOutlinePage {
  readonly slug: string;
  readonly title: string;
  readonly sections: ReadonlyArray<DocOutlineSection>;
}

export interface DocOutlineProps extends Omit<HTMLAttributes<HTMLElement>, "onSelect"> {
  readonly pages: ReadonlyArray<DocOutlinePage>;
  /** The page shown. */
  readonly current?: string | undefined;
  /** The section in view on it. */
  readonly currentSection?: string | undefined;
  readonly href: (page: string, section?: string) => string;
  readonly onPage: (page: string) => void;
  readonly onSection: (page: string, section: string) => void;
  /** The list's name; "Guide pages". */
  readonly label?: string | undefined;
}

/** A plain left click, for the caller; a modified one (new tab) is the browser's. */
function plain(e: MouseEvent): boolean {
  return e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey;
}

export function DocOutline({ pages, current, currentSection, href, onPage, onSection, label = "Guide pages", className, ...rest }: DocOutlineProps) {
  return (
    <nav aria-label={label} className={cx(styles["root"], className)} {...rest}>
      <ul className={styles["pages"]}>
        {pages.map((p) => {
          const open = p.slug === current;
          const sections = open ? p.sections.filter((s) => s.heading) : [];
          return (
            <li key={p.slug}>
              <a href={href(p.slug)} className={styles["page"]} aria-current={open ? "page" : undefined} data-testid="help-page-link"
                onClick={(e) => {
                  if (!plain(e)) return;
                  e.preventDefault();
                  onPage(p.slug);
                }}>
                {p.title}
              </a>
              {sections.length ? (
                <ul className={styles["sections"]} aria-label={`${p.title}: sections`}>
                  {sections.map((s) => (
                    <li key={s.anchor}>
                      <a href={href(p.slug, s.anchor)} className={styles["section"]} data-testid="help-section-link" data-anchor={s.anchor}
                        aria-current={s.anchor === currentSection ? "location" : undefined}
                        onClick={(e) => {
                          if (!plain(e)) return;
                          e.preventDefault();
                          onSection(p.slug, s.anchor);
                        }}>
                        {s.heading}
                      </a>
                    </li>
                  ))}
                </ul>
              ) : null}
            </li>
          );
        })}
      </ul>
    </nav>
  );
}

export interface DocSearchResult {
  readonly page: string;
  readonly anchor: string;
  readonly heading: string;
  readonly pageTitle: string;
  readonly snippet: string;
}

export interface DocSearchResultsProps extends Omit<HTMLAttributes<HTMLDivElement>, "onSelect" | "results"> {
  /** null while the first answer is on its way. */
  readonly results: ReadonlyArray<DocSearchResult> | null;
  /** The words searched: they are marked in each snippet and heading. */
  readonly query: string;
  /** The keyboard's row, as the field's `aria-activedescendant`. */
  readonly active: number;
  readonly onActiveChange: (index: number) => void;
  readonly onOpen: (result: DocSearchResult) => void;
  /** For the field: `aria-controls` and the option ids. */
  readonly listId: string;
}

/** An option's id in `DocSearchResults`, for the field's `aria-activedescendant`. */
export const docResultId = (listId: string, i: number) => `${listId}-${i}`;

/**
 * What a guide search found, in place of the page list: "N sections", then
 * one option per section — its heading, its page, and the snippet with the
 * words marked. A listbox driven from the field (↑↓ move, Enter opens);
 * the pointer opens on press, keeping the field's focus.
 */
export function DocSearchResults({ results, query, active, onActiveChange, onOpen, listId, className, ...rest }: DocSearchResultsProps) {
  const count = results?.length ?? 0;
  return (
    <div className={cx(styles["results"], className)} {...rest}>
      <p className={styles["count"]} role="status" data-testid="help-search-count">
        {results === null ? "Searching…" : count === 1 ? "1 section" : `${count} sections`}
      </p>
      {results && count === 0 ? (
        <p className={styles["empty"]} data-testid="help-search-empty">Nothing in the guide matches “{query.trim()}”.</p>
      ) : null}
      <ul id={listId} role="listbox" aria-label="Guide sections found" className={styles["list"]} hidden={!count}>
        {results?.map((r, i) => (
          <li key={`${r.page}#${r.anchor}`} id={docResultId(listId, i)} role="option" aria-selected={i === active}
            className={cx(styles["result"], i === active && styles["active"])} data-testid="help-search-result" data-page={r.page} data-anchor={r.anchor}
            onMouseDown={(e) => {
              e.preventDefault();
              onOpen(r);
            }}
            onMouseEnter={() => onActiveChange(i)}>
            <span className={styles["heading"]}><Marked text={r.heading || r.pageTitle} query={query} /></span>
            <span className={styles["pageTitle"]}>{r.pageTitle}</span>
            <span className={styles["snippet"]}><Marked text={r.snippet} query={query} /></span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** The query's words, at least two letters each, lowercased. */
export function queryWords(query: string): string[] {
  return [...new Set(query.toLowerCase().split(/[^\p{L}\p{N}]+/u).filter((w) => w.length >= 2))];
}

/**
 * `text` split into runs, each marked when it begins one of the query's
 * words. A word is matched by its stem's first letters (reviewer, reviewers,
 * reviewed), as the search's own English stemming finds them.
 */
export function markWords(text: string, query: string): Array<{ readonly text: string; readonly marked: boolean }> {
  const stems = queryWords(query).map((w) => (w.length > 5 ? w.slice(0, w.length - 2) : w));
  if (!stems.length) return [{ text, marked: false }];
  const escaped = stems.map((s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).sort((a, b) => b.length - a.length);
  const re = new RegExp(`(?<![\\p{L}\\p{N}])(?:${escaped.join("|")})[\\p{L}\\p{N}]*`, "giu");
  const out: Array<{ text: string; marked: boolean }> = [];
  let at = 0;
  for (const m of text.matchAll(re)) {
    if (m.index! > at) out.push({ text: text.slice(at, m.index), marked: false });
    out.push({ text: m[0], marked: true });
    at = m.index! + m[0].length;
  }
  if (at < text.length) out.push({ text: text.slice(at), marked: false });
  return out;
}

function Marked({ text, query }: { readonly text: string; readonly query: string }): ReactNode {
  return markWords(text, query).map((run, i) => (run.marked ? <mark key={i} className={styles["mark"]}>{run.text}</mark> : <Fragment key={i}>{run.text}</Fragment>));
}

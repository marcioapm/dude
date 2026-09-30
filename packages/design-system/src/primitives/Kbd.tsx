import type { ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { modKey } from "../util/keys.ts";
import { FORMAT_KEYS, type MarkdownFormat } from "../util/markdownEdit.ts";
import styles from "./Kbd.module.css";

/** One key cap. `mod` is the platform's shortcut modifier: ⌘ on Apple devices, Ctrl elsewhere. */
export function Kbd({ children, className }: { readonly children: ReactNode; readonly className?: string | undefined }) {
  return <kbd className={cx(styles["kbd"], className)}>{children === "mod" ? modKey() : children}</kbd>;
}

/** A shortcut and what it does, muted: `Ctrl` `Enter` create. */
export function KeyHint({ keys, children }: { readonly keys: ReadonlyArray<string>; readonly children: ReactNode }) {
  return (
    <span className={styles["hint"]}>
      <span className={styles["keys"]}>
        {keys.map((k, i) => (
          <Kbd key={i}>{k}</Kbd>
        ))}
      </span>
      {children}
    </span>
  );
}

/**
 * Help beside a form: a small-caps title over a short list in secondary
 * ink. The title is an h3: it sits under a dialog's title, which is the h2.
 */
export function HelpList({ title, items }: { readonly title: string; readonly items: ReadonlyArray<ReactNode> }) {
  return (
    <section className={styles["help"]}>
      <h3 className={cx("ds-label", styles["helpTitle"])}>{title}</h3>
      <ul className={styles["helpList"]}>
        {items.map((item, i) => (
          <li key={i}>{item}</li>
        ))}
      </ul>
    </section>
  );
}

// What each shortcut's format looks like in the source; the keys come from FORMAT_KEYS.
const SHORTCUT_SOURCE: Readonly<Partial<Record<MarkdownFormat, string>>> = { bold: "**bold**", italic: "_italic_", link: "[text](url)", code: "`code`" };
const WITHOUT_SHORTCUT: ReadonlyArray<readonly [string, string]> = [
  ["- [ ] item", "a checklist item"],
  ["## Heading", "a section"],
];

export interface MarkdownCheatsheetProps {
  readonly title?: string | undefined;
  /**
   * More `[source, meaning]` rows, for what the text means where it is
   * written. A row whose source is already listed gives it that meaning
   * instead of adding a row: `["- [ ] item", "a criterion"]`. A shortcut
   * row keeps its keys after the meaning.
   */
  readonly extra?: ReadonlyArray<readonly [string, string]> | undefined;
}

/** The Markdown `MarkdownEditor` reads, and its shortcuts, for the column beside it. */
export function MarkdownCheatsheet({ title = "Markdown", extra = [] }: MarkdownCheatsheetProps) {
  // One row per source, in order of first appearance: shortcuts, the rows without one, then extras.
  const rows = new Map<string, { meaning?: string; keys?: ReadonlyArray<string> }>();
  for (const f of Object.keys(FORMAT_KEYS) as MarkdownFormat[]) {
    const source = SHORTCUT_SOURCE[f];
    if (source) rows.set(source, { keys: ["mod", FORMAT_KEYS[f]!.toUpperCase()] });
  }
  for (const [source, meaning] of [...WITHOUT_SHORTCUT, ...extra]) rows.set(source, { ...rows.get(source), meaning });
  return (
    <section className={styles["help"]}>
      <h3 className={cx("ds-label", styles["helpTitle"])}>{title}</h3>
      <dl className={styles["cheats"]}>
        {[...rows].map(([source, { meaning, keys }]) => (
          <div key={source} className={styles["cheat"]}>
            <dt>
              <code className={styles["source"]}>{source}</code>
            </dt>
            <dd>
              {meaning}
              {meaning && keys ? " " : null}
              {keys ? (
                <span className={styles["keys"]}>
                  {keys.map((k) => (
                    <Kbd key={k}>{k}</Kbd>
                  ))}
                </span>
              ) : null}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

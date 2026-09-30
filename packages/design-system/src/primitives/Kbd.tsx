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

/** Help beside a form: a small-caps title over a short list in secondary ink. */
export function HelpList({ title, children }: { readonly title: string; readonly children: ReactNode }) {
  return (
    <section className={styles["help"]}>
      <h3 className={cx("ds-label", styles["helpTitle"])}>{title}</h3>
      <ul className={styles["helpList"]}>{children}</ul>
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
   * written. A row whose source is already listed replaces its meaning:
   * `["- [ ] item", "a criterion"]`.
   */
  readonly extra?: ReadonlyArray<readonly [string, string]> | undefined;
}

/** The Markdown `MarkdownEditor` reads, and its shortcuts, for the column beside it. */
export function MarkdownCheatsheet({ title = "Markdown", extra = [] }: MarkdownCheatsheetProps) {
  const shortcuts = (Object.keys(FORMAT_KEYS) as MarkdownFormat[]).flatMap((f) => {
    const source = SHORTCUT_SOURCE[f];
    return source ? [[source, ["mod", FORMAT_KEYS[f]!.toUpperCase()]] as const] : [];
  });
  const meanings = new Map<string, string>(WITHOUT_SHORTCUT);
  for (const [source, what] of extra) meanings.set(source, what);
  const rows: ReadonlyArray<readonly [string, ReadonlyArray<string> | string]> = [...shortcuts, ...meanings];
  return (
    <section className={styles["help"]}>
      <h3 className={cx("ds-label", styles["helpTitle"])}>{title}</h3>
      <dl className={styles["cheats"]}>
        {rows.map(([source, what]) => (
          <div key={source} className={styles["cheat"]}>
            <dt>
              <code className={styles["source"]}>{source}</code>
            </dt>
            <dd>
              {typeof what === "string" ? (
                what
              ) : (
                <span className={styles["keys"]}>
                  {what.map((k) => (
                    <Kbd key={k}>{k}</Kbd>
                  ))}
                </span>
              )}
            </dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

import type { ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { modKey } from "../util/keys.ts";
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

const CHEATS: ReadonlyArray<readonly [string, ReadonlyArray<string> | string]> = [
  ["**bold**", ["mod", "B"]],
  ["_italic_", ["mod", "I"]],
  ["[text](url)", ["mod", "K"]],
  ["`code`", ["mod", "E"]],
  ["- [ ] item", "a criterion"],
  ["## Heading", "a section"],
];

/** The Markdown `MarkdownEditor` reads, and its shortcuts, for the column beside it. */
export function MarkdownCheatsheet({ title = "Markdown" }: { readonly title?: string | undefined }) {
  return (
    <section className={styles["help"]}>
      <h3 className={cx("ds-label", styles["helpTitle"])}>{title}</h3>
      <dl className={styles["cheats"]}>
        {CHEATS.map(([source, what]) => (
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

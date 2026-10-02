import { lazy, Suspense, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { SkeletonLines } from "../primitives/Feedback.tsx";
import styles from "./CodeEditor.module.css";

/*
 * A code editor: CodeMirror 6, loaded only when one is on screen. The
 * editor is a chunk of its own (about 120 KB gzipped): an import of
 * CodeEditor costs nothing until it renders, and while the chunk loads the
 * frame shows the text's lines as a skeleton of the same height.
 *
 * The language, its lint and its completions are the caller's: the design
 * system knows Containerfiles no better than any other text.
 */

export interface CodeDiagnostic {
  readonly severity: "error" | "warning";
  /** 1-based line, 0-based columns on it [from, to). */
  readonly line: number;
  readonly from: number;
  readonly to: number;
  readonly message: string;
}

export interface CodeCompletion {
  /** What is inserted. */
  readonly label: string;
  /** Muted after it: "Acme's default base · v7". */
  readonly detail?: string | undefined;
  /** keyword: an instruction; image: something to build on. */
  readonly type?: "keyword" | "image" | "text" | undefined;
  /** Sorts first among those that match. */
  readonly boost?: number | undefined;
}

export interface CodeCompletionContext {
  /** The line up to the cursor. */
  readonly before: string;
  /** The word being typed (letters, digits, and :/.-_@). */
  readonly word: string;
}

export interface CodeEditorProps {
  readonly value: string;
  readonly onChange?: ((value: string) => void) | undefined;
  /** `dockerfile` for a Containerfile; `plain` for none. */
  readonly language?: "dockerfile" | "plain" | undefined;
  /** Marked in place, and in the gutter; recomputed by the caller on change. */
  readonly diagnostics?: ReadonlyArray<CodeDiagnostic> | undefined;
  /** What Ctrl-Space (and typing) offers at the cursor; null for nothing. */
  readonly complete?: ((ctx: CodeCompletionContext) => ReadonlyArray<CodeCompletion> | null) | undefined;
  readonly readOnly?: boolean | undefined;
  /** The editor's name, for a screen reader. */
  readonly "aria-label": string;
  /** Above the text, on the chrome shade: its name, its version, its keys. */
  readonly header?: ReactNode;
  /** Under the text, read-only and on a tint: what is added after it. */
  readonly after?: ReactNode;
  /** Under everything, on the chrome shade: counts, and what won't build. */
  readonly footer?: ReactNode;
  /** The least the text area is, in lines. */
  readonly minLines?: number | undefined;
  readonly className?: string | undefined;
  readonly "data-testid"?: string | undefined;
}

const Core = lazy(() => import("./CodeEditorCore.tsx"));

/**
 * A framed code editor. The frame (header, the dude layer under the text,
 * the footer) is drawn at once; the text area is CodeMirror, loaded the
 * first time an editor renders.
 */
export function CodeEditor(props: CodeEditorProps) {
  const { header, after, footer, className, minLines = 12, value } = props;
  const lines = Math.max(minLines, value.split("\n").length);
  return (
    <div className={cx(styles["frame"], className)} data-testid={props["data-testid"]}>
      {header ? <div className={styles["header"]}>{header}</div> : null}
      <div className={styles["text"]} style={{ minHeight: `calc(${minLines} * var(--ds-code-line))` }}>
        <Suspense
          fallback={
            <div className={styles["loading"]} aria-busy="true" aria-label={`${props["aria-label"]}, loading`}>
              <SkeletonLines lines={Math.min(lines, 12)} />
            </div>
          }
        >
          <Core {...props} />
        </Suspense>
      </div>
      {after ? <div className={styles["after"]}>{after}</div> : null}
      {footer ? <div className={styles["footer"]}>{footer}</div> : null}
    </div>
  );
}

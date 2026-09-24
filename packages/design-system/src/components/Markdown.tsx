import { useMemo, useState, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { outline as buildOutline, parseMarkdown, type Block, type Inline } from "../util/markdown.ts";
import { DiffView, parseUnifiedDiff } from "./DiffView.tsx";
import styles from "./Markdown.module.css";

export type MarkdownVariant = "message" | "document";

export interface MarkdownProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  readonly source: string;
  /**
   * `message` (default): a chat turn. One block reads at chat leading
   * (1.375); more than one switches to the long-form rhythm (1.5, 0.75em
   * between blocks). No outline. `document`: a published artifact read in full. Wider measure,
   * more air between sections, an optional heading outline.
   */
  readonly variant?: MarkdownVariant | undefined;
  /**
   * The source is still arriving. Unterminated constructs are rendered as
   * open (an unclosed fence is still a code block, an open `**` is still
   * bold) so nothing flickers when the closer lands. A caret marks the end.
   */
  readonly streaming?: boolean | undefined;
  /** Document variant only: show a heading outline beside the text. */
  readonly outline?: boolean | undefined;
  /** Where links open. Defaults to a new tab with `rel="noopener noreferrer"`. */
  readonly linkTarget?: "_blank" | "_self" | undefined;
  /** Render fenced ```diff / ```patch blocks with DiffView (default true). */
  readonly diffs?: boolean | undefined;
}

/**
 * Markdown from untrusted sources (models, repository files), rendered to
 * React elements from a typed AST — never an HTML string. Raw HTML in the
 * source shows literally; `javascript:` / `data:` URLs are dropped.
 *
 * Code blocks share their type with `LogStream`; a ```diff block hands off
 * to `DiffView` so diff colouring exists in exactly one place.
 */
export function Markdown({
  source,
  variant = "message",
  streaming,
  outline,
  linkTarget = "_blank",
  diffs = true,
  className,
  ...rest
}: MarkdownProps) {
  const blocks = useMemo(() => parseMarkdown(source, { streaming: streaming ?? false }), [source, streaming]);
  const headings = useMemo(() => (outline && variant === "document" ? buildOutline(blocks) : []), [blocks, outline, variant]);
  const ctx: RenderCtx = { linkTarget, diffs, streaming: streaming === true };

  const body = (
    <div className={cx(styles["root"], variant === "document" ? styles["document"] : styles["message"], variant === "message" && blocks.length > 1 && styles["long"], streaming && styles["streaming"], className)} {...rest}>
      {blocks.length === 0 && streaming ? (
        <p className={styles["p"]}>
          <Caret />
        </p>
      ) : (
        blocks.map((b, i) => <BlockNode key={i} block={b} ctx={ctx} last={i === blocks.length - 1} />)
      )}
    </div>
  );

  if (headings.length < 2) return body;
  return (
    <div className={styles["withOutline"]}>
      {body}
      <nav className={styles["outline"]} aria-label="Outline">
        <div className={styles["outlineTitle"]}>On this page</div>
        {headings.map((h) => (
          <a key={h.id} href={`#${h.id}`} className={styles["outlineLink"]} style={{ paddingLeft: `${(h.level - 1) * 10}px` }}>
            {h.text}
          </a>
        ))}
      </nav>
    </div>
  );
}

interface RenderCtx {
  readonly linkTarget: "_blank" | "_self";
  readonly diffs: boolean;
  readonly streaming: boolean;
}

/** The streaming caret. Base style is solid so reduced motion leaves it visible. */
function Caret() {
  return <span className={styles["caret"]} aria-hidden />;
}

function BlockNode({ block, ctx, last }: { readonly block: Block; readonly ctx: RenderCtx; readonly last: boolean }) {
  const tail = last && ctx.streaming ? <Caret /> : null;
  switch (block.t) {
    case "heading": {
      const Tag = `h${block.level}` as const;
      return (
        <Tag id={block.id} className={cx(styles["h"], styles[`h${block.level}`])}>
          <Inlines nodes={block.c} ctx={ctx} />
          {tail}
        </Tag>
      );
    }
    case "paragraph":
      return (
        <p className={styles["p"]}>
          <Inlines nodes={block.c} ctx={ctx} />
          {tail}
        </p>
      );
    case "code":
      return <CodeBlock lang={block.lang} value={block.v} open={block.open} ctx={ctx} tail={tail} />;
    case "quote":
      return (
        <blockquote className={styles["quote"]}>
          {block.c.map((b, i) => (
            <BlockNode key={i} block={b} ctx={ctx} last={last && i === block.c.length - 1} />
          ))}
        </blockquote>
      );
    case "list": {
      const Tag = block.ordered ? "ol" : "ul";
      const isTask = block.items.some((it) => it.task !== null);
      return (
        <Tag className={cx(styles["list"], isTask && styles["taskList"])} start={block.ordered && block.start !== 1 ? block.start : undefined}>
          {block.items.map((it, i) => (
            <li key={i} className={cx(styles["li"], it.task !== null && styles["task"], it.task === true && styles["taskDone"])}>
              {it.task !== null ? (
                <span className={styles["taskMark"]} aria-hidden>
                  <Icon name={it.task ? "check" : "circle"} size={11} strokeWidth={2} />
                </span>
              ) : null}
              <span className={styles["liBody"]}>
                {it.c.length === 0 && last && i === block.items.length - 1 && ctx.streaming ? <Caret /> : null}
                {it.c.map((b, j) => (
                  <BlockNode key={j} block={b} ctx={ctx} last={last && i === block.items.length - 1 && j === it.c.length - 1} />
                ))}
              </span>
            </li>
          ))}
        </Tag>
      );
    }
    case "table":
      return (
        <div className={styles["tableWrap"]}>
          <table className={styles["table"]}>
            <thead>
              <tr>
                {block.head.map((c, i) => (
                  <th key={i} style={alignStyle(block.align[i] ?? null)}>
                    <Inlines nodes={c} ctx={ctx} />
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {block.rows.map((r, ri) => (
                <tr key={ri}>
                  {block.head.map((_, ci) => (
                    <td key={ci} style={alignStyle(block.align[ci] ?? null)}>
                      {r[ci] ? <Inlines nodes={r[ci] ?? []} ctx={ctx} /> : null}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
          {tail}
        </div>
      );
    case "hr":
      return <hr className={styles["hr"]} />;
  }
}

function alignStyle(a: "left" | "center" | "right" | null): { textAlign: "left" | "center" | "right" } | undefined {
  return a ? { textAlign: a } : undefined;
}

const DIFF_LANGS: ReadonlySet<string> = new Set(["diff", "patch"]);

function CodeBlock({ lang, value, open, ctx, tail }: { readonly lang: string; readonly value: string; readonly open: boolean; readonly ctx: RenderCtx; readonly tail: ReactNode }) {
  const [copied, setCopied] = useState(false);
  const files = useMemo(() => (ctx.diffs && DIFF_LANGS.has(lang) && !open ? parseUnifiedDiff(value) : []), [ctx.diffs, lang, value, open]);

  if (files.length > 0) {
    return (
      <div className={styles["diff"]}>
        <DiffView files={files} summary={files.length > 1} />
      </div>
    );
  }

  const copy = () => {
    void navigator.clipboard?.writeText(value).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    });
  };

  return (
    <div className={cx(styles["code"], open && styles["codeOpen"])} data-lang={lang || undefined}>
      <div className={styles["codeBar"]} aria-hidden={lang ? undefined : true}>
        <span className={styles["codeLang"]}>{lang}</span>
        <span className={styles["codeSpacer"]} />
        {!open ? (
          <button type="button" className={styles["codeCopy"]} onClick={copy} aria-label="Copy code">
            <Icon name={copied ? "check" : "copy"} size={12} />
            {copied ? "Copied" : "Copy"}
          </button>
        ) : null}
      </div>
      <pre className={styles["pre"]}>
        <code>
          {value}
          {tail}
        </code>
      </pre>
    </div>
  );
}

function Inlines({ nodes, ctx }: { readonly nodes: readonly Inline[]; readonly ctx: RenderCtx }) {
  return (
    <>
      {nodes.map((n, i) => (
        <InlineNode key={i} node={n} ctx={ctx} />
      ))}
    </>
  );
}

function InlineNode({ node, ctx }: { readonly node: Inline; readonly ctx: RenderCtx }) {
  switch (node.t) {
    case "text":
      return <>{node.v}</>;
    case "code":
      return <code className={styles["inlineCode"]}>{node.v}</code>;
    case "strong":
      return (
        <strong className={styles["strong"]}>
          <Inlines nodes={node.c} ctx={ctx} />
        </strong>
      );
    case "em":
      return (
        <em>
          <Inlines nodes={node.c} ctx={ctx} />
        </em>
      );
    case "del":
      return (
        <del className={styles["del"]}>
          <Inlines nodes={node.c} ctx={ctx} />
        </del>
      );
    case "link":
      return (
        <a href={node.href} className={styles["link"]} target={ctx.linkTarget} rel={ctx.linkTarget === "_blank" ? "noopener noreferrer" : undefined}>
          <Inlines nodes={node.c} ctx={ctx} />
        </a>
      );
    case "image":
      // Images are shown as a link, not fetched: an <img> to an arbitrary
      // host is a tracking pixel and a layout jump. The consumer can opt in
      // by rendering artifacts itself.
      return (
        <a href={node.src} className={cx(styles["link"], styles["imageLink"])} target={ctx.linkTarget} rel="noopener noreferrer">
          <Icon name="file" size={11} />
          {node.alt || "image"}
        </a>
      );
    case "br":
      return <br />;
  }
}

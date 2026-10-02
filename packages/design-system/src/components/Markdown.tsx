import { useMemo, useState, type CSSProperties, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { outline as buildOutline, parseMarkdown, type Block, type Inline } from "../util/markdown.ts";
import { layoutWidth, parseLayout, type ImageLayout } from "../util/imageLayout.ts";
import { DiffView, parseUnifiedDiff } from "./DiffView.tsx";
import { isPromptVariable } from "@dude/domain";
import styles from "./Markdown.module.css";

export type MarkdownVariant = "message" | "document" | "prompt";

export interface MarkdownProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  /**
   * The Markdown. A list is sections of one document, each parsed on its
   * own — an unclosed fence in one cannot swallow the next — and rendered
   * as one, in one rhythm.
   */
  readonly source: string | ReadonlyArray<string>;
  /**
   * `message` (default): a chat turn. One block reads at chat
   * leading; more than one switches to the long-form rhythm (prose leading,
   * `--ds-md-gap` between blocks) once the reply has finished streaming, so
   * the switch happens once rather than when the second block arrives. No
   * outline. `document`: a published artifact read in full. Wider measure,
   * more air between sections, an optional heading outline. `prompt`: an
   * agent's instructions, read and edited in settings — UI body size, a
   * compact rhythm, and `{{variables}}` drawn as chips.
   */
  readonly variant?: MarkdownVariant | undefined;
  /**
   * The source is still arriving. Unterminated constructs are rendered as
   * open (an unclosed fence is still a code block, an open `**` is still
   * bold) so nothing flickers when the closer lands. A caret marks the end.
   */
  readonly streaming?: boolean | undefined;
  /**
   * A chat turn in a transcript (a message, a thought, a question): it
   * spans the transcript's column, with no reading measure. Other prose
   * (a finding, help text) keeps the measure.
   */
  readonly unmeasured?: boolean | undefined;
  /** Document variant only: show a heading outline beside the text. */
  readonly outline?: boolean | undefined;
  /**
   * The document's name, as its first heading (an `h1`), plain text: a
   * title a person typed is not Markdown. Blank, `untitled` stands in,
   * muted.
   */
  readonly title?: string | undefined;
  /** In place of a blank `title`. Default "Untitled". */
  readonly untitled?: string | undefined;
  /**
   * A single newline is a line break (`parseMarkdown`'s `breaks`). On where
   * a person writes the Markdown (a task, a prompt, a steer); off, the
   * default, for agent output.
   */
  readonly breaks?: boolean | undefined;
  /** Where links open. Defaults to a new tab with `rel="noopener noreferrer"`. */
  readonly linkTarget?: "_blank" | "_self" | undefined;
  /** Render fenced ```diff / ```patch blocks with DiffView (default true). */
  readonly diffs?: boolean | undefined;
  /**
   * Draws `![alt](attachment:id)`, an image of the caller's own (a task's
   * upload), in place: usually a `MarkdownImage` over bytes the caller
   * fetched. Without it such an image is its alt text. Every other image
   * stays a link: an <img> to an arbitrary host is a tracking pixel. `title`
   * is the reference's title, where a task keeps the image's layout; `n`
   * is the image's place among the attachment images drawn, in order.
   */
  readonly attachmentImage?: ((id: string, alt: string, title?: string, n?: number) => ReactNode) | undefined;
  /**
   * In place of the frame that lays an attachment image out (`ImageFigure`):
   * an editor's, which adds selection and handles around the same frame.
   */
  readonly attachmentFrame?: ((image: AttachmentFrameProps) => ReactNode) | undefined;
}

/** One attachment image as Markdown frames it: what it is, its place, its layout, and what the resolver drew. */
export interface AttachmentFrameProps {
  readonly id: string;
  readonly alt: string;
  readonly title: string | undefined;
  readonly n: number;
  readonly layout: ImageLayout;
  readonly children: ReactNode;
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
  breaks,
  unmeasured,
  outline,
  title,
  untitled = "Untitled",
  linkTarget = "_blank",
  diffs = true,
  attachmentImage,
  attachmentFrame,
  className,
  ...rest
}: MarkdownProps) {
  const blocks = useMemo(
    () => {
      const sections = typeof source === "string" ? [source] : source;
      const usedIds = new Map<string, number>();
      // Only the last section can still be arriving.
      return sections.flatMap((section, i) => parseMarkdown(section, { streaming: (streaming ?? false) && i === sections.length - 1, breaks, usedIds }));
    },
    [source, streaming, breaks],
  );
  const headings = useMemo(() => (outline && variant === "document" ? buildOutline(blocks) : []), [blocks, outline, variant]);
  const imageOrder = useMemo(() => (attachmentImage ? numberAttachmentImages(blocks) : new Map<Inline, number>()), [blocks, attachmentImage]);
  const ctx: RenderCtx = { linkTarget, diffs, streaming: streaming === true, variables: variant === "prompt", attachmentImage, attachmentFrame, imageOrder };

  const body = (
    <div className={cx(styles["root"], variant === "message" ? styles["message"] : styles[variant], variant === "message" && !streaming && blocks.length > 1 && styles["long"], streaming && styles["streaming"], unmeasured && styles["unmeasured"], imageOrder.size > 0 && styles["figures"], className)} {...rest}>
      {title !== undefined ? (
        <h1 className={cx(styles["h"], styles["h1"], !title.trim() && styles["untitled"])}>{title.trim() || untitled}</h1>
      ) : null}
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
  /** Draw `{{name}}` as a variable chip (prompts). */
  readonly variables: boolean;
  readonly attachmentImage: MarkdownProps["attachmentImage"];
  readonly attachmentFrame: MarkdownProps["attachmentFrame"];
  /** Each attachment image node's place among them, in reading order. */
  readonly imageOrder: ReadonlyMap<Inline, number>;
}

/** The attachment image nodes in reading order, numbered from 0: a block's inlines before the next block's, list items and table cells in order. */
function numberAttachmentImages(blocks: readonly Block[]): Map<Inline, number> {
  const order = new Map<Inline, number>();
  const inl = (nodes: readonly Inline[]): void => {
    for (const n of nodes) {
      if (n.t === "image" && n.src.startsWith(ATTACHMENT_URL)) order.set(n, order.size);
      else if (n.t === "strong" || n.t === "em" || n.t === "del" || n.t === "link") inl(n.c);
    }
  };
  const blk = (bs: readonly Block[]): void => {
    for (const b of bs) {
      if (b.t === "heading" || b.t === "paragraph") inl(b.c);
      else if (b.t === "quote") blk(b.c);
      else if (b.t === "list") for (const it of b.items) blk(it.c);
      else if (b.t === "table") {
        for (const c of b.head) inl(c);
        for (const r of b.rows) for (const c of b.head.keys()) inl(r[c] ?? []);
      }
    }
  };
  blk(blocks);
  return order;
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

const VARIABLE = /\{\{\s*([\w.]+)\s*\}\}/g;
const ATTACHMENT_URL = "attachment:";

export interface MarkdownImageProps {
  /** The bytes' URL (a blob: URL the caller read); none while they load. */
  readonly src?: string | undefined;
  readonly alt: string;
  /** Opens it full size (`ImageViewer`). */
  readonly onOpen?: (() => void) | undefined;
  /** Shown in place of the image: it cannot be shown ("Image unavailable"). */
  readonly unavailable?: boolean | undefined;
}

/**
 * An image a person put in their Markdown, in its place in the text: its
 * natural size up to the column's width, a button that opens it.
 */
export function MarkdownImage({ src, alt, onOpen, unavailable }: MarkdownImageProps) {
  if (unavailable) {
    return (
      <span className={styles["imageMissing"]} data-testid="markdown-image-missing">
        <Icon name="file" size={11} />
        {alt || "image"} · unavailable
      </span>
    );
  }
  const img = src ? <img src={src} alt={alt} className={styles["image"]} /> : <span className={styles["imageLoading"]} aria-label={`Loading ${alt}`} />;
  return onOpen ? (
    <button type="button" className={styles["imageButton"]} onClick={onOpen} aria-label={`Open ${alt || "image"}`} data-testid="markdown-image">
      {img}
    </button>
  ) : (
    <span className={styles["imageButton"]} data-testid="markdown-image">{img}</span>
  );
}

/**
 * Lays an attachment image out by its title: a width from its size (never
 * wider than the column), on its own line when centred, floated with the
 * text beside it when left or right. A column under 480 px centres every
 * image. The field and each list item hold their floats.
 */
export function ImageFigure({ layout, width, className, style, children, ...rest }: HTMLAttributes<HTMLSpanElement> & {
  readonly layout: ImageLayout;
  /** A width being dragged, in px, in place of the layout's. */
  readonly width?: number | undefined;
}) {
  return (
    <span className={cx(styles["figure"], className)} data-align={layout.align} data-size={String(layout.size)}
      style={{ width: width !== undefined ? `${width}px` : layoutWidth(layout.size), ...style } as CSSProperties}
      data-testid="markdown-figure" {...rest}>
      {children}
    </span>
  );
}

/**
 * Text with each `{{name}}` the orchestrator fills in as a chip. Anything
 * else in braces stays as written — as the agent will read it — so a typo
 * does not look like a variable.
 */
function WithVariables({ text }: { readonly text: string }) {
  const out: ReactNode[] = [];
  let at = 0;
  for (const m of text.matchAll(VARIABLE)) {
    if (!isPromptVariable(m[0])) continue;
    if (m.index > at) out.push(text.slice(at, m.index));
    out.push(<span key={m.index} className={styles["variable"]} title={`Filled in for each run: ${m[1]}`}>{m[1]}</span>);
    at = m.index + m[0].length;
  }
  if (at < text.length) out.push(text.slice(at));
  return <>{out}</>;
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
      return ctx.variables ? <WithVariables text={node.v} /> : <>{node.v}</>;
    case "code":
      return <code className={styles["inlineCode"]}>{ctx.variables ? <WithVariables text={node.v} /> : node.v}</code>;
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
      if (node.src.startsWith(ATTACHMENT_URL)) {
        if (!ctx.attachmentImage) return <>{node.alt}</>;
        const id = node.src.slice(ATTACHMENT_URL.length);
        const n = ctx.imageOrder.get(node) ?? 0;
        const drawn = ctx.attachmentImage(id, node.alt, node.title, n);
        const frame = { id, alt: node.alt, title: node.title, n, layout: parseLayout(node.title), children: drawn };
        return <>{ctx.attachmentFrame ? ctx.attachmentFrame(frame) : <ImageFigure layout={frame.layout}>{drawn}</ImageFigure>}</>;
      }
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

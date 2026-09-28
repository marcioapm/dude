import { useLayoutEffect, useRef, useState, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { SkeletonLines } from "../primitives/Feedback.tsx";
import { Markdown } from "./Markdown.tsx";
import { ARTIFACT_KIND_SPECS, artifactKind } from "./ArtifactRow.tsx";
import styles from "./ArtifactPreview.module.css";

export interface ArtifactPreviewProps extends Omit<HTMLAttributes<HTMLDivElement>, "children"> {
  readonly contentType: string | null | undefined;
  readonly name: string;
  /** The content, for text kinds. The app fetches it. */
  readonly text?: string | undefined;
  /**
   * Where the bytes are, for images and video; for HTML, a URL to show in
   * a sandboxed frame (scripts off, an opaque origin), which the server
   * also serves sandboxed.
   */
  readonly url?: string | undefined;
  readonly loading?: boolean | undefined;
  readonly error?: string | undefined;
  /** Height at which long content is clamped behind "Show all". Default 400. */
  readonly maxHeight?: number | undefined;
  /** The app's download link, shown when there is no preview for the type. */
  readonly download?: ReactNode;
  /** Too large to show in the page: says so, and offers the download. */
  readonly tooLarge?: boolean | undefined;
}

/**
 * The content of an artifact, by kind: Markdown as a document, text and
 * JSON in mono (JSON pretty-printed when it parses, shown as-is when it
 * does not — a half-written result is still worth reading), images on a
 * checkerboard so a transparent screenshot has edges, and a plain line
 * plus the download link for anything else. Long content clamps at
 * `maxHeight` with a "Show all" control.
 */
export function ArtifactPreview({ contentType, name, text, url, loading, error, maxHeight = 400, download, tooLarge, className, ...rest }: ArtifactPreviewProps) {
  const kind = artifactKind(contentType, name);
  if (tooLarge) {
    return (
      <div className={cx(styles["root"], styles["none"], className)} {...rest}>
        <span>Too large to show here.</span>
        {download}
      </div>
    );
  }
  if (loading) {
    return (
      <div className={cx(styles["root"], className)} aria-busy="true" {...rest}>
        <SkeletonLines lines={4} />
      </div>
    );
  }
  if (error) {
    return (
      <div className={cx(styles["root"], styles["error"], className)} role="alert" {...rest}>
        <Icon name="alert" size={12} /> {error}
      </div>
    );
  }
  if (kind === "image" && url) {
    return (
      <div className={cx(styles["root"], className)} {...rest}>
        <div className={styles["checker"]}>
          <img src={url} alt={name} className={styles["image"]} loading="lazy" />
        </div>
      </div>
    );
  }
  if (kind === "video" && url) {
    return (
      <div className={cx(styles["root"], className)} {...rest}>
        <video src={url} controls preload="metadata" className={styles["video"]} aria-label={name} />
      </div>
    );
  }
  if (kind === "html" && url) {
    // sandbox with no allowances: no scripts, no forms, no same origin.
    return (
      <div className={cx(styles["root"], className)} {...rest}>
        <iframe src={url} sandbox="" title={name} className={styles["frame"]} referrerPolicy="no-referrer" />
      </div>
    );
  }
  if (kind === "markdown" && text !== undefined) {
    return (
      <Clamp maxHeight={maxHeight} className={className} {...rest}>
        <Markdown source={text} variant="document" className={styles["markdown"]} />
      </Clamp>
    );
  }
  if ((kind === "text" || kind === "json") && text !== undefined) {
    return (
      <Clamp maxHeight={maxHeight} className={className} {...rest}>
        <pre className={styles["pre"]}>{kind === "json" ? prettyJson(text) : text}</pre>
      </Clamp>
    );
  }
  const label = kind === "other" ? contentType || "this file" : ARTIFACT_KIND_SPECS[kind].label.toLowerCase();
  return (
    <div className={cx(styles["root"], styles["none"], className)} {...rest}>
      <span>No preview for {label}.</span>
      {download}
    </div>
  );
}

/** Pretty-print JSON when it parses; return the text untouched when it does not. */
export function prettyJson(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

// ---------------------------------------------------------------------------

interface ClampProps extends HTMLAttributes<HTMLDivElement> {
  readonly maxHeight: number;
  readonly children: ReactNode;
}

/**
 * Clamps its content to `maxHeight` when it is taller, with a fade and a
 * "Show all" control; measured after layout, so short content gets no
 * control. Measured again when the content changes.
 */
function Clamp({ maxHeight, className, children, ...rest }: ClampProps) {
  const ref = useRef<HTMLDivElement>(null);
  const [tall, setTall] = useState(false);
  const [all, setAll] = useState(false);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setTall(el.scrollHeight > maxHeight + 8);
  }, [maxHeight, children]);
  const clamped = tall && !all;
  return (
    <div className={cx(styles["root"], className)} {...rest}>
      <div ref={ref} className={cx(styles["clamp"], clamped && styles["clamped"])} style={clamped ? { maxHeight } : undefined}>
        {children}
      </div>
      {tall ? (
        <button type="button" className={styles["showAll"]} aria-expanded={all} onClick={() => setAll((v) => !v)}>
          <Icon name={all ? "chevron-up" : "chevron-down"} size={12} />
          {all ? "Show less" : "Show all"}
        </button>
      ) : null}
    </div>
  );
}

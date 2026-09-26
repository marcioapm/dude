import { useId, type HTMLAttributes, type ReactNode } from "react";
import type { AgentRole } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Badge } from "../primitives/Badge.tsx";
import { EmptyState } from "../primitives/Feedback.tsx";
import { formatBytes, formatDuration } from "../util/format.ts";
import { useDisclosure } from "../util/useDisclosure.ts";
import { toMs, useNow } from "../util/useNow.ts";
import { AgentAvatar, ROLE_LABEL } from "./AgentAvatar.tsx";
import { Duration } from "./Numbers.tsx";
import styles from "./ArtifactRow.module.css";

// ---------------------------------------------------------------------------
// What kind of file is it — decides the glyph and the preview.
// ---------------------------------------------------------------------------

export type ArtifactKind = "markdown" | "text" | "json" | "image" | "other";

export interface ArtifactKindSpec {
  readonly label: string;
  readonly glyph: IconName;
}

export const ARTIFACT_KIND_SPECS: Record<ArtifactKind, ArtifactKindSpec> = {
  markdown: { label: "Markdown", glyph: "file" },
  text: { label: "Text", glyph: "file" },
  json: { label: "JSON", glyph: "list" },
  image: { label: "Image", glyph: "image" },
  other: { label: "File", glyph: "file" },
};

const TEXT_EXT = new Set(["txt", "log", "csv", "tsv", "yaml", "yml", "toml", "ini", "env", "sh", "py", "ts", "tsx", "js", "go", "rs", "sql", "css", "html", "xml", "diff", "patch"]);
const IMAGE_EXT = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg", "avif", "bmp"]);

/**
 * The kind of an artifact from its content type, falling back to the file
 * extension when the type is generic (`application/octet-stream`, missing)
 * — agents are not careful about media types, names are more reliable.
 */
export function artifactKind(contentType: string | null | undefined, name: string): ArtifactKind {
  const type = (contentType ?? "").split(";")[0]!.trim().toLowerCase();
  const ext = name.toLowerCase().split(".").pop() ?? "";
  if (type === "text/markdown" || type === "text/x-markdown" || ext === "md" || ext === "markdown") return "markdown";
  if (type === "application/json" || type.endsWith("+json") || ext === "json") return "json";
  if (type.startsWith("image/") || (type === "" || type === "application/octet-stream") && IMAGE_EXT.has(ext)) return "image";
  if (type.startsWith("text/") || type === "application/xml" || type === "application/x-yaml" || type === "application/yaml" || TEXT_EXT.has(ext)) return "text";
  return "other";
}

// ---------------------------------------------------------------------------
// Row
// ---------------------------------------------------------------------------

export type ArtifactChange = "new" | "updated";

export interface ArtifactProducer {
  readonly role: AgentRole;
  /** The workflow phase that wrote it ("implement", "review"). */
  readonly phase?: string | undefined;
}

export interface ArtifactRowProps extends Omit<HTMLAttributes<HTMLLIElement>, "children"> {
  /** A path: `design.md`, `screens/login.png`. Shown in mono. */
  readonly name: string;
  readonly contentType: string | null | undefined;
  readonly sizeBytes: number;
  /** Shown shortened in the title; full value on hover. */
  readonly sha256?: string | undefined;
  readonly producer: ArtifactProducer;
  readonly publishedAt: string | number | Date;
  /** Since the previous run of the same task; omit when unknown. */
  readonly change?: ArtifactChange | null | undefined;
  /** The app's `<a href download>`; rendered at the end of the row. */
  readonly download?: ReactNode;
  /** The inline content (an `ArtifactPreview`); makes the row expandable. */
  readonly preview?: ReactNode;
  readonly expanded?: boolean | undefined;
  readonly defaultExpanded?: boolean | undefined;
  readonly onExpandedChange?: ((open: boolean) => void) | undefined;
}

/**
 * One published file on a 28px row: a glyph for its kind, the path in
 * mono, the size, who produced it (a role avatar and label), how long ago,
 * and the app's download link. "New" / "Updated" is a neutral badge. With
 * a `preview` the leading part of the row is a disclosure button that
 * opens the content under it; the download link stays a sibling so Enter
 * on it downloads.
 */
export function ArtifactRow({ name, contentType, sizeBytes, sha256, producer, publishedAt, change, download, preview, expanded, defaultExpanded, onExpandedChange, className, ...rest }: ArtifactRowProps) {
  const kind = artifactKind(contentType, name);
  const spec = ARTIFACT_KIND_SPECS[kind];
  const bodyId = useId();
  const disc = useDisclosure({ expanded, defaultExpanded, onExpandedChange });
  const open = preview !== undefined && preview !== null && disc.open;
  const expandable = preview !== undefined && preview !== null;
  // Age reads in hours and days; one clock a minute is plenty.
  const now = useNow(true, 60_000);
  const published = toMs(publishedAt);
  const age = published === null ? null : Math.max(0, now - published);
  const roleLabel = ROLE_LABEL[producer.role];
  const who = producer.phase ? `${roleLabel} · ${producer.phase}` : roleLabel;
  const titleText = [`${spec.label}${contentType ? ` (${contentType})` : ""}`, `${sizeBytes} bytes`, sha256 ? `sha256 ${sha256}` : null].filter(Boolean).join("\n");

  const head = (
    <>
      <span className={styles["chevron"]} aria-hidden>
        {expandable ? <Icon name="chevron-right" size={12} className={styles["chevronIcon"]} /> : null}
      </span>
      <span className={styles["glyph"]} aria-hidden>
        <Icon name={spec.glyph} size={14} />
      </span>
      <span className={styles["name"]}>{name}</span>
      <span className={styles["kind"]}>{spec.label}</span>
      <span className={styles["size"]}>{formatBytes(sizeBytes)}</span>
    </>
  );

  return (
    <li className={cx(styles["root"], open && styles["open"], className)} data-kind={kind} data-change={change ?? undefined} {...rest}>
      <div className={styles["row"]} title={titleText}>
        {expandable ? (
          <button type="button" className={cx(styles["head"], styles["headButton"])} aria-expanded={open} aria-controls={open ? bodyId : undefined} onClick={disc.toggle}>
            {head}
          </button>
        ) : (
          <span className={styles["head"]}>{head}</span>
        )}
        <span className={styles["trailing"]}>
          {change ? (
            <Badge tone="neutral" emphasis="subtle" size="sm" className={styles["change"]}>
              {change === "new" ? "New" : "Updated"}
            </Badge>
          ) : null}
          <span className={styles["producer"]} title={who}>
            <AgentAvatar role={producer.role} size="xs" />
            <span className={styles["producerLabel"]}>{who}</span>
          </span>
          {age !== null ? <Duration ms={age} format="age" tone="muted" className={styles["age"]} title={`Published ${new Date(publishedAt).toLocaleString()} (${formatDuration(age, { style: "long" })} ago)`} /> : null}
          {download ? <span className={styles["download"]}>{download}</span> : null}
        </span>
      </div>
      {open ? (
        <div id={bodyId} className={styles["body"]}>
          {preview}
        </div>
      ) : null}
    </li>
  );
}

// ---------------------------------------------------------------------------
// Group
// ---------------------------------------------------------------------------

export interface ArtifactLike {
  readonly id: string;
}

export interface ArtifactGroupProps<T extends ArtifactLike> extends Omit<HTMLAttributes<HTMLElement>, "children" | "title"> {
  readonly artifacts: ReadonlyArray<T>;
  readonly renderRow: (artifact: T) => ReactNode;
  /** Section title. Default "Artifacts". */
  readonly title?: ReactNode;
  /** Right side of the header. */
  readonly actions?: ReactNode;
  /**
   * What to say when there are none. Without it an empty group renders
   * nothing at all — a task with no artifacts should not grow a
   * section to say so unless the screen wants one.
   */
  readonly empty?: ReactNode;
}

/**
 * A task's published files, in the order given (the app decides:
 * newest first is the usual). The header carries the count. `renderRow`
 * returns an `ArtifactRow` per artifact, so the app owns the download
 * links and the preview fetching.
 */
export function ArtifactGroup<T extends ArtifactLike>({ artifacts, renderRow, title = "Artifacts", actions, empty, className, ...rest }: ArtifactGroupProps<T>) {
  const headingId = useId();
  if (artifacts.length === 0 && (empty === undefined || empty === null || empty === false)) return null;
  return (
    <section className={cx(styles["group"], className)} aria-labelledby={headingId} {...rest}>
      <header className={styles["groupHead"]}>
        <span id={headingId} className={cx("ds-label", styles["groupTitle"])}>
          {title}
        </span>
        <span className={styles["groupCount"]}>{artifacts.length}</span>
        {actions ? <span className={styles["groupActions"]}>{actions}</span> : null}
      </header>
      {artifacts.length === 0 ? (
        <EmptyState compact icon="file" title="No artifacts yet" description={empty === true ? undefined : empty} className={styles["groupEmpty"]} />
      ) : (
        <ul className={styles["list"]}>{artifacts.map((a) => renderRow(a))}</ul>
      )}
    </section>
  );
}

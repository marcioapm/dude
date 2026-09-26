import { useId, type HTMLAttributes, type ReactNode } from "react";
import type { FindingSeverity, FindingStatus } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Badge } from "../primitives/Badge.tsx";
import type { ToneName } from "../tokens/palette.ts";
import { useDisclosure } from "../util/useDisclosure.ts";
import { Markdown } from "./Markdown.tsx";
import styles from "./FindingRow.module.css";

// ---------------------------------------------------------------------------
// Vocabulary. Keyed on the domain unions, so a new severity or status is a
// compile error here before it is an unstyled string on a screen.
// ---------------------------------------------------------------------------

export interface FindingSeveritySpec {
  readonly label: string;
  readonly tone: ToneName;
  readonly glyph: IconName;
  /** Sort order, most severe first. */
  readonly rank: number;
  readonly description: string;
}

export const FINDING_SEVERITIES = ["blocking", "high", "medium", "low", "note"] as const satisfies ReadonlyArray<FindingSeverity>;

export const FINDING_SEVERITY_SPECS: Record<FindingSeverity, FindingSeveritySpec> = {
  blocking: { label: "Blocking", tone: "danger", glyph: "alert", rank: 0, description: "Must be fixed before the change can land." },
  high: { label: "High", tone: "attention", glyph: "warning", rank: 1, description: "Should be fixed in this change." },
  medium: { label: "Medium", tone: "info", glyph: "info", rank: 2, description: "Worth fixing; may be deferred with a reason." },
  low: { label: "Low", tone: "neutral", glyph: "circle", rank: 3, description: "Minor; fix if cheap." },
  note: { label: "Note", tone: "neutral", glyph: "message", rank: 4, description: "An observation, not a request." },
};

export interface FindingStatusSpec {
  readonly label: string;
  readonly glyph: IconName | null;
  readonly description: string;
}

export const FINDING_STATUSES = ["open", "resolved", "superseded", "accepted"] as const satisfies ReadonlyArray<FindingStatus>;

/** Status is always a neutral badge: the severity carries the colour, the status carries a glyph and a word. */
export const FINDING_STATUS_SPECS: Record<FindingStatus, FindingStatusSpec> = {
  open: { label: "Open", glyph: null, description: "Not yet addressed." },
  resolved: { label: "Resolved", glyph: "check", description: "A later run fixed it." },
  superseded: { label: "Superseded", glyph: "arrow-right", description: "A newer finding replaced it." },
  accepted: { label: "Accepted", glyph: "minus", description: "Acknowledged and deliberately left as is." },
};

// ---------------------------------------------------------------------------
// Row
// ---------------------------------------------------------------------------

export interface FindingRowProps extends Omit<HTMLAttributes<HTMLLIElement>, "title"> {
  readonly severity: FindingSeverity;
  readonly status: FindingStatus;
  readonly category: string;
  readonly title: string;
  readonly file?: string | null | undefined;
  readonly line?: number | null | undefined;
  /** Markdown. Expands under the row. */
  readonly description?: string | undefined;
  /** Markdown. Expands under the row, labelled. */
  readonly suggestedFix?: string | undefined;
  /** Why it was resolved / accepted / superseded. */
  readonly resolutionNote?: string | undefined;
  /** How many fix rounds have tried; shown when > 0. */
  readonly fixAttempts?: number | undefined;
  /**
   * Where it was fixed: a link or button the app renders, labelled with
   * the run alone ("Fix 2"). The row puts "fixed in" before it.
   */
  readonly fixedIn?: ReactNode;
  /** Open the file at the line, when the app can. Makes the location a button. */
  readonly onOpenLocation?: ((file: string, line: number | null) => void) | undefined;
  readonly expanded?: boolean | undefined;
  readonly defaultExpanded?: boolean | undefined;
  readonly onExpandedChange?: ((open: boolean) => void) | undefined;
}

/**
 * One review finding. Severity is a glyph and a word in its tone — never
 * a colour alone, never an uppercase enum. Then the category, the location
 * in mono, the title, and the status as a neutral badge at the end so a
 * resolved blocking finding still reads as blocking and as resolved. The
 * description and suggested fix sit under the row behind one click.
 * Anything not open is dimmed, not struck through: the text stays legible.
 */
export function FindingRow({
  severity,
  status,
  category,
  title,
  file,
  line,
  description,
  suggestedFix,
  resolutionNote,
  fixAttempts,
  fixedIn,
  onOpenLocation,
  expanded,
  defaultExpanded,
  onExpandedChange,
  className,
  ...rest
}: FindingRowProps) {
  const sev = FINDING_SEVERITY_SPECS[severity];
  const st = FINDING_STATUS_SPECS[status];
  const bodyId = useId();
  const hasBody = Boolean(description || suggestedFix || resolutionNote);
  const disc = useDisclosure({ expanded, defaultExpanded, onExpandedChange });
  const open = hasBody && disc.open;
  const location = file ? (line !== null && line !== undefined ? `${file}:${line}` : file) : null;

  const head = (
    <>
      <span className={styles["chevron"]} aria-hidden>
        {hasBody ? <Icon name="chevron-right" size={12} className={styles["chevronIcon"]} /> : null}
      </span>
      <span className={styles["severity"]} title={sev.description}>
        <Icon name={sev.glyph} size={12} />
        <span className={styles["severityLabel"]}>{sev.label}</span>
      </span>
      <span className={styles["category"]} title={category}>
        {category}
      </span>
      <span className={styles["title"]}>{title}</span>
    </>
  );

  return (
    <li className={cx(styles["root"], styles[sev.tone], status !== "open" && styles["settled"], open && styles["open"], className)} data-severity={severity} data-status={status} {...rest}>
      <div className={styles["row"]}>
        {/* The disclosure is a real button over the chevron, severity, category
            and title; the location and the fix link are its siblings, so Enter
            on them does what they say and no button nests in a button. */}
        {hasBody ? (
          <button type="button" className={cx(styles["head"], styles["headButton"])} aria-expanded={open} aria-controls={bodyId} onClick={disc.toggle}>
            {head}
          </button>
        ) : (
          <span className={styles["head"]}>{head}</span>
        )}
        {location ? (
          onOpenLocation && file ? (
            <button type="button" className={cx(styles["location"], styles["locationLink"])} title={`Open ${location}`} onClick={() => onOpenLocation(file, line ?? null)}>
              {location}
            </button>
          ) : (
            <span className={styles["location"]} title={location}>
              {location}
            </span>
          )
        ) : null}
        <span className={styles["trailing"]}>
          {fixAttempts !== undefined && fixAttempts > 0 ? (
            <span className={styles["attempts"]} title={`${fixAttempts} fix ${fixAttempts === 1 ? "attempt" : "attempts"}`}>
              <Icon name="retry" size={11} /> {fixAttempts}
            </span>
          ) : null}
          {fixedIn ? (
            <span className={styles["fixedIn"]}>
              <span className={styles["fixedInLabel"]}>fixed in</span>
              {fixedIn}
            </span>
          ) : null}
          <Badge tone="neutral" emphasis={status === "open" ? "subtle" : "tinted"} size="sm" {...(st.glyph ? { icon: st.glyph } : {})} title={st.description} className={styles["status"]}>
            {st.label}
          </Badge>
        </span>
      </div>
      {open ? (
        <div id={bodyId} className={styles["body"]}>
          {description ? <Markdown source={description} className={styles["prose"]} /> : null}
          {suggestedFix ? (
            <div className={styles["section"]}>
              <div className={cx("ds-label", styles["sectionLabel"])}>Suggested fix</div>
              <Markdown source={suggestedFix} className={styles["prose"]} />
            </div>
          ) : null}
          {resolutionNote ? (
            <div className={styles["section"]}>
              <div className={cx("ds-label", styles["sectionLabel"])}>{st.label}</div>
              <Markdown source={resolutionNote} className={styles["prose"]} />
            </div>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}

// ---------------------------------------------------------------------------
// Group
// ---------------------------------------------------------------------------

export interface FindingLike {
  readonly id: string;
  readonly severity: FindingSeverity;
  readonly status: FindingStatus;
}

/**
 * Open findings first, most severe first; then everything already dealt
 * with, in the same order. Stable within a bucket, so the caller's order
 * (usually creation) is the tie-break.
 */
export function sortFindings<T extends FindingLike>(findings: ReadonlyArray<T>): T[] {
  return findings
    .map((f, i) => ({ f, i }))
    .sort((a, b) => {
      const ao = a.f.status === "open" ? 0 : 1;
      const bo = b.f.status === "open" ? 0 : 1;
      if (ao !== bo) return ao - bo;
      const r = FINDING_SEVERITY_SPECS[a.f.severity].rank - FINDING_SEVERITY_SPECS[b.f.severity].rank;
      return r !== 0 ? r : a.i - b.i;
    })
    .map((x) => x.f);
}

export interface FindingCounts {
  readonly open: number;
  readonly settled: number;
  /** Open findings of severity `blocking` — the ones that stop the change landing. */
  readonly blocking: number;
}

export function countFindings(findings: ReadonlyArray<FindingLike>): FindingCounts {
  let open = 0;
  let settled = 0;
  let blocking = 0;
  for (const f of findings) {
    if (f.status === "open") {
      open += 1;
      if (f.severity === "blocking") blocking += 1;
    } else settled += 1;
  }
  return { open, settled, blocking };
}

export interface FindingGroupProps<T extends FindingLike> extends Omit<HTMLAttributes<HTMLElement>, "children" | "title"> {
  readonly findings: ReadonlyArray<T>;
  readonly renderRow: (finding: T) => ReactNode;
  /** Section title. Default "Review findings". */
  readonly title?: ReactNode;
  /** Right side of the header: filters, a link to the run. */
  readonly actions?: ReactNode;
  /** Draw a hairline between the open and the settled findings. Default true. */
  readonly divide?: boolean | undefined;
}

/**
 * The findings of a task, open first. The header counts what is
 * still open (in attention ink when any is blocking) and what has been
 * dealt with. `renderRow` gets each finding in sorted order and
 * returns a `FindingRow`, so the app decides the links and the handlers.
 */
export function FindingGroup<T extends FindingLike>({ findings, renderRow, title = "Review findings", actions, divide = true, className, ...rest }: FindingGroupProps<T>) {
  const sorted = sortFindings(findings);
  const counts = countFindings(findings);
  const headingId = useId();
  const firstSettled = sorted.findIndex((f) => f.status !== "open");
  return (
    <section className={cx(styles["group"], className)} aria-labelledby={headingId} {...rest}>
      <header className={styles["groupHead"]}>
        <span id={headingId} className={cx("ds-label", styles["groupTitle"])}>
          {title}
        </span>
        <span className={styles["groupCounts"]}>
          {counts.open > 0 ? (
            <span className={cx(styles["groupOpen"], counts.blocking > 0 && styles["groupBlocking"])}>
              {counts.open} open{counts.blocking > 0 ? ` · ${counts.blocking} blocking` : ""}
            </span>
          ) : findings.length > 0 ? (
            <span className={styles["groupSettled"]}>all addressed</span>
          ) : null}
          {counts.open > 0 && counts.settled > 0 ? <span className={styles["groupSettled"]}> · {counts.settled} addressed</span> : null}
        </span>
        {actions ? <span className={styles["groupActions"]}>{actions}</span> : null}
      </header>
      {findings.length === 0 ? (
        <div className={styles["groupEmpty"]}>No findings.</div>
      ) : (
        <ul className={styles["list"]}>
          {sorted.map((f, i) => (
            <FindingSlot key={f.id} divider={divide && firstSettled > 0 && i === firstSettled}>
              {renderRow(f)}
            </FindingSlot>
          ))}
        </ul>
      )}
    </section>
  );
}

function FindingSlot({ divider, children }: { readonly divider: boolean; readonly children: ReactNode }) {
  return (
    <>
      {divider ? <li className={styles["divider"]} aria-hidden /> : null}
      {children}
    </>
  );
}

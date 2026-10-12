/*
 * What the design system would gain for capabilities, built here at its
 * quality (tokens only, both themes, both densities, named for a screen
 * reader) so the mockup can show it before the package changes:
 *
 * - LevelChoice: one of a few ordered levels, side by side, each with its
 *   sentence. Proposed as `ChoiceList layout="row"` with `tag` and
 *   `caution` on an option.
 * - CapabilityCell: a capability's value in a matrix cell, where it comes
 *   from on a project, and whether it goes beyond the organisation.
 * - BeyondMark: "Beyond Acme", the glyph and the words, wherever a
 *   project's value is wider than its organisation's.
 * - CapabilityChip: what a Run or a session's agent may do, in its header,
 *   in MachineChip's grammar with the globe glyph.
 */

import { useRef, type KeyboardEvent, type ReactNode } from "react";
import { Icon, cx, type IconName } from "@dude/design-system";
import { SettingSource } from "@dude/design-system/components";
import { Badge, Tooltip } from "@dude/design-system/primitives";
import styles from "./components.module.css";

// ------------------------------------------------------------------ LevelChoice

export interface Level<T extends string> {
  readonly value: T;
  readonly label: ReactNode;
  readonly description: ReactNode;
  /** A short muted word after the label: "Acme’s", the value a project inherits. */
  readonly tag?: ReactNode;
  /** Choosing it widens what an agent may do: the warning glyph before its label, named for a screen reader. */
  readonly caution?: string | undefined;
  /** Why it cannot be chosen, in place of its description. */
  readonly disabledReason?: ReactNode;
}

export interface LevelChoiceProps<T extends string> {
  readonly levels: ReadonlyArray<Level<T>>;
  readonly value: T;
  readonly onChange: (value: T) => void;
  /** Names the radio group: "Internet". */
  readonly label: string;
  readonly disabled?: boolean | undefined;
  readonly id?: string | undefined;
}

/**
 * Ordered levels side by side, narrowest first, each a radio with a
 * sentence: one tab stop, ← → ↑ ↓ move and choose, Home End. Stacks when
 * its frame is under 560px.
 */
export function LevelChoice<T extends string>({ levels, value, onChange, label, disabled, id }: LevelChoiceProps<T>) {
  const refs = useRef(new Map<T, HTMLButtonElement>());
  const enabled = disabled ? [] : levels.filter((l) => !l.disabledReason);
  const stop = enabled.some((l) => l.value === value) ? value : enabled[0]?.value;
  function move(e: KeyboardEvent<HTMLButtonElement>, from: T) {
    const step = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
    if (step === 0 && e.key !== "Home" && e.key !== "End") return;
    e.preventDefault();
    if (!enabled.length) return;
    const at = enabled.findIndex((l) => l.value === from);
    const next = e.key === "Home" ? enabled[0]! : e.key === "End" ? enabled.at(-1)! : enabled[(at + step + enabled.length) % enabled.length]!;
    onChange(next.value);
    refs.current.get(next.value)?.focus();
  }
  return (
    <div className={styles["levelsFrame"]}>
      <div role="radiogroup" aria-label={label} id={id} className={styles["levels"]}>
        {levels.map((l) => {
          const chosen = l.value === value;
          const off = disabled || Boolean(l.disabledReason);
          return (
            <button key={l.value} type="button" role="radio" aria-checked={chosen} aria-disabled={off || undefined}
              ref={(el) => { if (el) refs.current.set(l.value, el); else refs.current.delete(l.value); }}
              tabIndex={l.value === stop ? 0 : -1} className={cx(styles["level"], chosen && styles["levelChosen"])}
              onClick={() => !off && onChange(l.value)} onKeyDown={(e) => move(e, l.value)}>
              <span className={styles["levelHead"]}>
                <span className={styles["levelDot"]} aria-hidden />
                {l.caution ? <Icon name="warning" size={14} className={styles["levelCaution"]} title={l.caution} /> : null}
                <span className={styles["levelLabel"]}>{l.label}</span>
                {l.tag ? <span className={styles["levelTag"]}>{l.tag}</span> : null}
              </span>
              <span className={styles["levelDescription"]}>{l.disabledReason ?? l.description}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

// ------------------------------------------------------------------ BeyondMark

/** A project's value wider than its organisation's: glyph and words, in the danger tone, never the tone alone. */
export function BeyondMark({ from }: { readonly from: string }) {
  return (
    <Badge size="sm" tone="danger" icon="arrow-up" data-beyond="true">
      Beyond {from}
    </Badge>
  );
}

// ------------------------------------------------------------------ CapabilityCell

export interface CapabilityCellProps {
  /** The value in words: "Your lists", "On", "3 of 4". */
  readonly value: ReactNode;
  readonly icon: IconName;
  /** Nothing granted ("Off", "dude only"): muted ink, the cross or the glyph alone carries it. */
  readonly off?: boolean | undefined;
  /** Opens the role's page at this capability. Its name says role, capability and value. */
  readonly onOpen: () => void;
  readonly label: string;
  /** On a project: where the value comes from. Omitted on the organisation. */
  readonly source?: {
    readonly source: "organization" | "project";
    readonly from: string;
    readonly inherited?: ReactNode;
    readonly onReset?: (() => void) | undefined;
    /** The project's value goes beyond its organisation's. */
    readonly wider?: boolean | undefined;
  } | undefined;
}

/**
 * One capability of one role in the Capabilities matrix: the value is a
 * link to the role's page; under it on a project, `SettingSource` — the
 * one way "Overridden … Reset" is drawn — without its "Acme: …" words,
 * which go in the value's accessible name, and BeyondMark for a wider
 * override. Inherited cells say "From Acme" only in their accessible name,
 * so a column of them stays quiet.
 */
export function CapabilityCell({ value, icon, off, onOpen, label, source }: CapabilityCellProps) {
  const overridden = source?.source === "project";
  // The value underneath is said in the value's name, not drawn: a column of "Acme: …" would crowd the matrix.
  const name = overridden ? `${label}, overridden; ${source.from}: ${typeof source.inherited === "string" ? source.inherited : "different"}${source.wider ? `, beyond ${source.from}` : ""}` : label;
  return (
    <span className={cx(styles["cell"], overridden && styles["cellOverridden"])} data-source={source?.source}>
      <button type="button" className={cx(styles["cellValue"], off && styles["cellOff"])} onClick={onOpen} aria-label={name}>
        <Icon name={icon} size={14} className={styles["cellIcon"]} />
        <span>{value}</span>
      </button>
      {overridden ? (
        <span className={styles["cellSource"]}>
          <SettingSource source="project" from={source.from} onReset={source.onReset} />
          {source.wider ? <BeyondMark from={source.from} /> : null}
        </span>
      ) : null}
    </span>
  );
}

// ------------------------------------------------------------------ CapabilityChip

export interface CapabilityChipProps {
  /** The few that matter, in order: "Any host", "Web search", "No containers". */
  readonly parts: ReadonlyArray<string>;
  /** Everything it may and may not do, and when that was fixed. */
  readonly tooltip?: ReactNode;
  readonly "data-testid"?: string | undefined;
}

/**
 * What an agent may do, in a Run's or a session's header: the globe glyph,
 * then the parts, muted, cut before they wrap. A button only so its tooltip
 * opens on focus; it does nothing on a press.
 */
export function CapabilityChip({ parts, tooltip, "data-testid": testId }: CapabilityChipProps) {
  const chip = (
    <button type="button" className={styles["chip"]} data-testid={testId} aria-label={`It may: ${parts.join(", ")}`}>
      <Icon name="globe" size={12} className={styles["chipIcon"]} />
      <span className={cx(styles["chipText"], "ds-cap")}>{parts.join(" · ")}</span>
    </button>
  );
  return tooltip ? <Tooltip content={tooltip} side="bottom" keepOnPress>{chip}</Tooltip> : chip;
}

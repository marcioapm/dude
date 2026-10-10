import type { ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import styles from "./Tiers.module.css";

/*
 * Model tiers: the kind of work an agent is given, and the model asked for
 * it. The design system knows no tiers or models; the app passes names and
 * words.
 */

export type TierTone = "info" | "success" | "attention" | "neutral";

/** A tier's mark: its glyph on its tone's tint, square as an agent's tile. */
export function TierMark({ icon, tone, size = "md" }: { readonly icon: IconName; readonly tone: TierTone; readonly size?: "sm" | "md" }) {
  return (
    <span className={cx(styles["mark"], styles[`tone-${tone}`], styles[`mark-${size}`])} aria-hidden>
      <Icon name={icon} size={size === "sm" ? 10 : 14} />
    </span>
  );
}

/** A tier as a table's first cell or a picker's option: its mark, name, and what it is for under it. */
export function TierLine({ icon, tone, name, description }: {
  readonly icon: IconName;
  readonly tone: TierTone;
  readonly name: ReactNode;
  readonly description?: ReactNode;
}) {
  return (
    <span className={styles["line"]}>
      <TierMark icon={icon} tone={tone} />
      <span className={styles["lineText"]}>
        <span className={styles["lineName"]}>{name}</span>
        {description ? <span className={styles["lineDescription"]}>{description}</span> : null}
      </span>
    </span>
  );
}

/** One step of how something works, as a row of three explains it: a small-caps title, then the words. */
export interface FlowStep {
  readonly title: ReactNode;
  readonly children: ReactNode;
}

/** How a thing works, in steps side by side on the chrome shade; they stack when narrow. */
export function FlowSteps({ steps, ...rest }: { readonly steps: ReadonlyArray<FlowStep> } & { readonly "data-testid"?: string }) {
  return (
    <ol className={styles["flow"]} data-testid={rest["data-testid"]}>
      {steps.map((s, i) => (
        <li key={i} className={styles["flowStep"]}>
          <span className={cx(styles["flowTitle"], "ds-cap")}>{s.title}</span>
          <span className={styles["flowBody"]}>{s.children}</span>
        </li>
      ))}
    </ol>
  );
}

/**
 * Names to pick from, as small mono chips under a field: the chosen one on
 * the info tint. Suggestions only; the field takes any name.
 */
export function NameChips({ names, value, onPick, label }: {
  readonly names: ReadonlyArray<string>;
  readonly value: string;
  readonly onPick: (name: string) => void;
  /** What the chips are, for a screen reader. */
  readonly label: string;
}) {
  return (
    <div className={styles["chips"]} role="group" aria-label={label}>
      {names.map((n) => (
        <button key={n} type="button" className={cx(styles["nameChip"], "ds-mono", n === value && styles["nameChipOn"])}
          aria-pressed={n === value} onClick={() => onPick(n)}>
          {n}
        </button>
      ))}
    </div>
  );
}

export interface TierChipProps {
  /** The tier's name; none for a Run from before tiers, which shows its model alone. */
  readonly tier?: string | null | undefined;
  /** The model asked for, mono and strong. */
  readonly model: string;
  /** The reasoning effort asked for, after the model; none at the model's default. */
  readonly effort?: string | null | undefined;
  /** The harness that ran it, before the tier (as a person reads it: "Claude Code"). */
  readonly harness?: string | null | undefined;
  readonly tooltip?: ReactNode;
  readonly "data-testid"?: string | undefined;
}

/**
 * What a session asked for, in its header: the harness that ran it, the
 * tier, then the model it requested, then its effort when it asked for
 * one. A button only so the tooltip opens on focus.
 */
export function TierChip({ tier, model, effort, harness, tooltip, "data-testid": testId }: TierChipProps) {
  const requests = effort ? `${model} at effort ${effort}` : model;
  const chip = (
    <button type="button" className={styles["chip"]} data-testid={testId}
      aria-label={`${harness ? `${harness}, ` : ""}${tier ? `Model: ${tier}, requests ${requests}` : `Model: ${requests}`}`}>
      <Icon name="sparkle" size={12} className={styles["chipIcon"]} />
      {harness ? <span className={cx(styles["chipTier"], "ds-cap")} data-testid={testId ? `${testId}-harness` : undefined}>{harness} ·</span> : null}
      {tier ? <span className={cx(styles["chipTier"], "ds-cap")}>{tier} ·</span> : null}
      <span className={cx(styles["chipModel"], "ds-mono")}>{model}</span>
      {effort ? <span className={styles["chipEffort"]}>· {effort}</span> : null}
    </button>
  );
  return tooltip ? <Tooltip content={tooltip} side="bottom" keepOnPress>{chip}</Tooltip> : chip;
}

/** A tooltip's body for a tier: its name as a heading, the words, and a muted last line. */
export function TierTip({ title, children, aside }: { readonly title: ReactNode; readonly children: ReactNode; readonly aside?: ReactNode }) {
  return (
    <span className={styles["tip"]}>
      <b className={styles["tipTitle"]}>{title}</b>
      <span className={styles["tipBody"]}>{children}</span>
      {aside ? <span className={styles["tipAside"]}>{aside}</span> : null}
    </span>
  );
}

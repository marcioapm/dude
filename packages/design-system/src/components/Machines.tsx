import type { ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import styles from "./Machines.module.css";

/*
 * Machines: what an agent's machine is, and how much of a host it takes.
 * The design system knows no sizes or pools; the app passes numbers and
 * words.
 */

export interface ProportionSegment {
  readonly id: string;
  /** Its share of the whole, in any unit the other segments share. */
  readonly value: number;
  /** Inside the segment, when it has room: "Run A · asks 16 · gets 15". */
  readonly label?: ReactNode;
  /**
   * `reserved` is kept back, not given to anyone: hatched, and named in
   * the legend. `part` is a share given out, on the info tint.
   */
  readonly kind?: "part" | "reserved" | undefined;
}

export interface ProportionBarProps {
  readonly segments: ReadonlyArray<ProportionSegment>;
  /** Under the bar: its key, left, and what the whole is, right. */
  readonly legend?: ReactNode;
  readonly total?: ReactNode;
  /** What the bar says, for a screen reader: it is drawn, not read. */
  readonly "aria-label": string;
  readonly className?: string | undefined;
}

/**
 * A whole split into parts, each as wide as its share: the memory of one
 * host, the part Linux and the host keep hatched, the rest given to runs.
 * Square, as structure is; parts are told apart by a gap and a shade, not
 * lines.
 */
export function ProportionBar({ segments, legend, total, className, "aria-label": label }: ProportionBarProps) {
  const sum = segments.reduce((n, s) => n + s.value, 0) || 1;
  return (
    <figure className={cx(styles["proportion"], className)} aria-label={label} role="img">
      <div className={styles["bar"]}>
        {segments.map((s) => (
          <span
            key={s.id}
            className={cx(styles["segment"], s.kind === "reserved" && styles["reserved"])}
            style={{ flexGrow: s.value / sum }}
            data-segment={s.id}
          >
            {s.label ? <span className={cx(styles["segmentLabel"], "ds-tnum")}>{s.label}</span> : null}
          </span>
        ))}
      </div>
      {legend || total ? (
        <figcaption className={styles["legend"]}>
          <span className={styles["legendKey"]}>{legend}</span>
          {total ? <span className={cx(styles["legendTotal"], "ds-tnum")}>{total}</span> : null}
        </figcaption>
      ) : null}
    </figure>
  );
}

/** The hatch a `reserved` segment has, as a legend's swatch. */
export function ReservedSwatch() {
  return <span className={styles["swatch"]} aria-hidden />;
}

export interface FitBarProps {
  /** How much of one host it takes, 0..1; null when nobody knows the host's size. */
  readonly share: number | null;
  /** Beside the bar: "50% of a host", or "Unknown". */
  readonly children: ReactNode;
}

/**
 * How much of one host a size takes: a short bar and the words. The bar is
 * the success tone while it fits; an unknown share draws no bar, only its
 * words, so "Unknown" never looks like "none".
 */
export function FitBar({ share, children }: FitBarProps) {
  return (
    <span className={styles["fit"]}>
      {share !== null ? (
        <span className={styles["fitTrack"]} role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(share * 100)}>
          <span className={styles["fitFill"]} style={{ width: `${Math.max(4, Math.min(100, share * 100))}%` }} />
        </span>
      ) : null}
      <span className={cx(styles["fitText"], share === null && styles["fitUnknown"])}>{children}</span>
    </span>
  );
}

export interface MachineChipProps {
  /** The size's name, strong. */
  readonly name: string;
  /** Its spec, muted: "16 CPUs · 48 GiB · 200 GiB". */
  readonly spec: string;
  /** What the tooltip says: where the size came from, and when it changes. */
  readonly tooltip?: ReactNode;
  /** `cube`: the image a session runs in, in the same grammar ("Image: …"). */
  readonly icon?: "chip" | "cube" | undefined;
  readonly "data-testid"?: string | undefined;
}

/**
 * The machine a session runs on, in its header beside the model: the chip
 * glyph, the size's name and its spec, with where it came from in a
 * tooltip. A button only so the tooltip opens on focus; it does nothing on
 * a press. With the cube glyph it is the image the session runs in.
 */
export function MachineChip({ name, spec, tooltip, icon = "chip", "data-testid": testId }: MachineChipProps) {
  const chip = (
    <button type="button" className={styles["chip"]} data-testid={testId} aria-label={`${icon === "cube" ? "Image" : "Machine"}: ${name}, ${spec}`}>
      <Icon name={icon} size={12} className={styles["chipIcon"]} />
      <span className={cx(styles["chipName"], "ds-cap")}>{name}</span>
      <span className={cx(styles["chipSpec"], "ds-cap", "ds-tnum")}>{spec}</span>
    </button>
  );
  return tooltip ? <Tooltip content={tooltip} side="bottom" keepOnPress>{chip}</Tooltip> : chip;
}

/** Who uses something: a row of small faces (agents' tiles, projects' squares), then the words. */
export function UsedBy({ faces, children }: { readonly faces: ReadonlyArray<ReactNode>; readonly children: ReactNode }) {
  return (
    <span className={styles["usedBy"]}>
      {faces.length ? <span className={styles["usedFaces"]}>{faces}</span> : null}
      <span className={styles["usedWords"]}>{children}</span>
    </span>
  );
}

/** A tooltip's body for a machine: its name as a heading, then the words. */
export function MachineTip({ name, children }: { readonly name: string; readonly children: ReactNode }) {
  return (
    <span className={styles["tip"]}>
      <b className={styles["tipTitle"]}>Machine: {name}</b>
      <span className={styles["tipBody"]}>{children}</span>
    </span>
  );
}

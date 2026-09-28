import type { HTMLAttributes, ReactNode } from "react";
import type { PreviewStage } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Button } from "../primitives/Button.tsx";
import { Callout } from "../primitives/Layout.tsx";
import styles from "./PreviewStages.module.css";

/** A preview's stages, in order. */
export const PREVIEW_STAGES = ["scheduling", "cloning", "setup", "starting", "ready"] as const satisfies readonly PreviewStage[];

export interface PreviewStagesProps extends HTMLAttributes<HTMLDivElement> {
  readonly stage: PreviewStage;
  /** The branch being cloned, for the second step's words. */
  readonly branch?: string | null | undefined;
  /** What setup runs ("npm ci, make deps"), for the third step's words. */
  readonly setup?: string | null | undefined;
  /** How long the current stage has taken, beside it. */
  readonly elapsed?: ReactNode;
}

const STAGE_WORDS: Record<PreviewStage, string> = {
  scheduling: "Scheduling",
  cloning: "Cloning",
  setup: "Setup",
  starting: "Starting servers",
  ready: "Ready",
};

/**
 * A branch preview coming up, as a small step strip: Scheduling › Cloning
 * › Setup › Starting servers › Ready. Done steps are checked, the current
 * one spins with how long it has taken, the rest are hollow.
 */
export function PreviewStages({ stage, branch, setup, elapsed, className, ...rest }: PreviewStagesProps) {
  const at = PREVIEW_STAGES.indexOf(stage);
  return (
    <div className={cx(styles["root"], className)} aria-label="Preview progress" data-stage={stage} {...rest}>
      {PREVIEW_STAGES.map((s, i) => {
        const done = i < at;
        const now = i === at;
        const words = s === "cloning" && branch ? `${STAGE_WORDS[s]} ${branch}` : s === "setup" && setup ? `${STAGE_WORDS[s]}: ${setup}` : STAGE_WORDS[s];
        return (
          <span key={s} className={styles["item"]}>
            {i > 0 ? <span className={styles["sep"]}><Icon name="chevron-right" size={10} /></span> : null}
            <span className={cx(styles["step"], done && styles["done"], now && styles["now"])} aria-current={now ? "step" : undefined}>
              <span className={styles["glyph"]}>
                <Icon name={done ? "check" : now && s !== "ready" ? "spinner" : "circle"} size={12} strokeWidth={2} />
              </span>
              {words}
              {now && elapsed ? <span className={styles["elapsed"]}> · {elapsed}</span> : null}
            </span>
          </span>
        );
      })}
    </div>
  );
}

export interface ServersMovedProps {
  /** "14:32" */
  readonly at: ReactNode;
  readonly fromHost?: string | null | undefined;
  readonly toHost?: string | null | undefined;
  readonly onStartAll?: (() => void) | undefined;
  readonly busy?: boolean | undefined;
}

/** The run moved host and its servers stopped with the old placement: say so, with Start all. */
export function ServersMoved({ at, fromHost, toHost, onStartAll, busy }: ServersMovedProps) {
  const hosts = fromHost && toHost ? ` (${fromHost} → ${toHost})` : toHost ? ` (to ${toHost})` : "";
  return (
    <Callout tone="attention" data-testid="servers-moved">
      <span className={styles["moved"]}>
        <span>
          This run moved to another host at <b>{at}</b>{hosts} — servers were stopped. Their checkout and state came along; start them again to serve from the new host.
        </span>
        {onStartAll ? (
          <Button size="sm" variant="primary" leadingIcon="play" disabled={busy} onClick={onStartAll} data-testid="servers-moved-start-all">
            Start all
          </Button>
        ) : null}
      </span>
    </Callout>
  );
}

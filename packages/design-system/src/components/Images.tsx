import { useMemo, useState, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Badge } from "../primitives/Badge.tsx";
import { Button } from "../primitives/Button.tsx";
import { DiffFile } from "./DiffView.tsx";
import { Segmented } from "./ScreenHeader.tsx";
import { PersonAvatar } from "./PersonAvatar.tsx";
import { AgentAvatar } from "./AgentAvatar.tsx";
import { lineDiff } from "../util/lineDiff.ts";
import styles from "./Images.module.css";

/*
 * The image library's pieces: the builder's queue in a line, a build's
 * stages, an image's state in a row, and its history with diffs. The
 * design system knows no images; the app passes words and numbers.
 */

// ---------------------------------------------------------------------------
// The queue
// ---------------------------------------------------------------------------

export interface BuildQueueStripProps {
  /** What is building now: "node-pnpm v5", and for how long; null for nothing. */
  readonly building: { readonly label: ReactNode; readonly elapsed?: ReactNode } | null;
  /** What waits, in the builder's order: "python-uv v3". */
  readonly waiting: ReadonlyArray<ReactNode>;
  /** Open the build that runs. */
  readonly onOpen?: (() => void) | undefined;
  /** The limits every build has, on the right: "Rootless", "1.5 CPU", "1.5 GB", "One at a time". */
  readonly limits?: ReadonlyArray<string> | undefined;
  /** Builds are off: this says why, in place of the queue. */
  readonly unavailable?: ReactNode;
}

/**
 * The builder's queue in one line over the images list: what builds now,
 * with a breathing dot, then how many wait and which; the limits every
 * build runs under on the right.
 */
export function BuildQueueStrip({ building, waiting, onOpen, limits, unavailable }: BuildQueueStripProps) {
  return (
    <div className={styles["queue"]} data-testid="build-queue">
      {unavailable ? (
        <span className={styles["queueNow"]}>
          <Icon name="warning" size={14} />
          {unavailable}
        </span>
      ) : building ? (
        <span className={styles["queueNow"]}>
          <span className="ds-live-dot" aria-hidden />
          <b>Building {building.label}</b>
          {building.elapsed ? <span className={cx(styles["muted"], "ds-tnum")}>· {building.elapsed}</span> : null}
        </span>
      ) : (
        <span className={cx(styles["queueNow"], styles["muted"])}>
          <Icon name="check" size={14} />
          Nothing building
        </span>
      )}
      {!unavailable && waiting.length > 0 ? (
        <span className={styles["queueWaiting"]}>
          <span className={styles["muted"]}>·</span> {waiting.length} waiting:{" "}
          {waiting.map((w, i) => (
            <span key={i}>
              {i > 0 ? ", " : null}
              {w}
            </span>
          ))}
        </span>
      ) : null}
      {onOpen && building && !unavailable ? (
        <button type="button" className={styles["link"]} onClick={onOpen}>
          Open
        </button>
      ) : null}
      <span className={styles["spacer"]} />
      {limits?.length ? (
        <span className={styles["limits"]}>
          {limits.map((l) => (
            <span key={l}>{l}</span>
          ))}
        </span>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// A build's stages
// ---------------------------------------------------------------------------

export type BuildStageState = "done" | "current" | "todo" | "failed";

export interface BuildStage {
  readonly id: string;
  readonly label: ReactNode;
  readonly detail?: ReactNode;
  readonly state: BuildStageState;
}

const STAGE_ICON: Record<BuildStageState, "check" | "circle" | "cross" | null> = {
  done: "check",
  todo: "circle",
  failed: "cross",
  current: null,
};

/**
 * Where a build is: waiting → building → pushed and published, each a
 * square cell with its state's glyph and words; the current one on the
 * info tint with a breathing dot, a failed one on danger's.
 */
export function BuildStages({ stages }: { readonly stages: ReadonlyArray<BuildStage> }) {
  return (
    <ol className={styles["stages"]} aria-label="Build stages">
      {stages.map((s) => (
        <li key={s.id} className={cx(styles["stage"], styles[`stage-${s.state}`])} data-state={s.state} aria-current={s.state === "current" ? "step" : undefined}>
          <span className={styles["stageMark"]} aria-hidden>
            {s.state === "current" ? <span className="ds-live-dot" /> : <Icon name={STAGE_ICON[s.state]!} size={12} />}
          </span>
          <span className={styles["stageText"]}>
            <b>{s.label}</b>
            {s.detail ? <span className={styles["stageDetail"]}>{s.detail}</span> : null}
          </span>
        </li>
      ))}
    </ol>
  );
}

// ---------------------------------------------------------------------------
// An image's state, in a row
// ---------------------------------------------------------------------------

export type ImageStateKind = "published" | "building" | "waiting" | "failed" | "draft" | "none" | "archived";

const STATE_TONE: Record<ImageStateKind, "success" | "info" | "neutral" | "danger" | "attention"> = {
  published: "success",
  building: "info",
  waiting: "neutral",
  failed: "danger",
  draft: "attention",
  none: "neutral",
  archived: "neutral",
};

/** An image's state as words with a dot in its tone: "Published · 2h ago", "v4 failed · v3 still live". */
export function ImageState({ kind, children }: { readonly kind: ImageStateKind; readonly children: ReactNode }) {
  return (
    <span className={cx(styles["state"], styles[`tone-${STATE_TONE[kind]}`])} data-state={kind}>
      {kind === "building" ? (
        <span className="ds-live-dot" aria-hidden />
      ) : (
        <span className={cx(styles["stateDot"], (kind === "waiting" || kind === "none") && styles["stateDotHollow"])} aria-hidden />
      )}
      <span>{children}</span>
    </span>
  );
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

export interface ImageHistoryVersion {
  readonly id: string;
  /** null for a draft. */
  readonly number: number | null;
  readonly state: "draft" | "queued" | "building" | "pushing" | "published" | "failed" | "superseded" | "cancelled";
  readonly containerfile: string;
  readonly note: string;
  /** null: dude (a rebuild on a new base). */
  readonly author: { readonly id: string; readonly name: string; readonly photoUrl?: string | null } | null;
  readonly when: ReactNode;
  /** What it was built on: "acme-base v6". */
  readonly builtOn?: ReactNode;
  readonly error?: string | null | undefined;
  /** Runs in it may start containers; absent where an image says nothing of it. */
  readonly canRunContainers?: boolean | undefined;
}

export interface ImageHistoryProps {
  readonly versions: ReadonlyArray<ImageHistoryVersion>;
  /** The id of the published version. */
  readonly publishedId: string | null;
  /** Publish an older built version again; absent for someone who may not. */
  readonly onRepublish?: ((version: ImageHistoryVersion) => void) | undefined;
  /** Open a failed draft's build, or a version's. */
  readonly onOpenBuild?: ((version: ImageHistoryVersion) => void) | undefined;
  readonly initialId?: string | undefined;
}

const VERSION_BADGE: Partial<Record<ImageHistoryVersion["state"], { tone: "success" | "info" | "danger" | "attention" | "neutral"; words: string }>> = {
  published: { tone: "success", words: "published" },
  building: { tone: "info", words: "building" },
  pushing: { tone: "info", words: "pushing" },
  queued: { tone: "neutral", words: "waiting" },
  failed: { tone: "danger", words: "failed" },
  draft: { tone: "attention", words: "draft" },
  cancelled: { tone: "neutral", words: "cancelled" },
};

const versionName = (v: ImageHistoryVersion) => (v.number === null ? "draft" : `v${v.number}`);

/** "on" or "off" for a version that says, else undefined. */
const onOff = (v: ImageHistoryVersion | undefined) => (v?.canRunContainers === undefined ? undefined : v.canRunContainers ? "on" : "off");

/** "Can run containers" as a version flipped it against the one before; null when it did not. */
function flipped(v: ImageHistoryVersion, before: ImageHistoryVersion | undefined): "on" | "off" | null {
  const now = onOff(v);
  if (now === undefined || !before) return null;
  return onOff(before) !== now ? now : null;
}

/**
 * Each version's predecessor: the next older one not cancelled, in one
 * pass from the oldest.
 */
function predecessors(versions: ReadonlyArray<ImageHistoryVersion>): Array<ImageHistoryVersion | undefined> {
  const out = new Array<ImageHistoryVersion | undefined>(versions.length);
  let older: ImageHistoryVersion | undefined;
  for (let i = versions.length - 1; i >= 0; i--) {
    const v = versions[i]!;
    out[i] = older;
    if (v.state !== "cancelled") older = v;
  }
  return out;
}

/**
 * Every version of an image, newest first — failed ones and the draft too —
 * and the selected one's Containerfile against the one before it or
 * against the published one. A built version that is not published can be
 * published again, at once: its image is still in the registry.
 */
export function ImageHistory({ versions, publishedId, onRepublish, onOpenBuild, initialId }: ImageHistoryProps) {
  const [selected, setSelected] = useState(initialId ?? versions[0]?.id ?? null);
  const [against, setAgainst] = useState<"previous" | "published">("previous");
  const index = Math.max(0, versions.findIndex((v) => v.id === selected));
  const version = versions[index];
  const published = versions.find((v) => v.id === publishedId);
  const before = useMemo(() => predecessors(versions), [versions]);
  const base = against === "published" ? published : before[index];
  const diff = useMemo(() => (version ? lineDiff(base?.containerfile ?? "", version.containerfile) : null), [version, base]);
  if (!version || !diff) return <p className={styles["muted"]}>No versions yet.</p>;
  const canRepublish = onRepublish && version.id !== publishedId && (version.state === "superseded" || version.state === "published");
  const title = base ? (
    <>
      {versionName(base)}
      {base.id === publishedId ? " (published)" : ""} → {versionName(version)}
    </>
  ) : (
    <>{versionName(version)}, whole</>
  );
  return (
    <div className={styles["history"]}>
      <ol className={styles["versions"]} aria-label="Versions">
        {versions.map((v, i) => {
          const badge = v.id === publishedId ? VERSION_BADGE.published : v.state === "superseded" ? null : VERSION_BADGE[v.state];
          const flip = flipped(v, before[i]);
          return (
            <li key={v.id}>
              <button
                type="button"
                className={cx(styles["version"], v.id === version.id && styles["versionCurrent"])}
                aria-current={v.id === version.id ? "true" : undefined}
                data-version={v.number ?? "draft"}
                onClick={() => setSelected(v.id)}
              >
                {v.author ? <PersonAvatar person={v.author} size={20} /> : <AgentAvatar role="system" size="sm" />}
                <span className={styles["versionText"]}>
                  <span className={styles["versionHead"]}>
                    <b className={cx(v.state === "failed" && styles["failedName"])}>{versionName(v)}</b>
                    {badge ? (
                      <Badge size="sm" tone={badge.tone}>
                        {v.state === "failed" && v.number !== null ? "failed draft" : badge.words}
                      </Badge>
                    ) : null}
                  </span>
                  {v.note ? <span className={styles["versionNote"]}>{v.note}</span> : null}
                  {flip ? (
                    <span className={styles["versionFlag"]} data-testid="version-flag">
                      <Icon name="cube" size={12} /> Can run containers turned {flip}
                    </span>
                  ) : null}
                  <small className={styles["muted"]}>
                    {v.author?.name ?? "dude"} · {v.when}
                    {v.builtOn ? <> · on {v.builtOn}</> : null}
                  </small>
                </span>
              </button>
            </li>
          );
        })}
      </ol>
      <div className={styles["diffPane"]}>
        <div className={styles["diffHead"]}>
          <b className={styles["diffTitle"]}>{title}</b>
          <span className={styles["add"]}>+{diff.additions}</span>
          <span className={styles["del"]}>−{diff.deletions}</span>
          <span className={styles["spacer"]} />
          <Segmented
            label="Compare"
            size="sm"
            value={against}
            onChange={setAgainst}
            options={[
              { value: "previous", label: "With the one before" },
              { value: "published", label: "With published", disabled: !published },
            ]}
          />
          {canRepublish ? (
            <Button size="sm" variant="secondary" leadingIcon="arrow-up" onClick={() => onRepublish(version)}>
              Publish {versionName(version)} again
            </Button>
          ) : null}
          {onOpenBuild && version.state !== "draft" ? (
            <Button size="sm" variant="quiet" onClick={() => onOpenBuild(version)}>
              Build log
            </Button>
          ) : null}
        </div>
        {version.error ? (
          <p className={styles["versionError"]}>
            <Icon name="alert" size={14} /> {version.error}
          </p>
        ) : null}
        {onOff(version) !== undefined && (!base || onOff(base) !== onOff(version)) ? (
          <div className={styles["propDiff"]} data-testid="flag-diff">
            <Icon name="cube" size={14} />
            <span>Can run containers</span>
            {base && onOff(base) !== undefined ? (
              <>
                <s className={styles["propBefore"]}>{onOff(base)}</s>
                <span>→</span>
                <ins className={styles["propAfter"]}>{onOff(version)}</ins>
              </>
            ) : (
              <span className={styles["propValue"]}>{onOff(version)}</span>
            )}
          </div>
        ) : null}
        <DiffFile
          className={styles["diff"]}
          file={{ path: "Containerfile", hunks: diff.hunks, additions: diff.additions, deletions: diff.deletions }}
        />
      </div>
    </div>
  );
}

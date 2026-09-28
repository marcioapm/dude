import { useMemo, useState, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { lineDiff } from "../util/lineDiff.ts";
import { Button } from "../primitives/Button.tsx";
import { DiffFile } from "./DiffView.tsx";
import { PersonAvatar } from "./PersonAvatar.tsx";
import { AgentAvatar } from "./AgentAvatar.tsx";
import { Markdown } from "./Markdown.tsx";
import { Segmented } from "./ScreenHeader.tsx";
import styles from "./PromptHistory.module.css";

export interface PromptHistoryVersion {
  readonly id: string;
  /** 1 for the first. */
  readonly number: number;
  readonly body: string;
  readonly note: string;
  /** Who saved it; null for dude's own prompt, where a history starts. */
  readonly author: { readonly id: string; readonly name: string } | null;
  /** When, as a person reads it ("yesterday, 16:42"). */
  readonly when: string;
  readonly current: boolean;
  /** "Adds to Acme's", "Replaces Acme's": a project's version says how it goes with its organization's. */
  readonly mode?: ReactNode;
  /** Sessions told with it: how many, and links to the latest. */
  readonly sessions: { readonly count: number; readonly recent: ReadonlyArray<{ readonly id: string; readonly label: ReactNode; readonly onOpen?: (() => void) | undefined }> };
}

export interface PromptHistoryProps {
  /** Newest first. */
  readonly versions: ReadonlyArray<PromptHistoryVersion>;
  /** Save this version again, as the newest. Absent: nobody here may. */
  readonly onRestore?: ((id: string) => Promise<void> | void) | undefined;
  /** For "since it was saved, in …" on the current version. */
  readonly emptyText?: ReactNode;
}

/**
 * Every version of a prompt, newest first: who saved it, why, and what
 * changed. Selecting one shows its changes against the version before it
 * (or the whole prompt), how many sessions were told with it, and — for
 * any but the current one — Restore, which saves it again as the newest,
 * so the history only ever grows.
 */
export function PromptHistory({ versions, onRestore, emptyText = "No versions yet." }: PromptHistoryProps) {
  const [selected, setSelected] = useState(versions[0]?.id ?? null);
  const [view, setView] = useState<"changes" | "whole">("changes");
  const [restoring, setRestoring] = useState(false);
  const index = Math.max(0, versions.findIndex((v) => v.id === selected));
  const version = versions[index];
  const previous = versions[index + 1];
  // Each version against the one before: its +/- in the list, and the
  // selected one's hunks.
  const stats = useMemo(
    () => new Map(versions.map((v, i) => [v.id, lineDiff(versions[i + 1]?.body ?? "", v.body)] as const)),
    [versions],
  );
  const diff = version ? stats.get(version.id) : undefined;

  if (!version || !diff) return <p className={styles["empty"]}>{emptyText}</p>;

  return (
    <div className={styles["root"]}>
      <ol className={styles["list"]} aria-label="Versions">
        {versions.map((v) => {
          const s = stats.get(v.id)!;
          return (
            <li key={v.id}>
              <button
                type="button"
                className={cx(styles["item"], v.id === version.id && styles["itemCurrent"])}
                aria-current={v.id === version.id ? "true" : undefined}
                data-version={v.number}
                onClick={() => setSelected(v.id)}
              >
                {v.author ? <PersonAvatar person={v.author} size={24} /> : <AgentAvatar role="system" size="md" />}
                <span className={styles["itemText"]}>
                  <span className={styles["itemHead"]}>
                    <b>v{v.number}</b>
                    {v.current ? <span className={styles["currentTag"]}>current</span> : null}
                    <span className={styles["stats"]}>
                      <span className={styles["add"]}>+{s.additions}</span> <span className={styles["del"]}>−{s.deletions}</span>
                    </span>
                  </span>
                  <small>
                    {v.author?.name ?? "dude"} · {v.when}
                  </small>
                  {v.note ? <span className={styles["itemNote"]}>{v.note}</span> : null}
                </span>
              </button>
            </li>
          );
        })}
      </ol>
      <div className={styles["main"]}>
        <div className={styles["mainHead"]}>
          <b className={styles["mainTitle"]}>v{version.number}</b>
          <span className={styles["by"]}>
            by {version.author?.name ?? "dude"} · {version.when}
            {version.mode ? <> · {version.mode}</> : null}
          </span>
          <span className={styles["spacer"]} />
          <Segmented
            label="Show"
            size="sm"
            value={view}
            onChange={setView}
            options={[
              { value: "changes", label: "Changes" },
              { value: "whole", label: "Whole prompt" },
            ]}
          />
          {!version.current && onRestore ? (
            <Button
              variant="secondary"
              leadingIcon="retry"
              disabled={restoring}
              data-testid="prompt-restore"
              onClick={async () => {
                setRestoring(true);
                try {
                  await onRestore(version.id);
                } finally {
                  setRestoring(false);
                }
              }}
            >
              Restore v{version.number}
            </Button>
          ) : null}
        </div>
        {version.note ? <p className={styles["note"]}>“{version.note}”</p> : null}
        {view === "changes" ? (
          <>
            <p className={styles["compared"]}>{previous ? `Compared with v${previous.number}` : "The first version"}</p>
            {diff.hunks.length ? (
              <DiffFile file={{ path: `v${previous?.number ?? 0} → v${version.number}`, hunks: diff.hunks, additions: diff.additions, deletions: diff.deletions }} />
            ) : (
              <p className={styles["compared"]}>The same text as v{previous?.number}.</p>
            )}
          </>
        ) : (
          <div className={styles["whole"]}>
            <Markdown source={version.body || "_Empty: nothing added._"} variant="document" />
          </div>
        )}
        <div className={styles["used"]}>
          <span className="ds-label">Used by</span>
          <p>
            <b>
              {version.sessions.count} {version.sessions.count === 1 ? "session" : "sessions"}
            </b>{" "}
            {version.current ? "since it was saved" : "ran with this version"}
            {version.sessions.recent.length ? " — the latest:" : "."}
          </p>
          {version.sessions.recent.length ? (
            <ul className={styles["sessions"]}>
              {version.sessions.recent.map((s) => (
                <li key={s.id}>
                  {s.onOpen ? (
                    <button type="button" className={styles["sessionLink"]} onClick={s.onOpen}>
                      {s.label}
                    </button>
                  ) : (
                    s.label
                  )}
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      </div>
    </div>
  );
}

import { useId, type ReactNode } from "react";
import { Select } from "../primitives/Select.tsx";
import styles from "./BeforePullRequestFact.module.css";

/** Who may change the fact now: its owner; nobody, with why; or someone else, who reads it. */
export type BeforePullRequestFactState =
  | { readonly kind: "editable" }
  | { readonly kind: "locked"; readonly reason: string }
  | { readonly kind: "read-only"; readonly owner: string };

export interface BeforePullRequestFactProps<T extends string> {
  readonly value: T;
  readonly options: ReadonlyArray<{ readonly value: T; readonly label: string; readonly description?: ReactNode }>;
  readonly state: BeforePullRequestFactState;
  readonly onValueChange?: ((value: T) => void) | undefined;
  /** Under it while editable: when it is read. */
  readonly hint?: string | undefined;
  readonly label?: string | undefined;
  /** While a change is saved. */
  readonly busy?: boolean | undefined;
}

/**
 * A task's delivery choice that can still change, under the task's facts
 * in the Overview rail: a compact Select for its owner, disabled with the
 * reason once nothing it says would apply, and the value as text for
 * anyone else. A disabled trigger takes no focus, so a locked one carries
 * its reason in its name, and the visible reason is hidden from the
 * accessibility tree so it is read once.
 */
export function BeforePullRequestFact<T extends string>({
  value, options, state, onValueChange, hint, label = "Before the pull request", busy,
}: BeforePullRequestFactProps<T>) {
  const id = useId();
  const picked = options.find((o) => o.value === value);
  const note = state.kind === "locked" ? state.reason : state.kind === "read-only" ? `Only ${state.owner} can change it.` : hint;
  return (
    <div className={styles["root"]} data-state={state.kind}>
      {state.kind === "read-only" ? (
        <>
          <span className={styles["label"]} id={id}>{label}</span>
          <span className={styles["value"]} aria-labelledby={id}>{picked?.label}</span>
        </>
      ) : (
        <>
          <label className={styles["label"]} htmlFor={id}>{label}</label>
          <Select<T>
            size="sm"
            id={id}
            value={value}
            onValueChange={onValueChange}
            disabled={state.kind === "locked" || busy}
            className={styles["select"]}
            aria-label={state.kind === "locked" ? `${label}: ${picked?.label ?? ""}. ${state.reason}` : label}
            options={options.map((o) => ({ value: o.value, label: o.label, description: o.description }))}
          />
        </>
      )}
      {note ? <span className={styles["note"]} aria-hidden={state.kind === "locked" || undefined}>{note}</span> : null}
    </div>
  );
}

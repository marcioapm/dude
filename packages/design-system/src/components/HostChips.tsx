import { useState, type HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { IconButton } from "../primitives/Button.tsx";
import { Input } from "../primitives/Input.tsx";
import styles from "./HostChips.module.css";

interface EditableHostChips {
  readonly onChange: (hosts: string[]) => void;
  readonly readOnly?: false | undefined;
  readonly muted?: undefined;
  readonly disabled?: boolean | undefined;
  readonly placeholder?: string | undefined;
  /** The field's accessible name. */
  readonly label?: string | undefined;
  /** Why a host would be refused, or null: one typed stays in the field, saying why; one listed already is marked. */
  readonly validate?: ((host: string) => string | null) | undefined;
}

interface ReadOnlyHostChips {
  readonly readOnly: true;
  /** Quieter still: what is always there, never listed (the model's host). */
  readonly muted?: boolean | undefined;
  readonly onChange?: undefined;
  readonly disabled?: undefined;
  readonly placeholder?: undefined;
  readonly label?: undefined;
  readonly validate?: undefined;
}

export type HostChipsProps = Omit<HTMLAttributes<HTMLDivElement>, "onChange"> & { readonly hosts: ReadonlyArray<string> } & (EditableHostChips | ReadOnlyHostChips);

/**
 * A list of hosts as chips, each with its remove, and a field to add one:
 * Enter, a comma or a space commits what is typed; Backspace on an empty
 * field takes the last chip back. A host `validate` refuses stays in the
 * field with the reason under it. `readOnly` lists hosts that are changed
 * elsewhere (an organisation's, on a project's page): no remove, no field.
 */
export function HostChips(props: HostChipsProps) {
  const { hosts, onChange, readOnly, muted, disabled, placeholder = "Add host…", label = "Add a host", validate, className, ...rest } = props;
  const [draft, setDraft] = useState("");
  const [problem, setProblem] = useState<string | null>(null);
  if (readOnly) {
    return (
      <div className={cx(styles["chips"], className)} data-readonly="true" data-muted={muted ? "true" : undefined} {...rest}>
        {hosts.map((h) => (
          <span key={h} className={cx(styles["chip"], styles["fixed"])} data-host={h}>{h}</span>
        ))}
      </div>
    );
  }
  const commit = () => {
    const host = draft.trim().replace(/,$/, "").toLowerCase();
    const refused = host ? (validate?.(host) ?? null) : null;
    setProblem(refused);
    if (refused) return;
    setDraft("");
    if (host && !hosts.includes(host)) onChange([...hosts, host]);
  };
  return (
    <div className={cx(styles["chips"], className)} {...rest}>
      {hosts.map((h) => {
        const bad = validate?.(h) ?? null;
        return (
          <span key={h} className={styles["chip"]} data-host={h} data-invalid={bad ? "true" : undefined} title={bad ?? undefined}>
            {h}
            <IconButton size="sm" icon="close" label={`Remove ${h}`} disabled={disabled} onClick={() => onChange(hosts.filter((x) => x !== h))} />
          </span>
        );
      })}
      <Input
        aria-label={label}
        placeholder={placeholder}
        size="sm"
        mono
        className={styles["add"]}
        value={draft}
        disabled={disabled}
        error={problem ?? undefined}
        onChange={(e) => {
          setDraft(e.target.value);
          if (problem) setProblem(null);
        }}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === "," || e.key === " ") {
            e.preventDefault();
            commit();
          } else if (e.key === "Backspace" && draft === "" && hosts.length > 0) {
            onChange(hosts.slice(0, -1));
          }
        }}
      />
    </div>
  );
}

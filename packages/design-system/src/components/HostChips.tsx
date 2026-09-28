import { useState, type HTMLAttributes } from "react";
import { cx } from "../util/cx.ts";
import { IconButton } from "../primitives/Button.tsx";
import { Input } from "../primitives/Input.tsx";
import styles from "./HostChips.module.css";

export interface HostChipsProps extends Omit<HTMLAttributes<HTMLDivElement>, "onChange"> {
  readonly hosts: ReadonlyArray<string>;
  readonly onChange: (hosts: string[]) => void;
  readonly disabled?: boolean | undefined;
  readonly placeholder?: string | undefined;
  /** The field's accessible name. */
  readonly label?: string | undefined;
}

/**
 * A list of hosts as chips, each with its remove, and a field to add one:
 * Enter, a comma or a space commits what is typed; Backspace on an empty
 * field takes the last chip back.
 */
export function HostChips({ hosts, onChange, disabled, placeholder = "Add host…", label = "Add a host", className, ...rest }: HostChipsProps) {
  const [draft, setDraft] = useState("");
  const commit = () => {
    const host = draft.trim().replace(/,$/, "").toLowerCase();
    setDraft("");
    if (host && !hosts.includes(host)) onChange([...hosts, host]);
  };
  return (
    <div className={cx(styles["chips"], className)} {...rest}>
      {hosts.map((h) => (
        <span key={h} className={styles["chip"]} data-host={h}>
          {h}
          <IconButton size="sm" icon="close" label={`Remove ${h}`} disabled={disabled} onClick={() => onChange(hosts.filter((x) => x !== h))} />
        </span>
      ))}
      <Input
        aria-label={label}
        placeholder={placeholder}
        size="sm"
        mono
        className={styles["add"]}
        value={draft}
        disabled={disabled}
        onChange={(e) => setDraft(e.target.value)}
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

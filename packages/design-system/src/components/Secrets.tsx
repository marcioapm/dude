import { Icon } from "../icons/index.tsx";
import { Badge } from "../primitives/Badge.tsx";
import { Checkbox } from "../primitives/Checkbox.tsx";
import { Fieldset } from "../primitives/Layout.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import { cx } from "../util/cx.ts";
import chip from "./Machines.module.css";
import styles from "./Secrets.module.css";

/*
 * Project secrets: who gets one, and which ones an agent session got. The
 * design system knows recipients only as ids, labels and descriptions the
 * app passes, and only ever names, never values.
 */

export interface SecretRecipientOption {
  readonly id: string;
  readonly label: string;
  readonly description: string;
}

export interface GivenToProps {
  /** Labels of the recipients, in the app's order. */
  readonly given: readonly string[];
}

/** A secret's recipients in a table cell: neutral badges, or "Nobody" in muted words. */
export function GivenTo({ given }: GivenToProps) {
  if (given.length === 0) return <span className={styles["nobody"]}>Nobody</span>;
  return (
    <span className={styles["given"]}>
      {given.map((g) => <Badge key={g} size="sm">{g}</Badge>)}
    </span>
  );
}

export interface GivenToFieldsetProps {
  readonly options: readonly SecretRecipientOption[];
  readonly value: readonly string[];
  readonly onChange: (next: string[]) => void;
  /** Under the legend: when a change applies. */
  readonly hint?: string | undefined;
  /** Shown in place of the hint, the fieldset marked invalid: none ticked. */
  readonly error?: string | undefined;
  readonly disabled?: boolean | undefined;
  readonly "data-testid"?: string | undefined;
}

/** "Given to": one Checkbox per recipient with its line, in options' order. */
export function GivenToFieldset({ options, value, onChange, hint, error, disabled, "data-testid": testId }: GivenToFieldsetProps) {
  return (
    <Fieldset legend="Given to" hint={hint} error={error} aria-invalid={error ? true : undefined} data-testid={testId}>
      <div className={styles["choices"]}>
        {options.map((o) => (
          <Checkbox key={o.id} name="recipients" value={o.id} label={o.label} description={o.description} disabled={disabled}
            checked={value.includes(o.id)}
            onCheckedChange={(on) => onChange(options.map((x) => x.id).filter((id) => (id === o.id ? on === true : value.includes(id))))} />
        ))}
      </div>
    </Fieldset>
  );
}

export interface SecretsChipProps {
  /** The names of the secrets the session was given, as recorded when it started. */
  readonly names: readonly string[];
  readonly "data-testid"?: string | undefined;
}

export const SECRETS_TIP_SOURCE = "Given by project settings; values are never shown";

/**
 * The project secrets an agent session got, in its header beside the
 * MachineChip: the lock glyph and "N secrets"; its tip lists the names.
 * Nothing for a session given none. A button only so the tip opens on
 * focus; it does nothing on a press.
 */
export function SecretsChip({ names, "data-testid": testId }: SecretsChipProps) {
  if (names.length === 0) return null;
  const count = `${names.length} ${names.length === 1 ? "secret" : "secrets"}`;
  return (
    <Tooltip side="bottom" keepOnPress content={
      <div className={styles["tip"]}>
        <ul className={styles["tipNames"]}>
          {names.map((n) => <li key={n} className="ds-mono">{n}</li>)}
        </ul>
        <p className={styles["tipSource"]}>{SECRETS_TIP_SOURCE}</p>
      </div>
    }>
      <button type="button" className={chip["chip"]} data-testid={testId} aria-label={`Project secrets: ${count}, ${names.join(", ")}`}>
        <Icon name="lock" size={12} className={chip["chipIcon"]} />
        <span className={cx(chip["chipName"], "ds-cap", "ds-tnum")}>{count}</span>
      </button>
    </Tooltip>
  );
}

import type { ReactNode } from "react";
import { sameSecretRef, secretPicksInForce, type SecretRef, type SecretSource } from "@dude/domain";
import { Icon } from "../icons/index.tsx";
import { Badge } from "../primitives/Badge.tsx";
import { Checkbox } from "../primitives/Checkbox.tsx";
import { Fieldset } from "../primitives/Layout.tsx";
import { Tooltip } from "../primitives/Tooltip.tsx";
import { cx } from "../util/cx.ts";
import chip from "./Machines.module.css";
import { SettingSource } from "./Settings.tsx";
import styles from "./Secrets.module.css";

/*
 * Project and organisation secrets: who gets one, which ones an automation
 * picks, and which ones an agent session got. The design system knows
 * recipients only as ids, labels and descriptions the app passes, and only
 * ever names and hints, never values.
 */

export interface SecretRecipientOption {
  readonly id: string;
  readonly label: string;
  readonly description: string;
}

export interface GivenToProps {
  /** Labels of the recipients, in the app's order. */
  readonly given: readonly string[];
  /** What an empty list reads as. Default "Nobody". */
  readonly none?: ReactNode;
}

/** A secret's recipients in a table cell: neutral badges, or `none` in muted words. */
export function GivenTo({ given, none = "Nobody" }: GivenToProps) {
  if (given.length === 0) return <span className={styles["nobody"]}>{none}</span>;
  return (
    <span className={styles["given"]}>
      {given.map((g) => <Badge key={g} size="sm">{g}</Badge>)}
    </span>
  );
}

export interface OverriddenByProps {
  /** The winning secret's name. */
  readonly name: string;
  /** Whose it is, before the name. Default "this project's". */
  readonly by?: string | undefined;
}

/**
 * On a secret a more specific one replaces: the layers glyph and
 * "Overridden by this project's NAME", muted. It takes the place of the
 * row's Given to or a checkbox's description; the row's own name is
 * struck through.
 */
export function OverriddenBy({ name, by = "this project's" }: OverriddenByProps) {
  return (
    <span className={styles["overriddenBy"]} data-testid="overridden-by">
      <Icon name="layers" size={12} className={styles["overriddenIcon"]} />
      <span>Overridden by {by} <code className="ds-mono">{name}</code></span>
    </span>
  );
}

export interface SecretNameProps {
  readonly name: string;
  /** A more specific secret of the same name replaces it: struck through in muted ink. */
  readonly struck?: boolean | undefined;
  /** An inherited row's owner: SettingSource "From <from>" after the name. */
  readonly from?: string | undefined;
  /** Muted xs words after the name, such as which projects have their own. */
  readonly aside?: ReactNode;
}

/** A secret's name in a table cell: mono, struck when overridden, then its "From Acme" marker or aside, wrapping under it. */
export function SecretName({ name, struck, from, aside }: SecretNameProps) {
  return (
    <span className={styles["name"]}>
      <span className={cx("ds-mono", struck && styles["struck"])} data-struck={struck ? "true" : undefined}>{name}</span>
      {from !== undefined ? <SettingSource source="organization" from={from} /> : null}
      {aside ? <span className={styles["aside"]} data-testid="secret-aside">{aside}</span> : null}
    </span>
  );
}

export interface PresetSecretsProps {
  /** The role's label: "Tester". */
  readonly role: string;
  /** Names its runs get from each owner, in order; an owner with none is left out. */
  readonly given: readonly { readonly from: string; readonly names: readonly string[] }[];
  /** Said after "<role> gets" when `given` names nothing. Default "no secrets". */
  readonly none?: string | undefined;
  readonly "data-testid"?: string | undefined;
}

/**
 * A dude-role preset's secrets in place of a SecretPicker, read-only: the
 * lock glyph and "Tester gets NPM_TOKEN, SENTRY_TOKEN from Acme ·
 * DATABASE_SEED_URL from platform", what its role is ticked for.
 */
export function PresetSecrets({ role, given, none = "no secrets", "data-testid": testId }: PresetSecretsProps) {
  const groups = given.filter((g) => g.names.length > 0);
  return (
    <span className={styles["preset"]} data-testid={testId}>
      <Icon name="lock" size={12} className={styles["presetIcon"]} />
      <span>
        {role} gets{" "}
        {groups.length === 0 ? none : groups.map((g, i) => (
          // An organisation and a project may share a display name: the key is the group's place.
          <span key={i}>
            {i ? " · " : null}
            {g.names.map((n, j) => <span key={n}>{j ? ", " : ""}<code className="ds-mono">{n}</code></span>)}
            {" "}<span className={styles["presetFrom"]}>from {g.from}</span>
          </span>
        ))}
      </span>
    </span>
  );
}

export type SecretPick = SecretRef;

export interface SecretChoice {
  readonly name: string;
  /** The value's last characters, as the API shows them. */
  readonly hint: string;
}

export interface SecretPickerProps {
  readonly orgName: string;
  readonly org: readonly SecretChoice[];
  /** A project automation's project, with its own secrets. */
  readonly project?: { readonly name: string; readonly secrets: readonly SecretChoice[] } | undefined;
  readonly value: readonly SecretPick[];
  readonly onChange: (next: SecretPick[]) => void;
  readonly disabled?: boolean | undefined;
  readonly "data-testid"?: string | undefined;
}

/**
 * An automation's secrets: checkbox groups "<org>'s" and, for a project
 * automation, "<project>'s", side by side and stacked when narrow. A name
 * the project also holds cannot be ticked under the organisation: it is
 * disabled, struck through and described by OverriddenBy. An organisation
 * pick of such a name shows, and is passed back, as the project's.
 */
export function SecretPicker({ orgName, org, project, value, onChange, disabled, "data-testid": testId }: SecretPickerProps) {
  const own = new Set((project?.secrets ?? []).map((s) => s.name));
  const picks = secretPicksInForce(value, [...own]);
  const toggle = (pick: SecretPick, on: boolean) =>
    onChange(on ? [...picks.filter((v) => !sameSecretRef(v, pick)), pick] : picks.filter((v) => !sameSecretRef(v, pick)));
  const group = (source: SecretSource, owner: string, list: readonly SecretChoice[]) => (
    <div role="group" aria-label={`${owner}'s`} className={styles["pickGroup"]} data-source={source}>
      <span className={styles["pickHead"]} aria-hidden="true">{owner}&rsquo;s</span>
      {list.length === 0 ? <span className={styles["nobody"]}>None yet.</span> : list.map((s) => {
        const overridden = source === "organization" && own.has(s.name);
        const pick = { source, name: s.name };
        return (
          <div key={s.name} className={styles["pickRow"]} data-overridden={overridden ? "true" : undefined}>
            <Checkbox
              label={<span className={cx("ds-mono", overridden && styles["struck"])}>{s.name}</span>}
              aria-label={`${s.name} (${owner})`}
              checked={!overridden && picks.some((v) => sameSecretRef(v, pick))}
              disabled={disabled || overridden}
              description={overridden ? <OverriddenBy name={s.name} by={`${project!.name}'s`} /> : `…${s.hint}`}
              onCheckedChange={(on) => toggle(pick, on === true)} />
          </div>
        );
      })}
    </div>
  );
  return (
    <div className={styles["picker"]} data-testid={testId}>
      {group("organization", orgName, org)}
      {project ? group("project", project.name, project.secrets) : null}
    </div>
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

export const SECRETS_TIP_SOURCE = "Given by project and organisation settings; values are never shown";

/**
 * The secrets an agent session got, in its header beside the
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
      <button type="button" className={chip["chip"]} data-testid={testId} aria-label={`Secrets: ${count}, ${names.join(", ")}`}>
        <Icon name="lock" size={12} className={chip["chipIcon"]} />
        <span className={cx(chip["chipName"], "ds-cap", "ds-tnum")}>{count}</span>
      </button>
    </Tooltip>
  );
}

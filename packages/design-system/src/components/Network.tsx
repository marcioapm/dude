import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Button } from "../primitives/Button.tsx";
import { Table, TBody, Td, Th, THead, Tr } from "../primitives/Table.tsx";
import styles from "./Network.module.css";

/*
 * What an agent's Run may reach, in the pieces its settings page and its
 * transcript show: toolchains added by name (HostPresets), the hosts its
 * agents tried and were refused (RefusedHosts), and the note under a tool
 * call whose output names one (NetworkRefusedNote).
 */

export interface HostPreset {
  readonly name: string;
  readonly hosts: ReadonlyArray<string>;
}

export interface HostPresetsProps extends HTMLAttributes<HTMLDivElement> {
  readonly presets: ReadonlyArray<HostPreset>;
  /** Whether what a Run gets already reaches a host: a preset it reaches whole is ticked. */
  readonly has: (host: string) => boolean;
  readonly onAdd: (preset: HostPreset) => void;
  readonly disabled?: boolean | undefined;
}

/**
 * Add a toolchain's hosts by its name, so nobody has to know PyPI is two
 * hosts. One the list already reaches whole is ticked and adds nothing.
 */
export function HostPresets({ presets, has, onAdd, disabled, className, ...rest }: HostPresetsProps) {
  return (
    <div className={cx(styles["presets"], className)} {...rest}>
      <span className={styles["presetsLabel"]}>Add</span>
      {presets.map((p) => {
        const whole = p.hosts.every(has);
        return (
          <button key={p.name} type="button" className={styles["preset"]} data-preset={p.name} data-has={whole ? "true" : undefined}
            disabled={disabled || whole} title={p.hosts.join(", ")} onClick={() => onAdd(p)}>
            <Icon name={whole ? "check" : "plus"} size={12} />
            {p.name}
          </button>
        );
      })}
    </div>
  );
}

export interface RefusedHost {
  readonly name: string;
  readonly calls: number;
  readonly roles: ReadonlyArray<string>;
}

export interface RefusedHostsProps extends HTMLAttributes<HTMLDivElement> {
  readonly refused: ReadonlyArray<RefusedHost>;
  /** Whose list Allow adds to: "jervasion". */
  readonly target: string;
  /** Allow one or several; absent for someone who may not change the list. */
  readonly onAllow?: ((names: string[]) => void) | undefined;
  readonly disabled?: boolean | undefined;
}

/** Hosts agents tried and were refused, most called first, each with Allow, and Allow all. */
export function RefusedHosts({ refused, target, onAllow, disabled, className, ...rest }: RefusedHostsProps) {
  return (
    <div className={cx(styles["refused"], className)} {...rest}>
      <Table density="compact">
        <THead>
          <Tr>
            <Th>Host</Th>
            <Th align="right" width="64px">Calls</Th>
            <Th>By</Th>
            {onAllow ? <Th align="right" width="88px"><span className="ds-sr-only">Allow</span></Th> : null}
          </Tr>
        </THead>
        <TBody>
          {refused.map((r) => (
            <Tr key={r.name} data-refused={r.name}>
              <Td mono>{r.name}</Td>
              <Td align="right">{r.calls}</Td>
              <Td muted title={r.roles.join(", ")}>
                <span className={styles["by"]}><Icon name="agent" size={12} />{r.roles.join(", ")}</span>
              </Td>
              {onAllow ? (
                <Td align="right">
                  <Button size="sm" variant="secondary" leadingIcon="plus" disabled={disabled} onClick={() => onAllow([r.name])}>Allow</Button>
                </Td>
              ) : null}
            </Tr>
          ))}
        </TBody>
      </Table>
      {onAllow ? (
        <div className={styles["allowAll"]}>
          <Button size="sm" variant="quiet" leadingIcon="plus" disabled={disabled} onClick={() => onAllow(refused.map((r) => r.name))}>
            Allow all {refused.length}
          </Button>
          <span className={styles["allowAllNote"]}>Adds them to {target}’s list. The next Run on this project gets them.</span>
        </div>
      ) : null}
    </div>
  );
}

export interface NetworkRefusedNoteProps extends HTMLAttributes<HTMLDivElement> {
  readonly host: string;
  readonly project: string;
  /** The organisation whose list the Run also had; null when the project runs on its own list. */
  readonly organization: string | null;
  /** Allow for the project; absent for someone who may not change its list. */
  readonly onAllow?: (() => void) | undefined;
  readonly onSettings?: (() => void) | undefined;
  /** Allowed since: the next Run gets it, this one never will. */
  readonly allowed?: boolean | undefined;
  readonly busy?: boolean | undefined;
}

/**
 * Under a tool call whose output names a host lux refused the Run: which
 * lists it is on neither of, with Allow for the project — the person
 * reading the failed install is the one who can fix it, right there.
 */
export function NetworkRefusedNote({ host, project, organization, onAllow, onSettings, allowed, busy, className, ...rest }: NetworkRefusedNoteProps): ReactNode {
  return (
    <div className={cx(styles["note"], className)} data-testid="network-refused" data-host={host} {...rest}>
      <Icon name="globe" size={14} className={styles["noteIcon"]} />
      <span className={styles["noteText"]}>
        <b>Network refused:</b> <code>{host}</code> is not on {project}’s list{organization ? `, nor ${organization}’s` : ""}.
      </span>
      <span className={styles["noteActions"]}>
        {allowed ? (
          <span className={styles["allowed"]} data-testid="network-allowed"><Icon name="check" size={12} />Allowed · next Run gets it</span>
        ) : onAllow ? (
          <Button size="sm" variant="secondary" leadingIcon="plus" disabled={busy} onClick={onAllow} data-testid="network-allow">Allow for {project}</Button>
        ) : null}
        {onSettings ? <Button size="sm" variant="quiet" onClick={onSettings}>Settings</Button> : null}
      </span>
    </div>
  );
}

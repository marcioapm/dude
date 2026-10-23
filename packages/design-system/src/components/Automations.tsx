import { useId, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { PersonAvatar } from "./PersonAvatar.tsx";
import { Segmented } from "./ScreenHeader.tsx";
import { Input } from "../primitives/Input.tsx";
import { NumberInput } from "../primitives/NumberInput.tsx";
import { Select } from "../primitives/Select.tsx";
import type { Person } from "../util/viewTypes.ts";
import styles from "./Automations.module.css";

/*
 * Automations: work nobody started by hand, on a schedule or a GitHub
 * event. The pieces their lists and settings pages show; the app owns the
 * words that name its organisation and roles.
 */

export type AutomationKindName = "agent" | "command" | "server" | "webhook";

const KIND: Record<AutomationKindName, { icon: IconName; label: string }> = {
  agent: { icon: "agent", label: "Agent" },
  command: { icon: "terminal", label: "Command" },
  server: { icon: "globe", label: "Server" },
  webhook: { icon: "send", label: "Webhook only" },
};

/** The kind as glyph and word; an agent that is one of dude's roles adds "· Tester" in muted text. */
export function KindMark({ kind, role }: { readonly kind: AutomationKindName; readonly role?: string | undefined }) {
  const k = KIND[kind];
  return (
    <span className={styles["kind"]} data-kind={kind}>
      <Icon name={k.icon} size={14} className={styles["kindIcon"]} />
      <span>{k.label}</span>
      {role ? <span className={styles["muted"]}>· {role}</span> : null}
    </span>
  );
}

/** Where an automation lives: the organisation (building glyph) or a project, by name. */
export function ScopeChip({ project, org }: { readonly project?: string | null | undefined; readonly org: string }) {
  return (
    <span className={styles["scope"]} data-scope={project ? "project" : "organization"}>
      <Icon name={project ? "folder" : "building"} size={12} className={styles["scopeIcon"]} />
      <span className={styles["scopeName"]}>{project ?? org}</span>
    </span>
  );
}

/**
 * Who owns an automation: PersonAvatar and name, or only the avatar with
 * the name for screen readers (list cells). `left`: the organisation's
 * admins hold it — a building glyph in info tone and "<org>'s admins".
 */
export function OwnerMark({ owner, avatarOnly, left, org }: {
  readonly owner: Person | null;
  readonly avatarOnly?: boolean | undefined;
  readonly left?: boolean | undefined;
  readonly org: string;
}) {
  if (left || !owner) {
    const name = `${org}'s admins`;
    return (
      <span className={cx(styles["owner"], styles["ownerHeld"])} data-held="true" role={avatarOnly ? "img" : undefined} aria-label={avatarOnly ? name : undefined} title={avatarOnly ? name : undefined}>
        <span className={styles["heldFace"]} aria-hidden><Icon name="building" size={12} /></span>
        {avatarOnly ? null : <span>{name}</span>}
      </span>
    );
  }
  return (
    <span className={styles["owner"]}>
      <PersonAvatar person={owner} size={20} ring={false} title={owner.name} />
      {avatarOnly ? null : <span>{owner.name}</span>}
    </span>
  );
}

/** A trigger in one line: "Daily at 03:00 · Lisbon" or "pull_request · opened, synchronize". */
export function TriggerSummary({ trigger }: {
  readonly trigger: { readonly kind: "schedule"; readonly cron: string; readonly tz: string }
    | { readonly kind: "github"; readonly event: string; readonly actions?: readonly string[] | undefined };
}) {
  if (trigger.kind === "schedule") {
    return (
      <span className={styles["trigger"]}>
        <Icon name="clock" size={14} className={styles["kindIcon"]} />
        <span>{describeCron(trigger.cron)}</span>
        <span className={styles["muted"]}>· {cityOf(trigger.tz)}</span>
      </span>
    );
  }
  return (
    <span className={styles["trigger"]}>
      <Icon name="github" size={14} className={styles["kindIcon"]} />
      <span className="ds-mono">{trigger.event}</span>
      <span className={styles["muted"]}>· {trigger.actions?.length ? trigger.actions.join(", ") : "any action"}</span>
    </span>
  );
}

/** A time zone's city, as a person names it: "Europe/Lisbon" is Lisbon. */
export function cityOf(tz: string): string {
  return (tz.split("/").pop() ?? tz).replaceAll("_", " ");
}

const DAY = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const pad = (n: string) => n.padStart(2, "0");

/** A cron line in words when it is one of the presets, else the line itself. */
export function describeCron(cron: string): string {
  const f = cron.trim().split(/\s+/);
  if (f.length !== 5) return cron;
  const [m, h, dom, mon, dow] = f as [string, string, string, string, string];
  const step = /^\*\/(\d+)$/.exec(m);
  if (step && h === "*" && dom === "*" && mon === "*" && dow === "*") return `Every ${step[1]} minutes`;
  if (/^\d+$/.test(m) && h === "*" && dom === "*" && mon === "*" && dow === "*") return `Hourly at :${pad(m)}`;
  if (!/^\d+$/.test(m) || !/^\d+$/.test(h) || dom !== "*" || mon !== "*") return cron;
  const at = `${pad(h)}:${pad(m)}`;
  if (dow === "*") return `Daily at ${at}`;
  if (dow === "1-5") return `Weekdays at ${at}`;
  if (/^[0-7](,[0-7])*$/.test(dow)) return `${dow.split(",").map((d) => DAY[Number(d) % 7]).join(", ")} at ${at}`;
  return cron;
}

/** Ephemeral (a fresh session each run) or Resumable (one session, kept N days without a run). */
export function SessionMark({ mode, expiryDays }: { readonly mode: "ephemeral" | "resumable"; readonly expiryDays?: number | null | undefined }) {
  return (
    <span className={styles["session"]} data-mode={mode}>
      <Icon name={mode === "resumable" ? "retry" : "circle-dotted"} size={12} className={styles["kindIcon"]} />
      {mode === "resumable" ? `Resumable${expiryDays ? ` · ${expiryDays} d` : ""}` : "Ephemeral"}
    </span>
  );
}

/** The next few times a schedule runs, in its zone. */
export function NextRuns({ times, tz }: { readonly times: readonly Date[]; readonly tz: string }) {
  const fmt = useMemo(() => new Intl.DateTimeFormat("en-GB", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", timeZone: tz }), [tz]);
  if (times.length === 0) return <span className={styles["muted"]}>Never runs: no date matches.</span>;
  return (
    <ol className={styles["nextRuns"]} aria-label="Next runs">
      {times.map((t) => <li key={t.toISOString()}><time dateTime={t.toISOString()}>{fmt.format(t)}</time></li>)}
    </ol>
  );
}

/** A list switch: on or off from a row, its name saying which automation. */
export function ListSwitch({ checked, onCheckedChange, label, disabled, testId }: {
  readonly checked: boolean;
  readonly onCheckedChange: (on: boolean) => void;
  /** The automation's name: the switch reads "Turn <name> on/off". */
  readonly label: string;
  readonly disabled?: boolean | undefined;
  readonly testId?: string | undefined;
}) {
  return (
    <button type="button" role="switch" aria-checked={checked} disabled={disabled} data-testid={testId}
      aria-label={`${label}: ${checked ? "on" : "off"}`} className={cx(styles["listSwitch"], checked && styles["listSwitchOn"])}
      onClick={(e) => { e.stopPropagation(); onCheckedChange(!checked); }}>
      <span className={styles["listThumb"]} />
    </button>
  );
}

/**
 * Values typed as chips: Enter or comma adds, Backspace in an empty field
 * takes the last back. `glob` marks chips with `*` (info tone) and says how
 * many of `universe` each matches.
 */
export function ChipsInput({ values, onChange, label, placeholder, disabled, matches, suggestions, id }: {
  readonly values: readonly string[];
  readonly onChange: (next: string[]) => void;
  readonly label: string;
  readonly placeholder?: string | undefined;
  readonly disabled?: boolean | undefined;
  /** How many a glob chip matches, for its count. */
  readonly matches?: ((value: string) => number) | undefined;
  /** Offered as the person types, grouped. */
  readonly suggestions?: ReadonlyArray<{ readonly group: string; readonly values: readonly string[] }> | undefined;
  readonly id?: string | undefined;
}) {
  const [text, setText] = useState("");
  const [open, setOpen] = useState(false);
  const listId = useId();
  const input = useRef<HTMLInputElement>(null);
  const add = (v: string) => {
    const t = v.trim();
    if (t && !values.includes(t)) onChange([...values, t]);
    setText("");
  };
  const filtered = (suggestions ?? []).map((g) => ({ group: g.group, values: g.values.filter((v) => !values.includes(v) && v.toLowerCase().includes(text.toLowerCase())) }))
    .filter((g) => g.values.length > 0);
  function onKey(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      add(text);
    } else if (e.key === "Backspace" && text === "" && values.length > 0) {
      e.preventDefault();
      setText(values[values.length - 1]!);
      onChange(values.slice(0, -1));
    } else if (e.key === "Escape") {
      setOpen(false);
    }
  }
  return (
    <div className={cx(styles["chips"], disabled && styles["chipsDisabled"])} onClick={() => input.current?.focus()}>
      {values.map((v) => {
        const glob = v.includes("*");
        const n = glob && matches ? matches(v) : null;
        return (
          <span key={v} className={styles["chip"]} data-glob={glob ? "true" : undefined}>
            <span className="ds-mono">{v}</span>
            {n !== null ? <span className={styles["chipCount"]}>{n}</span> : null}
            {disabled ? null : (
              <button type="button" className={styles["chipRemove"]} aria-label={`Remove ${v}`} onClick={(e) => { e.stopPropagation(); onChange(values.filter((x) => x !== v)); }}>
                <Icon name="close" size={10} />
              </button>
            )}
          </span>
        );
      })}
      <input ref={input} id={id} className={styles["chipsField"]} value={text} disabled={disabled} placeholder={values.length ? undefined : placeholder}
        aria-label={label} role={suggestions ? "combobox" : undefined} aria-expanded={suggestions ? open && filtered.length > 0 : undefined}
        aria-controls={suggestions ? listId : undefined} aria-autocomplete={suggestions ? "list" : undefined}
        onFocus={() => setOpen(true)} onBlur={() => { setTimeout(() => setOpen(false), 120); if (text.trim()) add(text); }}
        onChange={(e) => setText(e.target.value)} onKeyDown={onKey} />
      {suggestions && open && filtered.length > 0 ? (
        <div id={listId} role="listbox" className={styles["suggest"]} aria-label={`${label}: suggestions`}>
          {filtered.map((g) => (
            <div key={g.group} role="group" aria-label={g.group}>
              <div className={styles["suggestGroup"]}>{g.group}</div>
              {g.values.map((v) => (
                <div key={v} role="option" aria-selected={false} className={styles["suggestOption"]}
                  onMouseDown={(e) => { e.preventDefault(); add(v); }}>
                  <span className="ds-mono">{v}</span>
                </div>
              ))}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

export interface RepoFilterValue {
  readonly mode: "all" | "only" | "except";
  readonly repos: readonly string[];
}

/** Whether a glob (`*` within a segment, any case) matches owner/name. */
export function repoGlob(pattern: string, name: string): boolean {
  const seg = (p: string, s: string) => new RegExp(`^${p.split("*").map((x) => x.replace(/[.+?^${}()|[\]\\-]/g, "\\$&")).join("[^/]*")}$`, "i").test(s);
  const [po, pn] = pattern.split("/");
  const [no, nn] = name.split("/");
  return po !== undefined && pn !== undefined && no !== undefined && nn !== undefined && seg(po, no) && seg(pn, nn);
}

/** How many of `all` a filter covers. */
export function repoFilterCount(value: RepoFilterValue, all: readonly string[]): number {
  if (value.mode === "all") return all.length;
  const named = (r: string) => value.repos.some((p) => repoGlob(p, r));
  return all.filter((r) => (value.mode === "only" ? named(r) : !named(r))).length;
}

/**
 * Which of the organisation's repositories an organisation automation
 * covers: All, Only these or All except these, the last two a chips input
 * of owner/name values and globs, and "Matches N of M repositories".
 */
export function RepoFilterField({ value, onChange, org, groups, disabled }: {
  readonly value: RepoFilterValue;
  readonly onChange: (next: RepoFilterValue) => void;
  readonly org: string;
  readonly groups: ReadonlyArray<{ readonly project: string; readonly repos: readonly string[] }>;
  readonly disabled?: boolean | undefined;
}) {
  const all = useMemo(() => [...new Set(groups.flatMap((g) => g.repos))], [groups]);
  const n = repoFilterCount(value, all);
  return (
    <div className={styles["repoFilter"]} data-testid="repo-filter">
      <Segmented<RepoFilterValue["mode"]> label="Repositories" size="sm" value={value.mode} disabled={disabled}
        onChange={(mode) => onChange({ mode, repos: mode === "all" ? [] : value.repos })}
        options={[{ value: "all", label: `All repositories in ${org}` }, { value: "only", label: "Only these" }, { value: "except", label: "All except these" }]} />
      {value.mode === "all" ? null : (
        <ChipsInput label={value.mode === "only" ? "Repositories it covers" : "Repositories it leaves out"} values={value.repos} disabled={disabled}
          placeholder={`${org.toLowerCase()}/name or ${org.toLowerCase()}/svc-*`} onChange={(repos) => onChange({ mode: value.mode, repos })}
          matches={(g) => all.filter((r) => repoGlob(g, r)).length}
          suggestions={groups.map((g) => ({ group: g.project, values: g.repos }))} />
      )}
      <span className={styles["muted"]} role="status" data-testid="repo-count">
        Matches {n} of {all.length} repositories{value.mode === "all" ? ", and any added later" : ""}
      </span>
    </div>
  );
}

export type ScheduleMode = "minutes" | "hourly" | "daily" | "weekly" | "cron";

/** The picker's reading of a cron line: which friendly mode shows it, and its values. */
export function scheduleOf(cron: string): { mode: ScheduleMode; every: number; minute: number; hour: number; days: number[] } {
  const f = cron.trim().split(/\s+/);
  const base = { every: 15, minute: 0, hour: 9, days: [1, 2, 3, 4, 5] };
  if (f.length !== 5) return { mode: "cron", ...base };
  const [m, h, dom, mon, dow] = f as [string, string, string, string, string];
  const step = /^\*\/(\d+)$/.exec(m);
  if (step && h === "*" && dom === "*" && mon === "*" && dow === "*") return { mode: "minutes", ...base, every: Number(step[1]) };
  if (/^\d+$/.test(m) && h === "*" && dom === "*" && mon === "*" && dow === "*") return { mode: "hourly", ...base, minute: Number(m) };
  if (/^\d+$/.test(m) && /^\d+$/.test(h) && dom === "*" && mon === "*") {
    if (dow === "*") return { mode: "daily", ...base, minute: Number(m), hour: Number(h) };
    if (/^[0-7](,[0-7])*$/.test(dow) || dow === "1-5") {
      const days = dow === "1-5" ? [1, 2, 3, 4, 5] : [...new Set(dow.split(",").map((d) => Number(d) % 7))].sort();
      return { mode: "weekly", ...base, minute: Number(m), hour: Number(h), days };
    }
  }
  return { mode: "cron", ...base };
}

/** The cron line a friendly mode's values make. */
export function cronOf(mode: Exclude<ScheduleMode, "cron">, v: { every: number; minute: number; hour: number; days: readonly number[] }): string {
  switch (mode) {
    case "minutes": return `*/${v.every} * * * *`;
    case "hourly": return `${v.minute} * * * *`;
    case "daily": return `${v.minute} ${v.hour} * * *`;
    case "weekly": return `${v.minute} ${v.hour} * * ${v.days.length ? [...v.days].sort().join(",") : "1"}`;
  }
}

/** Seven toggles, Monday first; at least one stays on. */
export function DayToggles({ days, onChange, disabled }: { readonly days: readonly number[]; readonly onChange: (next: number[]) => void; readonly disabled?: boolean | undefined }) {
  const order = [1, 2, 3, 4, 5, 6, 0];
  return (
    <span className={styles["days"]} role="group" aria-label="Days">
      {order.map((d) => {
        const on = days.includes(d);
        return (
          <button key={d} type="button" aria-pressed={on} disabled={disabled || (on && days.length === 1)} className={cx(styles["day"], on && styles["dayOn"])}
            onClick={() => onChange(on ? days.filter((x) => x !== d) : [...days, d])}>
            {DAY[d]}
          </button>
        );
      })}
    </span>
  );
}

/**
 * A schedule: Minutes, Hourly, Daily, Weekly or a cron line, in a time
 * zone, with its next three runs. `next` is the app's (the orchestrator's
 * parser decides; the picker only shows). The time is 24-hour text; the
 * line under it says the schedule as cron, and Advanced opens the cron line.
 */
export function SchedulePicker({ cron, tz, onChange, zones, next, problem, disabled, zoneSource }: {
  readonly cron: string;
  readonly tz: string;
  readonly onChange: (next: { cron: string; tz: string }) => void;
  readonly zones: readonly string[];
  readonly next?: ReactNode;
  readonly problem?: string | null | undefined;
  readonly disabled?: boolean | undefined;
  /** Whose time zone it starts from ("Example's"), said beside the zone. */
  readonly zoneSource?: string | undefined;
}) {
  const read = scheduleOf(cron);
  const [mode, setMode] = useState<ScheduleMode>(read.mode);
  const v = read;
  const set = (m: Exclude<ScheduleMode, "cron">, over: Partial<typeof v>) => onChange({ cron: cronOf(m, { ...v, ...over }), tz });
  const timeValue = `${String(v.hour).padStart(2, "0")}:${String(v.minute).padStart(2, "0")}`;
  const [timeText, setTimeText] = useState(timeValue);
  const [lastTime, setLastTime] = useState(timeValue);
  if (lastTime !== timeValue) {
    setLastTime(timeValue);
    setTimeText(timeValue);
  }
  const time = /^([01]?\d|2[0-3]):([0-5]\d)$/.exec(timeText.trim());
  return (
    <div className={styles["schedule"]} data-testid="schedule-picker">
      <Segmented<ScheduleMode> label="Repeats" size="sm" value={mode} disabled={disabled}
        onChange={(m) => { setMode(m); if (m !== "cron") set(m, {}); }}
        options={[{ value: "minutes", label: "Minutes" }, { value: "hourly", label: "Hourly" }, { value: "daily", label: "Daily" }, { value: "weekly", label: "Weekly" }, { value: "cron", label: "cron" }]} />
      <div className={styles["scheduleLine"]}>
        {mode === "minutes" ? (
          <span className={styles["inline"]}>Every
            <Select<string> aria-label="Every how many minutes" size="sm" value={String(v.every)} disabled={disabled}
              options={[5, 10, 15, 20, 30].map((n) => ({ value: String(n), label: String(n) }))}
              onValueChange={(n) => set("minutes", { every: Number(n) })} /> minutes</span>
        ) : mode === "hourly" ? (
          <span className={styles["inline"]}>At minute
            <NumberInput step={1} aria-label="At minute" min={0} max={59} value={v.minute} disabled={disabled}
              onValueChange={(n) => set("hourly", { minute: Math.min(59, Math.max(0, n ?? 0)) })} />
          </span>
        ) : mode === "cron" ? (
          <Input aria-label="cron" data-testid="schedule-cron" mono value={cron} disabled={disabled}
            aria-invalid={problem ? true : undefined} onChange={(e) => onChange({ cron: e.target.value, tz })} />
        ) : (
          <>
            {mode === "weekly" ? <DayToggles days={v.days} disabled={disabled} onChange={(days) => set("weekly", { days })} /> : null}
            <span className={styles["inline"]}>at
              <Input aria-label="Time" mono inputMode="numeric" placeholder="HH:MM" value={timeText} disabled={disabled}
                className={styles["time"]} aria-invalid={time ? undefined : true}
                onChange={(e) => {
                  setTimeText(e.target.value);
                  const m = /^([01]?\d|2[0-3]):([0-5]\d)$/.exec(e.target.value.trim());
                  if (m) set(mode, { hour: Number(m[1]), minute: Number(m[2]) });
                }} />
            </span>
          </>
        )}
        <span className={styles["inline"]}>in
          <Select<string> aria-label="Time zone" size="sm" value={tz} disabled={disabled}
            options={(zones.includes(tz) ? zones : [tz, ...zones]).map((z) => ({ value: z, label: z, meta: z === tz && zoneSource ? zoneSource : undefined }))}
            onValueChange={(z) => onChange({ cron, tz: z })} />
        </span>
      </div>
      {mode === "cron" ? null : (
        <span className={styles["muted"]} data-testid="schedule-as-cron">
          As cron <code className="ds-mono">{cron}</code> ·{" "}
          <button type="button" className={styles["advanced"]} disabled={disabled} onClick={() => setMode("cron")}>Advanced: write a cron expression</button>
        </span>
      )}
      {problem ? <span className={styles["problem"]} role="alert">{problem}</span> : null}
      {next}
    </div>
  );
}

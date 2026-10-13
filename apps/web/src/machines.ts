/**
 * What the machine screens compute from sizes and pools: labels, the
 * words for who uses a size, a pool's host as a person reads it, the fit
 * as a callout says it, and a dialog's draft as a size. Pure, so the rules
 * are tested apart from the screens.
 */

import {
  MACHINE_LIMITS,
  SETTINGS_ROLE_LABEL,
  gib,
  machineSizeInputSchema,
  sizePool,
  type Fit,
  type MachinePool,
  type MachinePools,
  type MachineSize,
  type MachineSizeInput,
  type MachineSizeUse,
  type MachineSizeWithUse,
  type SettingsRole,
} from "@dude/domain";

import type { Run, SettingsResponse } from "@dude/domain";
import type { MachineRaise } from "@dude/design-system/components";
import { formatDuration } from "@dude/design-system";

export type RaisedMachine = NonNullable<Run["machine"]> & {
  raised?: { sizeId: string; moveId: string; askedBy: string; role?: string; reason: string; at: string };
};

// The task detail's resolved entries are isolated here until the backend contract lands.
export function taskRaisedMachines(detail: unknown): Partial<Record<SettingsRole, RaisedMachine>> {
  return (detail as { agentMachines?: Partial<Record<SettingsRole, RaisedMachine>> }).agentMachines ?? {};
}

export function raisedProvenance(machine: RaisedMachine, now = Date.now()): string | null {
  const r = machine.raised;
  if (!r) return null;
  const at = Date.parse(r.at);
  const when = Number.isFinite(at) ? `${formatDuration(Math.max(0, now - at), { style: "age" })} ago` : r.at;
  return `Asked for by ${r.askedBy}${r.role ? ` (the ${roleLabel(r.role).toLowerCase()})` : ""}, ${when}: ${r.reason}`;
}

export function sessionRaisedMachine(detail: unknown): RaisedMachine | null {
  const d = detail as { agentMachine?: RaisedMachine | null };
  return d.agentMachine ?? null;
}

export interface MachineEventView {
  role: string;
  from: string;
  to: string;
  pool: string | null;
  reason: string;
  note: string;
  by: string;
  moveId: string | null;
}

export function machineEventView(p: Record<string, unknown>): MachineEventView {
  const name = (v: unknown): string => typeof v === "string" ? v : v && typeof v === "object" && typeof (v as { name?: unknown }).name === "string" ? (v as { name: string }).name : "its requested size";
  const pool = p.pool && typeof p.pool === "object" ? (p.pool as { to?: unknown }).to : p.pool;
  return { role: typeof p.role === "string" ? p.role : "brainstorm", from: name(p.from), to: name(p.to),
    pool: typeof pool === "string" ? pool : null, reason: typeof p.message === "string" ? p.message : typeof p.reason === "string" ? p.reason : typeof p.refused === "string" ? p.refused : "",
    note: typeof p.note === "string" ? p.note : "", by: typeof p.by === "string" ? p.by : "Someone",
    moveId: typeof p.moveId === "string" ? p.moveId : null };
}

export function raisedRows(machines: Partial<Record<SettingsRole, RaisedMachine>>, settings: SettingsResponse, sizes: readonly MachineSize[],
  reset: (role: SettingsRole, from: RaisedMachine, to: MachineSize) => void): MachineRaise[] {
  return Object.entries(machines).flatMap(([key, machine]) => {
    const role = key as SettingsRole;
    if (!machine.raised) return [];
    const field = settings.roles[role]?.machineSize;
    const to = sizes.find((s) => s.id === field?.value) ?? sizes.find((s) => s.isDefault);
    const follows = role === "fixer" && (machine.raised.role === "implementer" || (field?.followsImplementer && !machine.raised.role));
    const avatar = role === "fixer" ? "implementer" : role;
    return [{ role: avatar as MachineRaise["role"], label: roleLabel(role), size: machine, settings: { name: to?.name ?? "its settings' size" },
      detail: raisedProvenance(machine), follows: follows ? "implementer" : undefined,
      onReset: to && !follows ? () => reset(role, machine, to) : undefined }];
  });
}

/** A pool's host, as a person reads it: "16 CPUs · 32 GiB · 180 GiB", disk "not reserved" when 0. */
export function hostSpec(pool: MachinePool): string | null {
  const h = pool.hostSize;
  if (!h) return null;
  return `${h.cpus} CPUs · ${gib(h.memory)} GiB · ${h.disk > 0 ? `${gib(h.disk)} GiB` : "disk not reserved"}`;
}

/** What a pool's machines are: "EC2 · c7a.4xlarge", or "Static · platform". */
export function poolMachines(pool: MachinePool): string {
  let provider = "Static";
  if (pool.provider === "ec2") {
    provider = "EC2";
  } else if (pool.provider) {
    provider = pool.provider[0]!.toUpperCase() + pool.provider.slice(1);
  }
  const machines = pool.instanceType || (pool.platform ? "platform" : "hosts");
  return `${provider} · ${machines}`;
}

/** Where lux learned a pool's host size. */
export function poolKnownFrom(pool: MachinePool): string {
  if (!pool.hostSize) return "no host yet";
  const running = pool.hostsRunning ?? null;
  if (pool.hostSizeFrom === "history" || running === 0) return "none running — from its last hosts";
  if (running === null) return "hosts running";
  return `${running} ${running === 1 ? "host" : "hosts"} running`;
}

/** A pool as the size dialog's picker lists it: "big — EC2 · c7a.8xlarge · 32 CPUs · 64 GiB · 380 GiB". */
export function poolOptionLabel(pool: MachinePool, orgName: string): string {
  const name = pool.isDefault ? `${pool.name} (${orgName}’s default)` : pool.name;
  const host = hostSpec(pool);
  return `${name} — ${poolMachines(pool)}${host ? ` · ${host}` : " · host size unknown"}`;
}

/** lux's pools as the fit check takes them: null while unread, or when lux could not be read. */
export function knownPools(pools: MachinePools | null): MachinePool[] | null {
  return pools && pools.problem === null ? pools.pools : null;
}

/**
 * A size's pool by lux's current name: "Default pool" for none, else the
 * name in lux's list, else the one the API read, else its id.
 */
export function poolLabel(size: Pick<MachineSizeWithUse, "poolId" | "poolName">, known: readonly MachinePool[] | null): string {
  if (size.poolId === null) return "Default pool";
  const pool = known === null ? null : sizePool(size.poolId, known);
  return pool?.name ?? size.poolName ?? size.poolId;
}

export function roleLabel(role: string | null): string {
  return role ? SETTINGS_ROLE_LABEL[role as SettingsRole] ?? role : "";
}

/** Who uses a size, in words: "3 agents", "2 agents · 1 project", "Tester · and any with none set". */
export function usedByWords(size: Pick<MachineSize, "isDefault">, uses: readonly MachineSizeUse[]): string {
  const agents = new Set(uses.filter((u) => u.kind === "organization").map((u) => u.role)).size;
  const projects = new Set(uses.filter((u) => u.kind !== "organization").map((u) => u.project?.id)).size;
  const parts = [
    agents ? `${agents} ${agents === 1 ? "agent" : "agents"}` : null,
    projects ? `${projects} ${projects === 1 ? "project" : "projects"}` : null,
  ].filter(Boolean);
  if (size.isDefault) return parts.length ? `${parts.join(" · ")} · and any with none set` : "Any with none set";
  return parts.length ? parts.join(" · ") : "Nobody";
}

/** One use, as the remove dialog lists it: what, and where it is set. */
export function useLine(use: MachineSizeUse, orgName: string): { what: string; where: string } {
  if (use.kind === "preview") return { what: `${use.project!.name} · Branch previews`, where: "project setting" };
  if (use.kind === "project") return { what: `${use.project!.name} · ${roleLabel(use.role)}`, where: use.inherited ? "follows the implementer" : "project override" };
  return { what: roleLabel(use.role), where: use.inherited ? "follows the implementer" : `${orgName}’s setting` };
}

/** A size dialog's fields as typed: numbers in the units shown (memory in GiB), or null where not a number. */
export interface SizeDraft {
  name: string;
  cpus: number | null;
  memoryGiB: number | null;
  diskGiB: number | null;
  /** lux's id for the pool; null: the organisation's default pool. */
  poolId: string | null;
  isDefault: boolean;
}

export function draftOf(size: MachineSize | null): SizeDraft {
  return size
    ? { name: size.name, cpus: size.cpus, memoryGiB: size.memoryMiB / 1024, diskGiB: size.diskGiB, poolId: size.poolId, isDefault: size.isDefault }
    : { name: "", cpus: 2, memoryGiB: 8, diskGiB: 20, poolId: null, isDefault: false };
}

/** Each field's problem, in the step's words; none when the draft is a size. */
export function draftProblems(d: SizeDraft): Partial<Record<"name" | "cpus" | "memory" | "disk", string>> {
  const out: Partial<Record<"name" | "cpus" | "memory" | "disk", string>> = {};
  const parsed = machineSizeInputSchema.safeParse(asInput(d));
  if (parsed.success) return out;
  for (const issue of parsed.error.issues) {
    let key: "name" | "cpus" | "memory" | "disk";
    switch (issue.path[0]) {
      case "memoryMiB":
        key = "memory";
        break;
      case "diskGiB":
        key = "disk";
        break;
      default:
        key = issue.path[0] as "name" | "cpus";
    }
    out[key] ??= issue.message;
  }
  return out;
}

/** The draft as the API takes it; a field that is not a number goes as NaN and is refused with its step. */
export function asInput(d: SizeDraft): MachineSizeInput {
  return {
    name: d.name.trim(),
    cpus: d.cpus ?? NaN,
    memoryMiB: d.memoryGiB === null ? NaN : Math.round(d.memoryGiB * 1024 * 1e6) / 1e6,
    diskGiB: d.diskGiB ?? NaN,
    poolId: d.poolId,
    isDefault: d.isDefault,
  };
}

export const STEP_HINT = {
  cpus: `In steps of ${MACHINE_LIMITS.cpus.step}`,
  memory: `In steps of ${MACHINE_LIMITS.memoryMiB.step / 1024} GiB`,
  disk: `In steps of ${MACHINE_LIMITS.diskGiB.step} GiB`,
} as const;

/** How much of one host a size takes, as its row says it; "—" for a pool gone from lux. */
export function fitWords(fit: Fit): { share: number | null; text: string } {
  if (fit.kind === "fits") return { share: fit.share, text: `${Math.round(fit.share * 100)}% of a host` };
  if (fit.kind === "too_big") return { share: 1, text: "Too big for a host" };
  if (fit.kind === "gone") return { share: null, text: "—" };
  return { share: null, text: "Unknown" };
}

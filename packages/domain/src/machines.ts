import { z } from "zod";
import type { AgentModels } from "./hierarchy.ts";

/**
 * Machine sizes: what an agent's machine (or a branch preview's) can be.
 * They are the organization's, changed by its admins; every agent role
 * names one in its settings (org default, project override, field by
 * field like its model), and one that names none runs on the
 * organization's default size. lux runs the machine: dude sends the size
 * as the RunSpec's `resources`, and its pool as `placement.poolId`.
 *
 * A size keeps its pool by lux's id, which a rename in lux leaves alone;
 * the pool's name is always lux's current one, from its list.
 *
 * Sizes move in steps (half a CPU, half a GiB of memory, 5 GiB of disk),
 * checked here and by the database (migration 064), so an illegal size
 * cannot exist.
 */

export const MIB = 1024 * 1024;
export const GIB = 1024 * MIB;

/** The step, least and most of each dimension, in the units they are stored in. */
export const MACHINE_LIMITS = {
  cpus: { step: 0.5, min: 0.5, max: 256 },
  memoryMiB: { step: 512, min: 512, max: 2048 * 1024 },
  diskGiB: { step: 5, min: 5, max: 20_000 },
} as const;

export const MACHINE_NAME_MAX = 40;

/** What an off-step or out-of-range value is told, naming the step. */
export const MACHINE_STEP_MESSAGE = {
  cpus: "Whole or half CPUs: 0.5, 1, 1.5…",
  memoryMiB: "In steps of 0.5 GiB: 0.5, 1, 1.5…",
  diskGiB: "In steps of 5 GiB: 5, 10, 15…",
} as const;

/** What a value above a dimension's most is told. */
export const MACHINE_MAX_MESSAGE = {
  cpus: `At most ${MACHINE_LIMITS.cpus.max} CPUs`,
  memoryMiB: `At most ${MACHINE_LIMITS.memoryMiB.max / 1024} GiB`,
  diskGiB: `At most ${MACHINE_LIMITS.diskGiB.max} GiB`,
} as const;

const onStep = (value: number, step: number) => Number.isFinite(value) && Math.abs(value / step - Math.round(value / step)) < 1e-9;

function dimension(key: keyof typeof MACHINE_LIMITS) {
  const { step, min, max } = MACHINE_LIMITS[key];
  return z
    .number({ invalid_type_error: MACHINE_STEP_MESSAGE[key] })
    .min(min, MACHINE_STEP_MESSAGE[key])
    .max(max, MACHINE_MAX_MESSAGE[key])
    .refine((v) => onStep(v, step), MACHINE_STEP_MESSAGE[key]);
}

/** A lux pool's id, as lux makes them (`pool_…`); migration 064 holds the same pattern. */
export const poolIdSchema = z.string().regex(/^pool_[A-Za-z0-9_-]+$/, "a lux pool’s id");

/** A size as an admin writes it (`POST /v1/machines/sizes`, `PUT /v1/machines/sizes/:id`). */
export const machineSizeInputSchema = z
  .object({
    name: z.string().trim().min(1, "A size needs a name").max(MACHINE_NAME_MAX, `At most ${MACHINE_NAME_MAX} characters`),
    cpus: dimension("cpus"),
    memoryMiB: dimension("memoryMiB"),
    diskGiB: dimension("diskGiB"),
    /** null: the organization's default pool in lux. */
    poolId: poolIdSchema.nullable().default(null),
    isDefault: z.boolean().default(false),
  })
  .strict();
export type MachineSizeInput = z.infer<typeof machineSizeInputSchema>;

/** A size as the API shows it. */
export interface MachineSize extends MachineSizeInput {
  id: string;
  updatedAt: string;
  updatedBy: { id: string; name: string } | null;
}

/** Who names a size: an organization's or a project's role, a project's previews. */
export interface MachineSizeUse {
  /** "organization": the organization's role; "project": a project's override; "preview": a project's branch previews. */
  kind: "organization" | "project" | "preview";
  role: string | null;
  project: { id: string; name: string; imageUrl: string | null } | null;
  /** The fixer with no size of its own, taking the implementer's. */
  inherited?: boolean;
}

export interface MachineSizeWithUse extends MachineSize {
  /** lux's current name for its pool; null for the default pool, or when lux's list lacks it or could not be read. */
  poolName: string | null;
  usedBy: MachineSizeUse[];
}

/** `DELETE /v1/machines/sizes/:id`: where what named it goes. null follows the default. */
export const removeMachineSizeSchema = z.object({ replacement: z.string().min(1).nullable().default(null) }).strict();

// ---------------------------------------------------------------------------
// Pools, as lux reports them
// ---------------------------------------------------------------------------

/** One host of a pool's resources, in lux's terms: memory and disk in bytes (disk 0: not reserved). */
export interface HostSize {
  cpus: number;
  memory: number;
  disk: number;
}

/** A pool dude's lux key can use (`GET /v1/machines/pools`). */
export interface MachinePool {
  /** lux's id for it, unchanged by a rename: what a size stores. */
  id: string;
  name: string;
  /** Where lux puts a Run that names no pool. */
  isDefault: boolean;
  /** A platform pool, not the organization's own. */
  platform: boolean;
  /** ec2, static… */
  provider: string | null;
  /** The instance type its hosts are launched as; null for a static pool. */
  instanceType: string | null;
  /** One host's size; null when lux does not know it (an older lux, a pool that never had a host). */
  hostSize: HostSize | null;
  /** Whether hostSize is from hosts running now, or from its last hosts. */
  hostSizeFrom: "running" | "history" | null;
  /** Hosts running now, when lux says. */
  hostsRunning: number | null;
}

export interface MachinePools {
  pools: MachinePool[];
  /** When dude read them. */
  readAt: string;
  /** Why there are none: lux could not be reached, or refused. */
  problem: string | null;
}

// ---------------------------------------------------------------------------
// The fit check
// ---------------------------------------------------------------------------

export type FitDimension = "cpus" | "memory" | "disk";

export type Fit =
  /** share: the largest part of one host it takes, 0..1. */
  | { kind: "fits"; pool: MachinePool; share: number; diskReserved: boolean }
  | { kind: "too_big"; pool: MachinePool; over: Array<{ what: FitDimension; asked: number; offers: number }> }
  /** The size names a pool lux's list does not have: deleted in lux, or never the organisation's. */
  | { kind: "gone"; poolId: string }
  /** Allowed: lux will place it or say why. */
  | { kind: "unknown"; pool: MachinePool | null; reason: "no_pool" | "no_host_size" };

/**
 * The pool a size runs in: its own by id, or the one lux marks as the
 * default. null when lux's list does not have it.
 */
export function sizePool(poolId: string | null, pools: readonly MachinePool[]): MachinePool | null {
  return poolId === null ? (pools.find((p) => p.isDefault) ?? null) : (pools.find((p) => p.id === poolId) ?? null);
}

/**
 * Whether a size's pool is gone: it names one by id and lux's list, read,
 * does not have it. An unread list (null) says nothing is gone.
 */
export function poolGone(poolId: string | null, pools: readonly MachinePool[] | null): boolean {
  return poolId !== null && pools !== null && sizePool(poolId, pools) === null;
}

/**
 * Whether a size fits one host of its pool. Too big for a known host size
 * is refused; a host size nobody knows (an older lux, a pool that never
 * had a host) is allowed, with a note. `pools` null is lux's list unread
 * (lux unreachable): nothing is known, not even whether the pool exists.
 */
export function machineFit(
  size: Pick<MachineSizeInput, "cpus" | "memoryMiB" | "diskGiB" | "poolId">,
  pools: readonly MachinePool[] | null,
): Fit {
  if (poolGone(size.poolId, pools)) return { kind: "gone", poolId: size.poolId! };
  const pool = pools ? sizePool(size.poolId, pools) : null;
  if (!pool) return { kind: "unknown", pool: null, reason: "no_pool" };
  const host = pool.hostSize;
  if (!host) return { kind: "unknown", pool, reason: "no_host_size" };
  const memory = size.memoryMiB * MIB;
  const disk = size.diskGiB * GIB;
  const over: Array<{ what: FitDimension; asked: number; offers: number }> = [];
  if (size.cpus > host.cpus) over.push({ what: "cpus", asked: size.cpus, offers: host.cpus });
  if (memory > host.memory) over.push({ what: "memory", asked: memory, offers: host.memory });
  const diskReserved = host.disk > 0;
  if (diskReserved && disk > host.disk) over.push({ what: "disk", asked: disk, offers: host.disk });
  if (over.length) return { kind: "too_big", pool, over };
  const shares = [size.cpus / host.cpus, memory / host.memory, ...(diskReserved ? [disk / host.disk] : [])];
  return { kind: "fits", pool, share: Math.max(...shares), diskReserved };
}

/** A byte count as GiB, to one decimal place when it has one: "32", "22.5", "0.5". */
export function gib(bytes: number): string {
  const v = Math.round((bytes / GIB) * 10) / 10;
  return String(v);
}

/** "8 CPUs · 16 GiB · 80 GiB": a size's spec, as every picker and chip reads it. */
export function machineSpec(size: Pick<MachineSizeInput, "cpus" | "memoryMiB" | "diskGiB">): string {
  return `${size.cpus} ${size.cpus === 1 ? "CPU" : "CPUs"} · ${gib(size.memoryMiB * MIB)} GiB · ${size.diskGiB} GiB`;
}

/** Why a fit refuses, in a person's words: "72 GiB memory (it offers 64)". */
export function fitProblem(over: Extract<Fit, { kind: "too_big" }>["over"]): string {
  return over
    .map((o) =>
      o.what === "cpus" ? `${o.asked} CPUs (it offers ${o.offers})` : `${gib(o.asked)} GiB ${o.what} (it offers ${gib(o.offers)})`,
    )
    .join(", ");
}

// ---------------------------------------------------------------------------
// Removing a size: what named it is moved
// ---------------------------------------------------------------------------

/**
 * A layer's agent models with every role's `machineSize` naming `from`
 * moved to `to`, or removed (null: the role follows the default). A role
 * left with nothing is dropped, as a Reset leaves it. Returns the same
 * object when nothing named it.
 */
export function replaceMachineSize(models: AgentModels, from: string, to: string | null): AgentModels {
  let changed = false;
  const out: Record<string, Record<string, unknown>> = {};
  for (const [role, config] of Object.entries(models as Record<string, Record<string, unknown>>)) {
    if (config?.["machineSize"] !== from) {
      out[role] = config;
      continue;
    }
    changed = true;
    const { machineSize: _gone, ...rest } = config;
    const next = to === null ? rest : { ...rest, machineSize: to };
    if (Object.keys(next).length) out[role] = next;
  }
  return changed ? (out as AgentModels) : models;
}

/** A project's stored preview settings with `machineSize` moved from `from`, the same way. */
export function replacePreviewMachineSize<T extends Record<string, unknown>>(previews: T, from: string, to: string | null): T {
  if (previews["machineSize"] !== from) return previews;
  const { machineSize: _gone, ...rest } = previews;
  return (to === null ? rest : { ...rest, machineSize: to }) as unknown as T;
}

// ---------------------------------------------------------------------------
// Agent machine moves (docs/design/agent-machine-request.md)
// ---------------------------------------------------------------------------

/** A Run's machine as recorded (`runs.machine`) and as move events carry it. */
export interface Machine {
  sizeId: string;
  name: string;
  cpus: number;
  memoryMiB: number;
  diskGiB: number;
  /** lux's pool id; null: lux's default pool. */
  poolId: string | null;
  /** lux's pool name, when known. */
  pool: string | null;
  /** Where the size came from: "task" or "session" for a raise. */
  from: "task" | "session" | "project" | "organization" | "implementer" | "default" | "";
  raised?: Raise;
  note?: string;
  diskKept?: { requestedGiB: number; reason?: string };
  sinceEpoch?: number;
}

/** A size an agent raised for its task (per role) or session. */
export interface Raise {
  sizeId: string;
  moveId: string;
  /** The Run that asked. */
  askedBy: string;
  role?: string;
  reason: string;
  at: string;
}

/** A pool on a move event: lux's id (null: the default pool) and its name when known. */
export interface MovePool {
  id: string | null;
  name: string | null;
}

/** What every move event carries: whole machines, and pools when they differ. */
export interface MachineMove {
  /** Absent on a refused request: no move was made. */
  moveId?: string;
  role: string;
  from: Machine;
  to: Machine;
  pool?: { from: MovePool; to: MovePool };
}

/** `machine.requested`, on the Run: the agent's call, accepted or refused before any park. */
export interface MachineRequestedPayload extends MachineMove {
  reason: string;
  /** lux's code, or `not_larger`, for a refusal; absent when accepted. */
  refused?: string;
  /** The refusal's exact words, as the agent was told. */
  message?: string;
}

/** `machine.raised`, on the Run: the layer is written and the move starts. */
export type MachineRaisedPayload = MachineMove & { moveId: string };

/** `machine.refused`, on the Run: lux refused the real resume; it resumed where it was. */
export interface MachineRefusedPayload extends MachineMove {
  moveId: string;
  kind: "raise" | "reset";
  /** What lux reports the Run kept. */
  kept: Machine;
  code: string;
  message: string;
}

/** `machine.reset`, on the task or session: a person moved the role back to its settings' size. */
export interface MachineResetPayload extends MachineMove {
  /** "" when nothing was running: only the layer was cleared. */
  moveId: string;
  by: { id: string; name: string };
  note: string;
}

/** A raised role on a task's or session's detail (`agentMachines`). */
export interface AgentMachine {
  role: string;
  /** The raised size, names resolved; null when the size was removed since. */
  size: Machine | null;
  /** What the role runs on without the raise. */
  settings: Machine | null;
  raise: Raise;
  /** The Run that asked, and its role. */
  askedBy: { runId: string; role: string | null };
  /** A role that follows another's raise (the fixer the implementer's) has no Reset of its own. */
  followedBy: string[];
}

/** `DELETE /v1/tasks/:id/agent-machines/:role`, `DELETE /v1/sessions/:id/agent-machine`. */
export const resetMachineSchema = z
  .object({ note: z.string().refine((s) => [...s].length <= 2000, "at most 2000 characters").optional() })
  .strict();

/**
 * The size a role runs on: the project's, then the organization's — for
 * the fixer, then the implementer's over the same layers — else the
 * organization's default. A stored id that names no size is skipped. The
 * orchestrator's delivery.Sizes.ForRole is the same rule, for the Run.
 */
export function resolveMachineSize(
  role: string,
  layers: { project?: AgentModels | null | undefined; organization: AgentModels | null | undefined },
  sizes: ReadonlyArray<Pick<MachineSize, "id" | "isDefault">>,
): { sizeId: string | null; from: "project" | "organization" | "implementer" | "default" } {
  const chain = role === "fixer" ? ["fixer", "implementer"] : [role];
  const ordered = [["project", layers.project], ["organization", layers.organization]] as const;
  for (const r of chain) {
    for (const [name, layer] of ordered) {
      const id = (layer as Record<string, { machineSize?: string }> | null | undefined)?.[r]?.machineSize;
      if (id && sizes.some((s) => s.id === id)) return { sizeId: id, from: r === role ? name : "implementer" };
    }
  }
  return { sizeId: sizes.find((s) => s.isDefault)?.id ?? null, from: "default" };
}

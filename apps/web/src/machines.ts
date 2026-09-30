/**
 * What the machine screens compute from sizes and pools: labels, the
 * words for who uses a size, a pool's host as a person reads it, the fit
 * as a callout says it, and a dialog's draft as a size. Pure, so the rules
 * are tested apart from the screens.
 */

import {
  GIB,
  MACHINE_STEP_MESSAGE,
  MACHINE_LIMITS,
  SETTINGS_ROLE_LABEL,
  gib,
  machineFit,
  machineSizeInputSchema,
  machineSpec,
  type Fit,
  type MachinePool,
  type MachineSize,
  type MachineSizeInput,
  type MachineSizeUse,
  type SettingsRole,
} from "@dude/domain";

export { machineSpec };

/** A pool's host, as a person reads it: "16 CPUs · 32 GiB · 180 GiB", disk "not reserved" when 0. */
export function hostSpec(pool: MachinePool): string | null {
  const h = pool.hostSize;
  if (!h) return null;
  return `${h.cpus} CPUs · ${gib(h.memory)} GiB · ${h.disk > 0 ? `${gib(h.disk)} GiB` : "disk not reserved"}`;
}

/** What a pool's machines are: "EC2 · c7a.4xlarge", or "Static · platform". */
export function poolMachines(pool: MachinePool): string {
  const provider = pool.provider === "ec2" ? "EC2" : pool.provider ? pool.provider[0]!.toUpperCase() + pool.provider.slice(1) : "Static";
  return pool.instanceType ? `${provider} · ${pool.instanceType}` : `${provider} · ${pool.platform ? "platform" : "hosts"}`;
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

export const roleLabel = (role: string | null) => (role ? SETTINGS_ROLE_LABEL[role as SettingsRole] ?? role : "");

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
  pool: string | null;
  isDefault: boolean;
}

export function draftOf(size: MachineSize | null): SizeDraft {
  return size
    ? { name: size.name, cpus: size.cpus, memoryGiB: size.memoryMiB / 1024, diskGiB: size.diskGiB, pool: size.pool, isDefault: size.isDefault }
    : { name: "", cpus: 2, memoryGiB: 8, diskGiB: 20, pool: null, isDefault: false };
}

/** Each field's problem, in the step's words; none when the draft is a size. */
export function draftProblems(d: SizeDraft): Partial<Record<"name" | "cpus" | "memory" | "disk", string>> {
  const out: Partial<Record<"name" | "cpus" | "memory" | "disk", string>> = {};
  const parsed = machineSizeInputSchema.safeParse(asInput(d));
  if (parsed.success) return out;
  for (const issue of parsed.error.issues) {
    const key = issue.path[0] === "memoryMiB" ? "memory" : issue.path[0] === "diskGiB" ? "disk" : (issue.path[0] as "name" | "cpus");
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
    pool: d.pool,
    isDefault: d.isDefault,
  };
}

export const STEP_HINT = {
  cpus: `In steps of ${MACHINE_LIMITS.cpus.step}`,
  memory: `In steps of ${MACHINE_LIMITS.memoryMiB.step / 1024} GiB`,
  disk: `In steps of ${MACHINE_LIMITS.diskGiB.step} GiB`,
} as const;
export { MACHINE_STEP_MESSAGE };

/** How much of one host a size takes, as its row says it. */
export function fitWords(fit: Fit): { share: number | null; text: string } {
  if (fit.kind === "fits") return { share: fit.share, text: `${Math.round(fit.share * 100)}% of a host` };
  if (fit.kind === "too_big") return { share: 1, text: "Too big for a host" };
  return { share: null, text: "Unknown" };
}

export { machineFit, GIB };

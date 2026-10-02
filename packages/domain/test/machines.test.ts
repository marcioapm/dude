import { describe, expect, test } from "bun:test";
import {
  GIB,
  machineFit,
  machineSizeInputSchema,
  machineSpec,
  MACHINE_STEP_MESSAGE,
  MIB,
  poolGone,
  replaceMachineSize,
  replacePreviewMachineSize,
  resolveMachineSize,
  sizePool,
  type MachinePool,
} from "../src/machines.ts";

const size = (over: Record<string, unknown> = {}) => machineSizeInputSchema.safeParse({ name: "Large", cpus: 8, memoryMiB: 16384, diskGiB: 80, ...over });
const errorOf = (r: ReturnType<typeof size>) => (r.success ? null : r.error.issues[0]!.message);

describe("a size's steps and bounds", () => {
  test("half steps are sizes", () => {
    const r = size({ cpus: 6.5, memoryMiB: 22.5 * 1024, diskGiB: 120 });
    expect(r.success).toBe(true);
    expect(r.success && r.data.poolId).toBeNull();
    expect(r.success && r.data.isDefault).toBe(false);
  });

  test("an off-step value is refused, naming the step", () => {
    expect(errorOf(size({ cpus: 2.3 }))).toBe(MACHINE_STEP_MESSAGE.cpus);
    expect(errorOf(size({ memoryMiB: 1000 }))).toBe(MACHINE_STEP_MESSAGE.memoryMiB);
    expect(errorOf(size({ diskGiB: 12 }))).toBe(MACHINE_STEP_MESSAGE.diskGiB);
  });

  test("the least of each is one step", () => {
    expect(size({ cpus: 0.5, memoryMiB: 512, diskGiB: 5 }).success).toBe(true);
    expect(errorOf(size({ cpus: 0 }))).toBe(MACHINE_STEP_MESSAGE.cpus);
    expect(errorOf(size({ memoryMiB: 0 }))).toBe(MACHINE_STEP_MESSAGE.memoryMiB);
    expect(errorOf(size({ diskGiB: 0 }))).toBe(MACHINE_STEP_MESSAGE.diskGiB);
    expect(size({ cpus: 512 }).success).toBe(false);
  });

  test("past the most of each is refused, saying the most", () => {
    expect(errorOf(size({ cpus: 256.5 }))).toBe("At most 256 CPUs");
    expect(errorOf(size({ memoryMiB: 2048 * 1024 + 512 }))).toBe("At most 2048 GiB");
    expect(errorOf(size({ diskGiB: 20_005 }))).toBe("At most 20000 GiB");
  });

  test("a name is 1 to 40 characters, a pool a lux pool's id", () => {
    expect(size({ name: " " }).success).toBe(false);
    expect(size({ name: "x".repeat(41) }).success).toBe(false);
    expect(size({ poolId: "pool_b8r2n5w1c7z3" }).success).toBe(true);
    // A pool's name is not its id.
    expect(size({ poolId: "big" }).success).toBe(false);
    expect(size({ poolId: "pool_" }).success).toBe(false);
    expect(size({ pool: "big" }).success).toBe(false);
  });

  test("its spec reads as the pickers show it", () => {
    expect(machineSpec({ cpus: 1.5, memoryMiB: 3584, diskGiB: 20 })).toBe("1.5 CPUs · 3.5 GiB · 20 GiB");
    expect(machineSpec({ cpus: 1, memoryMiB: 512, diskGiB: 5 })).toBe("1 CPU · 0.5 GiB · 5 GiB");
  });
});

const pool = (name: string, host: MachinePool["hostSize"], isDefault = false): MachinePool => ({
  id: `pool_${name}_id`, name, isDefault, platform: false, provider: "ec2", instanceType: null, hostSize: host,
  hostSizeFrom: host ? "running" : null, hostsRunning: null,
});
const POOLS = [
  pool("default", { cpus: 16, memory: 32 * GIB, disk: 180 * GIB }, true),
  pool("big", { cpus: 32, memory: 64 * GIB, disk: 380 * GIB }),
  pool("shared", { cpus: 8, memory: 16 * GIB, disk: 0 }),
  pool("new", null),
];

describe("the fit check", () => {
  test("a size that fits one host says how much of it it takes", () => {
    const fit = machineFit({ cpus: 8, memoryMiB: 16 * 1024, diskGiB: 80, poolId: null }, POOLS);
    expect(fit.kind).toBe("fits");
    expect(fit.kind === "fits" && fit.pool.name).toBe("default");
    expect(fit.kind === "fits" && fit.share).toBe(0.5);
  });

  test("too big for a known host is refused, naming what does not fit", () => {
    const fit = machineFit({ cpus: 16, memoryMiB: 72 * 1024, diskGiB: 200, poolId: "pool_big_id" }, POOLS);
    expect(fit.kind).toBe("too_big");
    expect(fit.kind === "too_big" && fit.over).toEqual([{ what: "memory", asked: 72 * GIB, offers: 64 * GIB }]);
  });

  test("the pool is found by its id, whatever lux calls it now", () => {
    const renamed = POOLS.map((p) => (p.id === "pool_big_id" ? { ...p, name: "huge" } : p));
    const fit = machineFit({ cpus: 16, memoryMiB: 32 * 1024, diskGiB: 190, poolId: "pool_big_id" }, renamed);
    expect(fit).toMatchObject({ kind: "fits", pool: { id: "pool_big_id", name: "huge" }, share: 0.5 });
    expect(sizePool("pool_big_id", renamed)?.name).toBe("huge");
    // A pool's name is never taken for its id.
    expect(sizePool("big", POOLS)).toBeNull();
  });

  test("a pool lux's list does not have is gone; lux unread knows nothing", () => {
    expect(machineFit({ cpus: 2, memoryMiB: 8192, diskGiB: 20, poolId: "pool_deleted" }, POOLS)).toEqual({ kind: "gone", poolId: "pool_deleted" });
    // An empty list is lux saying it has none: gone too.
    expect(machineFit({ cpus: 2, memoryMiB: 8192, diskGiB: 20, poolId: "pool_big_id" }, [])).toEqual({ kind: "gone", poolId: "pool_big_id" });
    expect(machineFit({ cpus: 2, memoryMiB: 8192, diskGiB: 20, poolId: "pool_big_id" }, null)).toEqual({ kind: "unknown", pool: null, reason: "no_pool" });
    expect(poolGone("pool_deleted", POOLS)).toBe(true);
    expect(poolGone("pool_big_id", [])).toBe(true);
    expect(poolGone("pool_big_id", POOLS)).toBe(false);
    expect(poolGone("pool_big_id", null)).toBe(false);
    expect(poolGone(null, [])).toBe(false);
  });

  test("disk counts only where the host reserves it", () => {
    const fit = machineFit({ cpus: 8, memoryMiB: 14 * 1024, diskGiB: 500, poolId: "pool_shared_id" }, POOLS);
    expect(fit).toMatchObject({ kind: "fits", diskReserved: false, share: 1 });
  });

  test("a host size nobody knows is allowed, with the reason", () => {
    expect(machineFit({ cpus: 64, memoryMiB: 512, diskGiB: 5, poolId: "pool_new_id" }, POOLS)).toMatchObject({ kind: "unknown", reason: "no_host_size" });
    // lux unreachable, or no pools at all: the default pool unknown too.
    expect(machineFit({ cpus: 2, memoryMiB: 8192, diskGiB: 20, poolId: null }, [])).toMatchObject({ kind: "unknown", pool: null });
    expect(machineFit({ cpus: 2, memoryMiB: 8192, diskGiB: 20, poolId: null }, null)).toMatchObject({ kind: "unknown", pool: null });
    // An older lux lists pools but marks none the default: the default pool is unknown.
    expect(machineFit({ cpus: 64, memoryMiB: 512, diskGiB: 5, poolId: null }, POOLS.map((p) => ({ ...p, isDefault: false }))))
      .toEqual({ kind: "unknown", pool: null, reason: "no_pool" });
  });

  test("memory is compared in bytes", () => {
    expect(machineFit({ cpus: 1, memoryMiB: 32 * 1024, diskGiB: 5, poolId: null }, POOLS).kind).toBe("fits");
    expect(machineFit({ cpus: 1, memoryMiB: 32 * 1024 + 512, diskGiB: 5, poolId: null }, POOLS).kind).toBe("too_big");
    expect(MIB * 1024).toBe(GIB);
  });
});

describe("resolving a role's size", () => {
  const sizes = [{ id: "std", isDefault: true }, { id: "lg", isDefault: false }, { id: "xl", isDefault: false }];

  test("the project's, then the organization's, then the default", () => {
    expect(resolveMachineSize("reviewer", { project: { reviewer: { machineSize: "xl" } }, organization: { reviewer: { machineSize: "lg" } } }, sizes))
      .toEqual({ sizeId: "xl", from: "project" });
    expect(resolveMachineSize("reviewer", { project: {}, organization: { reviewer: { machineSize: "lg" } } }, sizes))
      .toEqual({ sizeId: "lg", from: "organization" });
    expect(resolveMachineSize("reviewer", { project: {}, organization: {} }, sizes)).toEqual({ sizeId: "std", from: "default" });
  });

  test("the fixer follows the implementer where it has none of its own", () => {
    expect(resolveMachineSize("fixer", { project: { implementer: { machineSize: "xl" } }, organization: {} }, sizes))
      .toEqual({ sizeId: "xl", from: "implementer" });
    expect(resolveMachineSize("fixer", { project: { implementer: { machineSize: "xl" } }, organization: { fixer: { machineSize: "lg" } } }, sizes))
      .toEqual({ sizeId: "lg", from: "organization" });
  });

  test("an id that names no size is passed over, to the next layer or the implementer's", () => {
    expect(resolveMachineSize("reviewer", { organization: { reviewer: { machineSize: "gone" } } }, sizes)).toEqual({ sizeId: "std", from: "default" });
    expect(resolveMachineSize("reviewer", { project: { reviewer: { machineSize: "gone" } }, organization: { reviewer: { machineSize: "lg" } } }, sizes))
      .toEqual({ sizeId: "lg", from: "organization" });
    expect(resolveMachineSize("fixer", { project: { fixer: { machineSize: "gone" } }, organization: { implementer: { machineSize: "xl" } } }, sizes))
      .toEqual({ sizeId: "xl", from: "implementer" });
  });
});

describe("removing a size moves what named it", () => {
  test("to another size", () => {
    const models = { implementer: { tier: "m", machineSize: "lg" }, reviewer: { machineSize: "xl" } };
    expect(replaceMachineSize(models, "lg", "xl")).toEqual({ implementer: { tier: "m", machineSize: "xl" }, reviewer: { machineSize: "xl" } });
  });

  test("to none: the key goes, and a role left with nothing goes", () => {
    const models = { implementer: { tier: "m", machineSize: "lg" }, fixer: { machineSize: "lg" }, reviewer: { machineSize: "xl" } };
    expect(replaceMachineSize(models, "lg", null)).toEqual({ implementer: { tier: "m" }, reviewer: { machineSize: "xl" } });
  });

  test("nothing named it: the same object", () => {
    const models = { reviewer: { machineSize: "xl" } };
    expect(replaceMachineSize(models, "lg", null)).toBe(models);
  });

  test("a project's previews", () => {
    const previews: Record<string, unknown> = { egress: [], machineSize: "lg" };
    expect(replacePreviewMachineSize(previews, "lg", "xl")).toEqual({ egress: [], machineSize: "xl" });
    expect(replacePreviewMachineSize(previews, "lg", null)).toEqual({ egress: [] });
    const other = { machineSize: "xl" };
    expect(replacePreviewMachineSize(other, "lg", null)).toBe(other);
  });
});

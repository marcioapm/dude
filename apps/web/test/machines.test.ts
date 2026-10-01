import { describe, expect, test } from "bun:test";
import { GIB, machineFit, type MachinePool, type MachineSizeUse } from "@dude/domain";
import { asInput, draftOf, draftProblems, fitWords, hostSpec, poolKnownFrom, poolMachines, poolOptionLabel, useLine, usedByWords } from "../src/machines.ts";

const pool = (over: Partial<MachinePool> = {}): MachinePool => ({
  name: "default", isDefault: true, platform: false, provider: "ec2", instanceType: "c7a.4xlarge",
  hostSize: { cpus: 16, memory: 32 * GIB, disk: 180 * GIB }, hostSizeFrom: "running", hostsRunning: 3, ...over,
});
const project = { id: "p", name: "Checkout", imageUrl: null };

describe("a size dialog's draft", () => {
  test("memory is typed in GiB and sent in MiB", () => {
    const draft = { ...draftOf(null), name: " Large ", cpus: 6.5, memoryGiB: 22.5, diskGiB: 120 };
    expect(asInput(draft)).toEqual({ name: "Large", cpus: 6.5, memoryMiB: 23040, diskGiB: 120, pool: null, isDefault: false });
    expect(draftProblems(draft)).toEqual({});
  });

  test("each field off its step says the step; one that is not a number too", () => {
    const d = { ...draftOf(null), name: "X", cpus: 2.3, memoryGiB: 0.7, diskGiB: null };
    expect(draftProblems(d)).toEqual({
      cpus: "Whole or half CPUs: 0.5, 1, 1.5…",
      memory: "In steps of 0.5 GiB: 0.5, 1, 1.5…",
      disk: "In steps of 5 GiB: 5, 10, 15…",
    });
  });

  test("editing starts from the size, in the dialog's units", () => {
    expect(draftOf({ id: "s", name: "Small", cpus: 1.5, memoryMiB: 3584, diskGiB: 20, pool: "big", isDefault: false, updatedAt: "", updatedBy: null }))
      .toEqual({ name: "Small", cpus: 1.5, memoryGiB: 3.5, diskGiB: 20, pool: "big", isDefault: false });
  });
});

describe("pools as the page reads them", () => {
  test("a host, its machines and where lux knows it from", () => {
    expect(hostSpec(pool())).toBe("16 CPUs · 32 GiB · 180 GiB");
    expect(hostSpec(pool({ hostSize: { cpus: 8, memory: 16 * GIB, disk: 0 } }))).toBe("8 CPUs · 16 GiB · disk not reserved");
    expect(hostSpec(pool({ hostSize: null }))).toBeNull();
    expect(poolMachines(pool())).toBe("EC2 · c7a.4xlarge");
    expect(poolMachines(pool({ provider: "static", instanceType: null, platform: true }))).toBe("Static · platform");
    expect(poolKnownFrom(pool())).toBe("3 hosts running");
    expect(poolKnownFrom(pool({ hostSizeFrom: "history", hostsRunning: 0 }))).toBe("none running — from its last hosts");
    expect(poolOptionLabel(pool(), "Acme")).toBe("default (Acme’s default) — EC2 · c7a.4xlarge · 16 CPUs · 32 GiB · 180 GiB");
  });

  test("a row's fit", () => {
    expect(fitWords(machineFit({ cpus: 8, memoryMiB: 16384, diskGiB: 80, pool: null }, [pool()]))).toEqual({ share: 0.5, text: "50% of a host" });
    expect(fitWords(machineFit({ cpus: 8, memoryMiB: 16384, diskGiB: 80, pool: null }, []))).toEqual({ share: null, text: "Unknown" });
    expect(fitWords(machineFit({ cpus: 32, memoryMiB: 16384, diskGiB: 80, pool: null }, [pool()]))).toEqual({ share: 1, text: "Too big for a host" });
  });

  test("a pool lux knows less about", () => {
    expect(poolKnownFrom(pool({ hostSize: null }))).toBe("no host yet");
    expect(poolKnownFrom(pool({ hostsRunning: null }))).toBe("hosts running");
    expect(poolKnownFrom(pool({ hostsRunning: 1 }))).toBe("1 host running");
    expect(poolMachines(pool({ provider: "static", instanceType: null, platform: false }))).toBe("Static · hosts");
    expect(poolMachines(pool({ provider: null, instanceType: null, platform: false }))).toBe("Static · hosts");
    expect(poolOptionLabel(pool({ name: "new", isDefault: false, hostSize: null }), "Acme")).toBe("new — EC2 · c7a.4xlarge · host size unknown");
  });
});

describe("who uses a size", () => {
  const uses: MachineSizeUse[] = [
    { kind: "organization", role: "implementer", project: null },
    { kind: "organization", role: "fixer", project: null, inherited: true },
    { kind: "project", role: "reviewer", project },
    { kind: "preview", role: null, project },
  ];

  test("in words: agents, then projects; the default adds everyone with none", () => {
    expect(usedByWords({ isDefault: false }, uses)).toBe("2 agents · 1 project");
    expect(usedByWords({ isDefault: true }, uses)).toBe("2 agents · 1 project · and any with none set");
    expect(usedByWords({ isDefault: true }, [])).toBe("Any with none set");
    expect(usedByWords({ isDefault: false }, [])).toBe("Nobody");
  });

  test("line by line, saying where each is set", () => {
    expect(uses.map((u) => useLine(u, "Acme"))).toEqual([
      { what: "Implementer", where: "Acme’s setting" },
      { what: "Fixer", where: "follows the implementer" },
      { what: "Checkout · Reviewer", where: "project override" },
      { what: "Checkout · Branch previews", where: "project setting" },
    ]);
  });
});

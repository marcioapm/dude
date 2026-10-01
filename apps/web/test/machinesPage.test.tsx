/**
 * The Machines page's size dialog and pool picker against a lux list read
 * this instant: a size whose pool lux deleted, and one that names the
 * default pool by its id. Mounted in happy-dom against the fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { GIB, type MachinePool, type MachineSizeWithUse } from "@dude/domain";
import { act, click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { MachinesPage, type Sizes } from "../src/screens/MachinesSettings.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

const DEFAULT_ID = "pool_d3f4u1t9k2m7";
const BIG_ID = "pool_b8r2n5w1c7z3";
const pool = (id: string, name: string, isDefault: boolean): MachinePool => ({
  id, name, isDefault, platform: false, provider: "ec2", instanceType: null,
  hostSize: { cpus: 16, memory: 32 * GIB, disk: 180 * GIB }, hostSizeFrom: "running", hostsRunning: 1,
});
const POOLS = [pool(DEFAULT_ID, "general", true), pool(BIG_ID, "big", false)];
const size = (id: string, name: string, poolId: string | null, poolName: string | null): MachineSizeWithUse => ({
  id, name, cpus: 2, memoryMiB: 8192, diskGiB: 20, poolId, poolName, isDefault: false,
  updatedAt: new Date().toISOString(), updatedBy: null, usedBy: [],
});
const SIZES: Sizes = {
  canEdit: true,
  sizes: [
    { ...size("msz_standard", "Standard", null, null), isDefault: true },
    size("msz_orphan", "Orphan", "pool_g0n3d3l3t3d0", null),
    size("msz_pinned", "Pinned", DEFAULT_ID, "general"),
  ],
};

class PoolsClient extends FixtureClient {
  override machinePools(): ReturnType<FixtureClient["machinePools"]> {
    return Promise.resolve({ pools: POOLS, readAt: new Date().toISOString(), problem: null });
  }
}

const press = (el: Element) =>
  act(async () => void el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));

/** Mounts the page and opens the Edit dialog of the size `name`. */
async function edit(name: string): Promise<HTMLElement> {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <MachinesPage client={new PoolsClient("a")} orgName="Acme" sizes={SIZES} problem={null} setSizes={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  // The pools are read once the page mounts; the table shows them by name when they are.
  await until(() => container.querySelector("[data-pool=general]"), "lux's pools");
  const menu = container.querySelector<HTMLButtonElement>(`[aria-label="Actions for ${name}"]`)!;
  await press(menu);
  const item = await until(() => [...document.querySelectorAll<HTMLElement>("[role=menuitem]")].find((i) => i.textContent?.includes("Edit")), "the Edit item");
  await click(item);
  return until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the size dialog");
}

const save = (dialog: HTMLElement) => dialog.querySelector<HTMLButtonElement>("[data-testid=machine-size-save]")!;
const picker = (dialog: HTMLElement) => dialog.querySelector<HTMLButtonElement>("[data-testid=machine-size-pool]")!;

test("a size whose pool lux deleted says so, and cannot be saved until it has another", async () => {
  const dialog = await edit("Orphan");
  expect(dialog.querySelector("[data-testid=machine-pool-gone]")?.textContent).toBe(
    "Its pool is gone from lux. Sessions on Orphan fail until it runs in another: choose one, or Acme’s default pool.",
  );
  expect(save(dialog).disabled).toBe(true);
  expect(picker(dialog).textContent).toBe("Pool gone from lux");

  await press(picker(dialog));
  const gone = await until(() => document.querySelector<HTMLElement>('[role=option][data-value="pool_g0n3d3l3t3d0"]'), "the gone option");
  expect(gone.getAttribute("aria-disabled")).toBe("true");
  expect(gone.hasAttribute("data-disabled")).toBe(true);
  // The pools lux lists can still be chosen.
  expect(document.querySelector(`[role=option][data-value="${BIG_ID}"]`)?.hasAttribute("data-disabled")).toBe(false);
});

test("a size naming the default pool by its id shows that pool, and saves", async () => {
  const dialog = await edit("Pinned");
  expect(picker(dialog).textContent).toBe("general (Acme’s default) — EC2 · hosts · 16 CPUs · 32 GiB · 180 GiB");
  expect(dialog.querySelector("[data-testid=machine-pool-gone]")).toBeNull();
  expect(save(dialog).disabled).toBe(false);
});

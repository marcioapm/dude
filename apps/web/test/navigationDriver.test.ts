/**
 * The navigation read's driver fields as the board and sidebar take them:
 * who decides and whether a conductor is live become one `driver`, and a
 * live conductor alone never makes the conductor the driver.
 */

import { afterEach, expect, test } from "bun:test";
import { ApiClient } from "../src/api/client.ts";

const originalFetch = globalThis.fetch;
afterEach(() => { globalThis.fetch = originalFetch; });

test("decider and a live conductor become the task's driver; the raw fields are not passed on", async () => {
  const task = (id: string, decider: string, conductorLive: boolean) => ({ id, title: id, status: "running", decider, conductorLive });
  globalThis.fetch = (async () => Response.json({ projects: [{ id: "p", name: "Web", tasks: [
    task("a", "policy", false), task("b", "policy", true), task("c", "conductor", false), task("d", "conductor", true),
    { id: "e", title: "e", status: "running" },
  ] }] })) as unknown as typeof fetch;
  const { projects } = await new ApiClient({ apiKey: "k" }).navigation();
  const tasks = projects[0]!.tasks!;
  expect(tasks.map((t) => [t.id, t.driver])).toEqual([["a", "deliver"], ["b", "deliver-chat"], ["c", "conductor"], ["d", "conductor"], ["e", undefined]]);
  expect(tasks.some((t) => "decider" in t || "conductorLive" in t)).toBe(false);
});

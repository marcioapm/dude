/**
 * The session screen: what its bar offers and what it says for a branch
 * preview. Mounted in happy-dom against the fixture client, so it reads
 * the Run and its events as it would from the API.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { RUN_ID } from "../src/fixtures/data.ts";
import { RunScreen } from "../src/screens/RunScreen.tsx";
import type { RunDetail } from "../src/api/client.ts";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

/** The fixture world with the Run patched: its status, its kind. */
class RunClient extends FixtureClient {
  constructor(private readonly patch: Partial<RunDetail>, scenario: "a" | "e" = "a") {
    super(scenario);
  }
  override async getRun(id: string): Promise<RunDetail> {
    return { ...(await super.getRun(id)), ...this.patch };
  }
}

async function session(props: Partial<Parameters<typeof RunScreen>[0]> & { client: FixtureClient }) {
  const { container, unmount } = await mount(<RunScreen runId={RUN_ID} onBack={() => {}} {...props} />);
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=run-screen]"), "the session");
  return container;
}

describe("the session's bar", () => {
  test("is Conversation, Changes and Events: no Servers toggle, no drawer", async () => {
    // A browser that had the drawer open keeps no way back to it.
    localStorage.setItem("dude.run.servers", "1");
    const page = await session({ client: new RunClient({}) });
    const bar = page.querySelector("[data-testid=session-view]")!;
    const options = [...bar.querySelectorAll("button, [role=radio]")].map((b) => b.textContent?.replace(/\d+/g, "").trim());
    expect(options).toEqual(["Conversation", "Changes", "Events"]);
    expect(page.textContent).not.toContain("Servers");
    expect(page.querySelector("[aria-label=Servers]")).toBeNull();
  });
});

describe("a branch preview's session", () => {
  test("on its task's page, points at the task's Servers tab", async () => {
    let opened = 0;
    const page = await session({ client: new RunClient({ kind: "preview", phase: null, role: null }, "e"), onOpenServers: () => void opened++ });
    const note = await until(() => page.querySelector("[data-testid=preview-run-note]"), "the preview note");
    expect(note.textContent).toContain("Servers tab");
    expect(page.querySelector("textarea")).toBeNull();
    await click(note.querySelector("[data-testid=preview-run-servers]")!);
    expect(opened).toBe(1);
  });

  test("on its own, opens its task on the Servers tab once the task is known", async () => {
    const opened: Array<[string, string | undefined]> = [];
    const page = await session({ client: new RunClient({ kind: "preview", phase: null, role: null }, "e"), onOpenTask: (id, tab) => void opened.push([id, tab]) });
    const button = await until(() => page.querySelector("[data-testid=preview-run-servers]"), "the way to the servers");
    await click(button);
    expect(opened).toEqual([["tsk_01j9x4kqf8b2m7e3", "servers"]]);
  });

  test("with no task to reach, says where its servers are in words", async () => {
    class NoTask extends RunClient {
      override getTask(): Promise<never> {
        return Promise.reject(new Error("unreachable"));
      }
    }
    const page = await session({ client: new NoTask({ kind: "preview", phase: null, role: null }, "e"), onOpenTask: () => {} });
    const note = await until(() => page.querySelector("[data-testid=preview-run-note]"), "the preview note");
    expect(note.textContent).toContain("on its task’s page, under the Servers tab");
    expect(note.querySelector("button")).toBeNull();
  });
});

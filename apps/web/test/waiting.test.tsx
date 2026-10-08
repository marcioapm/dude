/**
 * Why a Run waits for a host: useRunServers with WaitingForHost, as the
 * Run page renders them, and the Run page itself. lux's answer, read with the
 * Run's servers, is what shows or hides the wait, whatever dude's status
 * says: a Run resumed or moved is running to dude while lux finds a host.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { mount, settle, until } from "./dom.ts";
import { FixtureClient, emit } from "../src/fixtures/client.ts";
import { ORG, PROJECT, RUN_ID, TASK_ID } from "../src/fixtures/data.ts";
import { RunScreen } from "../src/screens/RunScreen.tsx";
import { WaitingForHost } from "../src/waiting.tsx";
import { useRunServers } from "../src/runServers.ts";
import type { RunDetail } from "../src/api/client.ts";
import type { RunStatus, TaskServers } from "@dude/domain";

const NESTED = "waiting for capacity: 1 host in its pool does not support nested containers";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

/** A client whose Run lux reports waiting with `reason` (null: placed), per Run id; each read counted. */
class WaitClient extends FixtureClient {
  reasons = new Map<string, string | null>();
  reads: string[] = [];
  constructor(private readonly patch: Partial<RunDetail> = {}) {
    super("a");
  }
  override async getRun(id: string): Promise<RunDetail> {
    return { ...(await super.getRun(id)), ...this.patch };
  }
  override async runServers(runId = RUN_ID): Promise<TaskServers> {
    this.reads.push(runId);
    const s = await super.runServers();
    const reason = this.reasons.get(runId) ?? null;
    return { ...s, run: s.run ? { ...s.run, id: runId, waitingReason: reason } : s.run };
  }
}

type Props = { runId: string; status: RunStatus; waitAsk: number };

/** The hook and the callout as the Run page puts them together. */
function Harness({ client, runId, status, waitAsk }: { client: WaitClient } & Props) {
  const { waitingReason } = useRunServers(client, runId, status, 0, waitAsk);
  return waitingReason ? <WaitingForHost reason={waitingReason} onRunPage /> : <p data-testid="placed">placed</p>;
}

let drive: ((p: Props) => void) | null = null;
function Driven({ client, initial }: { client: WaitClient; initial: Props }) {
  const [p, setP] = useState(initial);
  drive = setP;
  return <Harness client={client} {...p} />;
}

async function harness(client: WaitClient, initial: Props) {
  const { container, unmount } = await mount(<Driven client={client} initial={initial} />);
  mounted.push(unmount);
  return container;
}

const callout = (page: HTMLElement) => page.querySelector("[data-testid=waiting-for-host]");

describe("useRunServers: why a Run waits", () => {
  test("the same Run, once placed, asked again: the wait is gone", async () => {
    const client = new WaitClient();
    client.reasons.set("run_1", NESTED);
    const page = await harness(client, { runId: "run_1", status: "scheduled", waitAsk: 0 });
    const shown = await until(() => callout(page), "the wait");
    expect(shown.textContent).toBe(
      "Waiting for a host that can run containers. lux has no host that can run containers. An admin can add one to a pool. Nothing is spent meanwhile.");
    client.reasons.set("run_1", null);
    await act(async () => drive!({ runId: "run_1", status: "scheduled", waitAsk: 1 }));
    await until(() => page.querySelector("[data-testid=placed]"), "the wait gone");
    expect(callout(page)).toBeNull();
  });

  test("a Run that ends shows no wait, without asking", async () => {
    const client = new WaitClient();
    client.reasons.set("run_1", NESTED);
    const page = await harness(client, { runId: "run_1", status: "scheduled", waitAsk: 0 });
    await until(() => callout(page), "the wait");
    const reads = client.reads.length;
    await act(async () => drive!({ runId: "run_1", status: "completed", waitAsk: 0 }));
    expect(callout(page)).toBeNull();
    await settle();
    expect(client.reads.length).toBe(reads);
  });

  test("another Run's answer, late, is not this one's", async () => {
    const client = new WaitClient();
    client.reasons.set("run_1", NESTED);
    let release!: () => void;
    const slow = new Promise<void>((r) => (release = r));
    const read = client.runServers.bind(client);
    client.runServers = async (id = RUN_ID) => {
      if (id === "run_1") await slow;
      return read(id);
    };
    const page = await harness(client, { runId: "run_1", status: "scheduled", waitAsk: 0 });
    await act(async () => drive!({ runId: "run_2", status: "scheduled", waitAsk: 0 }));
    await until(() => page.querySelector("[data-testid=placed]"), "run_2 placed");
    await act(async () => release());
    await settle();
    expect(callout(page)).toBeNull();
  });

  test("a running Run that lux has placed is asked once, until lux's state changes", async () => {
    const client = new WaitClient();
    const page = await harness(client, { runId: "run_1", status: "running", waitAsk: 0 });
    await until(() => page.querySelector("[data-testid=placed]"), "placed");
    await settle(100);
    expect(client.reads).toEqual(["run_1"]);
    // lux moves it: a state change, and now it waits.
    client.reasons.set("run_1", NESTED);
    await act(async () => drive!({ runId: "run_1", status: "running", waitAsk: 1 }));
    await until(() => callout(page), "the wait on a running Run being moved");
    expect(client.reads).toEqual(["run_1", "run_1"]);
  });

  test("any other reason of lux's is shown as lux wrote it, with one period", async () => {
    const client = new WaitClient();
    client.reasons.set("run_1", "waiting for capacity: 2 hosts in its pool lack cpus (requested 4).");
    const page = await harness(client, { runId: "run_1", status: "scheduled", waitAsk: 0 });
    const shown = await until(() => callout(page), "the wait");
    expect(shown.textContent).toBe("Waiting for a host. lux says: waiting for capacity: 2 hosts in its pool lack cpus (requested 4). Nothing is spent meanwhile.");
    client.reasons.set("run_1", "waiting for capacity: 2 hosts in its pool lack cpus (requested 4)");
    await act(async () => drive!({ runId: "run_1", status: "scheduled", waitAsk: 1 }));
    await until(() => (callout(page)?.textContent?.includes("(requested 4). Nothing") ? true : null), "one period, added");
  });
});

describe("the Run page", () => {
  async function runPage(client: WaitClient) {
    const { container, unmount } = await mount(<RunScreen runId={RUN_ID} onBack={() => {}} client={client} />);
    mounted.push(unmount);
    await until(() => container.querySelector("[data-testid=run-screen]"), "the session");
    return container;
  }
  const serversChanged = () =>
    act(async () => emit({ eventType: "servers.changed", occurredAt: new Date().toISOString(), organizationId: ORG.id, projectId: PROJECT.id,
      taskId: TASK_ID, runId: RUN_ID, sessionId: null, workflowRunId: null, actor: { type: "system", id: RUN_ID }, source: "runner",
      correlationId: null, causationId: null, payload: { change: "state", luxState: "scheduled" } } as never));

  test("a Run running to dude that lux is resuming says why it waits, and stops saying it once lux places it", async () => {
    const client = new WaitClient({ status: "running" });
    client.reasons.set(RUN_ID, NESTED);
    const page = await runPage(client);
    await until(() => callout(page), "the wait on a running Run");
    const badge = page.querySelector("[data-testid=run-waiting]")!;
    expect(badge.textContent).toBe("Waiting for a host");
    // Beside the status, in the title's row, not among the chips under it.
    const row = badge.parentElement!.parentElement!;
    expect(row.querySelector("[data-status]")?.textContent).toContain("Running");
    expect([...row.querySelectorAll("button")].some((b) => b.getAttribute("aria-label")?.startsWith("Machine:"))).toBe(false);
    client.reasons.set(RUN_ID, null);
    await serversChanged();
    await until(() => (callout(page) === null && page.querySelector("[data-testid=run-waiting]") === null ? true : null), "the wait gone");
  });

  test("an ended Run is not asked", async () => {
    const client = new WaitClient({ status: "completed" });
    client.reasons.set(RUN_ID, NESTED);
    const page = await runPage(client);
    await settle(200);
    expect(callout(page)).toBeNull();
    expect(client.reads).toEqual([]);
  });
});

/**
 * The conductor's steers in the web app: under the line of the Run it
 * steered in Chat, each with a person's steer's delivery states — sent,
 * landing at the next step, read, not delivered and why — and, in that
 * Run's own session, a steer signed by the conductor, never a person.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { PersistedEvent, RunStatus } from "@dude/domain";
import { mount, until } from "./dom.ts";
import { FixtureClient, type LedgerQuery } from "../src/fixtures/client.ts";
import { RUN_ID, TASK_ID } from "../src/fixtures/data.ts";
import { conductorSteers, type ConductorSteer } from "../src/conducted.ts";
import { ConductorSteerTurn } from "../src/screens/ChatSection.tsx";
import { RunScreen } from "../src/screens/RunScreen.tsx";
import { activityLines } from "../src/screens/TaskScreen.tsx";
import { project } from "../src/api/conversation.ts";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

const CONDUCTOR = "run_cond_steer";
const IMPL = "run_impl_steer";
const at = (s: number) => new Date(Date.UTC(2026, 9, 4, 10, 0, s)).toISOString();
let cursor = 50_000;
function ev(eventType: string, payload: Record<string, unknown>, s: number, runId = IMPL,
  actor: PersistedEvent["actor"] = { type: "system", id: "dude" }): PersistedEvent {
  cursor++;
  return { eventId: `evt_s${cursor}`, cursor, eventType, occurredAt: at(s), organizationId: "org", projectId: "prj",
    taskId: TASK_ID, runId, sessionId: null, workflowRunId: null, actor, source: "orchestrator", correlationId: null,
    causationId: null, payload };
}
const steered = (id: string, text: string, s: number, runId = IMPL) =>
  ev("run.steered", { directiveId: id, text, scope: "run", interrupt: false, by: "conductor", conductorRunId: CONDUCTOR }, s, runId,
    { type: "agent", id: CONDUCTOR });

const plain = (h: string) => h.replace(/<[^>]+>/g, "").replace(/&#x27;/g, "'");
const shown = (steer: ConductorSteer, status: RunStatus = "running") => renderToStaticMarkup(<ConductorSteerTurn steer={steer} runStatus={status} />);

describe("the conductor's steer under its Run's line", () => {
  const ledger = [
    steered("dir_sent", "Use staging.", 1),
    steered("dir_landing", "Hide the invoice option.", 2),
    ev("run.directive.accepted", { directiveId: "dir_landing", lands: "next_step", receipt: true }, 3),
    steered("dir_read", "Cover the empty cart.", 4),
    ev("run.directive.accepted", { directiveId: "dir_read", lands: "next_step", receipt: true }, 5),
    ev("run.directive.delivered", { directiveId: "dir_read", read: true }, 6),
    steered("dir_failed", "Run the migration first.", 7),
    ev("run.directive.failed", { directiveId: "dir_failed", error: "the run finished before the agent read it" }, 8),
    // A person's steer is not the conductor's: not under the line.
    ev("run.steered", { directiveId: "dir_person", text: "mine", scope: "run", interrupt: false }, 9, IMPL, { type: "human", id: "key_1" }),
  ];
  const steers = conductorSteers(ledger).get(IMPL)!;
  const byId = (id: string) => steers.find((s) => s.directiveId === id)!;

  test("only the conductor's, in order, by the Run they went to", () => {
    expect(steers.map((s) => s.directiveId)).toEqual(["dir_sent", "dir_landing", "dir_read", "dir_failed"]);
  });

  test("sent: lux has not taken it yet", () => {
    const h = shown(byId("dir_sent"));
    expect(h).toContain('data-pending="true"');
    expect(plain(h)).toContain("ConductorSteerQueued");
    expect(plain(h)).toContain("Use staging.Sent.");
  });

  test("taken: reading at the next step, in a person's steer's words", () => {
    expect(plain(shown(byId("dir_landing")))).toContain("Lands at the agent's next step.");
    // Paused, it says so, as a person's does.
    expect(plain(shown(byId("dir_sent"), "paused"))).toContain("Lands when the run resumes.");
  });

  test("read: sent and read times, no queued mark", () => {
    const h = shown(byId("dir_read"));
    expect(h).not.toContain("data-pending");
    expect(plain(h)).toMatch(/sent \d\d:\d\d · read \d\d:\d\d:\d\d/);
  });

  test("not delivered, and why", () => {
    const h = shown(byId("dir_failed"));
    expect(h).toContain('data-failed="true"');
    expect(plain(h)).toContain("Not delivered: the run finished before the agent read it");
    expect(plain(h)).not.toContain("Retry");
  });

  test("signed by the conductor, in its colour", () => {
    const h = shown(byId("dir_read"));
    expect(h).toContain('data-role="conductor"');
    expect(plain(h)).toContain("Conductor");
  });
});

describe("competing receipts for one steer", () => {
  const failed = (s: number) => () => ev("run.directive.failed", { directiveId: "dir_c", error: "late failure" }, s);
  const read = (s: number) => () => ev("run.directive.delivered", { directiveId: "dir_c", read: true }, s);
  for (const c of [
    { name: "a failure, then a read: read", receipts: [failed(2), read(3)], readAt: at(3) },
    { name: "a read, then a failure: read", receipts: [read(2), failed(3)], readAt: at(2) },
    { name: "read twice: the first read's time", receipts: [read(2), read(5)], readAt: at(2) },
  ]) {
    test(c.name, () => {
      // Built in ledger order: each event's cursor after the one before.
      const ledger = [steered("dir_c", "Use staging.", 1)];
      for (const receipt of c.receipts) ledger.push(receipt());
      const steer = conductorSteers(ledger).get(IMPL)![0]!;
      expect([steer.deliveredAt, steer.read, steer.failed]).toEqual([c.readAt, true, null]);
      const h = plain(shown(steer));
      expect(h).not.toContain("Not delivered:");
      expect(h).toMatch(/sent \d\d:\d\d · read \d\d:\d\d:\d\d/);
    });
  }
});

describe("a person's Retry of the conductor's steer", () => {
  const person = { type: "human", id: "key_1" } as const;
  const retried = (supersedes: string, id: string, text: string, s: number) =>
    ev("run.steered", { directiveId: id, text, scope: "run", interrupt: false, supersedes }, s, IMPL, person);
  test("is the same steer: read, on one line", () => {
    const steers = conductorSteers([
      steered("dir_c1", "Use staging.", 1),
      ev("run.directive.failed", { directiveId: "dir_c1", error: "the agent errored" }, 2),
      retried("dir_c1", "dir_p2", "Use staging.", 3),
      ev("run.directive.delivered", { directiveId: "dir_p2", read: true }, 4),
    ]).get(IMPL)!;
    expect(steers.map((s) => [s.directiveId, s.deliveredAt, s.read, s.failed])).toEqual([["dir_c1", at(4), true, null]]);
    const h = plain(shown(steers[0]!));
    expect(h).toMatch(/sent \d\d:\d\d · read \d\d:\d\d:\d\d/);
    expect(h).not.toContain("Not delivered");
  });
  test("new words superseding it are the person's, not the conductor's steer", () => {
    const steers = conductorSteers([
      steered("dir_c1", "Use staging.", 1),
      ev("run.directive.failed", { directiveId: "dir_c1", error: "the agent errored" }, 2),
      retried("dir_c1", "dir_p2", "Use production.", 3),
      ev("run.directive.delivered", { directiveId: "dir_p2", read: true }, 4),
    ]).get(IMPL)!;
    expect(steers.map((s) => [s.directiveId, s.text, s.deliveredAt, s.failed])).toEqual([["dir_c1", "Use staging.", null, "the agent errored"]]);
  });
});

/** The fixture Run's ledger with a conductor's steer and a person's. */
class SteeredClient extends FixtureClient {
  protected override ledgerFor(params: LedgerQuery): PersistedEvent[] {
    const base = super.ledgerFor(params);
    if (params.runId !== RUN_ID) return base;
    const last = base.at(-1)?.cursor ?? 0;
    return [...base,
      { ...steered("dir_c", "The conductor's words.", 1, RUN_ID), cursor: last + 1, occurredAt: new Date().toISOString() },
    ].filter((e) => e.cursor > (params.after ?? 0));
  }
}

describe("the conductor's steer in the Run's session", () => {
  test("the task's Activity says the conductor steered, not someone", () => {
    const people = { names: new Map<string, string>(), byId: new Map(), you: null } as unknown as Parameters<typeof activityLines>[1];
    const lines = activityLines([steered("dir_c", "Use staging.", 1)], people, [], [], 1);
    const h = renderToStaticMarkup(<>{lines.map((l) => <p key={l.id}>{l.text}</p>)}</>);
    expect(plain(h)).toBe("The conductor steered the agent");
  });
  test("the projection marks it the conductor's, and a person's not", () => {
    const turns = project([
      steered("dir_c", "conductor", 1),
      ev("run.steered", { directiveId: "dir_p", text: "person", scope: "run", interrupt: false }, 2, IMPL, { type: "human", id: "key_1" }),
    ], "running").turns.filter((t) => t.kind === "human");
    expect(turns.map((t) => t.kind === "human" && [t.text, t.conductor, t.by?.id ?? null])).toEqual([
      ["conductor", true, null], ["person", false, "key_1"]]);
  });

  test("is signed by the conductor, with its face, never as someone", async () => {
    const { container, unmount } = await mount(<RunScreen runId={RUN_ID} onBack={() => {}} client={new SteeredClient("a")} />);
    mounted.push(unmount);
    const turn = await until(() => container.querySelector("[data-testid=human-turn][data-by=conductor]"), "the conductor's steer");
    expect(turn.getAttribute("data-role")).toBe("conductor");
    expect(turn.textContent).toContain("Conductor");
    expect(turn.textContent).toContain("The conductor's words.");
    expect(turn.textContent).not.toContain("Someone");
  });
});

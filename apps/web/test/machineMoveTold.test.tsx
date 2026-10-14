/**
 * A machine move in the transcript, from ledgers written event by event as
 * the orchestrator writes them (the machinemoves mockup's `events.ts`): one
 * notice per move, timed by its resume, and what dude told the agent as a
 * Told turn of its own, just after it and before the agent's first words.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { EventTypes, type PersistedEvent } from "@dude/domain";
import { project, type Turn } from "../src/api/conversation.ts";
import { renderTurn } from "../src/screens/RunScreen.tsx";
import type { People } from "../src/people.tsx";
import { conductor, raise, refused, reset, session } from "../mockups/machinemoves/events.ts";
import { LARGE, PEOPLE, RESET_NOTE, STANDARD, XL, toldKept, toldRaise, toldReset } from "../mockups/machinemoves/data.ts";

const NUDGE = "You were paused and have been resumed. Continue the task where you left off.";

/** Each turn from the move's notice on, in one word each: notices by text, the rest by kind and its first words. */
function fromTheMove(events: readonly PersistedEvent[]): string[] {
  const turns = project(events, "running").turns;
  const start = turns.findIndex((t) => t.kind === "notice" && /^The \w+ (moved|asked for|was to move)/.test(t.text));
  return turns.slice(start).map(word);
}

function word(t: Turn): string {
  switch (t.kind) {
    case "notice": return `notice:${t.text}`;
    case "told": return `told:${t.text}`;
    case "message": return `message:${t.text.slice(0, 20)}`;
    case "human": return `${t.intent}:${t.text.slice(0, 20)}`;
    default: return t.kind;
  }
}

const notices = (events: readonly PersistedEvent[]) => project(events, "running").turns.flatMap((t) => t.kind === "notice" ? [t.text] : []);

describe("a move's notice and what the agent was told", () => {
  test("a raise: one move row timed by its resume, the Told turn, then the agent's first words; no Resumed row", () => {
    const events = raise(false);
    expect(fromTheMove(events).slice(0, 3)).toEqual([
      "notice:The implementer moved to Large in 48s, with its conversation and work.",
      `told:${toldRaise(LARGE, false)}\n\n${NUDGE}`,
      "message:On Large now, same c",
    ]);
    expect(notices(events).filter((n) => n.startsWith("Resumed") || n.includes("moved to"))).toEqual([
      "The implementer moved to Large in 48s, with its conversation and work.",
    ]);
  });

  test("the move's row lists the resume's stages, the first its stop", () => {
    const row = project(raise(false), "running").turns.find((t) => t.kind === "notice" && t.text.includes("moved to"));
    expect(row?.kind === "notice" && row.title?.split("\n")).toEqual([
      "Stopped, work kept 9.8s", "Machine found 3.1s", "Image ready 400ms", "Workspace restored 6.2s", "Container started 2.0s",
      "agent reloaded 12s", "took its input 1.2s", "first words 14s",
    ]);
  });

  test("a Reset's row says its stop first, as a raise's does", () => {
    const row = project(reset(false), "running").turns.find((t) => t.kind === "notice" && t.text.includes("moved back to"));
    expect(row?.kind === "notice" && row.title?.split("\n")[0]).toBe("Stopped, work kept 7.4s");
  });

  test("a raise to another pool: the move's time includes the wait for a machine there", () => {
    expect(fromTheMove(raise(true)).slice(0, 2)).toEqual([
      "notice:The implementer moved to XL in 1m 11s, with its conversation and work.",
      `told:${toldRaise(XL, true)}\n\n${NUDGE}`,
    ]);
  });

  test("a Reset with a note: moved back, timed; then the Reset's words", () => {
    const events = reset(false);
    expect(fromTheMove(events).slice(0, 3)).toEqual([
      "notice:The implementer moved back to Standard in 46s, with its conversation and work.",
      `told:${toldReset(STANDARD, PEOPLE.ana.name, RESET_NOTE)}\n\n${NUDGE}`,
      "message:Back on Standard: th",
    ]);
    expect(notices(events).some((n) => n.startsWith("Resumed"))).toBe(false);
  });

  test("a Reset with a note and a person's steer: two rows, the Told turn first, then the steer where the agent read it", () => {
    const shown = fromTheMove(reset(true));
    expect(shown.slice(0, 2)).toEqual([
      "notice:The implementer moved back to Standard in 46s, with its conversation and work.",
      `told:${toldReset(STANDARD, PEOPLE.ana.name, RESET_NOTE)}`,
    ]);
    const told = shown.indexOf(`told:${toldReset(STANDARD, PEOPLE.ana.name, RESET_NOTE)}`);
    const steer = shown.findIndex((w) => w.startsWith("steer:"));
    const bash = shown.findIndex((w, i) => i > told && w === "tool");
    expect(told).toBe(1);
    expect(steer).toBeGreaterThan(told);
    // Read after the Bash it ran first, not where it was typed.
    expect(bash).toBeGreaterThan(told);
    expect(bash).toBeLessThan(steer);
  });

  test("a refused move: its refusal, the kept words as the Told turn, and no timing row", () => {
    const events = refused();
    expect(fromTheMove(events).slice(0, 3)).toEqual([
      "notice:The implementer asked for XL; lux kept it on Standard: XL is in highmem, whose machines are linux/arm64; this agent's image is linux/amd64 only.",
      `told:${toldKept(XL, STANDARD)}\n\n${NUDGE}`,
      "message:XL's machines cannot",
    ]);
    expect(notices(events).filter((n) => /^(Resumed|Taken back up)|moved/.test(n))).toEqual([]);
  });

  test("an older ledger with no run.told: one move row, timed by run.resized's epoch, and no Told turn", () => {
    const events = raise(false).filter((e) => e.eventType !== EventTypes.RunTold);
    const shown = fromTheMove(events);
    expect(shown.slice(0, 2)).toEqual([
      "notice:The implementer moved to Large in 48s, with its conversation and work.",
      "message:On Large now, same c",
    ]);
    expect(notices(events).some((n) => n.startsWith("Resumed"))).toBe(false);
    expect(shown.some((w) => w.startsWith("told:"))).toBe(false);
  });

  test("an older ledger whose move resumed into another epoch than run.resized names: its timing stays a row of its own", () => {
    const events = raise(false).filter((e) => e.eventType !== EventTypes.RunTold)
      .map((e) => e.eventType === EventTypes.RunResumeTimed ? { ...e, payload: { ...e.payload, epoch: 3 } } : e);
    expect(notices(events).filter((n) => n.startsWith("Resumed") || n.includes("moved to"))).toEqual([
      "The implementer moved to Large, with its conversation and work.",
      "Resumed in 48s, on another host.",
    ]);
  });

  test("run.told's epoch times the move when run.resized's differs", () => {
    const events = raise(false).map((e) => e.eventType === EventTypes.RunResized
      ? { ...e, payload: { machine: { ...(e.payload.machine as object), sinceEpoch: 1 } } } : e);
    expect(notices(events).filter((n) => n.startsWith("Resumed") || n.includes("moved to"))).toEqual([
      "The implementer moved to Large in 48s, with its conversation and work.",
    ]);
  });

  test("a Run parked for a person, then moved: its return is the move's one row, not a second Taken back up", () => {
    const events = raise(false);
    const request = events.findIndex((e) => e.eventType === EventTypes.MachineRequested);
    const told = events.findIndex((e) => e.eventType === EventTypes.RunTold);
    const parked = { ...events[request - 1]!, eventId: "ev_parked", eventType: "run.parked", payload: { reason: "person", message: "waits" } };
    const unparked = { ...events[told]!, eventId: "ev_unparked", eventType: "run.unparked", payload: { reason: "person", epoch: 2 } };
    const ledger = [...events.slice(0, request), parked, ...events.slice(request, told + 1), unparked, ...events.slice(told + 1)]
      .map((e, i) => ({ ...e, cursor: i + 1 }));
    expect(notices(ledger).filter((n) => /^(Resumed|Taken back up)|moved/.test(n))).toEqual([
      "The implementer moved to Large in 48s, with its conversation and work.",
    ]);
  });

  for (const eventType of [EventTypes.RunFailed, EventTypes.RunAborted]) {
    test(`${eventType} after first words keeps late timing on the move's row, with Told before first words`, () => {
      const events = raise(false);
      const timing = events.find((e) => e.eventType === EventTypes.RunResumeTimed)!;
      const failure = {
        ...timing, eventId: "ev_ended", eventType,
        payload: eventType === EventTypes.RunFailed ? { error: "model overloaded", kept: true } : { reason: "enough" },
      };
      const ledger = [...events.filter((e) => e !== timing), failure, timing]
        .map((e, i) => ({ ...e, cursor: i + 1 }));
      expect(notices(ledger).filter((n) => n.startsWith("Resumed") || n.includes("moved to"))).toEqual([
        "The implementer moved to Large in 48s, with its conversation and work.",
      ]);
      expect(fromTheMove(ledger).slice(0, 3)).toEqual([
        "notice:The implementer moved to Large in 48s, with its conversation and work.",
        `told:${toldRaise(LARGE, false)}\n\n${NUDGE}`,
        "message:On Large now, same c",
      ]);
    });
  }

  test("folding the ledger one event at a time gives the same transcript", async () => {
    const { apply, emptyProjection, snapshot } = await import("../src/api/conversation.ts");
    for (const events of [raise(false), reset(true), refused(), conductor(), session()]) {
      const state = emptyProjection();
      for (const e of events) apply(state, [e]);
      expect(snapshot(state, "running").turns.map(word)).toEqual(project(events, "running").turns.map(word));
    }
  });
});

describe("two moves on one Run", () => {
  const into3 = (p: Record<string, unknown>) => {
    if (p.epoch === 2) return { ...p, epoch: 3 };
    const m = p.machine as { sinceEpoch?: number } | undefined;
    return m?.sinceEpoch === 2 ? { ...p, machine: { ...m, sinceEpoch: 3 } } : p;
  };
  /** The raise into epoch 2, then a Reset of the same Run on its next resume, into epoch 3; `drop` leaves out a move's timing. */
  function raiseThenReset(drop: number | null = null): PersistedEvent[] {
    const first = raise(false);
    const later = reset(false);
    const runId = first[0]!.runId;
    const second = later.slice(later.findIndex((e) => e.eventType === EventTypes.MachineReset))
      .map((e) => ({ ...e, runId, payload: into3(e.payload) }));
    return [...first, ...second]
      .filter((e) => !(e.eventType === EventTypes.RunResumeTimed && e.payload.epoch === drop))
      .map((e, i) => ({ ...e, eventId: `ev_${i + 1}`, cursor: i + 1 }));
  }
  const moves = (events: readonly PersistedEvent[]) =>
    project(events, "running").turns.flatMap((t) => t.kind === "notice" && /^The \w+ moved|^Resumed|^Taken back up/.test(t.text) ? [t.text]
      : t.kind === "told" ? [`told:${t.text}`] : []);

  test("each move has its own timed row, each followed by its own Told turn, in order", () => {
    expect(moves(raiseThenReset())).toEqual([
      "The implementer moved to Large in 48s, with its conversation and work.",
      `told:${toldRaise(LARGE, false)}\n\n${NUDGE}`,
      "The implementer moved back to Standard in 46s, with its conversation and work.",
      `told:${toldReset(STANDARD, PEOPLE.ana.name, RESET_NOTE)}\n\n${NUDGE}`,
    ]);
  });

  test("the second move's timing never lands on the first's row", () => {
    expect(moves(raiseThenReset(2)).filter((m) => !m.startsWith("told:"))).toEqual([
      "The implementer moved to Large, with its conversation and work.",
      "The implementer moved back to Standard in 46s, with its conversation and work.",
    ]);
    expect(moves(raiseThenReset(3)).filter((m) => !m.startsWith("told:"))).toEqual([
      "The implementer moved to Large in 48s, with its conversation and work.",
      "The implementer moved back to Standard, with its conversation and work.",
    ]);
  });
});

describe("a move whose resume failed", () => {
  const size = (name: string) => ({ sizeId: name, name, cpus: 2, memoryMiB: 8192, diskGiB: 20, poolId: null, pool: "default", from: "task" });
  /** A conductor's move into epoch 2 that fails before it is timed, then Try again's Run, whose own epoch 2 is what `later` does. */
  function ledger(later: (add: (type: string, payload?: Record<string, unknown>, agent?: boolean) => void) => void): PersistedEvent[] {
    const events: PersistedEvent[] = [];
    let runId = "run_a";
    const add = (eventType: string, payload: Record<string, unknown> = {}, agent = false) => {
      const cursor = events.length + 1;
      events.push({
        eventId: `ev_${cursor}`, eventType, occurredAt: new Date(Date.UTC(2026, 9, 14, 8) + cursor * 1_000).toISOString(), organizationId: "o",
        projectId: "p", taskId: "t", runId, sessionId: null, workflowRunId: null, actor: { type: agent ? "agent" : "system", id: "x" },
        source: "x", correlationId: null, causationId: null, payload, cursor,
      } as PersistedEvent);
    };
    add(EventTypes.RunCreated, { role: "conductor" });
    add(EventTypes.SessionStarted, {}, true);
    add(EventTypes.AgentMessage, { text: "hi" }, true);
    add(EventTypes.MachineRequested, { moveId: "mmv_a", role: "conductor", from: size("Standard"), to: size("Large"), reason: "oom" });
    add(EventTypes.MachineRaised, { moveId: "mmv_a", role: "conductor", from: size("Standard"), to: size("Large") });
    add(EventTypes.RunPaused, { confirmed: true });
    add(EventTypes.RunResized, { machine: { ...size("Large"), sinceEpoch: 2 } });
    add(EventTypes.RunTold, { epoch: 2, moveId: "mmv_a", kind: "moved", text: "You now run on Large." });
    add(EventTypes.RunFailed, { error: "placement failed", stage: "container" });
    runId = "run_b";
    add(EventTypes.RunCreated, { role: "conductor", retries: "run_a" });
    add(EventTypes.SessionStarted, {}, true);
    add(EventTypes.AgentMessage, { text: "again" }, true);
    later(add);
    return events;
  }

  test("Try again's Run resumed into the same epoch number: its Taken back up row gets its own time", () => {
    const events = ledger((add) => {
      add("run.parked", { reason: "idle" });
      add("run.unparked", { reason: "a person", epoch: 2 });
      add(EventTypes.SessionStarted, {}, true);
      add(EventTypes.AgentMessage, { text: "back" }, true);
      add(EventTypes.RunResumeTimed, { epoch: 2, cause: "person", moved: false, totalMs: 21_000, phases: { react: 1_000 } });
    });
    const back = project(events, "running").turns.find((t) => t.kind === "notice" && t.text.startsWith("Taken back up"));
    expect(back?.kind === "notice" && back.text).toBe("Taken back up in 21s.");
    expect(back?.kind === "notice" && back.title).toBeTruthy();
  });

  test("Try again's Run moved into the same epoch number: the timing is its own move's row, not the failed one's", () => {
    const events = ledger((add) => {
      add(EventTypes.MachineRequested, { moveId: "mmv_b", role: "conductor", from: size("Standard"), to: size("XL"), reason: "oom" });
      add(EventTypes.MachineRaised, { moveId: "mmv_b", role: "conductor", from: size("Standard"), to: size("XL") });
      add(EventTypes.RunPaused, { confirmed: true });
      add(EventTypes.RunResized, { machine: { ...size("XL"), sinceEpoch: 2 } });
      add(EventTypes.RunTold, { epoch: 2, moveId: "mmv_b", kind: "moved", text: "You now run on XL." });
      add(EventTypes.SessionStarted, {}, true);
      add(EventTypes.AgentMessage, { text: "on XL" }, true);
      add(EventTypes.RunResumeTimed, { epoch: 2, cause: "machine", moved: true, totalMs: 33_000, phases: { react: 3_000 } });
    });
    expect(notices(events).filter((n) => /^(Resumed|Taken back up)|moved/.test(n))).toEqual([
      "The conductor moved to XL in 33s, with its conversation and work.",
    ]);
  });
});

describe("the Told turn as drawn", () => {
  const people: People = { you: "u", me: null, all: [], byId: new Map(), names: new Map(), refresh: async () => people, seen: () => false };
  const draw = (t: Turn) => renderToStaticMarkup(renderTurn(t, "implementer", 200_000, false, people, "El Duderino") as React.ReactElement);

  test("dude's, tagged Told, with dude's face", () => {
    const told = project(raise(false), "running").turns.find((t) => t.kind === "told")!;
    const h = draw(told);
    expect(h).toContain('data-testid="chat-told"');
    expect(h).toContain('data-intent="told"');
    expect(h.replace(/<[^>]+>/g, "")).toStartWith("El DuderinoTold");
  });

  test("a session's session.told is tagged Told, not Task", () => {
    const ev: PersistedEvent = {
      eventId: "evt_t", cursor: 1, eventType: EventTypes.BrainstormTold, occurredAt: "2026-10-14T10:00:00Z", organizationId: "o", projectId: null,
      taskId: null, runId: "r", sessionId: "s", workflowRunId: null, actor: { type: "system", id: "dude" }, source: "orchestrator",
      correlationId: null, causationId: null, payload: { text: "Ana Ribeiro owns this session now." },
    } as unknown as PersistedEvent;
    const turns = project([ev], "running").turns;
    expect(turns.map((t) => t.kind)).toEqual(["told"]);
    const h = draw(turns[0]!).replace(/<[^>]+>/g, "").replace(/\d\d:\d\d:\d\d/, "");
    expect(h).toBe("El DuderinoToldAna Ribeiro owns this session now.");
  });
});

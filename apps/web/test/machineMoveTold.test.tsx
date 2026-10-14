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

  test("folding the ledger one event at a time gives the same transcript", async () => {
    const { apply, emptyProjection, snapshot } = await import("../src/api/conversation.ts");
    for (const events of [raise(false), reset(true), refused(), conductor(), session()]) {
      const state = emptyProjection();
      for (const e of events) apply(state, [e]);
      expect(snapshot(state, "running").turns.map(word)).toEqual(project(events, "running").turns.map(word));
    }
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

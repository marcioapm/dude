/**
 * A conflict names who acted first, from the ledger, and never names you.
 */

import { describe, expect, test } from "bun:test";
import type { PersistedEvent } from "@dude/domain";
import { conflictNotice } from "../src/conflict.ts";

let cursor = 0;
const ev = (eventType: string, actor: { type: string; id: string; name?: string }): PersistedEvent =>
  ({ eventId: `e${++cursor}`, cursor, eventType, actor, payload: {}, occurredAt: "2026-01-01T00:00:00Z" }) as unknown as PersistedEvent;

const people = { you: "key_you", names: new Map([["key_bo", "Bo Lindqvist"], ["key_you", "Ana Ribeiro"]]) };

describe("conflictNotice", () => {
  test("names the person who acted first, and where the Run stands", () => {
    const n = conflictNotice("pause this run", "run r is already paused", [ev("run.steered", { type: "human", id: "key_you" }), ev("run.paused", { type: "human", id: "key_bo" })], people, "paused");
    expect(n).toEqual({ text: "Bo Lindqvist paused it first, so you did not pause this run. It is paused.", by: "Bo Lindqvist" });
  });

  test("your own acts are not the other person", () => {
    const n = conflictNotice("abort this run", "run r is already aborted", [ev("run.aborted", { type: "human", id: "key_you" })], people, "aborted");
    expect(n).toEqual({ text: "Could not abort this run: run r is already aborted", by: null });
  });

  test("the system's acts are not a person's", () => {
    const n = conflictNotice("steer this run", "run r is completed", [ev("run.completed", { type: "system", id: "dude" })], people, "completed");
    expect(n.by).toBeNull();
  });

  test("an unknown person is someone else", () => {
    const n = conflictNotice("answer the agent", "already answered", [ev("question.answered", { type: "human", id: "key_gone" })], people, "running");
    expect(n.text).toBe("Someone else answered it first, so you did not answer the agent. It is running.");
  });

  test("a name the event carries wins", () => {
    const n = conflictNotice("resume this run", "not paused", [ev("run.resumed", { type: "human", id: "key_x", name: "Cy Okafor" })], people, "running");
    expect(n.by).toBe("Cy Okafor");
  });

  test("an act that does not explain where the Run is names no one", () => {
    const n = conflictNotice("pause this run", "run r is completed", [ev("run.paused", { type: "human", id: "key_bo" })], people, "completed");
    expect(n.by).toBeNull();
  });

  test("without knowing who you are, no one is named", () => {
    const n = conflictNotice("pause this run", "already paused", [ev("run.paused", { type: "human", id: "key_you" })], { ...people, you: null }, "paused");
    expect(n.by).toBeNull();
  });
});

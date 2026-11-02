/**
 * A quiet conductor's delivery is held before its pull request by Deliver's
 * own rules (the orchestrator's deliverOpens): the hold and a fix sent back
 * from it are read as Deliver's, never a chatty conductor's.
 */

import { describe, expect, test } from "bun:test";
import { deliverOpens } from "@dude/domain";
import { heldAtGate, sentBackFromGate } from "../src/beforePullRequest.ts";

const at = { awaitingDecision: { point: "before_pull_request" as const }, hold: null, pullRequestOpened: false, status: "awaiting_input" as const, escalation: null };

describe("whose rules hold the pull request", () => {
  test("Deliver's when no conductor decides, or a quiet one does; never a chatty one's", () => {
    expect(deliverOpens({ decider: "policy" })).toBe(true);
    expect(deliverOpens({ decider: "policy", voice: null })).toBe(true);
    expect(deliverOpens({ decider: "conductor", voice: "quiet" })).toBe(true);
    expect(deliverOpens({ decider: "conductor", voice: "chatty" })).toBe(false);
    expect(deliverOpens({ decider: "conductor", voice: null })).toBe(false);
  });

  test("a quiet conductor's delivery at the gate is held, a chatty one's is not", () => {
    expect(heldAtGate({ ...at, decider: "conductor", voice: "quiet" })).toBe(true);
    expect(heldAtGate({ ...at, decider: "conductor", voice: "chatty" })).toBe(false);
    expect(heldAtGate({ ...at, decider: "policy" })).toBe(true);
  });

  test("a fix of the feedback sent back from a quiet hold is Deliver's sent-back round", () => {
    const sent = { ...at, awaitingDecision: null, status: "running" as const, hold: { round: 1, heads: {}, sentBack: true } };
    expect(sentBackFromGate({ ...sent, decider: "conductor", voice: "quiet" } as unknown as Parameters<typeof sentBackFromGate>[0])).toBe(true);
    expect(sentBackFromGate({ ...sent, decider: "conductor", voice: "chatty" } as unknown as Parameters<typeof sentBackFromGate>[0])).toBe(false);
  });
});

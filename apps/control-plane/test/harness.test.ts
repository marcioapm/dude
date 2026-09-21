/**
 * Harness tests.
 *
 * The fake harness is exercised directly (it is test infrastructure, so its
 * own correctness matters), and the OpenCode adapter's pure translation
 * functions are tested without a server.
 */

import { describe, expect, test } from "bun:test";
import type { HarnessEvent, SessionSpec } from "@dude/domain";
import { EventTypes } from "@dude/domain";
import { FakeHarness } from "../src/harness/fake.ts";
import { HarnessRegistry } from "../src/harness/registry.ts";
import { OpenCodeHarness, normalizeEvent, parseModelRef, renderDelta } from "../src/harness/opencode.ts";

function spec(overrides: Partial<SessionSpec> = {}): SessionSpec {
  return {
    sessionId: "ses_test",
    organizationId: "org_test",
    runId: "run_test",
    role: "orchestrator",
    model: "claude-opus-5",
    workspacePath: "/workspace",
    instructions: "you are an orchestrator",
    prompt: "do the thing",
    ...overrides,
  };
}

/** Drain a session's event stream to completion. */
async function collect(harness: FakeHarness, externalSessionId: string): Promise<HarnessEvent[]> {
  const events: HarnessEvent[] = [];
  for await (const event of harness.events(externalSessionId)) {
    events.push(event);
  }
  return events;
}

describe("FakeHarness", () => {
  test("replays a script and emits matching events", async () => {
    const harness = new FakeHarness().script("orchestrator", {
      actions: [
        { kind: "message", text: "investigating" },
        { kind: "tool", tool: "read", args: { path: "README.md" } },
        { kind: "complete" },
      ],
    });

    const handle = await harness.createSession(spec());
    const events = await collect(harness, handle.externalSessionId);

    expect(events.map((e) => e.type)).toEqual([
      EventTypes.SessionStarted,
      EventTypes.AgentMessage,
      EventTypes.ToolCalled,
      EventTypes.ToolCompleted,
      EventTypes.SessionStopped,
    ]);
    expect((await harness.status(handle.externalSessionId)).status).toBe("completed");
  });

  test("a blocking question parks the session until it is resumed", async () => {
    const harness = new FakeHarness().script("orchestrator", {
      actions: [
        { kind: "ask", question: "which database?" },
        { kind: "message", text: "using postgres" },
        { kind: "complete" },
      ],
    });

    const handle = await harness.createSession(spec());
    // Let the script reach the question.
    await Bun.sleep(10);

    // Parked: this is the state in which waiting must cost nothing.
    expect((await harness.status(handle.externalSessionId)).status).toBe("waiting_on_human");
    expect(await harness.messages(handle.externalSessionId)).toHaveLength(0);

    await harness.resumeSession(handle.externalSessionId, {
      summary: "answered",
      answeredQuestions: [{ question: "which database?", answer: "postgres" }],
    });
    await Bun.sleep(10);

    expect((await harness.status(handle.externalSessionId)).status).toBe("completed");
    expect(await harness.messages(handle.externalSessionId)).toHaveLength(1);
    expect(harness.resumesFor(handle.externalSessionId)).toHaveLength(1);
  });

  test("tracks subagents as children", async () => {
    const harness = new FakeHarness().script("orchestrator", {
      actions: [
        { kind: "spawn", role: "reviewer" },
        { kind: "spawn", role: "implementer" },
        { kind: "complete" },
      ],
    });

    const handle = await harness.createSession(spec());
    await Bun.sleep(10);

    const children = await harness.children(handle.externalSessionId);
    expect(children).toHaveLength(2);
    expect(children.every((c) => c.parentExternalSessionId === handle.externalSessionId)).toBe(true);
  });

  test("accumulates cost across samples", async () => {
    const harness = new FakeHarness().script("implementer", {
      actions: [
        { kind: "cost", costUsd: 0.5, inputTokens: 1000, outputTokens: 200 },
        { kind: "cost", costUsd: 0.25, inputTokens: 500, outputTokens: 100 },
        { kind: "complete" },
      ],
    });

    const handle = await harness.createSession(spec({ role: "implementer" }));
    await Bun.sleep(10);

    const usage = await harness.usage(handle.externalSessionId);
    expect(usage.costUsd).toBeCloseTo(0.75);
    expect(usage.inputTokens).toBe(1500);
    expect(usage.outputTokens).toBe(300);
  });

  test("records steering instructions for audit", async () => {
    const harness = new FakeHarness().script("orchestrator", {
      actions: [{ kind: "ask", question: "wait here" }],
    });

    const handle = await harness.createSession(spec());
    await Bun.sleep(10);
    await harness.steer(handle.externalSessionId, "prefer the simpler approach");

    expect(harness.steeringFor(handle.externalSessionId)).toEqual(["prefer the simpler approach"]);
  });

  test("abort stops the script and ends the stream", async () => {
    const harness = new FakeHarness().script("orchestrator", {
      actions: [
        { kind: "message", text: "one" },
        { kind: "message", text: "two" },
        { kind: "complete" },
      ],
      stepDelayMs: 50,
    });

    const handle = await harness.createSession(spec());
    await harness.abort(handle.externalSessionId);

    expect((await harness.status(handle.externalSessionId)).status).toBe("aborted");
    // The stream must terminate rather than hang, or a consumer leaks.
    const events = await collect(harness, handle.externalSessionId);
    expect(events.some((e) => e.payload.reason === "aborted")).toBe(true);
  });

  test("a failing session reports the error", async () => {
    const harness = new FakeHarness().script("implementer", {
      actions: [{ kind: "fail", error: "compilation failed" }],
    });

    const handle = await harness.createSession(spec({ role: "implementer" }));
    const events = await collect(harness, handle.externalSessionId);

    expect((await harness.status(handle.externalSessionId)).status).toBe("failed");
    expect(events.at(-1)?.payload.error).toBe("compilation failed");
  });
});

describe("HarnessRegistry", () => {
  test("selects harnesses by capability, not by name", () => {
    const registry = new HarnessRegistry()
      .register(new FakeHarness())
      .register(new OpenCodeHarness({ baseUrl: "http://localhost:4096" }));

    // Both support subagents; only OpenCode supports MCP.
    expect(registry.select(["subagents"]).map((h) => h.name).sort()).toEqual(["fake", "opencode"]);
    expect(registry.select(["mcp"]).map((h) => h.name)).toEqual(["opencode"]);
  });

  test("raises a clear error for an unknown harness", () => {
    const registry = new HarnessRegistry().register(new FakeHarness());
    expect(() => registry.require("nonexistent")).toThrow(/unknown harness "nonexistent".*fake/);
  });
});

describe("OpenCode translation", () => {
  test("parses provider-qualified and bare model refs", () => {
    // Field names match OpenCode's ModelRef schema: {id, providerID}.
    expect(parseModelRef("anthropic/claude-opus-5")).toEqual({
      providerID: "anthropic",
      id: "claude-opus-5",
    });
    // A bare id assumes the default provider rather than failing.
    expect(parseModelRef("claude-opus-5")).toEqual({
      providerID: "anthropic",
      id: "claude-opus-5",
    });
  });

  test("renders a resume delta compactly", () => {
    const rendered = renderDelta({
      summary: "The human answered your question.",
      answeredQuestions: [{ question: "which db?", answer: "postgres" }],
      steering: ["keep it simple"],
    });

    expect(rendered).toContain("The human answered your question.");
    expect(rendered).toContain("which db?");
    expect(rendered).toContain("postgres");
    expect(rendered).toContain("keep it simple");
  });

  test("normalizes known events into the factory schema", () => {
    const toolCall = normalizeEvent(
      { type: "tool.execute.before", properties: { tool: "bash", callID: "c1" } },
      "ses_1",
    );
    expect(toolCall?.type).toBe(EventTypes.ToolCalled);
    expect(toolCall?.payload.tool).toBe("bash");
    expect(toolCall?.externalSessionId).toBe("ses_1");

    const idle = normalizeEvent({ type: "session.idle", properties: {} }, "ses_1");
    expect(idle?.type).toBe(EventTypes.SessionStopped);
  });

  test("drops events with no factory meaning", () => {
    // The ledger records semantic milestones, not every internal tick.
    expect(normalizeEvent({ type: "storage.write", properties: {} }, "ses_1")).toBeNull();
    expect(normalizeEvent({}, "ses_1")).toBeNull();
  });

  test("declares the capabilities the plan expects of it", () => {
    const caps = new OpenCodeHarness({ baseUrl: "http://localhost:4096" }).capabilities();
    expect(caps.subagents).toBe(true);
    expect(caps.eventStream).toBe(true);
    expect(caps.liveSteering).toBe(true);
    expect(caps.mcp).toBe(true);
  });
});

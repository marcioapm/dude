/**
 * OpenCode integration test.
 *
 * Runs against a real `opencode serve` to verify the adapter's assumptions
 * about the API — response envelopes, session shape, endpoint paths. It does
 * NOT send prompts, so it spends no tokens; model behaviour is not what is
 * under test here.
 *
 * Skipped unless OPENCODE_TEST_URL is set, so the default suite stays
 * hermetic.
 */

import { beforeAll, describe, expect, test } from "bun:test";
import { OpenCodeHarness } from "../src/harness/opencode.ts";

const baseUrl = process.env.OPENCODE_TEST_URL;
const describeOrSkip = baseUrl ? describe : describe.skip;

describeOrSkip("OpenCodeHarness against a live server", () => {
  let harness: OpenCodeHarness;

  beforeAll(() => {
    harness = new OpenCodeHarness({ baseUrl: baseUrl! });
  });

  test("creates a session and reads it back", async () => {
    // Exercises the `{data: ...}` envelope unwrapping the adapter relies on.
    const handle = await harness.createSession({
      sessionId: "ses_local",
      organizationId: "org_local",
      runId: "run_local",
      role: "orchestrator",
      model: "anthropic/claude-sonnet-5",
      workspacePath: "/tmp",
      instructions: "",
      // Empty prompt: this test must not spend tokens.
      prompt: "",
    });

    expect(handle.externalSessionId).toMatch(/^ses/);

    const status = await harness.status(handle.externalSessionId);
    expect(["running", "idle", "waiting_on_human"]).toContain(status.status);
  });

  test("reports usage for cost accounting", async () => {
    const handle = await harness.createSession({
      sessionId: "ses_cost",
      organizationId: "org_local",
      runId: "run_local",
      role: "orchestrator",
      model: "anthropic/claude-sonnet-5",
      workspacePath: "/tmp",
      instructions: "",
      prompt: "",
    });

    const usage = await harness.usage(handle.externalSessionId);
    // A fresh session has spent nothing; the shape is what matters.
    expect(usage.costUsd).toBe(0);
    expect(usage.inputTokens).toBe(0);
  });

  test("lists children of a session", async () => {
    const handle = await harness.createSession({
      sessionId: "ses_children",
      organizationId: "org_local",
      runId: "run_local",
      role: "orchestrator",
      model: "anthropic/claude-sonnet-5",
      workspacePath: "/tmp",
      instructions: "",
      prompt: "",
    });

    expect(await harness.children(handle.externalSessionId)).toEqual([]);
  });
});

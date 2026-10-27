import { describe, expect, test } from "bun:test";
import {
  automationDefaults,
  automationInputSchema,
  conditionHolds,
  conditionMiss,
  conditionSchema,
  groupKeyOf,
  INCOMING_NEEDS_PROJECT,
  INCOMING_SEND_NEEDS_GROUP,
  incomingTriggerSchema,
  jsonPathValue,
  newHookId,
  redactedHeader,
  runNowSchema,
  type Condition,
} from "../src/index.ts";
import shared from "./incoming-conditions.json";

const issuesOf = (r: { success: boolean; error?: { issues: { path: (string | number)[]; message: string }[] } }) =>
  r.success ? [] : r.error!.issues.map((i) => `${i.path.join(".")}: ${i.message}`);

const hookId = "hk_7f3a9c21e4b04d18";
const alert = {
  name: "Investigate the alert",
  kind: "agent",
  agent: { own: { prompt: "An alert fired: {{payload.commonLabels.alertname}}" } },
  trigger: { kind: "incoming", hookId, conditions: [{ path: "status", op: "is", values: ["firing"] }], groupBy: "groupKey" },
};

describe("the incoming trigger", () => {
  test("defaults to a signature in X-Signature after sha256=, no conditions, no group and 30 an hour", () => {
    const t = incomingTriggerSchema.parse({ kind: "incoming", hookId });
    expect(t).toEqual({ kind: "incoming", hookId, verify: { mode: "signature", header: "X-Signature", prefix: "sha256=" },
      conditions: [], groupBy: "", ratePerHour: 30 });
  });

  test("takes a rate of 1 to 600 an hour", () => {
    expect(incomingTriggerSchema.safeParse({ kind: "incoming", hookId, ratePerHour: 1 }).success).toBe(true);
    expect(incomingTriggerSchema.safeParse({ kind: "incoming", hookId, ratePerHour: 600 }).success).toBe(true);
    expect(incomingTriggerSchema.safeParse({ kind: "incoming", hookId, ratePerHour: 0 }).success).toBe(false);
    expect(issuesOf(incomingTriggerSchema.safeParse({ kind: "incoming", hookId, ratePerHour: 601 }))).toEqual(["ratePerHour: at most 600 an hour"]);
  });

  test("verifies by signature, header token or query parameter, each with its own fields", () => {
    const ok = (verify: unknown) => incomingTriggerSchema.safeParse({ kind: "incoming", hookId, verify }).success;
    expect(ok({ mode: "signature", header: "X-Grafana-Alerting-Signature", prefix: "" })).toBe(true);
    expect(ok({ mode: "header", header: "Authorization" })).toBe(true);
    expect(ok({ mode: "query", param: "token" })).toBe(true);
    expect(ok({ mode: "header", header: "Bad Header" })).toBe(false);
    expect(ok({ mode: "query", header: "Authorization" })).toBe(false);
    expect(ok({ mode: "basic" })).toBe(false);
  });

  test("each operator takes the values it reads", () => {
    const ok = (c: unknown) => conditionSchema.safeParse(c).success;
    expect(ok({ path: "status", op: "is", values: ["firing"] })).toBe(true);
    expect(ok({ path: "status", op: "is", values: [] })).toBe(false);
    expect(ok({ path: "status", op: "one_of", values: ["a", "b"] })).toBe(true);
    expect(ok({ path: "status", op: "one_of", values: [] })).toBe(false);
    expect(ok({ path: "status", op: "exists" })).toBe(true);
    expect(ok({ path: "status", op: "exists", values: ["x"] })).toBe(false);
    expect(ok({ path: "a..b", op: "exists" })).toBe(false);
    expect(ok({ path: "", op: "exists" })).toBe(false);
  });

  test("a new hook id is hk_ and 16 base-36 characters, a different one each time", () => {
    const a = newHookId();
    expect(a).toMatch(/^hk_[a-z0-9]{16}$/);
    expect(newHookId()).not.toBe(a);
  });
});

describe("an incoming automation's rules", () => {
  test("an organisation one names at least one project", () => {
    expect(issuesOf(automationInputSchema("organization").safeParse(alert))).toEqual([`projectIds: ${INCOMING_NEEDS_PROJECT}`]);
    expect(automationInputSchema("organization").safeParse({ ...alert, projectIds: ["prj_platform"] }).success).toBe(true);
  });

  test("a project's names none; nor does a trigger that is not incoming", () => {
    expect(automationInputSchema("project").safeParse(alert).success).toBe(true);
    expect(issuesOf(automationInputSchema("project").safeParse({ ...alert, projectIds: ["prj_x"] })))
      .toEqual(["projectIds: a project automation works in its own project"]);
    expect(issuesOf(automationInputSchema("organization").safeParse({ ...alert, trigger: { kind: "github", event: "push" }, projectIds: ["prj_x"] })))
      .toEqual(["projectIds: only an organisation automation on an incoming webhook names projects"]);
  });

  test("Send to the running agent needs a group-by path", () => {
    const send = { ...alert, sessionMode: "resumable", expiryDays: 1, concurrency: "send" };
    expect(automationInputSchema("project").safeParse(send).success).toBe(true);
    expect(issuesOf(automationInputSchema("project").safeParse({ ...send, trigger: { ...alert.trigger, groupBy: "" } })))
      .toEqual([`trigger.groupBy: ${INCOMING_SEND_NEEDS_GROUP}`]);
    // Queue keeps its default: no group needed.
    expect(automationInputSchema("project").safeParse({ ...alert, trigger: { ...alert.trigger, groupBy: "" } }).success).toBe(true);
  });

  test("a server has no incoming webhook", () => {
    const server = { name: "S", kind: "server", trigger: alert.trigger, server: { command: "x", port: 80 } };
    expect(issuesOf(automationInputSchema("project").safeParse(server))).toEqual(["trigger: a server answers GitHub events; it has no incoming webhook"]);
  });

  test("the New dialog's Incoming webhook makes an incoming trigger with the hook id it is given", () => {
    const a = automationDefaults({ name: "N", kind: "agent", preset: null, trigger: "incoming" }, "UTC", hookId);
    expect(a.trigger).toEqual(incomingTriggerSchema.parse({ kind: "incoming", hookId }));
    expect(automationInputSchema("project").safeParse(a).success).toBe(true);
    // A server answers GitHub events whatever the dialog said.
    expect(automationDefaults({ name: "N", kind: "server", preset: null, trigger: "incoming" }).trigger.kind).toBe("github");
  });

  test("Run now takes a written body", () => {
    expect(runNowSchema.safeParse({ payload: { status: "firing" } }).success).toBe(true);
    expect(runNowSchema.safeParse({ payload: ["firing"] }).success).toBe(false);
  });
});

describe("conditions on a body", () => {
  for (const c of shared.cases) {
    test(`${c.path} ${c.op} ${c.values.join(",")} → ${c.ok} (body has ${c.actual})`, () => {
      expect(conditionHolds(c as Condition, shared.body)).toEqual({ ok: c.ok, actual: c.actual });
    });
  }

  test("an array index past the end, a key on an array and a key on a string are missing", () => {
    expect(jsonPathValue({ a: [1] }, "a.1")).toBeUndefined();
    expect(jsonPathValue({ a: [1] }, "a.x")).toBeUndefined();
    expect(jsonPathValue({ a: "s" }, "a.length")).toBeUndefined();
    expect(jsonPathValue({}, "toString")).toBeUndefined();
  });

  test("a group key is the path's value as text, empty when absent", () => {
    for (const g of shared.groupKeys) expect(groupKeyOf(g.groupBy, shared.body)).toBe(g.key);
  });

  test("a missed condition says what the body had", () => {
    const c = { path: "commonLabels.severity", op: "one_of", values: ["critical", "high"] } as Condition;
    expect(conditionMiss(c, conditionHolds(c, { commonLabels: { severity: "warning" } })))
      .toBe("commonLabels.severity is warning, not one of critical, high");
  });
});

describe("a delivery's headers", () => {
  test("keep no value for what proves the sender or is named like a secret", () => {
    for (const name of ["Authorization", "Cookie", "X-Signature", "X-Api-Token", "X-Webhook-Secret", "x-grafana-alerting-signature"]) {
      expect(redactedHeader(name, "X-Signature")).toBe(true);
    }
    expect(redactedHeader("X-Auth", "X-Auth")).toBe(true);
    for (const name of ["Content-Type", "User-Agent", "X-Forwarded-For"]) expect(redactedHeader(name, "X-Signature")).toBe(false);
  });
});

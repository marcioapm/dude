import { describe, expect, test } from "bun:test";
import {
  automationDefaults,
  automationInputSchema,
  parseCron,
  repoFilterAllows,
  repoFilterSchema,
  repoGlobMatches,
  REVIEWER_NEEDS_PULL_REQUEST,
  runNowSchema,
  triggerSchema,
} from "../src/index.ts";

const base = {
  name: "Nightly QA pass",
  kind: "agent",
  trigger: { kind: "schedule", cron: "0 3 * * *", tz: "Europe/Lisbon" },
  agent: { preset: { role: "qa_browser", overrides: {} }, brief: "Check the sign-in page." },
};
const issuesOf = (r: { success: boolean; error?: { issues: { path: (string | number)[]; message: string }[] } }) =>
  r.success ? [] : r.error!.issues.map((i) => `${i.path.join(".")}: ${i.message}`);

describe("an automation's session", () => {
  test("a resumable session expires after 1 to 30 days, no more", () => {
    const org = automationInputSchema("organization");
    expect(org.safeParse({ ...base, sessionMode: "resumable", expiryDays: 30 }).success).toBe(true);
    expect(org.safeParse({ ...base, sessionMode: "resumable", expiryDays: 1 }).success).toBe(true);
    expect(issuesOf(org.safeParse({ ...base, sessionMode: "resumable", expiryDays: 31 }))).toEqual(["expiryDays: at most 30 days"]);
    expect(org.safeParse({ ...base, sessionMode: "resumable", expiryDays: 0 }).success).toBe(false);
    expect(issuesOf(org.safeParse({ ...base, sessionMode: "resumable" }))).toEqual(["expiryDays: a resumable session expires after 1 to 30 days without a run"]);
    expect(issuesOf(org.safeParse({ ...base, expiryDays: 5 }))).toEqual(["expiryDays: only a resumable session expires"]);
  });

  test("only an agent or a server keeps a session to resume", () => {
    const org = automationInputSchema("organization");
    const event = { kind: "github", event: "push" };
    const resumable = { sessionMode: "resumable", expiryDays: 7 };
    expect(issuesOf(org.safeParse({ name: "Build", kind: "command", trigger: base.trigger, command: "make", ...resumable })))
      .toEqual(["sessionMode: a command runs to its end; it keeps no session to resume"]);
    expect(issuesOf(org.safeParse({ name: "Hook", kind: "webhook", trigger: base.trigger, webhook: { url: "https://example.com/hook" }, ...resumable })))
      .toEqual(["sessionMode: a webhook keeps no session"]);
    expect(org.safeParse({ name: "Serve", kind: "server", trigger: event, server: { command: "bun run serve", port: 8080 }, ...resumable }).success).toBe(true);
    expect(org.safeParse({ ...base, ...resumable }).success).toBe(true);
  });

  test("send to the running agent needs a resumable agent; parallel needs ephemeral", () => {
    const org = automationInputSchema("organization");
    expect(issuesOf(org.safeParse({ ...base, concurrency: "send" }))).toEqual(["concurrency: only a resumable agent is sent events while it runs"]);
    expect(org.safeParse({ ...base, concurrency: "send", sessionMode: "resumable", expiryDays: 7 }).success).toBe(true);
    expect(org.safeParse({ ...base, concurrency: "parallel", parallelMax: 4 }).success).toBe(true);
    expect(org.safeParse({ ...base, concurrency: "parallel", parallelMax: 11 }).success).toBe(false);
  });
});

describe("the reviewer preset", () => {
  const reviewer = { ...base, agent: { preset: { role: "reviewer", overrides: {} }, brief: "" } };
  test("needs a pull_request* event", () => {
    const org = automationInputSchema("organization");
    expect(issuesOf(org.safeParse(reviewer))).toEqual([`agent: ${REVIEWER_NEEDS_PULL_REQUEST}`]);
    expect(issuesOf(org.safeParse({ ...reviewer, trigger: { kind: "github", event: "issues" } }))).toEqual([`agent: ${REVIEWER_NEEDS_PULL_REQUEST}`]);
    for (const event of ["pull_request", "pull_request_review", "pull_request_review_comment"]) {
      expect(org.safeParse({ ...reviewer, trigger: { kind: "github", event } }).success).toBe(true);
    }
  });

  test("only Tester, Reviewer and Investigator run without a task", () => {
    const org = automationInputSchema("organization");
    for (const role of ["implementer", "conductor", "brainstorm", "simplifier", "fixer"]) {
      expect(org.safeParse({ ...base, agent: { preset: { role, overrides: {} } } }).success).toBe(false);
    }
  });
});

describe("the repository filter", () => {
  test("is refused on a project automation and taken on an organisation one", () => {
    const withFilter = { ...base, repoFilter: { mode: "except", repos: ["acme/sandbox-*", "acme/notebooks"] } };
    expect(automationInputSchema("organization").safeParse(withFilter).success).toBe(true);
    expect(issuesOf(automationInputSchema("project").safeParse(withFilter)))
      .toEqual(["repoFilter: a project automation covers the project's repositories; only an organisation automation filters them"]);
  });

  test("all names none; only and except name at least one", () => {
    expect(repoFilterSchema.safeParse({ mode: "all", repos: [] }).success).toBe(true);
    expect(repoFilterSchema.safeParse({ mode: "all", repos: ["acme/x"] }).success).toBe(false);
    expect(repoFilterSchema.safeParse({ mode: "only", repos: [] }).success).toBe(false);
    expect(repoFilterSchema.safeParse({ mode: "only", repos: ["acme"] }).success).toBe(false);
  });

  test("globs match within a segment, ignoring case; except inverts", () => {
    expect(repoGlobMatches("acme/svc-*", "Acme/svc-billing")).toBe(true);
    expect(repoGlobMatches("acme/svc-*", "acme/web")).toBe(false);
    expect(repoGlobMatches("acme/svc.*", "acme/svc-billing")).toBe(false);
    expect(repoGlobMatches("*/docs", "other/docs")).toBe(true);
    const only = { mode: "only" as const, repos: ["acme/svc-*"] };
    const except = { mode: "except" as const, repos: ["acme/svc-*"] };
    expect([repoFilterAllows(only, "acme/svc-a"), repoFilterAllows(only, "acme/web")]).toEqual([true, false]);
    expect([repoFilterAllows(except, "acme/svc-a"), repoFilterAllows(except, "acme/web")]).toEqual([false, true]);
    expect(repoFilterAllows(null, "acme/anything")).toBe(true);
  });
});

describe("triggers", () => {
  test("any GitHub event name is a trigger, including ones dude has never seen", () => {
    expect(triggerSchema.safeParse({ kind: "github", event: "merge_group", actions: ["checks_requested"] }).success).toBe(true);
    expect(triggerSchema.safeParse({ kind: "github", event: "Not An Event" }).success).toBe(false);
  });

  test("a schedule is five cron fields and a known time zone", () => {
    expect(triggerSchema.safeParse({ kind: "schedule", cron: "*/15 9-17 * * 1-5", tz: "Europe/Lisbon" }).success).toBe(true);
    expect(triggerSchema.safeParse({ kind: "schedule", cron: "0 3 * *", tz: "UTC" }).success).toBe(false);
    expect(triggerSchema.safeParse({ kind: "schedule", cron: "0 24 * * *", tz: "UTC" }).success).toBe(false);
    expect(triggerSchema.safeParse({ kind: "schedule", cron: "0 3 * * *", tz: "Mars/Olympus" }).success).toBe(false);
  });

  test("cron reads lists, ranges and steps; 7 is Sunday", () => {
    const p = parseCron("0,30 */6 1-3 * 5-7");
    expect(p.ok && p.spec).toEqual({ minutes: [0, 30], hours: [0, 6, 12, 18], days: [1, 2, 3], months: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12], weekdays: [0, 5, 6], dayOr: true });
    const bad = parseCron("0 3 * * 1/0");
    expect(bad.ok ? "" : bad.message).toBe('day of week: "1/0" has a step of 0');
  });

  test("a server answers events only", () => {
    const server = { name: "Changelog builder", kind: "server", trigger: base.trigger, server: { command: "bun run serve", port: 8080 } };
    expect(issuesOf(automationInputSchema("project").safeParse(server))).toEqual(["trigger: a server answers GitHub events; it has no schedule"]);
  });
});

describe("Run now", () => {
  test("takes nothing, a past delivery, or a written test event", () => {
    expect(runNowSchema.safeParse({}).success).toBe(true);
    expect(runNowSchema.safeParse({ deliveryId: "d-1" }).success).toBe(true);
    expect(runNowSchema.safeParse({ testEvent: { event: "pull_request", action: "opened", deliveryId: "test-1", body: { action: "opened" } } }).success).toBe(true);
    expect(runNowSchema.safeParse({ testEvent: { event: "pull_request", deliveryId: "test-1", body: [] } }).success).toBe(false);
  });
});

describe("the New automation dialog's defaults", () => {
  test("make an automation each scope accepts", () => {
    for (const kind of ["agent", "command", "server", "webhook"] as const) {
      for (const trigger of ["schedule", "github"] as const) {
        const a = automationDefaults({ name: "N", kind, preset: null, trigger });
        expect(issuesOf(automationInputSchema("project").safeParse(a))).toEqual([]);
      }
    }
    const reviewer = automationDefaults({ name: "N", kind: "agent", preset: "reviewer", trigger: "schedule" });
    expect(reviewer.trigger.kind).toBe("github");
    expect(automationInputSchema("organization").safeParse(reviewer).success).toBe(true);
  });
});

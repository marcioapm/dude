/**
 * The checks gate's judgements in a task's Activity, as the orchestrator
 * records them (`run.checks.judged`): a turn that changed nothing it could
 * push reads as checks not needed, never as verified, and the earlier Run's
 * waiver keeps its own line.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { EventTypes, runDoer, type PersistedEvent, type Run } from "@dude/domain";
import { TASK_ID } from "../src/fixtures/data.ts";
import { activityLines } from "../src/screens/TaskScreen.tsx";
import { dudeName } from "../src/DudeMark.tsx";

const at = (s: number) => new Date(Date.UTC(2026, 9, 21, 10, 0, s)).toISOString();

function run(id: string, phase: string, role: string): Run {
  return { id, organizationId: "org", projectId: "prj", taskId: TASK_ID, attempt: 1, status: "completed", workerId: null,
    workspacePath: null, error: null, kind: "agent", phase, role, category: null, parentRunId: null,
    conductorRunId: null, baseRefs: {}, heads: {}, branch: null, harness: "scripted", model: "fake/scripted", modelTier: null, dudePause: null,
    tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, context: 0 }, machine: null, image: null, preparingImage: null,
    createdAt: at(0), startedAt: at(0), endedAt: at(1) } as unknown as Run;
}

let cursor = 0;
function judged(runId: string, payload: Record<string, unknown>, s: number): PersistedEvent {
  cursor++;
  return { eventId: `evt_cj${cursor}`, cursor: 30_000 + cursor, eventType: EventTypes.RunChecksJudged, occurredAt: at(s), organizationId: "org",
    projectId: "prj", taskId: TASK_ID, runId, sessionId: null, workflowRunId: null,
    actor: { type: payload.waived ? "person" : "system", id: "dude" }, source: "orchestrator",
    correlationId: null, causationId: null, payload } as unknown as PersistedEvent;
}

const people = { names: new Map(), byId: new Map(), you: null } as unknown as Parameters<typeof activityLines>[1];
const textOf = (node: React.ReactNode) => {
  const div = document.createElement("div");
  div.innerHTML = renderToStaticMarkup(<>{node}</>);
  return div.textContent;
};

describe("checks judgements in the task's Activity", () => {
  const impl = run("run_impl_cj", "implement", "implementer");
  const simp = run("run_simp_cj", "simplify", "simplifier");
  const unchangedMessage = "No changes; checks not needed. When the turn ended, every repository this Run may push was at the commit it started from, with nothing staged, unstaged or untracked.";

  test("an unchanged turn says its checks were not needed, beside the implementer's waiver", () => {
    const lines = activityLines([
      judged(impl.id, { turn: 1, verified: true, waived: true, message: "Pre-existing failures on main.", checks: [] }, 1),
      judged(simp.id, { turn: 2, round: 0, verified: true, unchanged: true, epoch: 1, heads: { api: "a".repeat(40) },
        message: unchangedMessage, checks: [] }, 2),
    ], people, [impl, simp], [], 1);
    const texts = lines.filter((l) => textOf(l.text)?.includes("checks")).map((l) => [textOf(l.text), l.quote]);
    expect(texts).toEqual([
      ["The simplifier's checks were not needed: it changed nothing", unchangedMessage],
      ["The implementer's checks were waived", "Pre-existing failures on main."],
    ]);
  });

  test("conflicting flags preserve waiver and no-tier precedence, and unchanged precedes carried", () => {
    for (const [flags, outcome] of [
      [{ skipped: true, waived: true, unchanged: true, carried: true }, "were not model-checked"],
      [{ waived: true, unchanged: true, carried: true }, "were waived"],
      [{ unchanged: true, carried: true }, "were not needed: it changed nothing"],
      [{ carried: true }, "carried a person's waiver"],
      [{ carried: true, verified: false }, "were not verified"],
    ] as const) {
      const lines = activityLines([judged(simp.id, { verified: true, checks: [], ...flags }, 1)], people, [simp], [], 1);
      expect(lines.map((l) => textOf(l.text))).toEqual([`The simplifier's checks ${outcome}`]);
    }
  });

  test("a judged turn still reads as verified, and no-tier as not model-checked", () => {
    const lines = activityLines([
      judged(impl.id, { turn: 1, round: 0, verified: true, message: "", checks: [] }, 1),
      judged(simp.id, { turn: 2, verified: true, skipped: true, message: "no tier", checks: [] }, 2),
    ], people, [impl, simp], [], 1);
    expect(lines.filter((l) => textOf(l.text)?.includes("checks")).map((l) => textOf(l.text))).toEqual([
      "The simplifier's checks were not model-checked",
      "The implementer's checks were verified",
    ]);
  });

  test("a failure the judge passed reads as pre-existing or deferred with its reason, never as a plain pass or a waiver", () => {
    const lines = activityLines([
      judged(impl.id, { turn: 1, round: 0, verified: true, judgePassed: true, message: "", checks: [
        { command: "npx tsc --noEmit", result: "pre-existing", decision: "yes", reason: "95 errors at the parent, 95 with the change", callId: "mcall_1" },
        { command: "open the settings page", result: "deferred", decision: "yes", reason: "the tester runs it", callId: "mcall_2" },
        { command: "make lint", result: "pass", decision: "yes", reason: "clean", callId: "mcall_3" },
      ] }, 1),
    ], people, [impl], [], 1);
    expect(lines.map((l) => [textOf(l.text), l.quote])).toEqual([[
      "The implementer's checks were verified, with failures the judge passed",
      "npx tsc --noEmit: pre-existing, passed by the judge: it fails the same at the commit of the target branch the task builds on — 95 errors at the parent, 95 with the change (judgement mcall_1)\n"
        + "open the settings page: deferred, passed by the judge: a later phase runs it — the tester runs it (judgement mcall_2)\n"
        + "make lint: pass — clean (judgement mcall_3)",
    ]]);
  });

  test("a failure the judge did not pass carries the judge's reason", () => {
    const lines = activityLines([
      judged(impl.id, { turn: 1, round: 0, verified: false, message: "Your checks report was not verified.", checks: [
        { command: "npx tsc --noEmit", result: "fail", decision: "unsure", reason: "94 errors", judgeReason: "not passed by the judge: no run at the parent" },
      ] }, 1),
    ], people, [impl], [], 1);
    expect(lines.map((l) => l.quote)).toEqual([
      "Your checks report was not verified.\nnpx tsc --noEmit: fail — 94 errors — not passed by the judge: no run at the parent"]);
  });

  test("a test step that started no tester says why, which paths changed, and that the verdicts stand", () => {
    const test1 = run("run_test_cj", "test", "qa_browser");
    const skipped = { ...judged(test1.id, {}, 3), eventType: EventTypes.TestSkipped, runId: null,
      payload: { fromRunId: test1.id, paths: ["api/.github/workflows/ci.yml"],
        criteria: [{ criterion: 1, status: "pass", decision: "" }, { criterion: 2, status: "pass", decision: "" }] } } as PersistedEvent;
    const lines = activityLines([skipped], people, [test1], [], 1);
    expect(lines.map((l) => [textOf(l.text), l.quote, l.open?.runId])).toEqual([[
      `${dudeName(TASK_ID)} did not run the tester again: the change since the last test touches only paths no criterion depends on. Its 2 verdicts stand.`,
      "api/.github/workflows/ci.yml", test1.id]]);
  });

  test("a merge of the target branch not tested again says so and names the round whose verdicts stand", () => {
    const test1 = run("run_test_mg", "test", "qa_browser");
    const skipped = { ...judged(test1.id, {}, 3), eventType: EventTypes.TestSkipped, runId: null,
      payload: { fromRunId: test1.id, paths: ["api/lib/other.go"], why: "merged target branch touched none of the task's files",
        criteria: [{ criterion: 1, status: "pass", decision: "" }] } } as PersistedEvent;
    const lines = activityLines([skipped], people, [test1], [], 1);
    expect(lines.map((l) => [textOf(l.text), l.quote, l.open?.runId])).toEqual([[
      `${dudeName(TASK_ID)} did not run the tester again: the merge of the target branch before the pull request touches none of the task's files; the pull request's CI checks it. The verdict of ${runDoer(test1)} stands.`,
      "api/lib/other.go", test1.id]]);
    // The round is named by its doer, not as an anonymous agent.
    expect(runDoer(test1)).not.toBe("agent");
    const two = { ...skipped, payload: { ...skipped.payload,
      criteria: [{ criterion: 1, status: "pass", decision: "" }, { criterion: 2, status: "pass", decision: "" }] } } as PersistedEvent;
    expect(textOf(activityLines([two], people, [test1], [], 1)[0]!.text)).toEndWith(`The 2 verdicts of ${runDoer(test1)} stand.`);
  });
});

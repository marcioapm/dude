/**
 * What the tester showed, on the task page: each criterion's verdict with
 * its evidence opening the file that test published, the commits tested
 * and a stale mark once the branch moved past them, a waiver with who and
 * why — and the banner that asks for a reason before a waiver or an
 * acceptance is sent, and lets an admin who is not the owner take only
 * those two.
 */

import { afterEach, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { TASK_ID, YOU, taskFor } from "../src/fixtures/data.ts";
import type { Artifact, EscalationAction, Finding, Verification } from "../src/api/client.ts";
import { VerificationSection } from "../src/screens/VerificationSection.tsx";
import { EscalationPanel } from "../src/screens/EscalationPanel.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

const count = (root: ParentNode, sel: string) => root.querySelectorAll(sel).length;

function artifact(id: string, name: string, runId: string): Artifact {
  return { id, taskId: TASK_ID, runId, name, contentType: name.endsWith(".png") ? "image/png" : "video/webm", sizeBytes: 10,
    sha256: "x", description: "", epoch: 1, createdAt: "2026-10-01T00:00:00Z", phase: "test", role: "qa_browser", version: 1, versions: 1 };
}

const verification = (over: Partial<Verification> = {}): Verification => ({
  runId: "run_test2", attempt: 1, heads: { api: "3adab691c0ffee00" }, stale: false, candidate: false, createdAt: "2026-10-01T00:00:00Z",
  criteria: [
    { criterion: 1, text: "it greets", status: "pass", evidence: ["walkthrough.webm", "01-hello.png"], observed: "Hello, Ana.", problem: "",
      decision: null, decidedBy: null, decisionNote: null },
    { criterion: 2, text: "it pays", status: "blocked", evidence: [], observed: "No sandbox key.", problem: "",
      decision: "waived", decidedBy: { id: "per_cy", name: "Cy" }, decisionNote: "Checked by hand on staging." },
    { criterion: 3, text: "it sends a receipt", status: "unverified", evidence: ["receipt.png"], observed: "A receipt.",
      problem: "receipt.png is not a file this test published.", decision: null, decidedBy: null, decisionNote: null },
  ],
  ...over,
});

async function section(v: Verification, artifacts: Artifact[]) {
  const client = new FixtureClient("a");
  const opened: string[] = [];
  client.artifactContent = async (id) => {
    opened.push(id);
    return new Blob(["x"], { type: "image/png" });
  };
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider>
      <VerificationSection client={client} verifications={[v]} artifacts={artifacts} onOpenRun={() => {}} />
    </ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  return { page: await until(() => container.querySelector<HTMLElement>("[data-testid=verification]"), "the verification"), opened };
}

test("each criterion shows its verdict, a waiver who and why, and the tested commit", async () => {
  const { page } = await section(verification(), [artifact("art_w", "walkthrough.webm", "run_test2"), artifact("art_h", "01-hello.png", "run_test2")]);
  const rows = [...page.querySelectorAll<HTMLElement>("[data-testid=criterion]")];
  expect(rows.map((r) => r.getAttribute("data-state"))).toEqual(["pass", "waived", "unverified"]);
  expect(rows[0]!.textContent).toContain("Hello, Ana.");
  expect(rows[1]!.querySelector("[data-testid=criterion-decision]")?.textContent).toBe("Waived by Cy: Checked by hand on staging.");
  expect(rows[1]!.textContent).toContain("was blocked");
  expect(rows[2]!.textContent).toContain("receipt.png is not a file this test published.");
  expect(page.querySelector("[data-testid=verification-heads]")?.textContent).toBe("tested at 3adab69");
  expect(count(page, "[data-testid=verification-stale]")).toBe(0);
  expect(count(page, "[data-testid=verification-candidate]")).toBe(0);
});

test("evidence opens the file that test published; a name it never published opens nothing", async () => {
  // A later test's walkthrough of the same name is not this test's evidence.
  const { page, opened } = await section(verification(), [artifact("art_w", "walkthrough.webm", "run_test2"),
    artifact("art_h", "01-hello.png", "run_test2"), artifact("art_later", "01-hello.png", "run_test3")]);
  const links = [...page.querySelectorAll<HTMLElement>("[data-testid=evidence]")];
  expect(links.map((l) => l.textContent)).toEqual(["walkthrough.webm", "01-hello.png"]);
  expect([...page.querySelectorAll("[data-testid=evidence-missing]")].map((l) => l.textContent)).toEqual(["receipt.png"]);
  await click(links[1]!);
  await until(() => document.querySelector("[data-testid=file-content]"), "the viewer");
  expect(opened).toContain("art_h");
  expect(opened).not.toContain("art_later");
});

test("a criterion carried from the previous round is marked carried, and its evidence opens that round's file", async () => {
  const v = verification({ criteria: [
    { criterion: 1, text: "it greets", status: "pass", evidence: ["01-hello.png"], observed: "Only CI changed.", problem: "",
      decision: null, decidedBy: null, decisionNote: null, carriedFrom: "run_test1" },
    { criterion: 2, text: "it says goodbye", status: "pass", evidence: ["02-bye.png"], observed: "Bye.", problem: "",
      decision: null, decidedBy: null, decisionNote: null, carriedFrom: null },
  ] });
  const { page, opened } = await section(v, [artifact("art_h1", "01-hello.png", "run_test1"), artifact("art_h2", "01-hello.png", "run_test2"),
    artifact("art_b", "02-bye.png", "run_test2")]);
  const rows = [...page.querySelectorAll<HTMLElement>("[data-testid=criterion]")];
  expect(rows.map((r) => count(r, "[data-testid=criterion-carried]"))).toEqual([1, 0]);
  await click(rows[0]!.querySelector<HTMLElement>("[data-testid=evidence]")!);
  await until(() => document.querySelector("[data-testid=file-content]"), "the viewer");
  expect(opened).toContain("art_h1");
  expect(opened).not.toContain("art_h2");
});

test("a verdict the branch has moved past is marked stale", async () => {
  const { page } = await section(verification({ stale: true, heads: { api: "aaaaaaa1", web: "bbbbbbb2" } }), []);
  expect(count(page, "[data-testid=verification-stale]")).toBe(1);
  expect(count(page, "[data-testid=verification-candidate]")).toBe(0);
  expect(page.querySelector("[data-testid=verification-heads]")?.textContent).toBe("tested at api aaaaaaa, web bbbbbbb");
});

test("a pull request's fix still being checked is labelled the candidate's, not stale", async () => {
  const { page } = await section(verification({ candidate: true, heads: { api: "ccccccc3" } }), []);
  expect(page.querySelector("[data-testid=verification-candidate]")?.textContent).toBe("Candidate — not on the pull request yet");
  expect(count(page, "[data-testid=verification-stale]")).toBe(0);
  expect(page.querySelector("[data-testid=verification-heads]")?.textContent).toBe("tested at ccccccc");
});

// -- the banner --

async function banner(reason: string, actions: EscalationAction[], opts: { owner?: string; you?: string; admin?: boolean; findings?: Finding[]; detail?: Record<string, unknown> } = {}) {
  const client = new FixtureClient("a");
  const sent: Array<[EscalationAction, string] | [EscalationAction, string, string]> = [];
  client.decide = async (_task, action, note, scope) => {
    sent.push(scope ? [action, note, scope] : [action, note]);
    return { action };
  };
  const task = { ...taskFor("a"), owner: { id: opts.owner ?? YOU, name: opts.owner ? "Ana" : "Márcio", photoUrl: null, online: false },
    escalation: { reason, actions, at: "2026-10-01T00:00:00Z",
      detail: opts.detail ?? { runId: "run_test", criteria: [{ criterion: 2, text: "it pays", status: "blocked" }], findingIds: [] } } };
  const { container, unmount } = await mount(
    <EscalationPanel client={client} findings={opts.findings ?? []} task={task as Parameters<typeof EscalationPanel>[0]["task"]} you={opts.you ?? YOU}
      youAdmin={opts.admin ?? false} onOpenRun={() => {}} onDecided={() => {}} />,
  );
  mounted.push(unmount);
  return { page: await until(() => container.querySelector<HTMLElement>("[data-testid=escalation]"), "the banner"), sent };
}

async function typeNote(page: HTMLElement, text: string) {
  const field = page.querySelector<HTMLTextAreaElement>("[data-testid=escalation-note]")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
  });
  return field;
}

test("a blocked test names the criteria, and a waiver is not sent without a reason", async () => {
  const { page, sent } = await banner("test_blocked", ["retry", "waive", "stop"]);
  expect([...page.querySelectorAll("[data-testid=escalation-criterion]")].map((c) => c.textContent)).toEqual(["#2 it pays (blocked)"]);
  expect(page.textContent).toContain("Test blocked.");
  await click(page.querySelector("[data-testid=escalation-waive]")!);
  expect(sent).toEqual([]);
  const field = page.querySelector<HTMLTextAreaElement>("[data-testid=escalation-note]")!;
  expect(field.required).toBe(true);
  expect(page.textContent).toContain("Say why");
  await typeNote(page, "Checked by hand on staging.");
  await click(page.querySelector("[data-testid=escalation-waive]")!);
  expect(sent).toEqual([["waive", "Checked by hand on staging."]]);
});

test("trying again needs no reason; accepting a failed criterion does", async () => {
  const blocked = await banner("test_blocked", ["retry", "waive", "stop"]);
  await click(blocked.page.querySelector("[data-testid=escalation-retry]")!);
  expect(blocked.sent).toEqual([["retry", ""]]);
  const failed = await banner("test_failed_criteria", ["retry", "accept", "stop"]);
  expect(failed.page.querySelector("[data-testid=escalation-accept]")?.textContent).toBe("Accept the failure and go on");
  await click(failed.page.querySelector("[data-testid=escalation-accept]")!);
  expect(failed.sent).toEqual([]);
});

test("an admin who is not the owner may waive, and nothing else; a member only waits", async () => {
  const admin = await banner("test_blocked", ["retry", "waive", "stop"], { owner: "per_ana", you: "per_cy", admin: true });
  expect([...admin.page.querySelectorAll("[data-testid^=escalation-]")].map((b) => b.getAttribute("data-testid"))
    .filter((id) => ["escalation-retry", "escalation-waive", "escalation-stop"].includes(id!))).toEqual(["escalation-waive"]);
  const member = await banner("test_blocked", ["retry", "waive", "stop"], { owner: "per_ana", you: "per_bo" });
  expect(count(member.page, "[data-testid=escalation-waive]")).toBe(0);
  expect(member.page.textContent).toContain("Waiting for Ana to decide.");
});

test("convergence actions require nonblank owner direction, while stop does not", async () => {
  for (const action of ["retry", "accept", "amend"] as const) {
    const { page, sent } = await banner("not_converging", ["retry", "accept", "amend", "stop"]);
    expect(page.querySelector("[data-testid=escalation-retry]")?.textContent).toBe("Fix differently");
    expect(page.querySelector("[data-testid=escalation-accept]")?.textContent).toBe("Accept the risk");
    expect(page.querySelector("[data-testid=escalation-amend]")?.textContent).toBe("Change the intended behaviour");
    await typeNote(page, "   ");
    await click(page.querySelector(`[data-testid=escalation-${action}]`)!);
    expect(sent).toEqual([]);
    expect(page.querySelector<HTMLTextAreaElement>("textarea")!.required).toBe(true);
    await typeNote(page, "Preserve the original timestamp.");
    await click(page.querySelector(`[data-testid=escalation-${action}]`)!);
    expect(sent).toEqual([[action, "Preserve the original timestamp."]]);
  }
  const stopped = await banner("not_converging", ["retry", "accept", "amend", "stop"]);
  await click(stopped.page.querySelector("[data-testid=escalation-retry]")!);
  await click(stopped.page.querySelector("[data-testid=escalation-stop]")!);
  expect(stopped.sent).toEqual([["stop", ""]]);
});

test("stuck retry needs direction without granting admins owner-only decisions", async () => {
  const stuck = await banner("stuck", ["retry", "accept", "stop"]);
  await click(stuck.page.querySelector("[data-testid=escalation-retry]")!);
  expect(stuck.sent).toEqual([]);
  await typeNote(stuck.page, "Check the clock conversion first.");
  await click(stuck.page.querySelector("[data-testid=escalation-retry]")!);
  expect(stuck.sent).toEqual([["retry", "Check the clock conversion first."]]);
  for (const reason of ["stuck", "not_converging"]) {
    const admin = await banner(reason, ["retry", "accept", "amend", "stop"], { owner: "per_ana", admin: true });
    expect(admin.page.querySelectorAll(".escalationActions button").length).toBe(0);
  }
});

test("a stalled loop's note asks for the person's direction, before and after an action is picked", async () => {
  const noteLabel = (page: HTMLElement) =>
    page.querySelector(`label[for="${page.querySelector("[data-testid=escalation-note]")!.id}"]`)?.textContent ?? "";
  const nc = await banner("not_converging", ["retry", "accept", "amend", "stop"]);
  expect(noteLabel(nc.page)).toContain("A note for the agents (required to fix differently or accept the risk or change the intended behaviour)");
  await click(nc.page.querySelector("[data-testid=escalation-amend]")!);
  expect(noteLabel(nc.page)).toContain("Your direction to change the intended behaviour (required)");
  expect(nc.page.textContent).toContain("Give your direction: how to fix differently, the risk you accept, or the intended behaviour.");
  const stuck = await banner("stuck", ["retry", "accept", "stop"]);
  expect(noteLabel(stuck.page)).toContain("A note for the agents (required to try again)");
  await click(stuck.page.querySelector("[data-testid=escalation-retry]")!);
  expect(noteLabel(stuck.page)).toContain("Your direction to try again (required)");
  expect(stuck.page.textContent).toContain("Give your direction: how to fix differently, the risk you accept, or the intended behaviour.");
});

test("convergence shows only named findings and their loaded criteria", async () => {
  const { page } = await banner("not_converging", ["retry", "accept", "amend", "stop"], {
    detail: { findingIds: ["f2", "f3", "missing"] },
    findings: [
      { id: "f1", title: "Unrelated", acceptance: "Unrelated criterion" },
      { id: "f2", title: "Timestamp", acceptance: "The original timestamp is preserved." },
      { id: "f3", title: "Legacy blocker", acceptance: null },
    ] as Finding[],
  });
  expect([...page.querySelectorAll("[data-testid=escalation-finding]")].map((f) => [f.getAttribute("data-finding-id"), f.textContent]))
    .toEqual([["f2", "Timestamp — Done when: The original timestamp is preserved."], ["f3", "Legacy blocker"]]);
});

const checksDetail = { runId: "run_impl", rounds: 1,
  message: "Your checks report was not verified. Nothing from this Run has been pushed.\n\n- make check: exit 1 (judgement: no)\n\n- bun test: no call recorded (judgement: unsure)\n\nRun the missing checks." };
const label = (page: HTMLElement) => {
  const field = page.querySelector<HTMLTextAreaElement>("[data-testid=escalation-note]")!;
  return page.querySelector(`label[for="${field.id}"]`)?.textContent ?? "";
};

test("unverified checks show the send-back naming each check, and a waiver is not sent without a reason", async () => {
  const { page, sent } = await banner("checks_unverified", ["retry", "waive", "stop"], { detail: checksDetail });
  expect(page.textContent).toContain("Checks unverified.");
  expect(page.textContent).toContain("after 1 send-back");
  // The send-back is Markdown, as the Run's Told turn shows it: a list item per check.
  const checks = page.querySelector("[data-testid=escalation-checks]")!;
  expect([...checks.querySelectorAll("li")].map((li) => li.textContent))
    .toEqual(["make check: exit 1 (judgement: no)", "bun test: no call recorded (judgement: unsure)"]);
  expect(checks.textContent).not.toContain("- ");
  expect(checks.textContent).toContain("Run the missing checks.");
  expect(label(page)).toContain("required to waive");
  expect(label(page)).not.toContain("optional");
  await click(page.querySelector("[data-testid=escalation-waive]")!);
  expect(sent).toEqual([]);
  expect(page.querySelector<HTMLTextAreaElement>("[data-testid=escalation-note]")!.required).toBe(true);
  expect(page.textContent).toContain("Say why");
  await typeNote(page, "CI hardware was unavailable.");
  await click(page.querySelector("[data-testid=escalation-waive]")!);
  expect(sent).toEqual([["waive", "CI hardware was unavailable."]]);
});

test("sending unverified checks back needs no reason", async () => {
  const { page, sent } = await banner("checks_unverified", ["retry", "waive", "stop"], { detail: checksDetail });
  expect(page.querySelector("[data-testid=escalation-retry]")?.textContent).toBe("Send it back once more");
  await click(page.querySelector("[data-testid=escalation-retry]")!);
  expect(sent).toEqual([["retry", ""]]);
});

test("an admin who is not the owner may waive unverified checks, and nothing else", async () => {
  const admin = await banner("checks_unverified", ["retry", "waive", "stop"], { owner: "per_ana", you: "per_cy", admin: true, detail: checksDetail });
  expect([...admin.page.querySelectorAll("[data-testid^=escalation-]")].map((b) => b.getAttribute("data-testid"))
    .filter((id) => ["escalation-retry", "escalation-waive", "escalation-stop"].includes(id!))).toEqual(["escalation-waive"]);
  await click(admin.page.querySelector("[data-testid=escalation-waive]")!);
  expect(admin.sent).toEqual([]);
  await typeNote(admin.page, "Reviewed the risk.");
  await click(admin.page.querySelector("[data-testid=escalation-waive]")!);
  expect(admin.sent).toEqual([["waive", "Reviewed the risk."]]);
});

const failingDetail = { ...checksDetail, failures: [{ command: "make test", result: "fail", evidence: "TestClock needs a hardware clock" }] };

test("a waiver of reported failures is the Run's alone unless the person ticks later Runs", async () => {
  const runOnly = await banner("checks_unverified", ["retry", "waive", "stop"], { detail: failingDetail });
  const box = runOnly.page.querySelector<HTMLElement>("[data-testid=escalation-later-runs] [role=checkbox]")!;
  expect(box.getAttribute("aria-checked")).toBe("false");
  expect(runOnly.page.querySelector("[data-testid=escalation-later-runs]")?.textContent).toBe("Apply to later Runs in this attempt" +
    "A later Run's failure of make test passes only if a model judges it the same failure; anything new is held as now.");
  await typeNote(runOnly.page, "Needs a hardware clock.");
  await click(runOnly.page.querySelector("[data-testid=escalation-waive]")!);
  expect(runOnly.sent).toEqual([["waive", "Needs a hardware clock."]]);

  const attempt = await banner("checks_unverified", ["retry", "waive", "stop"], { detail: failingDetail });
  await click(attempt.page.querySelector<HTMLElement>("[data-testid=escalation-later-runs] [role=checkbox]")!);
  // Ticked, it changes nothing but a waiver.
  await click(attempt.page.querySelector("[data-testid=escalation-retry]")!);
  await typeNote(attempt.page, "Needs a hardware clock.");
  await click(attempt.page.querySelector("[data-testid=escalation-waive]")!);
  expect(attempt.sent).toEqual([["retry", ""], ["waive", "Needs a hardware clock.", "attempt"]]);
});

test("later Runs are not offered when the held report has no reported failure", async () => {
  const { page } = await banner("checks_unverified", ["retry", "waive", "stop"], { detail: checksDetail });
  expect(count(page, "[data-testid=escalation-later-runs]")).toBe(0);
  const blocked = await banner("test_blocked", ["retry", "waive", "stop"], { detail: { ...failingDetail, runId: "run_test" } });
  expect(count(blocked.page, "[data-testid=escalation-later-runs]")).toBe(0);
});

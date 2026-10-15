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
import type { Artifact, EscalationAction, Verification } from "../src/api/client.ts";
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
  runId: "run_test2", attempt: 1, heads: { api: "3adab691c0ffee00" }, stale: false, createdAt: "2026-10-01T00:00:00Z",
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

test("a verdict the branch has moved past is marked stale", async () => {
  const { page } = await section(verification({ stale: true, heads: { api: "aaaaaaa1", web: "bbbbbbb2" } }), []);
  expect(count(page, "[data-testid=verification-stale]")).toBe(1);
  expect(page.querySelector("[data-testid=verification-heads]")?.textContent).toBe("tested at api aaaaaaa, web bbbbbbb");
});

// -- the banner --

async function banner(reason: string, actions: EscalationAction[], opts: { owner?: string; you?: string; admin?: boolean } = {}) {
  const client = new FixtureClient("a");
  const sent: Array<[EscalationAction, string]> = [];
  client.decide = async (_task, action, note) => {
    sent.push([action, note]);
    return { action };
  };
  const task = { ...taskFor("a"), owner: { id: opts.owner ?? YOU, name: opts.owner ? "Ana" : "Márcio", photoUrl: null, online: false },
    escalation: { reason, actions, at: "2026-10-01T00:00:00Z",
      detail: { runId: "run_test", criteria: [{ criterion: 2, text: "it pays", status: "blocked" }], findingIds: [] } } };
  const { container, unmount } = await mount(
    <EscalationPanel client={client} task={task as Parameters<typeof EscalationPanel>[0]["task"]} you={opts.you ?? YOU}
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

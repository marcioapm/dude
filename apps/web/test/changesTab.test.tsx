/**
 * The task's Changes tab, the whole-branch review: the bar, the pinned
 * snapshot and its explicit refresh, comments on the branch through the
 * task's feedback drafts, comment marks and their cycling, and every state
 * of the fixtures' branch (`?branch=`). Mounted in happy-dom against the
 * fixture client.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { FeedbackDraftInput } from "../src/api/client.ts";
import { act, click, emitForTest, mount, settle, type, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { BRANCH_KEY } from "../src/fixtures/branchDiff.ts";
import { TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { commentsByFile, marksLabel, newerWords, nextComment, refreshedWords } from "../src/branchReview.ts";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

const fx = (branch: string | null) => {
  if (branch) localStorage.setItem(BRANCH_KEY, branch);
  return new FixtureClient("a");
};

async function changes(client: FixtureClient, props: Partial<Parameters<typeof TaskScreen>[0]> = {}) {
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} tab="changes" {...props} />
    </PeopleProvider></ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  const tab = await until(() => container.querySelector<HTMLElement>("[data-testid=changes-tab]"), "the Changes tab");
  await settle();
  return { page: container, tab };
}

const q = (el: ParentNode, id: string) => el.querySelector<HTMLElement>(`[data-testid=${id}]`);
const qa = (el: ParentNode, id: string) => [...el.querySelectorAll<HTMLElement>(`[data-testid=${id}]`)];
const sections = (tab: HTMLElement) => qa(tab, "diff-section").map((s) => s.dataset["path"]);
const headMark = (tab: HTMLElement, path: string) => tab.querySelector<HTMLButtonElement>(`[data-testid=comment-mark][data-where=head][data-path="${path}"]`);
const listMark = (tab: HTMLElement, path: string) => tab.querySelector<HTMLButtonElement>(`[data-testid=comment-mark][data-where=list][data-path="${path}"]`);

async function typeText(el: Element, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

/** Pick one line with its gutter "+" (as by key), and open the editor under it. */
async function pickLine(tab: HTMLElement, path: string, line: number) {
  const section = tab.querySelector(`[data-testid=diff-section][data-path="${path}"]`)!;
  const row = section.querySelector(`[data-cside=new][data-cline="${line}"]`)!;
  await click(row.querySelector("[data-testid=gutter-add]")!);
  return until(() => tab.querySelector("textarea"), "the comment editor");
}

describe("the Changes tab: the branch", () => {
  test("populated: one bar with the repository, the scope and the snapshot; the diff right under it", async () => {
    const { tab } = await changes(fx("populated"));
    const bar = q(tab, "changes-bar")!;
    expect(q(bar, "changes-repo")).not.toBeNull();
    expect(q(bar, "changes-scope")!.textContent).toBe("BranchPending work");
    expect(q(bar, "snapshot-line")!.textContent).toContain("main 2be91c7…7ac42e1");
    expect(q(bar, "snapshot-line")!.textContent).toContain("5 files");
    // Unified / Split is LiveDiff's own, drawn into the bar.
    expect(q(bar, "diff-view")).not.toBeNull();
    expect(sections(tab)).toContain("src/checkout/PaymentStep.tsx");
    // Binary and too-large files are listed with why their lines are not shown.
    expect(q(tab, "changes-unseen")!.textContent).toContain("card-icons.png (binary)");
    expect(q(tab, "changes-unseen")!.textContent).toContain("package-lock.json (too large)");
    // The details: target, merge base, head, checksum, uncommitted work excluded.
    await click(q(bar, "snapshot-line")!);
    const details = q(tab, "snapshot-details")!.textContent!;
    for (const words of ["Merge base", "2be91c7", "7ac42e1", "sum-tbs_fx_web", "uncommitted work excluded", "GitHub compare"]) expect(details).toContain(words);
  });

  test("the repository Select shows with more than one repository", async () => {
    const { tab } = await changes(fx("populated"));
    expect(tab.querySelectorAll("[data-testid=changes-repo]").length).toBe(1);
  });

  test("with one repository there is no repository Select", async () => {
    const one = fx("populated");
    const read = one.taskBranchDiff.bind(one);
    one.taskBranchDiff = async (id, o) => {
      const d = await read(id, o);
      return { ...d, repositories: d.repositories.slice(0, 1) };
    };
    const { tab } = await changes(one);
    await until(() => q(tab, "snapshot-line"), "the snapshot line");
    expect(tab.querySelectorAll("[data-testid=changes-repo]").length).toBe(0);
  });

  test("empty, unavailable, preparing and none say so; never an empty diff that looks like success", async () => {
    const empty = (await changes(fx("empty"))).tab;
    expect(q(empty, "changes-empty")!.textContent).toContain("No committed changes between the merge base");
    const unavailable = (await changes(fx("unavailable"))).tab;
    expect(q(unavailable, "changes-unavailable")!.textContent).toContain("Resource not accessible by integration");
    expect(q(unavailable, "changes-unavailable")!.textContent).toContain("not an empty diff");
    expect(q(unavailable, "diff-section")).toBeNull();
    expect(q(unavailable, "changes-general")).toBeNull();
    const preparing = (await changes(fx("preparing"))).tab;
    expect(q(preparing, "changes-preparing")!.textContent).toContain("not an empty diff");
  });

  test("an older attempt: one read-only line, no comment affordances", async () => {
    const { tab } = await changes(fx("older"), { attempt: 1 });
    await until(() => q(tab, "changes-read-only"), "the read-only line");
    expect(q(tab, "changes-read-only")!.textContent).toContain("Attempt 1 · ended, read-only");
    expect(tab.dataset["snapshot"]).toBe("tbs_fx_attempt1");
    expect(q(tab, "gutter-add")).toBeNull();
    expect(q(tab, "changes-general")).toBeNull();
    expect(headMark(tab, "src/checkout/PaymentStep.tsx")).not.toBeNull();
    expect(tab.textContent).toContain("Earlier attempt: keep annual invoice wording.");
    expect(tab.textContent).toContain("Earlier draft: preserve the plan event.");
    expect(tab.textContent).not.toContain("Would a missing plan id");
    await click(headMark(tab, "src/checkout/PaymentStep.tsx")!);
    expect(document.activeElement?.getAttribute("data-fb-item")).toBe("fbi_fx_old3");
    await click(headMark(tab, "src/checkout/PaymentStep.tsx")!);
    expect(document.activeElement?.getAttribute("data-fb-item")).toBe("fbi_fx_old1");
    expect(tab.querySelector("textarea")).toBeNull();
    expect([...tab.querySelectorAll("button")].filter((b) => /^(Reply|Resolve|Reopen|Edit|Delete)$/.test(b.textContent ?? ""))).toEqual([]);
  });

  test("historical feedback makes zero mutations, reports read errors, and switching current restores current comments", async () => {
    const client = fx("older");
    const calls: string[] = [];
    for (const name of ["createFeedbackDraft", "updateFeedbackDraft", "deleteFeedbackDraft", "submitFeedback", "replyFeedback", "resolveFeedback", "reopenFeedback"] as const) {
      const was = client[name].bind(client) as (...args: unknown[]) => unknown;
      (client as unknown as Record<string, unknown>)[name] = (...args: unknown[]) => { calls.push(name); return was(...args); };
    }
    const { page, tab } = await changes(client, { attempt: 1 });
    await until(() => tab.textContent?.includes("Earlier draft: preserve the plan event.") ? tab : null, "historical items");
    expect(calls).toEqual([]);
    await click(q(page, "go-current")!);
    await until(() => q(page, "changes-tab")?.textContent?.includes("Would a missing plan id") ? page : null, "current items");
    expect(q(page, "changes-tab")!.textContent).not.toContain("Earlier draft: preserve the plan event.");
    expect(calls).toEqual([]);
  });

  test("a failed historical feedback read is shown without current-attempt placements", async () => {
    const client = fx("older");
    const read = client.feedback.bind(client);
    client.feedback = (id, attempt) => attempt === 1 ? Promise.reject(new Error("history unavailable")) : read(id, attempt);
    const { tab, page } = await changes(client, { attempt: 1 });
    await until(() => q(page, "historical-feedback-problem"), "the historical read problem");
    expect(q(page, "historical-feedback-problem")!.textContent).toContain("history unavailable");
    expect(tab.textContent).not.toContain("Would a missing plan id");
  });

  test("Pending reads checkout changes without commit heads, distinguishes empty/error, retries and refreshes on events", async () => {
    const client = fx("populated");
    const task = client.getTask.bind(client);
    client.getTask = async (id) => { const t = await task(id); return { ...t, runs: t.runs.map((r) => ({ ...r, heads: {} })) }; };
    const original = client.runDiff.bind(client);
    let calls = 0;
    let state = "populated";
    client.runDiff = async (id) => {
      calls++;
      if (state === "error") throw new Error("Read denied");
      const diff = await original(id);
      return { ...diff, files: state === "empty" ? [] : diff.files };
    };
    const { tab } = await changes(client);
    expect(calls).toBe(0);
    await click([...q(tab, "changes-scope")!.querySelectorAll("button")].find((b) => b.textContent === "Pending work")!);
    await until(() => q(tab, "checkout-summary")?.textContent?.includes("2 files"), "checkout counts without heads");
    expect(tab.textContent).toContain("Session checkout changes (uncommitted work included)");
    state = "empty";
    await emitForTest("run.diff.updated", { checksum: "empty" });
    await until(() => q(tab, "checkout-summary")?.textContent?.includes("No checkout changes recorded"), "empty checkout event refresh");
    state = "error";
    await emitForTest("run.diff.updated", { checksum: "error" });
    await until(() => q(tab, "checkout-summary")?.textContent?.includes("Checkout changes unavailable"), "honest read error");
    expect(tab.textContent).not.toContain("No checkout changes recorded");
    state = "populated";
    await click([...tab.querySelectorAll("button")].find((b) => b.textContent === "Retry")!);
    await until(() => q(tab, "checkout-summary")?.textContent?.includes("2 files"), "retry succeeded");
    expect(calls).toBeGreaterThanOrEqual(4);
  });

  test("Pending work lists the live sessions, each to its own Changes; never in the branch view", async () => {
    const opened: string[] = [];
    const { tab } = await changes(fx("populated"), { onOpenRun: (id) => opened.push(id) });
    await click([...q(tab, "changes-scope")!.querySelectorAll("button")].find((b) => b.textContent === "Pending work")!);
    const pending = q(tab, "changes-pending")!;
    expect(q(tab, "diff-section")).toBeNull();
    const rows = qa(pending, "changes-session");
    expect(rows.length).toBeGreaterThan(0);
    await click([...rows[0]!.querySelectorAll("button")].find((b) => b.textContent === "Open its Changes")!);
    expect(opened).toEqual([rows[0]!.dataset["run"]!]);
  });
});

describe("the Changes tab: a pinned snapshot and an explicit refresh", () => {
  test("updated: the snapshot shown stays on re-reads; the notice names the merge with the target's name; Refresh moves it", async () => {
    const client = fx("updated");
    const asked: Array<string | undefined> = [];
    const read = client.taskBranchDiff.bind(client);
    client.taskBranchDiff = (id, o = {}) => {
      asked.push(o.snapshot);
      return read(id, o);
    };
    const { tab } = await changes(client);
    expect(tab.dataset["snapshot"]).toBe("tbs_fx_web");
    expect(q(tab, "changes-newer-words")!.textContent).toBe("main was merged in — base moved 2be91c7 → 8c1d0e4");
    // A re-read of the page (its stream) asks for the snapshot shown, by id.
    await emitForTest("task.branch_snapshot.captured", { snapshotId: "tbs_fx_merged" });
    await until(() => (asked.length > 1 ? asked : null), "the page's re-read");
    expect(asked.at(-1)).toBe("tbs_fx_web");
    expect(tab.dataset["snapshot"]).toBe("tbs_fx_web");
    await click(q(tab, "changes-refresh")!);
    await until(() => (tab.dataset["snapshot"] === "tbs_fx_merged" ? tab : null), "the newer snapshot");
    expect(asked.at(-1)).toBe("tbs_fx_merged");
    expect(q(tab, "changes-newer")).toBeNull();
    expect(q(tab, "changes-refreshed")!.textContent).toContain("Now at 3e7a2b9 · base moved 2be91c7 → 8c1d0e4");
    expect(q(tab, "snapshot-line")!.textContent).toContain("8c1d0e4…3e7a2b9");
  });

  test("a captured snapshot re-reads the page but never replaces the snapshot shown: only the notice comes", async () => {
    const client = fx("populated");
    // The API once a newer snapshot is captured: the latest with no `snapshot`, `newer` beside a named one.
    let captured = false;
    const read = client.taskBranchDiff.bind(client);
    client.taskBranchDiff = async (id, o = {}) => {
      if (!captured) return read(id, o);
      localStorage.setItem(BRANCH_KEY, "updated");
      try {
        return await read(id, { ...o, snapshot: o.snapshot ?? "tbs_fx_merged" });
      } finally {
        localStorage.setItem(BRANCH_KEY, "populated");
      }
    };
    const { tab } = await changes(client);
    expect(tab.dataset["snapshot"]).toBe("tbs_fx_web");
    expect(q(tab, "changes-newer")).toBeNull();
    captured = true;
    await emitForTest("task.branch_snapshot.captured", { snapshotId: "tbs_fx_merged", attempt: 1, repository: "web-console", relation: "merged-target" });
    await until(() => q(tab, "changes-newer"), "the notice");
    expect(tab.dataset["snapshot"]).toBe("tbs_fx_web");
    expect(q(tab, "snapshot-line")!.textContent).toContain("2be91c7…7ac42e1");
  });

  test("rebased: the notice says rewritten; Refresh is refused while a comment editor holds words", async () => {
    const { tab } = await changes(fx("rebased"));
    expect(q(tab, "changes-newer-words")!.textContent).toBe(
      "Branch was rewritten (rebase or force-push) — earlier comments are kept, marked outdated where their lines moved");
    const editor = await pickLine(tab, "src/checkout/PaymentStep.tsx", 2);
    await typeText(editor, "Keep the old import until the funnel moves.");
    const refused = q(tab, "changes-refresh")!;
    expect(refused.getAttribute("aria-disabled")).toBe("true");
    expect(refused.textContent).toContain("Save or cancel your open comment first. It stays on the snapshot it was written on.");
    await click(refused);
    expect(tab.dataset["snapshot"]).toBe("tbs_fx_web");
    expect(tab.textContent).not.toContain("Now at");
    // Cancelled, Refresh is offered again.
    await click([...tab.querySelectorAll("button")].find((b) => b.textContent === "Cancel")!);
    const ok = q(tab, "changes-refresh")! as HTMLButtonElement;
    expect(ok.disabled).toBe(false);
    await click(ok);
    await until(() => (tab.dataset["snapshot"] === "tbs_fx_rebased" ? tab : null), "the rebased snapshot");
  });

  test("a reply typed in a comment's card refuses Refresh until it is cancelled (told by the card, not by its labels)", async () => {
    const { tab } = await changes(fx("rebased"));
    const replyButton = await until(() => tab.querySelector<HTMLElement>("[data-fb-item] [data-testid=fb-reply]"), "a Reply");
    await click(replyButton);
    const box = await until(() => tab.querySelector<HTMLTextAreaElement>("[data-fb-item] textarea[aria-label=Reply]"), "the reply box");
    await typeText(box, "Agreed, but later.");
    expect(q(tab, "changes-refresh")!.getAttribute("aria-disabled")).toBe("true");
    await click([...box.closest("[data-fb-item]")!.querySelectorAll("button")].find((b) => b.textContent === "Cancel")!);
    expect((q(tab, "changes-refresh") as HTMLButtonElement).disabled).toBe(false);
    expect(q(tab, "changes-refresh")!.getAttribute("aria-disabled") === "true").toBe(false);
  });
});

describe("the Changes tab: feedback on the branch", () => {
  test("a line comment is a branch draft carrying the snapshot's id and checksum, submitted from the tray with the rest", async () => {
    const client = fx("populated");
    const sent: FeedbackDraftInput[] = [];
    const create = client.createFeedbackDraft.bind(client);
    client.createFeedbackDraft = (id, d) => { sent.push(d); return create(id, d); };
    const { page, tab } = await changes(client);
    const editor = await pickLine(tab, "src/flags.ts", 13);
    await typeText(editor, "Say when the flag goes.");
    await click([...tab.querySelectorAll("button")].find((b) => b.textContent === "Save draft")!);
    await until(() => (sent.length ? sent : null), "the draft");
    expect(sent[0]!.anchor).toEqual({ kind: "code", scope: "branch", snapshotId: "tbs_fx_web", repository: "web-console", path: "src/flags.ts",
      side: "new", startLine: 13, endLine: 13, diffChecksum: "sum-tbs_fx_web" });
    // Drawn under its line, marked on its file.
    await until(() => headMark(tab, "src/flags.ts"), "the mark on flags.ts");
    expect(tab.querySelector('[data-testid=diff-section][data-path="src/flags.ts"]')!.textContent).toContain("Say when the flag goes.");
    // General feedback from the bar: a general draft in the same model.
    await click(q(tab, "changes-general")!);
    await typeText(q(tab, "changes-general-editor")!.querySelector("textarea")!, "Ship it behind the flag.");
    await click([...q(tab, "changes-general-editor")!.querySelectorAll("button")].find((b) => b.textContent === "Save draft")!);
    await until(() => (sent.length === 2 ? sent : null), "the general draft");
    expect(sent[1]!.anchor).toEqual({ kind: "general" });
    // The tray submits them (and the seeded draft) in one batch, with its confirmation.
    await click(await until(() => page.querySelector("[data-testid=fb-submit]"), "Submit"));
    const confirm = await until(() => document.querySelector<HTMLElement>("[data-testid=fb-confirm]"), "the confirmation");
    expect(confirm.textContent).toBe("Submit 3");
    await click(confirm);
    await until(() => (client.feedbackItems.filter((i) => i.status === "draft").length === 0 ? true : null), "the batch submitted");
    expect(client.feedbackItems.filter((i) => i.batchId?.startsWith("fbt_fixture_")).length).toBe(3);
    expect(tab.querySelector('[data-testid=diff-section][data-path="src/flags.ts"]')!.textContent).toContain("Say when the flag goes.");
  });

  test("an older attempt refuses a submit with its reason", async () => {
    const client = fx("older");
    await client.createFeedbackDraft(TASK_ID, { body: "On the current attempt.", anchor: { kind: "general" } });
    const { page, tab } = await changes(client, { attempt: 1 });
    await until(() => q(tab, "changes-read-only"), "read-only");
    expect(q(tab, "gutter-add")).toBeNull();
    const refused = await until(() => page.querySelector("[data-testid=fb-submit-refused]"), "the refused Submit");
    expect(refused.querySelector("button")!.disabled).toBe(true);
    expect(page.querySelector("[data-testid=fb-submit]")).toBeNull();
  });
});

describe("the Changes tab: comment marks and cycling", () => {
  const PAY = "src/checkout/PaymentStep.tsx";
  const TEST = "src/checkout/PaymentStep.test.tsx";

  test("a mark on each commented file's header and list entry, in words; cycling visits outdated first, then by line, and wraps", async () => {
    const { tab } = await changes(fx("populated"));
    const mark = (await until(() => headMark(tab, PAY), "the mark"));
    expect(mark.getAttribute("aria-label")).toBe("4 comments on src/checkout/PaymentStep.tsx — show next");
    expect(listMark(tab, PAY)).not.toBeNull();
    expect(listMark(tab, TEST)!.getAttribute("aria-label")).toBe("1 comment on src/checkout/PaymentStep.test.tsx — show next");
    expect(headMark(tab, "src/flags.ts")).toBeNull();
    const order: string[] = [];
    for (let i = 0; i < 5; i++) {
      await click(headMark(tab, PAY)!);
      await settle(10);
      order.push((document.activeElement as HTMLElement).dataset["fbItem"]!);
      expect(document.activeElement!.hasAttribute("data-flash")).toBe(true);
    }
    // Lin's outdated one under the header, then lines 2–3, 8, 9; then round again.
    expect(order).toEqual(["fbi_fx_b4", "fbi_fx_b2", "fbi_fx_b1", "fbi_fx_b3", "fbi_fx_b4"]);
    expect(headMark(tab, PAY)!.textContent).toBe("1 / 4");
    expect(headMark(tab, PAY)!.getAttribute("aria-label")).toBe("Comment 1 of 4 on src/checkout/PaymentStep.tsx — show next");
  });

  test("a commented file hidden by the filter stays listed; its mark clears the filter and moves to its comment", async () => {
    const { tab } = await changes(fx("populated"));
    await type(q(tab, "changes-filter")!, "flags");
    expect(sections(tab)).toEqual(["src/flags.ts"]);
    const hidden = qa(tab, "filtered-out-file").map((r) => r.dataset["path"]);
    expect(hidden.sort()).toEqual([TEST, PAY].sort());
    await click(listMark(tab, TEST)!);
    expect((q(tab, "changes-filter") as HTMLInputElement).value).toBe("");
    expect(sections(tab)).toContain(TEST);
    expect((document.activeElement as HTMLElement).dataset["fbItem"]).toBe("fbi_fx_b5");
  });

  test("with another file shown alone, the mark shows its own file alone", async () => {
    const { tab } = await changes(fx("populated"));
    await click(tab.querySelector('[data-testid=diff-file][data-path="src/flags.ts"]')!);
    expect(sections(tab)).toEqual(["src/flags.ts"]);
    await click(listMark(tab, PAY)!);
    expect(sections(tab)).toEqual([PAY]);
  });

  test("the cycle restarts after a refresh", async () => {
    const { tab } = await changes(fx("updated"));
    await click(await until(() => headMark(tab, PAY), "the mark"));
    expect(headMark(tab, PAY)!.textContent).toContain("1 / ");
    await click(q(tab, "changes-refresh")!);
    await until(() => (tab.dataset["snapshot"] === "tbs_fx_merged" ? tab : null), "the newer snapshot");
    expect(headMark(tab, PAY)!.textContent).not.toContain("/");
  });
});

describe("the branch review's words", () => {
  test("each relation's notice, and the line after a refresh", () => {
    const n = { id: "x", fromMergeBaseSha: "aaaaaaa1", fromHeadSha: "bbbbbbb1", mergeBaseSha: "ccccccc1", headSha: "ddddddd1", capturedAt: "", count: 1 };
    expect(newerWords({ ...n, relation: "fast-forward" }, "main")).toBe("New commits on the branch — head bbbbbbb → ddddddd");
    expect(newerWords({ ...n, relation: "merged-target" }, "develop")).toBe("develop was merged in — base moved aaaaaaa → ccccccc");
    expect(refreshedWords({ mergeBaseSha: "aaaaaaa1", headSha: "b" }, { mergeBaseSha: "aaaaaaa1", headSha: "ddddddd1", relation: "fast-forward" }))
      .toStartWith("Now at ddddddd. ");
    expect(nextComment(undefined, 3)).toBe(0);
    expect(nextComment(2, 3)).toBe(0);
  });

  test("the mark's tooltip tells submitted, drafts and outdated apart", () => {
    const item = (id: string, author: string, status: "open" | "draft", line: number | null) => ({
      item: { id, status, author: { id: author, name: author === "me" ? "Me" : `${author} Silva` }, createdAt: id } as never,
      path: "a", at: line === null ? { head: "outdated" as const } : { side: "new" as const, startLine: line, endLine: line },
    });
    const list = commentsByFile([item("1", "ana", "open", 8), item("2", "me", "draft", 3), item("3", "tom", "open", null)]).get("a")!;
    expect(list.map((p) => p.item.id)).toEqual(["3", "2", "1"]);
    expect(marksLabel(list, "me")).toBe("2 submitted by tom Silva, ana Silva · 1 your draft · 1 outdated · lines changed since");
  });
});

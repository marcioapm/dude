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
import { act, click, mount, settle, type, until } from "./dom.ts";
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

  test("the repository Select shows only with more than one repository", async () => {
    const client = fx("populated");
    expect(q((await changes(client)).tab, "changes-repo")).not.toBeNull();
    for (const unmount of mounted) await unmount();
    mounted = [];
    const one = fx("populated");
    const read = one.taskBranchDiff.bind(one);
    one.taskBranchDiff = async (id, o) => {
      const d = await read(id, o);
      return { ...d, repositories: d.repositories.slice(0, 1) };
    };
    const { tab } = await changes(one);
    expect(q(tab, "snapshot-line")).not.toBeNull();
    expect(q(tab, "changes-repo")).toBeNull();
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
    expect(q(tab, "comment-mark")).toBeNull();
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

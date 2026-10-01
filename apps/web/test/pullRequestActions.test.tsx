/**
 * The actions a pull request offers, rendered: nothing re-runs a check
 * that could not be read, and Merge stays off with the reason.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { PullRequest } from "@dude/domain";
import type { ApiClient } from "../src/api/client.ts";
import { PullRequestActions } from "../src/screens/PullRequestActions.tsx";

const denied = { name: "GitHub check runs", status: "unavailable", conclusion: "", diagnostic: "check_runs_forbidden" };
const pr = (over: Partial<PullRequest>): PullRequest => ({
  id: "pr_1", taskId: "t", runId: null, repositoryId: "r", repositoryName: "web", number: 41, url: "https://github.com/a/web/pull/41",
  headBranch: "dude/t/attempt-1", baseBranch: "main", title: "Chart library", state: "open",
  checks: [], checkState: "pending", review: "approved", reviews: [], mergeable: "clean", behindBy: 0, unresolvedThreads: 0,
  display: "ci_running", createdAt: "", updatedAt: "", ...over,
});

const render = (p: PullRequest) => renderToStaticMarkup(
  <PullRequestActions client={{} as ApiClient} pr={p} defaultMethod="squash" onChanged={() => {}}>
    {({ facts, merge, note }) => <div>{facts.checks}{merge}{note}</div>}
  </PullRequestActions>,
);

describe("pull request actions", () => {
  test("unreadable check runs offer no re-run, and Merge is off saying why", () => {
    const h = render(pr({ checks: [{ name: "CodeRabbit", status: "completed", conclusion: "success" }, denied] }));
    expect(h).not.toContain('data-testid="pr-rerun"');
    expect(h).toMatch(/data-testid="pr-merge"[^>]*disabled|disabled[^>]*data-testid="pr-merge"/);
    const note = h.match(/<span data-testid="pr-blocked">([^<]*)<\/span>/)?.[1]?.replaceAll("&#x27;", "'");
    expect(note).toBe("Blocked: dude's GitHub token can't read this repository's checks (it needs Checks: Read)");
  });

  test("a real failure beside them can still be re-run", () => {
    const h = render(pr({ checkState: "failing", display: "ci_red", checks: [{ name: "lint", status: "completed", conclusion: "failure" }, denied] }));
    expect(h).toContain('data-testid="pr-rerun"');
  });
});

describe("asking for a review", () => {
  test("picks from GitHub's suggestions and a search, and asks for them together", async () => {
    const { act } = await import("react");
    const { createRoot } = await import("react-dom/client");
    const { PullRequestPanel } = await import("@dude/design-system/components");
    const asked: string[][] = [];
    const found: string[] = [];
    const client = {
      reviewerCandidates: async (_id: string | null, q: string) => {
        found.push(q);
        return q
          ? [{ kind: "user", login: "hanna", name: "Hanna Lindqvist" }, { kind: "team", login: "acme/platform", name: "Platform", members: 7 }]
          : [{ kind: "user", login: "ana", name: "Ana Ribeiro", reason: "changed", requested: true }, { kind: "user", login: "tom", name: "Tom Okafor", reason: "commented" }];
      },
      requestReview: async (_id: string, logins: string[]) => void asked.push(logins),
    } as unknown as ApiClient;
    const host = document.body.appendChild(document.createElement("div"));
    const root = createRoot(host);
    const p = pr({ checkState: "passing", checks: [], display: "awaiting", review: "pending" });
    await act(async () => root.render(
      <PullRequestActions client={client} pr={p} defaultMethod="squash" onChanged={() => {}}>
        {(a) => <PullRequestPanel pr={p} factActions={a.facts} factUnder={a.under} />}
      </PullRequestActions>,
    ));
    const q = <T extends Element>(s: string) => host.querySelector<T>(s)!;
    const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 250))));
    const key = (k: string, init: KeyboardEventInit = {}) =>
      act(async () => void q("[role=combobox]").dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, ...init })));

    await act(async () => q<HTMLButtonElement>('[data-testid="pr-request-review"]').click());
    await settle();
    expect(found).toEqual([""]);
    const options = () => [...host.querySelectorAll("[role=option]")].map((o) => o.textContent);
    expect(options()[0]).toContain("Already asked");
    expect(options()[1]).toContain("Commented on this pull request");
    await key("Enter");
    await settle();
    expect(q('[data-testid="reviewer-picks"]').textContent).toContain("Tom Okafor");

    await act(async () => {
      const field = q<HTMLInputElement>("[role=combobox]");
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, "an");
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await settle();
    expect(options().map((o) => o?.slice(0, 2))).toEqual(["HL", "PL"]);
    await key("ArrowDown");
    await key("Enter");
    await settle();
    expect(q('[data-testid="pr-review-send"]').textContent).toBe("Ask 2 reviewers");
    await key("Enter", { ctrlKey: true });
    await settle();
    expect(asked).toEqual([["tom", "acme/platform"]]);
    expect(host.querySelector('[data-testid="pr-review-ask"]')).toBeNull();
    await act(async () => root.unmount());
    host.remove();
  });
});

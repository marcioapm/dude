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
    expect(note).toBe("Blocked: GitHub won't show dude this repository's checks");
  });

  test("a real failure beside them can still be re-run", () => {
    const h = render(pr({ checkState: "failing", display: "ci_red", checks: [{ name: "lint", status: "completed", conclusion: "failure" }, denied] }));
    expect(h).toContain('data-testid="pr-rerun"');
  });
});

describe("resolving conflicts", () => {
  const conflicting = pr({ mergeable: "conflicting", behindBy: 2, display: "conflict" });
  const panel = async (client: ApiClient, changed: () => void) => {
    const { mount } = await import("./dom.ts");
    return mount(
      <PullRequestActions client={client} pr={conflicting} defaultMethod="squash" onChanged={changed}>
        {({ facts, note }) => <div>{facts.base}{note}</div>}
      </PullRequestActions>,
    );
  };

  test("a clean pull request offers no resolve, and a conflicting one no Update branch", () => {
    const base = (p: PullRequest) => renderToStaticMarkup(
      <PullRequestActions client={{} as ApiClient} pr={p} defaultMethod="squash" onChanged={() => {}}>
        {({ facts }) => <div>{facts.base}</div>}
      </PullRequestActions>,
    );
    expect(base(pr({ behindBy: 0 }))).not.toContain("pr-resolve-conflicts");
    expect(base(pr({ behindBy: 2 }))).not.toContain("pr-resolve-conflicts");
    const h = base(conflicting);
    expect(h).toContain('data-testid="pr-resolve-conflicts"');
    expect(h).not.toContain("pr-update-branch");
  });

  test("asks the workflow once, disabled while asking, and reads the pull request back", async () => {
    const { click, settle } = await import("./dom.ts");
    let release!: () => void;
    const asked: string[] = [];
    let changed = 0;
    const client = {
      resolvePullRequestConflicts: (id: string) => {
        asked.push(id);
        return new Promise<void>((r) => { release = r; });
      },
    } as unknown as ApiClient;
    const { container, unmount } = await panel(client, () => void changed++);
    const button = () => container.querySelector<HTMLButtonElement>('[data-testid="pr-resolve-conflicts"]')!;
    await click(button());
    expect(button().disabled).toBe(true);
    expect(button().textContent).toBe("Asking…");
    release();
    await settle();
    expect(asked).toEqual(["pr_1"]);
    expect(changed).toBe(1);
    expect(button().disabled).toBe(false);
    await unmount();
  });

  test("a refusal is shown in the server's words", async () => {
    const { click, settle } = await import("./dom.ts");
    const { ApiError } = await import("../src/api/client.ts");
    const client = {
      resolvePullRequestConflicts: () =>
        Promise.reject(new ApiError(409, "conflict", "dude is already working on the task; try again once it waits on the pull request")),
    } as unknown as ApiClient;
    const { container, unmount } = await panel(client, () => {});
    await click(container.querySelector('[data-testid="pr-resolve-conflicts"]')!);
    await settle();
    expect(container.querySelector('[data-testid="pr-problem"]')?.textContent ?? "no problem shown").toContain("already working on the task");
    await unmount();
  });
});

describe("a conflict on the panel", () => {
  const conflicting = pr({ mergeable: "conflicting", behindBy: 2, display: "conflict" });
  // As the task page puts the two together.
  const shown = async (p: PullRequest, props: { resolving?: boolean; waitingFor?: string }) => {
    const { mount } = await import("./dom.ts");
    const { PullRequestPanel } = await import("@dude/design-system/components");
    return mount(
      <PullRequestActions client={{} as ApiClient} pr={p} defaultMethod="squash" onChanged={() => {}} {...props}>
        {(a) => <PullRequestPanel pr={p} factActions={a.facts} conflict={a.conflict} />}
      </PullRequestActions>,
    );
  };
  const base = (c: HTMLElement) => c.querySelector('[data-fact="base"]')?.textContent ?? "no base line";

  test("for the owner: the base by name in mono, and Resolve conflicts", async () => {
    const { container, unmount } = await shown(conflicting, {});
    expect(base(container)).toBe("Conflicts with mainResolve conflicts");
    expect(container.querySelector('[data-fact="base"] .ds-mono')?.textContent).toBe("main");
    expect(container.querySelectorAll('[data-testid="pr-resolve-conflicts"]').length).toBe(1);
    await unmount();
  });

  test("while dude resolves it: says so, and offers nothing to press", async () => {
    const { container, unmount } = await shown(conflicting, { resolving: true });
    expect(base(container)).toBe("Conflicts with main · dude is resolving it");
    expect(container.querySelectorAll('[data-testid="pr-resolve-conflicts"]').length).toBe(0);
    expect(container.querySelectorAll('[data-testid="pr-conflict-waiting"]').length).toBe(0);
    await unmount();
  });

  test("for anyone but the owner: whom it waits on, and no action", async () => {
    const { container, unmount } = await shown(conflicting, { waitingFor: "Ana Ribeiro" });
    expect(base(container)).toBe("Conflicts with mainWaiting for Ana Ribeiro");
    expect(container.querySelector('[data-testid="pr-conflict-waiting"]')?.textContent).toBe("Waiting for Ana Ribeiro");
    expect(container.querySelectorAll('[data-testid="pr-resolve-conflicts"]').length).toBe(0);
    await unmount();
  });

  test("a pull request that does not conflict says neither", async () => {
    const { container, unmount } = await shown(pr({ mergeable: "clean" }), { resolving: true, waitingFor: "Ana Ribeiro" });
    expect(base(container)).toBe("Up to date with main");
    expect(container.querySelectorAll('[data-testid="pr-conflict-waiting"], [data-testid="pr-resolve-conflicts"]').length).toBe(0);
    await unmount();
  });
});

describe("asking for a review", () => {
  test("picks from GitHub's suggestions and a search, and asks for them together", async () => {
    const { act, click, mount, settle, type } = await import("./dom.ts");
    const { PullRequestPanel } = await import("@dude/design-system/components");
    const asked: string[][] = [];
    const found: string[] = [];
    const client = {
      reviewerCandidates: async (_id: string | null, q: string) => {
        found.push(q);
        return q
          ? [{ kind: "user", login: "hanna", name: "Hanna Lindqvist" }, { kind: "team", login: "acme/platform", name: "Platform", members: 7 }]
          : [{ kind: "user", login: "ana", name: "Ana Ribeiro", reason: "changed" }, { kind: "user", login: "tom", name: "Tom Okafor", reason: "commented" }];
      },
      requestReview: async (_id: string, logins: string[]) => void asked.push(logins),
    } as unknown as ApiClient;
    // Ana was asked already: the picker shows her, and will not pick her.
    const p = pr({ checkState: "passing", checks: [], display: "awaiting", review: "pending", reviews: [{ login: "ana", state: "REQUESTED" }] });
    const { container, unmount } = await mount(
      <PullRequestActions client={client} pr={p} defaultMethod="squash" onChanged={() => {}}>
        {(a) => <PullRequestPanel pr={p} factActions={a.facts} factUnder={a.under} />}
      </PullRequestActions>,
    );
    const q = <T extends Element>(s: string) => container.querySelector<T>(s)!;
    const options = () => [...container.querySelectorAll("[role=option]")].map((o) => o.textContent);
    const key = (k: string, init: KeyboardEventInit = {}) =>
      act(async () => void q("[role=combobox]").dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, ...init })));

    await click(q('[data-testid="pr-request-review"]'));
    await settle(250);
    expect(found).toEqual([""]);
    expect(options()[0]).toContain("Already asked");
    expect(options()[1]).toContain("Commented on this pull request");
    await key("Enter");
    await settle();
    expect(q('[data-testid="reviewer-picks"]').textContent).toContain("Tom Okafor");

    await type(q("[role=combobox]"), "an");
    await settle(250);
    expect(options().map((o) => o?.slice(0, 2))).toEqual(["HL", "PL"]);
    await key("ArrowDown");
    await key("Enter");
    await settle();
    expect(q('[data-testid="pr-review-send"]').textContent).toBe("Ask 2 reviewers");
    await key("Enter", { ctrlKey: true });
    await settle();
    expect(asked).toEqual([["tom", "acme/platform"]]);
    expect(container.querySelectorAll('[data-testid="pr-review-ask"]').length).toBe(0);
    await unmount();
  });
});

describe("the reviewers a setting names", () => {
  test("one saved in another case is not offered again", async () => {
    const { mount, settle, type } = await import("./dom.ts");
    const { ReviewerPicker } = await import("../src/screens/ReviewerPicker.tsx");
    const client = {
      reviewerCandidates: async () => [{ kind: "user", login: "ana", name: "Ana Ribeiro" }, { kind: "user", login: "hanna", name: "Hanna Lindqvist" }],
    } as unknown as ApiClient;
    const { container, unmount } = await mount(
      <ReviewerPicker client={client} pullRequestId={null} picked={[{ kind: "user", login: "Ana" }]} onChange={() => {}} />,
    );
    await type(container.querySelector("[role=combobox]")!, "an");
    await settle(250);
    expect([...container.querySelectorAll("[role=option]")].map((o) => o.textContent?.slice(0, 2))).toEqual(["HL"]);
    await unmount();
  });
});

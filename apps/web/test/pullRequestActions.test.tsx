/**
 * The actions a pull request offers, rendered: nothing re-runs a check
 * that could not be read, and Merge stays off with the reason.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { PullRequest } from "@dude/domain";
import type { ApiClient, TaskDetail } from "../src/api/client.ts";
import type { People } from "../src/people.tsx";
import { PullRequestActions } from "../src/screens/PullRequestActions.tsx";
import { notYours } from "../src/screens/TaskLifecycle.tsx";

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

const four = ["pr-merge", "pr-update-branch", "pr-rerun", "pr-request-review"];
const buttons = (c: HTMLElement) => four.map((id) => c.querySelector<HTMLButtonElement>(`[data-testid="${id}"]`));

// Each of the four is off, and focusing its wrapper shows tip.
async function expectFourRefused(c: HTMLElement, tip: string): Promise<void> {
  const { act, until } = await import("./dom.ts");
  for (const [i, b] of buttons(c).entries()) {
    expect(b ? b.disabled : `${four[i]} missing`).toBe(true);
    await act(async () => (b!.parentElement as HTMLElement).focus());
    const shown = await until(() => document.querySelector("[role=tooltip]"), `${four[i]}'s tooltip`);
    expect(shown.textContent).toBe(tip);
    await act(async () => (b!.parentElement as HTMLElement).blur());
    await until(() => (document.querySelector("[role=tooltip]") ? null : true), `${four[i]}'s tooltip gone`);
  }
}

describe("who may act on it", () => {
  // Behind on its base, a failed check to re-run, and read as ready (Merge not blocked): all four are offered.
  const actionable = pr({ behindBy: 2, display: "ready",
    checks: [{ name: "lint", status: "completed", conclusion: "failure" }] });
  const task = { owner: { id: "per_ana", name: "Ana Ribeiro" } } as Pick<TaskDetail, "owner">;
  const shownTo = async (people: { you: string | null; me: { role: string } | null }, on: Pick<TaskDetail, "owner"> = task) => {
    const { mount } = await import("./dom.ts");
    return mount(
      <PullRequestActions client={{} as ApiClient} pr={actionable} defaultMethod="squash" onChanged={() => {}}
        notAllowed={notYours(on, people as Pick<People, "you" | "me">)}>
        {({ facts, merge }) => <div>{facts.checks}{facts.base}{facts.reviews}{merge}</div>}
      </PullRequestActions>,
    );
  };

  test("a member who is neither the owner nor an admin sees the four off, saying who can", async () => {
    const { container, unmount } = await shownTo({ you: "per_bo", me: { role: "member" } });
    await expectFourRefused(container, "Only Ana, the owner, or an admin can do this");
    await unmount();
  });

  for (const [who, people] of [["the owner", { you: "per_ana", me: { role: "member" } }], ["an admin", { you: "per_cy", me: { role: "admin" } }]] as const) {
    test(`${who} sees the four on`, async () => {
      const { act, settle, until } = await import("./dom.ts");
      const { container, unmount } = await shownTo(people);
      expect(buttons(container).map((b) => b ? b.disabled : "missing")).toEqual([false, false, false, false]);
      // Focused past the tooltip's open delay: no refusal, and no extra tab stop on the wrapper.
      // Merge keeps its own tooltip, saying what it does.
      for (const [i, b] of buttons(container).entries()) {
        const wrapper = b!.parentElement as HTMLElement;
        await act(async () => wrapper.focus());
        await settle(600);
        const tip = document.querySelector("[role=tooltip]");
        if (four[i] === "pr-merge") expect(tip?.textContent).toBe("Merges it on GitHub: squash and merge");
        else expect(tip ? `${four[i]}'s tooltip: "${tip.textContent}"` : null).toBeNull();
        expect(wrapper.tabIndex).toBe(-1);
        await act(async () => wrapper.blur());
        await until(() => (document.querySelector("[role=tooltip]") ? null : true), `${four[i]}'s tooltip gone`);
      }
      await unmount();
    });
  }

  test("while one action runs, the others are off, Request review too", async () => {
    const { click, mount } = await import("./dom.ts");
    const client = { updatePullRequestBranch: () => new Promise(() => {}) } as unknown as ApiClient;
    const { container, unmount } = await mount(
      <PullRequestActions client={client} pr={actionable} defaultMethod="squash" onChanged={() => {}}>
        {({ facts, merge }) => <div>{facts.checks}{facts.base}{facts.reviews}{merge}</div>}
      </PullRequestActions>,
    );
    await click(container.querySelector('[data-testid="pr-update-branch"]')!);
    expect(buttons(container).map((b) => b ? b.disabled : "missing")).toEqual([true, true, true, true]);
    await unmount();
  });

  test("on a blocked pull request, Merge's tooltip says who can, not what blocks it", async () => {
    const { act, mount, until } = await import("./dom.ts");
    const blocked = pr({ display: "ci_red", checkState: "failing", checks: [{ name: "lint", status: "completed", conclusion: "failure" }] });
    const { container, unmount } = await mount(
      <PullRequestActions client={{} as ApiClient} pr={blocked} defaultMethod="squash" onChanged={() => {}}
        notAllowed={notYours(task, { you: "per_bo", me: { role: "member" } } as Pick<People, "you" | "me">)}>
        {({ merge }) => <div>{merge}</div>}
      </PullRequestActions>,
    );
    const merge = container.querySelector<HTMLButtonElement>('[data-testid="pr-merge"]')!;
    expect(merge.disabled).toBe(true);
    await act(async () => (merge.parentElement as HTMLElement).focus());
    const tip = await until(() => document.querySelector("[role=tooltip]"), "Merge's tooltip");
    expect(tip.textContent).toBe("Only Ana, the owner, or an admin can do this");
    await unmount();
  });

  describe("a task with no owner", () => {
    const unowned = { owner: null } as Pick<TaskDetail, "owner">;

    test("a member sees the four off: only an admin can", async () => {
      const { container, unmount } = await shownTo({ you: "per_bo", me: { role: "member" } }, unowned);
      await expectFourRefused(container, "Only an admin can do this: the task has no owner");
      await unmount();
    });

    test("an admin sees the four on", async () => {
      const { container, unmount } = await shownTo({ you: "per_cy", me: { role: "admin" } }, unowned);
      expect(buttons(container).map((b) => b ? b.disabled : "missing")).toEqual([false, false, false, false]);
      await unmount();
    });
  });
});

describe("who may act on it, on the task page", () => {
  // The fixture's task, owned by Ana, with a pull request on which all four are offered.
  const page = async (role: "member" | "admin" | "unknown") => {
    const { mount, until } = await import("./dom.ts");
    const { TooltipProvider, ToastProvider } = await import("@dude/design-system/primitives");
    const { FixtureClient } = await import("../src/fixtures/client.ts");
    const { PULL_REQUEST, TASK_ID } = await import("../src/fixtures/data.ts");
    const { PeopleProvider } = await import("../src/people.tsx");
    const { TaskScreen } = await import("../src/screens/TaskScreen.tsx");
    const client = new FixtureClient("a");
    const base = await client.getTask(TASK_ID);
    const task: TaskDetail = { ...base, owner: { id: "u_ana", name: "Ana Ribeiro", photoUrl: null, online: false } };
    client.getTask = async () => task;
    client.listPullRequests = async () => ({ pullRequests: [{ ...PULL_REQUEST, behindBy: 2, display: "ready", checkState: "failing",
      checks: [{ name: "lint", status: "completed", conclusion: "failure" }] }] });
    const listed = await client.listPeople();
    let reads = 0;
    client.listPeople = role === "unknown" ? () => new Promise(() => {})
      : async () => {
        reads++;
        return { ...listed, people: listed.people.map((p) => (p.id === listed.you ? { ...p, role } : p)) };
      };
    const { container, unmount } = await mount(
      <TooltipProvider><ToastProvider><PeopleProvider client={client}>
        <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} />
      </PeopleProvider></ToastProvider></TooltipProvider>,
    );
    await until(() => container.querySelector('[data-testid="pr-merge"]'), "Merge on the task page");
    return { container, unmount, peopleRead: () => reads };
  };
  // The page keeps the tab picked per task.
  afterEach(() => localStorage.clear());

  test("a member who is not the owner sees the four off, saying who can", async () => {
    const { until } = await import("./dom.ts");
    const { container, unmount } = await page("member");
    await until(() => (buttons(container)[0]?.disabled ? true : null), "Merge off once people are read");
    await expectFourRefused(container, "Only Ana, the owner, or an admin can do this");
    await unmount();
  });

  test("an admin sees the four on", async () => {
    const { settle, until } = await import("./dom.ts");
    const { container, unmount, peopleRead } = await page("admin");
    // The four are on until people are read too: only after a read does "on" mean the admin may act.
    await until(() => peopleRead() > 0 || null, "people read");
    await settle(100);
    expect(buttons(container).map((b) => b ? b.disabled : "missing")).toEqual([false, false, false, false]);
    await unmount();
  });

  test("while people are being read, none is off for who may not be allowed", async () => {
    const { settle } = await import("./dom.ts");
    const { container, unmount } = await page("unknown");
    await settle(100);
    expect(buttons(container).map((b) => b ? b.disabled : "missing")).toEqual([false, false, false, false]);
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

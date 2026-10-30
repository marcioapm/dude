/**
 * The whole shell, from its URL: `App` mounted at a hash, against the
 * fixture client, so the hash is parsed and routed the way a browser's is.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { ThemeProvider } from "@dude/design-system";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, emitForTest, mount, settle, until } from "./dom.ts";
import { EventTypes, type SessionsList } from "@dude/domain";
import { FixtureClient, emit } from "../src/fixtures/client.ts";
import { EPIC, ORG, PROJECT, RUN_ID, TASK_ID, YOU } from "../src/fixtures/data.ts";
import { ApiError, type RunDetail } from "../src/api/client.ts";
import { App } from "../src/App.tsx";
import { PeopleProvider } from "../src/people.tsx";
import { draftKey } from "../src/hooks/useDraft.ts";
import { WELCOME_DRAFT } from "../src/screens/WelcomeScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  window.history.replaceState(null, "", " ");
  localStorage.clear();
});

async function app(hash: string, client: FixtureClient) {
  window.history.replaceState(null, "", hash);
  const { container, unmount } = await mount(
    <ThemeProvider>
      <TooltipProvider>
        <ToastProvider>
          <PeopleProvider client={client}>
            <App client={client} onSignOut={() => {}} onKeyRefused={() => {}} />
          </PeopleProvider>
        </ToastProvider>
      </TooltipProvider>
    </ThemeProvider>,
  );
  mounted.push(unmount);
  return container;
}

const selectedTab = (page: HTMLElement) => page.querySelector("[data-testid=task-screen] [role=tab][aria-selected=true]")?.textContent ?? "";

async function onServers(page: HTMLElement) {
  await until(() => (selectedTab(page).startsWith("Servers") ? true : null), "the Servers tab selected");
  const panel = await until(() => page.querySelector<HTMLElement>("[role=tabpanel][data-state=active]"), "the selected panel");
  await until(() => panel.querySelector("[data-testid=servers-panel]"), "the servers in the selected panel");
}

describe("a project's Servers page", () => {
  test("the board says Servers · N between New epic and Settings, and it opens the project's Servers page", async () => {
    const page = await app(`#/project/${PROJECT.id}`, new FixtureClient("a"));
    const button = await until(() => {
      const b = page.querySelector<HTMLElement>("[data-testid=servers-button]");
      return b?.textContent === "Servers · 6" ? b : null;
    }, "Servers · 6");
    const order = [...page.querySelectorAll<HTMLElement>("[data-testid=new-epic], [data-testid=servers-button], [data-testid=project-settings-button]")]
      .map((b) => b.dataset.testid);
    expect(order).toEqual(["new-epic", "servers-button", "project-settings-button"]);
    await click(button);
    expect(window.location.hash).toBe(`#/project/${PROJECT.id}/servers`);
    await until(() => page.querySelector("[data-testid=servers-page] [data-testid=server-group]"), "the page's groups");
    expect(document.title).toStartWith(`${PROJECT.name} · Servers`);
  });

  test("a branch lookup lands on the project's Servers hash inside its organization", async () => {
    const descriptor = Object.getOwnPropertyDescriptor(window, "opener");
    const preview = {};
    Object.defineProperty(window, "opener", { configurable: true, value: preview });
    const client = new FixtureClient("a");
    client.previewServerAt = async () => ({ kind: "branch", organizationId: ORG.id, organizationSlug: "acme", projectId: PROJECT.id,
      previewRunId: "run_pv_checkout", server: "web", branch: "feat/checkout-polish" });
    try {
      await app("#/preview-connect", client);
      await act(async () => window.dispatchEvent(new window.MessageEvent("message", { origin: "https://web.preview.test",
        source: preview as MessageEventSource, data: { type: "hello", v: 1, url: "https://web.preview.test/cart" } })));
      await until(() => window.location.hash === `#/o/acme/project/${PROJECT.id}/servers`, "the organization-prefixed Servers hash");
      expect(window.location.hash).toBe(`#/o/acme/project/${PROJECT.id}/servers`);
    } finally {
      if (descriptor) Object.defineProperty(window, "opener", descriptor);
      else delete (window as { opener?: unknown }).opener;
      sessionStorage.clear();
    }
  });

  test("the board count reads only on mount and this project's server lifecycle events, not tree reloads", async () => {
    class Counting extends FixtureClient {
      counts = 0;
      trees = 0;
      override async runningServers(id: string) { this.counts++; return super.runningServers(id); }
      override async navigation() { this.trees++; return super.navigation(); }
    }
    const client = new Counting("a");
    const page = await app(`#/project/${PROJECT.id}`, client);
    await until(() => page.querySelector("[data-testid=servers-button]")?.textContent === "Servers · 6", "the count");
    const initial = client.counts;
    const tree = client.trees;
    await emitForTest(EventTypes.TaskUpdated);
    await until(() => client.trees > tree, "an unrelated tree reload");
    await settle(500);
    expect(client.counts).toBe(initial);
    await act(async () => emit({ eventType: EventTypes.ServersChanged, occurredAt: new Date().toISOString(), organizationId: ORG.id,
      projectId: "prj_other", taskId: null, runId: null, sessionId: null, workflowRunId: null, actor: { type: "system", id: "dude" },
      source: "control-plane", correlationId: null, causationId: null, payload: {} }));
    await settle(500);
    expect(client.counts).toBe(initial);
    for (const eventType of [EventTypes.ServersChanged, EventTypes.RunCreated, EventTypes.RunCompleted, EventTypes.RunFailed, EventTypes.RunAborted]) {
      const before = client.counts;
      await emitForTest(eventType);
      await until(() => client.counts > before, eventType);
      await settle(500);
      expect(client.counts - before).toBe(1);
    }
  }, 15_000);

  test("while the count is unknown the button says Servers", async () => {
    const client = new FixtureClient("a");
    client.runningServers = () => new Promise(() => {});
    const page = await app(`#/project/${PROJECT.id}`, client);
    const button = await until(() => page.querySelector<HTMLElement>("[data-testid=servers-button]"), "the button");
    expect(button.textContent).toBe("Servers");
  });
});

describe("Waiting routes and sidebar", () => {
  test("the others deep link selects its tab; the sidebar has no others row and its Waiting on you stays current", async () => {
    const page = await app("#/waiting/others", new FixtureClient("a"));
    const inbox = await until(() => page.querySelector<HTMLElement>("[data-testid=inbox]"), "Waiting");
    await until(() => page.querySelector("[data-testid=waiting-on-you]"), "the waiting sidebar link");
    expect(inbox.querySelector("[role=tab][aria-selected=true]")?.textContent).toStartWith("On others");
    expect(page.querySelectorAll("[data-testid=waiting-on-others]").length).toBe(0);
    expect(page.querySelector("[data-testid=waiting-on-you]")?.getAttribute("aria-current")).toBe("page");
    await act(async () => inbox.querySelector<HTMLElement>("[role=tab]")!.focus());
    expect(window.location.hash).toBe("#/waiting");
    expect(page.querySelector("[data-testid=waiting-on-you]")?.getAttribute("aria-current")).toBe("page");
    window.history.replaceState(null, "", "#/waiting/others");
    await act(async () => window.dispatchEvent(new HashChangeEvent("hashchange")));
    expect(inbox.querySelector("[role=tab][aria-selected=true]")?.textContent).toStartWith("On others");
    window.history.replaceState(null, "", "#/waiting");
    await act(async () => window.dispatchEvent(new HashChangeEvent("hashchange")));
    expect(inbox.querySelector("[role=tab][aria-selected=true]")?.textContent).toStartWith("On you");
  });

  test("Waiting with no projects still lists a real session question", async () => {
    const client = new FixtureClient("a");
    const navigation = client.navigation.bind(client);
    client.navigation = async () => ({ ...await navigation(), projects: [] });
    client.sessions = async () => ({ sessions: [], invitations: [], questions: [{ id: "question", sessionId: "private", title: null, prompt: "Choose", options: [], askedAt: new Date().toISOString() }] });
    const page = await app("#/waiting", client);
    await until(() => page.querySelector("[data-testid=session-question]"), "the session question without projects");
    expect(page.querySelector("[data-testid=inbox]")).not.toBeNull();
  });
});

describe("an epic's page reads its open waits cheaply", () => {
  test("the Board tab counts waits without loading threads; Coordination loads them once a reload", async () => {
    localStorage.setItem("dude.fixtures.run", "peers");
    const client = new FixtureClient("a");
    const calls = { threads: 0, waiting: 0 };
    const threads = client.epicThreads.bind(client);
    const waiting = client.epicWaiting.bind(client);
    client.epicThreads = (id) => { calls.threads++; return threads(id); };
    client.epicWaiting = (id) => { calls.waiting++; return waiting(id); };
    const { EPIC } = await import("../src/fixtures/data.ts");
    const page = await app(`#/epic/${EPIC.id}`, client);
    const count = await until(() => page.querySelector("[data-testid=epic-view] .runCount"), "the open-wait count");
    expect(count.textContent).toBe("1");
    await settle();
    expect(calls.threads).toBe(0);
    expect(calls.waiting).toBeGreaterThan(0);
    window.history.replaceState(null, "", `#/epic/${EPIC.id}/coordination`);
    window.dispatchEvent(new HashChangeEvent("hashchange"));
    await until(() => page.querySelector("[data-testid=coordination]"), "the Coordination view");
    await settle();
    expect(calls.threads).toBe(1);
  });
});

describe("epic archive navigation", () => {
  async function archiveClient(allEnded = false) {
    const client = new FixtureClient("a");
    let tree = await client.navigation();
    const endTasks = () => {
      tree = { projects: tree.projects.map((p) => ({ ...p, epics: p.epics?.map((e) => e.id === EPIC.id
        ? { ...e, tasks: e.tasks.map((t) => ({ ...t, status: "done" as const, runs: [] })) } : e) })) };
    };
    if (allEnded) endTasks();
    client.navigation = async () => tree;
    const overview = client.projectOverview.bind(client);
    client.projectOverview = async () => {
      const result = await overview();
      const live = tree.projects.find((p) => p.id === PROJECT.id)?.epics ?? [];
      return { ...result, epics: result.epics.filter((e) => live.some((l) => l.id === e.id)) };
    };
    client.archiveEpic = async (id, withEndedTasks = false) => {
      const epic = tree.projects.flatMap((p) => p.epics ?? []).find((e) => e.id === id)!;
      const ended = epic.tasks.filter((t) => t.status === "done");
      if (!withEndedTasks && ended.length !== epic.tasks.length) throw new ApiError(409, "not_ended", "Unfinished tasks remain.");
      const archived = ended.length === epic.tasks.length;
      tree = { projects: tree.projects.map((p) => ({ ...p, epics: p.epics?.filter((e) => e.id !== id || !archived)
        .map((e) => e.id === id ? { ...e, tasks: e.tasks.filter((t) => !ended.includes(t)) } : e) })) };
      return { archived, tasks: ended.map((t) => t.id) };
    };
    return { client, endTasks };
  }

  async function openArchive(page: HTMLElement) {
    await click(await until(() => page.querySelector("[data-testid=archive-epic-button]"), "the epic archive action"));
    return until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the archive dialog");
  }

  test("subset archive stays on the exact epic route and refreshes its unfinished tasks and count", async () => {
    const { client } = await archiveClient();
    const page = await app(`#/epic/${EPIC.id}`, client);
    const board = await until(() => page.querySelector<HTMLElement>(`[aria-label="${EPIC.title} board"]`), "the epic board");
    expect(board.textContent).toContain("Done1");
    expect(board.textContent).toContain("Cart summary: show tax breakdown");
    const dialog = await openArchive(page);
    await click(dialog.querySelector("[data-testid=archive-ended]")!);
    await until(() => document.querySelector("[role=dialog]") ? null : true, "the archive dialog closed");
    expect(window.location.hash).toBe(`#/epic/${EPIC.id}`);
    await until(() => board.textContent?.includes("Done0") ? true : null, "the refreshed ended-task count");
    expect(board.textContent).not.toContain("Cart summary: show tax breakdown");
    for (const title of ["Address form: autocomplete with Places", "Checkout v2: split payment step", "Payment step: Apple Pay fallback", "Order confirmation e-mail template", "Analytics: checkout funnel events"]) {
      expect(board.textContent).toContain(title);
    }
    expect(page.querySelector("[data-testid=epic-screen]")).not.toBeNull();
  });

  for (const allEnded of [true, false]) {
    test(`actual full archive redirects to the project and removes the epic (${allEnded ? "all ended" : "ended while dialog open"})`, async () => {
      const { client, endTasks } = await archiveClient(allEnded);
      const page = await app(`#/epic/${EPIC.id}`, client);
      const dialog = await openArchive(page);
      const archive = client.archiveEpic.bind(client);
      if (!allEnded) endTasks();
      const requests: boolean[] = [];
      client.archiveEpic = (id, withEndedTasks) => { requests.push(withEndedTasks ?? false); return archive(id, withEndedTasks); };
      await click(dialog.querySelector(`[data-testid=${allEnded ? "archive-confirm" : "archive-ended"}]`)!);
      await until(() => document.querySelector("[role=dialog]") ? null : true, "the archive dialog closed");
      expect(requests).toEqual([!allEnded]);
      expect(window.location.hash).toBe(`#/project/${PROJECT.id}`);
      await until(() => page.querySelector(`[aria-label="${PROJECT.name} board"]`), "the project board");
      expect(page.querySelectorAll("[data-testid=epic-screen]").length).toBe(0);
      await until(() => page.querySelector(`[data-epic="${EPIC.id}"]`) ? null : true, "the archived epic removed from navigation and project list");
      expect(page.querySelectorAll(`[data-epic="${EPIC.id}"]`).length).toBe(0);
    });
  }

  test("a rejected archive keeps the route, dialog, error and task count", async () => {
    const { client } = await archiveClient();
    client.archiveEpic = async () => { throw new ApiError(409, "conflict", "The epic changed. Try again."); };
    const page = await app(`#/epic/${EPIC.id}`, client);
    const dialog = await openArchive(page);
    await click(dialog.querySelector("[data-testid=archive-ended]")!);
    await until(() => dialog.textContent?.includes("The epic changed. Try again.") ? true : null, "the archive error");
    expect(window.location.hash).toBe(`#/epic/${EPIC.id}`);
    expect(document.querySelector("[role=dialog]")).toBe(dialog);
    expect(page.querySelector(`[aria-label="${EPIC.title} board"]`)?.textContent).toContain("Done1");
  });
});

describe("the shell's routes to a task's servers", () => {
  test("#/task/<id>/servers opens the task on its Servers tab", async () => {
    const page = await app(`#/task/${TASK_ID}/servers`, new FixtureClient("a"));
    await onServers(page);
  });

  test("#/task/<id> opens the task on its overview", async () => {
    const page = await app(`#/task/${TASK_ID}`, new FixtureClient("a"));
    await until(() => (selectedTab(page) === "Overview" ? true : null), "the Overview tab selected");
  });

  test("a standalone preview session's Servers → goes to #/task/<id>/servers", async () => {
    // A preview session whose task the shell could not learn: not in the
    // tree, and the shell's lookup of the Run found nothing. The session
    // itself still reads its Run, and through it its task.
    class Standalone extends FixtureClient {
      private lookups = 0;
      override navigation() {
        return super.navigation().then((n) => ({
          ...n,
          projects: n.projects.map((p) => ({
            ...p,
            tasks: p.tasks?.map((t) => ({ ...t, runs: [] })),
            epics: p.epics?.map((e) => ({ ...e, tasks: e.tasks.map((t) => ({ ...t, runs: [] })) })),
          })),
        }));
      }
      override async getRun(id: string): Promise<RunDetail> {
        if (this.lookups++ === 0) throw new ApiError(404, "not_found", "No such run.");
        return { ...(await super.getRun(id)), kind: "preview", phase: null, role: null };
      }
    }
    const page = await app(`#/session/${RUN_ID}`, new Standalone("e"));
    const button = await until(() => page.querySelector("[data-testid=preview-run-servers]"), "the standalone preview's way to its servers", 80);
    expect(page.querySelector("[data-testid=task-screen]") !== null).toBe(false);
    await click(button);
    expect(window.location.hash).toBe(`#/task/${TASK_ID}/servers`);
    await onServers(page);
  });
});

describe("settings pages", () => {
  test("yours has one heading, Your settings, and no You crumb above it; its sections keep their names", async () => {
    const page = await app("#/me/settings", new FixtureClient("a"));
    const screen = await until(() => page.querySelector<HTMLElement>("[data-testid=my-settings]"), "your settings");
    await until(() => screen.querySelector("[data-testid=profile]"), "the profile");
    expect([...screen.querySelectorAll("h1")].map((h) => h.textContent)).toEqual(["Your settings"]);
    expect(screen.querySelectorAll("nav[aria-label=Breadcrumb]").length).toBe(0);
    expect(document.title).toBe("Your settings — dude");
    expect(screen.querySelector("[data-testid=profile]")!.textContent).toContain("Organisation admin");
  });

  test("the organisation's tab title is spelt as its page is", async () => {
    await app("#/org/settings", new FixtureClient("a"));
    await until(() => (document.title === "Organisation settings — dude" ? true : null), "the tab title");
  });

  test("a role's page has the wide column; another page does not", async () => {
    const column = async (hash: string) => {
      const page = await app(hash, new FixtureClient("a"));
      const main = await until(() => page.querySelector<HTMLElement>("[data-testid=org-settings] [data-settings-main]"), `the column at ${hash}`);
      const wide = main.getAttribute("data-wide");
      for (const unmount of mounted) await unmount();
      mounted = [];
      return wide;
    };
    expect(await column("#/org/settings/implementer")).toBe("true");
    expect(await column("#/org/settings/delivery")).toBeNull();
  });
});

describe("the sidebar's profile band", () => {
  test("signed in by key, the band opens Your settings · Organisation settings · Help · Sign out, and each goes there", async () => {
    let signedOut = 0;
    window.history.replaceState(null, "", `#/project/${PROJECT.id}`);
    const client = new FixtureClient("a");
    const { container: page, unmount } = await mount(
      <ThemeProvider><TooltipProvider><ToastProvider><PeopleProvider client={client}>
        <App client={client} onSignOut={() => signedOut++} onKeyRefused={() => {}} />
      </PeopleProvider></ToastProvider></TooltipProvider></ThemeProvider>,
    );
    mounted.push(unmount);
    const band = await until(() => page.querySelector<HTMLElement>("[data-testid=my-settings-button]"), "the profile band");
    await until(() => (band.textContent?.includes("Márcio") ? true : null), "who you are");
    expect(band.getAttribute("aria-label")).toBe("Márcio Martins: your settings, organisation settings, help, sign out");
    const openMenu = () => act(async () => void band.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    await openMenu();
    expect([...document.querySelectorAll("[role=menu] [role=menuitem]")].map((i) => i.getAttribute("data-testid"))).toEqual([
      "rowmenu-settings", "rowmenu-org-settings", "rowmenu-help", "rowmenu-signout",
    ]);
    for (const [id, hash] of [["org-settings", "#/org/settings"], ["help", "#/help"], ["settings", "#/me"]] as const) {
      if (!document.querySelector("[role=menu]")) await openMenu();
      await click(await until(() => document.querySelector<HTMLElement>(`[data-testid=rowmenu-${id}]`), id));
      expect(window.location.hash).toStartWith(hash);
    }
    await openMenu();
    await click(await until(() => document.querySelector<HTMLElement>("[data-testid=rowmenu-signout]"), "Sign out"));
    expect(signedOut).toBe(1);
    // Neither is a row of the band any more.
    expect(page.querySelectorAll("[data-testid=help-button], [data-testid=org-settings-button]").length).toBe(0);
  });
});

describe("the sidebar's room", () => {
  const at = (m: number) => new Date(Date.now() - m * 60_000).toISOString();
  class FiveSessions extends FixtureClient {
    override sessions(): Promise<SessionsList> {
      const s = (id: string, minutes: number) => ({ id, title: id, role: "owner" as const, createdAt: at(9999),
        owner: { id: YOU, name: "Márcio Martins", photoUrl: null, online: true }, shared: false, projects: [], runStatus: null, dudePause: null,
        filed: 0, lastActivityAt: at(minutes), archived: false });
      return Promise.resolve({ sessions: [s("one", 50), s("five", 1), s("three", 30), s("two", 40), s("four", 20)], invitations: [], questions: [] });
    }
  }
  const shown = (page: HTMLElement) => [...page.querySelectorAll("[data-testid=sidebar-sessions] [data-session]")].map((r) => r.getAttribute("data-session"));

  test("five sessions: the three latest by activity, newest first", async () => {
    const page = await app(`#/project/${PROJECT.id}`, new FiveSessions("a"));
    await until(() => (shown(page).length ? true : null), "the sessions");
    expect(shown(page)).toEqual(["five", "four", "three"]);
  });

  test("folding Sessions is kept across a reload", async () => {
    const page = await app(`#/project/${PROJECT.id}`, new FiveSessions("a"));
    await until(() => (shown(page).length ? true : null), "the sessions");
    await click(page.querySelector<HTMLElement>("[data-testid=sidebar-sessions] [aria-label='Fold sessions']")!);
    expect(shown(page)).toEqual([]);
    for (const unmount of mounted) await unmount();
    mounted = [];
    const again = await app(`#/project/${PROJECT.id}`, new FiveSessions("a"));
    await until(() => again.querySelector("[data-testid=sidebar-sessions] [aria-label='Unfold sessions']"), "folded after reload");
    await settle();
    expect(shown(again)).toEqual([]);
    await click(again.querySelector<HTMLElement>("[data-testid=sidebar-sessions] [aria-label='Unfold sessions']")!);
    expect(shown(again)).toEqual(["five", "four", "three"]);
  });

  test("the brand row's faces are the others online, not you", async () => {
    const page = await app(`#/project/${PROJECT.id}`, new FixtureClient("a"));
    const online = await until(() => page.querySelector("header [data-testid=online]"), "who is online");
    // The fixtures have you, Ana and Tom online.
    expect(online.getAttribute("aria-label")).toBe("Online: Ana Ribeiro, Tom Okafor");
  });
});

describe("the first load with no place", () => {
  /** A client whose tree arrives only when `arrive` is called. */
  class SlowTree extends FixtureClient {
    private held = Promise.withResolvers<void>();
    asked = Promise.withResolvers<void>();
    /** Called as the tree is handed to the app. */
    onArrival = () => {};
    arrive() {
      this.held.resolve();
    }
    override async navigation() {
      this.asked.resolve();
      await this.held.promise;
      const tree = await super.navigation();
      this.onArrival();
      return tree;
    }
  }

  test("shows the welcome, not a board, and leaves the URL as it was", async () => {
    const client = new SlowTree("a");
    const page = await app("", client);
    client.arrive();
    await until(() => page.querySelector("[data-testid=welcome]"), "the welcome");
    await settle(100);
    expect(page.querySelector("[aria-label$=' board']") === null).toBe(true);
    expect(window.location.hash).toBe("");
    expect(document.title).toBe("dude");
    expect(document.activeElement === page.querySelector("[data-testid=welcome] textarea")).toBe(true);
  });

  test("keeps a place the URL named after the app read it, before the tree arrived", async () => {
    const client = new SlowTree("a");
    await app("", client);
    await client.asked.promise;
    // As a link followed straight after the shell showed: the hash is set
    // and the tree arrives in the same task, so the first-load effect runs
    // before the hashchange (a timer) reaches the app's place.
    const named = `#/project/${PROJECT.id}/settings/reviewer`;
    let fired = false;
    let firedOnArrival = null as boolean | null;
    window.addEventListener("hashchange", () => (fired = true), { once: true });
    client.onArrival = () => (firedOnArrival = fired);
    await act(async () => {
      window.history.replaceState(null, "", named);
      client.arrive();
      for (let i = 0; i < 10; i++) await Promise.resolve();
    });
    // The race was reproduced, so the test is not vacuous: when the tree was
    // handed to the app, whose first-load effect runs on it before any timer,
    // the hashchange (a timer, in happy-dom) had not reached the app yet.
    expect(firedOnArrival).toBe(false);
    expect(window.location.hash).toBe(named);
    await settle(100);
    expect(window.location.hash).toBe(named);
  });
});

describe("home", () => {
  test("the sidebar's brand is a button named for its words and home, that goes to the welcome", async () => {
    const page = await app(`#/project/${PROJECT.id}`, new FixtureClient("a"));
    await until(() => page.querySelector("[aria-label$=' board']"), "the board");
    const brand = page.querySelector<HTMLButtonElement>("[data-testid=brand-home]")!;
    expect(brand.tagName).toBe("BUTTON");
    expect(brand.getAttribute("aria-label")).toBe("El Duderino, home");
    await click(brand);
    expect(window.location.hash).toBe("#/");
    await until(() => page.querySelector("[data-testid=welcome]"), "the welcome");
  });

  test("the sidebar's New session goes to the welcome and makes nothing", async () => {
    class Counting extends FixtureClient {
      made = 0;
      override createSession() {
        this.made++;
        return super.createSession();
      }
    }
    const client = new Counting("a");
    const page = await app(`#/project/${PROJECT.id}`, client);
    await click(await until(() => page.querySelector("[data-testid=sidebar-sessions] [data-testid=new-session]"), "New session"));
    await until(() => page.querySelector("[data-testid=welcome]"), "the welcome");
    expect(client.made).toBe(0);
  });

  test("an untitled session in the sidebar reads Untitled session, apart from the New session action", async () => {
    class OneUntitled extends FixtureClient {
      override sessions(): Promise<SessionsList> {
        return Promise.resolve({
          sessions: [{ id: "bs_untitled", title: null, role: "owner", createdAt: new Date().toISOString(), owner: { id: YOU, name: "Márcio Martins", photoUrl: null, online: false },
            shared: false, projects: [], runStatus: null, dudePause: null, filed: 0, lastActivityAt: new Date().toISOString(), archived: false }],
          invitations: [], questions: [],
        });
      }
    }
    const page = await app(`#/project/${PROJECT.id}`, new OneUntitled("a"));
    const row = await until(() => page.querySelector<HTMLElement>("[data-testid=sidebar-sessions] [data-session=bs_untitled]"), "the untitled row");
    expect(row.textContent).toBe("Untitled session");
    const action = page.querySelector<HTMLElement>("[data-testid=sidebar-sessions] [data-testid=new-session]")!;
    expect(action.getAttribute("aria-label")).toBe("New session");
  });

  test("an organisation with no projects gets the welcome, with New project in it for an admin", async () => {
    class Empty extends FixtureClient {
      override navigation() {
        return super.navigation().then((n) => ({ ...n, projects: [] }));
      }
    }
    const page = await app("#/", new Empty("a"));
    const offer = await until(() => page.querySelector("[data-testid=welcome] [data-testid=new-project-empty]"), "New project in the welcome");
    expect(page.querySelector("[data-testid=welcome] textarea") !== null).toBe(true);
    expect(page.querySelector("main")!.textContent).not.toContain("No projects yet");
    await click(offer);
    await until(() => page.ownerDocument.querySelector("[data-testid=project-name]"), "the new-project dialog");
  });

  test("an organisation with no projects offers a member, who may not make one, the welcome alone", async () => {
    class EmptyForAMember extends FixtureClient {
      override navigation() {
        return super.navigation().then((n) => ({ ...n, projects: [] }));
      }
      override listPeople() {
        return super.listPeople().then((l) => ({ ...l, people: l.people.map((p) => (p.id === l.you ? { ...p, role: "member" as const } : p)) }));
      }
    }
    const page = await app("#/", new EmptyForAMember("a"));
    await until(() => page.querySelector("[data-testid=welcome]"), "the welcome");
    // Both reads in: the tree is empty and the profile band knows who you are.
    await until(() => (page.querySelector("aside")?.textContent?.includes("No projects yet") ? true : null), "the empty tree");
    await until(() => page.querySelector("[data-testid=my-settings-button]")?.textContent?.includes("Márcio") ? true : null, "who you are");
    await settle(50);
    expect(page.querySelector("[data-testid=new-project-empty]") === null).toBe(true);
  });

  test("while the projects load, the welcome offers no New project", async () => {
    class Never extends FixtureClient {
      override navigation(): never {
        return new Promise(() => undefined) as never;
      }
    }
    const page = await app("#/", new Never("a"));
    await until(() => page.querySelector("[data-testid=welcome]"), "the welcome");
    await settle(50);
    expect(page.querySelector("[data-testid=new-project-empty]") === null).toBe(true);
  });

  test("a send from the welcome opens the new session and reads the sessions list again", async () => {
    class Making extends FixtureClient {
      reads = 0;
      override sessions() {
        this.reads++;
        return super.sessions();
      }
      override async createSession() {
        return { id: "ssn_new", title: null, runId: "run_new" };
      }
    }
    const client = new Making("a");
    const page = await app("#/", client);
    const area = await until(() => page.querySelector<HTMLTextAreaElement>("[data-testid=welcome] textarea"), "the composer");
    await until(() => (client.reads >= 1 ? true : null), "the first read");
    await settle(100);
    const before = client.reads;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, "plan the meter");
      area.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => void area.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await until(() => (window.location.hash === "#/sessions/ssn_new" ? true : null), "the new session's place");
    // The fixture's stream sends no session event here, so every read after the send is the app's own.
    await settle(300);
    expect(client.reads).toBe(before + 1);
  });

  class Held extends FixtureClient {
    hold = Promise.withResolvers<void>();
    override async createSession() {
      await this.hold.promise;
      return { id: "ssn_late", title: null, runId: "run_late" };
    }
  }

  async function sendFromWelcome(page: HTMLElement, text: string) {
    const composer = await until(() => page.querySelector<HTMLTextAreaElement>("[data-testid=welcome] textarea"), "the welcome's composer");
    await settle();
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(composer, text);
      composer.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => void composer.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
  }

  test("a session made from the welcome opens", async () => {
    const client = new Held("a");
    const page = await app("#/", client);
    client.hold.resolve();
    await sendFromWelcome(page, "plan the meter");
    await until(() => (window.location.hash === "#/sessions/ssn_late" ? true : null), "the session opened");
  });

  test("a create that lands after the welcome was left does not move the person, and its draft is removed", async () => {
    const client = new Held("a");
    const page = await app("#/", client);
    await sendFromWelcome(page, "plan the meter");
    const away = `#/project/${PROJECT.id}`;
    await act(async () => {
      window.location.hash = away;
    });
    await until(() => (page.querySelector("[data-testid=welcome]") ? null : true), "the welcome left");
    // The unmount flush stored the sent words; the late create must remove them.
    expect(JSON.parse(localStorage.getItem(draftKey(YOU, WELCOME_DRAFT))!).text).toBe("plan the meter");
    await act(async () => client.hold.resolve());
    await settle(50);
    expect(window.location.hash).toBe(away);
    expect(localStorage.getItem(draftKey(YOU, WELCOME_DRAFT))).toBeNull();
  });
});

describe("the welcome's model picker", () => {
  test("is read once across visits, and again after the organisation's settings or tiers change", async () => {
    class Counting extends FixtureClient {
      reads = 0;
      override modelTiers() {
        this.reads++;
        return super.modelTiers();
      }
    }
    const client = new Counting("a");
    const page = await app("#/", client);
    const picker = () => page.querySelector("[data-testid=welcome] [data-testid=model-picker]");
    const revisit = async () => {
      await act(async () => {
        window.location.hash = `#/project/${PROJECT.id}`;
      });
      await until(() => page.querySelector("[aria-label$=' board']"), "the board");
      await click(page.querySelector("[data-testid=brand-home]")!);
      await until(picker, "the picker again");
    };
    await until(picker, "the picker");
    await revisit();
    expect(client.reads).toBe(1);
    await emitForTest(EventTypes.SettingsUpdated, { scope: "organization", changed: { modelTiers: {} } });
    await revisit();
    expect(client.reads).toBe(2);
  });

  test("is read again after the stream comes back: a settings change it missed is never replayed", async () => {
    class Counting extends FixtureClient {
      reads = 0;
      override modelTiers() {
        this.reads++;
        return super.modelTiers();
      }
    }
    const client = new Counting("a");
    const page = await app("#/", client);
    const picker = () => page.querySelector("[data-testid=welcome] [data-testid=model-picker]");
    await until(picker, "the picker");
    expect(client.reads).toBe(1);
    // The network back: every stream reopens, reconnecting until it is live.
    await act(async () => void window.dispatchEvent(new Event("online")));
    await until(() => (client.reads === 2 ? true : null), "the tiers read again");
  });
});

describe("the sidebar's rail", () => {
  afterEach(() => localStorage.removeItem("dude.sidebar"));
  const key = (target: EventTarget, init: KeyboardEventInit = {}) =>
    act(async () => void target.dispatchEvent(new KeyboardEvent("keydown", { key: "[", bubbles: true, cancelable: true, ...init })));

  test("[ folds the sidebar to its rail and back, and the choice is kept", async () => {
    const page = await app(`#/project/${PROJECT.id}`, new FixtureClient("a"));
    await until(() => page.querySelector("[aria-label$=' board']"), "the board");
    expect(page.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    await key(document.body);
    expect(page.querySelector("[data-testid=sidebar-rail]") !== null).toBe(true);
    expect(localStorage.getItem("dude.sidebar")).toBe("rail");
    await key(document.body);
    expect(page.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    expect(localStorage.getItem("dude.sidebar")).toBe("full");
  });

  test("folding with the profile menu open closes it, by [ and by the chevron, and it stays closed unfolded", async () => {
    const page = await app(`#/project/${PROJECT.id}`, new FixtureClient("a"));
    const band = () => page.querySelector<HTMLElement>("[data-testid=my-settings-button]");
    await until(() => (band()?.textContent?.includes("Márcio") ? true : null), "who you are");
    const openMenu = () =>
      act(async () => void band()!.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    const menu = () => document.querySelector("[role=menu]");
    // Unfolded, the band is back; its menu's state is on the trigger at once, and
    // a menu that came back open would be in the page once the render settles.
    const closedOnUnfold = async () => {
      expect(page.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
      expect((await until(band, "the band")).getAttribute("aria-expanded")).toBe("false");
      await settle();
      expect(menu()).toBeNull();
    };
    // [ folds to the rail and [ unfolds it: the menu does not follow, nor come back.
    await openMenu();
    await until(menu, "the menu");
    await key(document.body);
    expect(page.querySelector("[data-testid=sidebar-rail]") !== null).toBe(true);
    expect(menu()).toBeNull();
    await key(document.body);
    await closedOnUnfold();
    // The sidebar's own chevron folds it too, and the rail's chevron unfolds it.
    await openMenu();
    await until(menu, "the menu again");
    await click(page.querySelector("[data-testid=sidebar-collapse]")!);
    expect(page.querySelector("[data-testid=sidebar-rail]") !== null).toBe(true);
    expect(menu()).toBeNull();
    await click(page.querySelector("[data-testid=rail-expand]")!);
    await closedOnUnfold();
  });

  test("a reload keeps the rail; a project's face opens its board; the chevron expands it", async () => {
    localStorage.setItem("dude.sidebar", "rail");
    const page = await app("", new FixtureClient("a"));
    const face = await until(() => page.querySelector<HTMLElement>(`[data-testid=rail-project][data-project="${PROJECT.id}"]`), "the project's face");
    await click(face);
    expect(window.location.hash).toBe(`#/project/${PROJECT.id}`);
    expect(face.getAttribute("aria-current")).toBe("page");
    await click(page.querySelector("[data-testid=rail-expand]")!);
    expect(page.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    expect(localStorage.getItem("dude.sidebar")).toBe("full");
  });

  test("[ in a field is a character, and with ⌘ or Ctrl held it is not the shortcut", async () => {
    const page = await app("", new FixtureClient("a"));
    const field = await until(() => page.querySelector<HTMLTextAreaElement>("[data-testid=welcome] textarea"), "the composer");
    await key(field);
    const search = page.querySelector<HTMLInputElement>("input[type=search]")!;
    await key(search);
    await key(document.body, { metaKey: true });
    await key(document.body, { ctrlKey: true });
    expect(page.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    expect(localStorage.getItem("dude.sidebar")).toBeNull();
  });

  test("[ typed with Option (a Mac's pt/de layout) or AltGr (Ctrl+Alt) is the shortcut", async () => {
    const page = await app("", new FixtureClient("a"));
    await until(() => page.querySelector("[data-testid=welcome] textarea"), "the composer");
    await key(document.body, { altKey: true });
    expect(page.querySelector("[data-testid=sidebar-rail]") !== null).toBe(true);
    expect(localStorage.getItem("dude.sidebar")).toBe("rail");
    await key(document.body, { ctrlKey: true, altKey: true });
    expect(page.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    expect(localStorage.getItem("dude.sidebar")).toBe("full");
  });

  test("/ typed with Shift (Shift+7 on a pt/de layout) on the rail unfolds it with the search focused", async () => {
    localStorage.setItem("dude.sidebar", "rail");
    const page = await app(`#/project/${PROJECT.id}`, new FixtureClient("a"));
    await until(() => page.querySelector("[data-testid=sidebar-rail]"), "the rail");
    await key(document.body, { key: "/", shiftKey: true });
    expect(page.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    expect(localStorage.getItem("dude.sidebar")).toBe("full");
    expect(document.activeElement === page.querySelector("input[type=search]")).toBe(true);
  });

  test("[ in a contenteditable or on a select is not the shortcut", async () => {
    const page = await app("", new FixtureClient("a"));
    await until(() => page.querySelector("[data-testid=welcome] textarea"), "the composer");
    const editable = document.createElement("div");
    editable.contentEditable = "true";
    const select = document.createElement("select");
    document.body.append(editable, select);
    try {
      await key(editable);
      await key(select);
      expect(page.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
      expect(localStorage.getItem("dude.sidebar")).toBeNull();
    } finally {
      editable.remove();
      select.remove();
    }
  });

  test("[ under 1000px, where there is no rail, changes nothing kept", async () => {
    const happyDOM = (window as unknown as { happyDOM: { setInnerWidth(w: number): void } }).happyDOM;
    const width = window.innerWidth;
    happyDOM.setInnerWidth(800);
    try {
      const page = await app(`#/project/${PROJECT.id}`, new FixtureClient("a"));
      await until(() => page.querySelector("[aria-label$=' board']"), "the board");
      await key(document.body);
      expect(localStorage.getItem("dude.sidebar")).toBeNull();
    } finally {
      happyDOM.setInnerWidth(width);
    }
  });

  test("[ and / under 1000px leave a kept rail choice as it was", async () => {
    const happyDOM = (window as unknown as { happyDOM: { setInnerWidth(w: number): void } }).happyDOM;
    const width = window.innerWidth;
    happyDOM.setInnerWidth(800);
    localStorage.setItem("dude.sidebar", "rail");
    try {
      const page = await app(`#/project/${PROJECT.id}`, new FixtureClient("a"));
      await until(() => page.querySelector("[aria-label$=' board']"), "the board");
      await key(document.body);
      await key(document.body, { key: "/" });
      expect(localStorage.getItem("dude.sidebar")).toBe("rail");
    } finally {
      happyDOM.setInnerWidth(width);
    }
  });

  test("/ on the rail unfolds it with the search focused; with the sidebar open it does nothing here", async () => {
    localStorage.setItem("dude.sidebar", "rail");
    const page = await app(`#/project/${PROJECT.id}`, new FixtureClient("a"));
    await until(() => page.querySelector("[data-testid=sidebar-rail]"), "the rail");
    await key(document.body, { key: "/" });
    expect(page.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    expect(localStorage.getItem("dude.sidebar")).toBe("full");
    expect(document.activeElement === page.querySelector("input[type=search]")).toBe(true);
    (document.activeElement as HTMLElement).blur();
    localStorage.removeItem("dude.sidebar");
    await key(document.body, { key: "/" });
    expect(page.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    expect(localStorage.getItem("dude.sidebar")).toBeNull();
  });

  test("/ on the rail, in a field or with a modifier, is not the shortcut", async () => {
    localStorage.setItem("dude.sidebar", "rail");
    const page = await app("", new FixtureClient("a"));
    const field = await until(() => page.querySelector<HTMLTextAreaElement>("[data-testid=welcome] textarea"), "the composer");
    await key(field, { key: "/" });
    await key(document.body, { key: "/", metaKey: true });
    await key(document.body, { key: "/", ctrlKey: true });
    expect(page.querySelector("[data-testid=sidebar-rail]") !== null).toBe(true);
    expect(localStorage.getItem("dude.sidebar")).toBe("rail");
  });

  test("after / unfolds the rail, [ twice folds and unfolds it without taking the focus to the search", async () => {
    localStorage.setItem("dude.sidebar", "rail");
    const page = await app(`#/project/${PROJECT.id}`, new FixtureClient("a"));
    await until(() => page.querySelector("[data-testid=sidebar-rail]"), "the rail");
    await key(document.body, { key: "/" });
    (document.activeElement as HTMLElement).blur();
    await key(document.body);
    await key(document.body);
    expect(page.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    expect(document.activeElement === page.querySelector("input[type=search]")).toBe(false);
  });

  test("the rail's New session, Sessions and Waiting on you go where the sidebar's do", async () => {
    localStorage.setItem("dude.sidebar", "rail");
    const page = await app(`#/project/${PROJECT.id}`, new FixtureClient("a"));
    await click(await until(() => page.querySelector("[data-testid=rail-sessions]"), "Sessions"));
    expect(window.location.hash).toBe("#/sessions");
    await click(page.querySelector("[data-testid=rail-new-session]")!);
    expect(window.location.hash).toBe("#/");
    await click(page.querySelector("[data-testid=rail-waiting]")!);
    expect(window.location.hash).toBe("#/waiting");
    await click(page.querySelector("[data-testid=rail-home]")!);
    expect(window.location.hash).toBe("#/");
  });
});

describe("the sessions list in a background tab", () => {
  test("is not read while the page is hidden, and is read once when it is shown again", async () => {
    let hidden = false;
    const was = Object.getOwnPropertyDescriptor(Document.prototype, "hidden");
    Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
    try {
      class Counting extends FixtureClient {
        reads = 0;
        override sessions() {
          this.reads++;
          return super.sessions();
        }
      }
      const client = new Counting("a");
      await app(`#/project/${PROJECT.id}`, client);
      await until(() => (client.reads >= 1 ? true : null), "the first read");
      const first = client.reads;
      hidden = true;
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"));
      });
      await settle(50);
      expect(client.reads).toBe(first);
      hidden = false;
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"));
      });
      await settle(50);
      expect(client.reads).toBe(first + 1);
    } finally {
      delete (document as unknown as Record<string, unknown>).hidden;
      if (was) Object.defineProperty(Document.prototype, "hidden", was);
    }
  });
});

describe("navigation and an automation run's activity", () => {
  test("a run's log lines, cut marker, exit, deliveries and end do not reload the tree; a task's change does", async () => {
    class Counting extends FixtureClient {
      reads = 0;
      override navigation() {
        this.reads++;
        return super.navigation();
      }
    }
    const client = new Counting("a");
    const page = await app(`#/project/${PROJECT.id}`, client);
    await until(() => page.querySelector("[aria-label$=' board']"), "the board");
    // The first read and any the mount's own events caused have landed.
    await settle(600);
    const first = client.reads;
    expect(first).toBeGreaterThan(0);
    const { ORG } = await import("../src/fixtures/data.ts");
    const { emit } = await import("../src/fixtures/client.ts");
    const automation = (eventType: string, payload: Record<string, unknown>) => emit({ eventType, occurredAt: new Date().toISOString(),
      organizationId: ORG.id, projectId: PROJECT.id, taskId: null, runId: null, sessionId: null, workflowRunId: null, automationRunId: "arn_quiet",
      actor: { type: "system", id: "arn_quiet" }, source: "runner", correlationId: null, causationId: null, payload });
    await act(async () => {
      for (let i = 0; i < 20; i++) automation("automation.log.line", { stream: "stdout", text: `line ${i}` });
      automation("automation.log.cut", { lines: 2000 });
      automation("automation.log.exited", { exitCode: 1 });
      automation("automation.delivery.sent", { deliveryId: "dlv_q", source: "incoming", summary: "resolved" });
      automation("automation.run.ended", { status: "failed", reason: "exit 1" });
      automation("automation.run.queued", { automationId: "aut_q", automationRunId: "arn_quiet" });
      automation("automation.run.started", { automationId: "aut_q", automationRunId: "arn_quiet" });
    });
    // Past the 400 ms reload delay.
    await settle(900);
    expect(client.reads).toBe(first);
    await emitForTest(EventTypes.TaskUpdated, { changed: { title: "x" } });
    await settle(900);
    expect(client.reads).toBe(first + 1);
  });
});

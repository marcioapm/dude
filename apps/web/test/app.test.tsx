/**
 * The whole shell, from its URL: `App` mounted at a hash, against the
 * fixture client, so the hash is parsed and routed the way a browser's is.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT, RUN_ID, TASK_ID, YOU } from "../src/fixtures/data.ts";
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
    <TooltipProvider>
      <ToastProvider>
        <PeopleProvider client={client}>
          <App client={client} onSignOut={() => {}} onKeyRefused={() => {}} />
        </PeopleProvider>
      </ToastProvider>
    </TooltipProvider>,
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

describe("the first load with no place", () => {
  /** A client whose tree arrives only when `arrive` is called. */
  class SlowTree extends FixtureClient {
    private gate = Promise.withResolvers<void>();
    asked = Promise.withResolvers<void>();
    /** Called as the tree is handed to the app. */
    onArrival = () => {};
    arrive() {
      this.gate.resolve();
    }
    override async navigation() {
      this.asked.resolve();
      await this.gate.promise;
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
      override createSession(): Promise<never> {
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

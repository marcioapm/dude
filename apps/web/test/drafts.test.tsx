/**
 * A composer's unsent words, kept in this browser per person and place:
 * saved after 2 s idle, at once on leaving, restored on coming back, and
 * gone once the message is sent. Driven through a brainstorm session's
 * page, a Run, a task's Chat and the welcome's first message.
 */

import { afterEach, beforeEach, describe, expect, jest, test } from "bun:test";
import { useState } from "react";
import type { Member, SessionDetail } from "@dude/domain";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE, RUN_ID, TASK_ID, YOU } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { SessionScreen } from "../src/screens/SessionScreen.tsx";
import { RunScreen } from "../src/screens/RunScreen.tsx";
import { ChatSection } from "../src/screens/ChatSection.tsx";
import { EndedLedgers } from "../src/screens/endedLedgers.ts";
import { WELCOME_DRAFT, WelcomeScreen } from "../src/screens/WelcomeScreen.tsx";
import { DRAFT_MAX_AGE_MS, draftKey, pruneDrafts, readDraft } from "../src/hooks/useDraft.ts";

const SESSION = "ssn_drafts";
const RUN = "run_drafts";
const ANA = PEOPLE.find((p) => p.id !== YOU)!;
const ME = PEOPLE.find((p) => p.id === YOU)!;
const KEY = draftKey(YOU, `session:${SESSION}`);
const stored = (key = KEY) => localStorage.getItem(key);

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  jest.useRealTimers();
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

function detail(): SessionDetail {
  const ref = (p: { id: string; name: string }) => ({ id: p.id, name: p.name, photoUrl: null, online: true });
  return {
    session: {
      id: SESSION, title: "Drafts", titledBy: "person", createdAt: new Date().toISOString(),
      people: [{ person: ref(ME), role: "owner", accepted: true, becomesOwner: false, open: true },
        { person: ref(ANA), role: "chat", accepted: true, becomesOwner: false, open: false }],
      projects: [], run: null, runs: [], costUsd: 0, messages: 1,
    },
    you: { id: YOU, role: "owner" },
    proposals: [],
    question: null,
  };
}

class DraftClient extends FixtureClient {
  constructor(private readonly you = YOU) {
    super("a");
  }
  sent: string[] = [];
  refuse = false;
  override listPeople(): Promise<{ people: Member[]; you: string }> {
    return Promise.resolve({ people: PEOPLE, you: this.you });
  }
  override getSession(): Promise<SessionDetail> {
    const d = detail();
    return Promise.resolve({ ...d, you: { ...d.you, id: this.you } });
  }
  override sessionChat(_id: string, text: string) {
    if (this.refuse) return Promise.reject(new Error("the backend is down"));
    this.sent.push(text);
    return Promise.resolve({ runId: RUN, created: true });
  }
}

// The session page, its composer found before the person is known.
async function open(client: DraftClient) {
  const { container, unmount } = await mount(
    <PeopleProvider client={client}>
      <ToastProvider>
        <SessionScreen client={client} sessionId={SESSION} projects={[]} onBack={() => {}} onChanged={() => {}} />
      </ToastProvider>
    </PeopleProvider>,
  );
  let gone = false;
  const close = async () => {
    if (gone) return;
    gone = true;
    await unmount();
  };
  mounted.push(close);
  const composer = await until(() => container.querySelector<HTMLTextAreaElement>("[data-testid=session-screen] textarea"), "the composer");
  return { container, composer, close };
}

async function page(client = new DraftClient()) {
  const opened = await open(client);
  // The person is known: the draft is keyed by them.
  await settle();
  return opened;
}

async function typeInto(composer: HTMLTextAreaElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(composer, text);
    composer.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function enter(composer: HTMLTextAreaElement) {
  await act(async () => {
    composer.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
  });
  await settle();
}

describe("a composer's draft", () => {
  test("is saved 2 s after the last change, not before", async () => {
    const { composer } = await page();
    jest.useFakeTimers();
    await typeInto(composer, "half a thought");
    jest.advanceTimersByTime(1500);
    await typeInto(composer, "half a thought, and more");
    jest.advanceTimersByTime(1999);
    expect(stored()).toBeNull();
    jest.advanceTimersByTime(1);
    const entry = JSON.parse(stored()!) as { text: string; savedAt: number };
    expect(entry.text).toBe("half a thought, and more");
    expect(typeof entry.savedAt).toBe("number");
  });

  test("is kept at once when the composer goes, inside the 2 s, and is there with the caret at its end on coming back", async () => {
    const first = await page();
    await typeInto(first.composer, "where does metering go?");
    expect(stored()).toBeNull();
    await first.close();
    expect(JSON.parse(stored()!).text).toBe("where does metering go?");

    const again = await page();
    expect(again.composer.value).toBe("where does metering go?");
    expect(again.composer.selectionStart).toBe("where does metering go?".length);
  });

  test("is kept at once when the page is hidden", async () => {
    const { composer } = await page();
    await typeInto(composer, "before the tab goes away");
    let hidden = true;
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (hidden ? "hidden" : "visible") });
    try {
      await act(async () => {
        document.dispatchEvent(new Event("visibilitychange"));
      });
      expect(JSON.parse(stored()!).text).toBe("before the tab goes away");
    } finally {
      hidden = false;
      delete (document as unknown as Record<string, unknown>).visibilityState;
    }
  });

  test("is kept at once when the page is left", async () => {
    const { composer } = await page();
    await typeInto(composer, "closing the tab");
    expect(stored()).toBeNull();
    await act(async () => {
      window.dispatchEvent(new Event("pagehide"));
    });
    expect(JSON.parse(stored()!).text).toBe("closing the tab");
  });

  test("words typed before the person is known are kept, and stored as theirs", async () => {
    localStorage.setItem(KEY, JSON.stringify({ text: "an older draft", savedAt: Date.now() }));
    let known: () => void = () => {};
    class SlowPeople extends DraftClient {
      override listPeople(): Promise<{ people: Member[]; you: string }> {
        return new Promise((resolve) => {
          known = () => resolve({ people: PEOPLE, you: YOU });
        });
      }
    }
    const { composer, close } = await open(new SlowPeople());
    await typeInto(composer, "typed during first paint");
    await act(async () => known());
    await settle();
    expect(composer.value).toBe("typed during first paint");
    await close();
    expect(JSON.parse(stored()!).text).toBe("typed during first paint");
  });

  test("is cleared once the message is sent", async () => {
    const client = new DraftClient();
    const { composer, close } = await page(client);
    await typeInto(composer, "send me");
    await close();
    expect(stored()).not.toBeNull();
    const again = await page(client);
    await enter(again.composer);
    expect(client.sent).toEqual(["send me"]);
    expect(again.composer.value).toBe("");
    expect(stored()).toBeNull();
    await again.close();
    expect(stored()).toBeNull();
  });

  test("stays when the send fails", async () => {
    const client = new DraftClient();
    client.refuse = true;
    const { container, composer, close } = await page(client);
    await typeInto(composer, "this one bounces");
    await enter(composer);
    await until(() => container.querySelector("[data-testid=session-problem]"), "the problem");
    expect(composer.value).toBe("this one bounces");
    await close();
    expect(JSON.parse(stored()!).text).toBe("this one bounces");
  });

  test("is the person's: someone else in the same browser does not see it", async () => {
    const mine = await page();
    await typeInto(mine.composer, "mine only");
    await mine.close();
    const theirs = await page(new DraftClient(ANA.id));
    expect(theirs.composer.value).toBe("");
    await typeInto(theirs.composer, "Ana's");
    await theirs.close();
    expect(JSON.parse(stored(draftKey(ANA.id, `session:${SESSION}`))!).text).toBe("Ana's");
    expect(JSON.parse(stored()!).text).toBe("mine only");
  });

  test("emptied, it is removed rather than stored blank", async () => {
    localStorage.setItem(KEY, JSON.stringify({ text: "old words", savedAt: Date.now() }));
    const { composer, close } = await page();
    expect(composer.value).toBe("old words");
    await typeInto(composer, "   ");
    await close();
    expect(stored()).toBeNull();
  });

  test("storage refusing every write does not stop the message going", async () => {
    // The object itself is replaced, on the global scope the code reads: happy-dom's
    // Storage does not reliably go through a patched prototype.
    const real = Object.getOwnPropertyDescriptor(globalThis, "localStorage")!;
    let refused = 0;
    const backing = new Map<string, string>();
    Object.defineProperty(globalThis, "localStorage", { configurable: true, value: {
      get length() { return backing.size; },
      key: (i: number) => [...backing.keys()][i] ?? null,
      getItem: (k: string) => backing.get(k) ?? null,
      removeItem: (k: string) => void backing.delete(k),
      clear: () => backing.clear(),
      setItem: () => {
        refused++;
        throw new DOMException("quota", "QuotaExceededError");
      },
    } });
    try {
      const client = new DraftClient();
      const { composer, close } = await page(client);
      await typeInto(composer, "still goes");
      await close();
      const again = await page(client);
      await typeInto(again.composer, "still goes");
      await enter(again.composer);
      expect(client.sent).toEqual(["still goes"]);
      expect(again.composer.value).toBe("");
      expect(refused).toBeGreaterThan(0);
    } finally {
      Object.defineProperty(globalThis, "localStorage", real);
    }
  });

  test("typing re-renders the composer alone, not the screen and its transcript", async () => {
    // Every render of SessionScreen reads `detail.session`: counting the reads counts its renders.
    let reads = 0;
    class CountingClient extends DraftClient {
      override getSession(): Promise<SessionDetail> {
        const d = detail();
        return Promise.resolve(Object.defineProperty({ ...d, session: undefined as unknown as SessionDetail["session"] }, "session", {
          enumerable: true,
          get: () => {
            reads++;
            return d.session;
          },
        }));
      }
    }
    const { composer } = await page(new CountingClient());
    expect(reads).toBeGreaterThan(0);
    reads = 0;
    for (const text of ["a", "ab", "abc"]) await typeInto(composer, text);
    expect(composer.value).toBe("abc");
    expect(reads).toBe(0);
  });
});

describe("a Run's steer composer", () => {
  const RUN_KEY = draftKey(YOU, `run:${RUN_ID}`);

  class SteerClient extends FixtureClient {
    steered: string[] = [];
    constructor() {
      super("a");
    }
    override steer(runId: string, text: string, options: { interrupt?: boolean; supersedes?: string } = {}) {
      this.steered.push(text);
      return super.steer(runId, text, options);
    }
  }

  async function runPage(client: FixtureClient) {
    const { container, unmount } = await mount(
      <PeopleProvider client={client}>
        <ToastProvider>
          <RunScreen client={client} runId={RUN_ID} onBack={() => {}} />
        </ToastProvider>
      </PeopleProvider>,
    );
    let gone = false;
    const close = async () => {
      if (gone) return;
      gone = true;
      await unmount();
    };
    mounted.push(close);
    const composer = await until(() => container.querySelector<HTMLTextAreaElement>("[data-testid=run-screen] textarea"), "the steer composer");
    await settle();
    return { composer, close };
  }

  test("keeps a half-written steer across leaving the Run and coming back", async () => {
    const client = new SteerClient();
    const first = await runPage(client);
    await typeInto(first.composer, "check the invoice gate first");
    await first.close();
    expect(JSON.parse(stored(RUN_KEY)!).text).toBe("check the invoice gate first");
    const again = await runPage(client);
    expect(again.composer.value).toBe("check the invoice gate first");
  });

  test("is cleared once the steer is sent", async () => {
    localStorage.setItem(RUN_KEY, JSON.stringify({ text: "steer this way", savedAt: Date.now() }));
    const client = new SteerClient();
    const { composer, close } = await runPage(client);
    expect(composer.value).toBe("steer this way");
    await enter(composer);
    await until(() => (client.steered.length > 0 && composer.value === "" ? true : null), "the steer sent");
    expect(client.steered).toEqual(["steer this way"]);
    expect(stored(RUN_KEY)).toBeNull();
    await close();
    expect(stored(RUN_KEY)).toBeNull();
  });
});

describe("a task's Chat", () => {
  test("words typed before the conductor exists are in the conductor's composer once it does", async () => {
    const client = new FixtureClient("a");
    const task = await client.getTask(TASK_ID);
    let conduct: (id: string | null) => void = () => {};
    function Harness() {
      const [conductorId, setConductorId] = useState<string | null>(null);
      conduct = setConductorId;
      return (
        <ChatSection client={client} task={task} conductorId={conductorId} ledgers={new EndedLedgers(client)} findings={[]} pullRequests={[]}
          events={[]} owner={{ owner: null }} version={0} onSent={() => {}} onOpenRun={() => {}} onBack={() => {}} />
      );
    }
    const { container, unmount } = await mount(
      <PeopleProvider client={client}>
        <ToastProvider>
          <Harness />
        </ToastProvider>
      </PeopleProvider>,
    );
    mounted.push(unmount);
    const before = await until(() => container.querySelector<HTMLTextAreaElement>("[data-testid=task-chat] textarea"), "the Chat's composer");
    await settle();
    await typeInto(before, "plan the invoice option");
    await act(async () => conduct(RUN_ID));
    const after = await until(() => container.querySelector<HTMLTextAreaElement>("[data-testid=chat-screen] textarea"), "the conductor's composer");
    await until(() => (after.value === "plan the invoice option" ? true : null), "the words carried over");
    expect(JSON.parse(stored(draftKey(YOU, `task:${TASK_ID}`))!).text).toBe("plan the invoice option");
  });
});

describe("the welcome's first message", () => {
  const WELCOME_KEY = draftKey(YOU, WELCOME_DRAFT);

  class Welcoming extends DraftClient {
    made: string[] = [];
    fail: Error | null = null;
    hold: PromiseWithResolvers<void> | null = null;
    override async createSession(input: { message?: string } = {}) {
      if (this.fail) throw this.fail;
      this.made.push(input.message ?? "");
      await this.hold?.promise;
      return { id: "ssn_new", title: null, runId: "run_new" };
    }
  }

  async function welcome(client: Welcoming, created: Array<[string, boolean]> = []) {
    const { container, unmount } = await mount(
      <PeopleProvider client={client}>
        <TooltipProvider>
          <ToastProvider>
            <WelcomeScreen client={client} projects={[]} sessions={[]} name={ME.name} onOpenSession={() => {}} onAllSessions={() => {}}
              onCreated={(id, stillHere) => created.push([id, stillHere])} />
          </ToastProvider>
        </TooltipProvider>
      </PeopleProvider>,
    );
    let gone = false;
    const close = async () => {
      if (gone) return;
      gone = true;
      await unmount();
    };
    mounted.push(close);
    const composer = await until(() => container.querySelector<HTMLTextAreaElement>("[data-testid=welcome] textarea"), "the welcome's composer");
    await settle();
    return { container, composer, close };
  }

  test("is there again after leaving the welcome and coming back", async () => {
    const client = new Welcoming();
    const first = await welcome(client);
    await typeInto(first.composer, "where does metering go?");
    await first.close();
    expect(JSON.parse(stored(WELCOME_KEY)!).text).toBe("where does metering go?");
    const again = await welcome(client);
    expect(again.composer.value).toBe("where does metering go?");
  });

  test("is cleared once the create is confirmed", async () => {
    localStorage.setItem(WELCOME_KEY, JSON.stringify({ text: "plan the meter", savedAt: Date.now() }));
    const client = new Welcoming();
    const created: Array<[string, boolean]> = [];
    const { composer, close } = await welcome(client, created);
    expect(composer.value).toBe("plan the meter");
    await enter(composer);
    expect(client.made).toEqual(["plan the meter"]);
    expect(created).toEqual([["ssn_new", true]]);
    expect(composer.value).toBe("");
    expect(stored(WELCOME_KEY)).toBeNull();
    await close();
    expect(stored(WELCOME_KEY)).toBeNull();
  });

  test("stays when the create fails", async () => {
    const client = new Welcoming();
    client.fail = new Error("the orchestrator is down");
    const created: Array<[string, boolean]> = [];
    const { container, composer, close } = await welcome(client, created);
    await typeInto(composer, "this one bounces");
    await enter(composer);
    await until(() => container.querySelector("[data-testid=welcome-problem]"), "the problem");
    expect(created).toEqual([]);
    expect(composer.value).toBe("this one bounces");
    await close();
    expect(JSON.parse(stored(WELCOME_KEY)!).text).toBe("this one bounces");
  });

  test("a starter's words are drafted like typed ones", async () => {
    const client = new Welcoming();
    const first = await welcome(client);
    await act(async () => {
      first.container.querySelector<HTMLElement>("[data-starter=task]")!.click();
    });
    await settle();
    expect(first.composer.value).toBe("Help me write a task for ");
    expect(stored(WELCOME_KEY)).toBeNull();
    await first.close();
    expect(JSON.parse(stored(WELCOME_KEY)!).text).toBe("Help me write a task for ");
    const again = await welcome(client);
    expect(again.composer.value).toBe("Help me write a task for ");
    expect(client.made).toEqual([]);
  });

  test("a create that lands after the welcome was left removes the sent words and tells the app it was left", async () => {
    const client = new Welcoming();
    client.hold = Promise.withResolvers<void>();
    const created: Array<[string, boolean]> = [];
    const first = await welcome(client, created);
    await typeInto(first.composer, "plan the meter  ");
    await enter(first.composer);
    await first.close();
    expect(JSON.parse(stored(WELCOME_KEY)!).text).toBe("plan the meter  ");
    await act(async () => client.hold!.resolve());
    await until(() => (created.length ? true : null), "the create answered");
    expect(created).toEqual([["ssn_new", false]]);
    expect(client.made).toEqual(["plan the meter"]);
    expect(stored(WELCOME_KEY)).toBeNull();
  });

  test("a create that lands after the welcome was left and written in again keeps the new words", async () => {
    const client = new Welcoming();
    client.hold = Promise.withResolvers<void>();
    const created: Array<[string, boolean]> = [];
    const first = await welcome(client, created);
    await typeInto(first.composer, "plan the meter");
    await enter(first.composer);
    await first.close();
    const again = await welcome(client, created);
    await typeInto(again.composer, "a second idea");
    await act(async () => {
      window.dispatchEvent(new Event("pagehide"));
    });
    await act(async () => client.hold!.resolve());
    await until(() => (created.length ? true : null), "the create answered");
    expect(created).toEqual([["ssn_new", false]]);
    expect(again.composer.value).toBe("a second idea");
    expect(JSON.parse(stored(WELCOME_KEY)!).text).toBe("a second idea");
  });
});

describe("old drafts", () => {
  test("older than 30 days are dropped; newer ones stay", () => {
    const now = Date.now();
    const old = draftKey(YOU, "task:tsk_old");
    const fresh = draftKey(YOU, "task:tsk_fresh");
    localStorage.setItem(old, JSON.stringify({ text: "long ago", savedAt: now - DRAFT_MAX_AGE_MS - 1 }));
    localStorage.setItem(fresh, JSON.stringify({ text: "this week", savedAt: now - 7 * 864e5 }));
    localStorage.setItem("dude.board.groupByEpic", "1");
    pruneDrafts(now);
    expect(localStorage.getItem(old)).toBeNull();
    expect(readDraft(fresh)?.text).toBe("this week");
    expect(localStorage.getItem("dude.board.groupByEpic")).toBe("1");
  });
});

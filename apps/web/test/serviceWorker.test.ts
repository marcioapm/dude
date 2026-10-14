/**
 * The service worker's notification click (public/sw.js), run as the
 * browser runs it: its listeners registered on a stand-in `self`, then a
 * click dispatched with the url the server put in the push.
 */

import { expect, test } from "bun:test";

interface Tab {
  url: string;
  focused: boolean;
  navigatedTo: string | null;
  focus: () => Promise<void>;
  navigate: (url: string) => Promise<void>;
  postMessage: (m: unknown) => void;
}

const ORIGIN = "https://dude.example";

function tab(hash: string): Tab {
  const t: Tab = {
    url: `${ORIGIN}/${hash}`, focused: false, navigatedTo: null,
    focus: async () => void (t.focused = true),
    navigate: async (url) => void (t.navigatedTo = url),
    postMessage: () => {},
  };
  return t;
}

/** Load sw.js against `tabs`; returns a click and what a new window was opened at. */
async function worker(tabs: Tab[]) {
  const listeners = new Map<string, (event: unknown) => void>();
  const opened: string[] = [];
  const self = {
    location: new URL(ORIGIN),
    addEventListener: (type: string, fn: (event: unknown) => void) => void listeners.set(type, fn),
    skipWaiting: () => {},
    clients: {
      claim: async () => {},
      matchAll: async () => tabs,
      openWindow: async (url: string) => void opened.push(url),
    },
    registration: { showNotification: async () => {} },
  };
  const source = await Bun.file(new URL("../public/sw.js", import.meta.url)).text();
  new Function("self", source)(self);
  const click = async (url: string) => {
    let done: Promise<unknown> = Promise.resolve();
    listeners.get("notificationclick")!({ notification: { close() {}, data: { url } }, waitUntil: (p: Promise<unknown>) => void (done = p) });
    await done;
  };
  return { click, opened };
}

test("an org-scoped link goes to a tab already in that organisation, at the link", async () => {
  const acme = tab("#/o/acme/");
  const northwind = tab("#/o/northwind/task/wi_9");
  const { click, opened } = await worker([acme, northwind]);
  await click("#/o/northwind/run/run_1");
  expect(northwind.navigatedTo).toBe(`${ORIGIN}/#/o/northwind/run/run_1`);
  expect(acme.navigatedTo).toBeNull();
  expect(opened).toEqual([]);
});

test("with no tab in that organisation, the first dude tab goes there", async () => {
  const acme = tab("#/o/acme/");
  const { click } = await worker([acme]);
  await click("#/o/northwind/task/wi_2");
  expect(acme.navigatedTo).toBe(`${ORIGIN}/#/o/northwind/task/wi_2`);
});

test("a bare link from an older dude still opens, in the first tab, as it is", async () => {
  const acme = tab("#/o/acme/");
  const northwind = tab("#/o/northwind/");
  const { click } = await worker([acme, northwind]);
  await click("#/task/wi_1");
  expect(acme.navigatedTo).toBe(`${ORIGIN}/#/task/wi_1`);
  expect(northwind.navigatedTo).toBeNull();
});

test("no tab: a new window at the link", async () => {
  const { click, opened } = await worker([]);
  await click("#/o/acme/sessions/ses_1");
  expect(opened).toEqual([`${ORIGIN}/#/o/acme/sessions/ses_1`]);
});

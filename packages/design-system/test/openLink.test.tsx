/**
 * openLinkWith: which clicks a link hands to the app's `onOpen`, and when
 * the browser's default (following the link) is kept. Then ServerRow's
 * links, which get a handler only while the server is ready at https.
 */

import { afterEach, expect, test } from "bun:test";
import { act, type MouseEvent, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { openLinkWith } from "../src/util/openLink.ts";
import { ServerRow } from "../src/components/ServerRow.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

const URL_ = "https://web.preview.example.test/";

/** A click as React hands it to onClick: only the fields openLinkWith reads, and whether preventDefault was called. */
function click(init: Partial<{ button: number; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean; defaultPrevented: boolean }> = {}) {
  const e = { button: 0, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, defaultPrevented: false, ...init, prevented: false,
    preventDefault() { e.prevented = true; } };
  return e;
}

function recorder(answer: boolean) {
  const calls: string[] = [];
  return { calls, onOpen: (url: string) => { calls.push(url); return answer; } };
}

const run = (handler: ((e: MouseEvent<HTMLAnchorElement>) => void) | undefined, e: ReturnType<typeof click>) =>
  handler!(e as unknown as MouseEvent<HTMLAnchorElement>);

test("with no onOpen there is no handler: the link is the browser's", () => {
  expect(openLinkWith(undefined, URL_)).toBeUndefined();
});

test("a plain left click goes to onOpen; true prevents the default, false leaves it", () => {
  for (const answer of [true, false]) {
    const r = recorder(answer);
    const e = click();
    run(openLinkWith(r.onOpen, URL_), e);
    expect([answer, r.calls, e.prevented]).toEqual([answer, [URL_], answer]);
  }
});

test("a click already handled, another button, or any modifier is left to the browser", () => {
  const cases = [{ defaultPrevented: true }, { button: 1 }, { button: 2 }, { metaKey: true }, { ctrlKey: true }, { shiftKey: true }, { altKey: true }];
  for (const init of cases) {
    const r = recorder(true);
    const e = click(init);
    run(openLinkWith(r.onOpen, URL_), e);
    expect([init, r.calls, e.prevented]).toEqual([init, [], false]);
  }
});

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount(node: ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{node}</TooltipProvider>));
  return host;
}

test("a ServerRow that is not ready, or not at https, gives its links no onOpen", async () => {
  // happy-dom follows an unhandled link through window.open; nothing is to be opened here.
  const followed: string[] = [];
  (window as unknown as { open: unknown }).open = (url: string) => { followed.push(url); return null; };
  const cases: Array<[string, "ready" | "starting", string]> = [
    ["starting", "starting", URL_],
    ["http", "ready", "http://web.preview.example.test/"],
  ];
  for (const [what, state, url] of cases) {
    const r = recorder(true);
    const c = await mount(<ServerRow name="web" port={3000} state={state} url={url} onOpen={r.onOpen} />);
    const links = [...c.querySelectorAll("a")];
    // A starting server still shows its URL as a link (out of the tab order); an http one shows it as text.
    expect([what, links.length]).toEqual([what, state === "starting" ? 1 : 0]);
    for (const a of links) {
      await act(async () => void a.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true, button: 0 })));
    }
    expect([what, r.calls]).toEqual([what, []]);
    await act(async () => root?.unmount());
    root = null;
  }
  // The same row ready at https does hand its click over.
  const r = recorder(true);
  const c = await mount(<ServerRow name="web" port={3000} state="ready" url={URL_} onOpen={r.onOpen} />);
  await act(async () => void c.querySelector('[data-testid="server-url"]')!.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true, button: 0 })));
  expect(r.calls).toEqual([URL_]);
});

/**
 * Where a transcript scrolls: following the tail, but never past the top
 * of a turn that waits on the reader (`keepInView`), until the reader moves
 * it themselves. happy-dom lays nothing out, so the test gives the viewport
 * and the held turn their geometry: turns 100px tall, a 300px viewport.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ChatTranscript } from "../src/components/ChatTranscript.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  HTMLElement.prototype.getBoundingClientRect = measured;
});

const TURN = 100;
const HEIGHT = 300;

/** Gives the transcript's viewport a layout: each turn TURN tall, stacked in order, and scrollTo moving it. */
function laidOut(page: HTMLElement): HTMLElement {
  const viewport = page.querySelector<HTMLElement>("[role=log]")!;
  let top = 0;
  const turns = () => [...viewport.querySelectorAll<HTMLElement>("[data-turn]")];
  Object.defineProperty(viewport, "scrollHeight", { configurable: true, get: () => turns().length * TURN });
  Object.defineProperty(viewport, "clientHeight", { configurable: true, get: () => HEIGHT });
  Object.defineProperty(viewport, "scrollTop", { configurable: true, get: () => top, set: (v: number) => { top = v; } });
  viewport.scrollTo = ((o: ScrollToOptions) => {
    top = Math.max(0, Math.min(o.top ?? 0, turns().length * TURN - HEIGHT));
  }) as typeof viewport.scrollTo;
  viewport.getBoundingClientRect = () => ({ top: 0, bottom: HEIGHT }) as DOMRect;
  // Turns rendered later are laid out too: the transcript measures them as they commit.
  HTMLElement.prototype.getBoundingClientRect = function (this: HTMLElement) {
    const at = turns().indexOf(this);
    return (at < 0 ? measured.call(this) : { top: at * TURN - top, bottom: (at + 1) * TURN - top }) as DOMRect;
  };
  return viewport;
}
const measured = HTMLElement.prototype.getBoundingClientRect;

function Transcript({ turns, held }: { turns: string[]; held?: string | undefined }) {
  return (
    <ChatTranscript fill keepInView={held} revision={turns.length}>
      {turns.map((t) => <div key={t} data-turn={t}>{t}</div>)}
    </ChatTranscript>
  );
}

async function render(turns: string[], held?: string) {
  await act(async () => root!.render(<Transcript turns={turns} held={held} />));
}

async function mount(turns: string[], held?: string) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  // Laid out before the turns arrive, as the page's are: it opens on the transcript with nothing in it.
  await render([], held);
  const viewport = laidOut(host);
  await render(turns, held);
  return viewport;
}

const turns = (n: number) => Array.from({ length: n }, (_, i) => `t${i}`);

describe("ChatTranscript keepInView", () => {
  test("without one it follows the tail", async () => {
    const viewport = await mount(turns(8));
    expect(viewport.scrollTop).toBe(8 * TURN - HEIGHT);
  });

  test("the question waiting on you opens in view, its top under the edge, though turns came after it", async () => {
    const viewport = await mount(turns(8), "[data-turn=t3]");
    expect(viewport.scrollTop).toBe(3 * TURN - 12);
  });

  test("turns that arrive after it do not scroll it away", async () => {
    const viewport = await mount(turns(5), "[data-turn=t3]");
    await render(turns(9), "[data-turn=t3]");
    expect(viewport.scrollTop).toBe(3 * TURN - 12);
  });

  test("the reader's own scroll lets it go: Jump to latest then goes to the tail", async () => {
    const viewport = await mount(turns(8), "[data-turn=t3]");
    await act(async () => void viewport.dispatchEvent(new WheelEvent("wheel", { bubbles: true })));
    viewport.scrollTop = 4 * TURN;
    await act(async () => void viewport.dispatchEvent(new Event("scroll", { bubbles: true })));
    const jump = [...host!.querySelectorAll("button")].find((b) => b.textContent?.includes("Jump to latest"))!;
    await act(async () => void jump.click());
    expect(viewport.scrollTop).toBe(8 * TURN - HEIGHT);
  });

  for (const [name, gesture] of [
    ["wheel up", () => new WheelEvent("wheel", { bubbles: true, deltaY: -100 })],
    ["touch", () => new Event("touchmove", { bubbles: true })],
    ["viewport ArrowUp", () => new KeyboardEvent("keydown", { bubbles: true, key: "ArrowUp" })],
    ["viewport PageUp", () => new KeyboardEvent("keydown", { bubbles: true, key: "PageUp" })],
    ["viewport Home", () => new KeyboardEvent("keydown", { bubbles: true, key: "Home" })],
    ["scrollbar", () => new PointerEvent("pointerdown", { bubbles: true, button: 0 })],
  ] as const) {
    test(`${name} releases the question so new content preserves upward reading`, async () => {
      const viewport = await mount(turns(8), "[data-turn=t3]");
      await act(async () => {
        viewport.dispatchEvent(gesture());
        viewport.scrollTop = TURN;
        viewport.dispatchEvent(new Event("scroll", { bubbles: true }));
      });
      await render(turns(9), "[data-turn=t3]");
      expect(viewport.scrollTop).toBe(TURN);
    });
  }

  test("answering or removing the held question resumes tail following", async () => {
    const viewport = await mount(turns(8), "[data-turn=t3]");
    await render(turns(8));
    expect(viewport.scrollTop).toBe(8 * TURN - HEIGHT);
    await render(turns(9));
    expect(viewport.scrollTop).toBe(9 * TURN - HEIGHT);
  });

  test("a turn that is not there yet holds nothing, and holds once it is", async () => {
    const viewport = await mount(turns(3), "[data-turn=t5]");
    expect(viewport.scrollTop).toBe(0);
    await render(turns(9), "[data-turn=t5]");
    expect(viewport.scrollTop).toBe(5 * TURN - 12);
  });

  test("a new question to keep in view is scrolled to, after the reader had let the last go", async () => {
    const viewport = await mount(turns(8), "[data-turn=t3]");
    await act(async () => void viewport.dispatchEvent(new WheelEvent("wheel", { bubbles: true })));
    await render(turns(8), "[data-turn=t6]");
    expect(viewport.scrollTop).toBe(8 * TURN - HEIGHT);
    await render(turns(8), "[data-turn=t1]");
    expect(viewport.scrollTop).toBe(1 * TURN - 12);
  });
});

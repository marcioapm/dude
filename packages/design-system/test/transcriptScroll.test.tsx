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
import { QuestionCard } from "../src/components/QuestionCard.tsx";

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
      {turns.map((t) => (
        <div key={t} data-turn={t}>
          {t}<button>Choice</button><input /><textarea /><span contentEditable /><button data-consumed onKeyDown={(e) => e.preventDefault()}>Consumed</button>
          <select><option>One</option></select><div role="textbox" tabIndex={0} />
          {t === "t3" ? <QuestionCard role="conductor" text="" items={ITEMS} askedAt={new Date()} answeredAt={null} onSubmit={async () => {}} /> : null}
          <pre data-scroll="tall" style={{ overflowY: "auto" }}><code>output</code></pre>
          <pre data-scroll="visible"><code>short</code></pre>
        </div>
      ))}
    </ChatTranscript>
  );
}

const ITEMS = [
  { header: "Scope", question: "Which scope?", choices: [{ label: "A" }, { label: "B" }] },
  { header: "Order", question: "Which order?", choices: [{ label: "C" }, { label: "D" }] },
];

/** Gives each `pre[data-scroll]` 100px of box: "tall" holds 400px of content (scrollTop as set), "visible" overflows without a scroller. */
function scrollers(viewport: HTMLElement, at: number) {
  for (const pre of viewport.querySelectorAll<HTMLElement>("pre[data-scroll]")) {
    Object.defineProperty(pre, "clientHeight", { configurable: true, get: () => 100 });
    Object.defineProperty(pre, "scrollHeight", { configurable: true, get: () => 400 });
    Object.defineProperty(pre, "scrollTop", { configurable: true, get: () => at });
  }
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

  for (const key of ["PageUp", "PageDown", "Home", "End"]) {
    test(`unconsumed ${key} from a question button releases the hold`, async () => {
      const viewport = await mount(turns(8), "[data-turn=t3]");
      const button = viewport.querySelector<HTMLElement>("[data-turn=t3] button")!;
      await act(async () => {
        button.dispatchEvent(new KeyboardEvent("keydown", { key, ctrlKey: key === "Home" || key === "End", bubbles: true, cancelable: true }));
        viewport.scrollTop = viewport.scrollHeight - HEIGHT;
        viewport.dispatchEvent(new Event("scroll", { bubbles: true }));
      });
      await render(turns(9), "[data-turn=t3]");
      expect(viewport.scrollTop).toBe(9 * TURN - HEIGHT);
    });
  }

  for (const [selector, key] of [
    ["button", " "], ["button", "ArrowDown"], ["input", "End"], ["textarea", "PageDown"],
    ["[contenteditable]", "Home"], ["button[data-consumed]", "End"], ["select", "End"], ["[role=textbox]", "PageDown"],
  ] as const) {
    test(`${key} on ${selector} does not release the question hold`, async () => {
      const viewport = await mount(turns(8), "[data-turn=t3]");
      await act(async () => {
        viewport.querySelector<HTMLElement>(`[data-turn=t3] ${selector}`)!.dispatchEvent(
          new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }),
        );
      });
      await render(turns(9), "[data-turn=t3]");
      expect(viewport.scrollTop).toBe(3 * TURN - 12);
    });
  }

  // The browser scrolls the log for these (no widget consumes them), so the hold must go or it snaps back.
  for (const [selector, key, ctrlKey] of [
    ["[role=tab]", "End", true], ["[role=tab]", "PageDown", false], ["[role=radio]", "End", true], ["[role=radio]", "PageDown", false],
  ] as const) {
    test(`unconsumed ${key} on a question's ${selector} releases the hold`, async () => {
      const viewport = await mount(turns(8), "[data-turn=t3]");
      await act(async () => {
        viewport.querySelector<HTMLElement>(`[data-turn=t3] ${selector}`)!.dispatchEvent(new KeyboardEvent("keydown", { key, ctrlKey, bubbles: true, cancelable: true }));
        viewport.scrollTop = viewport.scrollHeight - HEIGHT;
        viewport.dispatchEvent(new Event("scroll", { bubbles: true }));
      });
      await render(turns(9), "[data-turn=t3]");
      expect(viewport.scrollTop).toBe(9 * TURN - HEIGHT);
    });
  }

  for (const [key, at] of [["PageDown", 0], ["End", 0], ["PageUp", 300], ["Home", 300]] as const) {
    test(`${key} in a code block that scrolls that way keeps the hold: the block moves, not the log`, async () => {
      const viewport = await mount(turns(8), "[data-turn=t3]");
      scrollers(viewport, at);
      await act(async () => {
        viewport.querySelector<HTMLElement>("[data-turn=t3] pre[data-scroll=tall] code")!.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true }));
      });
      await render(turns(9), "[data-turn=t3]");
      expect(viewport.scrollTop).toBe(3 * TURN - 12);
    });
  }

  for (const [name, selector, at] of [
    ["already at its end", "pre[data-scroll=tall]", 300],
    ["that overflows without scrolling", "pre[data-scroll=visible]", 0],
  ] as const) {
    test(`PageDown in a code block ${name} releases: the browser scrolls the log`, async () => {
      const viewport = await mount(turns(8), "[data-turn=t3]");
      scrollers(viewport, at);
      await act(async () => {
        viewport.querySelector<HTMLElement>(`[data-turn=t3] ${selector}`)!.dispatchEvent(new KeyboardEvent("keydown", { key: "PageDown", bubbles: true, cancelable: true }));
        viewport.scrollTop = viewport.scrollHeight - HEIGHT;
        viewport.dispatchEvent(new Event("scroll", { bubbles: true }));
      });
      await render(turns(9), "[data-turn=t3]");
      expect(viewport.scrollTop).toBe(9 * TURN - HEIGHT);
    });
  }

  test("scroll keys on the viewport still release while clicks on choices do not", async () => {
    const viewport = await mount(turns(8), "[data-turn=t3]");
    await act(async () => void viewport.querySelector("button")!.dispatchEvent(new Event("pointerdown", { bubbles: true })));
    await render(turns(9), "[data-turn=t3]");
    expect(viewport.scrollTop).toBe(3 * TURN - 12);
    await act(async () => void viewport.dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true })));
    await render(turns(10), "[data-turn=t3]");
    expect(viewport.scrollTop).toBe(10 * TURN - HEIGHT);
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

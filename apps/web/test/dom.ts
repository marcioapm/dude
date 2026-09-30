/**
 * A browser's globals for the screen tests: a happy-dom window, the
 * fixtures' quiet EventSource, and a way to mount a screen and wait for
 * what it reads. Import it before anything that touches `window`.
 */

import { GlobalRegistrator } from "@happy-dom/global-registrator";

if (!("window" in globalThis)) GlobalRegistrator.register({ url: "http://localhost/" });
// React reports updates outside act() unless it is told this is a test.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const { act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { installFixtureStream } = await import("../src/fixtures/client.ts");
installFixtureStream();

/** Mount `element` into a fresh container; `unmount` takes it down. */
export async function mount(element: React.ReactNode): Promise<{ container: HTMLElement; unmount: () => Promise<void> }> {
  const container = document.createElement("div");
  document.body.appendChild(container);
  const root = createRoot(container);
  await act(async () => root.render(element));
  return {
    container,
    unmount: async () => {
      await act(async () => root.unmount());
      container.remove();
    },
  };
}

/** Let reads, the stream's opening and the renders they cause settle. */
export async function settle(ms = 60): Promise<void> {
  await act(async () => {
    await new Promise((r) => setTimeout(r, ms));
  });
}

/** Wait until `find` returns something, settling between tries. */
export async function until<T>(find: () => T | null | undefined, what: string, tries = 40): Promise<T> {
  for (let i = 0; i < tries; i++) {
    // Each try inside act, so what the reads and the stream set lands in it.
    await settle(25);
    const found = find();
    if (found) return found;
  }
  throw new Error(`never found: ${what}`);
}

export async function click(el: Element): Promise<void> {
  await act(async () => {
    (el as HTMLElement).click();
  });
}

export { act };

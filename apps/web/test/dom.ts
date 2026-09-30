/**
 * For the screen tests: the fixtures' quiet EventSource, and a way to
 * mount a screen and wait for what it reads. The browser's globals are
 * the design system's preload (bunfig.toml).
 */

const { act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { installFixtureStream } = await import("../src/fixtures/client.ts");
installFixtureStream();
// The preload's `window` is happy-dom's own object, apart from the global scope the code reads.
(globalThis as { EventSource?: unknown }).EventSource = (window as unknown as { EventSource: unknown }).EventSource;

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

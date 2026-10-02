/**
 * Sent images in a transcript: a turn's bytes are read only once it
 * scrolls into view, and let go when the turn unmounts.
 */

import { afterAll, expect, test } from "bun:test";
import { useState } from "react";
import type { AttachmentInfo } from "@dude/domain";
import { act, mount } from "./dom.ts";
import type { ApiClient } from "../src/api/client.ts";
import { TurnImages, useSentImages } from "../src/hooks/useImages.tsx";

type Entry = { isIntersecting: boolean };
/** IntersectionObserver as the test drives it: `show(el)` brings an element into view. */
const watched: Array<{ el: Element; fire: (entries: Entry[]) => void; on: boolean }> = [];
class FakeObserver {
  readonly #fire: (entries: Entry[]) => void;
  readonly #mine: typeof watched = [];
  constructor(fire: (entries: Entry[]) => void) {
    this.#fire = fire;
  }
  observe(el: Element) {
    const w = { el, fire: this.#fire, on: true };
    this.#mine.push(w);
    watched.push(w);
  }
  disconnect() {
    for (const w of this.#mine) w.on = false;
  }
}
const scope = globalThis as { IntersectionObserver?: unknown };
const real = scope.IntersectionObserver;
scope.IntersectionObserver = FakeObserver;
afterAll(() => {
  scope.IntersectionObserver = real;
});

async function show(el: Element) {
  await act(async () => {
    for (const w of watched) if (w.on && w.el === el) w.fire([{ isIntersecting: true }]);
    await new Promise((r) => setTimeout(r, 10));
  });
}

const info = (id: string): AttachmentInfo => ({
  id, name: `${id}.png`, contentType: "image/png", width: 10, height: 10, bytes: 4,
  original: { contentType: "image/png", width: 10, height: 10, bytes: 4 },
});

test("a turn's images are read when it scrolls into view, and let go when it unmounts", async () => {
  const read: string[] = [];
  const client = { attachment: async (id: string, variant: string) => {
    read.push(`${id}:${variant}`);
    return new Blob([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], { type: "image/png" });
  } } as unknown as ApiClient;
  const revoked: string[] = [];
  const revoke = URL.revokeObjectURL;
  URL.revokeObjectURL = (url: string) => void revoked.push(url);
  let setTurns: (turns: string[][]) => void = () => {};
  function Transcript() {
    const images = useSentImages(client);
    const [turns, set] = useState<string[][]>([["a"], ["b"]]);
    setTurns = set;
    return <>{turns.map((ids) => <TurnImages key={ids.join()} attachments={ids.map(info)} images={images} onOpen={() => {}} />)}</>;
  }
  const { container, unmount } = await mount(<Transcript />);
  try {
    const turns = container.querySelectorAll('[data-testid="message-images"]');
    expect(turns.length).toBe(2);
    // Nothing is read on render.
    expect(read).toEqual([]);
    expect(container.querySelectorAll("img").length).toBe(0);

    await show(turns[1]!);
    expect(read).toEqual(["b:delivered"]);
    const src = container.querySelector("img")?.getAttribute("src") ?? "";
    expect(src).toStartWith("blob:");

    // Turn b leaves the transcript: its URL is let go, and a's was never made.
    await act(async () => setTurns([["a"]]));
    expect(revoked).toEqual([src]);
  } finally {
    await unmount();
    URL.revokeObjectURL = revoke;
  }
});

/**
 * The Checkbox's accessible name and description: named by its label
 * alone, described by its description and by any hint shown beside it
 * (`aria-describedby`), as a screen reader announces them on focus.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Checkbox } from "../src/primitives/Checkbox.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
  return host;
}

/** The text of the elements an aria-*by attribute names, in order. */
const named = (page: HTMLElement, attr: string | null) =>
  (attr ?? "").split(" ").filter(Boolean).map((id) => page.ownerDocument.getElementById(id)?.textContent ?? `#${id}?`);

describe("Checkbox", () => {
  test("is named by its label and described by its description and the hint beside it", async () => {
    const page = await mount(
      <>
        <Checkbox label="Can run containers" description="Runs in this image can start containers." aria-describedby="hint" />
        <p id="hint">Previews keep theirs.</p>
      </>,
    );
    const box = page.querySelector("[role=checkbox]")!;
    expect(named(page, box.getAttribute("aria-labelledby"))).toEqual(["Can run containers"]);
    expect(named(page, box.getAttribute("aria-describedby"))).toEqual(["Runs in this image can start containers.", "Previews keep theirs."]);
  });

  test("with no hint, its description alone; with aria-label, no labelledby", async () => {
    const page = await mount(<Checkbox label="Seen" description="Mark it read." aria-label="Mark seen" />);
    const box = page.querySelector("[role=checkbox]")!;
    expect(box.getAttribute("aria-labelledby")).toBeNull();
    expect(box.getAttribute("aria-label")).toBe("Mark seen");
    expect(named(page, box.getAttribute("aria-describedby"))).toEqual(["Mark it read."]);
  });
});

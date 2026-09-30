/**
 * MarkdownDocument mounted: what Edit does to the field's focus and
 * selection, and that the bar formats what is selected when it is pressed.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { flushSync } from "react-dom";
import { createRoot, type Root } from "react-dom/client";
import { MarkdownDocument } from "../src/components/MarkdownDocument.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;

afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function render(source: string) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <TooltipProvider>
        <MarkdownDocument source={source} onSave={async () => {}} />
      </TooltipProvider>,
    );
  });
}

const byTestId = <T extends HTMLElement>(id: string) => document.querySelector<T>(`[data-testid="${id}"]`)!;
const click = (id: string) => act(async () => void byTestId(id).click());
/** Past the next animation frames: anything Edit left for later has run. */
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 100))));

test("Edit focuses the field with the caret at its start in the same commit that opens it", async () => {
  await render("Write the change and its tests.");
  await act(async () => {
    flushSync(() => byTestId("markdown-edit").click());
    // Nothing later: what Edit does to the field is done by now.
    const field = byTestId<HTMLTextAreaElement>("markdown-source");
    expect(document.activeElement).toBe(field);
    expect([field.selectionStart, field.selectionEnd]).toEqual([0, 0]);
  });
});

test("a selection made the moment the field opens is the one Bold wraps", async () => {
  await render("Write the change and its tests.");
  await act(async () => {
    flushSync(() => byTestId("markdown-edit").click());
    // A person, or a test driver, selects before the next frame.
    const field = byTestId<HTMLTextAreaElement>("markdown-source");
    const at = field.value.indexOf("its tests");
    field.setSelectionRange(at, at + "its tests".length);
  });
  await settle();
  await click("markdown-bold");
  expect(byTestId<HTMLTextAreaElement>("markdown-source").value).toBe("Write the change and **its tests**.");
});

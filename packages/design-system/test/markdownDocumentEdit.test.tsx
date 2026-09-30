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

async function render(source: string, defaultEditing = false) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <TooltipProvider>
        <MarkdownDocument source={source} defaultEditing={defaultEditing} onSave={async () => {}} />
      </TooltipProvider>,
    );
  });
}

const byTestId = <T extends HTMLElement>(id: string) => document.querySelector<T>(`[data-testid="${id}"]`)!;
const click = (id: string) => act(async () => void byTestId(id).click());
/** Past the next animation frame: a frame Edit queued runs before this one. */
const nextFrame = () => act(async () => void (await new Promise((r) => requestAnimationFrame(() => r(undefined)))));
/** Select `text` in the field, as a person dragging over it would. */
const select = (field: HTMLTextAreaElement, text: string) => {
  const at = field.value.indexOf(text);
  field.setSelectionRange(at, at + text.length);
};

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
    select(byTestId<HTMLTextAreaElement>("markdown-source"), "its tests");
  });
  await nextFrame();
  await click("markdown-bold");
  expect(byTestId<HTMLTextAreaElement>("markdown-source").value).toBe("Write the change and **its tests**.");
});

test("a field that opens already editing keeps its selection for Bold", async () => {
  // defaultEditing opens the field without Edit, so nothing places the
  // caret: it never took focus on its own, and still does not.
  await render("Write the change and its tests.", true);
  const field = byTestId<HTMLTextAreaElement>("markdown-source");
  expect(document.activeElement).not.toBe(field);
  await act(async () => select(field, "its tests"));
  await nextFrame();
  await click("markdown-bold");
  expect(field.value).toBe("Write the change and **its tests**.");
});

test("Edit after Cancel puts the caret at the start once, then leaves a selection alone", async () => {
  await render("Write the change and its tests.");
  await click("markdown-edit");
  await click("markdown-cancel");
  await act(async () => {
    flushSync(() => byTestId("markdown-edit").click());
    const field = byTestId<HTMLTextAreaElement>("markdown-source");
    expect([field.selectionStart, field.selectionEnd]).toEqual([0, 0]);
    select(field, "its tests");
  });
  await nextFrame();
  await click("markdown-bold");
  expect(byTestId<HTMLTextAreaElement>("markdown-source").value).toBe("Write the change and **its tests**.");
});

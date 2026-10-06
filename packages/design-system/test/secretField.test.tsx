/**
 * SecretField: a value written once. Masked it is a password field, with
 * a paste that keeps every line; shown it is a textarea of the same value.
 * Its length is said beside the hint either way, and password managers
 * are told to leave it alone. Mounted in happy-dom, found as assistive
 * technology finds it: by its label and its buttons' names.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { SecretField, secretLength } from "../src/primitives/SecretField.tsx";
import { byLabel, byRole } from "./queries.ts";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const seen: string[] = [];
function Harness({ initial = "" }: { readonly initial?: string | undefined }) {
  const [v, setV] = useState(initial);
  return <SecretField label="Value" hint="Saved once." value={v} onChange={(next) => { seen.push(next); setV(next); }} />;
}

async function render(initial?: string) {
  seen.length = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<Harness initial={initial} />));
  const h = host;
  return {
    field: () => byLabel<HTMLInputElement | HTMLTextAreaElement>(h, "Value"),
    show: () => byRole<HTMLButtonElement>(h, "button", "Show value"),
    hide: () => byRole<HTMLButtonElement>(h, "button", "Hide value"),
    described: () => {
      const ids = (byLabel(h, "Value").getAttribute("aria-describedby") ?? "").split(/\s+/).filter(Boolean);
      return ids.map((id) => document.getElementById(id)?.textContent ?? "").join(" ");
    },
  };
}

/** Types into a field as React reads it: through the element's own value setter. */
async function typeInto(el: HTMLInputElement | HTMLTextAreaElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

/** An edit as the browser makes one: the selection, beforeinput naming it, then the field's new value. */
async function edit(el: HTMLInputElement, inputType: string, [start, end]: [number, number], after: string) {
  await act(async () => {
    el.focus();
    el.setSelectionRange(start, end);
    el.dispatchEvent(new InputEvent("beforeinput", { inputType, bubbles: true, cancelable: true }));
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, after);
    el.dispatchEvent(new InputEvent("input", { inputType, bubbles: true }));
  });
}

/** A paste of text, as the browser sends one (the field's value is the handler's to change). */
async function paste(el: HTMLElement, text: string) {
  await act(async () => {
    const e = new Event("paste", { bubbles: true, cancelable: true }) as Event & { clipboardData: unknown };
    e.clipboardData = { getData: (type: string) => (type === "text/plain" ? text : "") };
    el.dispatchEvent(e);
  });
}

const PEM = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\nk3Lq9bF0rT2yVf8mWJxQ1s0Z\n-----END PRIVATE KEY-----";

describe("secretLength", () => {
  test("says how much a masked value holds", () => {
    expect(secretLength("")).toBe("");
    expect(secretLength("x")).toBe("1 character");
    expect(secretLength("sk-test-4b1d")).toBe("12 characters");
    expect(secretLength(PEM)).toBe(`4 lines · ${PEM.length} characters`);
    // A trailing newline is not a line of its own; it is a character.
    expect(secretLength("a\nb\n")).toBe("2 lines · 4 characters");
  });
});

describe("SecretField", () => {
  // Masked is a password field.
  test("starts masked, and the eye shows and hides it, saying which", async () => {
    const { field, show, hide } = await render(PEM);
    expect(field().tagName).toBe("INPUT");
    expect(field().getAttribute("type")).toBe("password");
    expect(show().getAttribute("aria-pressed")).toBe("false");
    expect(show().getAttribute("aria-controls")).toBe(field().id);
    await act(async () => show().click());
    expect(field().tagName).toBe("TEXTAREA");
    expect(field().value).toBe(PEM);
    expect(hide().getAttribute("aria-pressed")).toBe("true");
    await act(async () => hide().click());
    expect(field().getAttribute("type")).toBe("password");
  });

  test("its label names the field, masked and shown", async () => {
    const { field, show } = await render("x");
    const label = [...host!.querySelectorAll("label")].find((l) => l.textContent === "Value")!;
    expect(label.htmlFor).toBe(field().id);
    await act(async () => show().click());
    expect(label.htmlFor).toBe(field().id);
    expect(field().tagName).toBe("TEXTAREA");
  });

  // Masked and shown.
  test("keeps the value as typed, newlines and spaces included, and says its length", async () => {
    const { described, show } = await render(PEM);
    expect(described()).toContain(`4 lines · ${PEM.length} characters`);
    expect(described()).toContain("Saved once.");
    await act(async () => show().click());
    expect(described()).toContain(`4 lines · ${PEM.length} characters`);
  });

  test("a paste while masked keeps every line of what was copied", async () => {
    const { field, described, show } = await render();
    await paste(field(), PEM);
    expect(seen.at(-1)).toBe(PEM);
    expect(described()).toContain(`4 lines · ${PEM.length} characters`);
    // Into the middle of what is there: the lines on either side are kept.
    await act(async () => show().click());
    await typeInto(field(), "a\nb");
    await act(async () => byRole<HTMLButtonElement>(host!, "button", "Hide value").click());
    const masked = field() as HTMLInputElement;
    // The password field shows "ab": its offset 1 is the whole value's 2, after the line break.
    await act(async () => masked.setSelectionRange(1, 1));
    await paste(masked, "X\nY");
    expect(seen.at(-1)).toBe("a\nX\nYb");
  });

  test("typing while masked edits the whole value, its line breaks kept", async () => {
    const { field } = await render("one\ntwo");
    const masked = field() as HTMLInputElement;
    await edit(masked, "insertText", [6, 6], "onetwo!");
    expect(seen.at(-1)).toBe("one\ntwo!");
    await edit(masked, "insertReplacementText", [2, 3], "onEtwo!");
    expect(seen.at(-1)).toBe("onE\ntwo!");
    // A deletion across a line break takes the break with it.
    await edit(masked, "deleteContentBackward", [1, 4], "owo!");
    expect(seen.at(-1)).toBe("owo!");
  });

  test("among repeated characters, the edit lands at the caret it was made at", async () => {
    const { field } = await render("a\na");
    const masked = field() as HTMLInputElement;
    await edit(masked, "insertText", [0, 0], "aaa");
    expect(seen.at(-1)).toBe("aa\na");
  });

  test("a change the browser announced nothing for, with no known caret, shows the value rather than guess", async () => {
    const { field } = await render("a\na");
    await typeInto(field(), "aaa");
    expect(seen).toEqual([]);
    expect(field().tagName).toBe("TEXTAREA");
    expect(field().value).toBe("a\na");
  });

  // Shown, where a line break is typed.
  test("passes every change on as typed", async () => {
    const { field, show, described } = await render();
    await act(async () => show().click());
    const typed = "  line one\nline two\n";
    await typeInto(field(), typed);
    expect(seen.at(-1)).toBe(typed);
    expect(described()).toContain(`2 lines · ${typed.length} characters`);
  });

  test("the eye keeps the field focused, its caret where it was", async () => {
    const { field, show, hide } = await render("one\ntwo");
    await act(async () => {
      field().focus();
      (field() as HTMLInputElement).setSelectionRange(4, 4); // "onet|wo" masked: after the t
    });
    await act(async () => show().click());
    expect(document.activeElement).toBe(field());
    expect(field().selectionStart).toBe(5); // "one\nt|wo"
    await act(async () => hide().click());
    expect(document.activeElement).toBe(field());
    expect(field().selectionStart).toBe(4);
  });

  // Masked and shown.
  test("is left alone by password managers, autocorrect and spellcheck", async () => {
    const { field, show } = await render("x");
    for (const shown of [false, true]) {
      expect(field().tagName).toBe(shown ? "TEXTAREA" : "INPUT");
      expect(field().getAttribute("autocomplete")).toBe("off");
      expect(field().getAttribute("spellcheck")).toBe("false");
      expect(field().getAttribute("autocorrect")).toBe("off");
      expect(field().hasAttribute("data-1p-ignore")).toBe(true);
      expect(field().getAttribute("data-lpignore")).toBe("true");
      if (!shown) await act(async () => show().click());
    }
  });

  test("plain Enter while masked submits nothing; Ctrl+Enter is left to the form", async () => {
    const { field } = await render("partial");
    const enter = (ctrlKey: boolean) => {
      const e = new KeyboardEvent("keydown", { key: "Enter", ctrlKey, bubbles: true, cancelable: true });
      field().dispatchEvent(e);
      return e.defaultPrevented;
    };
    expect(enter(false)).toBe(true);
    expect(enter(true)).toBe(false);
  });

  test("an error replaces the hint and marks the field", () => {
    const h = renderToStaticMarkup(<SecretField label="Value" hint="Saved once." error="Enter a value." value="" onChange={() => {}} />);
    expect(h).toContain("Enter a value.");
    expect(h).not.toContain("Saved once.");
    expect(h).toContain('aria-invalid="true"');
  });
});

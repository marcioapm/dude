/**
 * Markdown's measures, read from the computed style with the shipped
 * stylesheets loaded: a prompt is a working document and fills its
 * container; a message and a document keep their reading measures.
 */

import { afterAll, beforeAll, expect, mock, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

// Under bun test a CSS module maps to {}: keep each class name as written.
const asWritten = () => ({ default: new Proxy({}, { get: (_, key) => (typeof key === "string" ? key : undefined) }) });
mock.module("../src/components/Markdown.module.css", asWritten);

const { Markdown } = await import("../src/components/Markdown.tsx");

const SHEETS = ["tokens/tokens.css", "components/Markdown.module.css"];
const styleEls: HTMLStyleElement[] = [];
let root: Root | null = null;
let host: HTMLElement | null = null;

beforeAll(async () => {
  for (const path of SHEETS) {
    const el = document.createElement("style");
    el.textContent = await Bun.file(new URL(`../src/${path}`, import.meta.url)).text();
    document.head.append(el);
    styleEls.push(el);
  }
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <div>
      <Markdown source="A prompt." variant="prompt" data-md="prompt" />
      <Markdown source="A message." variant="message" data-md="message" />
      <Markdown source="A document." variant="document" data-md="document" />
    </div>,
  ));
});

afterAll(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  for (const el of styleEls) el.remove();
});

const token = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
const maxWidth = (variant: string) => getComputedStyle(host!.querySelector(`[data-md="${variant}"]`)!).maxWidth;

test("a prompt has no measure: it fills its container", () => {
  expect(maxWidth("prompt")).toBe("none");
});

test("a message and a document keep their measures", () => {
  expect(token("--ds-measure-message")).not.toBe("");
  expect(maxWidth("message")).toBe(token("--ds-measure-message"));
  expect(maxWidth("document")).toBe(token("--ds-measure-document"));
});

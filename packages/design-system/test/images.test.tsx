/**
 * The image library's pieces: the ImagePicker (opening, filtering, the
 * keys, none, making an image FROM the words, archived images), the build
 * stages, the queue strip, and ImageHistory (the diff it shows against the
 * one before or the published one, and Publish again only where it can).
 * Mounted in happy-dom and driven by the events a browser sends. The
 * CodeEditor's CodeMirror is a browser's: the browser suites drive it.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { ImagePicker, type ImageChoiceView } from "../src/components/ImagePicker.tsx";
import { BuildQueueStrip, BuildStages, ImageHistory, type ImageHistoryVersion } from "../src/components/Images.tsx";
import { CodeEditor } from "../src/components/CodeEditor.tsx";

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

const IMAGES: ImageChoiceView[] = [
  { id: "base", name: "acme-base", description: "Debian, Node 26, Python 3.14", version: 7, isDefault: true },
  { id: "uv", name: "python-uv", description: "uv and the Postgres client", version: 2, status: { kind: "waiting", version: 3 } },
  { id: "rails", name: "rails-legacy", description: "Ruby 3.1", version: 3, status: { kind: "failed", version: 4 } },
  { id: "old", name: "old-runner", description: "", version: 1, archived: true },
];

const picks: Array<string | null> = [];
const created: string[] = [];
function Picker({ initial = null, allowNone }: { initial?: string | null; allowNone?: string }) {
  const [v, setV] = useState<string | null>(initial);
  return (
    <ImagePicker images={IMAGES} value={v} label="Runtime image" heading="Acme's images" allowNone={allowNone} noneLabel="Acme's default base"
      onChange={(id) => {
        picks.push(id);
        setV(id);
      }}
      onCreateFrom={(t) => created.push(t)} />
  );
}

const input = () => host!.querySelector<HTMLInputElement>("input[role=combobox]")!;
const key = (k: string) => act(async () => void input().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })));
const focus = () => act(async () => input().dispatchEvent(new FocusEvent("focusin", { bubbles: true })));
const type = async (text: string) => {
  const el = input();
  const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!;
  await act(async () => {
    setter.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const options = () => [...host!.querySelectorAll("[role=option]")].map((o) => o.textContent);
const active = () => host!.querySelector("[role=option][aria-selected=true]")?.textContent;

describe("ImagePicker", () => {
  test("shows the chosen image's name and its version now; opening lists the rest, archived ones left out", async () => {
    await mount(<Picker initial="uv" />);
    expect(input().value).toBe("python-uv");
    expect(host!.textContent).toContain("v2 now");
    await focus();
    expect(input().getAttribute("aria-expanded")).toBe("true");
    expect(options().map((o) => o!.split(/(?=Debian|uv and|Ruby)/)[0])).toEqual(["acme-base", "python-uv", "rails-legacy"]);
    expect(host!.textContent).toContain("Acme's images");
  });

  test("the words filter by name and description, ↓ moves, Enter picks the id and closes", async () => {
    picks.length = 0;
    await mount(<Picker />);
    await focus();
    await type("py");
    expect(options()).toHaveLength(2);
    expect(active()).toContain("acme-base");
    await key("ArrowDown");
    expect(active()).toContain("python-uv");
    await key("Enter");
    expect(picks).toEqual(["uv"]);
    expect(input().getAttribute("aria-expanded")).toBe("false");
    expect(input().value).toBe("python-uv");
  });

  test("↑ wraps to the last; Escape closes without picking", async () => {
    picks.length = 0;
    await mount(<Picker />);
    await focus();
    await key("ArrowUp");
    expect(active()).toContain("rails-legacy");
    await key("Escape");
    expect(input().getAttribute("aria-expanded")).toBe("false");
    expect(picks).toEqual([]);
  });

  test("a waiting or failed newer version is a badge on its row", async () => {
    await mount(<Picker />);
    await focus();
    expect(options()[1]).toContain("v3 waiting");
    expect(options()[2]).toContain("v4 failed");
    expect(options()[0]).toContain("default");
  });

  test("nothing matching offers making an image FROM the words", async () => {
    created.length = 0;
    await mount(<Picker />);
    await focus();
    await type("node:24");
    expect(options()).toEqual(["Make an image FROM node:24A one-line Containerfile; dude adds its layer when it builds."]);
    await key("Enter");
    expect(created).toEqual(["node:24"]);
  });

  test("none, offered, picks null and says what it means", async () => {
    picks.length = 0;
    await mount(<Picker initial="base" allowNone="Use Acme's" />);
    await focus();
    expect(active()).toContain("Use Acme's");
    await key("Enter");
    expect(picks).toEqual([null]);
    expect(host!.textContent).toContain("Acme's default base");
  });

  test("an archived image is listed while it is the one chosen, and marked", async () => {
    await mount(<Picker initial="old" />);
    expect(host!.textContent).toContain("Archived");
    await focus();
    expect(options().some((o) => o!.startsWith("old-runner"))).toBe(true);
  });
});

describe("build pieces", () => {
  test("stages carry their state for the eye and the reader", () => {
    const html = renderToStaticMarkup(
      <BuildStages stages={[
        { id: "a", label: "Waiting", state: "done" },
        { id: "b", label: "Building", detail: "ran out of memory (1.5 GB) at step 3", state: "failed" },
        { id: "c", label: "Pushed and published", state: "todo" },
      ]} />,
    );
    expect(html.match(/data-state="(\w+)"/g)).toEqual(['data-state="done"', 'data-state="failed"', 'data-state="todo"']);
    expect(html).toContain("ran out of memory (1.5 GB) at step 3");
  });

  test("the queue says what builds and what waits, or why nothing can", () => {
    const html = renderToStaticMarkup(<BuildQueueStrip building={{ label: "node-pnpm v5", elapsed: "2m" }} waiting={["python-uv v3", "playwright v8"]} />);
    expect(html).toContain("Building node-pnpm v5");
    expect(html).toContain("2 waiting: <span>python-uv v3</span><span>, playwright v8</span>");
    const off = renderToStaticMarkup(<BuildQueueStrip building={{ label: "x" }} waiting={["y"]} unavailable="Builds are off" />);
    expect(off).toContain("Builds are off");
    expect(off).not.toContain("Building x");
  });
});

const V = (id: string, n: number | null, state: ImageHistoryVersion["state"], containerfile: string): ImageHistoryVersion => ({
  id, number: n, state, containerfile, note: "", author: null, when: "now",
});
const HISTORY = [
  V("v3", 3, "failed", "FROM a\nRUN three\n"),
  V("v2", 2, "published", "FROM a\nRUN two\n"),
  V("v1", 1, "superseded", "FROM a\nRUN one\n"),
];

describe("ImageHistory", () => {
  test("the selected version against the one before it, then against the published one", async () => {
    await mount(<ImageHistory versions={HISTORY} publishedId="v2" onRepublish={() => {}} />);
    expect(host!.textContent).toContain("v2 (published) → v3");
    expect(host!.textContent).toContain("RUN three");
    await act(async () => host!.querySelector<HTMLButtonElement>("[data-version='1']")!.click());
    // v1 is the oldest: the whole of it.
    expect(host!.textContent).toContain("v1, whole");
    const published = [...host!.querySelectorAll("button")].find((b) => b.textContent === "With published")!;
    await act(async () => published.click());
    expect(host!.textContent).toContain("v2 (published) → v1");
  });

  test("Publish again only for a built version that is not the published one, and only with onRepublish", async () => {
    const asked: string[] = [];
    await mount(<ImageHistory versions={HISTORY} publishedId="v2" onRepublish={(v) => asked.push(v.id)} initialId="v1" />);
    const again = () => [...host!.querySelectorAll("button")].find((b) => b.textContent?.startsWith("Publish"));
    expect(again()?.textContent).toBe("Publish v1 again");
    await act(async () => again()!.click());
    expect(asked).toEqual(["v1"]);
    await act(async () => host!.querySelector<HTMLButtonElement>("[data-version='3']")!.click());
    expect(again()).toBeUndefined();
    await act(async () => host!.querySelector<HTMLButtonElement>("[data-version='2']")!.click());
    expect(again()).toBeUndefined();
  });
});

describe("CodeEditor", () => {
  test("draws its frame at once, the text a skeleton until the editor's chunk has loaded", () => {
    const html = renderToStaticMarkup(
      <CodeEditor aria-label="Containerfile" value={"FROM a\nRUN b\n"} header={<span>Containerfile</span>} after="USER agent" footer="3 lines" />,
    );
    expect(html).toContain("Containerfile");
    expect(html).toContain("USER agent");
    expect(html).toContain("3 lines");
    expect(html).toContain('aria-busy="true"');
  });
});

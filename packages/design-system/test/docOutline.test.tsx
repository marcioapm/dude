/**
 * The guide's pieces, mounted in happy-dom: SearchField (named, clears,
 * shows its shortcut), DocOutline (the current page's sections, the one in
 * view marked, clicks the caller's), DocSearchResults (count, options,
 * marked words, empty) and the word marking itself.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { SearchField } from "../src/primitives/SearchField.tsx";
import { DocOutline, DocSearchResults, markWords, type DocOutlinePage, type DocSearchResult } from "../src/components/DocOutline.tsx";
import { byRole } from "./queries.ts";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(el: React.ReactElement): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(el));
  return host;
}

describe("SearchField", () => {
  test("is named by its label, shows its shortcut while empty and a Clear once there are words", async () => {
    function Owned() {
      const [v, setV] = useState("");
      return <SearchField value={v} onValueChange={setV} label="Search the guide" placeholder="Search the guide" shortcut="/" />;
    }
    const el = await mount(<Owned />);
    const input = el.querySelector<HTMLInputElement>("input[type=search][aria-label='Search the guide']")!;
    expect(input).not.toBeNull();
    expect(el.querySelector("kbd")?.textContent).toBe("/");
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(input, "reviewer");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(input.value).toBe("reviewer");
    expect(el.querySelectorAll("kbd").length).toBe(0);
    await act(async () => byRole(el, "button", "Clear search").click());
    expect(input.value).toBe("");
  });
});

const PAGES: DocOutlinePage[] = [
  { slug: "accounts", title: "Accounts", sections: [{ anchor: "keys", heading: "Keys" }] },
  { slug: "delivery", title: "Delivering a task", sections: [
    { anchor: "the-pipeline", heading: "The pipeline" }, { anchor: "which-reviewers-run", heading: "Which reviewers run" },
  ] },
];

describe("DocOutline", () => {
  test("lists every page and only the current page's sections, the one in view marked", async () => {
    const el = await mount(<DocOutline pages={PAGES} current="delivery" currentSection="which-reviewers-run" href={(p, s) => `#/help/${p}${s ? `/${s}` : ""}`}
      onPage={() => undefined} onSection={() => undefined} />);
    expect([...el.querySelectorAll("[data-testid=help-page-link]")].map((a) => [a.textContent, a.getAttribute("aria-current")])).toEqual([
      ["Accounts", null], ["Delivering a task", "page"],
    ]);
    const sections = [...el.querySelectorAll<HTMLAnchorElement>("[data-testid=help-section-link]")];
    expect(sections.map((a) => [a.textContent, a.getAttribute("href"), a.getAttribute("aria-current")])).toEqual([
      ["The pipeline", "#/help/delivery/the-pipeline", null],
      ["Which reviewers run", "#/help/delivery/which-reviewers-run", "location"],
    ]);
  });

  test("a plain click is the caller's; a modified one is left to the browser", async () => {
    const calls: string[] = [];
    const el = await mount(<DocOutline pages={PAGES} current="delivery" href={(p) => `#/help/${p}`}
      onPage={(p) => calls.push(`page ${p}`)} onSection={(p, s) => calls.push(`section ${p}#${s}`)} />);
    const section = el.querySelector<HTMLAnchorElement>("[data-anchor=the-pipeline]")!;
    const plain = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 });
    await act(async () => void section.dispatchEvent(plain));
    expect(plain.defaultPrevented).toBe(true);
    const newTab = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0, metaKey: true });
    await act(async () => void section.dispatchEvent(newTab));
    expect(newTab.defaultPrevented).toBe(false);
    await act(async () => el.querySelector<HTMLAnchorElement>("[data-testid=help-page-link]")!.click());
    expect(calls).toEqual(["section delivery#the-pipeline", "page accounts"]);
  });
});

describe("DocSearchResults", () => {
  const FOUND: DocSearchResult[] = [
    { page: "delivery", anchor: "which-reviewers-run", heading: "Which reviewers run", pageTitle: "Delivering a task", snippet: "Documentation never runs by default." },
    { page: "why", anchor: "a-reviewer-didnt-run", heading: "A reviewer didn't run", pageTitle: "Why is dude doing this?", snippet: "Documentation runs only where required." },
  ];

  test("counts the sections, shows heading, page and snippet, marks the words, and opens on press", async () => {
    const opened: string[] = [];
    const el = await mount(<DocSearchResults results={FOUND} query="documentation reviewer" active={1} onActiveChange={() => undefined}
      onOpen={(r) => opened.push(r.anchor)} listId="l" />);
    expect(el.querySelector("[data-testid=help-search-count]")!.textContent).toBe("2 sections");
    const options = [...el.querySelectorAll<HTMLElement>("[role=option]")];
    expect(options.map((o) => o.getAttribute("aria-selected"))).toEqual(["false", "true"]);
    expect(options[0]!.textContent).toBe("Which reviewers runDelivering a taskDocumentation never runs by default.");
    expect([...options[0]!.querySelectorAll("mark")].map((m) => m.textContent)).toEqual(["reviewers", "Documentation"]);
    await act(async () => void options[1]!.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true })));
    expect(opened).toEqual(["a-reviewer-didnt-run"]);
  });

  test("says so when nothing matches, and is searching before the first answer", async () => {
    const el = await mount(<DocSearchResults results={[]} query="zebra" active={0} onActiveChange={() => undefined} onOpen={() => undefined} listId="l" />);
    expect(el.querySelector("[data-testid=help-search-count]")!.textContent).toBe("0 sections");
    expect(el.querySelector("[data-testid=help-search-empty]")!.textContent).toBe("Nothing in the guide matches “zebra”.");
    await act(async () => root!.render(<DocSearchResults results={null} query="zebra" active={0} onActiveChange={() => undefined} onOpen={() => undefined} listId="l" />));
    expect(el.querySelector("[data-testid=help-search-count]")!.textContent).toBe("Searching…");
    expect(el.querySelectorAll("[data-testid=help-search-empty]").length).toBe(0);
  });
});

describe("markWords", () => {
  test("marks whole words that begin with a query word's stem, case-insensitively", () => {
    const marked = (t: string, q: string) => markWords(t, q).filter((r) => r.marked).map((r) => r.text);
    expect(marked("Reviewers review; a reviewer reviewed it.", "reviewer")).toEqual(["Reviewers", "review", "reviewer", "reviewed"]);
    expect(marked("preview and overview", "review")).toEqual([]);
    expect(marked("Docs (documentation)", "documentation")).toEqual(["documentation"]);
    expect(markWords("plain", "").map((r) => r.text).join("")).toBe("plain");
  });
});

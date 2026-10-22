/**
 * The Help page, mounted in happy-dom: it lists the guide's pages, opens
 * the first when none is named, shows a named page's sections under the
 * guide's own anchors, and scrolls to the section the URL names.
 */

import { afterEach, describe, expect, mock, test } from "bun:test";

// Under bun test a CSS module maps to {}: keep each class name as written.
mock.module("../../../packages/design-system/src/components/Markdown.module.css", () => ({ default: new Proxy({}, { get: (_, key) => (typeof key === "string" ? key : undefined) }) }));
import { ThemeProvider } from "@dude/design-system";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, settle, type, until } from "./dom.ts";
import type { HelpPage } from "../src/api/client.ts";
import { App } from "../src/App.tsx";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PeopleProvider } from "../src/people.tsx";
import { setPlaceOrganization } from "../src/place.ts";
import { HelpScreen, helpSectionId } from "../src/screens/HelpScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
  setPlaceOrganization(null);
});

const PAGES: Record<string, HelpPage> = {
  memory: { slug: "memory", title: "Memory and search", sections: [{ anchor: "", heading: "", text: "dude keeps a memory." },
    { anchor: "searching", heading: "Searching", text: "Search finds by **words**." }] },
  settings: { slug: "settings", title: "Settings", sections: [{ anchor: "delivery", heading: "Delivery", text: "The delivery policy." }] },
};

class Helping extends FixtureClient {
  read: string[] = [];
  override helpPages() {
    return Promise.resolve({ pages: Object.values(PAGES).map((p) => ({ ...p, sections: p.sections.filter((s) => s.heading).map(({ anchor, heading }) => ({ anchor, heading })) })) });
  }
  override helpPage(slug: string) {
    this.read.push(slug);
    return PAGES[slug] ? Promise.resolve(PAGES[slug]!) : Promise.reject(new Error("no help page"));
  }
}

test("with no page named, the first page opens; the list names every page", async () => {
  const client = new Helping("a");
  const { container, unmount } = await mount(<HelpScreen client={client} onPage={() => {}} />);
  mounted.push(unmount);
  await until(() => container.querySelector('[data-page="memory"]'), "the first page");
  expect([...container.querySelectorAll('[data-testid="help-page-link"]')].map((a) => a.textContent)).toEqual(["Memory and search", "Settings"]);
  expect(container.querySelector(`#${helpSectionId("searching")}`)?.textContent).toContain("Search finds by words.");
  expect(container.querySelector(`#${helpSectionId("")}`)?.textContent).toContain("dude keeps a memory.");
});

test("overview opens before the alphabetically first page, with same-tab guide links", async () => {
  const client = new Helping("a");
  const overview: HelpPage = { slug: "overview", title: "Overview", sections: [{ anchor: "", heading: "", text: "See [delivery](help:settings#delivery) or [memory](help:memory#)." }] };
  client.helpPages = async () => ({ pages: [PAGES.memory!, overview] });
  client.helpPage = async (slug: string) => slug === "overview" ? overview : PAGES[slug]!;
  setPlaceOrganization("acme co/one");
  const { container, unmount } = await mount(<HelpScreen client={client} onPage={() => {}} />);
  mounted.push(unmount);
  await until(() => container.querySelector('[data-page="overview"]'), "the overview");
  const links = [...container.querySelectorAll("article a")];
  expect(links.map((a) => [a.textContent, a.getAttribute("href"), a.getAttribute("target")])).toEqual([
    ["delivery", "#/o/acme%20co%2Fone/help/settings/delivery", "_self"],
    ["memory", "#/o/acme%20co%2Fone/help/memory", "_self"],
  ]);
});

test("an unknown page shows an error without crashing", async () => {
  const { container, unmount } = await mount(<HelpScreen client={new Helping("a")} page="missing" onPage={() => {}} />);
  mounted.push(unmount);
  await until(() => container.textContent?.includes("Could not read this page: no help page"), "the missing-page error");
  expect(container.querySelectorAll("article h1").length).toBe(0);
});

test("a named section is scrolled to, and picking a page asks for it", async () => {
  const client = new Helping("a");
  const scrolled: string[] = [];
  const picked: string[] = [];
  const original = HTMLElement.prototype.scrollIntoView;
  HTMLElement.prototype.scrollIntoView = function (this: HTMLElement) {
    scrolled.push(this.id);
  };
  try {
    const { container, unmount } = await mount(<HelpScreen client={client} page="settings" section="delivery" onPage={(p) => picked.push(p)} />);
    mounted.push(unmount);
    await until(() => container.querySelector('[data-page="settings"]'), "the settings page");
    expect(scrolled).toEqual([helpSectionId("delivery")]);
    expect(client.read).toEqual(["settings"]);
    (container.querySelector('[data-testid="help-page-link"]') as HTMLElement).click();
    expect(picked).toEqual(["memory"]);
  } finally {
    HTMLElement.prototype.scrollIntoView = original;
  }
});

describe("the layout", () => {
  test("the list is sticky and 248px; the screen adds no padding of its own and the page has no measure", async () => {
    const sheets = await Promise.all(["../../../packages/design-system/src/tokens/tokens.css", "../src/app.css", "../../../packages/design-system/src/components/Markdown.module.css"].map((p) => Bun.file(new URL(p, import.meta.url)).text()));
    const styles = sheets.map((css) => Object.assign(document.createElement("style"), { textContent: css }));
    document.head.append(...styles);
    try {
      const { container, unmount } = await mount(<HelpScreen client={new Helping("a")} page="memory" onPage={() => {}} />);
      mounted.push(unmount);
      await until(() => container.querySelector('[data-page="memory"]'), "the page");
      const nav = getComputedStyle(container.querySelector(".helpPages")!);
      expect([nav.position, nav.top, nav.width, nav.overflowY]).toEqual(["sticky", "0px", "248px", "auto"]);
      const screen = getComputedStyle(container.querySelector(".helpScreen")!);
      // No padding of its own: happy-dom computes an unset padding as "".
      for (const pad of [screen.paddingTop, screen.paddingRight, screen.paddingBottom, screen.paddingLeft]) expect(["", "0px"]).toContain(pad);
      expect(["", "none"]).toContain(getComputedStyle(container.querySelector(".helpPage")!).maxWidth);
      expect(getComputedStyle(container.querySelector(".helpPage section > div")!).maxWidth).toBe("none");
    } finally {
      for (const s of styles) s.remove();
    }
  });
});

describe("the outline", () => {
  const LONG: HelpPage = { slug: "delivery", title: "Delivering a task", sections: [
    { anchor: "", heading: "", text: "Deliver runs agents." },
    { anchor: "the-pipeline", heading: "The pipeline", text: "Steps." },
    { anchor: "which-reviewers-run", heading: "Which reviewers run", text: "Reviewers." },
    { anchor: "which-findings-block", heading: "Which findings block", text: "Findings." },
  ] };
  class Long extends Helping {
    override helpPages() {
      return Promise.resolve({ pages: [PAGES.memory!, { ...LONG, sections: LONG.sections.filter((s) => s.heading).map(({ anchor, heading }) => ({ anchor, heading })) }] });
    }
    override helpPage(slug: string) {
      return slug === "delivery" ? Promise.resolve(LONG) : super.helpPage(slug);
    }
  }
  const outline = (c: HTMLElement) => [...c.querySelectorAll("[data-testid=help-section-link]")].map((a) => [a.textContent, a.getAttribute("aria-current")]);

  test("lists the current page's sections under it and marks the one scrolled into view", async () => {
    const tops: Record<string, number> = { "the-pipeline": 40, "which-reviewers-run": 600, "which-findings-block": 1400 };
    const original = HTMLElement.prototype.getBoundingClientRect;
    HTMLElement.prototype.getBoundingClientRect = function (this: HTMLElement) {
      const top = this.dataset["anchor"] ? tops[this.dataset["anchor"]] ?? 0 : 0;
      return { top, bottom: top + 100, left: 0, right: 0, width: 0, height: 100, x: 0, y: top, toJSON() {} } as DOMRect;
    };
    try {
      const { container, unmount } = await mount(<HelpScreen client={new Long("a")} page="delivery" onPage={() => {}} />);
      mounted.push(unmount);
      await until(() => container.querySelector('[data-page="delivery"]'), "the page");
      await until(() => (outline(container).length === 3 ? true : null), "the sections");
      expect(outline(container)).toEqual([["The pipeline", "location"], ["Which reviewers run", null], ["Which findings block", null]]);
      // Scrolled: Which reviewers run's top has passed the list's.
      tops["the-pipeline"] = -900;
      tops["which-reviewers-run"] = 10;
      tops["which-findings-block"] = 800;
      await act(async () => void document.dispatchEvent(new Event("scroll")));
      await until(() => outline(container)[1]?.[1] === "location" ? true : null, "the scrolled section");
      expect(outline(container)).toEqual([["The pipeline", null], ["Which reviewers run", "location"], ["Which findings block", null]]);
      // Only the current page has its sections listed.
      expect(container.querySelectorAll("[data-testid=help-section-link]").length).toBe(3);
    } finally {
      HTMLElement.prototype.getBoundingClientRect = original;
    }
  });

  test("at the end of the page, a short last section that can't reach the top is the one in view", async () => {
    const tops: Record<string, number> = { "the-pipeline": -1400, "which-reviewers-run": -500, "which-findings-block": 500 };
    let pane: HTMLElement | null = null;
    const original = HTMLElement.prototype.getBoundingClientRect;
    HTMLElement.prototype.getBoundingClientRect = function (this: HTMLElement) {
      if (this === pane) return { top: 0, bottom: 800, left: 0, right: 0, width: 0, height: 800, x: 0, y: 0, toJSON() {} } as DOMRect;
      const top = this.dataset["anchor"] ? tops[this.dataset["anchor"]] ?? 0 : 0;
      return { top, bottom: top + 100, left: 0, right: 0, width: 0, height: 100, x: 0, y: top, toJSON() {} } as DOMRect;
    };
    try {
      // The pane scrolled to its end: 2200 of content, 800 shown, 1400 scrolled.
      const { container, unmount } = await mount(
        <div data-testid="pane" style={{ overflowY: "auto" }}><HelpScreen client={new Long("a")} page="delivery" section="which-findings-block" onPage={() => {}} /></div>,
      );
      mounted.push(unmount);
      pane = container.querySelector<HTMLElement>("[data-testid=pane]")!;
      for (const [k, v] of [["scrollHeight", 2200], ["clientHeight", 800], ["scrollTop", 1400]] as const) Object.defineProperty(pane, k, { configurable: true, value: v });
      await until(() => (outline(container).length === 3 ? true : null), "the sections");
      await act(async () => void pane!.dispatchEvent(new Event("scroll")));
      await until(() => outline(container)[2]?.[1] === "location" ? true : null, "the final section");
      expect(outline(container)).toEqual([["The pipeline", null], ["Which reviewers run", null], ["Which findings block", "location"]]);
    } finally {
      HTMLElement.prototype.getBoundingClientRect = original;
    }
  });

  test("clicking a section asks for it in the URL, as the app's place", async () => {
    const asked: string[] = [];
    const { container, unmount } = await mount(<HelpScreen client={new Long("a")} page="delivery" onPage={() => {}} onSection={(p, s) => asked.push(`${p}/${s}`)} />);
    mounted.push(unmount);
    await until(() => (outline(container).length === 3 ? true : null), "the sections");
    const link = container.querySelector<HTMLAnchorElement>("[data-anchor=which-findings-block][data-testid=help-section-link]")!;
    expect(link.getAttribute("href")).toBe("#/help/delivery/which-findings-block");
    await click(link);
    expect(asked).toEqual(["delivery/which-findings-block"]);
  });

  test("in the app, a section click sets #/…/help/<page>/<section>", async () => {
    window.history.replaceState(null, "", "#/help/delivery");
    const client = new Long("a");
    const { container, unmount } = await mount(
      <ThemeProvider><TooltipProvider><ToastProvider><PeopleProvider client={client}>
        <App client={client} onSignOut={() => {}} onKeyRefused={() => {}} />
      </PeopleProvider></ToastProvider></TooltipProvider></ThemeProvider>,
    );
    mounted.push(unmount);
    await until(() => (outline(container).length === 3 ? true : null), "the sections");
    await click(container.querySelector("[data-anchor=which-reviewers-run][data-testid=help-section-link]")!);
    expect(window.location.hash).toBe("#/help/delivery/which-reviewers-run");
    window.history.replaceState(null, "", " ");
  });
});

describe("searching the guide", () => {
  const FOUND = [
    { type: "doc", id: "settings#delivery", title: "Settings — Delivery", snippet: "The delivery policy names the reviewers.", textRank: 1, vectorRank: 0, score: 0.016, embedded: false },
    { type: "doc", id: "memory#searching", title: "Memory and search — Searching", snippet: "Search finds a reviewer by words.", textRank: 2, vectorRank: 0, score: 0.015, embedded: false },
  ];
  class Searching extends Helping {
    asked: Array<{ q: string; types?: string | undefined; limit?: number | undefined }> = [];
    override searchMemory(params: { q: string; types?: string; limit?: number }) {
      this.asked.push(params);
      const results = params.q.includes("zebra") ? [] : FOUND;
      return Promise.resolve({ results, mode: "words" as const }) as ReturnType<FixtureClient["searchMemory"]>;
    }
  }
  const field = (c: HTMLElement) => c.querySelector<HTMLInputElement>("[data-testid=help-search]")!;
  const key = (el: Element, k: string) => act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));

  test("typing replaces the list with what the guide search found: heading, page, snippet with the words marked, and a count", async () => {
    const client = new Searching("a");
    const { container, unmount } = await mount(<HelpScreen client={client} page="memory" onPage={() => {}} />);
    mounted.push(unmount);
    await until(() => container.querySelector("[data-testid=help-page-link]"), "the list");
    await type(field(container), "reviewer");
    const results = await until(() => (container.querySelectorAll("[data-testid=help-search-result]").length === 2 ? container.querySelectorAll("[data-testid=help-search-result]") : null), "the results");
    expect(client.asked).toEqual([{ q: "reviewer", types: "doc", limit: 20 }]);
    expect(container.querySelectorAll("[data-testid=help-page-link]").length).toBe(0);
    expect(container.querySelector("[data-testid=help-search-count]")!.textContent).toBe("2 sections");
    expect([...results].map((r) => [r.querySelector("[class*=heading]")?.textContent ?? r.firstElementChild!.textContent, r.children[1]!.textContent])).toEqual([
      ["Delivery", "Settings"], ["Searching", "Memory and search"],
    ]);
    expect([...results[0]!.querySelectorAll("mark")].map((m) => m.textContent)).toEqual(["reviewers"]);
  });

  test("↑/↓ move, Enter opens that page at that section, Escape clears", async () => {
    const opened: string[] = [];
    const { container, unmount } = await mount(<HelpScreen client={new Searching("a")} page="memory" onPage={() => {}}
      onSection={(p, s) => opened.push(`${p}/${s}`)} />);
    mounted.push(unmount);
    await until(() => container.querySelector("[data-testid=help-page-link]"), "the list");
    const input = field(container);
    await type(input, "reviewer");
    await until(() => container.querySelector("[data-testid=help-search-result]"), "the results");
    const selected = () => [...container.querySelectorAll("[role=option]")].findIndex((o) => o.getAttribute("aria-selected") === "true");
    expect(selected()).toBe(0);
    await key(input, "ArrowDown");
    expect(selected()).toBe(1);
    expect(input.getAttribute("aria-activedescendant")).toBe(container.querySelectorAll("[role=option]")[1]!.id);
    await key(input, "ArrowUp");
    await key(input, "ArrowUp");
    expect(selected()).toBe(1);
    await key(input, "Enter");
    expect(opened).toEqual(["memory/searching"]);
    await key(input, "Escape");
    expect(input.value).toBe("");
    await until(() => container.querySelector("[data-testid=help-page-link]"), "the list back");
    expect(container.querySelectorAll("[data-testid=help-search-result]").length).toBe(0);
  });

  test("nothing found says so; the search waits for typing to pause and reads at most 200 characters", async () => {
    const client = new Searching("a");
    const { container, unmount } = await mount(<HelpScreen client={client} page="memory" onPage={() => {}} />);
    mounted.push(unmount);
    await until(() => container.querySelector("[data-testid=help-page-link]"), "the list");
    const input = field(container);
    await type(input, "ze");
    await settle(60);
    expect(client.asked).toEqual([]);
    await type(input, "zebra");
    const empty = await until(() => container.querySelector("[data-testid=help-search-empty]"), "the empty state");
    expect(empty.textContent).toBe("Nothing in the guide matches “zebra”.");
    expect(container.querySelector("[data-testid=help-search-count]")!.textContent).toBe("0 sections");
    // Two quick keystrokes, one search.
    expect(client.asked.map((a) => a.q)).toEqual(["zebra"]);
    expect(input.maxLength).toBe(200);
  });

  test("a failed search says so in place of the results, and the next search clears it", async () => {
    const client = new Searching("a");
    let fail = true;
    const answer = client.searchMemory.bind(client);
    client.searchMemory = (params) => (fail ? Promise.reject(new Error("search is down")) : answer(params));
    const { container, unmount } = await mount(<HelpScreen client={client} page="memory" onPage={() => {}} />);
    mounted.push(unmount);
    await until(() => container.querySelector("[data-testid=help-page-link]"), "the list");
    const input = field(container);
    await type(input, "beta");
    const error = await until(() => container.querySelector("[data-testid=help-search-error]"), "the search error");
    expect(error.textContent).toBe("Could not search the guide: search is down");
    expect(container.querySelector("[data-testid=help-search-count]")).toBeNull();
    expect(container.querySelector("[data-testid=help-search-empty]")).toBeNull();
    // Only the search's own error: nothing screen-wide.
    expect(container.querySelectorAll("[role=alert]").length).toBe(1);
    fail = false;
    await type(input, "reviewer");
    await until(() => (container.querySelectorAll("[data-testid=help-search-result]").length === 2 ? true : null), "the results");
    expect(container.querySelector("[data-testid=help-search-error]")).toBeNull();
    expect(container.querySelectorAll("[role=alert]").length).toBe(0);
  });

  test("a query without a searchable word sends no request", async () => {
    const client = new Searching("a");
    const { container, unmount } = await mount(<HelpScreen client={client} page="memory" onPage={() => {}} />);
    mounted.push(unmount);
    await type(field(container), "a !");
    await settle(260);
    expect(client.asked).toEqual([]);
    expect(container.querySelector("[data-testid=help-page-link]")).not.toBeNull();
  });

  test("pages arriving during a search do not send the query again", async () => {
    const client = new Searching("a");
    let release!: (out: Awaited<ReturnType<Helping["helpPages"]>>) => void;
    client.helpPages = () => new Promise((resolve) => { release = resolve; });
    const { container, unmount } = await mount(<HelpScreen client={client} page="memory" onPage={() => {}} />);
    mounted.push(unmount);
    await type(field(container), "reviewer");
    await until(() => client.asked.length === 1 ? true : null, "the search");
    await act(async () => release({ pages: Object.values(PAGES) }));
    await settle(260);
    expect(client.asked.map((a) => a.q)).toEqual(["reviewer"]);
    expect(container.querySelectorAll("[data-testid=help-search-result]").length).toBe(2);
  });

  test("Enter cannot open the previous query's result while the next search is pending", async () => {
    const opened: string[] = [];
    const { container, unmount } = await mount(<HelpScreen client={new Searching("a")} page="memory" onPage={() => {}}
      onSection={(p, s) => opened.push(`${p}/${s}`)} />);
    mounted.push(unmount);
    await until(() => container.querySelector("[data-testid=help-page-link]"), "the list");
    const input = field(container);
    await type(input, "reviewer");
    await until(() => container.querySelector("[data-testid=help-search-result]"), "the results");
    await type(input, "zebra");
    await key(input, "Enter");
    expect(opened).toEqual([]);
    expect(container.querySelector("[data-testid=help-search-count]")?.textContent).toBe("Searching…");
    expect(container.querySelectorAll("[data-testid=help-search-result]").length).toBe(0);
  });

  test("a slower answer to an earlier query does not replace the later query's results", async () => {
    const client = new Searching("a");
    const held: Array<{ q: string; resolve: () => void }> = [];
    const answer = client.searchMemory.bind(client);
    client.searchMemory = (params) => new Promise((resolve) => {
      held.push({ q: params.q, resolve: () => resolve(answer(params)) });
    });
    const { container, unmount } = await mount(<HelpScreen client={client} page="memory" onPage={() => {}} />);
    mounted.push(unmount);
    await until(() => container.querySelector("[data-testid=help-page-link]"), "the list");
    const input = field(container);
    await type(input, "reviewer");
    await until(() => (held.length === 1 ? true : null), "the first search");
    await type(input, "zebra");
    await until(() => (held.length === 2 ? true : null), "the second search");
    expect(held.map((h) => h.q)).toEqual(["reviewer", "zebra"]);
    await act(async () => held[1]!.resolve());
    await until(() => container.querySelector("[data-testid=help-search-empty]"), "the second query's answer");
    await act(async () => held[0]!.resolve());
    await settle();
    expect(container.querySelector("[data-testid=help-search-empty]")?.textContent).toBe("Nothing in the guide matches “zebra”.");
    expect(container.querySelectorAll("[data-testid=help-search-result]").length).toBe(0);
  });
});

/**
 * The Help page, mounted in happy-dom: it lists the guide's pages, opens
 * the first when none is named, shows a named page's sections under the
 * guide's own anchors, and scrolls to the section the URL names.
 */

import { afterEach, expect, test } from "bun:test";
import { mount, until } from "./dom.ts";
import type { HelpPage } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { HelpScreen, helpSectionId } from "../src/screens/HelpScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
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

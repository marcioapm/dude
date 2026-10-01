/**
 * Asking for a review, in the design system's parts: SearchPicker mounted
 * as a reviewer picker (suggestions before words, groups, one it will not
 * pick, several picks), GitHubUserLine, and the panel's reviewer lines.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { GitHubUserLine } from "../src/components/GitHubUserLine.tsx";
import { PullRequestPanel } from "../src/components/PullRequestPanel.tsx";
import { SearchPicker } from "../src/components/SearchPicker.tsx";

interface Who { login: string; name: string; group: string; asked?: boolean }
const PEOPLE: Who[] = [
  { login: "ana", name: "Ana Ribeiro", group: "Suggested", asked: true },
  { login: "tom", name: "Tom Okafor", group: "Suggested" },
  { login: "hanna", name: "Hanna Lindqvist", group: "People" },
  { login: "acme/platform", name: "Platform", group: "Teams" },
];

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

const asked: string[][] = [];
// Suggestions before any words, the rest by name after; what is picked the picker hides.
const find = async (q: string) =>
  PEOPLE.filter((p) => (q ? p.group !== "Suggested" && (p.login + p.name).toLowerCase().includes(q.toLowerCase()) : p.group === "Suggested"));

function Picker() {
  const [picked, setPicked] = useState<Who[]>([]);
  return (
    <>
      <output>{picked.map((p) => p.login).join(",")}</output>
      <SearchPicker<Who>
        label="Who to ask" findOnEmpty clearOnPick delay={0}
        find={find}
        exclude={new Set(picked.map((p) => p.login))}
        optionKey={(p) => p.login}
        renderOption={(p) => <span>{p.name}</span>}
        group={(p) => p.group}
        optionDisabled={(p) => (p.asked ? "Already asked" : null)}
        empty={(q) => `Nobody matches “${q}”`}
        onPick={(p) => setPicked((l) => [...l, p])}
        onBackspaceEmpty={() => setPicked((l) => l.slice(0, -1))}
        onSubmit={() => asked.push(picked.map((p) => p.login))}
      />
    </>
  );
}

const field = () => document.querySelector<HTMLInputElement>('[role="combobox"]')!;
const options = () => [...document.querySelectorAll('[role="option"]')].map((o) => o.textContent);
const groups = () => [...document.querySelectorAll('[role="listbox"] [role="presentation"]')].map((o) => o.textContent);
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 5))));
const key = (k: string, mods: KeyboardEventInit = {}) => act(async () => void field().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, ...mods })));
async function type(text: string) {
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    set.call(field(), text);
    field().dispatchEvent(new Event("input", { bubbles: true }));
  });
  await settle();
}
const picked = () => document.querySelector("output")!.textContent;

describe("SearchPicker, picking reviewers", () => {
  test("suggests before any words, skips one it will not pick, and picks several", async () => {
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(<Picker />));
    await settle();
    expect(groups()).toEqual(["Suggested"]);
    expect(options()).toEqual(["Ana RibeiroAlready asked", "Tom Okafor"]);
    // The first it can pick is active, not Ana.
    expect(field().getAttribute("aria-activedescendant")).toBe(document.querySelectorAll('[role="option"]')[1]!.id);
    expect(document.querySelector('[aria-disabled="true"]')?.textContent).toContain("Ana");
    await key("ArrowDown");
    expect(field().getAttribute("aria-activedescendant")).toBe(document.querySelectorAll('[role="option"]')[1]!.id);
    await key("Enter");
    await settle();
    expect(picked()).toBe("tom");

    await type("a");
    expect(groups()).toEqual(["People", "Teams"]);
    expect(options()).toEqual(["Hanna Lindqvist", "Platform"]);
    await key("ArrowDown");
    await key("Enter");
    await settle();
    expect(picked()).toBe("tom,acme/platform");
    expect(field().value).toBe("");

    await type("zz");
    expect(document.querySelector('[role="listbox"]')!.textContent).toBe("Nobody matches “zz”");
    await type("");
    await key("Backspace");
    expect(picked()).toBe("tom");
    await key("Enter", { metaKey: true });
    expect(asked).toEqual([["tom"]]);
  });

  test("Escape closes the list before asking to cancel", async () => {
    let cancelled = 0;
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(
      <SearchPicker<Who> label="Who" findOnEmpty delay={0} find={async () => PEOPLE} optionKey={(p) => p.login}
        renderOption={(p) => p.name} onPick={() => {}} onCancel={() => cancelled++} />,
    ));
    await settle();
    expect(field().getAttribute("aria-expanded")).toBe("true");
    await key("Escape");
    expect(field().getAttribute("aria-expanded")).toBe("false");
    expect(cancelled).toBe(0);
    await key("Escape");
    expect(cancelled).toBe(1);
    await key("ArrowDown");
    expect(field().getAttribute("aria-expanded")).toBe("true");
  });

  test("Enter with nothing open never submits the form around it", async () => {
    let submitted = 0;
    host = document.body.appendChild(document.createElement("div"));
    root = createRoot(host);
    await act(async () => root!.render(
      <form onSubmit={(e) => { e.preventDefault(); submitted++; }}>
        <SearchPicker<Who> label="Who" delay={1000} find={async () => PEOPLE} optionKey={(p) => p.login}
          renderOption={(p) => p.name} onPick={() => {}} />
      </form>,
    ));
    await type("ok");
    const enter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    await act(async () => void field().dispatchEvent(enter));
    expect(enter.defaultPrevented).toBe(true);
    expect(submitted).toBe(0);
  });
});

const text = (h: string) => h.replace(/<[^>]+>/g, "").replaceAll("&#x27;", "'").replaceAll("&quot;", '"');

describe("GitHubUserLine", () => {
  test("a name over its login and why; a login alone is the name", () => {
    expect(text(renderToStaticMarkup(<GitHubUserLine user={{ login: "ana", name: "Ana Ribeiro" }} detail="Changed these files recently" />)))
      .toBe("ARAna Ribeiroana · Changed these files recently");
    expect(text(renderToStaticMarkup(<GitHubUserLine user={{ login: "kai" }} />))).toBe("KAkai");
  });
  test("a team is a rounded square, a person a circle with GitHub's photo", () => {
    const team = renderToStaticMarkup(<GitHubUserLine user={{ login: "acme/platform", name: "Platform", team: true }} />);
    expect(team).not.toContain('role="img" aria-label="acme');
    expect(team).toContain('aria-label="Platform"');
    const person = renderToStaticMarkup(<GitHubUserLine user={{ login: "ana", avatarUrl: "https://avatars/ana" }} />);
    expect(person).toContain('src="https://avatars/ana"');
  });
});

describe("PullRequestPanel reviewers", () => {
  const base = { number: 7, url: "https://github.com/acme/api/pull/7", title: "Retry", state: "open", review: "pending", checks: "passing" } as const;
  const line = (reviews: object[]) =>
    [...renderToStaticMarkup(<PullRequestPanel pr={{ ...base, reviews } as never} />).matchAll(/data-fact="reviews"[^>]*>([^]*?)<\/li>/g)].map((m) => text(m[1]!));

  // Each one's latest word, as the sync keeps them (forge.latestReviews).
  test("every reviewer keeps a line: a comment, a team asked, one asked again after a verdict", () => {
    expect(line([
      { login: "ana", state: "APPROVED", submittedAt: "1" },
      { login: "tom", state: "COMMENTED", submittedAt: "1" },
      { login: "dana", state: "APPROVED", submittedAt: "1", rerequested: true },
      { login: "acme/platform", state: "REQUESTED", team: true },
    ])).toEqual([
      "ANana approved",
      "TOtom commented",
      "DAdana · asked again · approved before",
      "ACacme/platform · review requested",
    ]);
  });
});

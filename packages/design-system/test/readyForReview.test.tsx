/**
 * ReadyForReview, ReviewEvidence and BeforePullRequestFact: a delivery held
 * before its pull request, what was checked, and the task's choice in the
 * Overview rail.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ReadyForReview, ReviewEvidence } from "../src/components/ReadyForReview.tsx";
import { BeforePullRequestFact } from "../src/components/BeforePullRequestFact.tsx";
import { feedbackStatusSpec } from "../src/components/FeedbackCard.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount(node: ReactNode): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{node}</TooltipProvider>));
  return host;
}

const REPO = { name: "web", head: "dude/web-142", base: "main", additions: 412, deletions: 38 };
const named = (el: HTMLElement, name: string) =>
  [...el.querySelectorAll<HTMLElement>("button, [role=button], a")].filter((b) => (b.getAttribute("aria-label") ?? b.textContent?.trim()) === name);
const all = { onOpen: () => {}, onDraft: () => {}, onFeedback: () => {}, onDoneWithout: () => {} };

describe("ReadyForReview", () => {
  test("sent back, both Open buttons are refused with the reason as their description, read once", async () => {
    const el = await mount(<ReadyForReview line="x" repositories={[REPO]} state={{ kind: "sent-back", items: 2, openRefused: "2 feedback items still open" }} {...all} />);
    for (const name of ["Open pull request", "Open as draft"]) {
      const [b] = named(el, name);
      expect(b!.getAttribute("aria-disabled")).toBe("true");
      expect(document.getElementById(b!.getAttribute("aria-describedby")!)?.textContent).toBe("2 feedback items still open");
    }
    const visible = [...el.querySelectorAll("[aria-hidden=true]")].find((n) => n.textContent === "2 feedback items still open");
    expect(visible).toBeDefined();
    expect(named(el, "Done without a pull request").length).toBe(0);
  });

  test("waiting renders no Open, Draft or Done", async () => {
    const el = await mount(<ReadyForReview line="x" repositories={[REPO]} state={{ kind: "waiting", on: "Sam" }} {...all} />);
    for (const name of ["Open pull request", "Open as draft", "Done without a pull request"]) expect(named(el, name).length).toBe(0);
    expect(el.textContent).toContain("Waiting for Sam…");
    expect(named(el, "Write feedback").length).toBe(1);
  });

  test("Done without renders only while yours, and only with onDoneWithout", async () => {
    const yours = await mount(<ReadyForReview line="x" repositories={[REPO]} state={{ kind: "yours" }} {...all} />);
    expect(named(yours, "Done without a pull request").length).toBe(1);
    await act(async () => root!.render(<TooltipProvider><ReadyForReview line="x" repositories={[REPO]} state={{ kind: "yours" }} onOpen={() => {}} /></TooltipProvider>));
    expect(named(host!, "Done without a pull request").length).toBe(0);
  });

  test("the tone follows whose move it is", async () => {
    const tone = async (state: Parameters<typeof ReadyForReview>[0]["state"]) => {
      await act(async () => root ? root.render(<TooltipProvider><ReadyForReview line="x" repositories={[REPO]} state={state} /></TooltipProvider>) : undefined);
      return host!.querySelector("[data-tone]")?.getAttribute("data-tone");
    };
    await mount(<span />);
    expect(await tone({ kind: "yours" })).toBe("attention");
    expect(await tone({ kind: "sent-back", items: 1, openRefused: "1 feedback item still open" })).toBe("info");
    expect(await tone({ kind: "waiting", on: "Sam" })).toBe("neutral");
  });

  test("the rows are a list named for what would be opened, and each Changes names its repository", async () => {
    const el = await mount(<ReadyForReview line="x" repositories={[REPO, { ...REPO, name: "api" }]} state={{ kind: "yours" }} onChanges={() => {}} />);
    const list = el.querySelector('ul[aria-label="What would be opened"]')!;
    expect(list.querySelectorAll("li").length).toBe(2);
    expect(list.querySelector('[aria-label="into"]')?.textContent).toBe("→");
    expect(named(el, "Changes in web").length).toBe(1);
    expect(named(el, "Changes in api").length).toBe(1);
  });
});

describe("ReviewEvidence", () => {
  test("a chip is a button to a tab, a link out in a new tab, or plain words", async () => {
    const el = await mount(<ReviewEvidence chips={[
      { id: "r", icon: "check", ok: true, label: "3 reviewers passed", name: "Reviewers passed", onOpen: () => {} },
      { id: "p", icon: "globe", label: "Preview", name: "Open the preview", href: "https://p.example.com" },
      { id: "s", icon: "info", label: "Simplified" },
    ]} />);
    expect(el.querySelector("ul")?.getAttribute("aria-label")).toBe("What was checked");
    expect(el.querySelector('button[aria-label="Reviewers passed"]')).not.toBeNull();
    const link = el.querySelector<HTMLAnchorElement>('a[aria-label="Open the preview"]')!;
    expect([link.getAttribute("target"), link.getAttribute("href")]).toEqual(["_blank", "https://p.example.com"]);
    expect(el.querySelectorAll("[data-static]").length).toBe(1);
  });
});

describe("BeforePullRequestFact", () => {
  const options = [{ value: "project", label: "Project default (Open it)" }, { value: "ask", label: "Ask me first" }];

  test("locked renders a disabled trigger whose name carries the reason", async () => {
    const el = await mount(<BeforePullRequestFact value="ask" options={options} state={{ kind: "locked", reason: "The pull request is already open." }} hint="Read when Deliver reaches the pull request." />);
    const trigger = el.querySelector<HTMLElement>('[aria-label^="Before the pull request"]')!;
    expect(trigger.getAttribute("aria-label")).toBe("Before the pull request: Ask me first. The pull request is already open.");
    expect(trigger.hasAttribute("disabled") || trigger.getAttribute("data-disabled") !== null).toBe(true);
    expect(el.textContent).not.toContain("Read when Deliver reaches the pull request.");
  });

  test("read-only renders no Select, the value as text and whose it is", async () => {
    const el = await mount(<BeforePullRequestFact value="ask" options={options} state={{ kind: "read-only", owner: "Sam" }} hint="Read when Deliver reaches the pull request." />);
    expect(el.querySelectorAll("[role=combobox], button").length).toBe(0);
    expect(el.textContent).toContain("Ask me first");
    expect(el.textContent).toContain("Only Sam can change it.");
    expect(el.textContent).not.toContain("Read when Deliver reaches the pull request.");
  });

  test("the hint shows only while editable", async () => {
    const el = await mount(<BeforePullRequestFact value="project" options={options} state={{ kind: "editable" }} hint="Read when Deliver reaches the pull request." onValueChange={() => {}} />);
    expect(el.textContent).toContain("Read when Deliver reaches the pull request.");
  });
});

describe("FeedbackCard's holds", () => {
  test("an open item holds the merge, or a held delivery's pull request opening", () => {
    expect(feedbackStatusSpec("open").description).toContain("holds the merge");
    expect(feedbackStatusSpec("open", false, undefined, "pull request").description).toContain("holds the pull request's opening");
    expect(feedbackStatusSpec("delivered", false, undefined, "pull request").description).toContain("holds the pull request's opening");
  });
});

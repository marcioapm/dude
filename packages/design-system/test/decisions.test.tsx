/**
 * A conducted task in the design system: the start choice (two equal
 * ways, no default), a Run the conductor started as one line in Chat that
 * opens its session, the notice for a decision waited on, and the line
 * that says who decides. Markup in happy-dom: what is shown, what is a
 * button, and what a click does.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { ChatMessage } from "../src/components/ChatMessage.tsx";
import { ChatNotice } from "../src/components/ChatNotice.tsx";
import { ChatRunLine } from "../src/components/ChatRunLine.tsx";
import { DeciderLine } from "../src/components/DeciderLine.tsx";
import { StartChoice } from "../src/components/StartChoice.tsx";

const html = (el: React.ReactElement) => renderToStaticMarkup(el);
const plain = (h: string) => h.replace(/<[^>]+>/g, "");
const buttons = (h: string) => [...h.matchAll(/<button\b[^>]*>([\s\S]*?)<\/button>/g)].map((m) => plain(m[1]!));

describe("StartChoice", () => {
  const options = [
    { id: "talk", icon: "message" as const, title: "Talk it through", description: "Plan it first.", action: <button>Talk it through</button> },
    { id: "deliver", icon: "zap" as const, title: "Deliver", description: "Run the pipeline.", points: ["No one needs to be here"],
      action: <button>Deliver</button> },
  ];

  test("the ways side by side, each with its own button, none marked primary or chosen", () => {
    const h = html(<StartChoice options={options} />);
    expect([...h.matchAll(/data-option="(\w+)"/g)].map((m) => m[1])).toEqual(["talk", "deliver"]);
    expect(buttons(h)).toEqual(["Talk it through", "Deliver"]);
    expect(h).not.toContain("primary");
    expect(h).not.toContain("aria-pressed");
    expect(h).not.toContain("aria-checked");
    expect(plain(h)).toContain("No one needs to be here");
  });
});

describe("ChatRunLine", () => {
  let root: Root | null = null;
  let host: HTMLElement | null = null;
  afterEach(async () => {
    await act(async () => root?.unmount());
    host?.remove();
    root = host = null;
  });

  test("its role, what it is, its status and facts, on its role's rail", () => {
    const h = html(<ChatRunLine role="reviewer" status="completed" what="correctness · 2 findings" facts={["4m", "$0.88"]} onOpen={() => undefined} />);
    expect(plain(h)).toContain("Reviewer· correctness · 2 findings");
    expect(plain(h)).toContain("4m$0.88");
    expect(h).toContain('data-status="completed"');
    expect(h).toMatch(/^<div[^>]*data-role="reviewer"/);
    expect(h).toContain('aria-label="Open the Reviewer&#x27;s session"');
  });

  test("the whole line opens the Run's session", async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    let opened = 0;
    await act(async () => root!.render(<ChatRunLine role="implementer" status="running" onOpen={() => opened++} />));
    await act(async () => host!.querySelector("button")!.click());
    expect(opened).toBe(1);
  });

  test("the conductor's steers sit under the line, outside its button, and none adds nothing", () => {
    const bare = html(<ChatRunLine role="implementer" status="running" onOpen={() => undefined} />);
    expect(bare).not.toContain("run-line-steers");
    const h = html(<ChatRunLine role="implementer" status="running" onOpen={() => undefined}
      steers={<ChatMessage role="conductor" name="Conductor" intent="steer" content="Use staging." deliveredAt={null}
        pendingReason="Lands at the agent's next step." />} />);
    const [, after] = h.split("</button>");
    expect(after).toContain('data-testid="run-line-steers"');
    expect(plain(after!)).toContain("ConductorSteerQueued");
    expect(plain(after!)).toContain("Use staging.");
    expect(plain(after!)).toContain("Lands at the agent&#x27;s next step.");
  });
});

describe("who decides", () => {
  test("the conductor, what it waits on, and the way to hand it back", () => {
    const h = html(<DeciderLine decider="conductor" waiting="whether to open the pull request" action={<button>Let Deliver finish it</button>} />);
    expect(h).toContain('data-decider="conductor"');
    expect(plain(h)).toBe("The conductor decides · waiting on it: whether to open the pull requestLet Deliver finish it");
  });

  test("Deliver, with nothing waited on", () => {
    expect(plain(html(<DeciderLine decider="policy" />))).toBe("Deliver decides · the pipeline runs to the pull request on its own");
  });

  test("Deliver, holding a gate for the person", () => {
    expect(plain(html(<DeciderLine decider="policy" waiting="whether to open the pull request" action={<button>Let Deliver finish it</button>} />)))
      .toBe("Deliver decides · waiting on the person: whether to open the pull requestLet Deliver finish it");
  });

  test("a decision waited on is dude's notice, signed", () => {
    const h = html(<ChatNotice kind="decision" by="El Duderino" text="Waiting on the conductor." at={0} />);
    expect(h).toContain('data-kind="decision"');
    expect(plain(h)).toContain("El Duderino: Waiting on the conductor.");
  });

  test("a commit the conductor published is its line, signed by it", () => {
    const h = html(<ChatNotice kind="commit" by="Conductor" text="The conductor published app@0123456: README.md." at={0} />);
    expect(h).toContain('data-kind="commit"');
    expect(plain(h)).toContain("Conductor: The conductor published app@0123456: README.md.");
  });
});

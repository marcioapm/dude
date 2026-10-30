import { afterEach, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Disclosure } from "../src/components/Disclosure.tsx";
import { RunningList, RunningRow } from "../src/components/RunningRow.tsx";
import { AgentAvatar } from "../src/components/AgentAvatar.tsx";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { OverviewSection } from "../src/gallery/sections/Overview.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | undefined;
let host: HTMLDivElement;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
});
async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{node}</TooltipProvider>));
  return host;
}

test("a Disclosure is a real details: closed by default, its summary names its region, and it opens in place", async () => {
  const page = await mount(<Disclosure title="Goal & acceptance criteria" meta="6 criteria"><p>The goal.</p></Disclosure>);
  const details = page.querySelector("details")!;
  expect(details.open).toBe(false);
  const summary = details.querySelector("summary")!;
  expect(summary.textContent).toBe("Goal & acceptance criteria6 criteria");
  const region = details.querySelector("[role=region]")!;
  const title = page.querySelector(`#${CSS.escape(region.getAttribute("aria-labelledby")!)}`)!;
  expect(title.textContent).toBe("Goal & acceptance criteria");
  // Body mounted while closed, for find-in-page and a parent that measures it.
  expect(region.textContent).toBe("The goal.");
  await act(async () => {
    details.open = true;
    details.dispatchEvent(new Event("toggle"));
  });
  expect(details.open).toBe(true);
});

test("a controlled Disclosure says each change and keeps what its owner says; lazy mounts its body only while open", async () => {
  const seen: boolean[] = [];
  function Owner() {
    const [open, setOpen] = useState(false);
    return <Disclosure title="Time & cost" lazy open={open} onOpenChange={(o) => { seen.push(o); setOpen(o); }}><output>figures</output></Disclosure>;
  }
  const page = await mount(<Owner />);
  const details = page.querySelector("details")!;
  expect(page.querySelector("output")).toBeNull();
  await act(async () => {
    details.open = true;
    details.dispatchEvent(new Event("toggle"));
  });
  expect(seen).toEqual([true]);
  expect(details.open).toBe(true);
  expect(page.querySelector("output")!.textContent).toBe("figures");
  // A toggle event that changes nothing says nothing.
  await act(async () => void details.dispatchEvent(new Event("toggle")));
  expect(seen).toEqual([true]);
});

test("a RunningRow says who, on what, what it is doing, its cost and time, and its way in; empty parts are left out", async () => {
  const page = await mount(
    <RunningList aria-label="Running now">
      <RunningRow data-testid="row" avatar={<AgentAvatar role="reviewer" size="lg" live />} label="Security review" detail="gpt-5 · Running"
        activity="Reading signature validation" cost="$0.69" elapsed="5m" action={<button type="button">Open session</button>} />
      <RunningRow data-testid="bare" avatar={<AgentAvatar role="implementer" size="lg" live />} label="Implement" />
    </RunningList>,
  );
  const list = page.querySelector("ul")!;
  expect(list.getAttribute("aria-label")).toBe("Running now");
  const [row, bare] = [...list.querySelectorAll("li")];
  expect(row!.textContent).toBe("Security reviewgpt-5 · RunningReading signature validation$0.695mOpen session");
  expect(bare!.textContent).toBe("Implement");
  // No empty figures box beside a row without figures.
  expect(bare!.children.length).toBe(2);
});

test("the Overview gallery section draws each block in both themes and densities", async () => {
  const page = await mount(<PaneDensityContext.Provider value="both"><OverviewSection mode="both" /></PaneDensityContext.Provider>);
  const blocks = ["ov-running", "ov-disclosure", "ov-states", "ov-phone"];
  expect([...page.querySelectorAll("section#overview [id^='ov-']")].map((b) => b.id)).toEqual(blocks);
  for (const id of blocks) {
    const panes = [...page.querySelectorAll(`#${id} [data-theme][data-density]`)].map((p) => `${p.getAttribute("data-theme")} ${p.getAttribute("data-density")}`);
    expect(new Set(panes)).toEqual(new Set(["dark comfortable", "light comfortable", "dark compact", "light compact"]));
  }
  // The states: active with two reviewers, needs you, ready, not started, ended; costs settling, zero and missing.
  const states = page.querySelector("#ov-states")!.textContent!;
  for (const words of ["2 agents", "Needs Sam Rivera", "Ready to merge", "Not started", "Attempt 1 · set aside", "Not reported", "$0.00"]) {
    expect(states).toContain(words);
  }
});

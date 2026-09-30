/**
 * A tab that carries a tooltip: it opens on hover and on keyboard focus,
 * the tab keeps its selected look and its place in the arrow-key walk.
 * Mounted in happy-dom, driven by the events a browser sends.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Tab, TabList, TabPanel, Tabs } from "../src/primitives/Tabs.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function render(initial = "overview") {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <TooltipProvider>
        <Tabs defaultValue={initial}>
          <TabList aria-label="Task">
            <Tab value="overview">Overview</Tab>
            <Tab value="servers" count={2} tooltip="2 servers on">Servers</Tab>
            <Tab value="activity">Activity</Tab>
          </TabList>
          <TabPanel value="overview">o</TabPanel>
          <TabPanel value="servers">s</TabPanel>
          <TabPanel value="activity">a</TabPanel>
        </Tabs>
      </TooltipProvider>,
    );
  });
  return host;
}

const tab = (name: string) => [...document.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith(name))!;
const tooltip = () => document.querySelector("[role=tooltip]")?.textContent ?? null;
const wait = (ms: number) => act(async () => void (await new Promise((r) => setTimeout(r, ms))));
// Radix's roving focus moves on a timer after the key, as in a browser.
const key = async (el: HTMLElement, k: string) => {
  await act(async () => void el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true })));
  await wait(10);
};
const focused = () => (document.activeElement as HTMLElement | null)?.textContent ?? null;

describe("a tab with a tooltip", () => {
  test("says it on keyboard focus, and the arrow keys walk past it as before", async () => {
    await render();
    await act(async () => tab("Overview").focus());
    await key(tab("Overview"), "ArrowRight");
    const servers = tab("Servers");
    expect(focused()).toBe("Servers2");
    // Focus selects, as on any tab, and the tip is up at once.
    expect(servers.getAttribute("aria-selected")).toBe("true");
    expect(tooltip()).toContain("2 servers on");
    await key(servers, "ArrowRight");
    expect(focused()).toBe("Activity");
    expect(tab("Activity").getAttribute("aria-selected")).toBe("true");
    await wait(20);
    expect(tooltip()).toBeNull();
    await key(tab("Activity"), "ArrowLeft");
    expect(focused()).toBe("Servers2");
  });

  test("says it on hover", async () => {
    await render();
    const servers = tab("Servers");
    await act(async () => void servers.dispatchEvent(new PointerEvent("pointermove", { bubbles: true, pointerType: "mouse" })));
    await wait(500);
    expect(tooltip()).toContain("2 servers on");
  });

  test("keeps the selected tab's state, not the tooltip's", async () => {
    await render("servers");
    const servers = tab("Servers");
    expect(servers.getAttribute("data-state")).toBe("active");
    await act(async () => servers.focus());
    expect(tooltip()).toContain("2 servers on");
    expect(servers.getAttribute("data-state")).toBe("active");
    expect(tab("Overview").getAttribute("data-state")).toBe("inactive");
  });

  test("a tab without one is the tab it always was", async () => {
    await render();
    await act(async () => tab("Activity").focus());
    await wait(20);
    expect(tooltip()).toBeNull();
  });
});

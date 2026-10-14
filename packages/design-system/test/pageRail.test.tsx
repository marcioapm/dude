import { afterEach, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { FacetList, PageRail, PageRailBlock, PageWithRail } from "../src/components/PageWithRail.tsx";
import { SessionRail, SessionRailBlock } from "../src/components/SessionRail.tsx";

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
  await act(async () => root!.render(node));
  return host;
}
const options = [
  { value: "open", label: "Open", count: 4 },
  { value: "settled", label: "Settled", count: 0 },
  { value: "locked", label: "Locked", disabled: true, description: "Earlier scope" },
];
function Filters() {
  const [value, setValue] = useState<string[]>(["open"]);
  return <><FacetList label="Status filters" options={options} value={value} onValueChange={setValue} /><output>{value.join(",")}</output></>;
}

test("facets compose selections, retain zero-count choices and can clear the final choice", async () => {
  const page = await mount(<Filters />);
  const boxes = page.querySelectorAll<HTMLButtonElement>("[role=checkbox]");
  expect(boxes[0]!.getAttribute("aria-checked")).toBe("true");
  expect(boxes[1]!.disabled).toBe(false);
  expect(page.textContent).toContain("Settled0");
  await act(async () => boxes[1]!.click());
  expect(page.querySelector("output")!.textContent).toBe("open,settled");
  await act(async () => boxes[0]!.click());
  expect(page.querySelector("output")!.textContent).toBe("settled");
  await act(async () => boxes[1]!.click());
  expect(page.querySelector("output")!.textContent).toBe("");
  await act(async () => boxes[2]!.click());
  expect(page.querySelector("output")!.textContent).toBe("");
});

test("facet groups and blocks are named, disabled choices explain scope, empty options keep a meaningful line", async () => {
  const page = await mount(<PageRail aria-label="Controls"><PageRailBlock label="Status" live><Filters /></PageRailBlock>
    <FacetList label="Projects" options={[]} value={[]} onValueChange={() => { throw new Error("empty selection"); }} empty="No projects in this scope" />
  </PageRail>);
  const section = page.querySelector("section")!;
  expect(document.getElementById(section.getAttribute("aria-labelledby")!)!.textContent).toBe("Status");
  expect(page.querySelector('[role=group][aria-label="Status filters"]')).not.toBeNull();
  const locked = page.querySelectorAll("[role=checkbox]")[2]!;
  expect(document.getElementById(locked.getAttribute("aria-describedby")!)!.textContent).toBe("Earlier scope");
  expect(page.textContent).toContain("No projects in this scope");
});

test("page rerenders keep controls and their focus; session aliases share generic blocks", async () => {
  expect(SessionRail).toBe(PageRail);
  expect(SessionRailBlock).toBe(PageRailBlock);
  const rail = <SessionRail aria-label="Controls"><SessionRailBlock label="Filters"><Filters /></SessionRailBlock></SessionRail>;
  const page = await mount(<PageWithRail rail={rail}><p>First content</p></PageWithRail>);
  const box = page.querySelector<HTMLButtonElement>("[role=checkbox]")!;
  box.focus();
  await act(async () => root!.render(<PageWithRail rail={rail}><p>Updated content</p></PageWithRail>));
  expect(document.activeElement).toBe(box);
  expect(page.querySelectorAll("aside").length).toBe(1);
  expect(page.textContent).toContain("Updated content");
  await act(async () => root!.render(<PageWithRail><p>No rail</p></PageWithRail>));
  expect(page.querySelector("aside")).toBeNull();
  expect(page.textContent).toBe("No rail");
});

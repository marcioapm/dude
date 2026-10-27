/**
 * Incoming webhooks' pieces: ConditionRows (editing, results, names),
 * DeliveryVerdict (every verdict, its run button), TriggerSummary's
 * incoming line, and KeyShownOnce's closing words.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { ConditionRows, DeliveryVerdict, type TriggerCondition } from "../src/components/Incoming.tsx";
import { TriggerSummary } from "../src/components/Automations.tsx";
import { KeyShownOnce } from "../src/components/KeyShownOnce.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount(node: React.ReactNode): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{node}</TooltipProvider>));
  return host;
}

const two: TriggerCondition[] = [
  { path: "status", op: "is", values: ["firing"] },
  { path: "commonLabels.severity", op: "one_of", values: ["critical", "high"] },
];

describe("ConditionRows", () => {
  test("no conditions says every JSON delivery starts a run", () => {
    expect(renderToStaticMarkup(<ConditionRows value={[]} />)).toContain("No conditions: every JSON delivery starts a run.");
  });

  test("rows lead When / and, are named by number, and add and remove", async () => {
    let value = two;
    const el = await mount(<ConditionRows value={value} onChange={(v) => { value = v; }} />);
    expect([...el.querySelectorAll("li > span[aria-hidden]")].map((s) => s.textContent)).toEqual(["When", "and"]);
    expect(el.querySelector('input[aria-label="Condition 1: path"]')!.getAttribute("value")).toBe("status");
    expect(el.querySelector('input[aria-label="Condition 2: values"]')).not.toBeNull();
    await act(async () => (el.querySelector('button[aria-label="Remove condition 1"]') as HTMLButtonElement).click());
    expect(value).toEqual([two[1]!]);
    await act(async () => (el.querySelector('[data-testid="add-condition"]') as HTMLButtonElement).click());
    expect(value.at(-1)).toEqual({ path: "", op: "is", values: [] });
  });

  test("with results, each row is marked Holds or Does not hold and a miss says what the body had", () => {
    const html = renderToStaticMarkup(<ConditionRows value={two} results={[{ ok: true, actual: "firing" }, { ok: false, actual: "warning" }]} />);
    expect(html).toContain('aria-label="Holds"');
    expect(html).toContain('aria-label="Does not hold"');
    expect(html).toContain("the body has <code class=\"ds-mono\">warning</code>");
    // Read-only: no remove, no add.
    expect(html).not.toContain("Remove condition");
    expect(html).not.toContain("add-condition");
  });

  test("exists takes no value", () => {
    expect(renderToStaticMarkup(<ConditionRows value={[{ path: "x", op: "exists", values: [] }]} />)).toContain("any value");
  });
});

describe("DeliveryVerdict", () => {
  test("every verdict reads its words", () => {
    const words = (v: Parameters<typeof DeliveryVerdict>[0]["verdict"]) => renderToStaticMarkup(<DeliveryVerdict verdict={v} />);
    expect(words({ kind: "started", runId: "arn_1" })).toContain("Started run");
    expect(words({ kind: "sent", runId: "arn_1" })).toContain("Sent to running agent");
    expect(words({ kind: "skipped", because: "status is resolved, not firing" })).toContain("status is resolved, not firing");
    for (const [reason, text] of [["signature", "Bad signature"], ["not_json", "Not JSON"], ["too_large", "Too large"], ["rate_limited", "Rate limited"]] as const) {
      expect(words({ kind: "refused", reason })).toContain(`Refused · ${text}`);
    }
    expect(renderToStaticMarkup(<DeliveryVerdict verdict={{ kind: "skipped", because: "x missed" }} detail={false} />)).not.toContain("x missed");
  });

  test("a verdict with a run is a button that opens it, without opening the row", async () => {
    const opened: string[] = [];
    let row = 0;
    const el = await mount(<div onClick={() => row++}><DeliveryVerdict verdict={{ kind: "sent", runId: "arn_9" }} onRun={(id) => opened.push(id)} /></div>);
    const b = el.querySelector('button[aria-label="Sent to running agent: open the run"]') as HTMLButtonElement;
    await act(async () => b.click());
    expect(opened).toEqual(["arn_9"]);
    expect(row).toBe(0);
    expect(renderToStaticMarkup(<DeliveryVerdict verdict={{ kind: "refused", reason: "signature" }} onRun={() => undefined} />)).not.toContain("<button");
  });
});

describe("TriggerSummary on an incoming webhook", () => {
  test("its first condition and +N, or any JSON", () => {
    const html = renderToStaticMarkup(<TriggerSummary trigger={{ kind: "incoming", conditions: two }} />);
    expect(html).toContain("Incoming");
    expect(html).toContain("status is firing +1");
    expect(html).toContain('data-trigger="incoming"');
    expect(renderToStaticMarkup(<TriggerSummary trigger={{ kind: "incoming", conditions: [] }} />)).toContain("any JSON");
  });
});

describe("KeyShownOnce", () => {
  test("doneLabel names the closing button; Done by default", async () => {
    const el = await mount(<KeyShownOnce title="New verification secret" secret="whsec_x" label="Secret" doneLabel="I've stored it" onClose={() => undefined} />);
    expect(document.querySelector('[data-testid="key-done"]')!.textContent).toBe("I've stored it");
    expect(document.body.textContent).toContain("Secret");
    expect(el).toBeTruthy();
  });
});

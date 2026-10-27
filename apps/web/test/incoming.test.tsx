/**
 * An incoming webhook's pages against the fixture client: the New dialog's
 * third trigger and the secret shown once after Create, the trigger rows
 * (endpoint, verification per mode, how to send), the organisation
 * automation's Projects refusal, Rotate, the Deliveries tab and a
 * delivery's detail, Run now on a payload, and an alert's run.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { ReactNode } from "react";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { INCOMING_NEEDS_PROJECT } from "@dude/domain";
import { act, click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE } from "../src/fixtures/data.ts";
import { AutomationPage, AutomationsList, automationsPlace, automationsSub, type AutomationsPlace } from "../src/screens/AutomationsSettings.tsx";
import { curlFor } from "../src/screens/automationIncoming.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

async function show(node: ReactNode) {
  const { container, unmount } = await mount(<TooltipProvider><ToastProvider>{node}</ToastProvider></TooltipProvider>);
  mounted.push(unmount);
  return container;
}

const page = (client: FixtureClient, place: AutomationsPlace, onPlace: (p: AutomationsPlace) => void = () => undefined) =>
  show(<AutomationPage client={client} orgName="Acme" people={PEOPLE} place={place} onPlace={onPlace} onBack={() => undefined} />);

const button = (root: ParentNode, text: string) => [...root.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === text);

describe("places", () => {
  test("Deliveries and one delivery round-trip through the settings sub-path", () => {
    for (const p of [{ id: "aut_1", tab: "deliveries" }, { id: "aut_1", tab: "deliveries", deliveryId: "dlv_2" }] as AutomationsPlace[]) {
      expect(automationsPlace(automationsSub(p))).toMatchObject(p);
    }
  });
});

describe("the list", () => {
  test("an incoming trigger reads Incoming and its first condition +N", async () => {
    const c = await show(<AutomationsList client={new FixtureClient("a")} orgName="Acme" projectId={null} canCreate onOpen={() => undefined} />);
    const row = await until(() => c.querySelector<HTMLElement>("[data-testid=automation-row][data-id=aut_alert]"), "the alert's row");
    expect(row.querySelector("[data-trigger=incoming]")?.textContent).toBe("Incoming·\u00a0status is firing +1");
  });
});

describe("New automation → Incoming webhook", () => {
  test("creates an incoming trigger and shows its secret once on the page that opens", async () => {
    const client = new FixtureClient("a");
    const opened: string[] = [];
    const c = await show(<AutomationsList client={client} orgName="Acme" projectId={null} canCreate onOpen={(id) => opened.push(id)} />);
    await click(await until(() => c.querySelector<HTMLElement>("[data-testid=new-automation]"), "New automation"));
    const triggers = await until(() => document.body.querySelector<HTMLElement>("[role=group][aria-label='When it runs']"), "the triggers");
    await click(button(triggers, "Incoming webhook")!);
    expect(document.body.textContent).toContain("Its own URL: anything that can POST JSON starts it.");
    const name = document.body.querySelector<HTMLInputElement>("[role=dialog] input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(name, "Investigate");
      name.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(document.body.querySelector("[data-testid=create-automation]")!);
    const id = await until(() => opened[0], "the new automation opened");
    expect(client.fixtureAutomation(id).trigger.kind).toBe("incoming");
    for (const unmount of mounted) await unmount();
    mounted = [];
    await page(client, { id });
    const done = await until(() => document.body.querySelector<HTMLButtonElement>("[data-testid=key-done]"), "the secret, once");
    expect(done.textContent).toBe("I've stored it");
    expect(document.body.querySelector<HTMLInputElement>("[data-testid=key-secret]")!.value).toMatch(/^whsec_/);
    await click(done);
    // Taken: opening the page again shows no secret.
    for (const unmount of mounted) await unmount();
    mounted = [];
    await page(client, { id });
    await until(() => document.body.querySelector("[data-testid=incoming-endpoint]"), "the endpoint");
    expect(document.body.querySelector("[data-testid=key-done]")).toBeNull();
  });
});

describe("the trigger rows", () => {
  test("the endpoint, a signature's fields and the curl; header and query modes change them", async () => {
    const c = await page(new FixtureClient("a"), { id: "aut_alert" });
    const url = await until(() => c.querySelector<HTMLElement>("[data-testid=endpoint-url]"), "the endpoint");
    expect(url.textContent).toBe(`${window.location.origin}/v1/hooks/hk_7f3a9c21e4b04d18`);
    expect(c.querySelector<HTMLInputElement>("input[aria-label='Header name']")!.value).toBe("X-Signature");
    expect(c.querySelector<HTMLInputElement>("input[aria-label='Signature prefix']")!.value).toBe("sha256=");
    expect(c.querySelector("[data-testid=secret-hint]")!.textContent).toBe("••••a91c");
    expect(c.querySelector("[aria-label='curl example']")!.textContent).toContain(`-H "X-Signature: sha256=$SIG"`);
    const verify = c.querySelector<HTMLElement>("[role=group][aria-label=Verification]")!;
    await click(button(verify, "Header token")!);
    expect(c.querySelector<HTMLInputElement>("input[aria-label='Header name']")!.value).toBe("Authorization");
    expect(c.querySelector("input[aria-label='Signature prefix']")).toBeNull();
    await click(button(verify, "URL query parameter")!);
    expect(c.querySelector("[data-testid=endpoint-url]")!.textContent).toContain("?token=••••");
    expect(c.querySelector<HTMLInputElement>("input[aria-label='Query parameter']")!.value).toBe("token");
  });

  test("an incoming agent automation's page has its trigger rows and the Tools card", async () => {
    const c = await page(new FixtureClient("a"), { id: "aut_alert" });
    await until(() => c.querySelector("[data-testid=incoming-endpoint]"), "the endpoint");
    const tools = await until(() => c.querySelector<HTMLElement>("[data-testid=tools-row]"), "the Tools card");
    expect(tools.textContent).toContain("post_to_chat");
    expect(c.querySelector("[data-testid=incoming-conditions]")).not.toBeNull();
  });

  test("curl signs the body for a signature, sends the token for a header, puts it in the URL for a query", () => {
    const url = "https://dude.example.com/v1/hooks/hk_x";
    expect(curlFor(url, { mode: "signature", header: "X-Sig", prefix: "" })).toContain(`openssl dgst -sha256 -hmac "$SECRET"`);
    expect(curlFor(url, { mode: "header", header: "Authorization" })).toContain(`-H "Authorization: $SECRET"`);
    expect(curlFor(url, { mode: "query", param: "t" })).toContain(`"${url}?t=$SECRET"`);
  });

  test("an organisation incoming automation is not saved without a project, and says so on the field and the button", async () => {
    const client = new FixtureClient("a");
    const c = await page(client, { id: "aut_pager" });
    const projects = await until(() => c.querySelector<HTMLElement>("[data-testid=incoming-projects]"), "Projects");
    const save = button(c, "Save")!;
    await click(save);
    expect(projects.textContent).toContain(INCOMING_NEEDS_PROJECT);
    expect(c.querySelector("[data-testid=not-saved]")!.textContent).toBe(`Not saved: ${INCOMING_NEEDS_PROJECT}`);
    expect(client.fixtureAutomation("aut_pager").projectIds).toEqual([]);
  });

  test("Rotate asks first, then shows the new secret once", async () => {
    const c = await page(new FixtureClient("a"), { id: "aut_alert" });
    await click(await until(() => c.querySelector<HTMLElement>("[data-testid=rotate]"), "Rotate"));
    await click(await until(() => document.body.querySelector<HTMLElement>("[data-testid=rotate-confirm]"), "the confirm"));
    await until(() => document.body.querySelector("[data-testid=key-done]"), "the secret");
    expect(document.body.textContent).toContain("New verification secret");
    expect(document.body.textContent).toContain("The old secret stops working now.");
  });
});

describe("Deliveries", () => {
  test("lists each delivery's verdict, group and size; a verdict with a run opens it without opening the row", async () => {
    const places: AutomationsPlace[] = [];
    const c = await page(new FixtureClient("a"), { id: "aut_alert", tab: "deliveries" }, (p) => places.push(p));
    const rows = await until(() => { const r = [...c.querySelectorAll<HTMLElement>("[data-testid=delivery-row]")]; return r.length ? r : null; }, "the rows");
    expect(rows.map((r) => r.querySelector("[data-verdict]")!.textContent)).toEqual(["Sent to running agent", "Refused · Rate limited",
      "Sent to running agent", "Skipped", "Started run", "Skipped", "Refused · Bad signature", "Refused · Not JSON", "Refused · Too large"]);
    expect(rows[3]!.textContent).toContain("commonLabels.severity is warning, not one of critical, high");
    expect(rows[8]!.textContent).toContain("1.3 MiB");
    await click(rows[0]!.querySelector<HTMLElement>("button[aria-label='Sent to running agent: open the run']")!);
    expect(places).toEqual([{ id: "aut_alert", tab: "runs", runId: "arn_alert1" }]);
    await click(rows[3]!);
    expect(places.at(-1)).toEqual({ id: "aut_alert", tab: "deliveries", deliveryId: "dlv_6p0z" });
  });

  test("a skipped delivery's detail marks each condition and keeps its headers' secrets as ••••", async () => {
    const c = await page(new FixtureClient("a"), { id: "aut_alert", tab: "deliveries", deliveryId: "dlv_6p0z" });
    const d = await until(() => c.querySelector<HTMLElement>("[data-testid=delivery-detail]"), "the detail");
    expect([...d.querySelectorAll("[role=img]")].map((m) => m.getAttribute("aria-label"))).toEqual(["Holds", "Does not hold"]);
    expect(d.textContent).toContain("the body has warning");
    expect(d.textContent).toContain("sha256=••••••••");
    expect(d.querySelector("[data-testid=run-with-delivery]")).not.toBeNull();
  });

  test("a refused delivery keeps no body and offers no run", async () => {
    const c = await page(new FixtureClient("a"), { id: "aut_alert", tab: "deliveries", deliveryId: "dlv_3w7d" });
    const d = await until(() => c.querySelector<HTMLElement>("[data-testid=delivery-detail]"), "the detail");
    expect(d.textContent).toContain("Refused before it was read: no body is kept.");
    expect(d.querySelector("[data-testid=run-with-delivery]")).toBeNull();
  });

  test("a GitHub automation has the tab too, its rows naming the event", async () => {
    const c = await page(new FixtureClient("a"), { id: "aut_triage", tab: "deliveries" });
    const rows = await until(() => { const r = [...c.querySelectorAll<HTMLElement>("[data-testid=delivery-row]")]; return r.length ? r : null; }, "the rows");
    expect(rows[0]!.textContent).toContain("pull_request · opened · example/web-console");
    expect(rows[1]!.textContent).toContain("Author: it is by dependabot[bot], who is excluded");
  });
});

describe("Run now on a payload", () => {
  test("a written body is checked condition by condition, and the run takes it", async () => {
    const client = new FixtureClient("a");
    const asked: unknown[] = [];
    const run = client.runAutomation.bind(client);
    client.runAutomation = async (id, body) => { asked.push(body); return run(id, body); };
    const c = await page(client, { id: "aut_alert" });
    await click(await until(() => c.querySelector<HTMLElement>("[data-testid=run-now]"), "Run now"));
    const verdict = await until(() => document.body.querySelector<HTMLElement>("[data-testid=test-verdict]"), "the verdict");
    // The last delivery is the resolved one, of the live run's group.
    await until(() => (verdict.textContent?.includes("a run of this group is going") ? true : null), "the live group");
    await click(document.body.querySelector("[data-testid=test-run]")!);
    await until(() => (asked.length ? asked : null), "the run");
    expect(asked[0]).toMatchObject({ payload: { status: "resolved" } });
  });

  test("a past delivery that missed a condition says it would be skipped", async () => {
    const c = await page(new FixtureClient("a"), { id: "aut_alert", tab: "deliveries", deliveryId: "dlv_6p0z" });
    await click(await until(() => c.querySelector<HTMLElement>("[data-testid=run-with-delivery]"), "Run with this delivery"));
    const verdict = await until(() => {
      const v = document.body.querySelector<HTMLElement>("[data-testid=test-verdict]");
      return v?.textContent?.includes("It would be skipped") ? v : null;
    }, "the skip");
    expect(verdict.textContent).toContain("commonLabels.severity is warning, not one of critical, high");
  });
});

describe("a run an alert started", () => {
  test("names the delivery that started it, its group, and the later ones sent to it", async () => {
    const delivered: string[] = [];
    const c = await page(new FixtureClient("a"), { id: "aut_alert", tab: "runs", runId: "arn_alert1" },
      (p) => p.deliveryId && delivered.push(p.deliveryId));
    const t = await until(() => c.querySelector<HTMLElement>("[data-testid=run-trigger]"), "the trigger block");
    expect(t.textContent).toContain("dlv_5n3x");
    expect(t.textContent).toContain('{}/{alertname="HighErrorRate"}:{service="api"}');
    const sent = t.querySelector<HTMLElement>("[data-testid=sent-to-run]")!;
    expect([...sent.querySelectorAll("li")].map((li) => li.querySelector(".ds-mono")?.textContent)).toEqual(["dlv_7c5m", "dlv_9k2h"]);
    await click(button(sent, "dlv_9k2h")!);
    expect(delivered).toEqual(["dlv_9k2h"]);
  });

  test("shows its trigger block and its agent's tool calls on one page", async () => {
    const c = await page(new FixtureClient("a"), { id: "aut_alert", tab: "runs", runId: "arn_alert1" }, () => undefined);
    const detail = await until(() => c.querySelector<HTMLElement>("[data-testid=run-detail]"), "the run");
    const trigger = detail.querySelector<HTMLElement>("[data-testid=run-trigger]")!;
    const tools = detail.querySelector<HTMLElement>("[data-testid=run-tools]")!;
    expect(tools.textContent).toContain("1 tool call.");
    expect(tools.textContent).toContain("create_task");
    expect(trigger.compareDocumentPosition(tools) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(detail.querySelector("[data-testid=run-failed]")).toBeNull();
  });
});

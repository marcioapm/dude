/**
 * The Judgements page, mounted in happy-dom against the fixture client:
 * Off by default with its callout; picking a tier saves it and says what
 * it requests; a tier naming no model warns and offers no test; the test
 * question's answer, success or failure, says it is not recorded; recent
 * calls open their dialog, a failed one with its danger callout; a member
 * reads it all and changes nothing.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { ReactNode } from "react";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { JudgementCall, JudgementCallDetail, JudgementCallsResponse, JudgementSettings, JudgementTestResult } from "@dude/domain";
import { act, click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { MODEL_TIERS } from "../src/fixtures/data.ts";
import { JudgementsPage, useJudgementSettings } from "../src/screens/JudgementsSettings.tsx";
import { testAnswerWords, testTokensWords } from "../src/judgements.ts";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

const press = (el: Element) =>
  act(async () => void el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));

const CALL: JudgementCall = {
  id: "mcl_1", createdAt: new Date(Date.now() - 2 * 60_000).toISOString(), asker: "delivery.comment", about: "ACME-28 · acme#5477",
  question: "Should the fixer act on this comment?", decision: "no", confidence: 0.93, reasoning: "It asks for nothing to change.",
  status: "ok", error: null, tierName: "Coder", model: "claude-opus-5-5", latencyMs: 1840, inputTokens: 1204, outputTokens: 96,
};
const FAILED: JudgementCall = { ...CALL, id: "mcl_2", decision: null, confidence: null, reasoning: null, status: "error",
  error: "no answer from the LLM proxy in 1m0s", inputTokens: 0, outputTokens: 0 };

class Judging extends FixtureClient {
  settings: JudgementSettings = { tierId: null, canEdit: true };
  calls: JudgementCall[] = [];
  set: Array<string | null> = [];
  tests: string[] = [];
  testAnswer: JudgementTestResult = { ok: true, decision: "yes", confidence: 0.99, reasoning: "Rayleigh scattering.", model: "claude-opus-5-5",
    latencyMs: 1200, inputTokens: 380, outputTokens: 32, error: null };
  override judgementSettings() {
    return Promise.resolve(this.settings);
  }
  override setJudgementTier(tierId: string | null) {
    this.set.push(tierId);
    this.settings = { ...this.settings, tierId };
    return Promise.resolve(this.settings);
  }
  override judgements(): Promise<JudgementCallsResponse> {
    return Promise.resolve({ calls: this.calls, summary: { calls: this.calls.length, since: new Date().toISOString() },
      askers: [...new Set(this.calls.map((c) => c.asker))], next: null });
  }
  override judgement(id: string): Promise<JudgementCallDetail> {
    const c = this.calls.find((x) => x.id === id)!;
    return Promise.resolve({ ...c, request: { messages: [{ role: "system", content: "You decide." }, { role: "user", content: c.question }] },
      responseText: c.status === "ok" ? "{\"decision\":\"no\"}" : null });
  }
  override testJudgement(tierId: string) {
    this.tests.push(tierId);
    return Promise.resolve(this.testAnswer);
  }
}

function Page({ client }: { client: Judging }) {
  const judgement = useJudgementSettings(client);
  return <JudgementsPage client={client} orgName="Acme" tiers={MODEL_TIERS} tiersProblem={null} judgement={judgement} />;
}

async function show(client: Judging) {
  const node: ReactNode = <TooltipProvider><ToastProvider><Page client={client} /></ToastProvider></TooltipProvider>;
  const { container, unmount } = await mount(node);
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=judgements-page]"), "the page");
  return container;
}

async function pick(container: HTMLElement, value: string) {
  await press(container.querySelector("[data-testid=judgement-tier]")!);
  await click(await until(() => document.querySelector<HTMLElement>(`[role=option][data-value="${value}"]`), value));
}

describe("the Judgements page", () => {
  test("is Off by default: the picker's first option, and the info callout; no test question", async () => {
    const container = await show(new Judging("a"));
    expect(container.querySelector("[data-testid=judgement-tier]")?.textContent).toBe("Off — nothing is judged");
    expect(container.querySelector("[data-testid=judgements-off]")?.textContent).toContain("Judgements are off.");
    expect(container.querySelectorAll("[data-testid=judgement-test]").length).toBe(0);
    await press(container.querySelector("[data-testid=judgement-tier]")!);
    const options = await until(() => {
      const o = [...document.querySelectorAll<HTMLElement>("[role=option]")];
      return o.length ? o : null;
    }, "the options");
    expect(options.map((o) => o.getAttribute("data-value"))).toEqual(["__off__", "mtr_thinker", "mtr_coder", "mtr_fast"]);
  });

  test("picking a tier saves it and says what it requests; Off again saves null", async () => {
    const client = new Judging("a");
    const container = await show(client);
    await pick(container, "mtr_coder");
    expect(client.set).toEqual(["mtr_coder"]);
    const requests = await until(() => container.querySelector("[data-testid=judgement-tier-requests]"), "what it requests");
    expect(requests.textContent).toBe("Requests claude-opus-5-5 · medium effort · through the proxy’s /v1/chat/completions");
    expect(container.querySelectorAll("[data-testid=judgements-off]").length).toBe(0);
    expect(container.querySelector("[data-testid=judgements-explainer]")?.textContent).toContain("Coder → claude-opus-5-5");
    await pick(container, "__off__");
    expect(client.set).toEqual(["mtr_coder", null]);
  });

  test("a tier naming no model warns, and offers no test question", async () => {
    const client = new Judging("a");
    client.settings = { tierId: "mtr_fast", canEdit: true };
    const container = await show(client);
    expect(container.querySelector("[data-testid=judgement-tier-no-model]")?.textContent)
      .toBe("Fast names no model, so every judgement fails and its caller falls back. Give Fast a model, or pick another tier.");
    expect(container.querySelectorAll("[data-testid=judgement-test]").length).toBe(0);
    expect(container.querySelectorAll("[data-testid=judgement-tier-requests]").length).toBe(0);
  });

  test("the test question is asked of the picked tier; its answer, or its failure, says it is not recorded", async () => {
    const client = new Judging("a");
    client.settings = { tierId: "mtr_coder", canEdit: true };
    const container = await show(client);
    await click(container.querySelector("[data-testid=judgement-test]")!);
    const result = await until(() => container.querySelector("[data-testid=judgement-test-result]"), "the answer");
    expect(client.tests).toEqual(["mtr_coder"]);
    expect(result.getAttribute("data-ok")).toBe("true");
    expect(result.textContent).toBe("claude-opus-5-5 answered in 1.2s: Yes, 99% sure.“Is the sky blue on a clear day?” — “Rayleigh scattering.”Not recorded in Recent judgements; 412 tokens.");
    client.testAnswer = { ok: false, decision: null, confidence: null, reasoning: null, model: "claude-opus-5-5", latencyMs: 0, inputTokens: 0, outputTokens: 0,
      error: "no answer from the LLM proxy: dial tcp: lookup llm.dude.test: no such host" };
    await click(container.querySelector("[data-testid=judgement-test]")!);
    await until(() => container.querySelector("[data-testid=judgement-test-result][data-ok=false]"), "the failure");
    expect(container.querySelector("[data-testid=judgement-test-result]")?.textContent)
      .toBe("claude-opus-5-5 gave no answer: no answer from the LLM proxy: dial tcp: lookup llm.dude.test: no such host.Not recorded in Recent judgements.");
  });

  test("nothing asked yet: the empty state", async () => {
    const container = await show(new Judging("a"));
    const recent = await until(() => container.querySelector("[data-testid=recent-judgements]"), "Recent judgements");
    await until(() => (recent.textContent?.includes("Nothing has asked for a judgement yet") ? recent : null), "the empty state");
    expect(recent.querySelectorAll("[data-testid=judgements-table]").length).toBe(0);
    expect(recent.textContent).not.toContain("30 days");
  });

  test("recent calls list the answer, how sure, who asked and the tokens; a row opens the call; a failed one says so", async () => {
    const client = new Judging("a");
    client.calls = [CALL, FAILED];
    const container = await show(client);
    const rows = await until(() => {
      const r = [...container.querySelectorAll<HTMLElement>("[data-testid=judgement-row]")];
      return r.length ? r : null;
    }, "the rows");
    expect(rows[0]!.textContent).toContain("Should the fixer act on this comment?ACME-28 · acme#5477No93%delivery.comment1.8s1.3ktok");
    expect(rows[1]!.textContent).toContain("Failed—delivery.comment1.8s—");
    expect(rows[0]!.querySelector("[title='1,300 tokens']")).not.toBeNull();
    expect(container.textContent).toContain("Last 7 days · 2 calls");
    expect(container.textContent).not.toContain("$");
    await click(rows[0]!);
    const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the dialog");
    expect(dialog.querySelector("[data-testid=judgement-answer]")?.textContent).toBe("No93% sureIt asks for nothing to change.");
    expect(dialog.textContent).toContain("in1.2k · out96");
    expect(dialog.querySelector("[title='1,204 in tokens']")).not.toBeNull();
    await until(() => (dialog.textContent?.includes("What was sent") ? dialog : null), "the disclosures");
    expect(dialog.textContent).toContain("What the model answered, as it came");
    await click([...dialog.querySelectorAll("button")].find((b) => b.textContent === "Close")!);
    await click(rows[1]!);
    const failed = await until(() => document.querySelector<HTMLElement>("[role=dialog] [data-testid=judgement-failed]"), "the failure");
    expect(failed.textContent).toBe("No answer. no answer from the LLM proxy in 1m0s. The caller fell back to what it does without a judgement.");
  });

  test("a row says what it opens, and opens on Enter or Space", async () => {
    const client = new Judging("a");
    client.calls = [CALL];
    const container = await show(client);
    const row = await until(() => container.querySelector<HTMLElement>("[data-testid=judgement-row]"), "the row");
    expect(row.getAttribute("aria-label")).toBe("Open the judgement: Should the fixer act on this comment?");
    const close = async () => {
      const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the dialog");
      await click([...dialog.querySelectorAll("button")].find((b) => b.textContent === "Close")!);
      await until(() => (document.querySelector("[role=dialog]") ? null : true), "the dialog closed");
    };
    for (const key of ["Enter", " "]) {
      const ev = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
      await act(async () => void row.dispatchEvent(ev));
      expect(ev.defaultPrevented).toBe(true);
      await close();
    }
    await act(async () => void row.dispatchEvent(new KeyboardEvent("keydown", { key: "a", bubbles: true })));
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });

  test("a member reads it all and changes nothing: the picker is disabled, no test question", async () => {
    const client = new Judging("a");
    client.settings = { tierId: "mtr_coder", canEdit: false };
    const container = await show(client);
    const picker = container.querySelector<HTMLButtonElement>("[data-testid=judgement-tier]")!;
    expect(picker.disabled).toBe(true);
    expect(container.querySelectorAll("[data-testid=judgement-test]").length).toBe(0);
    expect(container.querySelector("[data-testid=judgement-tier-requests]")).not.toBeNull();
  });
});

describe("the words", () => {
  test("the test's answer reads its time as the design system formats a duration", () => {
    const failed = { ok: false, decision: null, confidence: null, reasoning: null, model: "m", inputTokens: 0, outputTokens: 0,
      error: "the model's answer is not a decision: no JSON object in it" };
    expect(testAnswerWords({ ...failed, latencyMs: 30_000 })).toBe("gave no answer in 30s: the model's answer is not a decision: no JSON object in it.");
    expect(testAnswerWords({ ...failed, latencyMs: 75_000 })).toBe("gave no answer in 1m 15s: the model's answer is not a decision: no JSON object in it.");
    expect(testTokensWords({ inputTokens: 1204, outputTokens: 96 })).toBe("; 1.3k tokens");
    expect(testTokensWords({ inputTokens: 0, outputTokens: 0 })).toBe("");
  });
});

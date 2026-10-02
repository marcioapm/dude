/**
 * The Models page and a role's tier field, mounted in happy-dom against the
 * fixture client: the table as the tiers read, the edit dialog's
 * suggestions and its warning for a name the proxy does not list, a test
 * message's answer, the remove dialog for a tier in use, and the role's
 * picker on the organisation and on a project.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { ModelTiersResponse } from "@dude/domain";
import { act, click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { MODEL_TIERS, PROJECT } from "../src/fixtures/data.ts";
import { ModelsPage } from "../src/screens/ModelsSettings.tsx";
import { ProjectSettingsScreen } from "../src/screens/ProjectSettingsScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

const press = (el: Element) =>
  act(async () => void el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));

class TestingClient extends FixtureClient {
  tested: Array<[string, string | null]> = [];
  override testModel(model: string, tierId: string | null) {
    this.tested.push([model, tierId]);
    return Promise.resolve({ model, results: [
      { efforts: ["high", "max"], sent: "high", ok: true, latencyMs: 1200, status: 200, error: null },
      { efforts: ["low"], sent: "low", ok: false, latencyMs: 30, status: 404, error: "no such model" },
    ] });
  }
}

const TIERS: ModelTiersResponse = { tiers: MODEL_TIERS, canEdit: true, upgrade: [] };

async function page(client = new TestingClient("a"), tiers = TIERS) {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <ModelsPage client={client} orgName="Acme" tiers={tiers} problem={null} setTiers={() => {}} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  return container;
}

async function menuItem(container: HTMLElement, tier: string, label: string) {
  await press(container.querySelector<HTMLButtonElement>(`[aria-label="Actions for ${tier}"]`)!);
  const item = await until(() => [...document.querySelectorAll<HTMLElement>("[role=menuitem]")].find((i) => i.textContent?.includes(label)), label);
  await click(item);
  return until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the dialog");
}

describe("the Models page", () => {
  test("lists each tier with the model it requests, who changed it, and who uses it; one with none says Not set", async () => {
    const container = await page();
    const row = (name: string) => container.querySelector<HTMLElement>(`[data-tier="${name}"]`)!;
    expect(row("Thinker").querySelector("[data-model-cell]")?.textContent).toBe("claude-fable-5-1");
    expect(row("Coder").textContent).toContain("Writes and fixes code for hours at a time.");
    expect(row("Coder").textContent).toContain("2 agents");
    expect(row("Fast").querySelector("[data-model-cell]")?.textContent).toBe("Not set");
    expect(row("Fast").querySelector("[data-unset]")).not.toBeNull();
    expect(container.querySelector("[data-testid=models-explainer]")?.textContent).toContain("Coder → claude-opus-5-5");
  });

  test("a member sees no Add tier and no row menu", async () => {
    const container = await page(new TestingClient("a"), { ...TIERS, canEdit: false });
    expect(container.querySelector("[data-testid=add-model-tier]")).toBeNull();
    expect(container.querySelector('[aria-label="Actions for Coder"]')).toBeNull();
  });

  test("the edit dialog offers the proxy's names; one it does not list warns, and Save reads Save anyway", async () => {
    const container = await page();
    const dialog = await menuItem(container, "Coder", "Change model…");
    expect(dialog.textContent).toContain("On Coder now: Implementer, Fixer, at effort high.");
    const chips = [...dialog.querySelectorAll<HTMLButtonElement>("[aria-label='Names the proxy knows'] button")];
    expect(chips.map((c) => c.textContent)).toEqual(["claude-opus-5-5", "claude-fable-5-1", "gpt-5.6-sol"]);
    expect(chips[0]!.getAttribute("aria-pressed")).toBe("true");
    const save = dialog.querySelector<HTMLButtonElement>("[data-testid=model-tier-save]")!;
    expect(save.textContent).toBe("Save");
    const input = dialog.querySelector<HTMLInputElement>("[data-testid=model-tier-model]")!;
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(input, "gemini-3.8-pro");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(dialog.querySelector("[data-testid=model-tier-unlisted]")?.textContent).toContain("The proxy doesn’t list gemini-3.8-pro.");
    expect(save.textContent).toBe("Save anyway");
    await click(chips[2]!);
    expect(dialog.querySelector("[data-testid=model-tier-unlisted]")).toBeNull();
    expect(save.textContent).toBe("Save");
  });

  test("Send a test message tries the tier's model and shows each effort's answer", async () => {
    const client = new TestingClient("a");
    const container = await page(client);
    const dialog = await menuItem(container, "Coder", "Send a test message");
    const results = await until(() => {
      const r = dialog.querySelectorAll("[data-testid=model-tier-test-result]");
      return r.length === 2 ? [...r] : null;
    }, "the test's answers");
    expect(client.tested).toEqual([["claude-opus-5-5", "mtr_coder"]]);
    expect(results.map((r) => [r.getAttribute("data-ok"), r.textContent])).toEqual([
      ["true", "claude-opus-5-5 answered (efforts high, max) in 1.2 s (Implementer, Fixer)."],
      ["false", "claude-opus-5-5 (effort low): the proxy answered 404 — no such model."],
    ]);
  });

  test("removing a tier in use lists who uses it and asks where they go", async () => {
    const container = await page();
    const dialog = await menuItem(container, "Coder", "Remove…");
    expect(dialog.textContent).toContain("Remove Coder?");
    expect(dialog.textContent).toContain("Implementer and Fixer use it. Choose the tier they get instead; nothing is left on a tier that is gone.");
    expect(dialog.querySelector("[data-testid=model-tier-move]")?.textContent).toBe("Thinkerclaude-fable-5-1");
    expect(dialog.querySelector("[data-testid=remove-model-tier]")?.textContent).toBe("Remove and move them");
  });

  test("the upgrade's notes show until Done", async () => {
    let dismissed = 0;
    class Upgraded extends TestingClient {
      override dismissTierUpgrade() {
        dismissed++;
        return Promise.resolve(TIERS);
      }
    }
    const container = await page(new Upgraded("a"), { ...TIERS, upgrade: [
      { id: 1, role: "simplifier", project: null, oldModel: "llm-anthropic/claude-sonnet-5-5", tierId: "mtr_thinker", tierName: "Thinker", newTier: false, modelChanged: true },
      { id: 2, role: "reviewer", project: { id: "prj_abs", name: "abs", imageUrl: null }, oldModel: "llm-openai/gpt-5.6-sol", tierId: "mtr_x", tierName: "gpt-5.6-sol", newTier: true, modelChanged: false },
    ] });
    const banner = container.querySelector<HTMLElement>("[data-testid=tier-upgrade]")!;
    expect(banner.textContent).toContain("One role now asks for a different model than before, and one project override matched no tier and got its own — check them.");
    expect(banner.querySelector('[data-note="llm-anthropic/claude-sonnet-5-5"]')?.textContent).toBe("llm-anthropic/claude-sonnet-5-5Simplifier→ Thinker now claude-fable-5-1");
    // The project's face reads its initials first.
    expect(banner.querySelector('[data-note="llm-openai/gpt-5.6-sol"]')?.textContent).toBe("ABllm-openai/gpt-5.6-solabs · Reviewer→ New tier “gpt-5.6-sol”");
    await click(banner.querySelector("[data-testid=tier-upgrade-done]")!);
    expect(dismissed).toBe(1);
  });
});

describe("a role's tier", () => {
  test("on a project: the organisation's tier first, every tier after, and the tiers are the organisation's", async () => {
    const { container, unmount } = await mount(
      <TooltipProvider>
        <ToastProvider>
          <ProjectSettingsScreen client={new FixtureClient("a")} projectId={PROJECT.id} projects={[]} admin page="reviewer"
            onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={() => {}} />
        </ToastProvider>
      </TooltipProvider>,
    );
    mounted.push(unmount);
    const field = await until(() => container.querySelector<HTMLButtonElement>("[data-testid=role-tier]"), "the Model select");
    expect(field.getAttribute("aria-label")).toBe("Model");
    expect(field.textContent).toBe("From Example · Thinker");
    await press(field);
    const options = await until(() => {
      const o = [...document.querySelectorAll<HTMLElement>("[role=option]")];
      return o.length ? o : null;
    }, "the options");
    expect(options.map((o) => o.getAttribute("data-value"))).toEqual(["__inherit__", "mtr_thinker", "mtr_coder", "mtr_fast"]);
    expect(options[0]!.textContent).toBe("From Example · Thinkerclaude-fable-5-1");
    expect(options[3]!.textContent).toContain("Not set");
    expect(document.body.textContent).toContain("Tiers are Example’s — ask an admin to change one");
  });
});

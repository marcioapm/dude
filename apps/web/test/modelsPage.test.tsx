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
  await press(button(container, `Actions for ${tier}`)!);
  const item = await until(() => [...document.querySelectorAll<HTMLElement>("[role=menuitem]")].find((i) => i.textContent?.includes(label)), label);
  await click(item);
  return until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the dialog");
}

/** A button by its accessible name: its aria-label, else its text. */
function button(within: ParentNode, name: string): HTMLButtonElement | undefined {
  return [...within.querySelectorAll<HTMLButtonElement>("button, [role=button]")]
    .find((b) => (b.getAttribute("aria-label") ?? b.textContent?.trim()) === name);
}

/** A form control by the text of the <label> that names it. */
function byLabel<T extends HTMLElement>(within: ParentNode, text: string): T | null {
  const label = [...within.querySelectorAll<HTMLLabelElement>("label")].find((l) => l.textContent?.trim() === text);
  return label?.htmlFor ? (document.getElementById(label.htmlFor) as T | null) : null;
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
    expect(button(container, "Actions for Coder")).toBeUndefined();
  });

  test("the edit dialog offers the proxy's names; one it does not list warns, and Save reads Save anyway", async () => {
    const container = await page();
    const dialog = await menuItem(container, "Coder", "Change model…");
    expect(dialog.textContent).toContain("On Coder now: Implementer, Fixer, at effort high.");
    const chips = [...dialog.querySelectorAll<HTMLButtonElement>("[aria-label='Names the proxy knows'] button")];
    expect(chips.map((c) => c.textContent)).toEqual(["claude-opus-5-5", "claude-fable-5-1", "gpt-5.6-sol"]);
    expect(chips[0]!.getAttribute("aria-pressed")).toBe("true");
    expect(button(dialog, "Save")).toBeDefined();
    expect(button(dialog, "Save anyway")).toBeUndefined();
    const input = byLabel<HTMLInputElement>(dialog, "Model to request")!;
    expect(input.value).toBe("claude-opus-5-5");
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(input, "gemini-3.8-pro");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(dialog.querySelector("[data-testid=model-tier-unlisted]")?.textContent).toContain("The proxy doesn’t list gemini-3.8-pro.");
    expect(button(dialog, "Save anyway")).toBeDefined();
    expect(button(dialog, "Save")).toBeUndefined();
    await click(chips[2]!);
    expect(dialog.querySelector("[data-testid=model-tier-unlisted]")).toBeNull();
    expect(button(dialog, "Save")).toBeDefined();
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
  async function projectRole(admin: boolean, client = new FixtureClient("a"), onOrganization: (page: string) => void = () => {}) {
    const { container, unmount } = await mount(
      <TooltipProvider>
        <ToastProvider>
          <ProjectSettingsScreen client={client} projectId={PROJECT.id} projects={[]} admin={admin} page="reviewer"
            onPage={() => {}} onChanged={() => {}} onBack={() => {}} onOrganization={onOrganization} />
        </ToastProvider>
      </TooltipProvider>,
    );
    mounted.push(unmount);
    return container;
  }
  const openOptions = async (container: HTMLElement) => {
    await press(await until(() => container.querySelector<HTMLButtonElement>("[data-testid=role-tier]"), "the Model select"));
    return until(() => {
      const o = [...document.querySelectorAll<HTMLElement>("[role=option]")];
      return o.length ? o : null;
    }, "the options");
  };

  test("on a project: the organisation's tier first, every tier after; an admin is offered Models", async () => {
    const pages: string[] = [];
    const container = await projectRole(true, new FixtureClient("a"), (p) => pages.push(p));
    const field = await until(() => container.querySelector<HTMLButtonElement>("[data-testid=role-tier]"), "the Model select");
    expect(field.getAttribute("aria-label")).toBe("Model");
    expect(field.textContent).toBe("From Example · Thinker");
    const options = await openOptions(container);
    expect(options.map((o) => o.getAttribute("data-value"))).toEqual(["__inherit__", "mtr_thinker", "mtr_coder", "mtr_fast"]);
    expect(options[0]!.textContent).toBe("From Example · Thinkerclaude-fable-5-1");
    expect(options[3]!.textContent).toContain("Not set");
    expect(document.body.textContent).not.toContain("ask an admin");
    await click(button(document, "Manage tiers in Models")!);
    expect(pages).toEqual(["models"]);
  });

  test("on a project, for someone not an admin: the tiers are the organisation's, ask an admin", async () => {
    const container = await projectRole(false);
    await openOptions(container);
    expect(document.body.textContent).toContain("Tiers are Example’s — ask an admin to change one");
    expect(button(document, "Manage tiers in Models")).toBeUndefined();
  });

  test("tiers that could not be loaded say so, not just No tier", async () => {
    class Failing extends FixtureClient {
      override modelTiers(): Promise<ModelTiersResponse> {
        return Promise.reject(new Error("the backend answered 503"));
      }
    }
    const container = await projectRole(true, new Failing("a"));
    const problem = await until(() => container.querySelector("[data-testid=role-tier-problem]"), "the tiers' problem");
    expect(problem.textContent).toBe("Example’s tiers could not be loaded: the backend answered 503");
  });
});

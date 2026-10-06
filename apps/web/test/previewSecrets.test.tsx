/**
 * Servers → Branch previews → Secrets: the row lists names and hints, says
 * so when there are none, and is read-only for someone who cannot edit; the
 * Add and Replace dialogs refuse what the API would, in the mockup's words,
 * and every change says so in a toast naming the secret. Mounted in
 * happy-dom against the fixture client, which keeps no value.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { PreviewSecret, RecipeEnvNames } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PreviewSecretsRow, SECRETS_HELP, SECRETS_NOTE } from "../src/screens/PreviewSecrets.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

const SECRETS: PreviewSecret[] = [
  { name: "SEED_LLM_KEY", hint: "3f9a", updatedAt: new Date(Date.now() - 2 * 36e5).toISOString(), updatedBy: { id: "u_m", name: "Márcio" } },
  { name: "STRIPE_TEST_KEY", hint: "x7Qb", updatedAt: new Date(Date.now() - 3 * 864e5).toISOString(), updatedBy: { id: "u_r", name: "Rui" } },
];
const RECIPES: RecipeEnvNames[] = [{ name: "web", env: [{ name: "PORT" }, { name: "NODE_ENV" }] }];

/** Records what the row asked of the API, and answers as the API would. */
class Recording extends FixtureClient {
  asked: string[] = [];
  override async addProjectSecret(projectId: string, name: string, value: string) {
    this.asked.push(`add ${name} ${JSON.stringify(value)}`);
    return super.addProjectSecret(projectId, name, value);
  }
  override async replaceProjectSecret(projectId: string, name: string, value: string) {
    this.asked.push(`replace ${name} ${JSON.stringify(value)}`);
    return super.replaceProjectSecret(projectId, name, value);
  }
  override async removeProjectSecret(projectId: string, name: string) {
    this.asked.push(`remove ${name}`);
    return super.removeProjectSecret(projectId, name);
  }
}

async function render({ secrets = SECRETS, canEdit = true }: { secrets?: PreviewSecret[]; canEdit?: boolean } = {}) {
  const client = new Recording("a");
  let changed = 0;
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <PreviewSecretsRow client={client} projectId="prj" projectName="Jervasion" secrets={secrets} recipes={RECIPES} canEdit={canEdit}
          onChanged={() => changed++} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  return { container, client, changed: () => changed };
}

const press = (el: Element) =>
  act(async () => void el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
const dialog = () => until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the dialog");
const toasts = () => [...document.querySelectorAll("[data-toast]")].map((t) => t.textContent ?? "");

async function typeInto(el: HTMLInputElement | HTMLTextAreaElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function menuItem(container: HTMLElement, secret: string, label: string) {
  await press(container.querySelector(`[aria-label="Actions for ${secret}"]`)!);
  const item = await until(() => [...document.querySelectorAll<HTMLElement>("[role=menuitem]")].find((i) => i.textContent?.includes(label)), label);
  await click(item);
}

describe("the Secrets row", () => {
  test("lists each secret by name and hint, with the help and the note", async () => {
    const { container } = await render();
    const row = container.querySelector("[data-testid=preview-secrets]")!;
    expect(row.textContent).toContain("Secrets");
    expect(row.textContent).toContain(SECRETS_HELP);
    const rows = [...row.querySelectorAll("tr[data-secret]")].map((r) => [...r.querySelectorAll("td")].slice(0, 2).map((c) => c.textContent));
    expect(rows).toEqual([["SEED_LLM_KEY", "…3f9a"], ["STRIPE_TEST_KEY", "…x7Qb"]]);
    expect(row.textContent).toContain(SECRETS_NOTE);
    expect(SECRETS_NOTE).toBe("A new value reaches running previews when they next wake. A secret added or removed reaches previews started after the change; restart a preview to give it the change now.");
    expect(row.querySelector("[data-testid=add-secret]")?.textContent).toBe("Add secret");
    // Who changed it is the row's tooltip, not a column.
    expect(row.querySelector("thead")?.textContent).toBe("NameValueActions");
  });

  test("a row's tooltip says who changed it and when", async () => {
    const { container } = await render();
    const row = container.querySelector<HTMLElement>("tr[data-secret=SEED_LLM_KEY]")!;
    await act(async () => void row.dispatchEvent(new PointerEvent("pointermove", { bubbles: true, pointerType: "mouse" })));
    const tip = await until(() => document.querySelector("[role=tooltip]")?.textContent, "the tooltip");
    expect(tip).toMatch(/^Changed by Márcio · /);
  });

  test("says when there are none, with Add secret", async () => {
    const { container } = await render({ secrets: [] });
    const empty = container.querySelector("[data-testid=secrets-empty]")!;
    expect(empty.textContent).toContain("No secrets");
    expect(empty.textContent).toContain("Add a value a preview needs but shouldn’t be in the repository: a seed script’s API key, a test payment key.");
    expect(empty.querySelector("[data-testid=add-secret]")).not.toBeNull();
    expect(container.querySelector("table")).toBeNull();
  });

  test("is read-only for someone who cannot edit: no menus, no Add", async () => {
    const { container } = await render({ canEdit: false });
    expect(container.querySelectorAll("tr[data-secret]")).toHaveLength(2);
    expect(container.querySelector("[aria-label^='Actions for']")).toBeNull();
    expect(container.querySelector("[data-testid=add-secret]")).toBeNull();
    const empty = await render({ secrets: [], canEdit: false });
    expect(empty.container.textContent).toContain("No secrets");
    expect(empty.container.querySelector("[data-testid=add-secret]")).toBeNull();
  });
});

describe("Add a secret", () => {
  async function open() {
    const r = await render();
    await click(r.container.querySelector("[data-testid=add-secret]")!);
    const d = await dialog();
    return {
      ...r,
      d,
      name: d.querySelector<HTMLInputElement>("[data-testid=secret-name]")!,
      value: d.querySelector<HTMLTextAreaElement>("textarea")!,
      submit: d.ownerDocument.querySelector<HTMLButtonElement>("[data-testid=secret-save]")!,
    };
  }

  test("says what it is, and Add stays disabled until both fields are valid", async () => {
    const { d, name, value, submit } = await open();
    expect(d.textContent).toContain("Add a secret");
    expect(d.textContent).toContain("Every branch preview of Jervasion gets it as an environment variable.");
    expect(d.textContent).toContain("Letters, digits and _, starting with a letter or _. Upper case by convention.");
    expect(d.textContent).toContain("Saved once. dude shows only the last 4 characters after this.");
    expect(submit.disabled).toBe(true);
    await typeInto(name, "SIGNING_KEY_PEM");
    expect(submit.disabled).toBe(true);
    const pem = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7\nk3Lq9bF0rT2yVf8mWJxQ1s0ZpN4cD6eH5aR7uGvKtYwE9iL3oM\n-----END PRIVATE KEY-----";
    await typeInto(value, pem);
    expect(d.querySelector("[data-testid=secret-length]")?.textContent).toBe("4 lines · 157 characters");
    expect(submit.disabled).toBe(false);
  });

  test("each name the API refuses is refused here, in the mockup's words", async () => {
    const { d, name, value, submit } = await open();
    await typeInto(value, "sk-test-4b1d");
    for (const [typed, said] of [
      ["SEED-LLM-KEY", "Use letters, digits and _ only, starting with a letter or _."],
      ["LUX_TOKEN", "Names starting with LUX_ are lux’s own."],
      ["lux_token", "Names starting with LUX_ are lux’s own."],
      ["GIT_TOKEN", "dude sets GIT_TOKEN itself, from the GitHub connection."],
      ["SEED_LLM_KEY", "There is already a SEED_LLM_KEY. Replace its value instead."],
      ["PORT", "Server web sets PORT in its own environment, which would override this. Rename one of them."],
    ]) {
      await typeInto(name, typed!);
      expect(`${typed}: ${d.textContent?.includes(said!)}`).toBe(`${typed}: true`);
      expect(name.getAttribute("aria-invalid")).toBe("true");
      expect(submit.disabled).toBe(true);
    }
  });

  test("a value with a NUL is refused; one made of spaces is a value", async () => {
    const { d, name, value, submit } = await open();
    await typeInto(name, "OK_NAME");
    await typeInto(value, "a\0b");
    expect(d.textContent).toContain("A value cannot contain a NUL character.");
    expect(submit.disabled).toBe(true);
    await typeInto(value, "  ");
    expect(submit.disabled).toBe(false);
  });

  test("adds it as typed, and the toast names it", async () => {
    const { client, name, value, submit, changed } = await open();
    await typeInto(name, "NEW_KEY");
    await typeInto(value, "line 1\nline 2\n");
    await click(submit);
    await until(() => toasts().some((t) => t.includes("NEW_KEY added")), "the toast");
    expect(client.asked).toEqual([`add NEW_KEY ${JSON.stringify("line 1\nline 2\n")}`]);
    expect(changed()).toBe(1);
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });
});

describe("Replace and Remove", () => {
  test("Replace says what it ends in now and when previews get the new one; the toast names it", async () => {
    const { container, client } = await render();
    await menuItem(container, "SEED_LLM_KEY", "Replace value");
    const d = await dialog();
    expect(d.textContent).toContain("Replace SEED_LLM_KEY");
    expect(d.textContent).toContain("Now ends in 3f9a.");
    expect(d.textContent).toContain("Running previews get the new value when they next wake.");
    const submit = document.querySelector<HTMLButtonElement>("[data-testid=secret-replace]")!;
    expect(submit.disabled).toBe(true);
    await typeInto(d.querySelector("textarea")!, "sk-live-e2b8");
    expect(submit.disabled).toBe(false);
    await click(submit);
    await until(() => toasts().some((t) => t.includes("SEED_LLM_KEY value replaced")), "the toast");
    expect(client.asked).toEqual([`replace SEED_LLM_KEY "sk-live-e2b8"`]);
  });

  test("the row menu is Replace value, a separator, then Remove in danger", async () => {
    const { container } = await render();
    await press(container.querySelector(`[aria-label="Actions for STRIPE_TEST_KEY"]`)!);
    const menu = await until(() => document.querySelector("[role=menu]"), "the menu");
    const items = [...menu.querySelectorAll("[role=menuitem], [role=separator]")].map((el) => el.getAttribute("role") === "separator" ? "—" : el.textContent);
    expect(items).toEqual(["Replace value", "—", "Remove"]);
  });

  test("Remove asks, then removes it, and the toast names it", async () => {
    const { container, client } = await render();
    await menuItem(container, "STRIPE_TEST_KEY", "Remove");
    const d = await dialog();
    expect(d.textContent).toContain("Remove STRIPE_TEST_KEY?");
    await click(document.querySelector("[data-testid=secret-remove]")!);
    await until(() => toasts().some((t) => t.includes("STRIPE_TEST_KEY removed")), "the toast");
    expect(client.asked).toEqual(["remove STRIPE_TEST_KEY"]);
    await settle();
  });
});

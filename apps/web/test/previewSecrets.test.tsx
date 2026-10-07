/**
 * Servers → Branch previews → Secrets: the row lists names and hints, says
 * so when there are none, and is read-only for someone who cannot edit; the
 * Add and Replace dialogs refuse what the API would, in the mockup's words,
 * and every change says so in a toast naming the secret. Mounted in
 * happy-dom against the fixture client, which keeps no value; controls are
 * found by role and label, as assistive technology finds them.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { PreviewSecret, RecipeEnvNames } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { allByRole, byLabel, byRole } from "../../../packages/design-system/test/queries.ts";
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
const PEM = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7\nk3Lq9bF0rT2yVf8mWJxQ1s0ZpN4cD6eH5aR7uGvKtYwE9iL3oM\n-----END PRIVATE KEY-----";

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
const dialog = () => until(() => allByRole(document.body, "dialog")[0], "the dialog");
const toasts = () => [...document.querySelectorAll("[data-toast]")].map((t) => t.textContent ?? "");
/** The row's table as names and shown values. */
const tableRows = (scope: HTMLElement) =>
  allByRole(byRole(scope, "table", "Secrets"), "row").slice(1).map((r) => allByRole(r, "cell").slice(0, 2).map((c) => c.textContent));

async function typeInto(el: HTMLInputElement | HTMLTextAreaElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

/** A paste of text into the masked value, as the browser sends one: every line of it. */
async function pasteInto(el: HTMLElement, text: string) {
  await act(async () => {
    const e = new Event("paste", { bubbles: true, cancelable: true }) as Event & { clipboardData: unknown };
    e.clipboardData = { getData: (type: string) => (type === "text/plain" ? text : "") };
    el.dispatchEvent(e);
  });
}

async function menuItem(container: HTMLElement, secret: string, label: string) {
  await press(byRole(container, "button", `Actions for ${secret}`));
  await click(await until(() => allByRole(document.body, "menuitem", label)[0], label));
}

describe("the Secrets row", () => {
  test("lists each secret by name and hint, with the help and the note", async () => {
    const { container } = await render();
    expect(container.textContent).toContain("Secrets");
    expect(container.textContent).toContain(SECRETS_HELP);
    expect(tableRows(container)).toEqual([["SEED_LLM_KEY", "…3f9a"], ["STRIPE_TEST_KEY", "…x7Qb"]]);
    expect(container.textContent).toContain(SECRETS_NOTE);
    expect(SECRETS_NOTE).toBe("A new value reaches running previews when they next wake. A secret added or removed reaches previews started after the change; restart a preview to give it the change now.");
    byRole(container, "button", "Add secret");
    // Who changed it is the row's tooltip, not a column.
    expect(allByRole(byRole(container, "table", "Secrets"), "columnheader").map((h) => h.textContent)).toEqual(["Name", "Value", "Actions"]);
  });

  test("a row's tooltip says who changed it and when", async () => {
    const { container } = await render();
    const row = allByRole(byRole(container, "table", "Secrets"), "row").find((r) => r.textContent?.includes("SEED_LLM_KEY"))!;
    await act(async () => void row.dispatchEvent(new PointerEvent("pointermove", { bubbles: true, pointerType: "mouse" })));
    const tip = await until(() => allByRole(document.body, "tooltip")[0]?.textContent, "the tooltip");
    expect(tip).toMatch(/^Changed by Márcio · /);
  });

  test("says when there are none, with Add secret", async () => {
    const { container } = await render({ secrets: [] });
    expect(container.textContent).toContain("No secrets");
    expect(container.textContent).toContain("Add a value a preview needs but shouldn’t be in the repository: a seed script’s API key, a test payment key.");
    byRole(container, "button", "Add secret");
    expect(allByRole(container, "table")).toHaveLength(0);
  });

  test("is read-only for someone who cannot edit: no menus, no Add", async () => {
    const { container } = await render({ canEdit: false });
    expect(tableRows(container)).toHaveLength(2);
    expect(allByRole(container, "button").filter((b) => b.getAttribute("aria-label")?.startsWith("Actions for"))).toHaveLength(0);
    expect(allByRole(container, "button", "Add secret")).toHaveLength(0);
    const empty = await render({ secrets: [], canEdit: false });
    expect(empty.container.textContent).toContain("No secrets");
    expect(allByRole(empty.container, "button", "Add secret")).toHaveLength(0);
  });
});

describe("Add a secret", () => {
  async function open() {
    const r = await render();
    await click(byRole(r.container, "button", "Add secret"));
    const d = await dialog();
    return {
      ...r,
      d,
      name: byLabel<HTMLInputElement>(d, "Name"),
      value: () => byLabel<HTMLInputElement | HTMLTextAreaElement>(d, "Value"),
      submit: byRole<HTMLButtonElement>(d, "button", "Add secret"),
      cancel: byRole<HTMLButtonElement>(d, "button", "Cancel"),
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
    // Masked, a paste brings every line.
    expect(value().getAttribute("type")).toBe("password");
    await pasteInto(value(), PEM);
    expect(d.textContent).toContain("4 lines · 157 characters");
    expect(submit.disabled).toBe(false);
  });

  test("each name the API refuses is refused here, in the mockup's words", async () => {
    const { d, name, value, submit } = await open();
    await typeInto(value(), "sk-test-4b1d");
    for (const [typed, said] of [
      ["SEED-LLM-KEY", "Use letters, digits and _ only, starting with a letter or _."],
      ["LUX_TOKEN", "Names starting with LUX_ are lux’s own."],
      ["lux_token", "Names starting with LUX_ are lux’s own."],
      ["GIT_TOKEN", "dude sets GIT_TOKEN itself, from the GitHub connection."],
      ["DUDE_TOOLS_AUTH", "dude sets DUDE_TOOLS_AUTH itself."],
      ["DUDE_REGISTRY_AUTH", "dude sets DUDE_REGISTRY_AUTH itself."],
      ["A".repeat(64), "At most 63 characters."],
      ["SEED_LLM_KEY", "There is already a SEED_LLM_KEY. Replace its value instead."],
      ["PORT", "Server web sets PORT in its own environment, which would override this. Rename one of them."],
    ]) {
      await typeInto(name, typed!);
      expect(`${typed}: ${d.textContent?.includes(said!)}`).toBe(`${typed}: true`);
      expect(name.getAttribute("aria-invalid")).toBe("true");
      expect(submit.disabled).toBe(true);
    }
    // 63 is a name.
    await typeInto(name, "A".repeat(63));
    expect(name.getAttribute("aria-invalid")).toBeNull();
    expect(submit.disabled).toBe(false);
  });

  test("a value is at most 32 KiB in UTF-8 bytes, not characters", async () => {
    const { d, name, value, submit } = await open();
    await typeInto(name, "BIG_KEY");
    // 32 766 ASCII bytes and one 2-byte character: 32 768 bytes, the most.
    await typeInto(value(), "x".repeat(32 * 1024 - 2) + "é");
    expect(d.textContent).not.toContain("At most 32 KiB.");
    expect(submit.disabled).toBe(false);
    // One more ASCII character: 32 769 bytes, though only 32 768 characters.
    await typeInto(value(), "x".repeat(32 * 1024 - 1) + "é");
    expect(d.textContent).toContain("At most 32 KiB.");
    expect(byLabel(d, "Value").getAttribute("aria-invalid")).toBe("true");
    expect(submit.disabled).toBe(true);
  });

  test("a value with a NUL is refused; one made of spaces is a value", async () => {
    const { d, name, value, submit } = await open();
    await typeInto(name, "OK_NAME");
    await typeInto(value(), "a\0b");
    expect(d.textContent).toContain("A value cannot contain a NUL character.");
    expect(submit.disabled).toBe(true);
    await typeInto(value(), "  ");
    expect(submit.disabled).toBe(false);
  });

  test("adds it as typed, and the toast names it", async () => {
    const { client, d, name, value, submit, changed } = await open();
    await typeInto(name, "NEW_KEY");
    // Shown, a line break is typed.
    await click(byRole(d, "button", "Show value"));
    expect(value().tagName).toBe("TEXTAREA");
    await typeInto(value(), "line 1\nline 2\n");
    await click(submit);
    await until(() => toasts().some((t) => t.includes("NEW_KEY added")), "the toast");
    expect(client.asked).toEqual([`add NEW_KEY ${JSON.stringify("line 1\nline 2\n")}`]);
    expect(changed()).toBe(1);
    expect(allByRole(document.body, "dialog")).toHaveLength(0);
  });

  test("Cancel clears the value before Add is opened again", async () => {
    const { container, name, value, cancel, client } = await open();
    await typeInto(name, "CANCEL_KEY");
    await typeInto(value(), "cancel-secret-canary");
    await click(cancel);
    expect(allByRole(document.body, "dialog")).toHaveLength(0);
    expect(document.body.innerHTML).not.toContain("cancel-secret-canary");
    await click(byRole(container, "button", "Add secret"));
    const reopened = await dialog();
    expect(byLabel<HTMLInputElement>(reopened, "Value").value).toBe("");
    expect(client.asked).toEqual([]);
  });

  test("a rejected save stays open, says why, and can be retried", async () => {
    const { client, d, name, value, submit, changed } = await open();
    await typeInto(name, "RETRY_KEY");
    await typeInto(value(), "retry-secret-canary");
    const add = client.addProjectSecret.bind(client);
    client.addProjectSecret = async () => { throw new Error("The server is unavailable."); };
    await click(submit);
    await until(() => d.textContent?.includes("The server is unavailable."), "the save's error");
    expect(changed()).toBe(0);
    expect(value().value).toBe("retry-secret-canary");
    expect(submit.disabled).toBe(false);
    expect(toasts().some((t) => t.includes("RETRY_KEY added"))).toBe(false);
    client.addProjectSecret = add;
    await click(submit);
    await until(() => allByRole(document.body, "dialog").length === 0, "closed after the retry");
    expect(changed()).toBe(1);
  });
});

describe("a request outlives the dialog that made it", () => {
  /** A request held until the test settles it. */
  function held() {
    let settle!: { resolve: () => void; reject: (e: Error) => void };
    const promise = new Promise<void>((resolve, reject) => { settle = { resolve, reject }; });
    return { promise, ...settle! };
  }
  class Held extends Recording {
    pending = held();
    override async addProjectSecret(projectId: string, name: string, value: string) {
      this.asked.push(`add ${name}`);
      await this.pending.promise;
      return { name, hint: value.slice(-4), updatedAt: new Date().toISOString(), updatedBy: null };
    }
    override async replaceProjectSecret(projectId: string, name: string, value: string) {
      this.asked.push(`replace ${name}`);
      await this.pending.promise;
      return { name, hint: value.slice(-4), updatedAt: new Date().toISOString(), updatedBy: null };
    }
    override async removeProjectSecret(projectId: string, name: string) {
      this.asked.push(`remove ${name}`);
      await this.pending.promise;
    }
  }

  async function renderHeld() {
    const client = new Held("a");
    let changed = 0;
    const { container, unmount } = await mount(
      <TooltipProvider><ToastProvider>
        <PreviewSecretsRow client={client} projectId="prj" projectName="Jervasion" secrets={SECRETS} recipes={RECIPES} canEdit onChanged={() => changed++} />
      </ToastProvider></TooltipProvider>,
    );
    mounted.push(unmount);
    return { container, client, changed: () => changed };
  }

  /** Adds FIRST_KEY, cancels while it is pending, and opens Add again with NEXT_KEY typed. */
  async function cancelledAddThenAnother() {
    const r = await renderHeld();
    await click(byRole(r.container, "button", "Add secret"));
    let d = await dialog();
    await typeInto(byLabel(d, "Name"), "FIRST_KEY");
    await typeInto(byLabel(d, "Value"), "first-value");
    await click(byRole(d, "button", "Add secret"));
    expect(r.client.asked).toEqual(["add FIRST_KEY"]);
    // Cancel stays available while it is pending: the request is the old dialog's.
    const cancel = byRole<HTMLButtonElement>(d, "button", "Cancel");
    expect(cancel.disabled).toBe(false);
    await click(cancel);
    expect(allByRole(document.body, "dialog")).toHaveLength(0);
    await click(byRole(r.container, "button", "Add secret"));
    d = await dialog();
    await typeInto(byLabel(d, "Name"), "NEXT_KEY");
    await typeInto(byLabel(d, "Value"), "next-unsaved-value");
    return { ...r, d };
  }

  test("its success refreshes the list and says so, and leaves a newer dialog open as it is", async () => {
    const { client, changed } = await cancelledAddThenAnother();
    await act(async () => client.pending.resolve());
    await settle();
    expect(toasts().some((t) => t.includes("FIRST_KEY added"))).toBe(true);
    expect(changed()).toBe(1);
    const d = allByRole(document.body, "dialog");
    expect(d).toHaveLength(1);
    expect(byLabel<HTMLInputElement>(d[0]!, "Name").value).toBe("NEXT_KEY");
    expect(byLabel<HTMLInputElement>(d[0]!, "Value").value).toBe("next-unsaved-value");
    expect(document.activeElement).not.toBe(byRole(document.body, "button", "Add secret"));
    // The newer dialog's own submit is its own, not held busy by the old request.
    expect(byRole<HTMLButtonElement>(d[0]!, "button", "Add secret").disabled).toBe(false);
  });

  test("its failure is not put into a newer dialog", async () => {
    const { client, d, changed } = await cancelledAddThenAnother();
    // Busy is the old dialog's: the newer one can submit.
    expect(byRole<HTMLButtonElement>(d, "button", "Add secret").disabled).toBe(false);
    await act(async () => client.pending.reject(new Error("FIRST_KEY was refused")));
    await settle();
    expect(changed()).toBe(0);
    const open = allByRole(document.body, "dialog");
    expect(open).toHaveLength(1);
    expect(document.body.textContent).not.toContain("FIRST_KEY was refused");
    expect(byLabel<HTMLInputElement>(open[0]!, "Value").value).toBe("next-unsaved-value");
  });

  test("a replace or a remove whose dialog was closed leaves the next one alone", async () => {
    for (const [item, submit, title] of [["Replace value", "Replace", "Replace SEED_LLM_KEY"], ["Remove", "Remove", "Remove SEED_LLM_KEY?"]] as const) {
      const r = await renderHeld();
      await menuItem(r.container, "SEED_LLM_KEY", item);
      let d = await dialog();
      if (item === "Replace value") await typeInto(byLabel(d, "New value"), "old-dialog-value");
      await click(byRole(d, "button", submit));
      await click(byRole(d, "button", "Cancel"));
      await menuItem(r.container, "STRIPE_TEST_KEY", "Replace value");
      d = await dialog();
      await typeInto(byLabel(d, "New value"), "newer-dialog-value");
      await act(async () => r.client.pending.reject(new Error(`${title} was refused`)));
      await settle();
      expect(`${item}: ${document.body.textContent?.includes(`${title} was refused`)}`).toBe(`${item}: false`);
      const open = allByRole(document.body, "dialog");
      expect(open).toHaveLength(1);
      expect(open[0]!.textContent).toContain("Replace STRIPE_TEST_KEY");
      expect(byLabel<HTMLInputElement>(open[0]!, "New value").value).toBe("newer-dialog-value");
      for (const unmount of mounted.splice(0)) await unmount();
      document.body.innerHTML = "";
    }
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
    const submit = byRole<HTMLButtonElement>(d, "button", "Replace");
    expect(submit.disabled).toBe(true);
    await typeInto(byLabel(d, "New value"), "sk-live-e2b8");
    expect(submit.disabled).toBe(false);
    await click(submit);
    await until(() => toasts().some((t) => t.includes("SEED_LLM_KEY value replaced")), "the toast");
    expect(client.asked).toEqual([`replace SEED_LLM_KEY "sk-live-e2b8"`]);
  });

  test("the row menu is Replace value, a separator, then Remove in danger", async () => {
    const { container } = await render();
    await press(byRole(container, "button", "Actions for STRIPE_TEST_KEY"));
    const menu = await until(() => allByRole(document.body, "menu")[0], "the menu");
    const items = [...menu.querySelectorAll("[role=menuitem], [role=separator]")].map((el) => el.getAttribute("role") === "separator" ? "—" : el.textContent);
    expect(items).toEqual(["Replace value", "—", "Remove"]);
  });

  test("Remove asks, then removes it, and the toast names it", async () => {
    const { container, client } = await render();
    await menuItem(container, "STRIPE_TEST_KEY", "Remove");
    const d = await dialog();
    expect(d.textContent).toContain("Remove STRIPE_TEST_KEY?");
    await click(byRole(d, "button", "Remove"));
    await until(() => toasts().some((t) => t.includes("STRIPE_TEST_KEY removed")), "the toast");
    expect(client.asked).toEqual(["remove STRIPE_TEST_KEY"]);
    await settle();
  });

  test("a rejected replace stays open and says why in its dialog", async () => {
    const { container, client, changed } = await render();
    client.replaceProjectSecret = async () => { throw new Error("The value was refused."); };
    await menuItem(container, "SEED_LLM_KEY", "Replace value");
    const d = await dialog();
    await typeInto(byLabel(d, "New value"), "sk-live-e2b8");
    await click(byRole(d, "button", "Replace"));
    await until(() => d.textContent?.includes("The value was refused."), "the replace's error in its dialog");
    expect(allByRole(document.body, "dialog")).toHaveLength(1);
    expect(byRole<HTMLButtonElement>(d, "button", "Replace").disabled).toBe(false);
    expect(toasts().some((t) => t.includes("value replaced"))).toBe(false);
    expect(changed()).toBe(0);
  });

  test("a rejected remove stays open and says why in its dialog", async () => {
    const { container, client, changed } = await render();
    client.removeProjectSecret = async () => { throw new Error("The secret could not be removed."); };
    await menuItem(container, "STRIPE_TEST_KEY", "Remove");
    const d = await dialog();
    await click(byRole(d, "button", "Remove"));
    await until(() => d.textContent?.includes("The secret could not be removed."), "the remove's error in its dialog");
    expect(allByRole(document.body, "dialog")).toHaveLength(1);
    expect(byRole<HTMLButtonElement>(d, "button", "Remove").disabled).toBe(false);
    expect(toasts().some((t) => t.includes("removed"))).toBe(false);
    expect(changed()).toBe(0);
  });
});

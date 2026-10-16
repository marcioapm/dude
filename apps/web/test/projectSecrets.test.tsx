/**
 * Servers → Secrets: the section lists names, who gets each and hints,
 * says so when there are none, and is read-only for someone who cannot
 * edit; the Add, Change value and Change who gets it dialogs refuse what
 * the API would, in the spec's words, recipients and value apart; and
 * every change says so in a toast naming the secret. Mounted in happy-dom
 * against the fixture client, which keeps no value; controls are found by
 * role and label, as assistive technology finds them.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { ProjectSecret, RecipeEnvNames, SecretRecipient } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { allByRole, byLabel, byRole } from "../../../packages/design-system/test/queries.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { GIVEN_TO_HINT, NO_TALKERS_NOTE, ProjectSecretsSection, REDACTION_NOTE, SECRETS_HELP, SECRETS_NOTE } from "../src/screens/ProjectSecrets.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

const SECRETS: ProjectSecret[] = [
  { name: "SEED_LLM_KEY", recipients: ["previews"], hint: "3f9a", updatedAt: new Date(Date.now() - 2 * 36e5).toISOString(), updatedBy: { id: "u_m", name: "Márcio" } },
  { name: "SNOWFLAKE_PASSWORD", recipients: ["previews", "tester"], hint: "7Q2a", updatedAt: new Date(Date.now() - 864e5).toISOString(), updatedBy: { id: "u_m", name: "Márcio" } },
  { name: "STRIPE_TEST_KEY", recipients: ["previews"], hint: "x7Qb", updatedAt: new Date(Date.now() - 3 * 864e5).toISOString(), updatedBy: { id: "u_r", name: "Rui" } },
];
const RECIPES: RecipeEnvNames[] = [{ name: "web", env: [{ name: "PORT" }, { name: "NODE_ENV" }] }];
const PEM = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7\nk3Lq9bF0rT2yVf8mWJxQ1s0ZpN4cD6eH5aR7uGvKtYwE9iL3oM\n-----END PRIVATE KEY-----";
const ROLES = ["Previews", "Tester", "Implementer", "Fixer", "Simplifier", "Reviewer"];

/** Records what the section asked of the API, and answers as the API would. */
class Recording extends FixtureClient {
  asked: string[] = [];
  override async addProjectSecret(projectId: string, name: string, value: string, recipients?: readonly SecretRecipient[]) {
    this.asked.push(`add ${name} ${JSON.stringify(value)} ${JSON.stringify(recipients)}`);
    return super.addProjectSecret(projectId, name, value, recipients);
  }
  override async replaceProjectSecret(projectId: string, name: string, value: string) {
    this.asked.push(`replace ${name} ${JSON.stringify(value)}`);
    return super.replaceProjectSecret(projectId, name, value);
  }
  override async changeProjectSecretRecipients(projectId: string, name: string, recipients: readonly SecretRecipient[]) {
    this.asked.push(`recipients ${name} ${JSON.stringify(recipients)}`);
    return { ...SECRETS.find((s) => s.name === name)!, recipients: [...recipients] };
  }
  override async removeProjectSecret(projectId: string, name: string) {
    this.asked.push(`remove ${name}`);
    return super.removeProjectSecret(projectId, name);
  }
}

async function render({ secrets = SECRETS, canEdit = true }: { secrets?: ProjectSecret[]; canEdit?: boolean } = {}) {
  const client = new Recording("a");
  let changed = 0;
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <ProjectSecretsSection client={client} projectId="prj" secrets={secrets} recipes={RECIPES} canEdit={canEdit}
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
/** The section's table as names, recipients and shown values. */
const tableRows = (scope: HTMLElement) =>
  allByRole(byRole(scope, "table", "Secrets"), "row").slice(1).map((r) => allByRole(r, "cell").slice(0, 3).map((c) => c.textContent));

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

describe("the Secrets section", () => {
  test("lists each secret by name, who gets it and hint, with the help and the notes", async () => {
    const { container } = await render();
    expect(byRole(container, "heading", "Secrets")).toBeTruthy();
    expect(container.textContent).toContain(SECRETS_HELP);
    expect(tableRows(container)).toEqual([
      ["SEED_LLM_KEY", "Previews", "…3f9a"], ["SNOWFLAKE_PASSWORD", "PreviewsTester", "…7Q2a"], ["STRIPE_TEST_KEY", "Previews", "…x7Qb"]]);
    expect(SECRETS_NOTE).toBe("Agent sessions and previews that start after you save get it; running ones keep what they have.");
    expect(container.textContent).toContain(SECRETS_NOTE);
    expect(container.textContent).toContain(REDACTION_NOTE);
    expect(REDACTION_NOTE).toContain("defence in depth, not a guarantee");
    byRole(container, "button", "Add secret");
    // Who changed it is the row's tooltip, not a column.
    expect(allByRole(byRole(container, "table", "Secrets"), "columnheader").map((h) => h.textContent)).toEqual(["Name", "Given to", "Value", "Actions"]);
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
    expect(container.textContent).toContain("Add a value a preview or an agent needs but shouldn’t be in the repository: a seed script’s API key, a test database password.");
    byRole(container, "button", "Add secret");
    expect(allByRole(container, "table")).toHaveLength(0);
  });

  test("is read-only for someone who cannot edit: names and recipients, no menus, no Add", async () => {
    const { container } = await render({ canEdit: false });
    expect(tableRows(container)).toHaveLength(3);
    expect(container.textContent).toContain("Only project editors can change secrets.");
    expect(allByRole(container, "button").filter((b) => b.getAttribute("aria-label")?.startsWith("Actions for"))).toHaveLength(0);
    expect(allByRole(container, "button", "Add secret")).toHaveLength(0);
    const empty = await render({ secrets: [], canEdit: false });
    expect(empty.container.textContent).toContain("No secrets");
    expect(allByRole(empty.container, "button", "Add secret")).toHaveLength(0);
  });

  test("the row menu is Change value…, Change who gets it…, a separator, then Remove", async () => {
    const { container } = await render();
    await press(byRole(container, "button", "Actions for STRIPE_TEST_KEY"));
    const menu = await until(() => allByRole(document.body, "menu")[0], "the menu");
    const items = [...menu.querySelectorAll("[role=menuitem], [role=separator]")].map((el) => el.getAttribute("role") === "separator" ? "—" : el.textContent);
    expect(items).toEqual(["Change value…", "Change who gets it…", "—", "Remove"]);
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
    expect(d.textContent).toContain("Give this value to the previews and agents that need it as an environment variable.");
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

  test("Given to: every recipient with its line, Previews and Tester ticked, the conductor and brainstorm never offered", async () => {
    const { d } = await open();
    const boxes = allByRole(d, "checkbox");
    expect(boxes.map((b) => document.getElementById(b.getAttribute("aria-labelledby")!)?.textContent)).toEqual(ROLES);
    expect(boxes.map((b) => b.getAttribute("aria-checked"))).toEqual(["true", "true", "false", "false", "false", "false"]);
    for (const line of ["Servers and setup scripts of branch previews", "Runs tests, including against real services", "Implements the task",
      "Fixes findings and PR feedback", "Simplifies the changes", "Reviews the changes", GIVEN_TO_HINT, NO_TALKERS_NOTE]) {
      expect(`${line}: ${d.textContent?.includes(line)}`).toBe(`${line}: true`);
    }
    expect(NO_TALKERS_NOTE).toBe("Conductor and brainstorm never get secrets: they read text anyone can write, such as PR comments.");
    expect(d.textContent).toContain("*.snowflakecomputing.com");
    expect(d.textContent).not.toContain("Agents the conductor starts");
  });

  test("with no recipient ticked it says why and cannot be saved", async () => {
    const { d, name, value, submit } = await open();
    await typeInto(name, "NOBODY_KEY");
    await typeInto(value(), "nobody-test-value");
    expect(submit.disabled).toBe(false);
    for (const box of allByRole(d, "checkbox").filter((b) => b.getAttribute("aria-checked") === "true")) await click(box);
    expect(d.textContent).toContain("Give it to at least one, or don't add it yet");
    expect(submit.disabled).toBe(true);
  });

  test("a value too short to redact is warned about, not refused", async () => {
    const { d, name, value, submit } = await open();
    await typeInto(name, "PIN");
    await typeInto(value(), "12345");
    expect(d.textContent).toContain("Values shorter than 6 characters are not redacted from agent output");
    expect(submit.disabled).toBe(false);
    await typeInto(value(), "123456");
    expect(d.textContent).not.toContain("Values shorter than 6 characters");
  });

  test("each name the API refuses is refused here", async () => {
    const { d, name, value, submit } = await open();
    await typeInto(value(), "sk-test-4b1d");
    for (const [typed, said] of [
      ["SEED-LLM-KEY", "Use letters, digits and _ only, starting with a letter or _."],
      ["LUX_TOKEN", "Names starting with LUX_ are lux’s own."],
      ["GIT_TOKEN", "dude sets GIT_TOKEN and every GIT_TOKEN_ name itself, from the GitHub connection."],
      ["DUDE_TOOLS_AUTH", "dude sets DUDE_TOOLS_AUTH itself."],
      ["ANTHROPIC_API_KEY", "dude sets ANTHROPIC_API_KEY itself."],
      ["A".repeat(64), "At most 63 characters."],
      ["SEED_LLM_KEY", "There is already a SEED_LLM_KEY. Replace its value instead."],
      ["PORT", "Server web sets PORT in its own environment, which would override this. Rename one of them."],
    ]) {
      await typeInto(name, typed!);
      expect(`${typed}: ${d.textContent?.includes(said!)}`).toBe(`${typed}: true`);
      expect(name.getAttribute("aria-invalid")).toBe("true");
      expect(submit.disabled).toBe(true);
    }
  });

  test("a value is at most 32 KiB in UTF-8 bytes, not characters", async () => {
    const { d, name, value, submit } = await open();
    await typeInto(name, "BIG_KEY");
    await typeInto(value(), "x".repeat(32 * 1024 - 2) + "é");
    expect(d.textContent).not.toContain("At most 32 KiB.");
    expect(submit.disabled).toBe(false);
    await typeInto(value(), "x".repeat(32 * 1024 - 1) + "é");
    expect(d.textContent).toContain("At most 32 KiB.");
    expect(submit.disabled).toBe(true);
  });

  test("adds it as typed with its recipients, and the toast names it", async () => {
    const { client, d, name, value, submit, changed } = await open();
    await typeInto(name, "SNOWFLAKE_ROLE");
    await typeInto(value(), "sf-role-test-value");
    await click(allByRole(d, "checkbox")[2]!);
    await click(submit);
    await until(() => toasts().some((t) => t.includes("SNOWFLAKE_ROLE added")), "the toast");
    expect(client.asked).toEqual([`add SNOWFLAKE_ROLE "sf-role-test-value" ["previews","tester","implementer"]`]);
    expect(changed()).toBe(1);
    expect(allByRole(document.body, "dialog")).toHaveLength(0);
  });

  test("Cancel clears the value before Add is opened again", async () => {
    const { container, name, value, cancel, client } = await open();
    await typeInto(name, "CANCEL_KEY");
    await typeInto(value(), "cancel-secret-canary");
    await click(cancel);
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
    client.addProjectSecret = add;
    await click(submit);
    await until(() => allByRole(document.body, "dialog").length === 0, "closed after the retry");
    expect(changed()).toBe(1);
  });
});

describe("Change who gets it", () => {
  test("starts from its recipients, asks for no value, and saves only the recipients", async () => {
    const { container, client } = await render();
    await menuItem(container, "SNOWFLAKE_PASSWORD", "Change who gets it…");
    const d = await dialog();
    expect(d.textContent).toContain("Who gets SNOWFLAKE_PASSWORD");
    expect(d.textContent).toContain("Changing who gets it leaves its value as it is.");
    expect(allByRole(d, "checkbox").map((b) => b.getAttribute("aria-checked"))).toEqual(["true", "true", "false", "false", "false", "false"]);
    expect([...d.querySelectorAll("input[type=password], textarea")]).toHaveLength(0);
    await click(allByRole(d, "checkbox")[0]!);
    await click(allByRole(d, "checkbox")[5]!);
    await click(byRole(d, "button", "Save"));
    await until(() => toasts().some((t) => t.includes("SNOWFLAKE_PASSWORD now given to Tester, Reviewer")), "the toast");
    expect(client.asked).toEqual([`recipients SNOWFLAKE_PASSWORD ["tester","reviewer"]`]);
  });

  test("none ticked says why and cannot be saved", async () => {
    const { container } = await render();
    await menuItem(container, "STRIPE_TEST_KEY", "Change who gets it…");
    const d = await dialog();
    await click(allByRole(d, "checkbox")[0]!);
    expect(d.textContent).toContain("Give it to at least one, or don't add it yet");
    expect(byRole<HTMLButtonElement>(d, "button", "Save").disabled).toBe(true);
  });
});

describe("a request outlives the dialog that made it", () => {
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
      return { name, recipients: ["previews" as const], hint: value.slice(-4), updatedAt: new Date().toISOString(), updatedBy: null };
    }
    override async replaceProjectSecret(projectId: string, name: string, value: string) {
      this.asked.push(`replace ${name}`);
      await this.pending.promise;
      return { name, recipients: ["previews" as const], hint: value.slice(-4), updatedAt: new Date().toISOString(), updatedBy: null };
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
        <ProjectSecretsSection client={client} projectId="prj" secrets={SECRETS} recipes={RECIPES} canEdit onChanged={() => changed++} />
      </ToastProvider></TooltipProvider>,
    );
    mounted.push(unmount);
    return { container, client, changed: () => changed };
  }

  async function cancelledAddThenAnother() {
    const r = await renderHeld();
    await click(byRole(r.container, "button", "Add secret"));
    let d = await dialog();
    await typeInto(byLabel(d, "Name"), "FIRST_KEY");
    await typeInto(byLabel(d, "Value"), "first-value");
    await click(byRole(d, "button", "Add secret"));
    expect(r.client.asked).toEqual(["add FIRST_KEY"]);
    const cancel = byRole<HTMLButtonElement>(d, "button", "Cancel");
    expect(cancel.disabled).toBe(false);
    await click(cancel);
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
    expect(byLabel<HTMLInputElement>(d[0]!, "Value").value).toBe("next-unsaved-value");
    expect(byRole<HTMLButtonElement>(d[0]!, "button", "Add secret").disabled).toBe(false);
  });

  test("its failure is not put into a newer dialog", async () => {
    const { client, changed } = await cancelledAddThenAnother();
    await act(async () => client.pending.reject(new Error("FIRST_KEY was refused")));
    await settle();
    expect(changed()).toBe(0);
    expect(document.body.textContent).not.toContain("FIRST_KEY was refused");
  });

  test("a value change or a remove whose dialog was closed leaves the next one alone", async () => {
    for (const [item, submit, title] of [["Change value…", "Change value", "Change SEED_LLM_KEY"], ["Remove", "Remove", "Remove SEED_LLM_KEY?"]] as const) {
      const r = await renderHeld();
      await menuItem(r.container, "SEED_LLM_KEY", item);
      let d = await dialog();
      if (item === "Change value…") await typeInto(byLabel(d, "New value"), "old-dialog-value");
      await click(byRole(d, "button", submit));
      await click(byRole(d, "button", "Cancel"));
      await menuItem(r.container, "STRIPE_TEST_KEY", "Change value…");
      d = await dialog();
      await typeInto(byLabel(d, "New value"), "newer-dialog-value");
      await act(async () => r.client.pending.reject(new Error(`${title} was refused`)));
      await settle();
      expect(`${item}: ${document.body.textContent?.includes(`${title} was refused`)}`).toBe(`${item}: false`);
      const open = allByRole(document.body, "dialog");
      expect(open).toHaveLength(1);
      expect(open[0]!.textContent).toContain("Change STRIPE_TEST_KEY");
      expect(byLabel<HTMLInputElement>(open[0]!, "New value").value).toBe("newer-dialog-value");
      for (const unmount of mounted.splice(0)) await unmount();
      document.body.innerHTML = "";
    }
  });
});

describe("Change value and Remove", () => {
  test("Change value says what it ends in now and leaves who gets it alone; the toast names it", async () => {
    const { container, client } = await render();
    await menuItem(container, "SEED_LLM_KEY", "Change value…");
    const d = await dialog();
    expect(d.textContent).toContain("Change SEED_LLM_KEY");
    expect(d.textContent).toContain("Now ends in 3f9a.");
    expect(d.textContent).toContain("Changing the value leaves who gets it as it is.");
    expect(allByRole(d, "checkbox")).toHaveLength(0);
    const submit = byRole<HTMLButtonElement>(d, "button", "Change value");
    expect(submit.disabled).toBe(true);
    await typeInto(byLabel(d, "New value"), "sk-live-e2b8");
    await click(submit);
    await until(() => toasts().some((t) => t.includes("SEED_LLM_KEY value changed")), "the toast");
    expect(client.asked).toEqual([`replace SEED_LLM_KEY "sk-live-e2b8"`]);
  });

  test("Remove asks, says what running sessions keep, then removes it", async () => {
    const { container, client } = await render();
    await menuItem(container, "STRIPE_TEST_KEY", "Remove");
    const d = await dialog();
    expect(d.textContent).toContain("Remove STRIPE_TEST_KEY?");
    expect(d.textContent).toContain("New previews and agent sessions won't get it. Ones already running keep it until they start again.");
    await click(byRole(d, "button", "Remove"));
    await until(() => toasts().some((t) => t.includes("STRIPE_TEST_KEY removed")), "the toast");
    expect(client.asked).toEqual(["remove STRIPE_TEST_KEY"]);
    await settle();
  });

  test("a rejected remove stays open and says why in its dialog", async () => {
    const { container, client, changed } = await render();
    client.removeProjectSecret = async () => { throw new Error("The secret could not be removed."); };
    await menuItem(container, "STRIPE_TEST_KEY", "Remove");
    const d = await dialog();
    await click(byRole(d, "button", "Remove"));
    await until(() => d.textContent?.includes("The secret could not be removed."), "the remove's error in its dialog");
    expect(allByRole(document.body, "dialog")).toHaveLength(1);
    expect(changed()).toBe(0);
  });
});

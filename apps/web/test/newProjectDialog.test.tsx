/**
 * The new-project dialog's Key: prefilled as dude would derive it from the
 * slug and the organisation's keys, editable, and on a 409 the refusal in
 * place under it with the suggestion as a one-click fill. Mounted in
 * happy-dom against a client that answers as the API does.
 */

import { afterEach, expect, test } from "bun:test";
import type { Project } from "@dude/domain";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { click, mount, type, until } from "./dom.ts";
import { ApiError, type ProjectDetail } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PROJECT } from "../src/fixtures/data.ts";
import { NewProjectDialog } from "../src/screens/NewProjectDialog.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

type Created = Parameters<FixtureClient["createProject"]>[0];

class ProjectsClient extends FixtureClient {
  created: Created[] = [];
  /** The answer to the next create, or the project made. */
  refuse: ApiError | null = null;
  constructor(readonly keys: string[]) {
    super("a");
  }
  override listProjects(): Promise<{ projects: Project[] }> {
    return Promise.resolve({ projects: this.keys.map((key, i) => ({ ...PROJECT, id: `prj_${i}`, key })) });
  }
  override createProject(input: Created): Promise<ProjectDetail> {
    this.created.push(input);
    const refusal = this.refuse;
    this.refuse = null;
    return refusal ? Promise.reject(refusal) : Promise.resolve({ ...PROJECT, id: "prj_new", key: input.key ?? "" });
  }
}

const field = (id: string) => document.querySelector<HTMLInputElement>(`[data-testid=${id}]`)!;

async function open(client: ProjectsClient, onCreated: (id: string) => void = () => {}) {
  const { unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <NewProjectDialog client={client} open onOpenChange={() => {}} onCreated={onCreated} />
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => field("project-key"), "the dialog");
}

/** The text that describes the Key field: its hint, or its error. */
const keySays = () => document.getElementById(field("project-key").getAttribute("aria-describedby") ?? "")?.textContent ?? "";

test("the key follows the name as dude would derive it, past the organisation's keys, until it is edited", async () => {
  await open(new ProjectsClient(["BILL", "WEBC"]));
  expect(field("project-key").value).toBe("");
  await type(field("project-name"), "Billing API");
  // BILL is taken: the next of the documented sequence.
  await until(() => field("project-key").value === "BAPI", "BAPI, prefilled");
  await type(field("project-name"), "Search");
  await until(() => field("project-key").value === "SEAR", "SEAR, prefilled");
  await type(field("project-key"), "srch");
  expect(field("project-key").value).toBe("SRCH");
  await type(field("project-name"), "Search Service");
  expect(field("project-key").value).toBe("SRCH");
});

test("a key the API refuses as taken says so under the field, and its suggestion fills it with one click", async () => {
  const client = new ProjectsClient([]);
  const created: string[] = [];
  await open(client, (id) => created.push(id));
  await type(field("project-name"), "Billing Ledger");
  await until(() => field("project-key").value === "BILL", "BILL, prefilled");
  client.refuse = new ApiError(409, "conflict", "BILL is already the key of Billing API; pick another", { suggestion: "BLED" });
  await click(field("project-create"));
  await until(() => keySays() === "BILL is already the key of Billing API; pick another", "the refusal, under Key");
  expect(field("project-key").getAttribute("aria-invalid")).toBe("true");
  expect(client.created.map((c) => c.key)).toEqual(["BILL"]);
  // In place, not also in the footer.
  expect(document.body.textContent!.split("BILL is already the key of Billing API").length).toBe(2);

  const fix = await until(() => document.querySelector<HTMLButtonElement>("[data-testid=project-key-suggestion]"), "the suggestion");
  expect(fix.textContent).toBe("Use BLED");
  await click(fix);
  expect(field("project-key").value).toBe("BLED");
  expect(document.querySelectorAll("[data-testid=project-key-suggestion]").length).toBe(0);
  expect(keySays()).not.toContain("already the key");

  await click(field("project-create"));
  await until(() => created.length === 1, "the project, made");
  expect(client.created.map((c) => c.key)).toEqual(["BILL", "BLED"]);
});

test("a key that is not 2 to 6 letters or digits starting with a letter is said so before it is sent", async () => {
  const client = new ProjectsClient([]);
  await open(client);
  await type(field("project-name"), "Payments");
  await type(field("project-key"), "2pay");
  expect(keySays()).toBe("2 to 6 letters or digits, starting with a letter");
  expect(field("project-create").disabled).toBe(true);
  await type(field("project-key"), "pay2");
  expect(field("project-create").disabled).toBe(false);
});

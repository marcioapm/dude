/**
 * Your GitHub username and commit email in your profile: shown as saved,
 * sent only when changed, cleared by emptying, and the hint saying what
 * they are for. Mounted in happy-dom against the fixture client.
 */

import { afterEach, expect, test } from "bun:test";
import { useState } from "react";
import { ThemeProvider } from "@dude/design-system";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, type, until } from "./dom.ts";
import type { Me, MeResponse } from "../src/api/client.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE } from "../src/fixtures/data.ts";
import { MySettingsScreen } from "../src/screens/MySettingsScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
});

const ME = { ...PEOPLE[0]!, lastSeenWhere: null };

class ProfileClient extends FixtureClient {
  saved: Array<Record<string, unknown>> = [];
  reads = 0;
  name = ME.name;
  constructor(private github: Pick<Me, "githubLogin" | "commitEmail">) {
    super("a");
  }
  override me(): Promise<MeResponse> {
    this.reads++;
    return Promise.resolve({ person: { ...ME, ...this.github }, organization: { id: "org", name: "Org" } });
  }
  // Stores what it is sent, as the API does: an empty value clears.
  override updateMe(changes: Record<string, unknown>): Promise<{ person: Me }> {
    this.saved.push(changes);
    if ("name" in changes) this.name = changes.name as string;
    if ("githubLogin" in changes) this.github = { ...this.github, githubLogin: (changes.githubLogin as string) || null };
    if ("commitEmail" in changes) this.github = { ...this.github, commitEmail: (changes.commitEmail as string) || null };
    return Promise.resolve({ person: { ...ME, ...this.github } });
  }
}

async function open(client: ProfileClient, screen = <MySettingsScreen client={client} me={ME} onChanged={() => {}} />) {
  const { container, unmount } = await mount(
    // Stamps theme and density on an element of its own, not the shared
    // <html>: the gallery tests, in the same process, read its attributes.
    <ThemeProvider target={document.createElement("div")}>
      <TooltipProvider>
        <ToastProvider>
          {screen}
        </ToastProvider>
      </TooltipProvider>
    </ThemeProvider>,
  );
  mounted.push(unmount);
  const login = await until(() => container.querySelector<HTMLInputElement>("[data-testid=profile-github-login]"), "the GitHub username");
  const email = container.querySelector<HTMLInputElement>("[data-testid=profile-commit-email]")!;
  const save = container.querySelector<HTMLButtonElement>("[data-testid=profile-save]")!;
  return { container, login, email, save };
}

test("the profile shows your GitHub username and commit email, and saves only what changed", async () => {
  const client = new ProfileClient({ githubLogin: "ana-dev", commitEmail: null });
  const { container, login, email, save } = await open(client);
  expect(login.value).toBe("ana-dev");
  expect(email.value).toBe("");
  expect(container.querySelector("[data-testid=profile-github-hint]")?.textContent).toContain("co-author");
  expect(save.disabled).toBe(true);
  await type(email, "ana@users.example");
  expect(save.disabled).toBe(false);
  await click(save);
  await until(() => client.saved.length === 1 ? true : null, "the save");
  expect(client.saved).toEqual([{ commitEmail: "ana@users.example" }]);
});

test("emptying the username clears it", async () => {
  const client = new ProfileClient({ githubLogin: "ana-dev", commitEmail: "ana@users.example" });
  const { login, save } = await open(client);
  await type(login, "");
  await click(save);
  await until(() => client.saved.length === 1 ? true : null, "the save");
  expect(client.saved).toEqual([{ githubLogin: "" }]);
});

test("after a save the form shows what was saved, and has nothing left to save", async () => {
  const client = new ProfileClient({ githubLogin: null, commitEmail: null });
  const { container, login } = await open(client);
  // Typed with spaces the save trims, so only a form started again from the
  // saved value shows "octocat": one never started again keeps the spaces.
  await type(login, "  octocat  ");
  await click(container.querySelector<HTMLButtonElement>("[data-testid=profile-save]")!);
  await until(() => client.saved.length === 1 ? true : null, "the save");
  // Only once the save's re-read has landed: before it, the typed value is
  // still in the first form and proves nothing.
  await until(() => {
    if (client.reads < 2) return null;
    const input = container.querySelector<HTMLInputElement>("[data-testid=profile-github-login]");
    const button = container.querySelector<HTMLButtonElement>("[data-testid=profile-save]");
    return input?.value === "octocat" && button?.disabled ? true : null;
  }, "the saved username shown, with Save off");
  expect(client.saved).toEqual([{ githubLogin: "octocat" }]);
});

// The shell has your name from the people list, which it reads again after a
// save, later than /v1/me. The test plays the shell: it hands the shell's
// setter out and does nothing on its own, so the test sets the name itself,
// in its own act, once /v1/me's answer has rendered.
function Shell({ client, expose }: { client: ProfileClient; expose: (setName: (name: string) => void) => void }) {
  const [me, setMe] = useState(ME);
  expose((name) => setMe({ ...ME, name }));
  return <MySettingsScreen client={client} me={me} onChanged={() => {}} />;
}

test("a name saved with a GitHub username shows the new name once the shell has it, with nothing left to save", async () => {
  const client = new ProfileClient({ githubLogin: null, commitEmail: null });
  let setShellName: (name: string) => void = () => {};
  const { container, login, save } = await open(client, <Shell client={client} expose={(set) => { setShellName = set; }} />);
  const nameField = () => container.querySelector<HTMLInputElement>("[data-testid=profile-name]");
  await type(nameField()!, "Renamed Person");
  await type(login, "octocat");
  await click(save);
  await until(() => client.saved.length === 1 ? true : null, "the save");
  // /v1/me's answer has rendered: the form started again from it, with the
  // saved username and the name the shell still has.
  await until(() => client.reads >= 2 && login.isConnected === false
    && container.querySelector<HTMLInputElement>("[data-testid=profile-github-login]")?.value === "octocat"
    && nameField()?.value === ME.name ? true : null, "the form started again from /v1/me");
  await act(async () => setShellName(client.name));
  const button = container.querySelector<HTMLButtonElement>("[data-testid=profile-save]");
  expect(nameField()?.value).toBe("Renamed Person");
  expect(button?.disabled).toBe(true);
  expect(client.saved).toEqual([{ name: "Renamed Person", githubLogin: "octocat" }]);
});

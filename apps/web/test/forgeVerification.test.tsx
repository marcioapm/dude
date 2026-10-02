/**
 * Verify's results under the GitHub connection: the one-line verdict, and
 * per repository what is missing or untested and why. Mounted in happy-dom.
 */

import { afterEach, expect, test } from "bun:test";
import type { ForgePermission, ForgeVerification } from "../src/api/client.ts";
import { ForgePermissionList, verificationSummary } from "../src/screens/GithubSettings.tsx";
import { mount } from "./dom.ts";

let unmount: (() => Promise<void>) | null = null;
afterEach(async () => {
  await unmount?.();
  unmount = null;
  document.body.innerHTML = "";
});

const p = (permission: string, level: ForgePermission["level"], outcome: ForgePermission["outcome"], reason: string): ForgePermission =>
  ({ permission, level, outcome, reason });

const verification: ForgeVerification = {
  ok: false, login: "dude-bot", scopes: null, tokenKind: "fine_grained",
  repositories: [
    {
      id: "repo_a", name: "api", projectName: "Payments", slug: "acme/api",
      permissions: [
        p("Contents: Read and write", "required", "ok", "pushed nothing"),
        p("Workflows: Read and write", "optional", "untested", "Not tested: only pushing a workflow file would tell."),
        p("Webhooks: Read and write", "optional", "missing", "GitHub refused to register a webhook."),
        p("Checks: Read", "required", "missing", "fine-grained tokens cannot be granted Checks: Read"),
      ],
    },
    { id: "repo_b", name: "web", projectName: "Payments", slug: "acme/web", permissions: [p("Checks: Read", "required", "ok", "listed")] },
    { id: "repo_c", name: "local", projectName: "Payments", slug: null, error: "Its URL names no GitHub repository, so it was not tested.", permissions: [] },
    {
      id: "repo_d", name: "jobs", projectName: "Payments", slug: "acme/jobs",
      permissions: [p("Metadata: Read", "required", "untested", "Rate limited."), p("Checks: Read", "required", "untested", "Not tested: the repository could not be read.")],
    },
  ],
};

test("the verdict names who the token is and how many repositories lack a required permission", () => {
  expect(verificationSummary(verification)).toEqual({ ok: false, text: "Connected as dude-bot · a required permission is missing on 1 repository" });
  expect(verificationSummary({ ...verification, ok: true, scopes: "repo" })).toEqual({ ok: true, text: "Connected as dude-bot · scopes: repo" });
  expect(verificationSummary({ ok: false, reason: "GitHub rejected the token" })).toEqual({ ok: false, text: "GitHub rejected the token" });
});

test("each repository lists what is missing, required first, then what is untested, with why; granted ones are not listed", async () => {
  const m = await mount(<ForgePermissionList verification={verification} />);
  unmount = m.unmount;
  const repos = [...m.container.querySelectorAll('[data-testid="forge-permission-repo"]')];
  expect(repos.map((r) => r.querySelector(".forgePermissionRepo")!.textContent)).toEqual([
    "MissingPayments / api", "ReadyPayments / web", "Not testedPayments / localIts URL names no GitHub repository, so it was not tested.",
    "Not fully testedPayments / jobs",
  ]);
  const rows = [...repos[0]!.querySelectorAll('[data-testid="forge-permission"]')];
  expect(rows.map((r) => [r.getAttribute("data-outcome"), r.textContent])).toEqual([
    ["missing", "MissingChecks: Read fine-grained tokens cannot be granted Checks: Read"],
    ["missing", "MissingWebhooks: Read and write (optional) GitHub refused to register a webhook."],
    ["untested", "UntestedWorkflows: Read and write (optional) Not tested: only pushing a workflow file would tell."],
  ]);
  expect(repos[1]!.querySelectorAll('[data-testid="forge-permission"]').length).toBe(0);
});

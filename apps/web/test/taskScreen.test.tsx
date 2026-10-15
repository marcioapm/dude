/**
 * The task page's tabs: which one a URL opens, and what the Servers tab
 * says of the servers. Mounted in happy-dom against the fixture client.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { PersistedEvent, Run } from "@dude/domain";
import { act, click, facetCheckbox, mount, sessionOptions, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { TASK_ID } from "../src/fixtures/data.ts";
import { RUN_KEY } from "../src/fixtures/scenario.ts";
import { PeopleProvider } from "../src/people.tsx";
import { ResetMachineConfirm } from "../src/screens/actions.tsx";
import { ApiError } from "../src/api/client.ts";
import { TaskScreen, activityLines, filterActivity, filterFindings } from "../src/screens/TaskScreen.tsx";
import { filesOf, filterFiles, displayedFilesZip } from "../src/screens/FilesSection.tsx";
import type { Artifact, Finding } from "../src/api/client.ts";
import { RunScreen, type RunScreenProps } from "../src/screens/RunScreen.tsx";
import { emit } from "../src/fixtures/client.ts";
import { ORG, PEOPLE, PROJECT, RUN_ID } from "../src/fixtures/data.ts";
import type { ReactNode } from "react";

import { unzipSync } from "fflate";
import { ToastProvider } from "@dude/design-system/primitives";

describe("task rail filtering", () => {
  test("status and severity compose without treating an open note as settled", () => {
    const findings = [
      { id: "note", status: "open", severity: "note" },
      { id: "fixed", status: "resolved", severity: "blocking" },
      { id: "accepted", status: "accepted", severity: "low" },
      { id: "replaced", status: "superseded", severity: "high" },
    ] as Finding[];
    expect(filterFindings(findings, ["open"], []).map((f) => f.id)).toEqual(["note"]);
    expect(filterFindings(findings, ["resolved", "accepted"], ["blocking"]).map((f) => f.id)).toEqual(["fixed"]);
    expect(filterFindings(findings, [], []).length).toBe(4);
  });

  test("publisher and kind use latest versions, ZIP reads exactly the displayed IDs and keeps paths", async () => {
    const artifact = (id: string, name: string, phase: string, contentType: string) => ({ id, name, phase, role: "implementer", contentType, sizeBytes: 3, version: 1, createdAt: "2026-10-01", description: "" }) as Artifact;
    const files = filesOf([artifact("new", "notes/report.md", "fix", "text/markdown"), artifact("old", "notes/report.md", "implement", "text/markdown"), artifact("image", "shot.png", "implement", "image/png")]);
    const displayed = filterFiles(files, ["documents"], ["Fix"]);
    expect(displayed.map((f) => f.name)).toEqual(["notes/report.md"]);
    expect(displayed[0]!.versions.map((v) => v.id)).toEqual(["new", "old"]);
    expect(filterFiles(files, ["media"], ["Fix"])).toEqual([]);
    const reads: string[] = [];
    const zip = await displayedFilesZip({ artifactContent: async (id) => { reads.push(id); return new Blob([id]); } }, displayed);
    expect(reads).toEqual(["new"]);
    const entries = unzipSync(new Uint8Array(await zip.arrayBuffer()));
    expect(Object.keys(entries)).toEqual(["notes/report.md"]);
    expect(new TextDecoder().decode(entries["notes/report.md"])).toBe("new");
    expect(files.length).toBe(2);
  });

  test("finding controls compose in the mounted tab and their base counts stay unchanged", async () => {
    const page = await taskPage(new FixtureClient("d"), { tab: "findings" });
    const status = await until(() => page.querySelector<HTMLElement>('[role="group"][aria-label="Finding status"]'), "status filters");
    await until(() => page.querySelectorAll('[data-testid="finding"]').length > 0 ? true : null, "findings");
    const severity = page.querySelector<HTMLElement>('[role="group"][aria-label="Finding severity"]')!;
    const counts = status.textContent;
    const pick = (group: HTMLElement, label: string) => click(facetCheckbox(group, label));
    await pick(status, "Open");
    expect(page.querySelectorAll('[data-testid="finding"]').length).toBeGreaterThan(0);
    expect([...page.querySelectorAll('[data-testid="finding"]')].every((f) => f.getAttribute("data-status") === "open")).toBe(true);
    await pick(severity, "Note");
    expect([...page.querySelectorAll('[data-testid="finding"]')].map((f) => f.getAttribute("data-severity"))).toEqual(["note"]);
    await pick(severity, "Blocking");
    expect([...page.querySelectorAll('[data-testid="finding"]')].map((f) => f.getAttribute("data-severity"))).toEqual(["note"]);
    expect(status.textContent).toBe(counts);
  });

  test("findings filtered to nothing shows one empty state, without the empty group", async () => {
    const page = await taskPage(new FixtureClient("d"), { tab: "findings" });
    const severity = await until(() => page.querySelector<HTMLElement>('[role="group"][aria-label="Finding severity"]'), "severity filters");
    await click(facetCheckbox(severity, "High"));
    expect(page.textContent).toContain("No matching findings");
    expect(page.textContent).not.toContain("No findings.");
    expect(page.textContent).not.toContain("Review findings");
    expect(page.querySelectorAll('[data-testid="findings"]').length).toBe(0);
    expect(page.querySelectorAll('[data-testid="finding"]').length).toBe(0);
  });

  test("the findings rail names each review run with its status, opens it, and counts fix runs", async () => {
    const opened: string[] = [];
    const page = await taskPage(new FixtureClient("d"), { tab: "findings", onOpenRun: (id) => opened.push(id) });
    const rows = await until(() => {
      const found = [...page.querySelectorAll<HTMLElement>('[data-testid="reviewer"]')];
      return found.length > 0 ? found : null;
    }, "reviewer rows");
    expect(rows.map((r) => r.getAttribute("data-run"))).toEqual(["run_d2", "run_d4"]);
    for (const row of rows) {
      expect(row.querySelector('[data-status="completed"]')?.textContent).toBe("Completed");
      expect(row.closest("div")?.querySelector("dt")?.textContent).toBe("Review · correctness");
    }
    await click(rows[1]!);
    expect(opened).toEqual(["run_d4"]);
    const fix = [...page.querySelectorAll("dt")].find((dt) => dt.textContent === "Fix runs");
    expect(fix?.nextElementSibling?.textContent).toBe("1");
  });

  test("toggling a kind facet keeps the attempt's thumbnails instead of reading them again", async () => {
    const client = new FixtureClient("a");
    const artifacts = [
      { id: "image-a", name: "a.png", phase: "implement", role: "implementer", contentType: "image/png", sizeBytes: 3, version: 1, createdAt: "2026-10-01", description: "" },
      { id: "image-b", name: "b.png", phase: "implement", role: "implementer", contentType: "image/png", sizeBytes: 3, version: 1, createdAt: "2026-10-01", description: "" },
      { id: "doc", name: "report.md", phase: "implement", role: "implementer", contentType: "text/markdown", sizeBytes: 3, version: 1, createdAt: "2026-10-01", description: "" },
    ] as Artifact[];
    client.listArtifacts = async () => ({ artifacts });
    const reads: string[] = [];
    client.artifactContent = async (id) => { reads.push(id); return new Blob([id]); };
    const page = await taskPage(client, { tab: "files" });
    await until(() => reads.length === 2 ? true : null, "the two image thumbnails");
    await settle();
    const kinds = await until(() => page.querySelector<HTMLElement>('[role="group"][aria-label="File kind"]'), "kind facets");
    const doc = facetCheckbox(kinds, "Documents");
    await click(doc);
    await settle();
    await click(doc);
    await settle();
    expect(reads).toEqual(["image-a", "image-b"]);
  });

  test("task chatter from other runs does not re-render the open RunScreen", async () => {
    const memo = RunScreen as unknown as { type: (props: RunScreenProps) => ReactNode };
    const screen = memo.type;
    let renders = 0;
    memo.type = (props) => { renders++; return screen(props); };
    const source = globalThis.EventSource;
    const windowSource = window.EventSource;
    class ScopedSource extends source {
      constructor(url: string | URL) {
        super(url);
        const scope = new URL(String(url), window.location.origin).searchParams;
        let message: EventSource["onmessage"] = null;
        Object.defineProperty(this, "onmessage", {
          configurable: true,
          get: () => (e: MessageEvent) => {
            const event = JSON.parse(e.data) as PersistedEvent;
            if (["taskId", "runId", "sessionId"].every((k) => !scope.get(k) || scope.get(k) === event[k as "runId"])) message?.call(this, e);
          },
          set: (value: EventSource["onmessage"]) => { message = value; },
        });
      }
    }
    globalThis.EventSource = window.EventSource = ScopedSource;
    try {
      const page = await taskPage(new FixtureClient("a"), { tab: "sessions", runId: RUN_ID });
      await until(() => page.querySelector('[data-testid="run-screen"]'), "the open run");
      await settle(500);
      expect(renders).toBeGreaterThan(0);
      renders = 0;
      const frame = (runId: string) => emit({ eventType: "agent.thought", occurredAt: new Date().toISOString(), organizationId: ORG.id,
        projectId: PROJECT.id, taskId: TASK_ID, runId, sessionId: null, workflowRunId: null,
        actor: { type: "system", id: "dude" }, source: "control-plane", correlationId: null, causationId: null, payload: { text: "Thinking" } });
      for (let i = 0; i < 10; i++) {
        await act(async () => frame("run_unrelated_probe"));
        await settle(10);
      }
      await settle(350);
      expect(renders).toBe(0);
      await act(async () => frame(RUN_ID));
      await settle();
      expect(renders).toBeGreaterThan(0);
    } finally {
      for (const unmount of mounted) await unmount();
      mounted = [];
      memo.type = screen;
      globalThis.EventSource = source;
      window.EventSource = windowSource;
    }
  });

  for (const error of [new Error("boom"), new ApiError(404, "not_found", "missing")]) {
    test(`Sessions keeps its switcher when the open run rejects with ${error.message}`, async () => {
      const client = new FixtureClient("d");
      client.getRun = async () => { throw error; };
      const opened: string[] = [];
      const page = await taskPage(client, { tab: "sessions", onOpenRun: (id) => opened.push(id) });
      await until(() => page.textContent?.includes(error instanceof ApiError ? "doesn't exist" : "boom") ? true : null, "the run error");
      const list = await sessionOptions(page);
      const rows = [...list.querySelectorAll<HTMLElement>('[role="option"]')];
      expect(rows.length).toBe(5);
      const other = rows.find((r) => r.getAttribute("aria-current") !== "true")!;
      await act(async () => { other.dispatchEvent(new MouseEvent("mousedown", { bubbles: true })); });
      expect(opened).toEqual([other.getAttribute("data-run")!]);
    });
  }

  test("Files kind counts stay put and Download all reads only displayed latest versions", async () => {
    const client = new FixtureClient("a");
    const artifact = (id: string, name: string, contentType: string) => ({ id, name, phase: "implement", role: "implementer", contentType, sizeBytes: 3, version: 1, createdAt: "2026-10-01", description: "" }) as Artifact;
    client.listArtifacts = async () => ({ artifacts: [artifact("latest", "notes.md", "text/markdown"), artifact("old", "notes.md", "text/markdown"), artifact("image", "shot.png", "image/png")] });
    const reads: string[] = [];
    client.artifactContent = async (id) => { reads.push(id); return new Blob([id]); };
    const page = await taskPage(client, { tab: "files" });
    await until(() => reads.includes("image") ? true : null, "thumbnail read");
    const kinds = await until(() => page.querySelector<HTMLElement>('[role="group"][aria-label="File kind"]'), "kind facets");
    const counts = kinds.textContent;
    await click(facetCheckbox(kinds, "Documents"));
    expect(kinds.textContent).toBe(counts);
    expect(page.querySelectorAll('[data-testid="file-row"]').length).toBe(1);
    expect(page.querySelectorAll('[data-testid="file-card"]').length).toBe(0);
    reads.length = 0;
    const anchorClick = HTMLAnchorElement.prototype.click;
    HTMLAnchorElement.prototype.click = () => {};
    try {
      await click(page.querySelector('[data-testid="download-all"]')!);
      await until(() => reads.length ? true : null, "zip reads");
      await settle();
      expect(reads).toEqual(["latest"]);
    } finally {
      HTMLAnchorElement.prototype.click = anchorClick;
    }
  });

  test("Activity facets classify real steers and filter kind and person without changing counts", async () => {
    const client = new FixtureClient("d");
    const base = (await client.events({ taskId: TASK_ID })).events[0]!;
    const actors = PEOPLE.slice(0, 2);
    const events: PersistedEvent[] = [
      { ...base, eventId: "steer-a", cursor: 90001, eventType: "run.steered", actor: { type: "human", id: actors[0]!.id }, payload: { text: "First steer" } },
      { ...base, eventId: "steer-b", cursor: 90002, eventType: "run.steered", actor: { type: "human", id: actors[1]!.id }, payload: { text: "Second steer" } },
      { ...base, eventId: "created", cursor: 90003, eventType: "task.created", actor: { type: "human", id: actors[0]!.id }, payload: {} },
    ];
    client.events = async () => ({ events, nextCursor: 90003 });
    const page = await taskPage(client, { tab: "activity" });
    await until(() => page.querySelectorAll('[data-testid="activity-item"]').length === 3 ? true : null, "three real events");
    const facet = (group: string, label: string) => {
      const found = [...page.querySelectorAll<HTMLElement>(`[role="group"][aria-label="${group}"] [role="checkbox"]`)].find((b) => b.parentElement?.textContent?.startsWith(label));
      expect(found).toBeDefined();
      return found!;
    };
    const kinds = page.querySelector('[aria-label="Event kind"]')!;
    const counts = kinds.textContent;
    await click(facet("Event kind", "Steers & decisions"));
    expect([...page.querySelectorAll('[data-testid="activity-item"]')].map((l) => l.textContent)).toEqual([
      expect.stringContaining("Second steer"), expect.stringContaining("First steer"),
    ]);
    expect(kinds.textContent).toBe(counts);
    await click(facet("Activity person", actors[1]!.name));
    const lines = [...page.querySelectorAll('[data-testid="activity-item"]')];
    expect(lines.length).toBe(1);
    expect(lines[0]!.textContent).toContain("Second steer");
    expect(lines[0]!.textContent).toContain(actors[1]!.name);
    expect(kinds.textContent).toBe(counts);
  });

  for (const decider of ["policy", "conductor"] as const) {
    test(`the header, not the Overview rail, says who decides for a ${decider} task`, async () => {
      const client = new FixtureClient("d");
      const getTask = client.getTask.bind(client);
      client.getTask = async (id) => ({ ...(await getTask(id)), decider });
      const page = await taskPage(client);
      const chip = await until(() => page.querySelector<HTMLElement>("[data-testid=task-header] [data-testid=driver]"), "the driver chip");
      expect(chip.getAttribute("data-driver")).toBe(decider === "conductor" ? "conductor" : "deliver");
      // Right after the status.
      expect(chip.previousElementSibling?.getAttribute("data-testid")).toBe("header-status");
      expect([...page.querySelectorAll("dt")].filter((dt) => dt.textContent === "Decides").length).toBe(0);
    });
  }

  test("the session selector searches model and status words", async () => {
    const client = new FixtureClient("d");
    const getTask = client.getTask.bind(client);
    const runs = (await getTask(TASK_ID)).runs;
    const target = runs.find((r) => r.phase !== null)!;
    const failed = runs.find((r) => r.id !== target.id)!;
    client.getTask = async (id) => {
      const task = await getTask(id);
      return { ...task, runs: task.runs.map((r) => r.id === target.id ? { ...r, model: "zz-unique-model" } : r.id === failed.id ? { ...r, status: "failed" as const } : r) };
    };
    const page = await taskPage(client, { tab: "sessions" });
    const list = await sessionOptions(page);
    const all = list.querySelectorAll('[role="option"]').length;
    const input = document.querySelector<HTMLInputElement>('input[role="combobox"]')!;
    const search = async (words: string) => {
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, words);
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
      await settle(20);
      return [...document.querySelectorAll<HTMLElement>('[data-testid="sessions"] [role="option"]')].map((o) => o.getAttribute("data-run"));
    };
    expect(all).toBeGreaterThan(1);
    expect(await search("zz-unique-model")).toEqual([target.id]);
    expect(await search("failed")).toEqual([failed.id]);
  });

  test("activity filters structured person, event kind and attempt independently of prose", () => {
    const lines = [{ kind: "decisions", personId: "ana", attempt: 2 }, { kind: "sessions", personId: "agents", attempt: 1 }, { kind: "decisions", personId: "ana", attempt: 1 }] as ReturnType<typeof activityLines>;
    expect(filterActivity(lines, ["decisions"], ["ana"], ["2"])).toEqual([lines[0]!]);
    expect(filterActivity(lines, [], [], []).length).toBe(3);
  });
});

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

export async function taskPage(client: FixtureClient, props: Partial<Parameters<typeof TaskScreen>[0]> = {}) {
  const { container, unmount } = await mount(
    <ToastProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} {...props} />
    </PeopleProvider></ToastProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  return container;
}

describe("machine reset confirmation", () => {
  test("keeps the note on exact refusal, prevents duplicates and closes only after success", async () => {
    let closed = 0;
    const notes: string[] = [];
    let resolve: (() => void) | undefined;
    const m = await mount(<ResetMachineConfirm label="implementer" from="Large" to="Standard" by="Márcio"
      onClose={() => closed++} onConfirm={async (note) => {
        notes.push(note);
        if (notes.length === 1) throw new ApiError(409, "moving", "The implementer is being moved to Large now.");
        await new Promise<void>((r) => { resolve = r; });
      }} />);
    mounted.push(m.unmount);
    const field = document.querySelector<HTMLTextAreaElement>("[data-testid=reset-note]")!;
    expect(field.maxLength).toBe(2000);
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(field, "Keep two workers.");
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const button = document.querySelector<HTMLButtonElement>("[data-testid=confirm-reset]")!;
    await click(button);
    expect(document.querySelector("[data-testid=reset-error]")?.textContent).toBe("The implementer is being moved to Large now.");
    expect(field.value).toBe("Keep two workers.");
    expect(closed).toBe(0);
    await click(button);
    expect(button.disabled).toBe(true);
    await click(button);
    expect(notes).toEqual(["Keep two workers.", "Keep two workers."]);
    await act(async () => resolve!());
    expect(closed).toBe(1);
  });
});

describe("machines raised in the task, on its Overview", () => {
  afterEach(() => localStorage.removeItem(RUN_KEY));

  test("the implementer's raise with its provenance and Reset, the fixer following it without one", async () => {
    localStorage.setItem(RUN_KEY, "raised");
    const page = await taskPage(new FixtureClient("a"));
    const panel = await until(() => page.querySelector<HTMLElement>("[data-testid=machine-raises]"), "the Machines panel");
    const rows = [...panel.querySelectorAll("li")].map((li) => li.textContent ?? "");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toContain("Implementer runs on XL");
    expect(rows[0]).toContain("Asked for by run_01j9x5m2q7k8e4t1 (the implementer)");
    expect(rows[0]).toContain("jest OOM-killed with --maxWorkers 4 on 8 GiB");
    expect(rows[1]).toContain("Fixer runs on XL");
    expect(rows[1]).toContain("Follows the implementer: it names no size of its own");
    const resets = [...panel.querySelectorAll("button")].map((b) => b.getAttribute("aria-label"));
    expect(resets).toEqual(["Reset the implementer to Standard"]);
  });

  test("Reset posts the role and note, and the panel goes once the detail has no raise; a refusal keeps the dialog", async () => {
    localStorage.setItem(RUN_KEY, "raised");
    const client = new FixtureClient("a");
    const page = await taskPage(client);
    const reset = async () => click(await until(() => page.querySelector<HTMLElement>("[aria-label='Reset the implementer to Standard']"), "Reset"));
    const note = async (text: string) => {
      const field = await until(() => document.querySelector<HTMLTextAreaElement>("[data-testid=reset-note]"), "the note");
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(field, text);
        field.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await reset();
    expect(document.body.textContent).toContain("Move the implementer back to Standard now?");
    // The fixture refuses a note of "fail" as the API refuses a Reset during a move.
    await note("fail");
    await click(document.querySelector<HTMLElement>("[data-testid=confirm-reset]")!);
    await until(() => document.querySelector("[data-testid=reset-error]"), "the refusal");
    expect(document.querySelector("[data-testid=reset-error]")!.textContent).toBe("a machine move is under way");
    await note("keep two workers");
    await click(document.querySelector<HTMLElement>("[data-testid=confirm-reset]")!);
    await until(() => (page.querySelector("[data-testid=machine-raises]") ? null : true), "the panel gone");
  });
});

const selectedTab = (page: HTMLElement) => page.querySelector("[role=tab][aria-selected=true]")?.textContent ?? "";

describe("the task page's tab from its URL", () => {
  test("a URL naming Servers opens the page on its servers", async () => {
    const page = await taskPage(new FixtureClient("a"), { tab: "servers" });
    expect(selectedTab(page)).toStartWith("Servers");
    await until(() => page.querySelector("[data-testid=servers-panel]"), "the servers panel");
  });

  test("a URL naming no tab opens the overview", async () => {
    const page = await taskPage(new FixtureClient("a"));
    expect(selectedTab(page)).toBe("Overview");
  });
});

describe("the task header's owner", () => {
  test("says once whose task it is and whose agent works, and the select beside it is named for changing it", async () => {
    const page = await taskPage(new FixtureClient("a"));
    const header = await until(() => page.querySelector<HTMLElement>("[data-testid=task-header]"), "the header");
    const owner = await until(() => header.querySelector<HTMLElement>("[data-testid=task-owner]"), "the owner field");
    // The attribution: the owner's name and that their implementer is working.
    expect(header.textContent).toContain("Márcio Martins");
    expect(header.textContent).toContain("Owner · Márcio's implementer is working");
    // "Owner" labels the attribution only; the select is the action.
    expect(header.textContent!.split("Owner")).toHaveLength(2);
    const select = owner.querySelector<HTMLElement>("[role=combobox]")!;
    expect(select.getAttribute("aria-label")).toBe("Change owner");
    expect(owner.textContent).toContain("Change owner");
    expect(owner.textContent).toContain("Márcio Martins");
  });
});

describe("Activity's started lines", () => {
  const run = (id: string, phase: string, role: string, category: string | null = null) => ({ id, phase, role, category, kind: "agent", attempt: 1 }) as unknown as Run;
  let cursor = 0;
  const created = (runId: string | null, payload: Record<string, unknown>): PersistedEvent => ({
    eventId: `evt_c${++cursor}`, cursor, eventType: "run.created", occurredAt: new Date(Date.UTC(2026, 9, 3, 10, 0, cursor)).toISOString(),
    organizationId: "org", projectId: "prj", taskId: TASK_ID, runId, sessionId: null, workflowRunId: null,
    actor: { type: "system", id: "workflow" }, source: "control-plane", correlationId: null, causationId: null, payload,
  }) as PersistedEvent;
  const said = (events: PersistedEvent[], runs: Run[]) => {
    const people = { names: new Map<string, string>(), byId: new Map(), you: null } as unknown as Parameters<typeof activityLines>[1];
    return activityLines(events, people, runs, [], 1).map((l) => renderToStaticMarkup(<>{l.text}</>).replace(/<[^>]+>/g, ""));
  };

  test("name who started, as the role does, never the stage or a bare agent", () => {
    const runs = [run("r_impl", "implement", "implementer"), run("r_rev", "review", "reviewer", "correctness"),
      run("r_fix", "fix", "implementer"), run("r_simp", "simplify", "simplifier")];
    const lines = said([
      created("r_impl", { phase: "implement", role: "implementer" }),
      created("r_rev", { phase: "review", role: "reviewer", category: "correctness" }),
      // The round's own event: no Run, its reviewers named in the payload.
      created(null, { phase: "review", reviewers: ["correctness", "security"], iteration: 0 }),
      created("r_fix", { phase: "fix", role: "implementer" }),
      created("r_simp", { phase: "simplify", role: "simplifier" }),
    ], runs);
    // Newest first, as Activity reads.
    expect(lines).toEqual([
      "The simplifier started",
      "The fixer started",
      "A review round started: correctness, security",
      "The correctness reviewer started",
      "The implementer started",
    ]);
    expect(lines.some((l) => l.includes("The agent started") || l.includes("The simplify"))).toBe(false);
  });

  test("a Run the page has not read yet is named from its event", () => {
    expect(said([created("r_later", { phase: "simplify", role: "simplifier" })], [])).toEqual(["The simplifier started"]);
  });

  test("who changed who decides, by name, in the words of why; Talk it through's start is not said again", () => {
    const changed = (why: string, from: string | null, to: string, actor: PersistedEvent["actor"]): PersistedEvent =>
      ({ ...created(null, {}), eventType: "task.decider_changed", actor, payload: { from, to, why } }) as PersistedEvent;
    const ana = { type: "human", id: "u_ana", name: "Ana" } as PersistedEvent["actor"];
    const events = [
      changed("talk it through", null, "conductor", ana),
      changed("a person wrote in Chat", "policy", "conductor", ana),
      changed("a person let Deliver finish it", "conductor", "policy", ana),
      changed("a person gave the decisions to the conductor", "policy", "conductor", ana),
    ];
    expect(said(events, [])).toEqual([
      "Ana gave the decisions to the conductor",
      "Ana let Deliver finish it",
      "Ana wrote in Chat, which gave the decisions to the conductor",
    ]);
    const people = { names: new Map<string, string>(), byId: new Map(), you: null } as unknown as Parameters<typeof activityLines>[1];
    const quotes = activityLines(events, people, [], [], 1).map((l) => l.quote);
    expect(quotes).toEqual(["Deliver → Conductor", "Conductor → Deliver", "Deliver → Conductor"]);
  });

  test("the API reviewer keeps its capitals, in its own line and in the round's; other categories stay lower case", () => {
    const lines = said([
      created("r_api", { phase: "review", role: "reviewer", category: "api" }),
      created(null, { phase: "review", reviewers: ["correctness", "api"], iteration: 0 }),
      created("r_api_unread", { phase: "review", role: "reviewer", category: "api" }),
    ], [run("r_api", "review", "reviewer", "api")]);
    expect(lines).toEqual([
      "The API reviewer started",
      "A review round started: correctness, API",
      "The API reviewer started",
    ]);
  });
});

describe("a finding's category", () => {
  test("is its reviewer's flavour, with the reviewer's own word beside it when it has one", async () => {
    const page = await taskPage(new FixtureClient("d"), { tab: "findings" });
    const rows = await until(() => {
      const r = [...page.querySelectorAll<HTMLElement>("[data-testid=finding]")];
      return r.length === 4 ? r : null;
    }, "the findings");
    const text = rows.map((r) => r.textContent ?? "");
    expect(text.filter((t) => t.includes("correctness · css"))).toHaveLength(1);
    expect(text.filter((t) => t.includes("correctness") && !t.includes("·"))).toHaveLength(3);
  });
});

/** The Servers tab: its count, its mark's state, and what its tooltip says on focus. */
async function serversTab(scenario: "a" | "b" | "c" | "d" | "e") {
  const page = await taskPage(new FixtureClient(scenario));
  const tab = await until(() => {
    const t = [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((x) => x.textContent?.startsWith("Servers"));
    // The servers are read apart from the task: wait for them to say something.
    return t && (t.children.length > 0 || t.getAttribute("aria-describedby") !== null || scenario === "d") ? t : null;
  }, "the Servers tab");
  await settle(50);
  await act(async () => tab.focus());
  const tip = await until(() => document.querySelector("[role=tooltip]"), "the tab's tooltip");
  return {
    label: tab.textContent,
    mark: tab.querySelector("[data-server-state]")?.getAttribute("data-server-state") ?? null,
    tip: tip.textContent,
  };
}

describe("the task's Servers tab", () => {
  test("two on (web ready, api starting): the count, the starting dot, both named", async () => {
    const t = await serversTab("a");
    expect(t.label).toStartWith("Servers2");
    expect(t.mark).toBe("starting");
    expect(t.tip).toContain("2 servers on");
    expect(t.tip).toContain("web");
    expect(t.tip).toContain(":3000");
    expect(t.tip).toContain("api");
    expect(t.tip).toContain("Off: storybook");
  });

  test("one on and one exited: the count of what is on, and the danger dot", async () => {
    const t = await serversTab("c");
    expect(t.label).toStartWith("Servers1");
    expect(t.mark).toBe("exited");
    expect(t.tip).toContain("1 server on");
    expect(t.tip).toContain("Off: api (exited), storybook");
  });

  test("none on after a move: no count, the move's mark", async () => {
    const t = await serversTab("b");
    expect(t.label).not.toMatch(/\d/);
    expect(t.mark).toBe("unreachable");
    expect(t.tip).toContain("No servers on");
    expect(t.tip).toContain("Off: web, api, storybook");
  });

  test("a branch preview coming up: no count yet, the breathing dot", async () => {
    const t = await serversTab("e");
    expect(t.label).not.toMatch(/\d/);
    expect(t.mark).toBe("starting");
    expect(t.tip).toContain("No servers on");
  });

  test("no run: no count, no mark, and the project's servers named as off", async () => {
    const t = await serversTab("d");
    expect(t.label).toBe("Servers");
    expect(t.mark).toBeNull();
    expect(t.tip).toContain("No servers on");
    expect(t.tip).toContain("Off: web, api, storybook");
  });

  test("no run and no recipes: nothing to say, so no tooltip", async () => {
    class Bare extends FixtureClient {
      override taskServers() {
        return super.taskServers().then((d) => ({ ...d, run: null, servers: [], recipes: [] }));
      }
    }
    const page = await taskPage(new Bare("d"));
    const tab = await until(() => [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((x) => x.textContent === "Servers"), "the Servers tab");
    await settle(100);
    await act(async () => tab.focus());
    await settle(50);
    expect(document.querySelector("[role=tooltip]") !== null).toBe(false);
  });
});

describe("a branch preview on the Servers tab", () => {
  for (const since of ["present", "null", "absent"] as const) {
    test(`stage timer uses previewStageSince ${since}`, async () => {
      const timestamp = { present: new Date(Date.now() - 24000).toISOString(), null: null, absent: undefined }[since];
      class TimedPreview extends FixtureClient {
        override taskServers() {
          return super.taskServers().then((d) => ({ ...d, run: d.run ? {
            ...d.run, startedAt: new Date(Date.now() - 34 * 60000).toISOString(),
            previewStage: "volumes" as const, ...(timestamp === undefined ? {} : { previewStageSince: timestamp }),
          } : null }));
        }
      }
      const page = await taskPage(new TimedPreview("e"), { tab: "servers" });
      const progress = await until(() => page.querySelector<HTMLElement>('[aria-label="Preview progress"]'), "progress");
      expect(progress.textContent).toContain("Restoring its workspace");
      expect(progress.textContent).not.toContain("Cloning");
      const active = progress.querySelector('[aria-current="step"]')!;
      expect(active.textContent).not.toContain("34m");
      expect(active.textContent?.includes("·")).toBe(Boolean(timestamp));
      // The timer counts from previewStageSince (24s ago), not from now or startedAt.
      if (timestamp) expect(active.textContent).toMatch(/\b2[4-6]s\b/);
    });
  }
  test("the server rail names the normal server states, not dangling separators", async () => {
    const page = await taskPage(new FixtureClient("a"), { tab: "servers" });
    const rail = await until(() => page.querySelector<HTMLElement>('[aria-label="Serving run and server actions"]'), "the server rail");
    await until(() => rail.textContent?.includes("Setups") ? true : null, "server setups");
    const setups = [...rail.querySelectorAll("section")].find((s) => s.textContent?.includes("Setups"));
    expect(setups).toBeDefined();
    expect([...setups!.querySelectorAll("p")].map((p) => p.textContent)).toEqual(["web · Ready", "api · Starting", "storybook · Stopped"]);
    expect(setups!.querySelectorAll('[data-server-state]').length).toBe(3);
  });

  test("a stopping preview says Stopping, not starting, on its run and its tab", async () => {
    class StoppingPreview extends FixtureClient {
      override taskServers() {
        return super.taskServers().then((d) => ({ ...d, run: d.run ? {
          ...d.run, previewStage: "stopping" as const, previewStageSince: new Date().toISOString(), luxState: "stopping",
        } : null }));
      }
    }
    const page = await taskPage(new StoppingPreview("e"), { tab: "servers" });
    const progress = await until(() => page.querySelector<HTMLElement>('[aria-label="Preview progress"]'), "progress");
    expect(progress.getAttribute("data-stage")).toBe("stopping");
    const panel = page.querySelector<HTMLElement>("[data-testid=servers-panel]")!;
    const mark = await until(() => page.querySelector<HTMLElement>('[aria-label="Serving run and server actions"] [data-status]'), "the run's mark");
    expect(mark.getAttribute("data-status")).not.toBe("starting");
    expect(mark.textContent).toBe("Stopping");
    // The tab reads the same servers: nothing on, and no "Preview starting" dot.
    const tab = [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((x) => x.textContent?.startsWith("Servers"))!;
    await act(async () => tab.focus());
    const tip = await until(() => document.querySelector("[role=tooltip]"), "the tab's tooltip");
    expect(tip.textContent).toContain("No servers on");
    // toBeNull passes on a happy-dom element: compare instead.
    expect(tab.querySelector("[data-server-state]") === null).toBe(true);
  });
  /** Scenario e's preview, past its stages: ready, lux running it. */
  class ReadyPreview extends FixtureClient {
    override taskServers() {
      return super.taskServers().then((d) => ({ ...d, run: d.run ? { ...d.run, previewStage: "ready" as const, luxState: "running" } : null }));
    }
  }
  const states = (page: HTMLElement) =>
    Object.fromEntries([...page.querySelectorAll("[data-testid=servers-panel] [data-server][data-state]")].map((el) => [el.getAttribute("data-server"), el.getAttribute("data-state")]));
  // Every server of the three in one of `want`: an empty panel is not a pass.
  const all = (page: HTMLElement, ...want: string[]) => {
    const s = Object.values(states(page));
    return s.length === 3 && s.every((x) => want.includes(x as string)) ? true : null;
  };
  const button = (page: HTMLElement, id: string) => page.querySelector<HTMLButtonElement>(`[data-testid=${id}]`);

  test("offers Start all and Stop all beside Stop preview, and they leave the preview live", async () => {
    const page = await taskPage(new ReadyPreview("e"), { tab: "servers" });
    await until(() => page.querySelector("[data-testid=servers-panel][data-run=preview]"), "the preview's panel");
    expect(button(page, "stop-preview") !== null).toBe(true);
    const startAll = await until(() => button(page, "start-all"), "Start all on the preview");
    expect(button(page, "stop-all") !== null).toBe(true);
    for (const id of ["start-all", "stop-all", "stop-preview"]) {
      expect(button(page, id)?.querySelectorAll('svg[aria-hidden="true"]').length).toBe(1);
    }

    await click(startAll);
    await until(() => all(page, "starting", "ready"), "every server starting");
    await until(() => (button(page, "stop-all")?.disabled === false ? true : null), "Stop all ready to press");
    await click(button(page, "stop-all")!);
    await until(() => all(page, "stopped"), "every server stopped");
    // Stop all is not Stop preview: the preview run is still there, still live.
    expect(page.querySelector("[data-testid=servers-panel][data-run=preview]") !== null).toBe(true);
    expect(button(page, "stop-preview") !== null).toBe(true);
    expect(button(page, "add-server")?.disabled).toBe(false);

    await until(() => (button(page, "start-all")?.disabled === false ? true : null), "Start all ready to press");
    await click(button(page, "start-all")!);
    await until(() => all(page, "starting", "ready"), "every server started again");
  });

  test("a wakeable preview asleep: says so, how long until it sleeps, and offers no Add server", async () => {
    class AsleepPreview extends FixtureClient {
      override taskServers() {
        return super.taskServers().then((d) => ({
          ...d,
          run: d.run ? { ...d.run, state: "paused" as const, wakeable: true, asleep: true, previewStage: null, parksAfterMinutes: 30 } : null,
        }));
      }
    }
    const page = await taskPage(new AsleepPreview("e"), { tab: "servers" });
    const panel = await until(() => page.querySelector<HTMLElement>("[data-testid=servers-panel][data-run=preview]"), "the preview's panel");
    const rail = await until(() => page.querySelector<HTMLElement>('[aria-label="Serving run and server actions"]'), "the run rail");
    await until(() => (rail.textContent?.includes("· asleep") ? true : null), "the asleep detail");
    expect(rail.textContent).toContain("sleeps after 30m without a request");
    expect(rail.textContent).not.toContain("parks after");
    expect(rail.textContent).toContain("Opening its stable URLs wakes it on the branch's latest commit");
    expect(button(page, "add-server")?.disabled).toBe(true);
    expect(page.querySelector("[data-testid=preview-error]")).toBe(null);
  });

  test("a wakeable preview whose Run failed to start says why", async () => {
    const why = "the preview's Run failed to start (start-failed) 3 times in a row; starting a server, or opening a URL once lux stops waiting, tries a new Run";
    class FailedPreview extends FixtureClient {
      override taskServers() {
        return super.taskServers().then((d) => ({
          ...d,
          run: d.run ? { ...d.run, state: "paused" as const, wakeable: true, asleep: true, previewStage: null, error: why } : null,
        }));
      }
    }
    const page = await taskPage(new FailedPreview("e"), { tab: "servers" });
    const callout = await until(() => page.querySelector<HTMLElement>("[data-testid=preview-error]"), "the preview's error");
    expect(callout.textContent).toBe(why);
  });

  test("an old-style preview's error is not shown as a wakeable preview's callout", async () => {
    class OldStyle extends FixtureClient {
      override taskServers() {
        return super.taskServers().then((d) => ({
          ...d,
          run: d.run ? { ...d.run, wakeable: false, asleep: false, error: "the preview stopped: failed" } : null,
        }));
      }
    }
    const page = await taskPage(new OldStyle("e"), { tab: "servers" });
    await until(() => page.querySelector("[data-testid=servers-panel][data-run=preview]"), "the preview's panel");
    expect(page.querySelector("[data-testid=preview-error]")).toBe(null);
  });

  test("a wakeable preview's error goes once it recovers", async () => {
    let error: string | null = "the preview's Run failed to start (start-failed); trying a new Run (start 2 of 3)";
    class Recovering extends FixtureClient {
      override taskServers() {
        return super.taskServers().then((d) => ({
          ...d,
          run: d.run ? { ...d.run, wakeable: true, asleep: false, error } : null,
        }));
      }
    }
    const page = await taskPage(new Recovering("e"), { tab: "servers" });
    await until(() => page.querySelector("[data-testid=preview-error]"), "the preview's error");
    error = null;
    const { emitForTest } = await import("./dom.ts");
    await emitForTest("servers.changed");
    await until(() => (page.querySelector("[data-testid=preview-error]") === null ? true : null), "the error gone after recovery");
  });
});

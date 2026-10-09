/**
 * Servers: the words a Server object becomes, the marks a list puts on a
 * tab, and what the row and the recipe form say — server-rendered, so
 * a state that gained a treatment or lost one fails here.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { egressProblem, type RunServer, type TaskServers } from "@dude/domain";
import { HostChips } from "../src/components/HostChips.tsx";
import { PreviewAlsoRunning, PreviewStages } from "../src/components/PreviewStages.tsx";
import { draftOf, draftProblems, recipeOf } from "../src/components/ServerRecipe.tsx";
import { ServerRow, ServersTabTip } from "../src/components/ServerRow.tsx";
import { ServerStateMark } from "../src/components/ServerStateMark.tsx";
import { ServersSummaryRow } from "../src/components/ServersSummary.tsx";
import { SERVER_DISPLAY_STATES, SERVER_STATE_SPECS } from "../src/tokens/servers.ts";
import { canStartAny, canStopAny, describeServer, isOn, safeServerUrl, serverLogLines, summarizeServers, summarizeTaskServers } from "../src/util/servers.ts";

const html = (el: React.ReactElement) => renderToStaticMarkup(el);
const text = (h: string) => h.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();
const buttons = (h: string) => [...h.matchAll(/<button\b[^>]*>([\s\S]*?)<\/button>/g)].map((m) => m[1]!.replace(/<[^>]+>/g, "").trim()).filter(Boolean);

const NOW = Date.parse("2026-09-28T14:44:00Z");
const at = (agoMs: number) => new Date(NOW - agoMs).toISOString();
const MIN = 60_000;

function server(patch: Partial<RunServer> & { state: RunServer["state"] }): RunServer {
  return {
    name: "web", port: 3000, command: ["sh", "-c", "npm run dev"], workdir: "apps/web", env: {}, fromSpec: false,
    since: at(0), readySince: null, stopReason: null, stoppedEpoch: null, epoch: 1, url: "https://web-abc.lux.example",
    ...patch,
  };
}

describe("the server state vocabulary", () => {
  test("every state has a tone, a glyph and a label, and stopped is the filled square", () => {
    for (const s of SERVER_DISPLAY_STATES) {
      const spec = SERVER_STATE_SPECS[s];
      expect(spec.tone, s).toBeTruthy();
      expect(spec.glyph, s).toBeTruthy();
      expect(spec.label.trim().length, s).toBeGreaterThan(0);
    }
    expect(SERVER_STATE_SPECS.stopped.glyph).toBe("stop");
    expect(SERVER_STATE_SPECS.exited.tone).toBe("danger");
    expect(SERVER_STATE_SPECS.starting.live).toBe(true);
  });

  test("the mark carries the word, or an sr-only word with the title", () => {
    expect(text(html(<ServerStateMark state="exited" label="Exited 1" />))).toBe("Exited 1");
    const icon = html(<ServerStateMark state="ready" iconOnly />);
    expect(icon).toContain('title="Ready"');
    expect(icon).toContain('data-server-state="ready"');
  });
});

describe("describeServer", () => {
  test("ready: how long, from readySince", () => {
    const w = describeServer(server({ state: "ready", since: at(12 * MIN), readySince: at(12 * MIN) }), NOW);
    expect(w).toEqual({ state: "ready", detail: "ready for 12m" });
  });

  test("starting counts up; exited carries its code and when", () => {
    expect(describeServer(server({ state: "starting", since: at(9_000) }), NOW).detail).toBe("starting · 9.0s");
    const w = describeServer(server({ state: "exited", exitCode: 1, since: at(15 * MIN), stoppedEpoch: 1 }), NOW);
    expect(w.state).toBe("exited");
    expect(w.label).toBe("Exited 1");
    expect(w.detail).toMatch(/^exited \d\d:\d\d$/);
  });

  test("stopped: never started, or when and for how long it had been ready", () => {
    expect(describeServer(server({ state: "stopped" }), NOW).detail).toBe("not started");
    const w = describeServer(server({ state: "stopped", since: at(12 * MIN), readySince: at(53 * MIN), stopReason: "migrated", stoppedEpoch: 1 }), NOW);
    expect(w.detail).toMatch(/^stopped at \d\d:\d\d · was ready for 41m$/);
  });

  test("on a preview before its servers' turn, a spec server waits; a runtime one is manual", () => {
    const run = { kind: "preview" as const, previewStage: "setup" as const };
    expect(describeServer(server({ state: "stopped", fromSpec: true }), NOW, run)).toEqual({ state: "waiting", detail: "starts after setup" });
    expect(describeServer(server({ state: "stopped" }), NOW, run)).toEqual({ state: "stopped", detail: "manual" });
    expect(describeServer(server({ state: "stopped", fromSpec: true }), NOW, { ...run, previewStage: "starting" }).state).toBe("stopped");
  });

  test("a wakeable preview's server says lux's word while no process serves it", () => {
    expect(describeServer(server({ state: "stopped", serverState: "asleep" }), NOW)).toEqual({ state: "stopped", label: "Asleep", detail: "wakes when its URL is opened" });
    expect(describeServer(server({ state: "stopped", serverState: "waking" }), NOW)).toEqual({ state: "starting", label: "Waking", detail: "waking up" });
    expect(describeServer(server({ state: "starting", serverState: "waking", since: at(4_000) }), NOW).detail).toBe("starting · 4.0s");
    expect(describeServer(server({ state: "stopped", serverState: "no answer" }), NOW).label).toBe("No answer");
    // Once it serves, the process's own words.
    expect(describeServer(server({ state: "ready", serverState: "ready", readySince: at(2 * MIN), since: at(2 * MIN) }), NOW)).toEqual({ state: "ready", detail: "ready for 2m" });
  });
});

describe("summaries", () => {
  const list = [server({ state: "ready" }), server({ name: "api", state: "exited", exitCode: 1 }), server({ name: "docs", state: "stopped" })];

  test("the tab shows the first bad server, else how many are ready", () => {
    expect(summarizeServers(list)).toEqual({ bad: list[1]!, ready: 1 });
    expect(summarizeServers([list[0]!, list[2]!])).toEqual({ bad: null, ready: 1 });
  });

  const agent = { id: "run_1", luxRunId: "run_x", kind: "agent" as const, label: "Implementer run", state: "running", luxState: "running", host: null, startedAt: null,
    startedBy: null, branch: null, commit: null, previewStage: null, parksAfterMinutes: null, terminalUrl: null };
  const task = (servers: RunServer[], patch: Partial<TaskServers> = {}): TaskServers =>
    ({ run: agent, servers, moved: null, recipes: [], preview: null, ...patch });

  test("the task's tab counts the servers that are on: starting, ready or unreachable", () => {
    const all = [
      server({ name: "web", state: "ready" }),
      server({ name: "api", state: "starting" }),
      server({ name: "worker", state: "unreachable" }),
      server({ name: "docs", state: "stopped" }),
      server({ name: "jobs", state: "exited", exitCode: 1 }),
    ];
    const s = summarizeTaskServers(task(all))!;
    expect(s.on.map((x) => x.name)).toEqual(["web", "api", "worker"]);
    expect(s.off.map((x) => [x.name, x.state])).toEqual([["docs", "stopped"], ["jobs", "exited"]]);
    // The first bad one, by the rule the tab has always had: unreachable before a later exit.
    expect(s.bad?.name).toBe("worker");
    expect(s.starting).toBe(true);
    for (const state of ["starting", "ready", "unreachable"] as const) expect(isOn({ state })).toBe(true);
    for (const state of ["stopped", "exited", "waiting"] as const) expect(isOn({ state })).toBe(false);
  });

  test("nothing on is none, a bad one with nothing on is still bad, and nothing starting is still", () => {
    const off = summarizeTaskServers(task([server({ state: "stopped" }), server({ name: "api", state: "exited", exitCode: 1 })]))!;
    expect(off.on).toEqual([]);
    expect(off.bad?.name).toBe("api");
    expect(off.starting).toBe(false);
    expect(summarizeTaskServers(task([server({ state: "ready" })]))!.starting).toBe(false);
  });

  test("a branch preview still coming up is starting before any server is; a finished one is not", () => {
    const preview = { ...agent, kind: "preview" as const, previewStage: "setup" as const };
    const booting = summarizeTaskServers(task([server({ state: "stopped", fromSpec: true })], { run: preview }))!;
    expect([booting.starting, booting.booting]).toEqual([true, true]);
    const ready = summarizeTaskServers(task([server({ state: "stopped" })], { run: { ...preview, previewStage: "ready" } }))!;
    expect([ready.starting, ready.booting]).toEqual([false, false]);
    // A preview that ended is not coming up, whatever stage it last reported.
    const ended = summarizeTaskServers(task([], { run: { ...preview, state: "aborted" } }))!;
    expect(ended.booting).toBe(false);
    // An asleep wakeable preview is not coming up either.
    const asleep = summarizeTaskServers(task([server({ state: "stopped", serverState: "asleep" })],
      { run: { ...preview, state: "paused", previewStage: null, wakeable: true, asleep: true } }))!;
    expect([asleep.starting, asleep.booting]).toEqual([false, false]);
  });

  test("a branch preview stopping is not coming up", () => {
    const stopping = summarizeTaskServers(task([server({ state: "stopped", fromSpec: true })],
      { run: { ...agent, kind: "preview" as const, previewStage: "stopping" as const } }))!;
    expect([stopping.starting, stopping.booting]).toEqual([false, false]);
  });

  test("with no run, the recipes are all off; with no run and no recipes there is nothing to say", () => {
    const recipes = [{ name: "web" }, { name: "api" }] as TaskServers["recipes"];
    const none = summarizeTaskServers(task([], { run: null, recipes }))!;
    expect(none.on).toEqual([]);
    expect(none.off.map((x) => x.name)).toEqual(["web", "api"]);
    expect(summarizeTaskServers(task([], { run: null }))).toBeNull();
    expect(summarizeTaskServers(null)).toBeNull();
  });

  test("a move is remembered for the tab's mark", () => {
    expect(summarizeTaskServers(task([server({ state: "stopped" })], { moved: { at: at(0), fromHost: "a", toHost: "b" } }))!.moved).toBe(true);
  });

  test("the tab's tooltip: how many are on, a line for each with its word and port, then the rest", () => {
    const s = summarizeTaskServers(task([
      server({ name: "web", port: 3000, state: "ready" }),
      server({ name: "api", port: 8080, state: "starting" }),
      server({ name: "docs", port: 6006, state: "stopped" }),
      server({ name: "jobs", port: 9000, state: "exited", exitCode: 1 }),
    ]))!;
    const tip = html(<ServersTabTip summary={s} />);
    // What is read: the mark is aria-hidden, its screen-reader word with it, and the word after it is said once.
    const read = (h: string) => text(h.replace(/<span[^>]*aria-hidden="true"[^>]*>(?:<svg[\s\S]*?<\/svg>)?<span class="ds-sr-only">[^<]*<\/span><\/span>/g, ""));
    expect(read(tip)).toBe("2 servers on web ready :3000 api starting :8080 Off: docs, jobs (exited)");
    // The mark is its glyph: the word beside it is the one said.
    expect(tip).toContain('data-server-state="ready"');
    expect(text(html(<ServersTabTip summary={summarizeTaskServers(task([server({ state: "ready" })]))!} />))).toStartWith("1 server on");
    expect(text(html(<ServersTabTip summary={summarizeTaskServers(task([server({ state: "stopped" })]))!} />))).toBe("No servers on Off: web");
  });

  test("start all and stop all know when there is nothing to do; an unreachable server is restarted, not started", () => {
    expect(canStartAny(list)).toBe(true);
    expect(canStopAny(list)).toBe(true);
    expect(canStartAny([server({ state: "ready" })])).toBe(false);
    expect(canStartAny([server({ state: "unreachable" })])).toBe(false);
    expect(canStopAny([server({ state: "unreachable" })])).toBe(true);
    expect(canStopAny([server({ state: "stopped" })])).toBe(false);
  });

  test("lux's log lines keep their stream, mark the runtime's own, and keep their key across re-reads", () => {
    const lines = serverLogLines([{ t: 1000, stream: "stdout", text: "[lux] starting web" }, { t: 1000, stream: "stderr", text: "boom" }, { t: 1001, stream: "stdout", text: "ok" }]);
    expect(lines.map((l) => [l.seq, l.channel, l.level])).toEqual([[1_000_000, "stdout", "system"], [1_000_001, "stderr", undefined], [1_001_000, "stdout", undefined]]);
    // The same tail, slid on by one line: the lines kept have the seq they had.
    const later = serverLogLines([{ t: 1000, stream: "stderr", text: "boom" }, { t: 1001, stream: "stdout", text: "ok" }, { t: 1002, stream: "stdout", text: "more" }]);
    expect(later[1]!.seq).toBe(1_001_000);
    expect(later[2]!.seq).toBeGreaterThan(lines[2]!.seq);
  });
});

describe("ServerRow", () => {
  const row = (s: RunServer, extra = {}) => html(<ServerRow name={s.name} port={s.port} state={s.state} url={s.url} onStart={() => {}} onStop={() => {}} onRestart={() => {}} logs={{ open: false, onToggle: () => {}, lines: [] }} {...extra} />);
  const preview = (h: string) => h.match(/<a\b[^>]*data-testid="server-preview"[^>]*>/)?.[0] ?? null;

  test("ready: Preview (a link to a new tab), Logs, Restart, Stop; the URL opens", () => {
    const h = row(server({ state: "ready" }));
    expect(buttons(h)).toEqual(["Logs", "Stop"]);
    expect(preview(h)).toContain('href="https://web-abc.lux.example"');
    expect(preview(h)).toContain('target="_blank"');
    expect(preview(h)).toContain('rel="noopener noreferrer"');
    expect(h).toContain('aria-label="Restart web"');
    expect(h).toContain('aria-label="Open in a new tab"');
  });

  test("only an https URL is ever a link", () => {
    for (const url of ["javascript:alert(1)", "http://web-abc.lux.example", "data:text/html,x"]) {
      const h = row(server({ state: "ready", url }));
      expect(preview(h)).toBeNull();
      expect(h).not.toContain(`href="${url}"`);
      expect(h).not.toContain("Open in a new tab");
      const summary = html(<ServersSummaryRow name="web" state="ready" url={url} />);
      expect(summary).not.toContain("href=");
    }
    expect(safeServerUrl("https://web-abc.lux.example")).toBe("https://web-abc.lux.example");
    expect(safeServerUrl(null)).toBeNull();
    expect(html(<ServersSummaryRow name="web" state="ready" url="https://web-abc.lux.example" />)).toContain('href="https://web-abc.lux.example"');
  });

  test("stopped and exited: Start; the URL is there to copy but not to open", () => {
    const h = row(server({ state: "stopped" }));
    expect(buttons(h)).toEqual(["Logs", "Start"]);
    expect(h).toContain('aria-label="Copy URL"');
    expect(h).not.toContain("Open in a new tab");
    expect(buttons(row(server({ state: "exited", exitCode: 1 })))).toEqual(["Logs", "Start"]);
  });

  test("waiting: Start now, disabled; starting: Stop only; unreachable: Restart and Stop", () => {
    expect(row(server({ state: "stopped" }), { state: "waiting" })).toMatch(/<button[^>]*disabled[^>]*>(?:(?!<\/button>).)*Start now<\/button>/);
    expect(buttons(row(server({ state: "starting" })))).toEqual(["Logs", "Stop"]);
    const unreachable = row(server({ state: "unreachable" }));
    expect(buttons(unreachable)).toEqual(["Logs", "Stop"]);
    expect(unreachable).toContain('aria-label="Restart web"');
  });

  test("the log folds under the row as server:<name>", () => {
    const h = row(server({ state: "ready" }), { logs: { open: true, onToggle: () => {}, lines: [{ seq: 0, text: "hello" }] } });
    expect(h).toContain("server:web");
    expect(h).toContain("hello");
    expect(h).toContain('aria-expanded="true"');
  });
});

describe("PreviewStages", () => {
  for (const [stage, words] of [["image", "Preparing image"], ["volumes", "Restoring volumes"], ["container", "Starting container"], ["stopping", "Stopping"]] as const) {
    test(`${stage} is honest and has one accessible active step`, () => {
      const h = html(<PreviewStages stage={stage} branch="feature/x" />);
      expect(text(h)).toContain(words);
      expect(text(h)).not.toContain("Cloning");
      expect((h.match(/aria-current="step"/g) ?? []).length).toBe(1);
      expect(text(h)).not.toContain("·");
      if (stage === "stopping") expect(text(h)).toBe("Stopping");
    });
  }
  test("marks the current step and the done ones", () => {
    const h = html(<PreviewStages stage="setup" branch="feature/x" setup="npm ci" elapsed="24s" />);
    expect(h).toContain('data-stage="setup"');
    expect(text(h)).toBe("Scheduling Cloning feature/x Setup: npm ci · 24s Starting servers Ready");
    expect((h.match(/aria-current="step"/g) ?? []).length).toBe(1);
  });

  test("a preview behind the agent's run is said, with its Stop", () => {
    expect(text(html(<PreviewAlsoRunning onStop={() => {}} />))).toBe("A branch preview is also running. Stop preview");
    expect(text(html(<PreviewAlsoRunning parked />))).toBe("A branch preview is also parked.");
  });
});

describe("the recipe form", () => {
  test("a fresh draft needs a name, port and command; problems show once touched or typed", () => {
    const d = draftOf(null);
    expect(draftProblems(d, false)).toEqual({});
    expect(draftProblems(d, true)).toMatchObject({ command: expect.any(String) });
    expect(draftProblems({ ...d, name: "Web_App", port: "70000" }, false)).toMatchObject({ name: expect.stringContaining("Lowercase"), port: "Between 1 and 65535." });
    expect(draftProblems({ ...d, name: "web", port: "3000", command: "npm run dev" }, true)).toEqual({});
  });

  test("what is saved: trimmed, the directory without slashes, empty setup as null, blank vars dropped", () => {
    const d = { ...draftOf(null), name: " web ", port: "3000", command: " npm run dev ", workdir: "/apps/web/", setup: "  ", env: [{ name: "", value: "x" }, { name: "A", value: "1" }] };
    expect(recipeOf(d)).toEqual({ name: "web", port: 3000, command: "npm run dev", workdir: "apps/web", setup: null, env: [{ name: "A", value: "1" }], autostartInPreviews: false });
  });
});

describe("the egress allowlist", () => {
  test("what lux would refuse is said so, and a host listed already that it would is marked", () => {
    expect(egressProblem("*")).toBeNull();
    for (const ok of ["github.com", "10.0.0.5", "10.0.0.0/8", "2001:db8::/32", "::1", "*.github.com"]) expect(egressProblem(ok)).toBeNull();
    expect(egressProblem("*.com")).toContain("at least two labels");
    expect(egressProblem("10.0.0.0/33")).toContain("CIDR");
    const h = html(<HostChips hosts={["github.com", "*.npmjs"]} validate={egressProblem} onChange={() => {}} />);
    expect(h).toMatch(/data-host="\*\.npmjs" data-invalid="true"/);
    expect(h).not.toMatch(/data-host="github\.com" data-invalid/);
  });
});

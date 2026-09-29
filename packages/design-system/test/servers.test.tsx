/**
 * Servers: the words a Server object becomes, the marks a list puts on a
 * tab, and what the row and the recipe form say — server-rendered, so
 * a state that gained a treatment or lost one fails here.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { RunServer } from "@dude/domain";
import { PreviewStages } from "../src/components/PreviewStages.tsx";
import { draftOf, draftProblems, recipeOf } from "../src/components/ServerRecipe.tsx";
import { ServerRow } from "../src/components/ServerRow.tsx";
import { ServerStateMark } from "../src/components/ServerStateMark.tsx";
import { SERVER_DISPLAY_STATES, SERVER_STATE_SPECS } from "../src/tokens/servers.ts";
import { canStartAny, canStopAny, describeServer, serverLogLines, summarizeServers } from "../src/util/servers.ts";

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
});

describe("summaries", () => {
  const list = [server({ state: "ready" }), server({ name: "api", state: "exited", exitCode: 1 }), server({ name: "docs", state: "stopped" })];

  test("the tab shows the first bad server, else how many are ready", () => {
    expect(summarizeServers(list)).toEqual({ bad: list[1]!, ready: 1 });
    expect(summarizeServers([list[0]!, list[2]!])).toEqual({ bad: null, ready: 1 });
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
  const row = (s: RunServer, extra = {}) => html(<ServerRow name={s.name} port={s.port} state={s.state} url={s.url} onPreview={() => {}} onStart={() => {}} onStop={() => {}} onRestart={() => {}} logs={{ open: false, onToggle: () => {}, lines: [] }} {...extra} />);

  test("ready: Preview, Logs, Restart, Stop; the URL opens", () => {
    const h = row(server({ state: "ready" }));
    expect(buttons(h)).toEqual(["Preview", "Logs", "Stop"]);
    expect(h).toContain('aria-label="Restart web"');
    expect(h).toContain('aria-label="Open in a new tab"');
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
  test("marks the current step and the done ones", () => {
    const h = html(<PreviewStages stage="setup" branch="feature/x" setup="npm ci" elapsed="24s" />);
    expect(h).toContain('data-stage="setup"');
    expect(text(h)).toBe("Scheduling Cloning feature/x Setup: npm ci · 24s Starting servers Ready");
    expect((h.match(/aria-current="step"/g) ?? []).length).toBe(1);
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

/**
 * The calmer components, server-rendered: what each shows, what is a link
 * or a button, and what a screen reader is told. CSS module classes are
 * empty strings under `bun test`, so these read structure and text.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { CHECK_RUNS_FORBIDDEN, PR_DISPLAY_STATES, prCheckDiagnosticReason, type PrDisplayState } from "@dude/domain";
import { AgentPlan, PlanMeter } from "../src/components/AgentPlan.tsx";
import { Cost, costWords } from "../src/components/Cost.tsx";
import { MarkdownDocument, applyFormat, highlightMarkdown } from "../src/components/MarkdownDocument.tsx";
import { Markdown } from "../src/components/Markdown.tsx";
import { PersonAvatar, PersonAvatarStack } from "../src/components/PersonAvatar.tsx";
import { PR_DISPLAY_SPECS, PrChip, prFacts, type PrChipPullRequest } from "../src/components/PrChip.tsx";
import { ProjectAvatar } from "../src/components/ProjectAvatar.tsx";
import { PullRequestPanel } from "../src/components/PullRequestPanel.tsx";
import { StatusBadge } from "../src/components/StatusBadge.tsx";
import { StatusMark } from "../src/components/StatusMark.tsx";
import { ICON_NAMES } from "../src/icons/index.tsx";
import { ALL_STATUSES } from "../src/tokens/status.ts";

const html = (el: React.ReactElement) => renderToStaticMarkup(el);
const text = (h: string) => h.replace(/<[^>]+>/g, "");

describe("StatusMark", () => {
  test("every status is a glyph and a word", () => {
    for (const status of ALL_STATUSES) {
      const h = html(<StatusMark status={status} />);
      expect(h).toContain("<svg");
      expect(text(h).trim().length).toBeGreaterThan(0);
    }
  });
  test("icon only keeps the word for screen readers and the title", () => {
    const h = html(<StatusMark status="running" iconOnly />);
    expect(h).toContain('title="Running"');
    expect(text(h)).toBe("Running");
  });
  test("StatusBadge keeps its API and draws the mark", () => {
    const h = html(<StatusBadge status="failed" size="sm" />);
    expect(h).toContain('data-status="failed"');
    expect(text(h)).toBe("Failed");
  });
});

describe("PersonAvatar", () => {
  test("initials without a photo, the photo when there is one", () => {
    expect(text(html(<PersonAvatar person={{ id: "p1", name: "Ana Ribeiro" }} />))).toBe("AR");
    const h = html(<PersonAvatar person={{ id: "p1", name: "Ana Ribeiro", photoUrl: "/ana.jpg" }} />);
    expect(h).toContain('src="/ana.jpg"');
    expect(text(h)).toBe("");
  });
  test("says who, and that they are online", () => {
    expect(html(<PersonAvatar person={{ name: "Ana", online: true }} />)).toContain('aria-label="Ana · online"');
    expect(html(<PersonAvatar person={{ name: "Ana", online: false }} />)).toContain('aria-label="Ana"');
  });
  test("the agent working for them sits on the face and is named", () => {
    const h = html(<PersonAvatar person={{ name: "Ana" }} agent="implementer" live />);
    expect(h).toContain('data-role="implementer"');
    expect(h).toContain("Implementer working for them now");
  });
  test("a stack names everyone and folds the rest into +N", () => {
    const people = ["Ana", "Bo", "Cy", "Dee", "Eli"].map((name) => ({ name }));
    const h = html(<PersonAvatarStack people={people} max={3} />);
    expect(h).toContain('aria-label="Ana, Bo, Cy, Dee, Eli"');
    expect(text(h)).toContain("+3");
  });
});

describe("ProjectAvatar", () => {
  test("initials on its colour, or its image", () => {
    expect(text(html(<ProjectAvatar project={{ id: "prj", name: "Dashboard" }} />))).toBe("DA");
    expect(html(<ProjectAvatar project={{ name: "Billing API", imageUrl: "/b.svg" }} />)).toContain('src="/b.svg"');
  });
  test("a chosen colour wins over the hash", () => {
    expect(html(<ProjectAvatar project={{ name: "Greeter", colorSlot: 7 }} />)).toContain('data-identity="7"');
  });
});

describe("PrChip", () => {
  const pr = (over: Partial<PrChipPullRequest> = {}): PrChipPullRequest => ({
    number: 41, url: "https://github.com/acme/dashboard/pull/41", repositoryName: "acme/dashboard",
    state: "open", checks: "passing", review: "approved", ...over,
  });

  test("every display state has a word and a glyph that exists", () => {
    for (const state of PR_DISPLAY_STATES) {
      const spec = PR_DISPLAY_SPECS[state];
      expect(spec.label.length).toBeGreaterThan(0);
      expect(ICON_NAMES).toContain(spec.glyph);
    }
  });
  test("one state, a word and the number, linking to GitHub", () => {
    const h = html(<PrChip pr={pr({ checks: "failing", review: "changes_requested" })} />);
    expect(h).toContain('href="https://github.com/acme/dashboard/pull/41"');
    expect(h).toContain('data-pr-state="ci_red"');
    expect(text(h)).toBe("CI failing#41");
  });
  test("the API's display wins when it sends one", () => {
    expect(html(<PrChip pr={pr({ display: "conflict" as PrDisplayState })} />)).toContain('data-pr-state="conflict"');
  });
  test("icon only keeps the state in the accessible name", () => {
    const h = html(<PrChip pr={pr()} iconOnly />);
    expect(text(h)).toBe("");
    expect(h).toContain('aria-label="Ready to merge, pull request acme/dashboard#41 (opens on GitHub)"');
  });
  test("the tooltip lists the rest of what is true, from today's fields", () => {
    expect(prFacts(pr({ checks: "failing", review: "changes_requested" }))).toEqual(["Checks: failing", "Changes requested"]);
    expect(prFacts(pr({ review: "pending", checks: "unknown" }))).toEqual(["No review yet"]);
  });
  test("…and from the richer ones, when the forge sends them", () => {
    const facts = prFacts(pr({
      checks: [{ name: "e2e (chrome)", status: "completed", conclusion: "failure" }, { name: "unit", status: "completed", conclusion: "success" }],
      reviews: [{ login: "cy", state: "CHANGES_REQUESTED" }],
      mergeable: "behind", behindBy: 3, baseBranch: "main", unresolvedThreads: 2,
    }));
    expect(facts).toEqual(["Checks: e2e (chrome) failing", "Changes requested by cy", "3 commits behind main, no conflicts", "2 unresolved comments"]);
  });
  test("pending with no reason is CI pending, not CI at work", () => {
    const h = html(<PrChip pr={pr({ checks: "pending", display: "ci_running" })} />);
    expect(text(h)).toBe("CI pending#41");
    expect(prFacts(pr({ checks: "pending" }))).toContain("Checks: pending");
  });
});

describe("checks that cannot be read", () => {
  const denied = { name: "GitHub check runs", status: "unavailable", conclusion: "", url: "", durationMs: 0, diagnostic: CHECK_RUNS_FORBIDDEN };
  const codeRabbit = { name: "CodeRabbit", status: "completed", conclusion: "success" };
  const lint = { name: "lint", status: "completed", conclusion: "failure" };
  // As the API sends it: the scalar decides `display`, the list goes out as stored.
  const pr = (checks: PrChipPullRequest["checks"], display: PrDisplayState, over: Partial<PrChipPullRequest> = {}) => ({
    number: 101, url: "https://github.com/acme/dude/pull/101", title: "First run", repositoryName: "acme/dude",
    state: "open" as const, review: "approved" as const, checks, display, ...over,
  });

  test("the chip says CI unavailable, in its word, tooltip and accessible name", () => {
    const h = html(<PrChip pr={pr([codeRabbit, denied], "ci_running")} />);
    expect(text(h)).toBe("CI unavailable#101");
    expect(h).toContain('data-pr-state="ci_running"');
    const icon = html(<PrChip pr={pr([codeRabbit, denied], "ci_running")} iconOnly />);
    expect(icon).toContain('aria-label="CI unavailable, pull request acme/dude#101 (opens on GitHub)"');
    expect(prFacts(pr([codeRabbit, denied], "ci_running"))).toEqual(["Checks: 1 readable passing", prCheckDiagnosticReason(CHECK_RUNS_FORBIDDEN), "Approved"]);
  });

  test("a real failure, or the pull request being over, still wins the chip", () => {
    expect(text(html(<PrChip pr={pr([lint, denied], "ci_red")} />))).toBe("CI failing#101");
    expect(prFacts(pr([lint, denied], "ci_red"))).toEqual(["Checks: lint failing", prCheckDiagnosticReason(CHECK_RUNS_FORBIDDEN), "Approved"]);
    expect(text(html(<PrChip pr={pr([denied], "merged", { state: "merged" })} />))).toBe("Merged#101");
  });

  // What a reader sees, entities decoded.
  const seen = (el: React.ReactElement) => text(html(el)).replaceAll("&#x27;", "'");

  test("the panel warns without a hover and counts only real checks", () => {
    const h = seen(<PullRequestPanel pr={pr([codeRabbit, denied], "ci_running")} />);
    expect(h).toContain(prCheckDiagnosticReason(CHECK_RUNS_FORBIDDEN));
    expect(h).toContain("1 readable check passing");
    // One job row, CodeRabbit's: the diagnostic is no row of its own.
    const jobs = html(<PullRequestPanel pr={pr([codeRabbit, denied], "ci_running")} />).match(/data-fact="checks"[^]*?<ul[^>]*>([^]*?)<\/ul>/)?.[1] ?? "";
    expect((jobs.match(/<li[^>]*>[^]*?<\/li>/g) ?? []).map(text)).toEqual(["CodeRabbit"]);
    // Its checks line is not drawn green.
    const checksLine = html(<PullRequestPanel pr={pr([codeRabbit, denied], "ci_running")} />).match(/data-fact="checks"[^]*?data-icon="([^"]+)"/)?.[1];
    expect(checksLine).toBe("circle-dotted");
    expect(h).not.toMatch(/All \d+ checks passing/);
    const none = seen(<PullRequestPanel pr={pr([denied], "ci_running")} />);
    expect(none).toContain("No checks could be read");
    expect(none).toContain("Checks: Read");
    const failing = seen(<PullRequestPanel pr={pr([lint, denied], "ci_red")} />);
    expect(failing).toContain("1 of 1 checks failing");
    expect(failing).toContain("Checks: Read");
  });

  test("without a diagnostic the panel is as before", () => {
    const h = seen(<PullRequestPanel pr={pr([codeRabbit], "ready")} />);
    expect(h).toContain("All 1 checks passing");
    expect(h).not.toContain("Checks: Read");
  });
});

describe("PullRequestPanel", () => {
  const pr = {
    number: 41, url: "https://github.com/acme/dashboard/pull/41", title: "Chart library", repositoryName: "acme/dashboard",
    state: "open", review: "changes_requested", baseBranch: "main", mergeable: "behind", behindBy: 3, unresolvedThreads: 2,
    checks: [{ name: "e2e", status: "completed", conclusion: "failure" }],
    reviews: [{ login: "cy", state: "CHANGES_REQUESTED" }, { login: "bo", state: "REQUESTED" }],
  } as const;

  test("each fact's action sits on its line, a kind's once, after its last", () => {
    const h = html(<PullRequestPanel pr={pr} factActions={{ checks: <button>Re-run failed</button>, reviews: <button>Request review</button>,
      base: <button>Update branch</button>, threads: <a href="#">show</a> }} />);
    for (const kind of ["checks", "reviews", "base", "threads"]) expect(h).toContain(`data-fact="${kind}"`);
    expect((h.match(/Request review/g) ?? []).length).toBe(1);
    expect(h.indexOf("Request review")).toBeGreaterThan(h.indexOf("bo"));
    expect(text(h)).toContain("bo · review requested");
    expect(text(h)).toContain("3 commits behind main · no conflictsUpdate branch");
  });
});

describe("Cost", () => {
  test("a total with its split; unknown machine time is tokens only", () => {
    const h = html(<Cost tokensUsd={0.62} machineUsd={0.25} />);
    expect(text(h)).toBe("$0.87");
    expect(h).toContain('data-split="both"');
    expect(h).toContain('width:71%');
    const t = html(<Cost tokensUsd={0.62} />);
    expect(text(t)).toBe("$0.62");
    expect(t).toContain('data-split="tokens"');
  });
  test("nothing reported is a dash, never $0.00", () => {
    const h = html(<Cost tokensUsd={null} />);
    expect(text(h)).toBe("—");
    expect(h).toContain('title="Cost not reported"');
    // Even when told the parts would have come from lux.
    expect(text(html(<Cost tokensUsd={null} tokensFrom="lux" machineFrom="lux" settled />))).toBe("—");
  });
  // The tooltip's lines and the label come from costWords; the label is
  // what renders without a pointer.
  const label = (h: string) => /aria-label="([^"]*)"/.exec(h)?.[1] ?? "";
  test("without an origin it says nothing of one, as before", () => {
    expect(label(html(<Cost tokensUsd={0.62} machineUsd={0.25} />))).toBe("$0.87: model tokens $0.62, machine time $0.25");
  });
  test("a figure lux has settled is reported by lux; one it has not is still an estimate", () => {
    expect(label(html(<Cost tokensUsd={1.81} machineUsd={0.0077} tokensFrom="lux" machineFrom="lux" settled />)))
      .toBe("$1.82: model tokens $1.81 · reported by lux, machine time $0.0077 · lux");
    expect(label(html(<Cost tokensUsd={1.2} machineUsd={0.004} tokensFrom="lux" machineFrom="lux" />)))
      .toBe("$1.20: model tokens $1.20 · estimate, lux settling, machine time $0.0040 · lux, settling");
  });
  test("the harness's figure and dude's machine rate are estimates, settled or not", () => {
    expect(label(html(<Cost tokensUsd={0.3} machineUsd={0.2} tokensFrom="agent" machineFrom="estimate" settled />)))
      .toBe("$0.50: model tokens $0.30 · estimate, machine time $0.20 · estimated");
  });
  test("the tooltip's lines say what the label says, with the units", () => {
    expect(costWords({ tokensUsd: 1.81, machineUsd: 0.0077, tokens: 412_000, machineMs: 23 * 60_000,
      tokensFrom: "lux", machineFrom: "lux", settled: true }).lines).toEqual([
      "$1.82 total",
      "Model tokens $1.81 (412k tokens) · reported by lux",
      "Machine time $0.0077 (23m 00s) · lux",
    ]);
    expect(costWords({ tokensUsd: 0.3, tokensFrom: "agent" }).lines)
      .toEqual(["$0.30 total", "Model tokens $0.30 · estimate", "Machine time not counted yet"]);
    const w = costWords({ tokensUsd: null, machineUsd: 0.2, tokensFrom: "lux", machineFrom: "estimate" });
    expect(w.lines.slice(1)).toEqual(["Model tokens not reported", "Machine time $0.20 · estimated"]);
    expect(w.label).toBe("$0.20: model tokens not reported, machine time $0.20 · estimated");
  });
});

describe("AgentPlan", () => {
  const items = [
    { content: "Find every import", status: "completed" },
    { content: "Keep the wrapper", status: "in_progress" },
    { content: "Run the tests", status: "pending" },
  ] as const;

  test("folded it is one line: n of m, the meter, the step it is on", () => {
    const h = html(<AgentPlan items={items} defaultCollapsed />);
    expect(h).toContain('aria-expanded="false"');
    expect(text(h)).toContain("1 of 3");
    expect(text(h)).toContain("Keep the wrapper");
    expect(h).toContain('aria-label="1 of 3 steps done"');
    expect(h).not.toContain("<ol");
  });
  test("open it is the whole list, the current step marked", () => {
    const h = html(<AgentPlan items={items} />);
    expect(h).toContain("<ol");
    expect(h).toContain('aria-current="step"');
  });
  test("the meter draws a cell per step", () => {
    const h = html(<PlanMeter done={2} total={5} />);
    expect((h.match(/<i /g) ?? []).length).toBe(5);
    expect(h).toContain('data-s="current"');
  });
});

describe("prompt editing", () => {
  test("bold and code wrap the selection and keep it selected", () => {
    expect(applyFormat("say hi now", 4, 6, "bold")).toEqual({ text: "say **hi** now", start: 6, end: 8 });
    expect(applyFormat("run bun", 4, 7, "code")).toEqual({ text: "run `bun`", start: 5, end: 8 });
  });
  test("heading toggles on every selected line", () => {
    const on = applyFormat("one\ntwo\nthree", 0, 7, "heading");
    expect(on.text).toBe("## one\n## two\nthree");
    expect(applyFormat(on.text, on.start, on.end, "heading").text).toBe("one\ntwo\nthree");
  });
  test("a variable goes in at the caret", () => {
    expect(applyFormat("Goal: ", 6, 6, { insert: "{{task.goal}}" })).toEqual({ text: "Goal: {{task.goal}}", start: 19, end: 19 });
  });
  test("a prompt reads the variables dude fills as chips, and nothing else", () => {
    const h = html(<Markdown source={"Goal: {{task.goal}} on `{{run.branch}}`, `bun test`, {{task.goals}}"} variant="prompt" />);
    expect(h.match(/title="Filled in for each run: [\w.]+"/g)).toEqual([
      'title="Filled in for each run: task.goal"',
      'title="Filled in for each run: run.branch"',
    ]);
    // A backticked variable is still code; a typo stays as the agent reads it.
    expect(h).toMatch(/<code[^>]*><span[^>]*title="Filled in for each run: run.branch"/);
    expect(h).toContain(">bun test</code>");
    expect(text(h)).toContain("{{task.goals}}");
  });
});

describe("MarkdownDocument", () => {
  test("reads by default, with an Edit button when it can be saved", () => {
    const h = html(<MarkdownDocument source={"# Title\n\nBody"} onSave={() => {}} />);
    expect(h).toContain("<h1");
    expect(text(h)).toContain("Edit");
    expect(h).not.toContain("<textarea");
  });
  test("read-only without onSave", () => {
    expect(text(html(<MarkdownDocument source="Body" />))).not.toContain("Edit");
  });
  test("empty says so", () => {
    expect(text(html(<MarkdownDocument source="  " emptyText="No prompt yet." />))).toContain("No prompt yet.");
  });
  test("highlighting marks syntax and variables, and keeps every character", () => {
    const src = "## How\n- use `bun test` for {{task.goal}}\n```\ncode\n```\n> quoted **bold**";
    const lines = highlightMarkdown(src);
    expect(lines.map((spans) => spans.map(([, t]) => t).join(""))).toEqual(src.split("\n"));
    expect(lines[0]).toEqual([["heading-mark", "## "], ["heading", "How"]]);
    expect(lines[1]!.map(([k]) => k)).toEqual(["list-mark", "text", "code", "text", "var"]);
    expect(lines[3]).toEqual([["code", "code"]]);
    expect(lines[5]).toEqual([["quote", "> quoted **bold**"]]);
  });
});

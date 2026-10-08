/**
 * The agent network's pieces: hosts listed read-only (an organisation's, on
 * a project's page; what is always reachable), toolchain presets that say
 * what a list already has, the names agents were refused with Allow, and
 * the note under a tool call lux refused a name for — server-rendered, so
 * a state that gained a treatment or lost one fails here.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { HostChips, HostPresets, NetworkRefusedNote, RefusedHosts } from "../src/components/index.ts";
import { ToolCallCard } from "../src/components/ToolCallCard.tsx";

const html = (el: React.ReactElement) => renderToStaticMarkup(el);
const text = (h: string) => h.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();
const buttons = (h: string) => [...h.matchAll(/<button\b[^>]*>([\s\S]*?)<\/button>/g)].map((m) => m[1]!.replace(/<[^>]+>/g, "").trim());

describe("HostChips, read-only", () => {
  test("lists the hosts with no remove and no field, muted when asked", () => {
    const h = html(<HostChips readOnly hosts={["github.com", "*.github.com"]} />);
    expect(text(h)).toBe("github.com *.github.com");
    expect(h).not.toContain("<button");
    expect(h).not.toContain("<input");
    expect(h).toContain('data-readonly="true"');
    expect(html(<HostChips readOnly muted hosts={["llm.example"]} />)).toContain('data-muted="true"');
  });

  test("editable, each host has its remove and there is a field", () => {
    const h = html(<HostChips hosts={["pypi.org"]} onChange={() => {}} />);
    expect(buttons(h)).toEqual([""]);
    expect(h).toContain('aria-label="Remove pypi.org"');
    expect(h).toContain("<input");
  });
});

const PRESETS = [
  { name: "GitHub", hosts: ["github.com", "*.github.com"] },
  { name: "PyPI", hosts: ["pypi.org", "files.pythonhosted.org"] },
];

describe("HostPresets", () => {
  test("a preset the list has whole is ticked and cannot be added again; the rest add", () => {
    const h = html(<HostPresets presets={PRESETS} has={(host) => host.endsWith("github.com")} onAdd={() => {}} />);
    expect(text(h)).toBe("Add GitHub PyPI");
    expect(h).toMatch(/data-preset="GitHub"[^>]*data-has="true"[^>]*disabled=""/);
    expect(h).toMatch(/data-preset="PyPI"(?![^>]*data-has)[^>]*>/);
    expect(h).not.toMatch(/data-preset="PyPI"[^>]*disabled/);
  });

  test("disabled, nothing adds", () => {
    const h = html(<HostPresets presets={PRESETS} has={() => false} onAdd={() => {}} disabled />);
    expect(h).toMatch(/data-preset="PyPI"[^>]*disabled=""/);
  });
});

describe("RefusedHosts", () => {
  const refused = [
    { name: "files.pythonhosted.org", runs: 12, roles: ["fixer", "implementer"] },
    { name: "registry.npmjs.org", runs: 2, roles: ["reviewer"] },
  ];

  test("a row per host with how many Runs and who, Allow on each and Allow all", () => {
    const h = html(<RefusedHosts refused={refused} onAllow={() => {}} target="jervasion" />);
    expect(text(h)).toContain("Host Runs By");
    expect(text(h)).toContain("files.pythonhosted.org 12 fixer, implementer");
    expect(buttons(h)).toEqual(["Allow", "Allow", "Allow all 2"]);
    expect(text(h)).toContain("Adds them to jervasion’s list. The next Run on this project gets them.");
  });

  test("read-only, nothing can be allowed", () => {
    const h = html(<RefusedHosts refused={refused} target="jervasion" />);
    expect(buttons(h)).toEqual([]);
    expect(text(h)).toContain("registry.npmjs.org 2 reviewer");
  });
});

describe("NetworkRefusedNote", () => {
  test("names the host and the lists it is on neither of, with Allow and Settings", () => {
    const h = html(<NetworkRefusedNote host="files.pythonhosted.org" project="jervasion" organization="Acme" onAllow={() => {}} onSettings={() => {}} />);
    expect(text(h)).toContain("Network refused: files.pythonhosted.org is not on jervasion’s list, nor Acme’s.");
    expect(buttons(h)).toEqual(["Allow for jervasion", "Settings"]);
  });

  test("a project on its own list is not on its organisation's at all: says the project's alone", () => {
    expect(text(html(<NetworkRefusedNote host="x.example.com" project="jervasion" organization={null} />)))
      .toContain("Network refused: x.example.com is not on jervasion’s list.");
  });

  test("once allowed, Allow is the next Run's", () => {
    const h = html(<NetworkRefusedNote host="pypi.org" project="jervasion" organization="Acme" allowed onAllow={() => {}} onSettings={() => {}} />);
    expect(buttons(h)).toEqual(["Settings"]);
    expect(text(h)).toContain("Allowed · next Run gets it");
  });

  test("sits under a tool call's output as its note, shown with the call folded too", () => {
    const h = html(<ToolCallCard name="bash" status="completed" exitCode={1} output="cause: dns error" defaultExpanded={false}
      note={<NetworkRefusedNote host="pypi.org" project="jervasion" organization="Acme" />} />);
    expect(h).toContain('data-testid="network-refused"');
    expect(h).not.toContain("cause: dns error");
  });
});

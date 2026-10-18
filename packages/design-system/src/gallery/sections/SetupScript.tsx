import { useMemo, useState } from "react";
import { Col, Label } from "../Frame.tsx";
import { CodeEditor, type CodeDiagnostic } from "../../components/CodeEditor.tsx";
import { SetupScriptMark, SizeMeter, textLines } from "../../components/SetupScript.tsx";
import { RowMenu } from "../../primitives/RowMenu.tsx";
import { Table, TBody, Td, Th, THead, Tr } from "../../primitives/Table.tsx";
import { Callout } from "../../primitives/Layout.tsx";
import { Icon } from "../../icons/index.tsx";

/*
 * A repository's setup script in the pieces that show it, with made-up
 * repositories. The bypass lint is the app's; here a fixed pattern marks it.
 */

const LIMIT = 16_384;

const SCRIPT = `#!/usr/bin/env bash
set -euo pipefail

# The repository's own pre-commit hook. dude's co-author hook runs first.
ln -sf ../../scripts/pre-commit "$(git rev-parse --git-path hooks)/pre-commit"

# The client's dependencies, from the image's npm cache.
cd client
npm ci --prefer-offline
`;

const BYPASS = `#!/usr/bin/env bash
set -euo pipefail

git config core.hooksPath scripts/hooks
cd client
npm ci --prefer-offline
`;

const SETUP_PLACEHOLDER = `# Runs once in each new checkout of this repository, before the agent starts.
# For example, install the repository's own git hooks:
#   ln -sf ../../scripts/pre-commit "$(git rev-parse --git-path hooks)/pre-commit"
npm ci --prefer-offline`;

const bytes = (s: string) => new TextEncoder().encode(s).length;

/** One script in its frame: the header, the text, the footer's lines, warnings and meter. */
function ScriptFrame({ initial, readOnly, maxLines = 14 }: { readonly initial: string; readonly readOnly?: boolean; readonly maxLines?: number }) {
  const [text, setText] = useState(initial);
  const diagnostics = useMemo<CodeDiagnostic[]>(() => text.split("\n").flatMap((l, i) => {
    const at = l.indexOf("core.hooksPath");
    return at < 0 ? [] : [{ severity: "warning" as const, line: i + 1, from: at, to: at + "core.hooksPath".length,
      message: "Moves git’s hooks away from dude’s own commit hook. Install the hook into $(git rev-parse --git-path hooks) instead." }];
  }), [text]);
  const size = bytes(text);
  return (
    <Col>
      <CodeEditor
        aria-label="Setup script for api"
        language="shell"
        value={text}
        onChange={setText}
        readOnly={readOnly}
        placeholder={SETUP_PLACEHOLDER}
        diagnostics={diagnostics}
        minLines={6}
        maxLines={maxLines}
        header={<><span className="ds-mono">bash</span><span>· runs once per new checkout</span>{readOnly ? <span>· read-only</span> : null}</>}
        footer={
          <>
            <span>{textLines(text)} {textLines(text) === 1 ? "line" : "lines"}</span>
            {diagnostics.length ? (
              <span style={{ display: "inline-flex", gap: 4, alignItems: "center", color: "var(--ds-tone-attention-fg)" }}>
                <Icon name="warning" size={12} />{diagnostics.length} line bypasses hooks
              </span>
            ) : null}
            <span style={{ flex: 1 }} />
            <SizeMeter bytes={size} limit={LIMIT} />
          </>
        }
      />
      {size > LIMIT ? <span style={{ color: "var(--ds-tone-danger-fg)", fontSize: "var(--ds-text-xs)" }}>Over the 16 KB limit: keep long files in the repository and have the script use them.</span> : null}
    </Col>
  );
}

export function SetupScriptDemo() {
  const long = SCRIPT + "\n" + Array.from({ length: 360 }, (_, i) => `echo "line ${i} of a script that has grown far past what belongs here"`).join("\n");
  return (
    <Col>
      <Label>Repositories: api has a script, web none</Label>
      <Table density="compact" aria-label="Repositories">
        <THead>
          <Tr><Th width="22%">Name</Th><Th>URL</Th><Th width="12%">Branch</Th><Th align="right" width="48px"><span className="ds-sr-only">Actions</span></Th></Tr>
        </THead>
        <TBody>
          <Tr>
            <Td mono>api<SetupScriptMark repository="api" lines={textLines(SCRIPT)} action="edit" /></Td>
            <Td mono muted>https://github.com/acme/api.git</Td>
            <Td mono>main</Td>
            <Td align="right"><RowMenu label="Actions for api" items={[
              { id: "edit", label: "Edit", icon: "edit", onSelect: () => {} },
              { id: "setup", label: "Setup script…", icon: "terminal", onSelect: () => {} },
              { kind: "separator" },
              { id: "remove", label: "Remove", tone: "danger", onSelect: () => {} },
            ]} /></Td>
          </Tr>
          <Tr>
            <Td mono>web</Td>
            <Td mono muted>https://github.com/acme/web.git</Td>
            <Td mono>main</Td>
            <Td align="right" />
          </Tr>
        </TBody>
      </Table>
      <Label>A reader’s mark opens it read-only</Label>
      <span><span className="ds-mono">api</span><SetupScriptMark repository="api" lines={9} action="view" /></span>
      <Label>Populated</Label>
      <ScriptFrame initial={SCRIPT} />
      <Label>Empty: the placeholder is guidance, never a value</Label>
      <ScriptFrame initial="" />
      <Label>Bypasses hooks: a warning, Save still allowed</Label>
      <ScriptFrame initial={BYPASS} />
      <Label>Read-only</Label>
      <ScriptFrame initial={SCRIPT} readOnly />
      <Label>Too long: the text scrolls inside the cap; Save is the caller’s to disable</Label>
      <ScriptFrame initial={long} maxLines={10} />
      <Label>Meter: neutral, near the limit, over it</Label>
      <span style={{ display: "inline-flex", gap: 16, fontSize: "var(--ds-text-xs)" }}>
        <SizeMeter bytes={284} limit={LIMIT} />
        <SizeMeter bytes={15_200} limit={LIMIT} />
        <SizeMeter bytes={17_050} limit={LIMIT} />
      </span>
      <Label>The server refused it</Label>
      <Callout tone="danger">request body failed validation: setupScript: a setup script is text of at most 16384 bytes, with no NUL characters</Callout>
    </Col>
  );
}

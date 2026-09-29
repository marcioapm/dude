import { useEffect, useMemo, useState } from "react";
import { Block, Col, Label, Panes, Section, type PaneMode } from "../Frame.tsx";
import { AgentAvatar } from "../../components/AgentAvatar.tsx";
import { ChangedFiles, SessionFacts, SessionRail, SessionRailBlock, ToolUsage } from "../../components/SessionRail.tsx";
import { Segmented } from "../../components/ScreenHeader.tsx";
import { Icon } from "../../icons/index.tsx";
import { Cost } from "../../components/Cost.tsx";
import { LiveDiff, type LiveDiffFile } from "../../components/LiveDiff.tsx";
import { FileGallery, FileViewer, type GalleryFile } from "../../components/FileGallery.tsx";
import { ArtifactPreview } from "../../components/ArtifactPreview.tsx";

const line = (kind: " " | "+" | "-", old: number | null, n: number | null, text: string) => ({ kind, old, new: n, text });

const FILES: LiveDiffFile[] = [
  {
    path: "src/data/revenue.ts", status: "M", additions: 4, deletions: 2,
    hunks: [{ header: "@@ -1,6 +1,8 @@", lines: [
      line(" ", 1, 1, "import { useDashboardData } from '../dashboard';"),
      line("-", 2, null, "import { cache } from './cache';"),
      line("-", 3, null, "const loaded = new Map();"),
      line("+", null, 2, "/**"),
      line("+", null, 3, " * Kept for billing, which imports it: a thin wrapper over the"),
      line("+", null, 4, " * dashboard's loader, so there is one cache."),
      line("+", null, 5, " */"),
      line(" ", 4, 6, "export async function fetchRevenue(range: Range) {"),
    ] }],
  },
  {
    path: "src/charts/Revenue.tsx", status: "M", additions: 1, deletions: 1,
    hunks: [{ header: "@@ -2,4 +2,4 @@", lines: [
      line(" ", 2, 2, "import { scaleLinear } from '@visx/scale';"),
      line("-", 3, null, "import { drawAxis, drawArea } from '../svg/helpers';"),
      line("+", null, 3, "import { AreaClosed } from '@visx/shape';"),
      line(" ", 4, 4, "import { formatUsd } from '../format';"),
    ] }],
  },
];

const NEXT: LiveDiffFile = {
  path: "src/data/revenue.test.ts", status: "A", additions: 3, deletions: 0,
  hunks: [{ header: "@@ -0,0 +1,3 @@", lines: [
    line("+", null, 1, "import { test, expect } from 'bun:test';"),
    line("+", null, 2, "import { fetchRevenue } from './revenue';"),
    line("+", null, 3, "test('fetchRevenue shares the dashboard cache', async () => {});"),
  ] }],
};

/** A session's Conversation / Changes switch, as the app draws it. */
function SessionSwitch({ value: start = "chat" }: { readonly value?: "chat" | "changes" | "events" }) {
  const [value, setValue] = useState(start);
  return (
    <Segmented label="Show" value={value} onChange={setValue}
      options={[
        { value: "chat", label: <><Icon name="message" size={13} />Conversation</> },
        { value: "changes", label: <><Icon name="git-branch" size={13} />Changes 3<span className="ds-live-dot" /></> },
        { value: "events", label: <><Icon name="list" size={13} />Events 128</> },
      ]} />
  );
}

/** Every few seconds the agent writes a little more. */
function LiveDemo() {
  const [files, setFiles] = useState(FILES);
  useEffect(() => {
    const t = setInterval(() => setFiles((f) => (f.length === FILES.length ? [...f, NEXT] : FILES)), 3000);
    return () => clearInterval(t);
  }, []);
  return (
    <div style={{ height: 420, display: "flex" }}>
      <LiveDiff files={files} base="0fff44b9a1" live onOpenFile={() => {}} leading={<SessionSwitch value="changes" />}
        lastChange={{ face: <AgentAvatar role="implementer" size="xs" live />, tool: "Write", path: "revenue.test.ts", when: "just now" }} />
    </div>
  );
}

const T = Date.now();
const GALLERY: GalleryFile[] = [
  { name: "revenue-chart.png", versions: [3, 2, 1].map((v) => ({ id: `png${v}`, name: "revenue-chart.png", contentType: "image/png", sizeBytes: 84_000, createdAt: T - (4 - v) * 3_600_000, role: "implementer" as const, session: "Implement", version: v })) },
  { name: "session.webm", versions: [{ id: "webm", name: "session.webm", contentType: "video/webm", sizeBytes: 4_800_000, createdAt: T - 2_400_000, role: "reviewer", session: "Review · browser", version: 1 }] },
  { name: "DECISION.md", versions: [2, 1].map((v) => ({ id: `md${v}`, name: "DECISION.md", contentType: "text/markdown", sizeBytes: 3_200, createdAt: T - 6_600_000, role: "implementer" as const, session: "Implement", version: v })) },
  { name: "coverage.html", versions: [{ id: "html", name: "coverage.html", contentType: "text/html", sizeBytes: 212_000, createdAt: T - 6_600_000, role: "implementer", session: "Implement", version: 1 }] },
];

function FilesDemo() {
  const [open, setOpen] = useState<string | null>(null);
  const [version, setVersion] = useState<string | undefined>(undefined);
  const shown = useMemo(() => GALLERY.find((f) => f.name === open), [open]);
  return (
    <>
      <FileGallery files={GALLERY} onOpen={(f) => { setVersion(undefined); setOpen(f.name); }} onDownload={() => {}} onDownloadAll={() => {}} note="kept 90 days" />
      <FileViewer files={GALLERY} open={open} onOpenChange={(n) => { setVersion(undefined); setOpen(n); }} version={version} onVersionChange={setVersion}
        onDownload={() => {}} onCopy={() => {}} onOpenSession={() => {}}>
        {shown ? (
          <ArtifactPreview contentType={shown.versions[0]!.contentType} name={shown.name} text={"# Decision\n\nUse **visx**: it is small, typed, and composes."} />
        ) : null}
      </FileViewer>
    </>
  );
}

export function LiveSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="live" title="Live work" intro="What an agent is doing to the code as it does it, the files it leaves, and what it cost.">
      <Block id="l-diff" title="LiveDiff" note="The agent's checkout against the commit it started from, as it changes. Files on the left with status and counts; each file's diff under a header that sticks. New lines flash and keep a mark down their side for a moment; the file they are in lights up. Follow the agent scrolls to the newest change; picking a file shows it alone and turns Follow off. Unified or Split; each file opens in the viewer. The last change carries the agent's face.">
        <Panes mode={mode}>
          <LiveDemo />
        </Panes>
        <div style={{ height: 8 }} />
        <Panes mode={mode}>
          <div style={{ height: 160, display: "flex" }}>
            <LiveDiff files={[]} base="0fff44b" emptyMessage="The agent has not changed anything yet." />
          </div>
        </Panes>
      </Block>
      <Block id="l-session" title="A session's bar and its rail" note="Under a session's header, one bar: Conversation / Changes / Events as a small Segmented (Changes with its count and, while the agent changes files, the breathing dot; Events last, with its count), each view in the same place below it, and — on Changes — the diff's own controls after the switch (the LiveDiff demo above shows that row). Beside the conversation, the rail on the chrome shade: what the header does not say, the tools used, the files changed so far (each opens Changes on it alone).">
        <Panes mode={mode}>
          <div style={{ display: "grid", gridTemplateColumns: "minmax(0, 1fr) 260px", height: 360 }}>
            <div>
              <SessionSwitch />
            </div>
            <SessionRail aria-label="This session">
              <SessionRailBlock label="Session">
                <SessionFacts facts={[{ label: "Model", value: "claude-sonnet-5", mono: true }, { label: "Agent", value: "opencode" }, { label: "Attempt", value: 1 }]} />
              </SessionRailBlock>
              <SessionRailBlock label="Tools used">
                <ToolUsage tools={[{ name: "Read", count: 9 }, { name: "Edit", count: 5 }, { name: "Bash", count: 4 }, { name: "Grep", count: 3 }, { name: "Write", count: 1 }]} />
              </SessionRailBlock>
              <SessionRailBlock label="Files changed" live>
                <ChangedFiles files={FILES.map((f) => ({ path: f.path, additions: f.additions, deletions: f.deletions }))} onOpen={() => {}} />
              </SessionRailBlock>
            </SessionRail>
          </div>
        </Panes>
      </Block>
      <Block id="l-files" title="FileGallery / FileViewer" note="Everything a task's sessions saved: images and video as a gallery, documents as rows, each opening in the viewer with its versions down the side, prev/next on ← →, copy for text, and download — or all of them as a zip. HTML opens only in a sandboxed frame.">
        <Panes mode={mode}>
          <Col>
            <FilesDemo />
          </Col>
        </Panes>
      </Block>
      <Block id="l-cost" title="Cost, both halves" note="A cost is a total; the hairline is the split, tokens then machine time, and the tooltip gives both.">
        <Panes mode={mode}>
          <Col>
            <Label>Tokens and machine time</Label>
            <Cost tokensUsd={0.62} machineUsd={0.25} tokens={412_000} machineMs={23 * 60_000} />
            <Label>Large</Label>
            <Cost tokensUsd={9.8} machineUsd={4.4} size="lg" />
          </Col>
        </Panes>
      </Block>
    </Section>
  );
}

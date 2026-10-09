import { useId, useMemo, useState } from "react";
import { Block, Col, Label, Panes, Section, type PaneMode } from "../Frame.tsx";
import { CodeEditor, type CodeCompletion, type CodeDiagnostic } from "../../components/CodeEditor.tsx";
import { CanRunContainersBadge, ImagePicker, ImageMark, type ImageChoiceView } from "../../components/ImagePicker.tsx";
import { BuildQueueStrip, BuildStages, ImageHistory, ImageState, type ImageHistoryVersion } from "../../components/Images.tsx";
import { LogStream } from "../../components/LogStream.tsx";
import { MachineChip, MachineTip } from "../../components/Machines.tsx";
import { Badge } from "../../primitives/Badge.tsx";
import { Checkbox } from "../../primitives/Checkbox.tsx";
import { Callout } from "../../primitives/Layout.tsx";
import { Icon } from "../../icons/index.tsx";

/*
 * The image library's pieces with made-up images; the app's Containerfile
 * lint and completions live in @dude/domain, so the editor here marks a
 * fixed problem and completes from a fixed list.
 */

const IMAGES: ImageChoiceView[] = [
  { id: "base", name: "acme-base", description: "Debian, Node 26, Java 25, Python 3.14, Playwright", version: 7, isDefault: true },
  { id: "podman", name: "agents-podman", description: "Debian, git and rootless podman", version: 3, canRunContainers: true },
  { id: "pnpm", name: "node-pnpm", description: "pnpm and turbo, for the dashboard", version: 4, status: { kind: "building", version: 5 } },
  { id: "uv", name: "python-uv", description: "uv and the Postgres client", version: 2, status: { kind: "waiting", version: 3 } },
  { id: "rails", name: "rails-legacy", description: "Ruby 3.1 and Node 18, for the old billing app", version: 3, status: { kind: "failed", version: 4 } },
  { id: "old", name: "old-runner", description: "Before the library", version: 1, archived: true },
];

const SOURCE = `# pnpm and turbo for the dashboard's agents and previews.
FROM image:acme-base
ARG PNPM_VERSION=9.15.0
RUN npm install -g pnpm@\${PNPM_VERSION} turbo@2 \\
 && pnpm config set store-dir /var/cache/pnpm --global
ENV PNPM_HOME=/usr/local/share/pnpm CI=1
WORKDIR /workspace
COPY package.json /workspace/
`;

const COMPLETIONS: CodeCompletion[] = [
  { label: "image:acme-base", detail: "Acme's default base · v7", type: "image", boost: 2 },
  { label: "image:node-pnpm", detail: "Acme · v4", type: "image" },
  { label: "image:python-uv", detail: "Acme · v2", type: "image" },
  { label: "debian:bookworm-slim", detail: "registry", type: "image" },
  { label: "FROM", detail: "the image this one is built on", type: "keyword" },
  { label: "RUN", detail: "run a command while building", type: "keyword" },
];

function EditorDemo() {
  const [text, setText] = useState(SOURCE);
  const diagnostics = useMemo<CodeDiagnostic[]>(() => {
    const lines = text.split("\n");
    const at = lines.findIndex((l) => /^\s*COPY\s+(?!--from)/i.test(l));
    return at < 0 ? [] : [{ severity: "error", line: at + 1, from: 0, to: lines[at]!.length, message: "An image has no build files: COPY only --from a stage or another image" }];
  }, [text]);
  return (
    <CodeEditor
      aria-label="Containerfile"
      language="dockerfile"
      value={text}
      onChange={setText}
      diagnostics={diagnostics}
      complete={({ word }) => COMPLETIONS.filter((c) => c.label.toLowerCase().startsWith(word.toLowerCase()))}
      minLines={10}
      header={
        <>
          <span className="ds-mono">Containerfile</span>
          <Badge size="sm" tone="attention">draft v5</Badge>
          <span>from v4</span>
          <span style={{ flex: 1 }} />
          <span>⌘F search · ⌘Z undo · Ctrl-Space complete</span>
        </>
      }
      after={
        <>
          <b style={{ fontFamily: "var(--ds-font-sans)", fontSize: "var(--ds-text-2xs)", letterSpacing: "0.06em" }}>DUDE LAYER</b>
          <span style={{ fontFamily: "var(--ds-font-sans)", fontSize: "var(--ds-text-xs)" }}> · added last, from the running release · read-only</span>
          {"\nCOPY --from=dude-layer@sha256:51b9… /rootfs/ /   # dude CLI, OpenCode, its config\nUSER agent"}
        </>
      }
      footer={
        <>
          <span>{text.split("\n").length} lines</span>
          <span style={{ flex: 1 }} />
          {diagnostics.length ? <span style={{ color: "var(--ds-tone-danger-fg)" }}>✕ {diagnostics.length} won't build</span> : <span>Builds</span>}
        </>
      }
    />
  );
}

function PickerDemo() {
  const [value, setValue] = useState<string | null>("pnpm");
  const [none, setNone] = useState<string | null>(null);
  return (
    <Col>
      <Label>A project's runtime image</Label>
      <ImagePicker images={IMAGES} value={value} onChange={setValue} label="Runtime image" heading="Acme's images"
        onCreateFrom={() => {}} manage={{ label: "Manage images", onClick: () => {} }} />
      <Label>None chosen: what that means here</Label>
      <ImagePicker images={IMAGES} value={none} onChange={setNone} label="Tester's image" allowNone="Use Acme's"
        noneLabel="From Acme · playwright" heading="Acme's images" />
      <Label>Archived and still chosen</Label>
      <ImagePicker images={IMAGES} value="old" onChange={() => {}} label="Preview image" />
    </Col>
  );
}

const HISTORY: ImageHistoryVersion[] = [
  { id: "v5", number: 5, state: "building", containerfile: SOURCE.replace("COPY package.json /workspace/\n", ""), note: "Node 26 from the base; pnpm 9.15 via a build argument", author: { id: "mm", name: "Márcio Martins" }, when: "3 min ago" },
  { id: "v4", number: 4, state: "published", containerfile: "# pnpm and turbo for the dashboard's agents and previews.\nFROM image:acme-base\nRUN npm install -g pnpm@9 turbo@2 \\\n && pnpm config set store-dir /var/cache/pnpm --global\nENV PNPM_HOME=/usr/local/share/pnpm \\\n    CI=1\nWORKDIR /workspace\n", note: "pnpm 9; PNPM_HOME for the global bin", author: { id: "ep", name: "Eli Park" }, when: "yesterday, 16:44", builtOn: "acme-base v6" },
  { id: "v3", number: 3, state: "failed", containerfile: "FROM image:acme-base\nRUN npm install -g pnpm turbo@3\n", note: "Try turbo 3", author: { id: "bo", name: "Bo Lindqvist" }, when: "17 Sep", builtOn: "acme-base v5", error: "step 2 failed: npm ERR! notarget turbo@3" },
  { id: "v2", number: 2, state: "superseded", containerfile: "# pnpm and turbo for the dashboard's agents and previews.\nFROM image:acme-base\nRUN npm install -g pnpm turbo@2\nENV CI=1\nWORKDIR /workspace\n", note: "Add turbo; CI=1", author: { id: "bo", name: "Bo Lindqvist" }, when: "10 Sep", builtOn: "acme-base v4" },
  { id: "v1", number: 1, state: "superseded", containerfile: "FROM image:acme-base\nRUN npm install -g pnpm\n", note: "Rebuild on acme-base v3", author: null, when: "3 Sep", builtOn: "acme-base v3" },
];

/** A history in which v2 turned "Can run containers" on and v3 off again. */
const CONTAINERS_HISTORY: ImageHistoryVersion[] = [
  { id: "c3", number: 3, state: "published", containerfile: "FROM image:acme-base\nRUN apt-get install -y git\n", note: "No podman: the tests moved to CI", author: { id: "ep", name: "Eli Park" }, when: "today, 09:12", canRunContainers: false },
  { id: "c2", number: 2, state: "superseded", containerfile: "FROM image:acme-base\nRUN apt-get install -y git podman fuse-overlayfs uidmap\n", note: "Run podman for the tests", author: { id: "mm", name: "Márcio Martins" }, when: "yesterday, 16:44", canRunContainers: true },
  { id: "c1", number: 1, state: "superseded", containerfile: "FROM image:acme-base\nRUN apt-get install -y git\n", note: "First", author: { id: "mm", name: "Márcio Martins" }, when: "3 Sep", canRunContainers: false },
];

/** The image page's field: the box, its description, and the hint and warning beside it, each naming the box. */
function ContainersFieldDemo({ on, hint, warning }: { readonly on: boolean; readonly hint?: boolean; readonly warning?: boolean }) {
  const id = useId();
  const hintId = `${id}-hint`;
  const warningId = `${id}-warning`;
  const ids = [hint ? hintId : null, warning ? warningId : null].filter(Boolean).join(" ");
  const line = { display: "flex", gap: 6, margin: "0 0 0 22px", fontSize: "var(--ds-text-xs)", color: "var(--ds-color-text-secondary)" } as const;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
      <Checkbox checked={on} label="Can run containers" aria-describedby={ids || undefined}
        description="Runs in this image can start containers inside with podman or Docker. dude checks the build can, and fails it if not." />
      {hint ? (
        <p id={hintId} style={line}>
          <Icon name="warning" size={12} style={{ color: "var(--ds-tone-attention-fg)", marginTop: 2 }} />
          This Containerfile doesn't install podman or Docker. The build will fail its container check unless the base has them.
        </p>
      ) : null}
      {warning ? (
        <p id={warningId} style={line}>
          <Icon name="warning" size={12} style={{ color: "var(--ds-tone-attention-fg)", marginTop: 2 }} />
          New previews of this image can't run containers. Existing ones keep theirs until they start a fresh run.
        </p>
      ) : null}
    </div>
  );
}

const LOG = [
  "build of node-pnpm v5 · podman, rootless · 1.5 CPUs · 1.5 GB memory · linux/arm64",
  "resolve image:acme-base → v7 = 895757147740.dkr.ecr.eu-north-1.amazonaws.com/dude/custom@sha256:c41d…",
  "STEP 1/5: FROM 895757147740.dkr.ecr.eu-north-1.amazonaws.com/dude/custom@sha256:c41d…",
  "STEP 2/5: ARG PNPM_VERSION=9.15.0",
  "STEP 3/5: RUN npm install -g pnpm@9.15.0 turbo@2 && pnpm config set store-dir /var/cache/pnpm --global",
  "added 2 packages in 4s",
].map((text, seq) => ({ seq, text }));

export function ImagesGallerySection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="images" title="Image library" intro="An organisation's container images: a Containerfile editor that loads only where it is used, the picker every image field is, and a build's queue, stages and history.">
      <Block id="i-editor" title="CodeEditor" note="CodeMirror 6, lazy: its own chunk, loaded the first time an editor renders, a skeleton of the text's lines meanwhile. Tokens colour it, so light, dark and compact follow the page. Lint marks in place and in the gutter (hover for the reason); Ctrl-Space completes; ⌘F searches. The header, the read-only part under the text (here, the dude layer) and the footer are the app's.">
        <Panes mode={mode}>
          <EditorDemo />
        </Panes>
      </Block>
      <Block id="i-picker" title="ImagePicker" note="Every field that takes an image: a combobox over the organisation's images (name in mono, its description, the default base marked, a newer version building, waiting or failed as a badge, the published version on the right). It stores the id; there is no version to choose. ↑ ↓ Enter Esc. No match offers making an image FROM the words. An archived image is listed only while it is the one chosen.">
        <Panes mode={mode} style={{ minHeight: 420 }}>
          <PickerDemo />
        </Panes>
      </Block>
      <Block id="i-build" title="BuildQueueStrip / BuildStages / ImageState" note="The builder's queue in a line over the list: what builds, what waits, and the limits every build has. A build's stages as cells: done, current (info tint, breathing dot), to come, failed. An image's state in a row in its tone, with a dot, a breathing one while it builds.">
        <Panes mode={mode}>
          <Col>
            <BuildQueueStrip building={{ label: "node-pnpm v5", elapsed: "2m 10s" }} waiting={["python-uv v3", "playwright v8"]} onOpen={() => {}}
              limits={["Rootless", "1.5 CPU", "1.5 GB", "One at a time"]} />
            <BuildQueueStrip building={null} waiting={[]} limits={["Rootless", "1.5 CPU", "1.5 GB"]} />
            <BuildQueueStrip building={null} waiting={[]} unavailable="Builds are off: this dude has no dude layer configured (DUDE_LAYER_IMAGE)." />
            <Label>Building, then failed</Label>
            <BuildStages stages={[
              { id: "wait", label: "Waiting", detail: "in the queue", state: "done" },
              { id: "build", label: "Building", detail: "rootless · 1.5 CPU · 1.5 GB", state: "current" },
              { id: "publish", label: "Pushed and published", detail: "dude/custom in ECR", state: "todo" },
            ]} />
            <BuildStages stages={[
              { id: "wait", label: "Waiting", detail: "in the queue", state: "done" },
              { id: "build", label: "Building", detail: "ran out of memory (1.5 GB) at step 3", state: "failed" },
              { id: "publish", label: "Not published", detail: "v4 is still live", state: "todo" },
            ]} />
            <Label>States</Label>
            <ImageState kind="published">Published · 2h ago</ImageState>
            <ImageState kind="building">Building v5 · 2m</ImageState>
            <ImageState kind="waiting">Waiting · 2nd</ImageState>
            <ImageState kind="failed">v4 failed · v3 still live</ImageState>
            <ImageState kind="draft">Draft not built</ImageState>
            <span style={{ display: "inline-flex", gap: 8 }}><ImageMark isDefault /><ImageMark /></span>
            <LogStream lines={LOG} title="Build log" live maxHeight={200} />
          </Col>
        </Panes>
      </Block>
      <Block id="i-containers" title="Can run containers" note="A property of each image version, saved with its draft by a Checkbox under the editor. Badges (CanRunContainersBadge; lowercase in the picker) show only the published version's value; a Run's header shows the one it recorded at submit, after its image, as a focusable trigger with a tooltip, as the chips before it. The hint (box on, no podman or Docker named) and the off warning (box off where previews run it) are muted lines with the warning glyph, never a Callout, and describe the box. A version marked so gets a Check containers stage between Built and Published; a passed check stays done when the push after it fails. A Run lux cannot place yet says why in an attention Callout, with Waiting for a host beside its status.">
        <Panes mode={mode}>
          <Col>
            <Label>Badges: header, list, picker</Label>
            <span style={{ display: "inline-flex", gap: 8, alignItems: "center" }}>
              <CanRunContainersBadge size="md" /><CanRunContainersBadge /><CanRunContainersBadge lower />
            </span>
            <Label>A Run's header: after its image, with a tooltip</Label>
            <span data-testid="run-header-facts" style={{ display: "inline-flex", flexWrap: "wrap", gap: 8, alignItems: "center" }}>
              <MachineChip icon="cube" name="agents-podman" spec="v3"
                tooltip={<MachineTip name="agents-podman v3">Fixed when the session started.</MachineTip>} />
              <CanRunContainersBadge tooltip="This Run can start containers inside it. Set when the session started; resuming keeps it." />
            </span>
            <Label>On</Label>
            <ContainersFieldDemo on />
            <Label>On, nothing installs an engine</Label>
            <ContainersFieldDemo on hint />
            <Label>Off, where previews run it</Label>
            <ContainersFieldDemo on={false} warning />
            <Label>Checking, then failed, then passed with its push failing</Label>
            <BuildStages stages={[
              { id: "wait", label: "Waiting", detail: "in the queue", state: "done" },
              { id: "build", label: "Built", detail: "rootless · 1.5 CPU · 1.5 GB", state: "done" },
              { id: "check", label: "Check containers", detail: "podman or Docker, fuse-overlayfs, newuidmap/newgidmap, subuid", state: "current" },
              { id: "done", label: "Pushed and published", detail: "dude/custom, then live", state: "todo" },
            ]} />
            <BuildStages stages={[
              { id: "wait", label: "Waiting", detail: "in the queue", state: "done" },
              { id: "build", label: "Built", detail: "rootless · 1.5 CPU · 1.5 GB", state: "done" },
              { id: "check", label: "Check containers", detail: "Missing: podman or Docker, fuse-overlayfs, newuidmap, newgidmap", state: "failed" },
              { id: "done", label: "Not published", detail: "Not pushed · v4 is still live", state: "todo" },
            ]} />
            <BuildStages stages={[
              { id: "wait", label: "Waiting", detail: "in the queue", state: "done" },
              { id: "build", label: "Built", detail: "rootless · 1.5 CPU · 1.5 GB", state: "done" },
              { id: "check", label: "Check containers", detail: "podman 5.4, fuse-overlayfs, newuidmap/newgidmap with capabilities, subuid for agent", state: "done" },
              { id: "done", label: "Not published", detail: "pushing to the registry failed: 502 Bad Gateway", state: "failed" },
            ]} />
            <Label>Waiting for a host</Label>
            <Callout tone="attention">
              <b>Waiting for a host that can run containers.</b> lux has no host that can run containers. An admin can add one to a pool. Nothing is spent meanwhile.
            </Callout>
            <span style={{ display: "inline-flex", gap: 8 }}>
              <Badge size="sm" icon="clock">Waiting for a host</Badge>
            </span>
            <Label>History: turned on in v2, off in v3</Label>
            <ImageHistory versions={CONTAINERS_HISTORY} publishedId="c3" initialId="c2" />
          </Col>
        </Panes>
      </Block>
      <Block id="i-history" title="ImageHistory" note="Every version, newest first, failed ones too, with who saved it, why, and what it was built on. The selected one against the one before it or against the published one, in DiffView's file. A built version that is not published can be published again at once.">
        <Panes mode={mode}>
          <ImageHistory versions={HISTORY} publishedId="v4" onRepublish={() => {}} onOpenBuild={() => {}} initialId="v2" />
        </Panes>
      </Block>
    </Section>
  );
}

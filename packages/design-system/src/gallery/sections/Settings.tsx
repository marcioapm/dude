import { useState } from "react";
import { Block, Col, Label, Panes, Section, type PaneMode } from "../Frame.tsx";
import { MarkdownDocument } from "../../components/MarkdownDocument.tsx";
import { PromptHistory, type PromptHistoryVersion } from "../../components/PromptHistory.tsx";
import { AgentAvatar } from "../../components/AgentAvatar.tsx";
import { EpicCard, EpicRow, type EpicSummary } from "../../components/EpicCard.tsx";
import { Segmented } from "../../components/ScreenHeader.tsx";
import { SettingRow, SettingSource, SettingsHeader, SettingsLayout, SettingsNote, SettingsSection, Switch } from "../../components/Settings.tsx";
import { Input } from "../../primitives/Input.tsx";
import { Badge } from "../../primitives/Badge.tsx";
import { Button } from "../../primitives/Button.tsx";
import { FormActions, KeyValueList } from "../../primitives/Layout.tsx";
import { SearchResultList, SearchResultRow } from "../../components/SearchResultRow.tsx";
import { EntityLine } from "../../components/EntityLine.tsx";
import { AuthorLine } from "../../components/PersonAvatar.tsx";
import { RefLead } from "../../components/RefLead.tsx";
import { RemovableList } from "../../components/RemovableList.tsx";
import { SearchPicker } from "../../components/SearchPicker.tsx";
import { GitHubUserLine, type GitHubUser } from "../../components/GitHubUserLine.tsx";
import { PullRequestPanel } from "../../components/PullRequestPanel.tsx";
import { Icon } from "../../icons/index.tsx";
import { Select } from "../../primitives/Select.tsx";
import { Table, TBody, Td, Th, THead, Tr } from "../../primitives/Table.tsx";
import { NumberInput } from "../../primitives/NumberInput.tsx";
import { FitBar, MachineChip, MachineTip, ProportionBar, ReservedSwatch } from "../../components/Machines.tsx";
import { FlowSteps, NameChips, TierChip, TierLine, TierTip } from "../../components/Tiers.tsx";
import { HostChips } from "../../components/HostChips.tsx";
import { HostPresets, NetworkRefusedNote, RefusedHosts, type HostPreset } from "../../components/Network.tsx";
import { ToolCallCard } from "../../components/ToolCallCard.tsx";
import { egressAllows, egressProblem } from "@dude/domain";

const PROMPT = `# Implementer

You take one task, change the repository so it is done, and prove it with tests.

## How to work

1. **Read before you write.** Find where the behaviour lives today.
2. Make the smallest change that fully does the task.
3. Run \`bun test\` before you finish, for {{task.goal}}.

> The reviewers will see your summary and your diff.`;

const VERSIONS: PromptHistoryVersion[] = [
  { id: "v3", number: 3, body: PROMPT, note: "Ask before touching CI", author: { id: "eli", name: "Eli" }, when: "yesterday, 16:42", current: true, sessions: { count: 31, recent: [{ id: "r1", label: "DASH-3 · Implement" }] } },
  { id: "v2", number: 2, body: PROMPT.replace("3. Run", "3. Always run"), note: "Summary format", author: { id: "ana", name: "Ana" }, when: "Sep 24, 11:05", current: false, sessions: { count: 22, recent: [] } },
  { id: "v1", number: 1, body: "Implement this task.", note: "dude’s built-in prompt", author: null, when: "Sep 12, 10:02", current: false, sessions: { count: 4, recent: [] } },
];

const EPIC: EpicSummary = {
  id: "e1",
  title: "Charts v2",
  description: "Replace the hand-rolled SVG charts with one library.",
  lanes: { done: 2, review: 5, progress: 3, backlog: 1 },
  tasks: 11,
  prs: { merged: 1, open: 4 },
  people: [{ id: "ana", name: "Ana Ribeiro" }, { id: "bo", name: "Bo Lind" }],
  costUsd: 8.4,
  when: "updated just now",
  needsYou: 1,
};

function SettingsDemo() {
  const [page, setPage] = useState("implementer");
  const [effort, setEffort] = useState("medium");
  const [on, setOn] = useState(true);
  return (
    <SettingsLayout
      scope={{ title: "Dashboard", subtitle: "Project settings", leading: <AgentAvatar role="system" size="md" /> }}
      current={page}
      onSelect={setPage}
      items={[
        { id: "general", label: "General", icon: "settings" },
        {
          id: "agents",
          label: "Agents",
          icon: "edit",
          note: "2 changed",
          items: [
            { id: "implementer", label: "Implementer", leading: <AgentAvatar role="implementer" size="sm" />, note: "●" },
            { id: "reviewer", label: "Reviewer", leading: <AgentAvatar role="reviewer" size="sm" /> },
          ],
        },
        { id: "delivery", label: "Delivery", icon: "list" },
      ]}
      footer="Anything not changed here follows Acme’s settings."
    >
      <SettingsNote icon="layers">Values marked “From Acme” follow the organisation; change one to override it here.</SettingsNote>
      <SettingsHeader title="Delivery" description="How a task goes from Deliver to a pull request." />
      <SettingsSection title="Loop">
        <SettingRow label="Review rounds" help="Before the owner is asked." source={<SettingSource source="organization" from="Acme" />}>
          <Input aria-label="Review rounds" defaultValue="5" style={{ width: 80 }} />
        </SettingRow>
        <SettingRow label="Browser test" source={<SettingSource source="project" from="Acme" inherited="off" onReset={() => setOn(false)} />}>
          <Switch checked={on} onCheckedChange={setOn} label={on ? "On" : "Off"} />
        </SettingRow>
        <SettingRow label="Reasoning effort">
          <Select aria-label="Reasoning effort" value={effort} onValueChange={setEffort}
            options={["low", "medium", "high", "max"].map((v) => ({ value: v, label: v }))} />
        </SettingRow>
        <SettingRow label="Secrets" help="A block control: a table and its actions at the column's width." block>
          <Table density="compact" aria-label="Secrets">
            <THead><Tr><Th>Name</Th><Th>Value</Th></Tr></THead>
            <TBody>
              <Tr><Td mono>SEED_LLM_KEY</Td><Td mono muted>…3f9a</Td></Tr>
              <Tr><Td mono>STRIPE_TEST_KEY</Td><Td mono muted>…x7Qb</Td></Tr>
            </TBody>
          </Table>
          <FormActions><Button leadingIcon="plus">Add secret</Button></FormActions>
        </SettingRow>
      </SettingsSection>
    </SettingsLayout>
  );
}

const SIZES = [
  { value: "default", label: "Default", meta: "Standard · 4 CPUs · 8 GiB · 40 GiB", description: "Follows whichever size is the default" },
  { value: "small", label: "Small", meta: "1.5 CPUs · 3.5 GiB · 20 GiB" },
  { value: "standard", label: "Standard", meta: "4 CPUs · 8 GiB · 40 GiB" },
  { value: "large", label: "Large", meta: "8 CPUs · 16 GiB · 80 GiB" },
  { value: "xl", label: "XL", meta: "16 CPUs · 48 GiB · 200 GiB · big" },
];

function MachinesDemo() {
  const [cpus, setCpus] = useState<number | null>(6.5);
  const [memory, setMemory] = useState<number | null>(22.5);
  const [bad, setBad] = useState<number | null>(2.3);
  const [size, setSize] = useState("large");
  return (
    <Col>
      <Label>NumberInput: on step, and off it</Label>
      <div style={{ display: "grid", gridTemplateColumns: "repeat(3, minmax(0, 1fr))", gap: "var(--ds-space-16)", maxWidth: 560 }}>
        <NumberInput label="CPUs" unit="CPUs" value={cpus} onValueChange={setCpus} step={0.5} min={0.5} hint="In steps of 0.5" />
        <NumberInput label="Memory" unit="GiB" value={memory} onValueChange={setMemory} step={0.5} min={0.5} hint="In steps of 0.5 GiB" />
        <NumberInput label="CPUs" unit="CPUs" value={bad} onValueChange={setBad} step={0.5} min={0.5} error="Whole or half CPUs: 0.5, 1, 1.5…" />
      </div>
      <Label>Select, each option with its spec as meta, and a footer</Label>
      <div style={{ maxWidth: 260 }}>
        <Select aria-label="Machine" value={size} onValueChange={setSize} options={SIZES}
          footer={<>CPUs · memory · disk. <a href="#s-machines">Manage sizes in Machines</a></>} />
      </div>
      <Label>FitBar</Label>
      <FitBar share={0.11}>11% of a host</FitBar>
      <FitBar share={0.75}>75% of a host</FitBar>
      <FitBar share={null}>Unknown</FitBar>
      <Label>ProportionBar: one host&apos;s memory</Label>
      <ProportionBar aria-label="A 32 GiB host: 2 GiB kept by Linux and the host, two runs asking 16 each get 15"
        segments={[
          { id: "host", value: 2, kind: "reserved" },
          { id: "a", value: 15, label: "Run A · asks 16 · gets 15" },
          { id: "b", value: 15, label: "Run B · asks 16 · gets 15" },
        ]}
        legend={<><ReservedSwatch />Linux and the host · 2 GiB</>} total="c7a.4xlarge · 32 GiB" />
      <Label>MachineChip, with its tooltip</Label>
      <MachineChip name="XL" spec="16 CPUs · 48 GiB · 200 GiB"
        tooltip={<MachineTip name="XL">From Checkout’s settings for the Implementer. Fixed when the session started. It asked for 48 GiB and got 45.6.</MachineTip>} />
    </Col>
  );
}

function TiersDemo() {
  const [model, setModel] = useState("claude-opus-5-5");
  return (
    <Col>
      <Label>TierLine</Label>
      <TierLine icon="brain" tone="info" name="Thinker" description="Reads, plans, judges and tidies. Slow and thorough." />
      <TierLine icon="agent" tone="success" name="Coder" description="Writes and fixes code for hours at a time." />
      <TierLine icon="zap" tone="attention" name="Fast" description="Small, mechanical jobs where speed beats depth." />
      <Label>FlowSteps</Label>
      <FlowSteps steps={[
        { title: "An agent asks for a tier", children: <>Implementer → <code>Coder</code>, set in Agents or a project.</> },
        { title: "dude requests its model", children: <>Coder → <code>claude-opus-5-5</code>, sent as-is on every call.</> },
        { title: "The proxy serves it", children: "That model if it can, or a fallback from its own config. dude doesn’t see which." },
      ]} />
      <Label>NameChips</Label>
      <NameChips label="Names the proxy knows" names={["claude-opus-5-5", "claude-fable-5-1", "gpt-5.6-sol"]} value={model} onPick={setModel} />
      <Label>TierChip, with its tooltip; one with an effort; one with an effort and its tooltip; one with no tier</Label>
      <TierChip tier="Coder" model="claude-opus-5-5"
        tooltip={<TierTip title="Coder" aside="That is what dude asked for; how the proxy served it is the proxy’s to say.">When this session started, Coder asked the proxy for claude-opus-5-5.</TierTip>} />
      <TierChip tier="Coder" model="claude-sonnet-5" effort="medium" />
      <TierChip tier="Thinker" model="claude-opus-5-5" effort="high"
        tooltip={<TierTip title="Thinker" aside="That is what dude asked for; how the proxy served it is the proxy’s to say.">When this session started, Thinker asked the proxy for <code>claude-opus-5-5</code> at effort high; changing Thinker now changes the next session, not this one.</TierTip>} />
      <TierChip model="llm-anthropic/claude-sonnet-5" />
    </Col>
  );
}

const NETWORK_PRESETS: ReadonlyArray<HostPreset> = [
  { name: "GitHub", hosts: ["github.com", "*.github.com", "objects.githubusercontent.com"] },
  { name: "npm", hosts: ["registry.npmjs.org"] },
  { name: "PyPI", hosts: ["pypi.org", "files.pythonhosted.org"] },
];

/** A project's Network page: the organisation's hosts read-only, its own, presets, refused names. */
function NetworkDemo() {
  const org = ["github.com", "*.github.com", "objects.githubusercontent.com"];
  const [own, setOwn] = useState(["pypi.org"]);
  const [refused, setRefused] = useState([
    { name: "files.pythonhosted.org", runs: 12, roles: ["fixer", "implementer"] },
    { name: "registry.npmjs.org", runs: 2, roles: ["reviewer"] },
  ]);
  const [allowed, setAllowed] = useState(false);
  const allow = (names: string[]) => {
    setOwn((o) => [...new Set([...o, ...names])]);
    setRefused((r) => r.filter((x) => !names.includes(x.name)));
  };
  return (
    <Col>
      <SettingsSection>
        <SettingRow label="Agents may reach" help="Acme’s hosts, then this project’s. A hostname, an address or a range.">
          <SettingSource source="organization" from="Acme" />
          <HostChips readOnly hosts={org} />
          <Label>jervasion also</Label>
          <HostChips hosts={own} onChange={setOwn} validate={egressProblem} placeholder="a host this project needs" />
          <HostPresets presets={NETWORK_PRESETS} has={(h) => egressAllows([...org, ...own], h)} onAdd={(p) => setOwn((o) => [...new Set([...o, ...p.hosts])])} />
          <Label>Always reachable</Label>
          <HostChips readOnly muted hosts={["llmproxy.example.com", "dude’s tools"]} />
        </SettingRow>
        {refused.length ? (
          <SettingRow label="Refused recently" help="Hosts agents on jervasion tried in the last 7 days and were not allowed to reach." block>
            <RefusedHosts refused={refused} target="jervasion" onAllow={allow} />
          </SettingRow>
        ) : null}
      </SettingsSection>
      <Label>In the Run: a refused call’s note</Label>
      <ToolCallCard name="bash" status="completed" exitCode={1} args={{ command: "uv sync --dev" }}
        output={"error: Failed to fetch: https://files.pythonhosted.org/packages/…/idna-3.19-py3-none-any.whl\n  cause: dns error"}
        note={<NetworkRefusedNote host="files.pythonhosted.org" project="jervasion" organization="Acme" allowed={allowed}
          onAllow={() => setAllowed(true)} onSettings={() => {}} />} />
    </Col>
  );
}

export function SettingsGallerySection({ mode }: { readonly mode: PaneMode }) {
  const [source, setSource] = useState(PROMPT);
  const [prompt, setPrompt] = useState<"add" | "replace" | "inherit">("add");
  return (
    <Section id="settings" title="Settings and projects" intro="Organisation defaults and project overrides, prompts as documents with a history, and a project's epics.">
      <Block id="s-layout" title="Settings page" note="A left menu with sub-pages (the roles under Agents), rows of label and control, and on a project each value's source with Reset.">
        <Panes mode={mode}>
          <SettingsDemo />
        </Panes>
      </Block>
      <Block id="s-network" title="HostChips readOnly / HostPresets / RefusedHosts / NetworkRefusedNote" note="What an agent may reach. An organisation’s hosts on a project’s page are read-only chips under “From Acme”; what is always reachable is muted. Presets add a toolchain by name and tick one the list already reaches whole. Refused recently is a table with Allow on each and Allow all. In the Run, a tool call whose output names a refused host carries the note as ToolCallCard’s note, under it, never behind a click.">
        <Panes mode={mode}>
          <NetworkDemo />
        </Panes>
      </Block>
      <Block id="s-search" title="SearchResultRow" note="Memory's search: a ranked row per memory, task, epic or project in ArtifactRow's anatomy. The lead says what it is in the sidebar's grammar; which search found it is a quiet fact; why it ranked where it did is behind the click. No score chips, no bars.">
        <Panes mode={mode}>
          <SearchResultList>
            <SearchResultRow rank={1} lead={{ type: "memory" }} title="GitHub retries a delivery for up to 3 days; dedupe on X-GitHub-Delivery"
              facts={["words and meaning", "control-plane"]} defaultExpanded>
              <span>GitHub re-sends a webhook delivery it thinks failed for up to 3 days, with the same X-GitHub-Delivery id.</span>
              <KeyValueList items={[
                { label: "Words", value: "#1 · ts_rank_cd 0.612", mono: true },
                { label: "Meaning", value: "#1 · cosine distance 0.182", mono: true },
                { label: "Score", value: "0.0328 = 1/(60+1) + 1/(60+1)", mono: true },
              ]} />
              <div><Button size="sm" variant="secondary" leadingIcon="edit">Open memory</Button></div>
            </SearchResultRow>
            <SearchResultRow rank={2} lead={{ type: "task", taskKey: "WI-2402", status: "running" }}
              title="Dedupe deliveries by X-GitHub-Delivery across restarts" facts={["words and meaning", "control-plane"]}>
              <span>Goal: a redelivered webhook is processed once.</span>
            </SearchResultRow>
            <SearchResultRow rank={3} lead={{ type: "epic" }} title="Webhook reliability" facts={["words and meaning", "control-plane"]}>
              <span>Every GitHub webhook is verified, deduplicated and retried.</span>
            </SearchResultRow>
            <SearchResultRow rank={4} lead={{ type: "memory" }} title="4xx from a webhook consumer is never retried"
              badge={<Badge size="sm" emphasis="subtle" icon="clock">Text only</Badge>} facts={["words only", "control-plane"]}>
              <span>Only 5xx and timeouts are retried.</span>
            </SearchResultRow>
            <SearchResultRow rank={5} lead={{ type: "project", id: "p", name: "control-plane" }} title="control-plane" facts={["meaning only"]} />
          </SearchResultList>
        </Panes>
      </Block>
      <Block id="s-lines" title="EntityLine / AuthorLine / RefLead / RemovableList / SearchPicker" note="A face, a strong name, a muted line: a member, a memory, whoever wrote it — dude too, at a person's size. RefLead is what a thing is in the sidebar's grammar. RemovableList is what something is about; SearchPicker finds more, a combobox (↑↓, Enter, Escape).">
        <Panes mode={mode}>
          <Col>
            <Label>EntityLine, in a table cell</Label>
            <EntityLine lead={<Icon name="memory" size={16} />} name="Run the control-plane tests against a throwaway database"
              detail="Procedure · learned on WI-2402 · 2h ago" trailing={<Badge size="sm" icon="archive">Archived</Badge>} />
            <Label>AuthorLine: a person, an agent for a person, dude</Label>
            <AuthorLine author={{ kind: "person", person: { id: "ana", name: "Ana Ribeiro" } }} />
            <AuthorLine author={{ kind: "agent", person: { id: "marcio", name: "Márcio Martins" }, role: "implementer", task: "WI-2402" }} />
            <AuthorLine author={{ kind: "system", reason: "from an answer" }} />
            <Label>RefLead, named</Label>
            <RemovableList onRemove={() => {}} items={[
              { id: "e", label: "Webhook reliability", content: <RefLead type="epic" name="Webhook reliability" named /> },
              { id: "t", label: "WI-2402", content: <RefLead type="task" taskKey="WI-2402" status="running" /> },
              { id: "p", label: "control-plane", content: <RefLead type="project" id="p" name="control-plane" named /> },
            ]} />
            <Label>SearchPicker</Label>
            <SearchPicker<{ id: string; key: string; title: string }>
              label="Find a task" placeholder="A task key or words"
              find={async (q) => [{ id: "1", key: "WI-2401", title: "Add retry with backoff" }, { id: "2", key: "WI-2402", title: "Dedupe deliveries" }]
                .filter((t) => (t.key + t.title).toLowerCase().includes(q.toLowerCase()))}
              optionKey={(t) => t.id}
              renderOption={(t) => <><RefLead type="task" taskKey={t.key} status="queued" /><span>{t.title}</span></>}
              onPick={() => {}} />
          </Col>
        </Panes>
      </Block>
      <Block id="s-reviewers" title="Asking for a review: SearchPicker of GitHubUserLines / PullRequestPanel reviewers" note="Who to ask, as GitHub offers them: its suggestions before any words, people and teams by name after. One already asked is shown, not picked. Picks gather above the field; Backspace drops the last, ⌘Enter asks. In the panel every reviewer keeps a line — a comment, a team, one asked again after a verdict.">
        <Panes mode={mode}>
          <ReviewerDemo />
        </Panes>
      </Block>
      <Block id="s-machines" title="NumberInput / Select meta / FitBar / ProportionBar / MachineChip" note="A machine size in the pieces that show it. NumberInput moves in steps (− value + and ↑ ↓), keeps what is typed, and names the step when it is off. A Select option's meta is muted on its line and follows the label into the trigger; description is a line under it; footer sits under the list. FitBar is how much of one host a size takes, words only when nobody knows the host. ProportionBar splits a host's memory, the part nobody gets hatched. MachineChip is the session header's machine, its origin in a tooltip.">
        <Panes mode={mode}>
          <MachinesDemo />
        </Panes>
      </Block>
      <Block id="s-tiers" title="TierLine / FlowSteps / NameChips / TierChip" note="A model tier in the pieces that show it. TierLine is a tier in a table or picker: its mark, name and what it is for. FlowSteps explains how something works once, in steps side by side. NameChips are suggestions under a field that takes any name. TierChip is the session header's model: the tier and the model it requested, what dude asked for and no more.">
        <Panes mode={mode}>
          <TiersDemo />
        </Panes>
      </Block>
      <Block id="s-markdown" title="MarkdownDocument" note="Reads rendered; Edit swaps in the Markdown source, lightly highlighted, in place; Save or Cancel returns to reading.">
        <Panes mode={mode}>
          <Col>
            <Label>Prompt, with how a project uses it</Label>
            <Segmented label="Prompt" size="sm" value={prompt} onChange={setPrompt}
              options={[{ value: "add", label: "Add to Acme’s" }, { value: "replace", label: "Replace Acme’s" }, { value: "inherit", label: "Use Acme’s" }]} />
            <MarkdownDocument source={source} onSave={setSource} meta="Last changed by Eli · yesterday" />
            <Label>Editing, empty</Label>
            <MarkdownDocument source="" onSave={() => {}} defaultEditing emptyText="Nothing added." />
            <Label>Read-only</Label>
            <MarkdownDocument source={"## Notes\n\nNo **Edit** without `onSave`."} />
          </Col>
        </Panes>
      </Block>
      <Block id="s-history" title="PromptHistory" note="Versions newest first; the selected one's changes against the one before, or the whole prompt; the sessions it told; Restore for any but the current.">
        <Panes mode={mode}>
          <PromptHistory versions={VERSIONS} onRestore={() => {}} />
        </Panes>
      </Block>
      <Block id="s-epics" title="EpicCard / EpicRow" note="In progress as cards: progress by lane (each with a word), pull requests, cost and people. Planned and done as quiet rows.">
        <Panes mode={mode}>
          <Col>
            <EpicCard epic={EPIC} />
            <EpicRow epic={{ ...EPIC, id: "e2", title: "Exports", prs: {}, when: "not started" }} detail="4 tasks" />
            <EpicRow epic={{ ...EPIC, id: "e3", title: "Dark mode", prs: { merged: 3 }, when: "finished Sep 14" }} detail="3 tasks · $2.70" />
          </Col>
        </Panes>
      </Block>
    </Section>
  );
}

interface Reviewer extends GitHubUser {
  readonly reason?: string | undefined;
  readonly asked?: boolean | undefined;
}

const REVIEWERS: ReadonlyArray<Reviewer> = [
  { login: "anaribeiro", name: "Ana Ribeiro", reason: "Changed these files recently" },
  { login: "tokafor", name: "Tom Okafor", reason: "Commented on this pull request" },
  { login: "danabrams", name: "Dana Abrams", reason: "Changed these files recently", asked: true },
  { login: "hanna", name: "Hanna Lindqvist" },
  { login: "ananya-k", name: "Ananya Krishnan" },
  { login: "acme/platform", name: "Platform", team: true, reason: "7 members" },
];

/** GitHub's suggestions before any words; people and teams by name after. */
async function findReviewers(q: string): Promise<ReadonlyArray<Reviewer>> {
  return REVIEWERS.filter((r) => (q ? `${r.login} ${r.name}`.toLowerCase().includes(q.toLowerCase()) : r.reason && !r.team))
    .map((r) => (q && !r.team ? { login: r.login, name: r.name, asked: r.asked } : r));
}

function ReviewerDemo() {
  const [picked, setPicked] = useState<Reviewer[]>([]);
  return (
    <Col>
      <Label>Request review</Label>
      <RemovableList onRemove={(login) => setPicked((l) => l.filter((r) => r.login !== login))}
        items={picked.map((r) => ({ id: r.login, label: r.login, content: <GitHubUserLine user={r} size={20} inline /> }))} />
      <SearchPicker<Reviewer>
        label="Who to ask for a review" placeholder={picked.length ? "Anyone else?" : "Name or GitHub login"} size="sm"
        findOnEmpty clearOnPick find={findReviewers} exclude={new Set(picked.map((r) => r.login))}
        group={(r) => (r.reason && !r.team ? "Suggested by GitHub" : r.team ? "Teams" : "People")}
        renderGroup={(g) => <>{g === "Suggested by GitHub" ? <Icon name="github" size={12} /> : null}{g}</>}
        optionKey={(r) => r.login}
        renderOption={(r) => <GitHubUserLine user={r} detail={r.reason} />}
        optionDisabled={(r) => (r.asked ? "Already asked" : null)}
        empty={(q) => `Nobody who can review this repository matches “${q}”.`}
        onPick={(r) => setPicked((l) => [...l, r])}
        onBackspaceEmpty={() => setPicked((l) => l.slice(0, -1))} />
      <Label>PullRequestPanel, once they review</Label>
      <PullRequestPanel
        pr={{ number: 482, url: "#", title: "Retry webhook deliveries with exponential backoff", repositoryName: "control-plane",
          state: "open", review: "changes_requested", checks: "passing", baseBranch: "main", mergeable: "clean",
          reviews: [
            { login: "danabrams", state: "APPROVED", submittedAt: "2026-10-01T09:00:00Z", rerequested: true },
            { login: "anaribeiro", state: "APPROVED", submittedAt: "2026-10-01T10:00:00Z" },
            { login: "kai-n", state: "CHANGES_REQUESTED", submittedAt: "2026-10-01T10:05:00Z" },
            { login: "tokafor", state: "COMMENTED", submittedAt: "2026-10-01T10:10:00Z" },
            { login: "acme/platform", state: "REQUESTED", team: true },
          ] }}
        factActions={{ reviews: <Button size="sm" variant="quiet">Request review</Button> }} />
    </Col>
  );
}

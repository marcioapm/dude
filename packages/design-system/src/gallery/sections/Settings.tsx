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
import { KeyValueList } from "../../primitives/Layout.tsx";
import { SearchResultList, SearchResultRow } from "../../components/SearchResultRow.tsx";
import { EntityLine } from "../../components/EntityLine.tsx";
import { AuthorLine } from "../../components/PersonAvatar.tsx";
import { RefLead } from "../../components/RefLead.tsx";
import { RemovableList } from "../../components/RemovableList.tsx";
import { SearchPicker } from "../../components/SearchPicker.tsx";
import { Icon } from "../../icons/index.tsx";
import { Select } from "../../primitives/Select.tsx";

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
      </SettingsSection>
    </SettingsLayout>
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

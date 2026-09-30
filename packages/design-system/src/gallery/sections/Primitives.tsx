import { useRef, useState } from "react";
import { Block, Caption, Col, Label, Panes, Row, Section, States, type PaneMode } from "../Frame.tsx";
import styles from "../gallery.module.css";
import { Button, IconButton } from "../../primitives/Button.tsx";
import { Input } from "../../primitives/Input.tsx";
import { Textarea } from "../../primitives/Textarea.tsx";
import { MarkdownEditor, type MarkdownEditorProps } from "../../primitives/MarkdownEditor.tsx";
import { HelpList, KeyHint, MarkdownCheatsheet } from "../../primitives/Kbd.tsx";
import { RowMenu, RowMenuTrigger, rowMenuOpeners, type RowMenuItem } from "../../primitives/RowMenu.tsx";
import { Select } from "../../primitives/Select.tsx";
import { Checkbox } from "../../primitives/Checkbox.tsx";
import { Badge } from "../../primitives/Badge.tsx";
import { Card, CardBody, CardFooter, CardHeader } from "../../primitives/Card.tsx";
import { Table, TBody, Td, Th, THead, Tr, TableEmpty, type SortDirection } from "../../primitives/Table.tsx";
import { Tab, TabList, TabPanel, Tabs } from "../../primitives/Tabs.tsx";
import { Dialog, DialogClose } from "../../primitives/Dialog.tsx";
import { DiscardConfirm } from "../../primitives/DiscardConfirm.tsx";
import { useToast } from "../../primitives/Toast.tsx";
import { Tooltip } from "../../primitives/Tooltip.tsx";
import { EmptyState, Skeleton, SkeletonLines, Spinner } from "../../primitives/Feedback.tsx";
import { ScrollArea } from "../../primitives/ScrollArea.tsx";
import { Callout, Fieldset, FormActions, FormRow, FormStack, KeyValueList, Page, PageHeader, Section as PageSection } from "../../primitives/Layout.tsx";
import { StepList, StepRow } from "../../components/StepList.tsx";
import { Breadcrumb } from "../../components/Breadcrumb.tsx";
import { AgentAvatar } from "../../components/AgentAvatar.tsx";
import { Icon } from "../../icons/index.tsx";
import { StatusBadge } from "../../components/StatusBadge.tsx";
import { CostDisplay, Duration } from "../../components/Numbers.tsx";
import { TONE_NAMES } from "../../tokens/palette.ts";

const VARIANTS = ["primary", "secondary", "quiet", "danger"] as const;

const moveToEpic: RowMenuItem = {
  kind: "submenu",
  id: "move",
  label: "Move to epic",
  icon: "layers",
  items: [
    { id: "e1", label: "OAuth migration", icon: "layers" },
    { id: "e2", label: "Q4 performance", icon: "layers" },
    { id: "e3", label: "Webhook reliability and delivery guarantees", icon: "layers" },
    { kind: "separator" },
    { id: "none", label: "No epic" },
  ],
};
const taskMenu: ReadonlyArray<RowMenuItem> = [
  { id: "edit", label: "Edit", icon: "edit", shortcut: "E" },
  moveToEpic,
  { id: "split", label: "Split", icon: "simplifier" },
  { kind: "separator" },
  { id: "delete", label: "Delete", icon: "cross", tone: "danger", disabled: true, disabledReason: "It has run; abort it instead." },
  { id: "abort", label: "Abort run", icon: "stop", tone: "danger" },
];
const epicMenu: ReadonlyArray<RowMenuItem> = [
  { id: "edit", label: "Edit", icon: "edit" },
  { id: "new", label: "New task", icon: "plus", shortcut: "N" },
  { kind: "separator" },
  { id: "up", label: "Move up", icon: "arrow-up", disabled: true, disabledReason: "Already first" },
  { id: "down", label: "Move down", icon: "arrow-down" },
  { kind: "separator" },
  { id: "delete", label: "Delete", icon: "cross", tone: "danger" },
];
const projectMenu: ReadonlyArray<RowMenuItem> = [
  { id: "settings", label: "Settings", icon: "settings", shortcut: "," },
  { id: "new-epic", label: "New epic", icon: "layers" },
  { id: "new", label: "New task", icon: "plus" },
  { kind: "separator" },
  { id: "archive", label: "Archive", icon: "folder", tone: "danger" },
];

export function PrimitivesSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section
      id="primitives"
      title="Primitives"
      intro="Generic controls. Default control height is 28px; 24px small for toolbars and table rows. Every primitive is keyboard operable and shows the shared focus ring only on keyboard focus."
    >
      <Block id="p-button" title="Button" note="Secondary is the default — most actions in a console are quiet. One primary per view. Destructive only where work is lost, and always behind a confirm.">
        <Panes mode={mode}>
          <Col>
            <States
              items={VARIANTS.map((v) => [
                v,
                <>
                  <Button variant={v}>Label</Button>
                  <Button variant={v} leadingIcon="plus">
                    Icon
                  </Button>
                  <Button variant={v} loading>
                    Loading
                  </Button>
                  <Button variant={v} disabled>
                    Disabled
                  </Button>
                  <Button variant={v} size="sm">
                    Small
                  </Button>
                  <Button variant={v} size="lg">
                    Large
                  </Button>
                </>,
              ])}
            />
            <Label>IconButton</Label>
            <Row>
              <IconButton icon="more" label="More actions" />
              <IconButton icon="copy" label="Copy" variant="secondary" />
              <IconButton icon="close" label="Close" size="sm" />
              <IconButton icon="cross" label="Abort" variant="danger" />
              <IconButton icon="search" label="Search" disabled />
            </Row>
            <Label>Toolbar composition</Label>
            <Row>
              <Button size="sm" variant="quiet" leadingIcon="git-branch">
                main
              </Button>
              <Button size="sm" variant="secondary" leadingIcon="pause">
                Pause
              </Button>
              <Button size="sm" variant="danger" leadingIcon="stop">
                Abort run
              </Button>
              <span style={{ flex: 1 }} />
              <Button size="sm" variant="primary" leadingIcon="merge">
                Merge
              </Button>
            </Row>
          </Col>
        </Panes>
      </Block>

      <Block id="p-input" title="Input" note="Label, hint, and error are part of the field so every form has the same anatomy. Mono for IDs and paths.">
        <Panes mode={mode}>
          <div className={styles["grid2"]}>
            <Input label="Title" placeholder="Describe the change…" />
            <Input label="Branch" mono defaultValue="feat/webhook-retry" leading={<Icon name="git-branch" size={12} />} />
            <Input label="Budget" defaultValue="2.50" leading="$" trailing="USD" hint="Per-session hard limit" />
            <Input label="Repository URL" defaultValue="git@github" error="Must be an https:// or ssh:// URL" />
            <Input label="Disabled" defaultValue="Not editable" disabled />
            <Input size="sm" placeholder="Filter events…" leading={<Icon name="search" size={12} />} aria-label="Filter events" />
            <Input size="title" label="Title" labelNote="required" placeholder="What should change?" />
            <Input size="title" label="Title (locked)" defaultValue="Payment step keeps SEPA and Invoice" disabled />
          </div>
        </Panes>
      </Block>

      <Block id="p-textarea" title="Textarea" note="The Input anatomy, taller. Grows with its content from `rows` to `maxRows` (default 3 → 12) and then scrolls; no resize handle. Mono for commands and config. Try typing past the limit.">
        <Panes mode={mode}>
          <div className={styles["grid2"]}>
            <Textarea label="Goal" placeholder="Why, and any detail the agent should know…" hint="Markdown. Up to 10k characters." />
            <Textarea label="Description" defaultValue={"Migrate every login flow to PKCE.\n\n- Web\n- Mobile\n- CLI"} />
            <Textarea label="Runtime command" mono rows={2} maxRows={6} defaultValue={"bun install --frozen-lockfile\nbun test"} />
            <Textarea label="Acceptance criterion" defaultValue="" error="Each criterion must be under 2000 characters" rows={2} />
            <Textarea label="Locked" defaultValue="Kind and repository are fixed once a run exists." disabled rows={2} />
            <ControlledTextarea />
          </div>
        </Panes>
      </Block>

      <Block
        id="p-markdown-editor"
        title="MarkdownEditor"
        note="For writing one Markdown document — a task's goal, its criteria. Textarea's anatomy around a frame: Write / Preview on the chrome shade (Ctrl/⌘+Shift+P), quiet formatting (Ctrl/⌘+B I K E), the source in mono, growing with its content — the page scrolls, never the field. Enter continues a list; Enter on an empty item ends it. Preview is Markdown in the caller's variant (message by default: the variant the text is read in), the one safe renderer, at least as tall as the source was. Locked opens in Preview with Write disabled and says why in the hint. The count turns attention past 90% and danger over."
      >
        <Panes mode={mode}>
          <Col>
            <MarkdownEditorDemo label="Write" initial={SAMPLE_GOAL} hint="Why it matters, what exists today, and anything an agent can't guess." />
            <MarkdownEditorDemo label="Preview" initial={SAMPLE_GOAL} defaultMode="preview" />
            <MarkdownEditorDemo label="Empty" initial="" placeholder="Why does this matter? What exists today? What must an agent not break?" minRows={4} />
            <MarkdownEditorDemo label="Locked" initial={SAMPLE_CRITERIA} locked hint="Delivery has started, so what it asks for is fixed." />
            <MarkdownEditorDemo label="Near the limit" initial={"- [ ] " + "Each step fires its funnel event exactly once. ".repeat(4)} maxLength={200} minRows={2} />
            <MarkdownEditorDemo label="Over the limit" initial={"- [ ] " + "Each step fires its funnel event exactly once. ".repeat(5)} maxLength={200} minRows={2}
              error="Criterion 1 is over 200 characters." />
            <MarkdownEditorDemo label="With a summary" initial={SAMPLE_CRITERIA + "\n\nAnd a note that is not a criterion."} minRows={4}
              summary={<Badge tone="neutral" size="sm">4 criteria</Badge>} notice="Text outside a list item isn't saved as a criterion" />
          </Col>
        </Panes>
      </Block>

      <Block
        id="p-rowmenu"
        title="RowMenu"
        note="The overflow menu behind a '…' button on a row — tree rows, board headers, table rows. Items carry a glyph, a label, an optional shortcut hint, a danger tone, a disabled reason (tooltip and read aloud) and can open a submenu. Opens on click; a row that spreads rowMenuOpeners also opens it on right-click and Shift+F10. Portaled at the popover layer so it works inside a Dialog. Try ↑↓, →/← for the submenu, Esc."
      >
        <Panes mode={mode}>
          <Col>
            <Row style={{ gap: 24 }}>
              <States
                items={[
                  ["task", <RowMenu items={taskMenu} label="Actions for CP-41" />],
                  ["epic", <RowMenu items={epicMenu} label="Actions for OAuth migration" />],
                  ["project", <RowMenu items={projectMenu} label="Actions for Customer Portal" size="md" />],
                  ["custom trigger", <RowMenu items={epicMenu} label="Epic actions" trigger={<Button size="sm" trailingIcon="chevron-down">Edit epic</Button>} />],
                ]}
              />
            </Row>
            <Label>In a row: hover, focus, right-click, Shift+F10</Label>
            <MenuRowDemo />
            <Label>Inside a dialog</Label>
            <Row>
              <Dialog
                trigger={<Button>Open dialog with a menu</Button>}
                title="Edit task"
                description="The menu must open above the dialog, not beneath its scrim."
                footer={
                  <DialogClose asChild>
                    <Button variant="primary">Done</Button>
                  </DialogClose>
                }
              >
                <Row>
                  <Input label="Title" defaultValue="Add PKCE to the login flow" style={{ flex: 1 }} />
                  <span style={{ alignSelf: "flex-end" }}>
                    <RowMenu items={taskMenu} label="Actions for CP-41" size="md" />
                  </span>
                </Row>
              </Dialog>
            </Row>
          </Col>
        </Panes>
      </Block>

      <Block id="p-select" title="Select" note="Radix-backed: typeahead, arrow keys, groups. Below ~30 options only.">
        <Panes mode={mode}>
          <Row top>
            <Select
              label="Model"
              defaultValue="claude-opus-4"
              options={[
                { label: "Anthropic", options: [{ value: "claude-opus-4", label: "claude-opus-4" }, { value: "claude-sonnet-4", label: "claude-sonnet-4" }] },
                { label: "Other", options: [{ value: "gpt-5", label: "gpt-5" }, { value: "local", label: "local (disabled)", disabled: true }] },
              ]}
            />
            <Select label="Status" placeholder="Any status" options={[{ value: "running", label: "Running" }, { value: "awaiting_input", label: "Needs you" }, { value: "done", label: "Done" }]} />
            <Select size="sm" aria-label="Density" defaultValue="default" options={[{ value: "compact", label: "Compact" }, { value: "default", label: "Default" }, { value: "comfortable", label: "Comfortable" }]} />
            <Select label="Disabled" disabled defaultValue="x" options={[{ value: "x", label: "Locked" }]} />
          </Row>
        </Panes>
      </Block>

      <Block id="p-checkbox" title="Checkbox">
        <Panes mode={mode}>
          <Row top style={{ gap: 24 }}>
            <Checkbox label="Unchecked" />
            <Checkbox label="Checked" defaultChecked />
            <Checkbox label="Indeterminate" checked="indeterminate" />
            <Checkbox label="Disabled" disabled />
            <Checkbox label="Disabled checked" disabled defaultChecked />
            <Checkbox label="Auto-merge when green" description="Requires all required checks and one approving review." defaultChecked />
          </Row>
        </Panes>
      </Block>

      <Block id="p-badge" title="Badge / Tag" note="Generic chip. For statuses use StatusBadge. Solid is loud; reserve it.">
        <Panes mode={mode}>
          <Col>
            <States
              items={(["subtle", "tinted", "solid"] as const).map((e) => [
                e,
                <>
                  {TONE_NAMES.map((t) => (
                    <Badge key={t} tone={t} emphasis={e}>
                      {t}
                    </Badge>
                  ))}
                </>,
              ])}
            />
            <Row>
              <Badge icon="git-pr">#412</Badge>
              <Badge dot tone="success">
                ci passing
              </Badge>
              <Badge mono>a3f9c1e</Badge>
              <Badge mono size="sm">
                v0.9
              </Badge>
              <Badge size="sm" tone="info">
                claude-opus-4
              </Badge>
              <Badge tone="attention" icon="warning">
                2 findings
              </Badge>
            </Row>
          </Col>
        </Panes>
      </Block>

      <Block id="p-card" title="Card" note="One surface step above its parent. Do not nest cards; divide inside one.">
        <Panes mode={mode}>
          <div className={styles["grid3"]}>
            <Card>
              <CardHeader title="Default" actions={<IconButton icon="more" label="More" size="sm" />} />
              <CardBody>Body text at 12px padding.</CardBody>
              <CardFooter>Footer · secondary text</CardFooter>
            </Card>
            <Card variant="raised" interactive>
              <CardHeader title="Raised, interactive" />
              <CardBody>Hover me.</CardBody>
            </Card>
            <Card variant="flat" selected>
              <CardHeader title="Flat, selected" />
              <CardBody padding="dense">Dense padding.</CardBody>
            </Card>
          </div>
        </Panes>
      </Block>

      <Block id="p-table" title="Table" note="Dense, 28px rows (24 compact / 36 comfortable). Sticky header; sortable headers render aria-sort and the affordance but do not sort data. Numbers right-aligned and tabular.">
        <Panes mode={mode} surface>
          <SortableTableDemo />
        </Panes>
        <div style={{ height: 8 }} />
        <Panes mode={mode} surface>
          <Table density="compact">
            <THead>
              <Tr>
                <Th>Compact</Th>
                <Th align="right">Cost</Th>
              </Tr>
            </THead>
            <TBody>
              <TableEmpty colSpan={2}>
                <EmptyState compact icon="search" title="No runs match" description="Try clearing the status filter." />
              </TableEmpty>
            </TBody>
          </Table>
        </Panes>
      </Block>

      <Block id="p-tabs" title="Tabs" note="Underline for page-level navigation; segmented for view switches inside a toolbar. Counts are quiet.">
        <Panes mode={mode}>
          <Col>
            <Tabs defaultValue="timeline">
              <TabList aria-label="Task sections">
                <Tab value="overview">Overview</Tab>
                <Tab value="timeline" icon="list" count={128}>
                  Timeline
                </Tab>
                <Tab value="sessions" count={5}>
                  Sessions
                </Tab>
                <Tab value="questions" count={1}>
                  Questions
                </Tab>
                <Tab value="disabled" disabled>
                  Deployments
                </Tab>
              </TabList>
              <TabPanel value="overview">
                Overview panel
              </TabPanel>
              <TabPanel value="timeline">
                Timeline panel
              </TabPanel>
              <TabPanel value="sessions">
                Sessions panel
              </TabPanel>
              <TabPanel value="questions">
                Questions panel
              </TabPanel>
            </Tabs>
            <Tabs defaultValue="board">
              <TabList variant="segmented" aria-label="View">
                <Tab value="board">Board</Tab>
                <Tab value="list">List</Tab>
                <Tab value="tree">Tree</Tab>
              </TabList>
            </Tabs>
          </Col>
        </Panes>
      </Block>

      <Block id="p-dialog" title="Dialog" note="For decisions and small forms — and for writing one document: size='document' is a fixed 1120×900 (full screen under 640px) with an optional aside on the chrome shade that scrolls on its own and stacks under the writing below 960px; context puts where the thing sits above the title. Destructive confirmations get the danger tone and a destructive primary action. DiscardConfirm is the one asked before closing loses writing: Keep writing (quiet, focused) and Discard (danger solid).">
        <Panes mode={mode}>
          <Row>
            <TaskDialogExample />
            <DiscardConfirmExample />
            <Dialog
              trigger={<Button>Open dialog</Button>}
              title="Retry run"
              description="Creates attempt 3 from the last checkpoint. The previous attempts are kept."
              footer={
                <>
                  <DialogClose asChild>
                    <Button variant="quiet">Cancel</Button>
                  </DialogClose>
                  <DialogClose asChild>
                    <Button variant="primary">Retry</Button>
                  </DialogClose>
                </>
              }
            >
              <Input label="Note for the agent (optional)" placeholder="What changed since last time?" />
            </Dialog>
            <Dialog
              trigger={<Button variant="danger">Abort run</Button>}
              tone="danger"
              size="sm"
              title="Abort run 14?"
              description="The running sessions will be stopped. Work in the workspace is preserved; the run cannot be resumed."
              footer={
                <>
                  <DialogClose asChild>
                    <Button variant="quiet">Keep running</Button>
                  </DialogClose>
                  <DialogClose asChild>
                    <Button variant="danger" solid>Abort</Button>
                  </DialogClose>
                </>
              }
            />
          </Row>
        </Panes>
      </Block>

      <Block id="p-page" title="Page, PageHeader, Section" note="The frame of a page outside the board and the transcript — a task, settings, an inbox. The page keeps to --ds-measure-page; its header says where it is (a Breadcrumb, never a Back button), what it is, and what can be done; sections are titled in small caps. All spacing is the density's panel gap.">
        <Panes mode={mode} surface>
          <Page>
            <PageHeader
              status={<StatusBadge status="review" size="sm" />}
              itemKey="TEXT-20"
              title="Add a paragraph-count helper"
              description="Count paragraphs in a text. A line with only whitespace separates them."
              actions={<Button size="sm">Move</Button>}
            />
            <PageSection title="Pipeline" count={3}>
              <StepList>
                <StepRow step="1" avatar={<AgentAvatar role="implementer" size="sm" />} label="Implement" status={<StatusBadge status="completed" size="sm" />} meta="376bf6c" onOpen={() => undefined} />
                <StepRow step="2" avatar={<AgentAvatar role="reviewer" size="sm" />} label="Review · correctness" status={<StatusBadge status="running" size="sm" />} note="reading the change" onOpen={() => undefined} />
                <StepRow step="PR" label="Pull request #15" status={<StatusBadge status="review" size="sm" />} note="checks passing · review pending" meta="dude/wi_20/attempt-1" href="https://github.com" />
              </StepList>
            </PageSection>
          </Page>
        </Panes>
      </Block>

      <Block id="p-form" title="Form layout, Fieldset, Callout, KeyValueList" note="FormStack keeps a form to a readable width; FormRow puts fields side by side; Fieldset groups controls under one question with the Input anatomy (label, hint, error); FormActions puts the primary first and a note after. Callout says why something failed where it failed — not a toast, and never for agent events. KeyValueList is facts about a thing; mono for identifiers.">
        <Panes mode={mode} surface>
          <FormStack>
            <Callout tone="danger">url must be an https, ssh or git:// URL</Callout>
            <FormRow>
              <Input label="Review rounds" defaultValue="5" hint="Review → fix cycles before a person is asked." />
              <Input label="Park after (minutes)" defaultValue="10" />
            </FormRow>
            <Fieldset legend="Reviewers every delivery runs" hint="Others join when a change touches their area.">
              <Checkbox label="correctness" defaultChecked />
              <Checkbox label="security" />
            </Fieldset>
            <FormActions note="Settings left at the factory's defaults follow them if they change.">
              <Button variant="primary">Save</Button>
            </FormActions>
            <Callout tone="success">Connected as marcioapm · scopes: repo</Callout>
            <KeyValueList
              items={[
                { label: "Account", value: "marcioapm" },
                { label: "Webhook URL", value: "https://dude.example/webhooks/github/org_1", mono: true },
              ]}
            />
          </FormStack>
        </Panes>
      </Block>

      <Block id="p-toast" title="Toast" note="Only for outcomes of your own actions. Agent events never toast — they live in the stream and the queue. Danger toasts stick until dismissed.">
        <Panes mode={mode}>
          <ToastDemo />
        </Panes>
      </Block>

      <Block id="p-tooltip" title="Tooltip" note="Supplementary only. Inverted surface so it reads on any background.">
        <Panes mode={mode}>
          <Row>
            <Tooltip content="Open the session in a side panel" shortcut="⏎">
              <Button>Hover me</Button>
            </Tooltip>
            <Tooltip content="ses_01J9K2QF7X3M8N4P" mono side="bottom">
              <Badge mono>ses_01J9K2</Badge>
            </Tooltip>
            <Tooltip content="Cost so far this run, including subagents">
              <span>
                <CostDisplay usd={1.284} />
              </span>
            </Tooltip>
          </Row>
        </Panes>
      </Block>

      <Block id="p-loading" title="Skeleton / Spinner" note="Skeletons match the content shape; the shimmer is subtle. Spinner for short in-component waits.">
        <Panes mode={mode}>
          <Row top style={{ gap: 24 }}>
            <Col style={{ width: 220 }}>
              <Row>
                <Skeleton variant="circle" width={20} height={20} />
                <Skeleton width={120} height={12} />
              </Row>
              <SkeletonLines lines={3} />
            </Col>
            <Col>
              <Spinner />
              <Spinner label="Provisioning workspace…" />
            </Col>
            <Card style={{ width: 200 }}>
              <CardBody>
                <Skeleton width={60} height={10} />
                <div style={{ height: 6 }} />
                <Skeleton width={110} height={22} />
              </CardBody>
            </Card>
          </Row>
        </Panes>
      </Block>

      <Block id="p-empty" title="EmptyState" note="Say what would appear and how to make it appear. No illustrations.">
        <Panes mode={mode} surface>
          <Col>
            <EmptyState icon="hand" title="Nothing needs you" description="When an agent asks a question or a plan needs confirmation, it shows up here." />
            <EmptyState compact icon="terminal" title="No output yet" description="The session has not produced any log lines." action={<Button size="sm">Refresh</Button>} />
          </Col>
        </Panes>
      </Block>

      <Block id="p-scroll" title="ScrollArea" note="Overlay scrollbars that look the same in Chromium and WebKit (Tauri).">
        <Panes mode={mode}>
          <ScrollArea style={{ height: 120, border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6 }}>
            <div style={{ padding: 8, width: 900 }}>
              {Array.from({ length: 14 }, (_, i) => (
                <div key={i} style={{ padding: "3px 0", fontSize: 12, whiteSpace: "nowrap" }}>
                  Row {i + 1} — scrolls both ways; this line is intentionally wider than the viewport so a horizontal bar appears too.
                </div>
              ))}
            </div>
          </ScrollArea>
        </Panes>
      </Block>
    </Section>
  );
}

const RUNS = [
  { id: "run_9f2a", item: "WI-2481 Add retry with backoff", status: "running", cost: 1.284, ms: 1_640_000, attempt: 2 },
  { id: "run_8c11", item: "WI-2477 Migrate events table to partitions", status: "awaiting_input", cost: 0.63, ms: 4_010_000, attempt: 1 },
  { id: "run_7b03", item: "WI-2470 Fix flaky E2E login test", status: "completed", cost: 0.412, ms: 903_000, attempt: 1 },
  { id: "run_6a99", item: "WI-2466 Upgrade drizzle-orm", status: "failed", cost: 2.91, ms: 5_400_000, attempt: 3 },
  { id: "run_5d42", item: "WI-2460 Add org switcher", status: "paused", cost: 0.088, ms: 120_000, attempt: 1 },
  { id: "run_4e17", item: "WI-2459 Document runner protocol", status: "scheduled", cost: 0, ms: 0, attempt: 1 },
] as const;

function SortableTableDemo() {
  const [sort, setSort] = useState<{ key: "cost" | "ms" | "item"; dir: SortDirection }>({ key: "cost", dir: "desc" });
  const [selected, setSelected] = useState<string | null>("run_8c11");
  const rows = [...RUNS].sort((a, b) => {
    const va = a[sort.key];
    const vb = b[sort.key];
    const c = typeof va === "number" && typeof vb === "number" ? va - vb : String(va).localeCompare(String(vb));
    return sort.dir === "asc" ? c : -c;
  });
  return (
    <Table maxHeight={220}>
      <THead>
        <Tr>
          <Th width={32}>
            <Checkbox aria-label="Select all" checked="indeterminate" />
          </Th>
          <Th width={90}>Run</Th>
          <Th sort={sort.key === "item" ? sort.dir : null} onSort={(dir) => setSort({ key: "item", dir })}>
            Task
          </Th>
          <Th width={120}>Status</Th>
          <Th align="right" width={60}>
            Attempt
          </Th>
          <Th align="right" width={90} sort={sort.key === "ms" ? sort.dir : null} onSort={(dir) => setSort({ key: "ms", dir })}>
            Duration
          </Th>
          <Th align="right" width={90} sort={sort.key === "cost" ? sort.dir : null} onSort={(dir) => setSort({ key: "cost", dir })}>
            Cost
          </Th>
        </Tr>
      </THead>
      <TBody>
        {rows.map((r) => (
          <Tr key={r.id} interactive selected={selected === r.id} onClick={() => setSelected(r.id)}>
            <Td>
              <Checkbox aria-label={`Select ${r.id}`} checked={selected === r.id} />
            </Td>
            <Td mono muted>
              {r.id}
            </Td>
            <Td>{r.item}</Td>
            <Td>
              <StatusBadge status={r.status} size="sm" />
            </Td>
            <Td align="right" mono muted>
              {r.attempt}
            </Td>
            <Td align="right" mono>
              {r.ms > 0 ? <Duration ms={r.ms} /> : <span style={{ color: "var(--ds-color-text-disabled)" }}>—</span>}
            </Td>
            <Td align="right" mono>
              <CostDisplay usd={r.cost} />
            </Td>
          </Tr>
        ))}
      </TBody>
    </Table>
  );
}

function ControlledTextarea() {
  const [v, setV] = useState("");
  const over = v.length > 80;
  return <Textarea label="Controlled (80 chars)" value={v} onChange={(e) => setV(e.target.value)} rows={2} maxRows={4} hint={over ? undefined : `${80 - v.length} left`} error={over ? `${v.length - 80} over the limit` : undefined} />;
}

/** A focusable row with a hover/focus menu, opened from the row's own keyboard and right-click. */
function MenuRowDemo() {
  const [open, setOpen] = useState(false);
  const [last, setLast] = useState<string | null>(null);
  const rowRef = useRef<HTMLDivElement>(null);
  const openers = rowMenuOpeners(() => setOpen(true));
  return (
    <Col>
      <div
        ref={rowRef}
        tabIndex={0}
        role="row"
        aria-label="CP-41 Add PKCE to the login flow"
        className={styles["menuRow"]}
        {...openers}
      >
        <StatusBadge status="running" variant="dot" iconOnly />
        <span className="ds-mono" style={{ fontSize: 11, color: "var(--ds-color-text-muted)" }}>
          CP-41
        </span>
        <span style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>Add PKCE to the login flow</span>
        <span className={styles["menuRowSlot"]} data-open={open ? "true" : undefined}>
          <RowMenu
            items={taskMenu}
            label="Actions for CP-41"
            trigger={<RowMenuTrigger label="Actions for CP-41" />}
            open={open}
            onOpenChange={setOpen}
            onSelect={setLast}
            onCloseAutoFocus={(e) => {
              e.preventDefault();
              rowRef.current?.focus();
            }}
          />
        </span>
      </div>
      <Caption>last action: {last ?? "none"}</Caption>
    </Col>
  );
}

function ToastDemo() {
  const { toast } = useToast();
  return (
    <Row>
      <Button onClick={() => toast({ title: "Copied session ID" })}>Neutral</Button>
      <Button onClick={() => toast({ title: "Run 14 paused", description: "Resume from the run page.", tone: "info" })}>Info</Button>
      <Button onClick={() => toast({ title: "Merged #412", tone: "success", action: { label: "View", onClick: () => undefined } })}>Success</Button>
      <Button onClick={() => toast({ title: "Budget at 85%", description: "WI-2481 has used $2.12 of $2.50.", tone: "attention" })}>Attention</Button>
      <Button onClick={() => toast({ title: "Could not abort run", description: "Worker did not acknowledge within 10s.", tone: "danger", action: { label: "Retry", onClick: () => undefined } })}>Danger (sticky)</Button>
      <Caption>toasts render bottom-right of the page, in the active app theme</Caption>
    </Row>
  );
}

const SAMPLE_GOAL = `Checkout v2 dropped **SEPA** and **Invoice** from the payment step. About 18% of annual plans paid that way last quarter, and sales has had [three escalations](https://example.com/issues/412) this month.

## What exists today
- The old flow lives behind \`checkout_v2\` in \`web/src/checkout/legacy/\`.
- Payment methods come from \`GET /v1/billing/methods\`, which already returns \`sepa\` and \`invoice\`.

> Keep the old flow available for one week after release, then remove it in a follow-up.`;

export const SAMPLE_CRITERIA = `- [ ] Card, SEPA and Invoice all appear on the payment step
- [ ] Invoice appears **only** for annual plans
- [ ] The old flow still works behind \`checkout_v2=false\` for one week
- [ ] Each step fires its funnel event exactly once:
  \`checkout.plan_selected\`, \`checkout.payment_viewed\`, \`checkout.completed\``;

function MarkdownEditorDemo({ initial, ...props }: { readonly initial: string } & Omit<MarkdownEditorProps, "value" | "onChange">) {
  const [value, setValue] = useState(initial);
  return <MarkdownEditor minRows={6} maxLength={10_000} {...props} value={value} onChange={setValue} />;
}

/** The task dialog as the web app draws it: a document with where it sits beside it. */
function TaskDialogExample() {
  const [title, setTitle] = useState("Payment step keeps SEPA and Invoice");
  const [goal, setGoal] = useState(SAMPLE_GOAL);
  const [criteria, setCriteria] = useState(SAMPLE_CRITERIA);
  const [epic, setEpic] = useState("checkout");
  return (
    <Dialog
      trigger={<Button>Task dialog</Button>}
      size="document"
      context={<Breadcrumb size="sm" current={false} items={[{ id: "p", label: "Customer portal" }, { id: "e", label: "Checkout v2", icon: "layers" }]} />}
      title="New task"
      asideLabel="Where it sits"
      aside={
        <FormStack fill>
          <Select label="Epic" value={epic} onValueChange={setEpic} options={[{ value: "checkout", label: "Checkout v2" }, { value: "none", label: "No epic" }]} />
          <HelpList title="What makes a good task" items={[
            <><strong>Goal:</strong> why it matters, what exists today, and what an agent can't guess.</>,
            <><strong>Criteria:</strong> one checkable statement per list item.</>,
            <>Paste error output in a <code>```</code> block.</>,
          ]} />
          <MarkdownCheatsheet />
        </FormStack>
      }
      footerStart={<><KeyHint keys={["mod", "Enter"]}>create</KeyHint><KeyHint keys={["mod", "Shift", "P"]}>toggle preview</KeyHint></>}
      footer={
        <>
          <DialogClose asChild>
            <Button variant="quiet">Cancel</Button>
          </DialogClose>
          <Button variant="secondary">Create</Button>
          <Button variant="primary">Create and deliver</Button>
        </>
      }
    >
      <FormStack fill>
        <Input size="title" label="Title" labelNote="required" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="What should change?" />
        <MarkdownEditor label="Goal" hint="Why it matters, what exists today, and anything an agent can't guess." value={goal} onChange={setGoal} minRows={12} maxLength={10_000} />
        <MarkdownEditor label="Acceptance criteria" hint="One list item per criterion. Reviewers check each one." value={criteria} onChange={setCriteria} minRows={7}
          placeholder="- [ ] A thing that must be true when it's done" summary={<Badge tone="neutral" size="sm">4 criteria</Badge>} />
      </FormStack>
    </Dialog>
  );
}

function DiscardConfirmExample() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button onClick={() => setOpen(true)}>Discard confirm</Button>
      <DiscardConfirm
        open={open}
        title="Discard this task?"
        description="You have written 195 words that haven't been saved."
        onKeep={() => setOpen(false)}
        onDiscard={() => setOpen(false)}
      />
    </>
  );
}

import { useState } from "react";
import { PR_DISPLAY_STATES, type PrDisplayState } from "@dude/domain";
import { Block, Caption, Col, Label, Panes, Row, Section, States, type PaneMode } from "../Frame.tsx";
import { StatusMark } from "../../components/StatusMark.tsx";
import { PersonAvatar, PersonAvatarStack, type PersonAvatarSize } from "../../components/PersonAvatar.tsx";
import { ProjectAvatar } from "../../components/ProjectAvatar.tsx";
import { PR_DISPLAY_SPECS, PrChip, type PrChipPullRequest } from "../../components/PrChip.tsx";
import { Cost } from "../../components/Cost.tsx";
import { AgentPlan, PlanMeter, type PlanItem } from "../../components/AgentPlan.tsx";
import { MarkdownDocument } from "../../components/MarkdownDocument.tsx";
import { AgentAvatar } from "../../components/AgentAvatar.tsx";
import { ALL_STATUSES } from "../../tokens/status.ts";
import { Button } from "../../primitives/Button.tsx";

/** A stand-in photo: a drawn face on a gradient, so the gallery loads nothing. */
const PHOTO =
  "data:image/svg+xml;utf8," +
  encodeURIComponent(
    `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 40 40'><defs><linearGradient id='g' x1='0' y1='0' x2='1' y2='1'><stop offset='0' stop-color='#8a6d5a'/><stop offset='1' stop-color='#3c2f2a'/></linearGradient></defs><rect width='40' height='40' fill='url(#g)'/><circle cx='20' cy='16' r='7' fill='#e7c3a4'/><path d='M6 40c2-9 8-13 14-13s12 4 14 13z' fill='#e7c3a4'/></svg>`,
  );
const PROJECT_IMAGE =
  "data:image/svg+xml;utf8," +
  encodeURIComponent(`<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 40 40'><rect width='40' height='40' fill='#0d5b4a'/><rect x='11' y='8' width='18' height='24' rx='2' fill='#d2f4e7'/><path d='M15 15h10M15 20h10M15 25h6' stroke='#0d5b4a' stroke-width='2'/></svg>`);

const ANA = { id: "per_ana", name: "Ana Ribeiro", photoUrl: PHOTO, online: true };
const BO = { id: "per_bo", name: "Bo Lindqvist", online: true };
const CY = { id: "per_cy", name: "Cy Okafor", online: false };
const DEE = { id: "per_dee", name: "Dee Marsh" };
const ELI = { id: "per_eli", name: "Eli Park", online: true };

const SIZES: PersonAvatarSize[] = [16, 20, 24, 28, 32, 40, 56];

/** One pull request per display state, from the fields that make it so. */
const PR_BY_STATE: Record<PrDisplayState, Partial<PrChipPullRequest>> = {
  merged: { state: "merged" },
  closed: { state: "closed" },
  ci_red: { checks: [{ name: "e2e (chrome)", status: "completed", conclusion: "failure" }, { name: "unit", status: "completed", conclusion: "success" }], reviews: [{ login: "cy", state: "APPROVED" }] },
  ci_running: { checks: [{ name: "e2e (chrome)", status: "in_progress", conclusion: null }, { name: "unit", status: "completed", conclusion: "success" }] },
  awaiting: { review: "pending" },
  changes: { reviews: [{ login: "cy", state: "CHANGES_REQUESTED" }], unresolvedThreads: 3, behindBy: 3, mergeable: "behind" },
  conflict: { mergeable: "conflicting", reviews: [{ login: "bo", state: "APPROVED" }] },
  comments: { unresolvedThreads: 2, reviews: [{ login: "bo", state: "APPROVED" }] },
  ready: { mergeable: "clean", reviews: [{ login: "bo", state: "APPROVED" }] },
};
const pr = (state: PrDisplayState, n: number): PrChipPullRequest => ({
  number: n, url: `https://github.com/acme/dashboard/pull/${n}`, repositoryName: "acme/dashboard", baseBranch: "main",
  state: "open", checks: "passing", review: "approved", ...PR_BY_STATE[state],
});

const PLAN: PlanItem[] = [
  { content: "Find every visx import", status: "completed" },
  { content: "Replace the umbrella package with scale, shape and tooltip", status: "completed" },
  { content: "Keep fetchRevenue as a thin wrapper for billing", status: "in_progress" },
  { content: "Drop the old SVG helper import in Revenue.tsx", status: "pending" },
  { content: "Run bun test and bun run typecheck", status: "pending" },
  { content: "Pin @visx/* to one minor version", status: "cancelled" },
];

const PROMPT = `# Implementer

You take one task, change the repository so the task is done, and prove it with tests.

## The task

**Goal:** {{task.goal}}

## How to work

1. **Read before you write.** Find where the behaviour lives today.
2. Make the smallest change that fully does the task.
- Run \`bun test\` before you finish.

\`\`\`
## What changed
\`\`\`

> The reviewers will see your summary and your diff.`;

export function CalmerSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section
      id="calmer"
      title="Faces, states and costs"
      intro="What the calmer screens are made of: a status as a glyph and a word, three kinds of face, one chip per pull request, a cost that is a total, the agent's plan in one line, and a document you edit in place."
    >
      <Block id="k-status" title="StatusMark" note="A status is a glyph and a word in its tone: no box, no fill. Running breathes. The one filled pill on a screen is a state that waits on a person. StatusBadge draws this for its badge variant, so every existing status display changed with it.">
        <Panes mode={mode}>
          <Col>
            <Row>
              {ALL_STATUSES.map((s) => (
                <StatusMark key={s} status={s} />
              ))}
            </Row>
            <Label>Small, and icon only</Label>
            <Row>
              {ALL_STATUSES.map((s) => (
                <StatusMark key={s} status={s} size="sm" />
              ))}
            </Row>
            <Row>
              {ALL_STATUSES.map((s) => (
                <StatusMark key={s} status={s} iconOnly />
              ))}
            </Row>
          </Col>
        </Panes>
      </Block>

      <Block id="k-faces" title="PersonAvatar, ProjectAvatar" note="People are circles: a photo, or initials on their identity colour; a green ring means online. Projects are rounded squares: an image, or initials on theirs. Agents are rounder squares with their role's glyph. An agent working for a person sits on the person's face, pulsing while it works.">
        <Panes mode={mode}>
          <States
            items={[
              ["sizes 16–56", <>{SIZES.map((s) => <PersonAvatar key={s} person={BO} size={s} ring={false} />)}</>],
              ["photo · initials · online · offline", <>
                <PersonAvatar person={{ ...ANA, online: false }} size={40} />
                <PersonAvatar person={{ ...BO, online: false }} size={40} />
                <PersonAvatar person={ANA} size={40} />
                <PersonAvatar person={CY} size={40} />
              </>],
              ["agent working · agent idle", <>
                <PersonAvatar person={BO} size={56} agent="implementer" live />
                <PersonAvatar person={DEE} size={56} agent="reviewer" />
                <PersonAvatar person={ANA} size={32} agent="implementer" live />
                <PersonAvatar person={CY} size={20} agent="reviewer" live />
              </>],
              ["an agent", <><AgentAvatar role="implementer" size="lg" live /><AgentAvatar role="reviewer" size="md" /></>],
              ["stack, owner first", <PersonAvatarStack people={[ANA, CY]} size={28} agents={new Map([["per_ana", { role: "implementer", live: true }]])} />],
              ["stack past max", <PersonAvatarStack people={[ANA, BO, CY, DEE, ELI]} size={24} max={4} />],
              ["projects", <>
                {[16, 20, 32, 56].map((s) => <ProjectAvatar key={s} project={{ id: "prj_dash", name: "Dashboard" }} size={s} />)}
                <ProjectAvatar project={{ name: "Billing API", imageUrl: PROJECT_IMAGE }} size={56} />
                <ProjectAvatar project={{ name: "Greeter", colorSlot: 7 }} size={32} />
              </>],
            ]}
          />
        </Panes>
      </Block>

      <Block id="k-pr" title="PrChip" note="A task shows the first state that is true of its pull request, as an icon, a word and its number, linking to GitHub; the tooltip lists everything else that is true. The state comes from prDisplayState (in @dude/domain), from the fields a pull request has today and the richer ones when they arrive.">
        <Panes mode={mode}>
          <Col>
            <div style={{ display: "grid", gridTemplateColumns: "20px 200px 1fr", gap: "8px 14px", alignItems: "center" }}>
              {PR_DISPLAY_STATES.map((s, i) => (
                <FragmentRow key={s} cells={[<Caption>{i + 1}</Caption>, <PrChip pr={pr(s, 40 + i)} />, <Caption>{PR_DISPLAY_SPECS[s].description}</Caption>]} />
              ))}
            </div>
            <Label>Small, without the number, icon only</Label>
            <Row>
              <PrChip pr={pr("ci_red", 41)} size="sm" />
              <PrChip pr={pr("ready", 12)} showNumber={false} />
              {PR_DISPLAY_STATES.map((s, i) => (
                <PrChip key={s} pr={pr(s, 50 + i)} iconOnly size="sm" />
              ))}
            </Row>
          </Col>
        </Panes>
      </Block>

      <Block id="k-cost" title="Cost" note="Every cost is model tokens plus machine time: the number is the total, a 2px hairline under it is the split, and the tooltip has the parts. Until machine time is measured the total is tokens only and says so. Nothing reported is a dash, never $0.00.">
        <Panes mode={mode}>
          <States
            items={[
              ["tokens and machine", <><Cost tokensUsd={0.62} machineUsd={0.25} tokens={412_000} machineMs={23 * 60_000} /><Cost tokensUsd={9.8} machineUsd={4.4} size="lg" /></>],
              ["tokens only (machine not measured)", <><Cost tokensUsd={0.07} tokens={42_000} /><Cost tokensUsd={8.4} size="lg" /></>],
              ["small, muted", <Cost tokensUsd={0.21} machineUsd={0.03} size="sm" tone="muted" />],
              ["not reported", <Cost tokensUsd={null} />],
            ]}
          />
        </Panes>
      </Block>

      <Block id="k-plan" title="AgentPlan, PlanMeter" note="Pinned above the conversation, folded to one line: n of m, the step meter, the step it is on. Open, the whole list, the current step on the info tint. The meter alone goes on cards, pipeline rows and tree rows.">
        <Panes mode={mode} surface>
          <PlanDemo />
        </Panes>
      </Block>

      <Block id="k-document" title="MarkdownDocument" note="A document you read, then Edit: the same place becomes its source with light highlighting (headings, lists, code, variables), and Save or Cancel turns it back. No split view. Escape cancels; Ctrl/⌘+Enter saves.">
        <Panes mode={mode}>
          <DocumentDemo />
        </Panes>
      </Block>
    </Section>
  );
}

function FragmentRow({ cells }: { readonly cells: ReadonlyArray<React.ReactNode> }) {
  return <>{cells.map((c, i) => <span key={i} style={{ display: "flex", alignItems: "center" }}>{c}</span>)}</>;
}

function PlanDemo() {
  const [items, setItems] = useState(PLAN);
  const advance = () =>
    setItems((prev) => {
      const i = prev.findIndex((p) => p.status === "in_progress");
      if (i === -1) return PLAN;
      return prev.map((p, j) => (j === i ? { ...p, status: "completed" } : j === i + 1 && p.status === "pending" ? { ...p, status: "in_progress" } : p));
    });
  return (
    <Col>
      <AgentPlan items={items} defaultCollapsed sticky meta="updated 12s ago" />
      <AgentPlan items={items} meta="updated 12s ago" />
      <Row>
        <Button size="sm" onClick={advance}>Advance</Button>
        <Caption>meters:</Caption>
        <PlanMeter done={3} total={6} width={72} />
        <PlanMeter done={7} total={11} width={64} />
        <PlanMeter done={3} total={3} width={48} live={false} />
      </Row>
    </Col>
  );
}

function DocumentDemo() {
  const [source, setSource] = useState(PROMPT);
  return (
    <Col>
      <MarkdownDocument source={source} onSave={(s) => setSource(s)} meta="Last changed by Eli · yesterday" />
      <Label>Read-only, empty</Label>
      <MarkdownDocument source="" emptyText="Nothing added — this project uses the organisation's prompt as is." />
    </Col>
  );
}

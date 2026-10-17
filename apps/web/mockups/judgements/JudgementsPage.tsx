/*
 * Judgements, proposed: the tier dude asks when it needs one decision, no
 * agent and no tools, and every call it has made. Two places for it — a
 * section on the Models page (A), or a page of its own (B) — and the call
 * detail both open.
 */

import { useState, type ReactNode } from "react";
import {
  AgentAvatar,
  FlowSteps,
  SettingRow,
  SettingsHeader,
  SettingsMeta,
  SettingsNote,
  SettingsSection,
  SettingsDisclosure,
  TextButton,
  TierLine,
  TierMark,
  UsedBy,
} from "@dude/design-system/components";
import {
  Badge,
  Button,
  Callout,
  Dialog,
  FormStack,
  KeyValueList,
  RowMenu,
  Select,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
} from "@dude/design-system/primitives";
import type { IconName } from "@dude/design-system";
import { JUDGEMENTS, ORG, TEST_QUESTION, TIERS, type Judgement, type Tier, type Verdict } from "./data.ts";
import s from "./judgements.module.css";

export type Variant = "set" | "unset" | "empty" | "test";

type Tone = "info" | "success" | "attention" | "neutral";
export function tierMark(name: string): { icon: IconName; tone: Tone } {
  switch (name.toLowerCase()) {
    case "thinker": return { icon: "brain", tone: "info" };
    case "coder": return { icon: "agent", tone: "success" };
    case "fast": return { icon: "zap", tone: "attention" };
  }
  return { icon: "sparkle", tone: "neutral" };
}

const effortWord = (e: string | null) => (e ? e[0]!.toUpperCase() + e.slice(1) : "Model default");

const tiersFor = (variant: Variant): readonly Tier[] =>
  variant === "unset" ? TIERS.map((t) => (t.name === "Fast" ? { ...t, model: null } : t)) : TIERS;

const ago = (iso: string) => {
  const min = Math.round((Date.parse("2026-10-17T11:55:00Z") - Date.parse(iso)) / 60_000);
  return min < 60 ? `${min} min ago` : `${Math.floor(min / 60)} h ago`;
};

const VERDICT: Record<Verdict, { tone: "success" | "danger" | "attention"; icon: IconName; word: string }> = {
  yes: { tone: "success", icon: "check", word: "Yes" },
  no: { tone: "danger", icon: "cross", word: "No" },
  unsure: { tone: "attention", icon: "question", word: "Unsure" },
};

function VerdictBadge({ j }: { j: Judgement }) {
  if (j.error) return <Badge tone="danger" emphasis="subtle" icon="alert" size="sm">Failed</Badge>;
  const v = VERDICT[j.verdict!];
  return <Badge tone={v.tone} emphasis="tinted" icon={v.icon} size="sm">{v.word}</Badge>;
}

const pct = (c: number | null) => (c === null ? "—" : `${Math.round(c * 100)}%`);
const secs = (ms: number) => `${(ms / 1000).toFixed(1)}s`;
const usd = (n: number) => (n === 0 ? "—" : n < 0.01 ? `$${n.toFixed(4)}` : `$${n.toFixed(2)}`);

/** The tier field: which tier judgements ask, and what that requests. */
function JudgementTierRow({ variant, tier, onTier, onManage, onTest }: {
  variant: Variant;
  tier: string;
  onTier: (id: string) => void;
  onManage?: () => void;
  onTest: () => void;
}) {
  const tiers = tiersFor(variant);
  const t = tiers.find((x) => x.id === tier) ?? null;
  const options = [
    { value: "off", label: "Off — nothing is judged", description: "Every caller falls back to what it does today." },
    ...tiers.map((x) => {
      const m = tierMark(x.name);
      return { value: x.id, label: <span className="tierOption"><TierMark icon={m.icon} tone={m.tone} size="sm" />{x.name}</span>,
        meta: <span className="ds-mono">{x.model ?? "Not set"}</span>, description: x.description };
    }),
  ];
  return (
    <>
      <SettingRow label="Tier" htmlFor="judgement-tier"
        help="The tier dude asks when it needs one decision: one call to its model, no agent, no tools. Short questions, so a fast tier is usually right.">
        <FormStack>
          <Select id="judgement-tier" aria-label="Judgement tier" value={tier} onValueChange={onTier} options={options}
            footer={onManage ? <TextButton onClick={onManage}>Manage tiers in Models</TextButton> : "Only admins change tiers."} />
          {t?.model ? (
            <SettingsMeta>
              <span>Requests <code>{t.model}</code> · {effortWord(t.effort).toLowerCase()} effort · through the proxy’s <code>/v1/chat/completions</code></span>
            </SettingsMeta>
          ) : null}
          {t && !t.model ? (
            <Callout tone="attention">
              <b>{t.name}</b> names no model, so every judgement fails and its caller falls back. Give {t.name} a model, or pick another tier.
            </Callout>
          ) : null}
          {tier === "off" ? (
            <Callout tone="info">Judgements are off. Nothing asks the model; every caller does what it does today.</Callout>
          ) : null}
          {t?.model ? (
            <span><Button size="sm" variant="secondary" leadingIcon="send" onClick={onTest}>Ask a test question</Button></span>
          ) : null}
        </FormStack>
      </SettingRow>
    </>
  );
}

function TestResult() {
  return (
    <Callout tone="success" data-testid="judgement-test-result">
      <FormStack>
        <span><code>gpt-6-sol-mini</code> answered in 1.2s: <b>Yes</b>, 99% sure.</span>
        <span className={s["quote"]}>“{TEST_QUESTION}” — “Rayleigh scattering makes a clear daytime sky look blue.”</span>
        <SettingsMeta>Not recorded in Recent judgements; 412 tokens, $0.0003.</SettingsMeta>
      </FormStack>
    </Callout>
  );
}

function RecentJudgements({ variant, onOpen, compact }: { variant: Variant; onOpen: (j: Judgement) => void; compact?: boolean }) {
  const rows = variant === "empty" ? [] : JUDGEMENTS;
  const [asker, setAsker] = useState("all");
  const askers = ["all", ...new Set(JUDGEMENTS.map((j) => j.asker))];
  const shown = rows.filter((j) => asker === "all" || j.asker === asker);
  return (
    <SettingsSection title="Recent judgements"
      actions={rows.length ? (
        <>
          <SettingsMeta>Last 7 days · {rows.length} calls · {usd(rows.reduce((n, j) => n + j.usd, 0))}</SettingsMeta>
          <Select size="sm" aria-label="Asked by" value={asker} onValueChange={setAsker}
            options={askers.map((a) => ({ value: a, label: a === "all" ? "Everything" : a }))} />
        </>
      ) : null}>
      {rows.length === 0 ? (
        <div className={s["empty"]}>
          <span className={s["emptyTitle"]}>Nothing has asked for a judgement yet</span>
          <span>Every call is kept here for 30 days: what was asked, what the model answered, how long it took and what it cost.</span>
        </div>
      ) : (
        <Table>
          <THead>
            <Tr>
              <Th>Asked</Th>
              <Th>Answer</Th>
              <Th align="right">Sure</Th>
              {compact ? null : <Th hideWhenNarrow>By</Th>}
              <Th align="right" hideWhenNarrow>Took</Th>
              <Th align="right" hideWhenNarrow>Cost</Th>
              <Th align="right" hideWhenNarrow>When</Th>
            </Tr>
          </THead>
          <TBody>
            {shown.map((j) => (
              <Tr key={j.id} className={s["clickRow"]} onClick={() => onOpen(j)} tabIndex={0}
                onKeyDown={(e) => e.key === "Enter" && onOpen(j)}>
                <Td wrap>
                  <span className={s["asked"]}>
                    <span className={s["question"]}>{j.question}</span>
                    {j.about ? <span className={s["about"]}>{j.about}</span> : null}
                  </span>
                </Td>
                <Td fit><VerdictBadge j={j} /></Td>
                <Td fit align="right" mono muted={j.confidence === null}>{pct(j.confidence)}</Td>
                {compact ? null : <Td fit muted hideWhenNarrow>{j.asker}</Td>}
                <Td fit align="right" mono muted hideWhenNarrow>{secs(j.ms)}</Td>
                <Td fit align="right" mono muted hideWhenNarrow>{usd(j.usd)}</Td>
                <Td fit align="right" muted hideWhenNarrow>{ago(j.at)}</Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}
    </SettingsSection>
  );
}

export function JudgementDialog({ j, onClose }: { j: Judgement; onClose: () => void }) {
  const m = tierMark(j.tier);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="lg" title={j.question}
      description={`${j.asker}${j.about ? ` · ${j.about}` : ""} · ${ago(j.at)}`}
      footer={<Button variant="quiet" onClick={onClose}>Close</Button>}>
      <FormStack>
        {j.error ? (
          <Callout tone="danger"><b>No answer.</b> {j.error}. The caller fell back to what it does without a judgement.</Callout>
        ) : (
          <div className={s["answer"]}>
            <div className={s["answerHead"]}>
              <VerdictBadge j={j} />
              <span className={s["sure"]}>{pct(j.confidence)} sure</span>
            </div>
            <p className={s["reasoning"]}>{j.reasoning}</p>
          </div>
        )}
        <KeyValueList items={[
          { label: "Tier", value: <span className="tierOption"><TierMark icon={m.icon} tone={m.tone} size="sm" />{j.tier}</span> },
          { label: "Model", value: j.model, mono: true },
          { label: "Took", value: secs(j.ms), mono: true },
          { label: "Tokens", value: j.tokensIn ? `${j.tokensIn.toLocaleString("en")} in · ${j.tokensOut.toLocaleString("en")} out` : "—", mono: true },
          { label: "Cost", value: usd(j.usd), mono: true },
        ]} />
        <SettingsDisclosure summary="What was sent">
          <pre className={s["pre"]}>{j.prompt}</pre>
        </SettingsDisclosure>
        {j.raw ? (
          <SettingsDisclosure summary="What the model answered, as it came">
            <pre className={s["pre"]}>{j.raw}</pre>
          </SettingsDisclosure>
        ) : null}
      </FormStack>
    </Dialog>
  );
}

function HowJudgementsWork({ tier }: { tier: Tier | null }) {
  return (
    <FlowSteps steps={[
      { title: "Something needs a decision", children: <>Is this comment actionable? Does this question need a person?</> },
      { title: "dude asks the tier", children: <>{tier ? <code>{tier.name}</code> : "The judgement tier"} → <code>{tier?.model ?? "its model"}</code>, once, through the proxy, and reads a yes, no or unsure with how sure it is.</> },
      { title: "The caller decides", children: <>Below its own threshold, or on no answer, it does what it does today.</> },
    ]} />
  );
}

// ---------------------------------------------------------------------------
// A: a section on the Models page
// ---------------------------------------------------------------------------

function TierRows({ variant, judgementTier }: { variant: Variant; judgementTier: string }) {
  return (
    <Table>
      <THead>
        <Tr>
          <Th>Tier</Th>
          <Th>Requests</Th>
          <Th>Effort</Th>
          <Th hideWhenNarrow>Changed</Th>
          <Th hideWhenNarrow>Used by</Th>
          <Th align="right" width="48px"><span className="ds-sr-only">Actions</span></Th>
        </Tr>
      </THead>
      <TBody>
        {tiersFor(variant).map((t) => {
          const m = tierMark(t.name);
          const judges = t.id === judgementTier;
          const faces: ReactNode[] = t.roles.slice(0, 4).map((r) => <AgentAvatar key={r} role={r as "implementer"} size="xs" />);
          if (judges) faces.push(<span key="j" className={s["judgeFace"]}><TierMark icon="list-check" tone="neutral" size="sm" /></span>);
          const words = [t.roles.length ? `${t.roles.length} ${t.roles.length === 1 ? "agent" : "agents"}` : null, judges ? "Judgements" : null].filter(Boolean).join(" · ") || "Nobody";
          return (
            <Tr key={t.id}>
              <Td wrap><TierLine icon={m.icon} tone={m.tone} name={t.name} description={t.description} /></Td>
              {t.model ? <Td fit mono>{t.model}</Td> : <Td fit><span className="tierUnset">Not set</span></Td>}
              <Td fit muted={!t.effort}>{effortWord(t.effort)}</Td>
              <Td fit muted hideWhenNarrow>{t.changed}</Td>
              <Td wrap hideWhenNarrow><UsedBy faces={faces}>{words}</UsedBy></Td>
              <Td align="right">
                <RowMenu size="sm" label={`Actions for ${t.name}`} items={[{ id: "edit", label: "Change model…", icon: "edit", onSelect: () => undefined }]} />
              </Td>
            </Tr>
          );
        })}
      </TBody>
    </Table>
  );
}

export function ModelsWithJudgements({ variant, onVariant, detail }: { variant: Variant; onVariant: (v: Variant) => void; detail?: Judgement | null }) {
  const [tier, setTier] = useState("mtr_fast");
  const [open, setOpen] = useState<Judgement | null>(detail ?? null);
  return (
    <div className="settingsPage">
      <SettingsHeader title="Models"
        description="Agents are given a tier — Thinker, Coder or Fast — not a model. Each tier names the model dude asks the LLM proxy for; change it here and every agent on that tier follows from its next session." />
      <SettingsSection title="Tiers" actions={<>
        <SettingsMeta>Only admins change these</SettingsMeta>
        <Button size="sm" variant="secondary" leadingIcon="plus">Add tier</Button>
      </>}>
        <TierRows variant={variant} judgementTier={tier} />
      </SettingsSection>
      <SettingsSection title={<span className={s["newTitle"]}>Judgements <Badge tone="info" size="sm">New</Badge></span>}>
        <JudgementTierRow variant={variant} tier={tier} onTier={setTier} onTest={() => onVariant("test")} />
        {variant === "test" ? <TestResult /> : null}
      </SettingsSection>
      <RecentJudgements variant={variant} onOpen={setOpen} compact />
      {open ? <JudgementDialog j={open} onClose={() => setOpen(null)} /> : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// B: a page of its own
// ---------------------------------------------------------------------------

export function JudgementsPage({ variant, onVariant, onManage, detail }: { variant: Variant; onVariant: (v: Variant) => void; onManage: () => void; detail?: Judgement | null }) {
  const [tier, setTier] = useState("mtr_fast");
  const [open, setOpen] = useState<Judgement | null>(detail ?? null);
  const t = tiersFor(variant).find((x) => x.id === tier) ?? null;
  return (
    <div className="settingsPage">
      <SettingsHeader title="Judgements"
        description={`Some of dude’s decisions are better made by reading than by rule. For those, dude asks one model one question and gets a yes, no or unsure with how sure it is. ${ORG}’s admins pick which tier answers.`} />
      <HowJudgementsWork tier={t?.model ? t : null} />
      <SettingsSection title="Who answers">
        <JudgementTierRow variant={variant} tier={tier} onTier={setTier} onManage={onManage} onTest={() => onVariant("test")} />
        {variant === "test" ? <TestResult /> : null}
      </SettingsSection>
      <RecentJudgements variant={variant} onOpen={setOpen} />
      {open ? <JudgementDialog j={open} onClose={() => setOpen(null)} /> : null}
    </div>
  );
}

export function OrgNote() {
  return <SettingsNote icon="info">Organisation admins only change these. Every project starts from them; an admin can change them for one project in its own settings.</SettingsNote>;
}

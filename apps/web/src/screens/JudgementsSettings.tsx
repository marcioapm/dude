/**
 * Organisation settings → Judgements: which tier answers when dude needs
 * one decision (everyone reads; admins pick it and ask a test question),
 * and every judgement call it has made in the last 7 days.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import {
  Duration,
  EntityLine,
  FlowSteps,
  SettingRow,
  SettingsDisclosure,
  SettingsHeader,
  SettingsMeta,
  SettingsSection,
  TextButton,
  TierMark,
  TokenCount,
} from "@dude/design-system/components";
import { formatTimestamp } from "@dude/design-system";
import {
  Badge,
  Button,
  Callout,
  Dialog,
  EmptyState,
  FormStack,
  KeyValueList,
  Select,
  Spinner,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
} from "@dude/design-system/primitives";
import {
  JUDGEMENT_TEST_QUESTION,
  type JudgementCall,
  type JudgementCallDetail,
  type JudgementCallsResponse,
  type JudgementSettings,
  type JudgementTestResult,
  type ModelTier,
} from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { errorText, useSave } from "../hooks/useSave.tsx";
import { DECISION, errorSentence, saysNoAnswer, sure, testAnswerWords, testTokensWords } from "../judgements.ts";
import { tierEffortLabel, tierMark } from "../tiers.ts";
import { tierOption } from "./ModelsSettings.tsx";

/** The picker's value for Off (no tier). */
const OFF = "__off__";
/** The asker filter's value for every asker. */
const EVERYONE = "__all__";

/**
 * The organisation's judgement setting, loaded once by the settings screen
 * for the menu's "no model" note and the page. `reload` reads it again
 * after a tier's removal on the Models page, which may have moved it.
 */
export function useJudgementSettings(client: ApiClient) {
  const [settings, setSettings] = useState<JudgementSettings | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const reload = useCallback(() => {
    client.judgementSettings().then((s) => {
      setSettings(s);
      setProblem(null);
    }, (err: unknown) => setProblem(errorText(err)));
  }, [client]);
  useEffect(reload, [reload]);
  return { settings, setSettings, problem, reload };
}

const ago = (iso: string) => formatTimestamp(iso, "relative");

function DecisionBadge({ call }: { call: Pick<JudgementCall, "status" | "decision"> }) {
  if (call.status === "error" || !call.decision) return <Badge tone="danger" emphasis="subtle" icon="alert" size="sm">Failed</Badge>;
  const d = DECISION[call.decision];
  return <Badge tone={d.tone} emphasis="tinted" icon={d.icon} size="sm">{d.word}</Badge>;
}

export function JudgementsPage({ client, orgName, tiers, tiersProblem, judgement, onManageTiers, onChanged, onChat }: {
  client: ApiClient;
  orgName: string;
  tiers: readonly ModelTier[] | null;
  tiersProblem: string | null;
  judgement: ReturnType<typeof useJudgementSettings>;
  onManageTiers?: (() => void) | undefined;
  /** The setting changed: the Models page's Used by says so. */
  onChanged?: (() => void) | undefined;
  /** To the Chat settings, whose routing the tier also answers. */
  onChat?: (() => void) | undefined;
}) {
  const { settings, setSettings, problem } = judgement;
  if (problem || tiersProblem) return <div className="centered"><Callout tone="danger">{problem ?? `${orgName}’s tiers could not be loaded: ${tiersProblem}`}</Callout></div>;
  if (!settings || !tiers) return <div className="centered"><Spinner label="Loading…" /></div>;
  const tier = tiers.find((t) => t.id === settings.tierId) ?? null;
  return (
    <div className="settingsPage" data-testid="judgements-page">
      <SettingsHeader title="Judgements"
        description={`Some of dude’s decisions are better made by reading than by rule. For those, dude asks one model one question and gets a yes, no or unsure with how sure it is. ${orgName}’s admins pick which tier answers.`} />
      <FlowSteps data-testid="judgements-explainer" steps={[
        { title: "Something needs a decision", children: <>Is this comment actionable? Does this question need a person?</> },
        { title: "dude asks the tier", children: <>{tier?.model ? <code>{tier.name}</code> : "The judgement tier"} → <code>{tier?.model ?? "its model"}</code>, once, through the proxy, and reads a yes, no or unsure with how sure it is.</> },
        { title: "The caller decides", children: <>Below its own threshold, or on no answer, it does what it does today.</> },
      ]} />
      <WhoAnswers client={client} settings={settings} tiers={tiers} tier={tier} onManageTiers={onManageTiers} onChat={onChat}
        onSaved={(s) => {
          setSettings(s);
          onChanged?.();
        }} />
      <RecentJudgements client={client} />
    </div>
  );
}

function WhoAnswers({ client, settings, tiers, tier, onSaved, onManageTiers, onChat }: {
  client: ApiClient;
  settings: JudgementSettings;
  tiers: readonly ModelTier[];
  tier: ModelTier | null;
  onSaved: (s: JudgementSettings) => void;
  onManageTiers?: (() => void) | undefined;
  onChat?: (() => void) | undefined;
}) {
  const { save, problem } = useSave();
  const [test, setTest] = useState<{ tierId: string; result: JudgementTestResult } | { tierId: string; error: string } | "asking" | null>(null);
  const options = [
    { value: OFF, label: "Off — nothing is judged", description: "Every caller falls back to what it does today." },
    ...tiers.map(tierOption),
  ];
  const footer = onManageTiers ? <TextButton onClick={onManageTiers}>Manage tiers in Models</TextButton> : "Only admins change tiers.";
  const pick = (v: string) => {
    setTest(null);
    void save(async () => onSaved(await client.setJudgementTier(v === OFF ? null : v)), undefined,
      v === OFF ? "Judgements off" : "Judgement tier saved");
  };
  const ask = (t: ModelTier) => {
    setTest("asking");
    client.testJudgement(t.id).then((result) => setTest({ tierId: t.id, result }), (err: unknown) => setTest({ tierId: t.id, error: errorText(err) }));
  };
  const shown = test && test !== "asking" && test.tierId === tier?.id ? test : null;
  return (
    <SettingsSection title="Who answers">
      <SettingRow label="Tier" htmlFor="judgement-tier"
        help={<>The tier dude asks when it needs one decision: one call to its model, no agent, no tools. Short questions, so a fast tier is usually right.{onChat ? <> Also answers chat routing (<TextButton onClick={onChat} data-testid="judgements-chat-link">Chat settings</TextButton>).</> : null}</>}>
        <FormStack>
          <Select id="judgement-tier" aria-label="Judgement tier" data-testid="judgement-tier" disabled={!settings.canEdit}
            value={tier?.id ?? OFF} options={options} footer={footer} onValueChange={pick} />
          {tier?.model ? (
            <SettingsMeta>
              <span data-testid="judgement-tier-requests">
                Requests <code>{tier.model}</code> · {tierEffortLabel(tier.effort).toLowerCase()} effort · through the proxy’s <code>/v1/chat/completions</code>
              </span>
            </SettingsMeta>
          ) : null}
          {tier && !tier.model ? (
            <Callout tone="attention" data-testid="judgement-tier-no-model">
              <b>{tier.name}</b> names no model, so every judgement fails and its caller falls back. Give {tier.name} a model, or pick another tier.
            </Callout>
          ) : null}
          {!tier ? (
            <Callout tone="info" data-testid="judgements-off">Judgements are off. Nothing asks the model; every caller does what it does today.</Callout>
          ) : null}
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
          {settings.canEdit && tier?.model ? (
            <span>
              <Button size="sm" variant="secondary" leadingIcon="send" onClick={() => ask(tier)} disabled={test === "asking"} data-testid="judgement-test">
                {test === "asking" ? "Asking…" : "Ask a test question"}
              </Button>
            </span>
          ) : null}
        </FormStack>
      </SettingRow>
      {shown ? <TestOutcome test={shown} /> : null}
    </SettingsSection>
  );
}

function TestOutcome({ test }: { test: { result: JudgementTestResult } | { error: string } }) {
  if ("error" in test) {
    return (
      <Callout tone="danger" data-testid="judgement-test-result" data-ok={false}>
        <FormStack>
          <span>{errorSentence(test.error)}</span>
          <SettingsMeta>Not recorded in Recent judgements.</SettingsMeta>
        </FormStack>
      </Callout>
    );
  }
  const r = test.result;
  const ok = r.ok && r.decision !== null;
  return (
    <Callout tone={ok ? "success" : "danger"} data-testid="judgement-test-result" data-ok={ok}>
      <FormStack>
        <span><code>{r.model}</code>{testAnswerWords(r)}</span>
        {ok ? <span className="judgementQuote">“{JUDGEMENT_TEST_QUESTION}” — “{r.reasoning}”</span> : null}
        <SettingsMeta>Not recorded in Recent judgements{testTokensWords(r)}.</SettingsMeta>
      </FormStack>
    </Callout>
  );
}

/** What Recent judgements shows: the first page's summary and askers, and every page's calls. */
type Shown = Omit<JudgementCallsResponse, "summary" | "askers"> & {
  summary: NonNullable<JudgementCallsResponse["summary"]>;
  askers: string[];
};

function RecentJudgements({ client }: { client: ApiClient }) {
  const [asker, setAsker] = useState(EVERYONE);
  const [data, setData] = useState<Shown | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [open, setOpen] = useState<JudgementCall | null>(null);
  // Bumped whenever the filter changes: a response asked for under an earlier one is dropped.
  const generation = useRef(0);
  const load = useCallback((before?: string) => {
    const asked = generation.current;
    client.judgements({ asker: asker === EVERYONE ? undefined : asker, before }).then(
      (next) => asked === generation.current && (setProblem(null), setData((have) => (before && have
        ? { ...have, calls: [...have.calls, ...next.calls], next: next.next }
        : { ...next, summary: next.summary ?? { calls: next.calls.length, since: "" }, askers: next.askers ?? [] }))),
      (err: unknown) => asked === generation.current && setProblem(errorText(err)));
  }, [client, asker]);
  useEffect(() => {
    load();
    return () => {
      generation.current++;
    };
  }, [load]);

  let body;
  if (problem) body = <Callout tone="danger">{problem}</Callout>;
  else if (!data) body = <div className="centered"><Spinner label="Loading…" /></div>;
  else if (data.summary.calls === 0) {
    body = (
      <EmptyState icon="list-check" title="Nothing has asked for a judgement yet"
        description="Every call shows here: what was asked, what the model answered, how long it took and how many tokens it used." />
    );
  } else {
    body = (
      <FormStack>
        <Table data-testid="judgements-table">
          <THead>
            <Tr>
              <Th>Asked</Th>
              <Th>Answer</Th>
              <Th align="right">Sure</Th>
              <Th hideWhenNarrow>By</Th>
              <Th align="right" hideWhenNarrow>Took</Th>
              <Th align="right" hideWhenNarrow>Tokens</Th>
              <Th align="right" hideWhenNarrow>When</Th>
            </Tr>
          </THead>
          <TBody>
            {data.calls.map((c) => (
              <Tr key={c.id} interactive onClick={() => setOpen(c)} aria-label={`Open the judgement: ${c.question}`}
                onKeyDown={(e) => {
                  if (e.key !== "Enter" && e.key !== " ") return;
                  e.preventDefault();
                  setOpen(c);
                }}
                data-testid="judgement-row" data-call={c.id}>
                <Td wrap>
                  <EntityLine size="sm" wrap name={c.question} detail={c.about} />
                </Td>
                <Td fit><DecisionBadge call={c} /></Td>
                <Td fit align="right" mono muted={c.confidence === null}>{sure(c.confidence)}</Td>
                <Td fit muted hideWhenNarrow>{c.asker}</Td>
                <Td fit align="right" mono muted hideWhenNarrow><Duration ms={c.latencyMs} /></Td>
                <Td fit align="right" mono muted hideWhenNarrow>
                  {c.inputTokens + c.outputTokens ? <TokenCount tokens={c.inputTokens + c.outputTokens} /> : "—"}
                </Td>
                <Td fit align="right" muted hideWhenNarrow>{ago(c.createdAt)}</Td>
              </Tr>
            ))}
          </TBody>
        </Table>
        {data.next ? <span><Button size="sm" variant="quiet" onClick={() => load(data.next!)}>Show older</Button></span> : null}
      </FormStack>
    );
  }
  return (
    <SettingsSection title="Recent judgements" data-testid="recent-judgements"
      actions={data && data.summary.calls > 0 ? (
        <>
          <SettingsMeta>Last 7 days · {data.summary.calls} {data.summary.calls === 1 ? "call" : "calls"}</SettingsMeta>
          <Select size="sm" aria-label="Asked by" value={asker} onValueChange={setAsker}
            options={[{ value: EVERYONE, label: "Everything" }, ...data.askers.map((a) => ({ value: a, label: a }))]} />
        </>
      ) : null}>
      {body}
      {open ? <JudgementDialog client={client} call={open} onClose={() => setOpen(null)} /> : null}
    </SettingsSection>
  );
}

function JudgementDialog({ client, call, onClose }: { client: ApiClient; call: JudgementCall; onClose: () => void }) {
  const [detail, setDetail] = useState<JudgementCallDetail | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  useEffect(() => {
    client.judgement(call.id).then(setDetail, (err: unknown) => setProblem(errorText(err)));
  }, [client, call.id]);
  const mark = tierMark({ name: call.tierName });
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size="lg" title={call.question}
      description={`${call.asker}${call.about ? ` · ${call.about}` : ""} · ${ago(call.createdAt)}`}
      footer={<Button variant="quiet" onClick={onClose}>Close</Button>}>
      <FormStack>
        {call.status === "error" || !call.decision ? (
          <Callout tone="danger" data-testid="judgement-failed">
            {saysNoAnswer(call.error) ? <b>{errorSentence(call.error!)}</b> : <><b>No answer.</b>{call.error ? " " : ""}{errorSentence(call.error ?? "")}</>}
            {" "}The caller fell back to what it does without a judgement.
          </Callout>
        ) : (
          <div className="judgementAnswer" data-testid="judgement-answer">
            <div className="judgementAnswerHead">
              <DecisionBadge call={call} />
              <span className="judgementSure">{sure(call.confidence)} sure</span>
            </div>
            <p className="judgementReasoning">{call.reasoning}</p>
          </div>
        )}
        <KeyValueList items={[
          { label: "Tier", value: <span className="tierOption"><TierMark icon={mark.icon} tone={mark.tone} size="sm" />{call.tierName}</span> },
          { label: "Model", value: call.model, mono: true },
          { label: "Took", value: <Duration ms={call.latencyMs} />, mono: true },
          { label: "Tokens", mono: true, value: call.inputTokens || call.outputTokens
            ? <><TokenCount label="in" tokens={call.inputTokens} /> · <TokenCount label="out" tokens={call.outputTokens} /></>
            : "—" },
        ]} />
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
        {detail ? (
          <>
            <SettingsDisclosure summary="What was sent">
              <pre className="judgementText">{detail.request.messages.map((m) => `${m.role}:\n${m.content}`).join("\n\n")}</pre>
            </SettingsDisclosure>
            {detail.responseText ? (
              <SettingsDisclosure summary="What the model answered, as it came">
                <pre className="judgementText">{detail.responseText}</pre>
              </SettingsDisclosure>
            ) : null}
          </>
        ) : null}
      </FormStack>
    </Dialog>
  );
}

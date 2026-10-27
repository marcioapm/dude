/**
 * An incoming webhook on an automation's page: its trigger rows (endpoint,
 * verification, how to send, conditions, group by, rate limit), the
 * secret shown once, the Deliveries tab with a delivery's detail, Run now
 * on a payload, and an incoming run's trigger block. AutomationsSettings.tsx
 * places each; this module holds what only an incoming webhook has.
 */

import { useEffect, useState } from "react";
import {
  CodeEditor,
  CodeWell,
  ConditionRows,
  DeliveryVerdict,
  IncomingVerdictPanel,
  KeyShownOnce,
  Segmented,
  SettingField,
  SettingFields,
  SettingRow,
  SettingsDisclosure,
  SettingsNote,
  SettingsSection,
  TextButton,
  type DeliveryVerdictValue,
} from "@dude/design-system/components";
import { formatTimestamp } from "@dude/design-system";
import { Button, Callout, Checkbox, EmptyState, Fieldset, FormStack, Input, KeyValueList, NumberInput, Select, Spinner,
  Table, TBody, Td, Th, THead, Tr, Dialog } from "@dude/design-system/primitives";
import {
  conditionHolds,
  conditionMiss,
  DEFAULT_VERIFY_OF,
  describeCondition,
  INCOMING_MAX_BYTES,
  INCOMING_RATE_DEFAULT,
  INCOMING_RATE_MAX,
  INCOMING_VERIFY_LABEL,
  INCOMING_VERIFY_MODES,
  REFUSAL_CODES,
  type Automation,
  type AutomationDelivery,
  type AutomationDeliveryDetail,
  type AutomationRunDetail,
  type IncomingTrigger,
  type IncomingVerdict,
  type IncomingVerify,
  type IncomingVerifyMode,
} from "@dude/domain";
import type { ApiClient, DeliveryFilter } from "../api/client.ts";
import { errorText, useSave } from "../hooks/useSave.tsx";

const ago = (iso: string) => formatTimestamp(iso, "relative");

// A secret the API answered once (Create, a save that made it), held until
// the automation's page shows it: Create opens the page by id.
const shownOnce = new Map<string, string>();

/** Keep a just-made secret for the page that opens next. */
export function holdSecret(a: { id: string; incomingSecret?: string | undefined }): void {
  if (a.incomingSecret) shownOnce.set(a.id, a.incomingSecret);
}

/** The held secret of an automation, taken: it is shown once. */
export function takeSecret(id: string): string | null {
  const s = shownOnce.get(id) ?? null;
  shownOnce.delete(id);
  return s;
}

/** An automation's URL, as a sender POSTs it: at dude's public origin when known (where GitHub reaches it), else this page's. */
export function hookUrl(hookId: string, origin?: string | null): string {
  return `${(origin || window.location.origin).replace(/\/+$/, "")}/v1/hooks/${hookId}`;
}

const SAMPLE = `'{"status":"firing","groupKey":"example"}'`;

/** A curl that sends a test body by the verification's mode; `secret` stands in for the value. */
export function curlFor(url: string, v: IncomingVerify, secret = "$SECRET"): string {
  switch (v.mode) {
    case "signature":
      return [`BODY=${SAMPLE}`, `SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "${secret}" -hex | sed 's/^.* //')`,
        `curl -X POST ${url} \\`, "  -H 'Content-Type: application/json' \\", `  -H "${v.header}: ${v.prefix}$SIG" \\`, '  --data "$BODY"'].join("\n");
    case "header":
      return [`curl -X POST ${url} \\`, "  -H 'Content-Type: application/json' \\", `  -H "${v.header}: ${secret}" \\`, `  --data ${SAMPLE}`].join("\n");
    case "query":
      return [`curl -X POST "${url}?${v.param}=${secret}" \\`, "  -H 'Content-Type: application/json' \\", `  --data ${SAMPLE}`].join("\n");
  }
}

const copy = (text: string) => navigator.clipboard?.writeText(text).catch(() => undefined);

function HowToSend({ url, verify }: { url: string; verify: IncomingVerify }) {
  const [copied, setCopied] = useState(false);
  const curl = curlFor(url, verify);
  return (
    <div className="incomingHowTo" data-testid="how-to-send">
      <KeyValueList items={[
        { label: "POST to", value: verify.mode === "query" ? `${url}?${verify.param}=<secret>` : url, mono: true },
        { label: "Body", value: <>Any JSON object, up to 1 MiB, <span className="ds-mono">Content-Type: application/json</span></> },
        { label: verify.mode === "query" ? "Secret" : "Header", mono: true, value: verify.mode === "signature"
          ? `${verify.header}: ${verify.prefix}<hex HMAC-SHA256 of the raw body, keyed by the secret>`
          : verify.mode === "header" ? `${verify.header}: <the secret>` : `in the URL as ?${verify.param}=` },
        { label: "Answer", value: "202 at once; 401 bad secret, 403 turned off, 413 too large, 415 not JSON, 429 over the rate limit" },
      ]} />
      <CodeWell aria-label="curl example">{curl}</CodeWell>
      <TextButton onClick={() => { void copy(curl); setCopied(true); }}>{copied ? "Copied" : "Copy curl"}</TextButton>
    </div>
  );
}

const VERIFY_HELP: Record<IncomingVerifyMode, string> = {
  signature: "The sender signs the raw body with the secret (HMAC-SHA256) and puts it in this header.",
  header: "The sender puts the secret in this header.",
  query: "The secret is part of the URL.",
};

/** The Trigger section's incoming rows. */
export function IncomingTriggerRows({ trigger, hint, ro, onChange, onRotate, paths, groupError, origin }: {
  trigger: IncomingTrigger;
  hint: string | null;
  ro: boolean;
  onChange: (t: IncomingTrigger) => void;
  onRotate: () => void;
  /** The last delivery's keys, offered as condition paths. */
  paths?: readonly string[] | undefined;
  /** Group by's refusal after a save was tried. */
  groupError?: string | undefined;
  /** dude's public origin, when known. */
  origin?: string | null | undefined;
}) {
  const [copied, setCopied] = useState(false);
  const v = trigger.verify;
  const url = hookUrl(trigger.hookId, origin);
  const shown = v.mode === "query" ? `${url}?${v.param}=••••` : url;
  return (
    <>
      <SettingRow label="Endpoint" data-testid="incoming-endpoint"
        help={v.mode === "query" ? `Copy gives the URL up to ?${v.param}=; add the secret, which dude shows only once.` : "POST JSON here. It answers 202 at once."}>
        <span className="incomingEndpoint">
          <CodeWell inline data-testid="endpoint-url">{shown}</CodeWell>
          <Button size="sm" variant="secondary" leadingIcon="copy" type="button"
            aria-label={v.mode === "query" ? "Copy the URL, up to its token" : "Copy the URL"}
            onClick={() => { void copy(v.mode === "query" ? `${url}?${v.param}=` : url); setCopied(true); }}>{copied ? "Copied" : "Copy"}</Button>
        </span>
      </SettingRow>
      <SettingRow label="Verification" help={VERIFY_HELP[v.mode]} data-testid="incoming-verify">
        <div className="incomingVerify">
          <Segmented<IncomingVerifyMode> label="Verification" size="sm" value={v.mode} disabled={ro}
            onChange={(m) => onChange({ ...trigger, verify: DEFAULT_VERIFY_OF[m] })}
            options={INCOMING_VERIFY_MODES.map((m) => ({ value: m, label: INCOMING_VERIFY_LABEL[m] }))} />
          <SettingFields>
            {v.mode === "query" ? (
              <SettingField label="Parameter"><Input aria-label="Query parameter" mono size="sm" value={v.param} disabled={ro}
                onChange={(e) => onChange({ ...trigger, verify: { ...v, param: e.target.value } })} /></SettingField>
            ) : (
              <SettingField label="Header"><Input aria-label="Header name" mono size="sm" value={v.header} disabled={ro}
                onChange={(e) => onChange({ ...trigger, verify: { ...v, header: e.target.value } })} /></SettingField>
            )}
            {v.mode === "signature" ? (
              <SettingField label="Prefix"><Input aria-label="Signature prefix" mono size="sm" value={v.prefix} placeholder="none" disabled={ro}
                onChange={(e) => onChange({ ...trigger, verify: { ...v, prefix: e.target.value } })} /></SettingField>
            ) : null}
            <SettingField label="Secret">
              <span className="incomingSecret">
                <span className="ds-mono" data-testid="secret-hint">{hint ? `••••${hint}` : "None"}</span>
                {ro ? null : <Button size="sm" variant="secondary" leadingIcon="retry" type="button" data-testid="rotate" onClick={onRotate}>Rotate…</Button>}
              </span>
            </SettingField>
          </SettingFields>
        </div>
      </SettingRow>
      <SettingRow label="How to send" block>
        <SettingsDisclosure icon="send" full summary={`URL, ${v.mode === "query" ? "parameter" : "header"} and a curl example`}>
          <HowToSend url={url} verify={v} />
        </SettingsDisclosure>
      </SettingRow>
      <SettingRow label="Conditions" block data-testid="incoming-conditions"
        help="Paths into the JSON body; every row must hold, values in one row are any of them. Checked when no run of the delivery's group is going.">
        <ConditionRows value={trigger.conditions} disabled={ro} paths={paths}
          onChange={(conditions) => onChange({ ...trigger, conditions: conditions.map((c) => ({ ...c, values: [...c.values] })) })} />
      </SettingRow>
      <SettingRow label="Group by" help="Later deliveries with the same key go to the run that is still going." data-testid="incoming-group">
        <Input aria-label="Group by path" mono value={trigger.groupBy} placeholder="groupKey" disabled={ro} error={groupError}
          onChange={(e) => onChange({ ...trigger, groupBy: e.target.value.trim() })} />
      </SettingRow>
      <SettingRow label="Rate limit" help="Counts deliveries that started a run or were sent to one; skipped and refused ones do not. Deliveries over it are refused with 429 and listed as refused." data-testid="incoming-rate">
        <span className="incomingInline">
          <NumberInput step={1} aria-label="Deliveries per hour" min={1} max={INCOMING_RATE_MAX} value={trigger.ratePerHour} disabled={ro}
            onValueChange={(n) => onChange({ ...trigger, ratePerHour: n ?? INCOMING_RATE_DEFAULT })} />
          <span className="muted">per hour</span>
        </span>
      </SettingRow>
    </>
  );
}

/** The verification secret, shown this once after Create, a save that made it, or Rotate. */
export function SecretOnce({ secret, trigger, origin, rotated, onClose }: {
  secret: string; trigger: IncomingTrigger; origin?: string | null | undefined; rotated: boolean; onClose: () => void;
}) {
  return (
    <KeyShownOnce title={rotated ? "New verification secret" : "Verification secret"} label="Secret" doneLabel="I've stored it" secret={secret}
      description={rotated ? "The old secret stops working now. Copy this one: it is not shown again." : "Copy it now: it is not shown again."}
      usageHeading={`The sender sets · ${INCOMING_VERIFY_LABEL[trigger.verify.mode]}`}
      usage={curlFor(hookUrl(trigger.hookId, origin), trigger.verify, `${secret.slice(0, 6)}…`)}
      usageNote="dude keeps only its last 4 characters to show. Rotate makes a new one." onClose={onClose} />
  );
}

/** Rotate, confirmed: the old secret stops working at once. */
export function RotateDialog({ client, a, onClose, onRotated }: {
  client: ApiClient; a: Automation; onClose: () => void; onRotated: (a: Automation, secret: string) => void;
}) {
  const { busy, problem, save } = useSave();
  return (
    <Dialog open size="sm" onOpenChange={(o) => !o && onClose()} title="Rotate the verification secret?" footerProblem={problem}
      footer={<>
        <Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={busy} data-testid="rotate-confirm"
          onClick={() => void save(async () => { const r = await client.rotateIncomingSecret(a.id); onRotated(r, r.incomingSecret); })}>Rotate</Button>
      </>}>
      <p>The sender's secret stops working now; requests with it are refused until the sender has the new one.</p>
    </Dialog>
  );
}

/** An organisation incoming automation's projects: where create_task files, whose repositories it may check out. */
export function ProjectsRow({ projects, value, ro, error, onChange }: {
  projects: ReadonlyArray<{ id: string; name: string }>;
  value: readonly string[];
  ro: boolean;
  error: string | undefined;
  onChange: (ids: string[]) => void;
}) {
  return (
    <SettingRow label="Projects" help="Where create_task files its tasks; their repositories are the ones it may check out." data-testid="incoming-projects">
      <Fieldset legend="Projects it works in" error={error}>
        <div role="group" aria-label="Projects" className="automationActions">
          {projects.map((p) => (
            <Checkbox key={p.id} label={p.name} checked={value.includes(p.id)} disabled={ro}
              onCheckedChange={(on) => onChange(on ? [...value, p.id] : value.filter((x) => x !== p.id))} />
          ))}
        </div>
      </Fieldset>
    </SettingRow>
  );
}

// ---------------------------------------------------------------------------
// Deliveries
// ---------------------------------------------------------------------------

/** A delivery's verdict as the DS chip reads it. */
export function verdictOf(d: AutomationDelivery): DeliveryVerdictValue {
  switch (d.verdict) {
    case "started": return { kind: "started", runId: d.runId };
    case "sent_to_run": return { kind: "sent", runId: d.runId };
    case "skipped": return { kind: "skipped", because: d.reason ?? "" };
    case "refused": return { kind: "refused", reason: REFUSAL_CODES.find((c) => c === d.reason) ?? "bad_signature" };
    default: return { kind: "pending" };
  }
}

/** 778 B, 2.1 KiB, 1.3 MiB. */
export function sizeOf(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
}

const VERDICT_FILTER: Array<{ value: DeliveryFilter | "all"; label: string }> = [
  { value: "all", label: "Every delivery" }, { value: "started", label: "Started run" }, { value: "sent_to_run", label: "Sent to running agent" },
  { value: "skipped", label: "Skipped" }, { value: "refused", label: "Refused" },
];

/** The Deliveries tab: every request to the URL (or GitHub delivery of its event) in the last 30 days and what dude did. */
export function DeliveriesTab({ client, a, onOpen, onRun }: {
  client: ApiClient; a: Automation; onOpen: (id: string) => void; onRun: (runId: string) => void;
}) {
  const [list, setList] = useState<AutomationDelivery[] | null>(null);
  const [unstored, setUnstored] = useState(0);
  const [problem, setProblem] = useState<string | null>(null);
  const [verdict, setVerdict] = useState<DeliveryFilter | "all">("all");
  // Filtered by the server: a burst of refusals filling the newest 100 hides none of the rest.
  useEffect(() => {
    setList(null);
    client.automationDeliveryList(a.id, verdict === "all" ? undefined : verdict).then((r) => {
      setList(r.deliveries);
      setUnstored(r.refusedNotStored ?? 0);
    }, (e: unknown) => setProblem(errorText(e)));
  }, [client, a.id, verdict]);
  const incoming = a.trigger.kind === "incoming";
  const shown = list ?? [];
  return (
    <div data-testid="deliveries" className="incomingStack">
      <SettingsNote>Every request {incoming ? "to its URL" : "GitHub sent it"} in the last 30 days and what dude did with it. Refused requests keep no body.</SettingsNote>
      <span className="incomingFilter">
        <Select<DeliveryFilter | "all"> label="Verdict" size="sm" value={verdict} onValueChange={setVerdict} options={VERDICT_FILTER} />
      </span>
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {unstored > 0 && (verdict === "all" || verdict === "refused") ? (
        <div data-testid="refused-unstored"><SettingsNote icon="lock">
          {unstored} more refused this hour, not listed: past 100 refusals an hour dude counts them without keeping a row.
        </SettingsNote></div>
      ) : null}
      {!list ? <Spinner label="Loading…" /> : shown.length === 0 ? <EmptyState title="No deliveries in the last 30 days" /> : (
        <Table density="compact">
          <THead><Tr><Th width="14%">When</Th><Th>Verdict</Th><Th hideWhenNarrow>{incoming ? "Group key" : "Event"}</Th><Th hideWhenNarrow width={88} align="right">Size</Th></Tr></THead>
          <TBody>{shown.map((d) => (
            <Tr key={d.id} interactive onClick={() => onOpen(d.id)} data-testid="delivery-row" data-id={d.id}>
              <Td muted wrap>{ago(d.receivedAt)}</Td>
              <Td wrap><DeliveryVerdict verdict={verdictOf(d)} onRun={onRun} /></Td>
              <Td hideWhenNarrow wrap>{incoming
                ? d.groupKey ? <span className="incomingBreak ds-mono">{d.groupKey}</span> : <span className="muted">—</span>
                : <span className="ds-mono">{[d.event, d.action, d.repository].filter(Boolean).join(" · ")}</span>}</Td>
              <Td hideWhenNarrow muted align="right">{sizeOf(d.bytes)}</Td>
            </Tr>
          ))}</TBody>
        </Table>
      )}
    </div>
  );
}

/** One delivery: its verdict and ids, each condition's result, its headers (secrets as ••••) and its body. */
export function DeliveryDetail({ client, a, id, onRun, onRunWith }: {
  client: ApiClient; a: Automation; id: string; onRun: (runId: string) => void; onRunWith: () => void;
}) {
  const [d, setD] = useState<AutomationDeliveryDetail | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  useEffect(() => { client.automationDelivery(a.id, id).then(setD, (e: unknown) => setProblem(errorText(e))); }, [client, a.id, id]);
  if (!d) return problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />;
  const trigger = a.trigger.kind === "incoming" ? a.trigger : null;
  const json = d.body ? JSON.stringify(d.body, null, 2) : "";
  return (
    <div data-testid="delivery-detail" className="incomingSections">
      <SettingsSection title="Delivery"
        actions={d.body && (a.canEdit || a.canTransfer) ? <Button size="sm" variant="secondary" leadingIcon="play" onClick={onRunWith} data-testid="run-with-delivery">Run with this delivery</Button> : undefined}>
        <KeyValueList items={[
          { label: "Verdict", value: <DeliveryVerdict verdict={verdictOf(d)} onRun={onRun} /> },
          { label: "Received", value: formatTimestamp(d.receivedAt, "datetime") },
          { label: "Id", value: d.id, mono: true },
          ...(d.groupKey ? [{ label: "Group key", value: <span className="incomingBreak ds-mono">{d.groupKey}</span> }] : []),
          ...(d.event !== "incoming" ? [{ label: "Event", value: [d.event, d.action, d.repository].filter(Boolean).join(" · "), mono: true }] : []),
          { label: "Size", value: `${d.bytes.toLocaleString("en-GB")} bytes${d.bytes > INCOMING_MAX_BYTES ? " (over 1 MiB)" : ""}` },
        ]} />
      </SettingsSection>
      {trigger && d.body ? (
        <SettingsSection title="Conditions">
          {d.verdict === "sent_to_run" ? (
            <SettingsNote icon="send">Not checked: a run of its group was still going, so it was sent to that run.</SettingsNote>
          ) : trigger.conditions.length === 0 ? <SettingsNote>No conditions: every JSON delivery starts a run.</SettingsNote> : (
            <ConditionRows value={trigger.conditions} results={trigger.conditions.map((c) => conditionHolds(c, d.body))} />
          )}
        </SettingsSection>
      ) : null}
      {Object.keys(d.headers).length > 0 ? (
        <SettingsSection title="Headers">
          <KeyValueList items={Object.entries(d.headers).map(([label, value]) => ({ label, value, mono: true }))} />
          <SettingsNote icon="lock">Secrets are kept as ••••: dude stores neither a signature nor a token.</SettingsNote>
        </SettingsSection>
      ) : null}
      <SettingsSection title="Payload">
        {d.body ? (
          <CodeEditor aria-label="Payload" language="plain" readOnly value={json} minLines={6} maxLines={22}
            header={<span className="ds-mono">json · {d.bytes.toLocaleString("en-GB")} bytes</span>} />
        ) : <SettingsNote>{d.verdict === "refused" ? "Refused before it was read: no body is kept." : "Its body is kept 30 days."}</SettingsNote>}
      </SettingsSection>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Run now on a payload
// ---------------------------------------------------------------------------

type JsonCheck = { ok: true; value: unknown } | { ok: false; line: number; column: number; message: string };

/** The test dialog's verdict: valid JSON, its group, each condition, and what a real delivery would do. */
function VerdictBox({ trigger, verdict, body }: { trigger: IncomingTrigger; verdict: IncomingVerdict | null; body: Record<string, unknown> }) {
  const results = verdict?.results ?? trigger.conditions.map((c) => conditionHolds(c, body));
  const groupKey = verdict?.groupKey ?? "";
  const miss = trigger.conditions.findIndex((_, i) => !results[i]?.ok);
  const live = Boolean(verdict?.liveRunId);
  return (
    <IncomingVerdictPanel data-testid="test-verdict" ok={miss < 0}
      outcome={live ? undefined : miss < 0 ? <b>✓ Every condition holds: it would start a run.</b>
        : <><b>⊘ It would be skipped:</b> {conditionMiss(trigger.conditions[miss]!, results[miss]!)}.</>}>
      <p>✓ Valid JSON. Group key <code className="incomingBreak ds-mono">{groupKey || "(empty)"}</code>: {live ? "a run of this group is going." : "no run of this group is going."}</p>
      {live ? <p><b>As a real delivery it would be sent to the running agent;</b> conditions are not checked for it.</p>
        : trigger.conditions.length ? <ConditionRows value={trigger.conditions} results={results} /> : null}
    </IncomingVerdictPanel>
  );
}

/** Run now on an incoming webhook: a past delivery or a body written here, each condition's verdict shown live. */
export function RunNowIncomingDialog({ client, a, picked: initial, mode: initialMode, checkJson, onClose, onStarted }: {
  client: ApiClient;
  a: Automation;
  picked?: string | undefined;
  mode?: "past" | "write" | undefined;
  checkJson: (text: string) => JsonCheck;
  onClose: () => void;
  onStarted: (runId: string) => void;
}) {
  const trigger = a.trigger as IncomingTrigger;
  const [mode, setMode] = useState<"past" | "write">(initialMode ?? (initial ? "past" : "write"));
  const [list, setList] = useState<AutomationDelivery[] | null>(null);
  const [picked, setPicked] = useState<string | null>(initial ?? null);
  // The list carries no bodies: a picked delivery's is read from its detail.
  const [bodies, setBodies] = useState<Record<string, Record<string, unknown> | null>>({});
  const [text, setText] = useState("");
  const [verdict, setVerdict] = useState<IncomingVerdict | null>(null);
  const { busy, problem, save } = useSave();
  const bodyOf = (id: string) => client.automationDelivery(a.id, id).then((d) => {
    setBodies((b) => ({ ...b, [id]: d.body }));
    return d.body;
  });
  useEffect(() => {
    client.automationDeliveryList(a.id, "accepted").then((r) => {
      setList(r.deliveries);
      const last = r.deliveries[0];
      if (!last) { setText((t) => t || JSON.stringify({ status: "firing", groupKey: "example" }, null, 2)); return; }
      void bodyOf(last.id).then((b) => setText((t) => t || JSON.stringify(b ?? {}, null, 2)), () => undefined);
    }, () => setList([]));
  }, [client, a.id]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (picked && !(picked in bodies)) void bodyOf(picked).catch(() => setBodies((b) => ({ ...b, [picked]: null })));
  }, [picked]); // eslint-disable-line react-hooks/exhaustive-deps
  const json = checkJson(text);
  const written = json.ok && json.value && typeof json.value === "object" && !Array.isArray(json.value) ? json.value as Record<string, unknown> : null;
  const body = mode === "past" ? (picked ? bodies[picked] ?? null : null) : written;
  useEffect(() => {
    if (!body) { setVerdict(null); return; }
    const t = setTimeout(() => client.evaluateIncoming(a.id, body).then(setVerdict, () => setVerdict(null)), 250);
    return () => clearTimeout(t);
  }, [client, a.id, JSON.stringify(body)]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Run now on a payload" size="lg" footerProblem={problem}
      footer={<>
        <Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={busy || !body} data-testid="test-run"
          onClick={() => void save(async () => { const result = await client.runAutomation(a.id, mode === "past" ? { deliveryId: picked! } : { payload: written! }); onStarted(result.runId); }, () => undefined, "Run started")}>
          {mode === "past" ? "Run on this delivery" : "Run with this payload"}
        </Button>
      </>}>
      <FormStack>
        <Segmented<"past" | "write"> label="Payload" value={mode} onChange={setMode} data-testid="test-mode"
          options={[{ value: "past", label: "A past delivery" }, { value: "write", label: "Write a payload" }]} />
        {mode === "past" ? (
          !list ? <Spinner label="Loading…" /> : list.length === 0 ? <EmptyState title="No deliveries with a body in the last 30 days" /> : (
            <Table density="compact" data-testid="test-delivery">
              <THead><Tr><Th>Delivery</Th><Th>Verdict</Th><Th hideWhenNarrow>When</Th></Tr></THead>
              <TBody>{list.map((d) => (
                <Tr key={d.id} interactive selected={picked === d.id} onClick={() => setPicked(d.id)}>
                  <Td mono>{d.id}</Td><Td wrap><DeliveryVerdict verdict={verdictOf(d)} detail={false} /></Td><Td hideWhenNarrow muted>{ago(d.receivedAt)}</Td>
                </Tr>
              ))}</TBody>
            </Table>
          )
        ) : (
          <CodeEditor aria-label="Payload" language="plain" value={text} onChange={setText} data-testid="test-editor" minLines={10} maxLines={16}
            diagnostics={json.ok ? (written ? [] : [{ severity: "error", line: 1, from: 0, to: 1, message: "The body must be a JSON object." }])
              : [{ severity: "error", line: json.line, from: Math.max(0, json.column - 1), to: json.column, message: json.message }]}
            header={<span className="ds-mono">json · the request body</span>}
            footer={<span className="automationEditorFoot">
              <span>{text.split("\n").length} lines</span>
              <TextButton onClick={() => setText(JSON.stringify((list?.[0] && bodies[list[0].id]) ?? {}, null, 2))}>Reset to the last delivery</TextButton>
            </span>} />
        )}
        {body ? <VerdictBox trigger={trigger} verdict={verdict} body={body} />
          : <p className="muted" role="status">{mode === "past" ? "Pick a delivery to see what it would do."
            : json.ok ? "× The body must be a JSON object." : `× Not valid JSON, line ${json.line}, column ${json.column}: ${json.message}`}</p>}
        <SettingsNote>A test run starts its own run, with a fresh session, whatever is running or skipped. It is a real run: it can comment and create tasks.</SettingsNote>
      </FormStack>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// A run an incoming delivery started
// ---------------------------------------------------------------------------

/**
 * What started an incoming run, as one line of its Run section: the
 * delivery's id (opening it), when it was received, the conditions that
 * held, and how many later deliveries were sent to the run (each is in the
 * activity where it arrived); its group key and payload folded below.
 */
export function IncomingTriggerLine({ a, run, onDelivery }: { a: Automation; run: AutomationRunDetail; onDelivery: (id: string) => void }) {
  const trigger = a.trigger.kind === "incoming" ? a.trigger : null;
  const [first, ...later] = run.deliveries ?? [];
  if (!trigger || !first) return null;
  const held = trigger.conditions.length ? ` · ${trigger.conditions.map(describeCondition).join(" · ")}: ${trigger.conditions.length > 1 ? "all held" : "it held"}` : "";
  const body = run.payloads[0]?.body;
  return (
    <>
      <span className="automationTriggerLine" data-testid="run-trigger">
        Incoming <TextButton onClick={() => onDelivery(first.id)}><span className="ds-mono">{first.id}</span></TextButton>
        <span className="ds-hint"> · received {formatTimestamp(first.receivedAt, "time")}{held}</span>
        {later.length > 0 ? <span className="ds-hint" data-testid="sent-to-run-count"> · {later.length} later {later.length === 1 ? "delivery" : "deliveries"} sent to this run</span> : null}
      </span>
      {first.groupKey || body !== undefined ? (
        <SettingsDisclosure icon="braces" summary={<>Group key and payload · <span className="ds-mono">{first.id}</span></>}>
          {first.groupKey ? <p className="incomingBreak ds-mono">{first.groupKey}</p> : null}
          {body !== undefined ? <pre className="automationPayload">{JSON.stringify(body, null, 2)}</pre> : null}
        </SettingsDisclosure>
      ) : null}
    </>
  );
}

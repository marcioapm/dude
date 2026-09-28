/**
 * Settings → GitHub, below the connection: whether GitHub's webhooks reach
 * dude (and registering them, or the secret to do it by hand), and how dude
 * behaves on GitHub — how pull requests open and merge, who may wake a
 * fixer, what happens when main moves ahead, and the fix budget.
 */

import { useEffect, useState } from "react";
import { formatTimestamp } from "@dude/design-system";
import { Segmented, SettingRow, SettingsSection } from "@dude/design-system/components";
import { Badge, Button, Callout, Input, Select, Spinner } from "@dude/design-system/primitives";
import type { ApiClient, GithubSettings, WebhookHealth } from "../api/client.ts";
import { errorText, useSave } from "../hooks/useSave.tsx";
import { parseLogins } from "../pullRequests.ts";

const ago = (iso: string) => formatTimestamp(iso, "relative");

/** Webhook health in one line, as the mockup's: healthy, or what is wrong. */
export function webhookSummary(h: WebhookHealth): { ok: boolean; text: string } {
  const registered = h.repositories.filter((r) => r.registeredAt && !r.error).length;
  const parts = [`${registered} of ${h.repositories.length} repositor${h.repositories.length === 1 ? "y" : "ies"} registered`];
  parts.push(h.lastDeliveryAt ? `last delivery ${ago(h.lastDeliveryAt)}` : "nothing delivered yet");
  parts.push(`${h.failedToday} failed today`);
  const ok = registered === h.repositories.length && h.lastDeliveryAt !== null && h.failedToday === 0 && h.retrying === 0;
  return { ok, text: parts.join(" · ") };
}

export function WebhookCard({ client, health, onChanged, admin }: { client: ApiClient; health: WebhookHealth; onChanged: () => void; admin: boolean }) {
  const { busy, problem, save } = useSave();
  const [secret, setSecret] = useState<string | null>(null);
  const summary = webhookSummary(health);
  const origin = health.publicUrl ?? window.location.origin;
  return (
    <SettingsSection title="Webhooks" data-testid="webhooks"
      actions={<Badge tone={summary.ok ? "success" : "attention"} icon={summary.ok ? "check" : "warning"}>{summary.ok ? "Healthy" : "Needs a look"}</Badge>}>
      <Callout tone={summary.ok ? "success" : "neutral"} data-testid="webhook-summary">{summary.text}</Callout>
      {health.lastFailure ? (
        <Callout tone="danger">Last failure{health.lastFailureAt ? ` ${ago(health.lastFailureAt)}` : ""}: {health.lastFailure}</Callout>
      ) : null}
      {health.retrying > 0 ? <Callout tone="attention">{health.retrying} deliveries are being retried: {health.lastError}</Callout> : null}
      <ul className="webhookRepos">
        {health.repositories.map((r) => (
          <li key={r.id} data-testid="webhook-repo">
            <Badge size="sm" tone={r.error ? "danger" : r.registeredAt ? "success" : "neutral"} icon={r.error ? "cross" : r.registeredAt ? "check" : "circle"}>
              {r.error ? "Failed" : r.registeredAt ? "Registered" : "Not registered"}
            </Badge>
            <span className="ds-mono">{r.projectName} / {r.name}</span>
            {r.error ? <span className="muted">{r.error}</span> : null}
          </li>
        ))}
      </ul>
      {secret ? <Input label="Webhook secret" mono readOnly value={secret} data-testid="webhook-secret"
        hint="For a hook added by hand on GitHub: content type application/json." /> : null}
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {admin ? <div className="webhookActions">
        <Button variant="secondary" disabled={busy} data-testid="webhook-register"
          onClick={() => void save(() => client.registerWebhooks(origin), onChanged, "Webhooks registered")}>
          Register on every repository
        </Button>
        <Button variant="quiet" disabled={busy} onClick={() => void save(async () => setSecret((await client.revealWebhookSecret()).secret))}>
          Show secret
        </Button>
        <Button variant="quiet" disabled={busy} data-testid="webhook-rotate"
          onClick={() => void save(async () => setSecret((await client.rotateWebhookSecret(origin)).secret), onChanged, "Secret rotated; the old one works for a day")}>
          Rotate secret
        </Button>
      </div> : null}
    </SettingsSection>
  );
}

/** How dude behaves on GitHub; a member sees it, only an admin changes it. */
export function GithubBehaviour({ client, admin }: { client: ApiClient; admin: boolean }) {
  const [saved, setSaved] = useState<GithubSettings | null>(null);
  const [draft, setDraft] = useState<GithubSettings | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const { busy, problem: saveProblem, save } = useSave();

  useEffect(() => {
    void client.githubSettings().then((s) => {
      setSaved(s);
      setDraft(s);
    }, (err: unknown) => setProblem(errorText(err)));
  }, [client]);

  if (!draft || !saved) return problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />;
  const set = <K extends keyof GithubSettings>(k: K, v: GithubSettings[K]) => setDraft({ ...draft, [k]: v });
  const changed = (Object.keys(draft) as Array<keyof GithubSettings>).filter((k) => JSON.stringify(draft[k]) !== JSON.stringify(saved[k]));

  return (
    <form data-testid="github-behaviour" className="settingsForm" onSubmit={(e) => {
      e.preventDefault();
      void save(async () => {
        const next = await client.updateGithubSettings(Object.fromEntries(changed.map((k) => [k, draft[k]])));
        setSaved(next);
        setDraft(next);
      }, undefined, "GitHub settings saved");
    }}>
      <fieldset disabled={!admin} className="plainFieldset">
      <SettingsSection title="Pull requests">
        <SettingRow label="Open pull requests as">
          <Segmented label="Open pull requests as" value={draft.openAs} onChange={(v) => set("openAs", v)}
            options={[{ value: "ready", label: "Ready for review" }, { value: "draft", label: "Draft" }]} />
        </SettingRow>
        <SettingRow label="Request review from" help={draft.requestReviewFrom === "codeowners" ? "GitHub asks the code owners itself." : undefined}>
          <Segmented label="Request review from" value={draft.requestReviewFrom} onChange={(v) => set("requestReviewFrom", v)}
            options={[{ value: "nobody", label: "Nobody" }, { value: "codeowners", label: "CODEOWNERS" }, { value: "logins", label: "These people" }]} />
          {draft.requestReviewFrom === "logins" ? (
            <Input aria-label="GitHub logins" placeholder="GitHub logins, comma-separated" value={draft.reviewLogins.join(", ")}
              onChange={(e) => set("reviewLogins", parseLogins(e.target.value))} />
          ) : null}
        </SettingRow>
        <SettingRow label="Merge method" help="What the Merge button in dude does first.">
          <Segmented label="Merge method" value={draft.mergeMethod} onChange={(v) => set("mergeMethod", v)} data-testid="setting-merge-method"
            options={[{ value: "squash", label: "Squash" }, { value: "merge", label: "Merge commit" }, { value: "rebase", label: "Rebase" }]} />
        </SettingRow>
      </SettingsSection>
      <SettingsSection title="Reacting to GitHub">
        <SettingRow label="Who can wake a fixer" help="A comment from anyone else is shown on the task, not acted on.">
          <Select disabled={!admin} aria-label="Who can wake a fixer" value={draft.whoCanWake} onValueChange={(v) => set("whoCanWake", v)}
            options={[{ value: "collaborators", label: "Collaborators with write access" },
              { value: "members", label: "Organisation members only" }, { value: "anyone", label: "Anyone who can comment" }]} />
        </SettingRow>
        <SettingRow label="When main moves ahead">
          <Select disabled={!admin} aria-label="When main moves ahead" value={draft.whenBehind} onValueChange={(v) => set("whenBehind", v)}
            options={[{ value: "update", label: "Update the branch if it merges cleanly, else ask" },
              { value: "tell", label: "Only tell the task's people" }]} />
        </SettingRow>
        <SettingRow label="Fix rounds per pull request" help="Then the owner decides how it goes on. 0 for no limit.">
          <Input aria-label="Fix rounds per pull request" type="number" min={0} max={50} value={draft.fixRoundsPerPr}
            onChange={(e) => set("fixRoundsPerPr", Number(e.target.value))} data-testid="setting-fix-rounds" />
        </SettingRow>
        <SettingRow label="Ask when CI is stuck after" help="Minutes checks may stay pending on one commit.">
          <Input aria-label="Minutes before CI is stuck" type="number" min={1} value={draft.ciStuckMinutes}
            onChange={(e) => set("ciStuckMinutes", Number(e.target.value))} />
        </SettingRow>
      </SettingsSection>
      </fieldset>
      {saveProblem ? <Callout tone="danger">{saveProblem}</Callout> : null}
      {admin ? (
        <div>
          <Button type="submit" variant="primary" disabled={busy || changed.length === 0} data-testid="github-save">Save</Button>
        </div>
      ) : null}
    </form>
  );
}

/**
 * Settings → GitHub, below the connection: whether GitHub's webhooks reach
 * dude (and registering them, or the secret to do it by hand), and how dude
 * behaves on GitHub — how pull requests open and merge, who may wake a
 * fixer, what happens when main moves ahead, and the fix budget.
 */

import { useEffect, useState, type ReactNode } from "react";
import { formatDuration } from "@dude/design-system";
import { Badge, Button, Callout, Card, CardBody, CardFooter, CardHeader, Input, Select, Spinner } from "@dude/design-system/primitives";
import type { ApiClient, GithubSettings, WebhookHealth } from "../api/client.ts";
import { errorText, useSave } from "../hooks/useSave.tsx";

const ago = (iso: string) => `${formatDuration(Math.max(0, Date.now() - Date.parse(iso)))} ago`;

/** Webhook health in one line, as the mockup's: healthy, or what is wrong. */
export function webhookSummary(h: WebhookHealth): { ok: boolean; text: string } {
  const registered = h.repositories.filter((r) => r.registeredAt && !r.error).length;
  const parts = [`${registered} of ${h.repositories.length} repositor${h.repositories.length === 1 ? "y" : "ies"} registered`];
  parts.push(h.lastDeliveryAt ? `last delivery ${ago(h.lastDeliveryAt)}` : "nothing delivered yet");
  parts.push(`${h.failedToday} failed today`);
  const ok = registered === h.repositories.length && h.lastDeliveryAt !== null && h.failedToday === 0 && h.retrying === 0;
  return { ok, text: parts.join(" · ") };
}

export function WebhookCard({ client, health, onChanged }: { client: ApiClient; health: WebhookHealth; onChanged: () => void }) {
  const { busy, problem, save } = useSave();
  const [secret, setSecret] = useState<string | null>(null);
  const summary = webhookSummary(health);
  const origin = health.publicUrl ?? window.location.origin;
  return (
    <Card data-testid="webhooks">
      <CardHeader title="Webhooks" actions={<Badge tone={summary.ok ? "success" : "attention"} icon={summary.ok ? "check" : "warning"}>{summary.ok ? "Healthy" : "Needs a look"}</Badge>} />
      <CardBody>
        <p className="webhookHealth" data-testid="webhook-summary">{summary.text}</p>
        {health.lastFailure ? (
          <Callout tone="danger">Last failure {health.lastFailureAt ? ago(health.lastFailureAt) : ""}: {health.lastFailure}</Callout>
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
      </CardBody>
      <CardFooter>
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
      </CardFooter>
    </Card>
  );
}

function Row({ label, hint, children }: { label: string; hint?: string | undefined; children: ReactNode }) {
  return (
    <div className="settingsRow">
      <div>
        {label}
        {hint ? <small>{hint}</small> : null}
      </div>
      <div>{children}</div>
    </div>
  );
}

export function GithubBehaviour({ client }: { client: ApiClient }) {
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
    <Card data-testid="github-behaviour">
      <CardHeader title="On GitHub" />
      <CardBody>
        <div className="settingsRows">
          <Row label="Open pull requests as">
            <Select aria-label="Open pull requests as" value={draft.openAs} onValueChange={(v) => set("openAs", v)}
              options={[{ value: "ready", label: "Ready for review" }, { value: "draft", label: "Draft" }]} />
          </Row>
          <Row label="Request review from" hint={draft.requestReviewFrom === "codeowners" ? "GitHub asks the code owners itself." : undefined}>
            <Select aria-label="Request review from" value={draft.requestReviewFrom} onValueChange={(v) => set("requestReviewFrom", v)}
              options={[{ value: "nobody", label: "Nobody" }, { value: "codeowners", label: "CODEOWNERS" }, { value: "logins", label: "These people" }]} />
            {draft.requestReviewFrom === "logins" ? (
              <Input aria-label="GitHub logins" placeholder="GitHub logins, comma-separated" value={draft.reviewLogins.join(", ")}
                onChange={(e) => set("reviewLogins", e.target.value.split(/[\s,]+/).map((l) => l.replace(/^@/, "")).filter(Boolean))} />
            ) : null}
          </Row>
          <Row label="Merge method" hint="What the Merge button in dude does first.">
            <Select aria-label="Merge method" value={draft.mergeMethod} onValueChange={(v) => set("mergeMethod", v)}
              options={[{ value: "squash", label: "Squash" }, { value: "merge", label: "Merge commit" }, { value: "rebase", label: "Rebase" }]} />
          </Row>
          <Row label="Who can wake a fixer" hint="A comment from anyone else is shown on the task, not acted on.">
            <Select aria-label="Who can wake a fixer" value={draft.whoCanWake} onValueChange={(v) => set("whoCanWake", v)}
              options={[{ value: "collaborators", label: "Collaborators with write access" },
                { value: "members", label: "Organisation members" }, { value: "anyone", label: "Anyone who can comment" }]} />
          </Row>
          <Row label="When main moves ahead">
            <Select aria-label="When main moves ahead" value={draft.whenBehind} onValueChange={(v) => set("whenBehind", v)}
              options={[{ value: "update", label: "Update the branch if it merges cleanly, else ask" },
                { value: "tell", label: "Only tell the task's people" }]} />
          </Row>
          <Row label="Fix rounds per pull request" hint="Then the owner decides how it goes on. 0 for no limit.">
            <Input aria-label="Fix rounds per pull request" type="number" min={0} max={50} value={draft.fixRoundsPerPr}
              onChange={(e) => set("fixRoundsPerPr", Number(e.target.value))} data-testid="setting-fix-rounds" />
          </Row>
          <Row label="Ask when CI is stuck after" hint="Minutes checks may stay pending on one commit.">
            <Input aria-label="Minutes before CI is stuck" type="number" min={1} value={draft.ciStuckMinutes}
              onChange={(e) => set("ciStuckMinutes", Number(e.target.value))} />
          </Row>
        </div>
        {saveProblem ? <Callout tone="danger">{saveProblem}</Callout> : null}
      </CardBody>
      <CardFooter>
        <Button variant="primary" disabled={busy || changed.length === 0} data-testid="github-save"
          onClick={() => void save(async () => {
            const next = await client.updateGithubSettings(Object.fromEntries(changed.map((k) => [k, draft[k]])));
            setSaved(next);
            setDraft(next);
          }, undefined, "GitHub settings saved")}>
          Save
        </Button>
      </CardFooter>
    </Card>
  );
}

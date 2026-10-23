import { useState } from "react";
import { Block, Col, Label, Panes, Section, type PaneMode } from "../Frame.tsx";
import {
  ChipsInput,
  DayToggles,
  KindMark,
  ListSwitch,
  NextRuns,
  OwnerMark,
  RepoFilterField,
  SchedulePicker,
  ScopeChip,
  SessionMark,
  TriggerSummary,
  type RepoFilterValue,
} from "../../components/Automations.tsx";
import { SettingRow } from "../../components/Settings.tsx";

/*
 * Automations' pieces with made-up names: Acme's projects and people. The
 * next runs are fixed times; the app gets them from the orchestrator.
 */

const GROUPS = [
  { project: "web", repos: ["acme/web", "acme/web-e2e", "acme/docs-site"] },
  { project: "billing", repos: ["acme/svc-billing", "acme/svc-invoices", "acme/billing-ui"] },
  { project: "platform", repos: ["acme/svc-auth", "acme/svc-gateway", "acme/sandbox-1", "acme/sandbox-2", "acme/notebooks", "acme/runbooks"] },
];
const ANA = { id: "per_ana", name: "Ana Ribeiro" };
const ZONES = ["UTC", "Europe/Lisbon", "Europe/Berlin", "America/New_York"];
const NEXT = [new Date("2026-10-24T02:00:00Z"), new Date("2026-10-25T03:00:00Z"), new Date("2026-10-26T03:00:00Z")];

function RepoFilterDemo({ initial, disabled }: { readonly initial: RepoFilterValue; readonly disabled?: boolean }) {
  const [v, setV] = useState(initial);
  return <RepoFilterField value={v} onChange={setV} org="Acme" groups={GROUPS} disabled={disabled} />;
}

function ScheduleDemo({ initial }: { readonly initial: string }) {
  const [v, setV] = useState({ cron: initial, tz: "Europe/Lisbon" });
  return <SchedulePicker cron={v.cron} tz={v.tz} zones={ZONES} onChange={setV} next={<NextRuns times={NEXT} tz={v.tz} />} />;
}

function ChipsDemo() {
  const [v, setV] = useState(["feat/*", "main"]);
  return <ChipsInput label="Branch" values={v} onChange={setV} placeholder="main, release/*" />;
}

function SwitchDemo() {
  const [on, setOn] = useState(true);
  return <ListSwitch checked={on} onCheckedChange={setOn} label="PR triage" />;
}

function DaysDemo() {
  const [d, setD] = useState([1, 3, 5]);
  return <DayToggles days={d} onChange={setD} />;
}

export function AutomationsSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="automations" title="Automations" intro="Work nobody started by hand: on a schedule or a GitHub event, an agent, a command, a server or a webhook.">
      <Block id="au-marks" title="KindMark / ScopeChip / TriggerSummary / SessionMark / ListSwitch" note="An automation's row: on/off from the row, kind (an agent that is one of dude's roles adds it muted), where it lives, when it runs, its session.">
        <Panes mode={mode}>
          <Col>
            <Label>Kinds</Label>
            <KindMark kind="agent" />
            <KindMark kind="agent" role="Tester" />
            <KindMark kind="command" />
            <KindMark kind="server" />
            <KindMark kind="webhook" />
            <Label>Scope</Label>
            <ScopeChip org="Acme" />
            <ScopeChip org="Acme" project="web" />
            <Label>Triggers</Label>
            <TriggerSummary trigger={{ kind: "schedule", cron: "0 3 * * *", tz: "Europe/Lisbon" }} />
            <TriggerSummary trigger={{ kind: "schedule", cron: "30 9 * * 1-5", tz: "America/New_York" }} />
            <TriggerSummary trigger={{ kind: "github", event: "pull_request", actions: ["opened", "synchronize"] }} />
            <TriggerSummary trigger={{ kind: "github", event: "push" }} />
            <Label>Session</Label>
            <SessionMark mode="ephemeral" />
            <SessionMark mode="resumable" expiryDays={7} />
            <Label>On / off</Label>
            <SwitchDemo />
            <ListSwitch checked={false} onCheckedChange={() => undefined} label="Nightly audit" />
            <ListSwitch checked onCheckedChange={() => undefined} label="Release notes" disabled />
          </Col>
        </Panes>
      </Block>
      <Block id="au-owner" title="OwnerMark" note="Who owns it: name, the avatar alone in a list cell (its name for screen readers), or held by the organisation's admins after the owner left.">
        <Panes mode={mode}>
          <Col>
            <OwnerMark owner={ANA} org="Acme" />
            <OwnerMark owner={ANA} org="Acme" avatarOnly />
            <OwnerMark owner={null} org="Acme" left />
            <OwnerMark owner={null} org="Acme" left avatarOnly />
          </Col>
        </Panes>
      </Block>
      <Block id="au-schedule" title="SchedulePicker / NextRuns / DayToggles" note="Minutes, Hourly, Daily, Weekly or a cron line, in a time zone, with the next three runs.">
        <Panes mode={mode}>
          <Col>
            <ScheduleDemo initial="0 3 * * *" />
            <ScheduleDemo initial="30 9 * * 1,3,5" />
            <ScheduleDemo initial="0 */6 1 * *" />
            <Label>A cron line that does not parse</Label>
            <SchedulePicker cron="0 3 * *" tz="UTC" zones={ZONES} onChange={() => undefined}
              problem="a schedule is five fields: minute hour day-of-month month day-of-week" />
            <Label>No date matches</Label>
            <NextRuns times={[]} tz="UTC" />
            <Label>Days alone</Label>
            <DaysDemo />
          </Col>
        </Panes>
      </Block>
      <Block id="au-chips" title="ChipsInput" note="Filter values: Enter or comma adds, Backspace takes the last back; a glob chip is info-toned.">
        <Panes mode={mode}>
          <Col>
            <SettingRow label="Branch" help="Globs. Any one of them.">
              <ChipsDemo />
            </SettingRow>
            <SettingRow label="Author" help="Disabled: read-only to a member.">
              <ChipsInput label="Author" values={["!dependabot[bot]"]} onChange={() => undefined} disabled />
            </SettingRow>
          </Col>
        </Panes>
      </Block>
      <Block id="au-repo-filter" title="RepoFilterField" note="An organisation automation's repositories: all, only these, all except these; globs count what they match; focus suggests every project's repositories.">
        <Panes mode={mode}>
          <Col>
            <Label>All</Label>
            <RepoFilterDemo initial={{ mode: "all", repos: [] }} />
            <Label>Only these, with a glob</Label>
            <RepoFilterDemo initial={{ mode: "only", repos: ["acme/svc-*", "acme/web"] }} />
            <Label>All except these</Label>
            <RepoFilterDemo initial={{ mode: "except", repos: ["acme/sandbox-*", "acme/notebooks", "acme/runbooks"] }} />
            <Label>Disabled</Label>
            <RepoFilterDemo initial={{ mode: "only", repos: ["acme/svc-*"] }} disabled />
            <Label>390px</Label>
            <div style={{ width: 390 }}>
              <RepoFilterDemo initial={{ mode: "except", repos: ["acme/sandbox-*"] }} />
            </div>
          </Col>
        </Panes>
      </Block>
    </Section>
  );
}

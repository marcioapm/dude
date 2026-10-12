/*
 * A role's settings page (RolePage in the app) with the proposed switch
 * "May ask for a larger machine" beside Machine, layered organisation →
 * project like the role's other fields: Acme's Implementer, and billing's
 * Simplifier with the switch overridden off.
 */

import { useState } from "react";
import {
  AgentAvatar, SettingField, SettingFields, SettingSource, SettingsHeader, SettingsLayout, SettingsNote, SettingsSection, Switch, TextButton,
  type SettingsNavItem,
} from "@dude/design-system/components";
import { Select } from "@dude/design-system/primitives";
import { ORG, PROJECT, SIZES } from "./data.ts";
import s from "./bigmachine.module.css";

type RoleId = "implementer" | "simplifier";

const ROLES: ReadonlyArray<{ id: string; label: string; role: "conductor" | "brainstorm" | "investigator" | "implementer" | "reviewer" | "simplifier" | "qa_browser" }> = [
  { id: "conductor", label: "Conductor", role: "conductor" },
  { id: "brainstorm", label: "Brainstorm", role: "brainstorm" },
  { id: "investigator", label: "Investigator", role: "investigator" },
  { id: "implementer", label: "Implementer", role: "implementer" },
  { id: "reviewer", label: "Reviewer", role: "reviewer" },
  { id: "fixer", label: "Fixer", role: "implementer" },
  { id: "simplifier", label: "Simplifier", role: "simplifier" },
  { id: "qa_browser", label: "Tester", role: "qa_browser" },
];

const DESCRIPTION: Record<RoleId, string> = {
  implementer: "Writes the change and its tests.",
  simplifier: "Tidies the branch without changing what it does.",
};

export function RoleSettings({ where }: { where: "org" | "project" }) {
  const project = where === "project";
  const role: RoleId = project ? "simplifier" : "implementer";
  const label = project ? "Simplifier" : "Implementer";
  const [may, setMay] = useState(!project);
  const overridden = project && !may;
  const items: SettingsNavItem[] = [
    ...(project ? [] : [{ id: "members", label: "Members", icon: "human" as const }]),
    { id: "general", label: "General", icon: "settings" },
    {
      id: "agents", label: "Agents", icon: "agent", note: project ? "1 changed" : undefined,
      items: ROLES.map((r) => ({ id: r.id, label: r.label, leading: <AgentAvatar role={r.role} size="sm" />,
        note: project && r.id === "simplifier" ? "changed" : undefined })),
    },
    { id: "machines", label: "Machines", icon: "chip" },
    { id: "network", label: "Network", icon: "globe" },
  ];
  const sizeOptions = SIZES.map((z) => ({ value: z.id, label: z.name, meta: `${z.cpus} CPUs · ${z.memoryGiB} GiB · ${z.diskGiB} GiB${z.poolId ? ` · ${z.pool}` : ""}` }));
  return (
    <SettingsLayout key={where} items={items} current={role} onSelect={() => undefined}
      scope={project ? { title: PROJECT, subtitle: "Project settings" } : { title: ORG, subtitle: "Organisation settings", leading: <AgentAvatar role="conductor" size="lg" /> }}>
      <div className="settingsPage" data-testid="role-page">
        <SettingsHeader leading={<AgentAvatar role={role} size="lg" />} title={label} description={DESCRIPTION[role]}
          actions={project ? <Switch checked label={`On in ${PROJECT}`} onCheckedChange={() => undefined} /> : <Switch checked disabled label="Always on" onCheckedChange={() => undefined} />} />
        <SettingFields>
          <SettingField label="Harness" source={project ? <SettingSource source="organization" from={ORG} /> : null}>
            <Select aria-label="Harness" value="claude" onValueChange={() => undefined} options={[{ value: "claude", label: project ? `From ${ORG} · Claude Code` : "Claude Code" }]} />
          </SettingField>
          <SettingField label="Model" source={project ? <SettingSource source="organization" from={ORG} /> : null}>
            <Select aria-label="Model" value="high" onValueChange={() => undefined} options={[{ value: "high", label: project ? `From ${ORG} · Claude (High)` : "Claude (High)" }]} />
          </SettingField>
          <SettingField label="Time limit without progress" source={project ? <SettingSource source="organization" from={ORG} /> : null}>
            <Select aria-label="Time limit without progress" value="30" onValueChange={() => undefined} options={[{ value: "30", label: project ? `From ${ORG} · 30 minutes` : "30 minutes" }]} />
          </SettingField>
          <SettingField label="Machine" source={project ? <SettingSource source="organization" from={ORG} /> : null}>
            <Select aria-label="Machine" value="__inherit__" onValueChange={() => undefined}
              options={[{ value: "__inherit__", label: project ? `From ${ORG}` : "Default", meta: "Standard · 2 CPUs · 8 GiB · 20 GiB" }, ...sizeOptions]} />
          </SettingField>
          <SettingField label="May ask for a larger machine" htmlFor={`larger-${role}`}
            source={project ? (
              <SettingSource source={overridden ? "project" : "organization"} from={ORG} inherited={overridden ? "on" : undefined}
                onReset={overridden ? () => setMay(true) : undefined} />
            ) : null}>
            <span className={s.switchStack} data-testid="may-ask">
              <Switch id={`larger-${role}`} testId="may-ask-switch" checked={may} onCheckedChange={setMay}
                label={may ? "On" : project ? `Off in ${PROJECT}` : "Off"} />
              <small className={s.switchHint}>
                {may ? `It may move itself to a larger size when something runs out, saying what. Later ${label.toLowerCase()} Runs in the task run on it too.`
                  : `It is not offered the tool, and its prompt does not mention it. It runs on its Machine.`}
              </small>
            </span>
          </SettingField>
        </SettingFields>
        {project ? null : (
          <SettingsNote icon="info">
            The fixer follows the implementer's switch, as it does its machine, unless you give it its own. Anyone in a project may Reset a raised machine on the task's Overview.
          </SettingsNote>
        )}
        <SettingsSection title="Prompt" actions={<TextButton>History</TextButton>}>
          <SettingsNote icon="layers">{project ? `${ORG}'s prompt for the simplifier, as it is.` : "dude's built-in prompt."}</SettingsNote>
        </SettingsSection>
      </div>
    </SettingsLayout>
  );
}

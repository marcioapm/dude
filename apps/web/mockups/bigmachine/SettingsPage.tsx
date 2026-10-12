/*
 * The Machines settings page (MachinesSettings in the app) with the
 * proposed "Larger machines" section: Acme's choice, and billing's
 * override with SettingSource.
 */

import { useState } from "react";
import {
  AgentAvatar, SettingRow, SettingSource, SettingsHeader, SettingsLayout, SettingsMeta, SettingsNote, SettingsSection, TextButton,
  type SettingsNavItem,
} from "@dude/design-system/components";
import { Badge, Callout, ChoiceList, Select, Table, TBody, Td, Th, THead, Tr } from "@dude/design-system/primitives";
import { ORG, PROJECT, SIZES, TASK } from "./data.ts";
import s from "./bigmachine.module.css";

type Approval = "off" | "ask" | "auto";

const APPROVAL_OPTIONS = (ceiling: string) => [
  { value: "off" as const, label: "Off", description: "Agents are not offered the tool. They run on their settings' size, and say in their work when it was too small." },
  { value: "ask" as const, label: "Ask a person", description: "The task's owner decides in its Chat, or a session's owner in the session. Nothing moves until they approve." },
  { value: "auto" as const, label: `Automatically, up to ${ceiling}`, description: `A request up to ${ceiling} is approved at once and said in Chat. A larger one asks a person.` },
];

export function MachinesSettings({ where }: { where: "org" | "project" }) {
  const project = where === "project";
  const [mode, setMode] = useState<Approval>(project ? "auto" : "ask");
  const [ceiling, setCeiling] = useState("ms_large");
  const ceilingName = SIZES.find((x) => x.id === ceiling)!.name;
  const items: SettingsNavItem[] = project
    ? [
        { id: "general", label: "General", icon: "settings" },
        { id: "repositories", label: "Repositories", icon: "git-branch" },
        { id: "agents", label: "Agents", icon: "agent" },
        { id: "machines", label: "Machines", icon: "chip", note: "changed" },
        { id: "servers", label: "Servers", icon: "globe" },
      ]
    : [
        { id: "members", label: "Members", icon: "human" },
        { id: "general", label: "General", icon: "settings" },
        { id: "agents", label: "Agents", icon: "agent" },
        { id: "machines", label: "Machines", icon: "chip" },
        { id: "network", label: "Network", icon: "globe" },
      ];
  return (
    <SettingsLayout key={where} items={items} current="machines" onSelect={() => undefined}
      scope={project ? { title: PROJECT, subtitle: "Project settings" } : { title: ORG, subtitle: "Organisation settings", leading: <AgentAvatar role="conductor" size="lg" /> }}>
      <div className="settingsPage" data-testid="machines-page">
        <SettingsHeader title="Machines"
          description={project ? `What billing's agents run on beyond ${ORG}'s sizes and roles.` : "The sizes an agent's machine can be. Each agent runs on the size its settings name — in Agents, here or in a project — and on the default when it names none."} />
        <SettingsNote icon="info">
          {project ? `Values here apply to billing only. ${ORG}'s are shown as “From ${ORG}” and change in ${ORG}'s settings.`
            : "Only admins change these. A change applies to sessions that start after it, and to a stopped one when it resumes."}
        </SettingsNote>
        {!project ? (
          <SettingsSection title="Sizes">
            <Table>
              <THead><Tr><Th>Name</Th><Th align="right">CPUs</Th><Th align="right">Memory</Th><Th align="right">Disk</Th><Th hideWhenNarrow>Pool</Th></Tr></THead>
              <TBody>
                {SIZES.map((z) => (
                  <Tr key={z.id}>
                    <Td fit>{z.name}{z.isDefault ? <> <Badge tone="info" size="sm" icon="system">Default</Badge></> : null}</Td>
                    <Td align="right" fit className="ds-tnum">{z.cpus} CPUs</Td>
                    <Td align="right" fit className="ds-tnum">{z.memoryGiB} GiB</Td>
                    <Td align="right" fit className="ds-tnum">{z.diskGiB} GiB</Td>
                    <Td fit hideWhenNarrow mono={z.poolId !== null} muted={z.poolId === null}>{z.poolId ? z.pool : "Default pool"}</Td>
                  </Tr>
                ))}
              </TBody>
            </Table>
          </SettingsSection>
        ) : null}
        <SettingsSection title="Larger machines">
          <SettingRow label="Agents may ask for a larger machine" data-testid="approval-row"
            help="An agent that runs out of memory or time can ask for one of these sizes, larger than its own, with what ran out. Approved, it is moved, and later Runs of that role in the task (or the session) run on it too."
            source={project ? <SettingSource source="project" from={ORG} inherited="Ask a person" onReset={() => setMode("ask")} /> : undefined}>
            <div className={s.settingStack}>
              <ChoiceList label="Agents may ask for a larger machine" value={mode} onChange={setMode} options={APPROVAL_OPTIONS(ceilingName)} />
              {mode === "auto" ? (
                <div className={s.ceiling}>
                  <span className={s.ceilingLabel}>Up to</span>
                  <Select aria-label="Approve automatically up to" size="sm" value={ceiling} onValueChange={setCeiling}
                    options={SIZES.filter((z) => z.memoryGiB > 1).map((z) => ({ value: z.id, label: z.name, meta: `${z.cpus} CPUs · ${z.memoryGiB} GiB${z.poolId ? ` · ${z.pool} pool` : ""}` }))} />
                </div>
              ) : null}
              {project && mode === "auto" ? <SettingsMeta>{ORG}: Ask a person. billing approves up to {ceilingName} without asking.</SettingsMeta> : null}
              {mode === "auto" && ceiling === "ms_xl" ? (
                <Callout tone="attention">XL is in another pool: an agent moved to it starts fresh and loses what it had not pushed. Automatic approval still says so in Chat.</Callout>
              ) : null}
            </div>
          </SettingRow>
          <SettingRow label="At most" help="Requests one Run may make, approved or not. One waits at a time.">
            <span className="ds-tnum">3 per Run</span>
          </SettingRow>
        </SettingsSection>
        <SettingsSection title="Raised now" actions={<SettingsMeta>{project ? "billing's tasks" : `Across ${ORG}`}</SettingsMeta>}>
          <Table>
            <THead><Tr><Th>Task or session</Th><Th>Role</Th><Th>Runs on</Th><Th hideWhenNarrow>Approved by</Th><Th align="right" width="64px"><span className="ds-sr-only">Reset</span></Th></Tr></THead>
            <TBody>
              <Tr><Td fit mono>{TASK.key}</Td><Td wrap>Implementer, and the fixer</Td><Td fit>Standard → <b>Large</b></Td><Td hideWhenNarrow>Márcio</Td><Td align="right"><TextButton>Reset</TextButton></Td></Tr>
              {project ? null : <Tr><Td fit>A session</Td><Td>Brainstorm</Td><Td fit>Small → <b>Standard</b></Td><Td hideWhenNarrow>Ana</Td><Td align="right"><TextButton>Reset</TextButton></Td></Tr>}
            </TBody>
          </Table>
        </SettingsSection>
      </div>
    </SettingsLayout>
  );
}

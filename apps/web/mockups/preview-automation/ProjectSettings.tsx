import { useState } from "react";
import { SettingRow, SettingsHeader, SettingsLayout, SettingsSection, SettingsNote, ServerRecipeTable, HostChips, TextButton } from "@dude/design-system/components";
import { Button, Select, RowMenu } from "@dude/design-system/primitives";
import { RECIPES, type Screen } from "./data";
import s from "./styles.module.css";

export function ProjectSettings({ screen, enabled, onEnabled, go }: { screen: Screen; enabled: boolean; onEnabled: (v: boolean) => void; go: (s: Screen) => void }) {
  const empty = screen === "project-empty";
  const missing = screen === "project-missing" || empty;
  const before = screen === "project-before";
  const [saved, setSaved] = useState(false);
  const [configured, setConfigured] = useState(false);
  const [edit, setEdit] = useState<string | null>(null);
  const recipes = empty && !configured ? [] : RECIPES.map(r => ({ ...r, autostartInPreviews: missing && !configured ? false : r.autostartInPreviews }));
  return <SettingsLayout className={s.settingsLayout} scope={{ title: "Acme", subtitle: "Project settings" }} current="servers" onSelect={() => undefined}
    items={[{ id: "general", label: "General", icon: "settings" }, { id: "repositories", label: "Repositories", icon: "git-branch", note: 1 }, { id: "servers", label: "Servers", icon: "globe", note: recipes.length }, { id: "network", label: "Network", icon: "globe" }, { id: "automations", label: "Automations", icon: "zap" }, { id: "agents", label: "Agents", icon: "agent" }, { id: "delivery", label: "Delivery", icon: "list-check" }, { id: "memory", label: "Memory", icon: "layers" }]}
    footer={<>Anything not changed here follows Acme’s settings.</>}>
    <SettingsHeader title="Servers" description="What a run can serve. Each server is a port lux exposes at its own URL, with the command that starts it. People start them in a task, and a branch preview starts the ones marked to." actions={<span className={s.fixtureTag}>{before ? "Live UI · before" : "Proposed · design only"}</span>} />
    <SettingsNote icon="globe">Every server gets <code>https://&lt;name&gt;-&lt;run&gt;.&lt;preview domain&gt;</code>, behind your sign-in. Servers stop when a run pauses or moves host; a person starts them again.</SettingsNote>
    <SettingsSection title="Definitions" id="definitions" actions={<span className={s.muted}>Last changed by Márcio · today</span>}>
      {recipes.length ? <div className={s.recipeTable}><ServerRecipeTable recipes={recipes} menu={r => <RowMenu label={`Actions for ${r.name}`} items={[{ id: "edit", label: "Edit", icon: "edit", onSelect: () => setEdit(r.name) }]} />} /></div> : <p className={s.muted}>No servers yet. A server is a port and the command that starts it.</p>}
      <div className={s.formActions}><Button size="sm" variant="secondary" leadingIcon="plus" onClick={() => setEdit("new")}>Add server</Button></div>
      {edit ? <div className={s.setupEditor}><strong>{edit === "new" ? "Add web server" : `Edit ${edit}`}</strong><span>Port 3000 · bun run dev · apps/web</span><Button size="sm" variant="secondary" onClick={() => { setConfigured(true); setEdit(null); }}>Save · Start in previews</Button><Button size="sm" variant="quiet" onClick={() => setEdit(null)}>Cancel</Button></div> : null}
    </SettingsSection>
    <SettingsSection title="Branch previews" data-testid="preview-settings">
      {!before ? <SettingRow label="Automatic task previews" htmlFor="auto-preview" help={<>Prepare a preview when dude opens or reopens a task pull request. Starts only when opened.{enabled && missing && !configured ? <span className={s.setupHint} data-testid="setup-hint">Choose a server to start in previews. <a href="#definitions" onClick={() => document.getElementById("definitions")?.scrollIntoView({ block: "center", behavior: "smooth" })}>Definitions</a></span> : null}</>}>
        <Select id="auto-preview" aria-label="Automatic task previews" value={enabled ? "on" : "off"} onValueChange={v => { onEnabled(v === "on"); setSaved(false); }} options={[{ value: "off", label: "Off", description: "Default · no automatic preparation" }, { value: "on", label: "On", description: "Prepare a dormant task preview on PR open / reopen" }]} />
      </SettingRow> : null}
      <SettingRow label="Machine" help="The size a preview runs on: one run with every server that starts in previews."><Select aria-label="Machine" value="default" onValueChange={() => undefined} options={[{ value: "default", label: "Acme’s default · Medium", meta: "2 CPUs · 4 GiB" }]} /></SettingRow>
      <SettingsNote>A preview is one run with every server that starts in previews. Servers someone starts from a session run inside that agent’s machine instead, sharing its CPUs and memory.</SettingsNote>
      <SettingRow label="Image" help="What a preview run starts in. The project’s runtime image unless changed here."><Select aria-label="Preview image" value="runtime" options={[{ value: "runtime", label: "The project’s runtime image · Web base" }]} /></SettingRow>
      <SettingsNote icon="cube">This image can run containers. A preview keeps its containers while it sleeps, so it wakes in seconds instead of rebuilding them. Sleeping takes longer and stores the containers’ images and data.</SettingsNote>
      <SettingRow label="Egress allowlist" help="Hosts a preview run may reach, beyond the repository. Everything else is refused; * allows anywhere."><HostChips hosts={["registry.npmjs.org", "github.com"]} validate={() => null} onChange={() => undefined} /></SettingRow>
      <SettingRow label="Secrets" help="Project secrets given to previews reach their servers and setup scripts."><span className={s.muted}>2 reach previews · <a href="#secrets">Manage</a></span></SettingRow>
      <SettingRow label="Idle timeout" help="With no request for this long, the preview run is parked. Starting a server wakes it."><Select aria-label="Idle timeout" value="30" options={[{ value: "30", label: "30 minutes" }]} /></SettingRow>
      <SettingRow label="First wake timeout" help="How long lux waits until this preview has served once. Its first start may build images; default 15 minutes."><Select aria-label="First wake timeout" value="15" options={[{ value: "15", label: "15 minutes" }]} /></SettingRow>
      <SettingRow label="Wake timeout" help="How long lux waits on every later wake from the preview’s snapshot; default 5 minutes. A shorter timeout waits until an open wake is over."><Select aria-label="Wake timeout" value="5" options={[{ value: "5", label: "5 minutes" }]} /></SettingRow>
      <SettingRow label="Access" help="Who can open a preview."><span className={s.muted}>Visitors sign in the way they sign in to lux. Nothing is public.</span></SettingRow>
      {!before ? <div className={s.formActions}><Button size="sm" variant="secondary" onClick={() => setSaved(true)}>Save preview settings</Button><span className={s.muted} role="status">{saved ? "Saved locally · no configuration changed" : "Project default applies to dude tasks only."}</span><TextButton onClick={() => go("task-inherit")}>View task override recommendation</TextButton></div> : null}
    </SettingsSection>
    <SettingsSection title="Project secrets" id="secrets"><SettingsNote>Secrets are shared only with their selected recipients. Values remain hidden.</SettingsNote><SettingRow label="API_TOKEN" help="Project secret · previews, implementer"><span className={s.muted}>•••••••• · Edit</span></SettingRow><SettingRow label="SESSION_KEY" help="Project secret · previews"><span className={s.muted}>•••••••• · Edit</span></SettingRow><Button size="sm" variant="secondary" leadingIcon="plus">Add secret</Button></SettingsSection>
  </SettingsLayout>;
}

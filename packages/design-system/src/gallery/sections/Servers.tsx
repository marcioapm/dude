import { useState } from "react";
import { Block, Col, Label, Panes, Row, Section, States, type PaneMode } from "../Frame.tsx";
import styles from "../gallery.module.css";
import { AgentAvatar } from "../../components/AgentAvatar.tsx";
import { Duration } from "../../components/Numbers.tsx";
import { PersonAvatar } from "../../components/PersonAvatar.tsx";
import { PreviewAlsoRunning, PreviewStages, ServersMoved } from "../../components/PreviewStages.tsx";
import { EnvVarRows, ServerRecipeDialog, ServerRecipeTable, ServerUrlPreview } from "../../components/ServerRecipe.tsx";
import { AutostartMark, ServerList, ServerRecipeRow, ServerRow, ServersPanel, ServersRecipesPreview, ServersRunLine, ServersTabTip, ShortId, TerminalLink } from "../../components/ServerRow.tsx";
import { ServerStateDot, ServerStateMark } from "../../components/ServerStateMark.tsx";
import { ServersSummary, ServersSummaryRow } from "../../components/ServersSummary.tsx";
import { HostChips } from "../../components/HostChips.tsx";
import { StatusMark } from "../../components/StatusMark.tsx";
import { SettingRow, SettingSource, SettingsSection } from "../../components/Settings.tsx";
import { Button, IconButton, LinkButton } from "../../primitives/Button.tsx";
import { EmptyState } from "../../primitives/Feedback.tsx";
import { FormActions } from "../../primitives/Layout.tsx";
import { RowMenu } from "../../primitives/RowMenu.tsx";
import { Select } from "../../primitives/Select.tsx";
import { SERVER_DISPLAY_STATES } from "../../tokens/servers.ts";
import { canStartAny, canStopAny, describeServer, isMoving, serverLogLines, summarizeTaskServers } from "../../util/servers.ts";
import { Tab, TabList, Tabs } from "../../primitives/Tabs.tsx";
import { toggled } from "../../util/sets.ts";
import { formatTimestamp } from "../../util/format.ts";
import { egressProblem, type PreviewStage, type RunServer, type TaskServers } from "@dude/domain";
import type { RecipeEnvVar } from "../../components/ServerRecipe.tsx";
import { PREVIEW_DOMAIN, previewEgress, serverLogs, serverLogsExited, serverRecipes, serverScenarios, type ServerScenario } from "../serverFixtures.ts";
import { people } from "../navFixtures.ts";

const SCENARIO_WORDS: Record<ServerScenario, string> = {
  a: "a · agent run, servers running",
  b: "b · after a migration",
  c: "c · api exited 1",
  d: "d · no run: Preview branch",
  e: "e · branch preview booting",
};

/** The servers panel as the app composes it, for one scenario. */
function Panel({ data, openLogs = [] }: { readonly data: TaskServers; readonly openLogs?: readonly string[] | undefined }) {
  const [open, setOpen] = useState<Set<string>>(() => new Set(openLogs));
  const now = Date.now();
  const run = data.run;
  if (!run) {
    return (
      <ServersPanel>
        <EmptyState
          icon="globe"
          title="Nothing is serving this task"
          description="Servers live on a run. The implementer finished and its run ended, so its servers ended with it. Preview the branch to bring up the project’s servers on a lightweight run of their own — no agent, just the checkout."
          action={
            <FormActions note="feature/checkout-v2 @ 3f2a9c1 · web, api start automatically · parks after 30m idle">
              <Button variant="primary" leadingIcon="play">Preview branch</Button>
            </FormActions>
          }
          className={styles["serversEmpty"]}
        />
        <ServersRecipesPreview recipes={data.recipes} note="Defined in project settings → Servers." />
      </ServersPanel>
    );
  }
  const logs = data.servers.some((s) => s.state === "exited") ? serverLogsExited : serverLogs;
  const isPreview = run.kind === "preview";
  const toggle = (name: string) => setOpen((s) => toggled(s, name));
  return (
    <>
      <ServersPanel note={isPreview
        ? "A preview run has no agent. It is parked after 30 minutes without a request; starting a server wakes it. URLs open only for example.com accounts, through Cloudflare Access."
        : <>Servers stop when the run pauses, ends or moves host; they do not restart on their own. Output streams into the run log as <span className="ds-mono">server:&lt;name&gt;</span>. URLs open only for example.com accounts, through Cloudflare Access.</>}>
        <ServersRunLine
          avatar={isPreview ? <AgentAvatar role="system" size="lg" /> : <PersonAvatar person={people["marcio"]!} size={32} agent="implementer" live title="" />}
          title={run.label}
          status={<StatusMark status={isPreview ? "starting" : "running"} size="sm" />}
          detail={isPreview
            ? <><code>{run.branch} @ {run.commit}</code> · {run.host} · started <Duration ms={Math.max(0, Date.now() - Date.parse(run.startedAt ?? ""))} tone="muted" format="age" /> ago · parks after {run.parksAfterMinutes}m idle</>
            : <><ShortId id={run.luxRunId} /> · {run.host} · started <Duration ms={Math.max(0, Date.now() - Date.parse(run.startedAt ?? ""))} tone="muted" format="age" /> ago · for Márcio</>}
          actions={
            <>
              {/* An agent's terminal is in its session's rail; a preview has no session. */}
              {isPreview ? <TerminalLink href={run.terminalUrl ?? "#"} /> : null}
              {isPreview ? <Button size="sm" variant="quiet" leadingIcon="stop">Stop preview</Button> : (
                <>
                  <Button size="sm" variant="secondary" leadingIcon="play" disabled={!canStartAny(data.servers)}>Start all</Button>
                  <Button size="sm" variant="quiet" leadingIcon="stop" disabled={!canStopAny(data.servers)}>Stop all</Button>
                  <Button size="sm" variant="quiet" leadingIcon="plus">Add server</Button>
                </>
              )}
            </>
          }
        />
        {data.moved ? <ServersMoved at={formatTimestamp(data.moved.at, "time-short")} fromHost={data.moved.fromHost} toHost={data.moved.toHost} onStartAll={() => undefined} /> : null}
        {run.previewStage && run.previewStage !== "ready" ? (
          <PreviewStages stage={run.previewStage} branch={run.branch} setup="npm ci, make deps" elapsed={<Duration since={run.startedAt ?? undefined} live tone="muted" />} />
        ) : null}
        <ServerList aria-label="Servers">
          {data.servers.map((s) => {
            const words = describeServer(s, now, run);
            return (
              <ServerRow
                key={s.name}
                name={s.name}
                port={s.port}
                state={words.state}
                stateLabel={words.label}
                detail={words.detail}
                error={s.error}
                url={s.url}
                onStart={() => undefined}
                onStop={() => undefined}
                onRestart={() => undefined}
                menu={<RowMenu size="sm" label={`Actions for ${s.name}`} items={[{ id: "remove", label: "Remove", tone: "danger" }]} />}
                logs={{ open: open.has(s.name), onToggle: () => toggle(s.name), lines: serverLogLines(logs[s.name] ?? []), live: isMoving(s), onFull: () => undefined }}
              />
            );
          })}
        </ServerList>
      </ServersPanel>
    </>
  );
}

function Summary({ data }: { readonly data: TaskServers }) {
  const now = Date.now();
  if (!data.run) {
    return (
      <ServersSummary empty="No run is serving this branch." actions={<Button size="sm" variant="secondary" leadingIcon="play">Preview branch</Button>} />
    );
  }
  return (
    <ServersSummary
      where="on the implementer run"
      notice={data.moved ? `Stopped when the run moved host at ${formatTimestamp(data.moved.at, "time-short")}.` : undefined}
      actions={
        <>
          <Button size="sm" variant="quiet" trailingIcon="arrow-right">All servers</Button>
          <TerminalLink href={data.run.terminalUrl ?? "#"}>Terminal</TerminalLink>
        </>
      }
    >
      {data.servers.map((s) => {
        const words = describeServer(s, now, data.run);
        return <ServersSummaryRow key={s.name} name={s.name} state={words.state} stateLabel={words.label} url={s.url} detail={words.detail} onStart={() => undefined} />;
      })}
    </ServersSummary>
  );
}

function RecipeDialogDemo({ label, existing, initial }: { readonly label: string; readonly existing: (typeof serverRecipes)[number] | null; readonly initial?: { name: string; port: string } | undefined }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant="secondary" onClick={() => setOpen(true)}>{label}</Button>
      {open ? <ServerRecipeDialog open onOpenChange={setOpen} existing={existing} repository="web-console" domain={PREVIEW_DOMAIN} initial={initial} onSubmit={() => setOpen(false)} /> : null}
    </>
  );
}

function PreviewSettingsDemo() {
  const [hosts, setHosts] = useState(previewEgress);
  const [idle, setIdle] = useState("30");
  return (
    <SettingsSection title="Branch previews">
      <SettingRow label="Image" help="The container a preview run starts in. The project’s runner image unless changed here." source={<SettingSource source="organization" from="the project’s runner" />}>
        <Select aria-label="Image" value="ghcr.io/example/runner:node22-go1.23" options={[{ value: "ghcr.io/example/runner:node22-go1.23", label: <span className="ds-mono">ghcr.io/example/runner:node22-go1.23</span> }]} />
      </SettingRow>
      <SettingRow label="Egress allowlist" help="Hosts a preview run may reach, beyond the repository. Everything else is refused.">
        <HostChips hosts={hosts} onChange={setHosts} validate={egressProblem} />
      </SettingRow>
      <SettingRow label="Idle timeout" help="With no request for this long, the preview run is parked. Starting a server wakes it." source={<SettingSource source="project" from="Example" inherited="15 minutes" onReset={() => setIdle("15")} />}>
        <Select aria-label="Idle timeout" value={idle} onValueChange={setIdle} options={["15", "30", "60", "120"].map((m) => ({ value: m, label: `${m} minutes` }))} />
      </SettingRow>
      <SettingRow label="Access" help="Who can open a preview.">
        <AutostartMark autostart className={styles["serversAccess"]} />
        <LinkButton href="https://one.dash.cloudflare.com/" size="sm">Access policy</LinkButton>
      </SettingRow>
    </SettingsSection>
  );
}

function EnvDemo() {
  const [vars, setVars] = useState<RecipeEnvVar[]>([{ name: "VITE_API_URL", value: "http://localhost:8080" }, { name: "VITE_EXAMPLE_ENV", value: "preview" }]);
  return <EnvVarRows vars={vars} onChange={setVars} />;
}

const ROW_STATES: ReadonlyArray<readonly [string, RunServer, readonly string[]]> = [
  ["ready", serverScenarios.a.servers[0]!, []],
  ["starting", serverScenarios.a.servers[1]!, []],
  ["stopped, never started", serverScenarios.a.servers[2]!, []],
  ["stopped after a migration", serverScenarios.b.servers[0]!, []],
  ["exited, logs open", serverScenarios.c.servers[1]!, ["api"]],
  ["unreachable", { ...serverScenarios.a.servers[0]!, state: "unreachable", since: new Date(Date.now() - 20_000).toISOString() }, []],
];

export function ServersSection({ mode }: { readonly mode: PaneMode }) {
  const [scenario, setScenario] = useState<ServerScenario>("a");
  return (
    <Section
      id="servers"
      title="Servers and previews"
      intro="What a run serves: each server a named port with its state, URL and log, on the run it lives on; a branch preview bringing them up with no agent; and the project's recipes for them."
    >
      <Block id="sv-state" title="ServerStateMark" note={<>A server's state in StatusMark's grammar: stopped is a filled square, starting a half circle that breathes, ready a check, unreachable a warning, exited a cross with its code. <code>waiting</code> is a preview's spec server before its turn.</>}>
        <Panes mode={mode}>
          <Col>
            <Row>{SERVER_DISPLAY_STATES.map((s) => <ServerStateMark key={s} state={s} />)}</Row>
            <Label>Small, with the exit code</Label>
            <Row>{SERVER_DISPLAY_STATES.map((s) => <ServerStateMark key={s} state={s} size="sm" label={s === "exited" ? "Exited 1" : undefined} />)}</Row>
            <Label>Icon only</Label>
            <Row>{SERVER_DISPLAY_STATES.map((s) => <ServerStateMark key={s} state={s} iconOnly />)}</Row>
            <Label>Dot, for a tab</Label>
            <Row style={{ gap: 16 }}>{SERVER_DISPLAY_STATES.map((s) => <ServerStateDot key={s} state={s} />)}</Row>
            <Label>Grayscale check</Label>
            <Row style={{ filter: "grayscale(1)" }}>{SERVER_DISPLAY_STATES.map((s) => <ServerStateMark key={s} state={s} />)}</Row>
          </Col>
        </Panes>
      </Block>

      <Block id="sv-row" title="ServerRow / ServerList" note="One row per server: mono name and port, the state and what it means, the URL to copy or open, and the actions its state allows. Its log folds open under it as server:<name>. A recipe row shows what a preview would start.">
        <Panes mode={mode}>
          <Col>
            <States items={ROW_STATES.map(([k, s, openLogs]) => [k, <ServerList key={k} style={{ flex: 1 }}><RowDemo server={s} openLogs={openLogs} /></ServerList>])} />
            <Label>Recipes, as "What a preview starts"</Label>
            <ServerList>
              {serverRecipes.map((r) => <ServerRecipeRow key={r.name} name={r.name} port={r.port} command={r.command} autostart={r.autostartInPreviews} />)}
            </ServerList>
            <Label>AutostartMark</Label>
            <Row><AutostartMark autostart /><AutostartMark autostart={false} /><AutostartMark autostart short /></Row>
            <Label>LinkButton: a link among buttons</Label>
            <Row><TerminalLink href="https://lux.example/runs/run_1/terminal" /><LinkButton href="https://github.com" size="sm">Open on GitHub</LinkButton><LinkButton href="#" external={false} variant="secondary">Internal</LinkButton></Row>
          </Col>
        </Panes>
      </Block>

      <Block id="sv-panel" title="ServersPanel" note="The task's Servers tab: the run the servers live on as a line, a notice when it moved host, a preview's stages, the list, and a note. Pick a scenario; Preview opens a ready server in a new tab.">
        <div className={styles["row"]} style={{ marginBottom: 12 }}>
          <Select aria-label="Scenario" size="sm" value={scenario} onValueChange={setScenario} options={(Object.keys(SCENARIO_WORDS) as ServerScenario[]).map((k) => ({ value: k, label: SCENARIO_WORDS[k] }))} />
        </div>
        <Panes mode={mode} surface>
          <div className={styles["serversTab"]}>
            <Panel data={serverScenarios[scenario]} openLogs={scenario === "c" ? ["api"] : []} />
          </div>
        </Panes>
      </Block>

      <Block id="sv-tab" title="The task's Servers tab" note={<>Its count is the servers that are on (starting, ready or unreachable), none at zero; after it the first bad server's dot, else the breathing dot while one starts or a preview comes up. Hover or focus a Servers tab for its tooltip, from the same <code>summarizeTaskServers</code>.</>}>
        <Panes mode={mode}>
          <Col>
            <States items={TAB_STATES.map(([k, data]) => [k, <TabDemo key={k} data={data} />])} />
            <Label>The tooltip, open</Label>
            <Row style={{ alignItems: "flex-start", gap: 16 }}>
              {TAB_STATES.map(([k, data]) => {
                const summary = summarizeTaskServers(data);
                return summary ? <span key={k} className={styles["serversTip"]}><ServersTabTip summary={summary} /></span> : null;
              })}
            </Row>
          </Col>
        </Panes>
      </Block>

      <Block id="sv-stages" title="PreviewStages / ServersMoved / PreviewAlsoRunning" note="A branch preview coming up, stage by stage; the notice when a run moved host and its servers stopped with the old placement; and a preview live behind the agent run the panel shows, with its Stop.">
        <Panes mode={mode}>
          <Col>
            <States items={(["scheduling", "cloning", "setup", "starting", "ready"] as PreviewStage[]).map((s) => [s, <PreviewStages key={s} stage={s} branch="feature/checkout-v2" setup="npm ci, make deps" elapsed="24s" />])} />
            <ServersMoved at="14:32" fromHost="lux-c7" toHost="lux-c9" onStartAll={() => undefined} />
            <PreviewAlsoRunning onStop={() => undefined} />
            <PreviewAlsoRunning parked onStop={() => undefined} />
          </Col>
        </Panes>
      </Block>

      <Block id="sv-summary" title="ServersSummary" note="The task overview's aside, beside the pull request: one row per server, its state as a glyph, its URL while ready. With no run, the way to a preview.">
        <Panes mode={mode} surface>
          <Col>
            <Summary data={serverScenarios.a} />
            <Summary data={serverScenarios.b} />
            <Summary data={serverScenarios.d} />
          </Col>
        </Panes>
      </Block>

      <Block id="sv-recipes" title="ServerRecipeTable / ServerRecipeDialog" note="Project settings → Servers: the definitions as a table, the editor as a dialog with the URL the name makes, validation in words, environment variables as rows, and the preview settings.">
        <Panes mode={mode}>
          <Col>
            <ServerRecipeTable recipes={serverRecipes} menu={(r) => <RowMenu size="sm" label={`Actions for ${r.name}`} items={[{ id: "edit", label: "Edit", icon: "edit" }, { kind: "separator" }, { id: "remove", label: "Remove", tone: "danger" }]} />} />
            <Row>
              <RecipeDialogDemo label="Edit web" existing={serverRecipes[0]!} />
              <RecipeDialogDemo label="Add (invalid input)" existing={null} initial={{ name: "Web_App", port: "70000" }} />
              <RecipeDialogDemo label="Add" existing={null} />
            </Row>
            <Label>ServerUrlPreview</Label>
            <Row><ServerUrlPreview name="web" domain={PREVIEW_DOMAIN} /><ServerUrlPreview name="Web_App" domain={PREVIEW_DOMAIN} /><ServerUrlPreview name="web" /></Row>
            <Label>EnvVarRows</Label>
            <EnvDemo />
            <Label>Empty</Label>
            <EmptyState compact icon="globe" title="No servers yet" description="A server is a port and the command that starts it. Once defined, anyone can start it in a task, or preview a branch with it." action={<Button variant="secondary" leadingIcon="plus">Add server</Button>} />
            <Label>Preview settings</Label>
            <PreviewSettingsDemo />
          </Col>
        </Panes>
      </Block>
    </Section>
  );
}

/** The tab's four states, each from a scenario's servers. */
const TAB_STATES: ReadonlyArray<readonly [string, TaskServers]> = [
  ["0 on", serverScenarios.b],
  ["2 on", { ...serverScenarios.a, servers: serverScenarios.a.servers.map((x) => (x.name === "api" ? { ...x, state: "ready" as const, readySince: x.since } : x)) }],
  ["1 on, 1 exited", serverScenarios.c],
  ["starting", serverScenarios.a],
];

/** A task's tab row with the Servers tab as the app draws it: count, mark and tooltip from one reading. */
function TabDemo({ data }: { readonly data: TaskServers }) {
  const summary = summarizeTaskServers(data);
  const mark = !summary ? null : summary.bad ? <ServerStateDot state={summary.bad.state} label={`${summary.bad.name} ${summary.bad.state}`} />
    : summary.starting ? <ServerStateDot state="starting" label="Starting" />
    : summary.moved ? <ServerStateDot state="unreachable" label="Stopped when the run moved" />
    : null;
  return (
    <Tabs defaultValue="overview">
      <TabList aria-label="Task">
        <Tab value="overview">Overview</Tab>
        <Tab value="sessions" count={1}>Sessions</Tab>
        <Tab value="servers" count={summary && summary.on.length > 0 ? summary.on.length : undefined} trailing={mark}
          tooltip={summary ? <ServersTabTip summary={summary} /> : undefined}>Servers</Tab>
      </TabList>
    </Tabs>
  );
}

function RowDemo({ server, openLogs }: { readonly server: RunServer; readonly openLogs: readonly string[] }) {
  const [open, setOpen] = useState(openLogs.includes(server.name));
  const words = describeServer(server, Date.now());
  const logs = server.state === "exited" ? serverLogsExited : serverLogs;
  return (
    <ServerRow
      name={server.name}
      port={server.port}
      state={words.state}
      stateLabel={words.label}
      detail={words.detail}
      error={server.error}
      url={server.url}
      onStart={() => undefined}
      onStop={() => undefined}
      onRestart={() => undefined}
      menu={<IconButton size="sm" icon="more" label={`Actions for ${server.name}`} />}
      logs={{ open, onToggle: () => setOpen(!open), lines: serverLogLines(logs[server.name] ?? []), live: server.state === "ready" }}
    />
  );
}

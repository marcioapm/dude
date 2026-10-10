import { useContext, useState, type ReactNode } from "react";
import { Block, Label, PaneDensityContext, Panes, Section, densitiesFor, type PaneMode } from "../Frame.tsx";
import styles from "../gallery.module.css";
import { ChatComposer } from "../../components/ChatComposer.tsx";
import { Sidebar, SidebarLink, SidebarProfile, SidebarRailItem, SidebarSessions } from "../../components/Sidebar.tsx";
import { ComposerLinks, RECENT_SESSIONS_SHOWN, RecentSessions, StarterPills, WELCOME_FIRST_TIME, WELCOME_STARTERS, Welcome, WelcomeNote, type LinkableProject, type RecentSession } from "../../components/Welcome.tsx";
import { ModelPicker, type ModelChoice, type PickerTier } from "../../components/ModelPicker.tsx";
import { PersonAvatar } from "../../components/PersonAvatar.tsx";
import { Icon } from "../../icons/index.tsx";
import type { ThemeMode } from "../../tokens/themes.ts";
import type { Density } from "../../tokens/density.ts";
import type { NavRef } from "../../util/navModel.ts";
import { navProjects, people } from "../navFixtures.ts";

const P = people;

const PROJECTS: readonly LinkableProject[] = navProjects.map((p) => ({ id: p.id, name: p.name }));

/** An organisation's tiers, for the ModelPicker: two Anthropic, one OpenAI. */
export const GALLERY_TIERS: readonly PickerTier[] = [
  { id: "mtr_claude", name: "Claude (High)", model: "claude-opus-5" },
  { id: "mtr_coder", name: "Coder", model: "claude-sonnet-5" },
  { id: "mtr_sol", name: "Sol", model: "gpt-6-sol" },
];
/** Its Brainstorm setting: what "Organisation default" stands for. */
export const GALLERY_ORG_MODEL = { tier: GALLERY_TIERS[0]!, harness: "claude-code" as const };
/** A tier with no model yet: listed, refused. */
const GALLERY_MENU_TIERS: readonly PickerTier[] = [...GALLERY_TIERS, { id: "mtr_fast", name: "Fast", model: null }];
/** Why Coder on Codex no longer runs, as the orchestrator says it. */
export const GALLERY_MISFIT = "Codex takes an OpenAI model, but the tier Coder requests claude-sonnet-5. Choose another harness or tier in the session's Model.";

/** A picker that keeps its own choice, as the app's welcome does. */
function Picking({ start = { tier: null, harness: null } }: { readonly start?: ModelChoice }) {
  const [value, setValue] = useState<ModelChoice>(start);
  return <ModelPicker tiers={GALLERY_TIERS} organization={GALLERY_ORG_MODEL} value={value} onChange={setValue} />;
}

const RECENT: readonly RecentSession[] = [
  { id: "s1", title: "Usage-based billing", summary: "Filed 1 epic, 4 tasks", age: "yesterday" },
  { id: "s2", title: "Q4 cleanup ideas", summary: "Nothing filed yet", age: "3 days ago", shared: { owner: P["ana"] } },
  { id: "s3", title: "Runner sandbox egress", summary: "Filed 2 tasks · edited RUN-31", age: "last week" },
  { id: "s4", title: "Docs IA", summary: "Filed 1 epic", age: "2 weeks ago" },
  { id: "s5", title: "Webhook retry policy", summary: "Edited WI-2401", age: "3 weeks ago" },
  { id: "s6", title: "Onboarding checklist", summary: "Nothing filed yet", age: "a month ago" },
];

/** The app passes its own face (DudeMark); the gallery a stand-in in the brainstorm's colour. */
function Mark() {
  return <span className={styles["standInMark"]}><Icon name="brainstorm" size={24} /></span>;
}

/** The welcome as the app composes it: a composer with link chips, starters that fill it, recent sessions. */
function WelcomeDemo({ density, firstTime }: { readonly density: Density; readonly firstTime?: boolean }) {
  const [text, setText] = useState("");
  const [linked, setLinked] = useState<LinkableProject[]>([]);
  return (
    <Welcome
      mark={<Mark />}
      greeting="Afternoon, Márcio"
      line="What are we working out today?"
      composer={
        <ChatComposer mode="chat" variant="stage" value={text} onValueChange={setText} placeholder="Start a session: an idea, a question, a plan…"
          to={<>To <b>Brainstorm</b></>} toAside={<Picking />} onSubmit={() => false}
          leading={<ComposerLinks linked={linked} projects={PROJECTS} onLink={(p) => setLinked((l) => [...l, p])}
            onUnlink={(p) => setLinked((l) => l.filter((x) => x.id !== p.id))} />} />
      }
      starters={<StarterPills starters={WELCOME_STARTERS} onPick={(s) => setText(s.prompt)} />}
      footer={firstTime ? <WelcomeNote>{WELCOME_FIRST_TIME}</WelcomeNote>
        : <RecentSessions sessions={RECENT.slice(0, RECENT_SESSIONS_SHOWN[density])} onOpen={() => undefined} onAll={() => undefined} />}
    />
  );
}

/** The shell: the sidebar (full or as its rail) beside a main pane. */
function Shell({ collapsed: initial, children }: { readonly collapsed?: boolean; readonly children: ReactNode }) {
  const [collapsed, setCollapsed] = useState(Boolean(initial));
  const [selected, setSelected] = useState<NavRef | null>(null);
  const you = P["marcio"]!;
  return (
    <div className={styles["shell"]}>
      <Sidebar
        projects={navProjects}
        selected={selected}
        onSelect={setSelected}
        you={you.id}
        title={<span style={{ display: "inline-flex", alignItems: "center", gap: 10 }}><span style={{ width: 30, height: 30 }}><Mark /></span>El Duderino</span>}
        railMark={<span style={{ width: 28, height: 28 }}><Mark /></span>}
        homeSelected={selected === null}
        onHome={() => setSelected(null)}
        collapsed={collapsed}
        onCollapsedChange={setCollapsed}
        onWaitingSelect={() => undefined}
        railSessions={{ onNew: () => setSelected(null), onOpenList: () => undefined, recent: RECENT.map((r) => r.title) }}
        sessions={<SidebarSessions sessions={RECENT.slice(0, 4).map((r) => ({ id: r.id, title: r.title, ...(r.shared ? { shared: true, owner: r.shared.owner } : {}) }))}
          onSelect={() => undefined} onNew={() => setSelected(null)} onOpenList={() => undefined} />}
        footer={<>
          <SidebarLink icon="building">Organisation settings</SidebarLink>
          <SidebarProfile person={you} detail="marcio@acme.dev" onOpen={() => undefined} />
        </>}
        railFooter={<>
          <SidebarRailItem label="Organisation settings"><Icon name="building" size={16} /></SidebarRailItem>
          <SidebarRailItem label="Your settings"><PersonAvatar person={you} size={28} /></SidebarRailItem>
        </>}
      />
      <main className={`${styles["shellMain"]} ${styles["shellMainFlush"]}`}>{children}</main>
    </div>
  );
}

/** One full-width frame per theme and density, as the App shell section draws them. */
function Frames({ mode, shot, children }: { readonly mode: PaneMode; readonly shot: string; readonly children: (density: Density) => ReactNode }) {
  const paneDensity = useContext(PaneDensityContext);
  const themes: ThemeMode[] = mode === "both" ? ["dark", "light"] : [mode];
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 16 }}>
      {densitiesFor(paneDensity).flatMap((d) => themes.map((t) => (
        <div key={`${d}-${t}`} data-shot={`${shot}-${t}-${d}`} data-theme={t} data-density={d} style={{ colorScheme: t }} className={styles["shellFrame"]}>
          <span className={styles["paneLabel"]}>{`${t} · ${d}`}</span>
          {children(d)}
        </div>
      )))}
    </div>
  );
}

/** The link menu drawn open inside its pane, so it takes the pane's theme. */
function OpenMenu() {
  const [host, setHost] = useState<HTMLElement | null>(null);
  return (
    <div ref={setHost} className={styles["composerFrame"]}>
      <ChatComposer mode="chat" placeholder="Start a session…" to={<>To <b>Brainstorm</b></>} onSubmit={() => false}
        leading={<ComposerLinks linked={[PROJECTS[0]!]} projects={PROJECTS} onLink={() => undefined} onUnlink={() => undefined} previewMenu={host} />} />
    </div>
  );
}

/** The model menu drawn open inside its pane, on a pair whose harness refuses a tier. */
function OpenModelMenu() {
  const [host, setHost] = useState<HTMLElement | null>(null);
  return (
    <div ref={setHost} className={styles["composerFrame"]} style={{ minHeight: 0, paddingBottom: 620 }}>
      <ChatComposer mode="chat" placeholder="Start a session…" to={<>To <b>Brainstorm</b></>} onSubmit={() => false}
        toAside={<ModelPicker tiers={GALLERY_MENU_TIERS} organization={GALLERY_ORG_MODEL} value={{ tier: null, harness: null }}
          onChange={() => undefined} previewMenu={host} />} />
    </div>
  );
}

export function WelcomeSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="welcome" title="Welcome"
      intro="What dude opens on, and what New session opens: nothing is made until the first message is sent. The greeting, the composer raised off the page with what the session will read said in it, starters that fill it and never send, and the sessions you were in last. Beside it, the sidebar, which folds to a rail on a wide screen.">
      <Block id="wl-shell" title="Welcome beside the sidebar"
        note={`The 720px column a little above the middle: 12vh of air (6vh compact), a 64px face (48), four recent sessions (six). The composer is ChatComposer variant="stage": raised (shadow-2), a 72px field (52), only here.`}>
        <Frames mode={mode} shot="welcome">{(d) => <Shell><WelcomeDemo density={d} /></Shell>}</Frames>
      </Block>
      <Block id="wl-first" title="The first time"
        note="No sessions yet: a muted line in the recent list's place says what a session does.">
        <Frames mode={mode} shot="welcome-first">{(d) => <Shell><WelcomeDemo density={d} firstTime /></Shell>}</Frames>
      </Block>
      <Block id="wl-rail" title="Sidebar collapsed to its rail"
        note="56px on the chrome shade: home, expand, search, Waiting on you (the one loud count), New session, Sessions, each project's face (the needs-you diamond on one waiting on you, its counts in its tooltip), then the band. Every item named in a tooltip to the right. Only at 1000px and up.">
        <Frames mode={mode} shot="welcome-rail">{(d) => <Shell collapsed><WelcomeDemo density={d} /></Shell>}</Frames>
      </Block>
      <Block id="wl-starters" title="StarterPills"
        note="Raised full-radius pills; the glyph takes the label's ink, secondary, brightening with it on hover. No border: shade in dark, shadow-1 in light, a secondary-hover wash on hover. What each is for is its tooltip. Picking one fills the composer; it never sends.">
        <Panes mode={mode} surface>
          <StarterPills starters={WELCOME_STARTERS} onPick={() => undefined} />
        </Panes>
      </Block>
      <Block id="wl-links" title="ComposerLinks"
        note="What the session will read, in ChatComposer's leading: each project's face, name and a 12px close, then a quiet Link opening RowMenu's float. Read only without onLink and onUnlink.">
        <Panes mode={mode} surface>
          <Label>none linked</Label>
          <div className={styles["composerFrame"]} style={{ minHeight: 0 }}>
            <ChatComposer mode="chat" placeholder="Start a session…" to={<>To <b>Brainstorm</b></>} onSubmit={() => false}
              leading={<ComposerLinks linked={[]} projects={PROJECTS} onLink={() => undefined} onUnlink={() => undefined} />} />
          </div>
          <Label>two linked</Label>
          <div className={styles["composerFrame"]} style={{ minHeight: 0 }}>
            <ChatComposer mode="chat" placeholder="Start a session…" to={<>To <b>Brainstorm</b></>} onSubmit={() => false}
              leading={<ComposerLinks linked={PROJECTS.slice(0, 2)} projects={PROJECTS} onLink={() => undefined} onUnlink={() => undefined} />} />
          </div>
          <Label>read only</Label>
          <ComposerLinks linked={PROJECTS.slice(0, 2)} projects={PROJECTS} />
          <Label>the menu open</Label>
          <OpenMenu />
        </Panes>
      </Block>
      <Block id="wl-model" title="ModelPicker"
        note={`The model and harness a session's agent runs on, in ChatComposer's toAside (right after "To Brainstorm") and in the session's rail. A quiet chip reading the effective pair, a muted "default" while both follow the organisation. Its menu is RowMenu's float with two radio groups, each led by "Organisation default (…)"; a pair the harness cannot run, or a tier naming no model, is disabled and says why. A value that stopped fitting after it was chosen takes the danger mark and says why under the chip. Read only for a member who is not the owner: the chip on the hover wash, nothing to open.`}>
        <Panes mode={mode} surface>
          <Label>default</Label>
          <div data-testid="model-default"><Picking /></div>
          <Label>chosen</Label>
          <div data-testid="model-chosen"><Picking start={{ tier: "mtr_sol", harness: "opencode" }} /></div>
          <Label>read only</Label>
          <div data-testid="model-readonly">
            <ModelPicker tiers={GALLERY_TIERS} organization={GALLERY_ORG_MODEL} value={{ tier: "mtr_coder", harness: null }} readOnly />
          </div>
          <Label>the current value is a misfit: Coder on Codex, after the organisation moved Coder to an Anthropic model</Label>
          <div data-testid="model-misfit">
            <ModelPicker tiers={GALLERY_TIERS} organization={GALLERY_ORG_MODEL} value={{ tier: "mtr_coder", harness: "codex" }}
              misfit={GALLERY_MISFIT} onChange={() => undefined} />
          </div>
          <Label>the menu open: on Claude Code, Sol and Codex are refused; Fast names no model</Label>
          <OpenModelMenu />
        </Panes>
      </Block>
      <Block id="wl-recent" title="RecentSessions"
        note="One line each at row-default height (32, 28 compact), told apart by space and a row-hover wash: the bulb in the brainstorm's role colour (it is the role glyph), the title, the shared mark, what came of it (dropped on a phone), the age, tabular.">
        <Panes mode={mode} surface>
          {(_t, d) => <div style={{ maxWidth: 720 }}><RecentSessions sessions={RECENT.slice(0, RECENT_SESSIONS_SHOWN[d])} onOpen={() => undefined} onAll={() => undefined} /></div>}
        </Panes>
      </Block>
    </Section>
  );
}

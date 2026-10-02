import { useEffect, useState } from "react";
import styles from "./gallery.module.css";
import { ThemeProvider, useTheme } from "../theme.tsx";
import { ToastProvider } from "../primitives/Toast.tsx";
import { TooltipProvider } from "../primitives/Tooltip.tsx";
import { Select } from "../primitives/Select.tsx";
import { Checkbox } from "../primitives/Checkbox.tsx";
import { PaneDensityContext, type PaneDensity, type PaneMode } from "./Frame.tsx";
import { DEFAULT_DENSITY, isDensity } from "../tokens/density.ts";
import { TokensSection } from "./sections/Tokens.tsx";
import { PrimitivesSection } from "./sections/Primitives.tsx";
import { ComponentsSection } from "./sections/Components.tsx";
import { ChatSection } from "./sections/Chat.tsx";
import { NavigationSection } from "./sections/Navigation.tsx";
import { BoardSection } from "./sections/Board.tsx";
import { ShellSection } from "./sections/Shell.tsx";
import { SettingsGallerySection } from "./sections/Settings.tsx";
import { LiveSection } from "./sections/Live.tsx";
import { CalmerSection } from "./sections/Calmer.tsx";
import { ServersSection } from "./sections/Servers.tsx";
import { ImagesGallerySection } from "./sections/Images.tsx";

const NAV: ReadonlyArray<readonly [string, ReadonlyArray<readonly [string, string]>]> = [
  ["App shell", [["shell-session", "Sidebar + transcript"]]],
  [
    "Tokens",
    [
      ["tokens-neutrals", "Neutral ramp"],
      ["tokens-semantic", "Semantic colors"],
      ["tokens-tones", "Status tones"],
      ["tokens-roles", "Agent roles"],
      ["tokens-accent", "Accent & diff"],
      ["tokens-type", "Type scale"],
      ["tokens-space", "Space / radii / elevation"],
      ["tokens-motion", "Motion & layers"],
      ["tokens-status", "Status vocabulary"],
      ["tokens-icons", "Icons"],
    ],
  ],
  [
    "Primitives",
    [
      ["p-button", "Button"],
      ["p-input", "Input"],
      ["p-textarea", "Textarea"],
      ["p-markdown-editor", "MarkdownEditor"],
      ["p-rowmenu", "RowMenu"],
      ["p-select", "Select"],
      ["p-checkbox", "Checkbox"],
      ["p-choice", "ChoiceList"],
      ["p-badge", "Badge"],
      ["p-card", "Card"],
      ["p-table", "Table"],
      ["p-tabs", "Tabs"],
      ["p-dialog", "Dialog"],
      ["p-page", "Page & header"],
      ["p-form", "Form layout"],
      ["p-toast", "Toast"],
      ["p-tooltip", "Tooltip"],
      ["p-loading", "Skeleton / Spinner"],
      ["p-empty", "EmptyState"],
      ["p-scroll", "ScrollArea"],
    ],
  ],
  [
    "Faces, states, costs",
    [
      ["k-status", "StatusMark"],
      ["k-faces", "PersonAvatar / ProjectAvatar"],
      ["k-pr", "PrChip"],
      ["k-cost", "Cost"],
      ["k-plan", "AgentPlan / PlanMeter"],
      ["k-document", "MarkdownDocument"],
    ],
  ],
  [
    "Components",
    [
      ["c-status", "StatusBadge"],
      ["c-avatar", "AgentAvatar"],
      ["c-numbers", "Cost / Tokens / Duration"],
      ["c-metric", "MetricTile"],
      ["c-event", "EventRow"],
      ["c-tree", "SessionTreeNode"],
      ["c-diff", "DiffView"],
      ["c-log", "LogStream"],
      ["c-finding", "FindingRow"],
      ["c-artifact", "ArtifactRow / Preview"],
      ["c-breadcrumb", "Breadcrumb"],
      ["c-composed", "Composed"],
    ],
  ],
  [
    "Chat",
    [
      ["ch-activity", "ActivityIndicator"],
      ["ch-thinking", "ThinkingBlock"],
      ["ch-tool", "ToolCallCard"],
      ["ch-event", "ChatEvent / ChatProgress"],
      ["ch-plan", "AgentPlan"],
      ["ch-markdown", "Markdown"],
      ["ch-message", "ChatMessage"],
      ["ch-steer", "Steer delivery"],
      ["ch-thread", "ChatThread"],
      ["ch-question", "QuestionCard"],
      ["ch-composer", "ChatComposer"],
      ["ch-images", "Images"],
      ["ch-realistic", "A real session (static)"],
      ["ch-transcript", "ChatTranscript (live)"],
    ],
  ],
  [
    "Navigation",
    [
      ["nav-triage", "Triage"],
      ["nav-tree", "NavTree"],
      ["nav-tree-menus", "NavTree row menus"],
      ["nav-sidebar", "Sidebar (realistic)"],
      ["nav-sidebar-states", "Sidebar states"],
    ],
  ],
  [
    "Board",
    [
      ["board-columns", "Status → lane"],
      ["board-project", "Project board (realistic)"],
      ["board-epic", "Epic board"],
      ["board-swimlanes", "Group by epic"],
      ["board-composed", "Beside the sidebar"],
      ["board-states", "Board states"],
    ],
  ],
  [
    "Settings",
    [
      ["s-layout", "Settings page"],
      ["s-markdown", "MarkdownDocument"],
      ["s-history", "PromptHistory"],
      ["s-epics", "EpicCard / EpicRow"],
    ],
  ],
  [
    "Image library",
    [
      ["i-editor", "CodeEditor"],
      ["i-picker", "ImagePicker"],
      ["i-build", "Queue, stages, state"],
      ["i-history", "ImageHistory"],
    ],
  ],
  [
    "Live work",
    [
      ["l-diff", "LiveDiff"],
      ["l-files", "FileGallery / FileViewer"],
      ["l-cost", "Cost, both halves"],
    ],
  ],
  [
    "Servers",
    [
      ["sv-state", "ServerStateMark"],
      ["sv-row", "ServerRow / ServerList"],
      ["sv-panel", "ServersPanel"],
      ["sv-tab", "The Servers tab"],
      ["sv-stages", "PreviewStages / ServersMoved / PreviewAlsoRunning"],
      ["sv-summary", "ServersSummary"],
      ["sv-recipes", "ServerRecipeTable / Dialog"],
    ],
  ],
];

const PANE_DENSITY_KEY = "dude.gallery.paneDensity";

/** The stored density choice, or null when missing or not one the control offers. */
function storedPaneDensity(): PaneDensity | null {
  const v = localStorage.getItem(PANE_DENSITY_KEY);
  return v === "both" || isDensity(v) ? v : null;
}

export function Gallery() {
  // The density control is the one persisted choice; the provider only mirrors
  // it onto the gallery chrome, so it keeps no storage key of its own.
  const stored = storedPaneDensity();
  return (
    <ThemeProvider storageKey="dude.gallery.theme" densityStorageKey={null} defaultDensity={isDensity(stored) ? stored : DEFAULT_DENSITY} defaultPreference="dark">
      <TooltipProvider>
        <ToastProvider>
          <Shell />
        </ToastProvider>
      </TooltipProvider>
    </ThemeProvider>
  );
}

function Shell() {
  const theme = useTheme();
  const [panes, setPanes] = useState<PaneMode>(() => (localStorage.getItem("dude.gallery.panes") as PaneMode | null) ?? "both");
  useEffect(() => localStorage.setItem("dude.gallery.panes", panes), [panes]);
  const [paneDensity, setPaneDensity] = useState<PaneDensity>(() => storedPaneDensity() ?? theme.density);
  useEffect(() => {
    localStorage.setItem(PANE_DENSITY_KEY, paneDensity);
    if (paneDensity !== "both") theme.setDensity(paneDensity);
  }, [paneDensity, theme.setDensity]);

  return (
    <div className={styles["app"]}>
      <nav className={styles["nav"]}>
        <div className={styles["brand"]}>
          dude
          <small>design system gallery</small>
        </div>
        {NAV.map(([group, items]) => (
          <div key={group} className={styles["navGroup"]}>
            <div className="ds-label" style={{ padding: "0 8px 4px" }}>
              {group}
            </div>
            {items.map(([id, label]) => (
              <a key={id} href={`#${id}`} className={styles["navLink"]}>
                {label}
              </a>
            ))}
          </div>
        ))}
        <div className={styles["controls"]}>
          <Select
            label="Panes"
            size="sm"
            value={panes}
            onValueChange={(v) => setPanes(v)}
            options={[
              { value: "both", label: "Dark + light" },
              { value: "dark", label: "Dark only" },
              { value: "light", label: "Light only" },
            ]}
          />
          <Select
            label="Density"
            size="sm"
            value={paneDensity}
            onValueChange={(v) => setPaneDensity(v)}
            options={[
              { value: "comfortable", label: "Comfortable" },
              { value: "compact", label: "Compact" },
              { value: "both", label: "Both, stacked" },
            ]}
          />
          <Select
            label="Gallery chrome"
            size="sm"
            value={theme.preference}
            onValueChange={(v) => theme.setPreference(v)}
            options={[
              { value: "dark", label: "Dark" },
              { value: "light", label: "Light" },
              { value: "system", label: "System" },
            ]}
          />
          <Checkbox label="Reduced motion" checked={theme.reducedMotion} onCheckedChange={(v) => theme.setReducedMotion(v === true)} />
        </div>
      </nav>
      <main className={styles["main"]}>
        <PaneDensityContext.Provider value={paneDensity}>
          <ShellSection mode={panes} />
          <TokensSection mode={panes} />
          <PrimitivesSection mode={panes} />
          <CalmerSection mode={panes} />
          <ComponentsSection mode={panes} />
          <ChatSection mode={panes} />
          <NavigationSection mode={panes} />
          <BoardSection mode={panes} />
          <SettingsGallerySection mode={panes} />
          <ImagesGallerySection mode={panes} />
          <LiveSection mode={panes} />
          <ServersSection mode={panes} />
        </PaneDensityContext.Provider>
      </main>
    </div>
  );
}

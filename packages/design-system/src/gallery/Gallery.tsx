import { useEffect, useState } from "react";
import styles from "./gallery.module.css";
import { ThemeProvider, useTheme } from "../theme.tsx";
import { ToastProvider } from "../primitives/Toast.tsx";
import { TooltipProvider } from "../primitives/Tooltip.tsx";
import { Select } from "../primitives/Select.tsx";
import { Checkbox } from "../primitives/Checkbox.tsx";
import type { PaneMode } from "./Frame.tsx";
import { TokensSection } from "./sections/Tokens.tsx";
import { PrimitivesSection } from "./sections/Primitives.tsx";
import { ComponentsSection } from "./sections/Components.tsx";
import { ChatSection } from "./sections/Chat.tsx";
import { NavigationSection } from "./sections/Navigation.tsx";
import { BoardSection } from "./sections/Board.tsx";

const NAV: ReadonlyArray<readonly [string, ReadonlyArray<readonly [string, string]>]> = [
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
      ["p-rowmenu", "RowMenu"],
      ["p-select", "Select"],
      ["p-checkbox", "Checkbox"],
      ["p-badge", "Badge"],
      ["p-card", "Card"],
      ["p-table", "Table"],
      ["p-tabs", "Tabs"],
      ["p-dialog", "Dialog"],
      ["p-toast", "Toast"],
      ["p-tooltip", "Tooltip"],
      ["p-loading", "Skeleton / Spinner"],
      ["p-empty", "EmptyState"],
      ["p-scroll", "ScrollArea"],
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
      ["ch-thread", "ChatThread"],
      ["ch-question", "QuestionCard"],
      ["ch-composer", "ChatComposer"],
      ["ch-realistic", "A real session (static)"],
      ["ch-transcript", "ChatTranscript (live)"],
    ],
  ],
  [
    "Navigation",
    [
      ["nav-human", "HumanAvatar"],
      ["nav-triage", "Triage & TriageRollup"],
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
];

export function Gallery() {
  return (
    <ThemeProvider storageKey="dude.gallery.theme" defaultPreference="dark">
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
        <TokensSection mode={panes} />
        <PrimitivesSection mode={panes} />
        <ComponentsSection mode={panes} />
        <ChatSection mode={panes} />
        <NavigationSection mode={panes} />
        <BoardSection mode={panes} />
      </main>
    </div>
  );
}

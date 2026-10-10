/*
 * The sidebar, collapsed: a narrow rail of faces and glyphs on the same
 * chrome shade, so the main pane gets the width and nothing is lost. Each
 * thing in it is the expanded sidebar's, in the same order, with its name
 * in a tooltip to the right. Proposed as `Sidebar collapsed` (the
 * component draws the rail itself); drawn here on its own so the mockup
 * needs no change to the design system.
 *
 *   home        dude's face: the welcome
 *   expand      chevron-right, `[` from anywhere
 *   search      expands the sidebar with the search focused, `/`
 *   waiting     the inbox glyph and the one loud count
 *   new         New session
 *   sessions    the bulb: the Sessions list; yours in its tooltip
 *   projects    each project's face; the needs-you diamond on its corner; its
 *               name and counts in its tooltip; pressed, its board
 *   band        organisation settings, and your face
 */

import { type ReactNode } from "react";
import { NeedsYouCount, PersonAvatar, ProjectAvatar } from "@dude/design-system/components";
import { Icon, type IconName } from "@dude/design-system";
import { Tooltip } from "@dude/design-system/primitives";
import type { NavProject, NavRef } from "../../../../packages/design-system/src/util/navModel.ts";
import { globalCounts, projectCounts } from "../../../../packages/design-system/src/util/navModel.ts";
import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";
import dudeSvg from "../../public/dude.svg?url";
import dudeOutlinedSvg from "../../public/dude-outlined.svg?url";
import s from "./rail.module.css";

function RailButton({ label, shortcut, current, onClick, children, testId }: {
  label: ReactNode;
  shortcut?: string | readonly string[];
  current?: boolean;
  onClick?: () => void;
  children: ReactNode;
  testId?: string;
}) {
  return (
    <Tooltip content={label} side="right" shortcut={shortcut as string | ReadonlyArray<string> | undefined}>
      <button type="button" className={s.item} data-current={current ? "true" : undefined} aria-current={current ? "page" : undefined}
        aria-label={typeof label === "string" ? label : undefined} onClick={onClick} data-testid={testId}>
        {children}
      </button>
    </Tooltip>
  );
}

function Glyph({ name }: { name: IconName }) {
  return <Icon name={name} size={16} className={s.glyph} />;
}

/** A project's counts in words, for its tooltip: the loud one first, nothing that is zero. */
function projectTip(p: NavProject, you: string | undefined): ReactNode {
  const c = projectCounts(p, you);
  const parts = [
    c.needs_you ? `${c.needs_you} need${c.needs_you === 1 ? "s" : ""} you` : null,
    c.active ? `${c.active} running` : null,
    c.failed ? `${c.failed} failed` : null,
    c.ready ? `${c.ready} ready` : null,
  ].filter(Boolean);
  return <span className={s.tip}><b>{p.name}</b>{parts.length ? <span className={s.tipMuted}>{parts.join(" · ")}</span> : null}</span>;
}

export function SidebarRail({ projects, selected, sessions, you, home, sessionsOpen, onHome, onExpand, onSearch, onWaiting, onNew, onSessions, onProject }: {
  projects: readonly NavProject[];
  selected: NavRef | null;
  sessions: readonly { id: string; title: string }[];
  you: Person;
  home: boolean;
  sessionsOpen: boolean;
  onHome: () => void;
  onExpand: () => void;
  onSearch: () => void;
  onWaiting: () => void;
  onNew: () => void;
  onSessions: () => void;
  onProject: (id: string) => void;
}) {
  const waiting = globalCounts(projects, you.id).needs_you;
  return (
    <nav className={s.rail} aria-label="Navigation" data-testid="sidebar-rail">
      <div className={s.top}>
        <RailButton label="Home · El Duderino" current={home} onClick={onHome} testId="rail-home">
          <span className="dudeMark" style={{ width: 28, height: 28 }} aria-hidden="true">
            <img className="dudeMarkLight" src={dudeSvg} alt="" style={{ width: 28, height: 28 }} />
            <img className="dudeMarkDark" src={dudeOutlinedSvg} alt="" style={{ width: 28, height: 28 }} />
          </span>
        </RailButton>
        <RailButton label="Expand sidebar" shortcut="[" onClick={onExpand} testId="rail-expand"><Glyph name="chevron-right" /></RailButton>
      </div>
      <div className={s.group}>
        <RailButton label="Find work" shortcut="/" onClick={onSearch}><Glyph name="search" /></RailButton>
        <RailButton label={waiting ? `Waiting on you · ${waiting}` : "Nothing waiting on you"} onClick={onWaiting}>
          {/* With something waiting, the count is the item: its diamond is the needs-you shape. */}
          {waiting ? <NeedsYouCount count={waiting} /> : <Glyph name="inbox" />}
        </RailButton>
        <RailButton label="New session" onClick={onNew}><Glyph name="plus" /></RailButton>
        <RailButton current={sessionsOpen} onClick={onSessions}
          label={<span className={s.tip}><b>Sessions</b>{sessions.slice(0, 4).map((x) => <span key={x.id} className={s.tipMuted}>{x.title}</span>)}</span>}>
          <Glyph name="brainstorm" />
        </RailButton>
      </div>
      <div className={s.projects} aria-label="Projects">
        {projects.map((p) => {
          const c = projectCounts(p, you.id);
          const current = selected?.kind === "project" && selected.id === p.id;
          return (
            <RailButton key={p.id} label={projectTip(p, you.id)} current={current} onClick={() => onProject(p.id)}>
              <ProjectAvatar project={{ id: p.id, name: p.name, imageUrl: p.imageUrl, colorSlot: p.colorSlot }} size={24} />
              {c.needs_you ? <span className={s.mark} aria-hidden="true" /> : null}
            </RailButton>
          );
        })}
      </div>
      <div className={s.band}>
        <RailButton label="Organisation settings"><Glyph name="building" /></RailButton>
        <RailButton label={`${you.name} · your settings`}><PersonAvatar person={you} size={28} /></RailButton>
      </div>
    </nav>
  );
}

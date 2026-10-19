/*
 * The sidebar's epic rows with the layers glyph the README and NavTree's
 * own header already promise (sidebar-epics, sidebar-epic-variants).
 *
 * NavTree is the design system's, unmodified: the glyph is portalled into
 * each real epic row, in a 14px cell (the width of the `.mark` column that
 * holds a task's status dot) right after the chevron, the order Breadcrumb
 * and the board's epic lane header use. It stands in for the one `Icon`
 * the fix adds to NavTreeRow's epic branch. Variant (b) also puts the
 * epic's roll-up word before its total.
 */

import { useEffect, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { Icon } from "@dude/design-system";
import { NavTree } from "@dude/design-system/components";
import type { NavOverrides } from "../../../../packages/design-system/src/util/navModel.ts";
import { EPICS, YOU, epicOfId, world, type EpicState } from "./data.ts";
import styles from "./pages.module.css";

export type EpicVariant = "glyph" | "glyph-state";

const STATE_WORD: Record<EpicState, string> = { planned: "Planned", active: "Active", done: "Done" };

interface Slot { readonly key: string; readonly glyph: HTMLElement; readonly state: HTMLElement | null }

/** Finds every epic row under `root` (within `scope`, a selector) and gives it a glyph cell, and a state cell for variant b. */
function useEpicSlots(root: HTMLElement | null, variant: EpicVariant, scope: string): ReadonlyArray<Slot> {
  const [slots, setSlots] = useState<ReadonlyArray<Slot>>([]);
  useEffect(() => {
    if (!root) return;
    const scan = () => {
      const out: Slot[] = [];
      root.querySelectorAll<HTMLElement>(`${scope} [role="treeitem"][data-kind="epic"]`).forEach((row) => {
        let glyph = row.querySelector<HTMLElement>(":scope > [data-epic-glyph]");
        if (!glyph) {
          glyph = document.createElement("span");
          glyph.dataset["epicGlyph"] = "";
          glyph.className = styles["epicSlot"]!;
          row.insertBefore(glyph, row.children[1] ?? null);
        }
        let state: HTMLElement | null = null;
        if (variant === "glyph-state") {
          state = row.querySelector<HTMLElement>(":scope > [data-epic-state]");
          if (!state) {
            state = document.createElement("span");
            state.dataset["epicState"] = "";
            state.className = styles["epicStateSlot"]!;
            const title = glyph.nextElementSibling;
            row.insertBefore(state, title?.nextElementSibling ?? null);
          }
        }
        out.push({ key: row.dataset["navKey"]!, glyph, state });
      });
      // Only a changed set of rows re-renders; the portals' own writes land here too.
      setSlots((prev) => prev.length === out.length && prev.every((p, i) => p.glyph === out[i]!.glyph && p.state === out[i]!.state) ? prev : out);
    };
    scan();
    const mo = new MutationObserver(scan);
    mo.observe(root, { childList: true, subtree: true });
    return () => mo.disconnect();
  }, [root, variant, scope]);
  return slots;
}

/** Puts the glyph (and, for b, the roll-up word) into every epic row under `root`. */
export function EpicGlyphs({ root, variant = "glyph", scope = "" }: { readonly root: HTMLElement | null; readonly variant?: EpicVariant; readonly scope?: string }) {
  const slots = useEpicSlots(root, variant, scope);
  return (
    <>
      {slots.map((s) => {
        const id = epicOfId(s.key.replace(/^epic:/, ""));
        const state = id ? EPICS[id].state : "active";
        const out: ReactNode[] = [createPortal(<Icon name="layers" size={14} />, s.glyph, `g-${s.key}`)];
        if (s.state) out.push(createPortal(<span data-state={state}>{STATE_WORD[state]}</span>, s.state, `s-${s.key}`));
        return out;
      })}
    </>
  );
}

/** Billing v2 open, Webhook reliability folded with what needs you in it, Usage alerts folded (planned). */
export const SIDEBAR_EXPANDED: NavOverrides = new Map([
  ["project:p_cp", true],
  ["epic:e_billing", true],
  ["epic:e_webhooks", false],
  ["epic:e_alerts", false],
  ["task:t_CP-43", false],
  ["task:t_CP-61", false],
]);

function Column({ variant, label, lead }: { readonly variant: EpicVariant | null; readonly label: string; readonly lead: string }) {
  const [root, setRoot] = useState<HTMLElement | null>(null);
  return (
    <section className={styles["variantCol"]} data-variant={variant ?? "today"} data-testid={`variant-${variant ?? "today"}`}>
      <h2 className={styles["variantHead"]}>{label}</h2>
      <p className={styles["muted"]}>{lead}</p>
      <div ref={setRoot} className={styles["variantTree"]} data-unfixed={variant ? undefined : ""}>
        <NavTree projects={[world()]} expanded={SIDEBAR_EXPANDED} selected={{ kind: "epic", id: "e_billing" }} aria-label={`Projects, ${label}`} />
        {variant ? <EpicGlyphs root={root} variant={variant} /> : null}
      </div>
    </section>
  );
}

/** sidebar-epics: the fixed sidebar is on the left; beside it, the same tree as it is today and with the fix. */
export function SidebarEpicsPage() {
  return (
    <div className={styles["doc"]} data-testid="sidebar-epics">
      <div className={styles["docInner"]}>
        <h1>Epic rows carry the layers glyph</h1>
        <p className={styles["lede"]}>
          The design system's README and NavTree's own header already say it: “epics carry the layers glyph and a total”.
          The epic row draws no glyph today, so an epic reads as a task without a key. The fix is one 14px icon after the
          chevron, in Breadcrumb's muted ink, where Breadcrumb and the board's epic lane put it. The sidebar on the left has
          it; Usage alerts is folded, Webhook reliability is folded with what needs you in it, Billing v2 is open.
        </p>
        <div className={styles["variants"]}>
          <Column variant={null} label="Today" lead="Title, needs-you count, total. Nothing says it is an epic." />
          <Column variant="glyph" label="With the fix" lead="The layers glyph, as Breadcrumb and the board's epic lane draw it." />
        </div>
      </div>
    </div>
  );
}

/** sidebar-epic-variants: what was considered, (a) glyph only (chosen) beside (b) glyph and the roll-up word. */
export function EpicVariantsPage() {
  return (
    <div className={styles["doc"]} data-testid="epic-variants">
      <div className={styles["docInner"]}>
        <h1>Epic rows in the sidebar: what was considered</h1>
        <p className={styles["lede"]}>
          Both draw the layers glyph, 14px, right after the chevron. (a) was chosen: the glyph alone tells an epic from a
          task. (b), which adds the roll-up word, was rejected: nearly every epic in the tree is Active, so the word repeats
          on every row and crowds the needs-you count. Reading as {YOU.name}.
        </p>
        <div className={styles["variants"]}>
          <Column variant="glyph" label="(a) Glyph only · chosen" lead="The README's rule as written: the layers glyph and a total." />
          <Column variant="glyph-state" label="(b) Glyph and roll-up · rejected" lead="Planned, Active or Done after the title, as the Epics page groups them." />
        </div>
      </div>
    </div>
  );
}

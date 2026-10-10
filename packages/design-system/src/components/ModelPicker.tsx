import { useId, useState, type ReactElement } from "react";
import { HARNESSES, HARNESS_LABEL, harnessMisfit, harnessWants, type Harness } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { RowMenu, type RowMenuAction } from "../primitives/RowMenu.tsx";
import styles from "./ModelPicker.module.css";

/** A model tier as the picker offers it. */
export interface PickerTier {
  readonly id: string;
  readonly name: string;
  /** The model it requests; null when the tier names none yet. */
  readonly model: string | null;
}

/** A session's own choice; null follows the organisation. */
export interface ModelChoice {
  readonly tier: string | null;
  readonly harness: Harness | null;
}

export interface ModelPickerProps {
  /** The organisation's tiers, in its order. */
  readonly tiers: ReadonlyArray<PickerTier>;
  /** The organisation's Brainstorm setting: what null stands for. */
  readonly organization: { readonly tier: PickerTier | null; readonly harness: Harness };
  readonly value: ModelChoice;
  /** Both halves, every time; null is "Organisation default". */
  readonly onChange?: ((choice: ModelChoice) => void) | undefined;
  /** A member who is not the owner: the chip, and no menu. */
  readonly readOnly?: boolean | undefined;
  /**
   * Why the pair it is on now cannot run (a tier removed, or the
   * organisation's half changed since it was chosen): the chip takes a
   * danger mark and the sentence is its description, shown under it.
   */
  readonly misfit?: string | null | undefined;
  /** The menu opened or closed: the rail reads the organisation's tiers on its first opening. */
  readonly onOpenChange?: ((open: boolean) => void) | undefined;
  /** A muted line atop the menu, for what it cannot list yet ("Could not load the tiers"). */
  readonly menuNote?: string | undefined;
  /** Draw the menu open inside this element: the gallery's still. Never in the product. */
  readonly previewMenu?: HTMLElement | null | undefined;
  readonly "data-testid"?: string | undefined;
}

const TIER_DEFAULT = "tier-default";
const HARNESS_DEFAULT = "harness-default";
const NO_MODEL = "names no model yet";

/** What the pair the picker shows would run on. */
export function effectiveModel({ tiers, organization, value }: Pick<ModelPickerProps, "tiers" | "organization" | "value">): {
  readonly tier: PickerTier | null;
  readonly harness: Harness;
  readonly isDefault: boolean;
} {
  const own = value.tier === null ? null : tiers.find((t) => t.id === value.tier) ?? null;
  return { tier: own ?? organization.tier, harness: value.harness ?? organization.harness, isDefault: value.tier === null && value.harness === null };
}

/** "Model: Claude (High) on Claude Code", the chip's accessible name and its words. */
export function modelWords(tier: PickerTier | null, harness: Harness): string {
  return `${tier?.name ?? "No tier"} on ${HARNESS_LABEL[harness]}`;
}

/**
 * Why `harness` cannot run `tier`'s model, short, for a disabled item;
 * null when it can. The rule and the words for what a harness takes are
 * the domain's (harnessMisfit, harnessWants).
 */
function misfit(harness: Harness, tier: PickerTier | null): string | null {
  if (!tier || !harnessMisfit(harness, tier.model)) return null;
  return `${HARNESS_LABEL[harness]} takes ${harnessWants(harness)}; ${tier.name} requests ${tier.model}`;
}

/**
 * Which model tier and harness a brainstorm session's agent runs on: a
 * quiet chip reading the effective pair ("Claude (High) · Claude Code",
 * with a muted "default" while both follow the organisation), opening a
 * float with two groups, Model tier and Harness, each led by
 * "Organisation default (…)". A pair the harness cannot run, or a tier
 * naming no model, is never offered: such an item is disabled and says
 * why. A pair that stopped fitting after it was chosen takes a danger mark
 * and says why under the chip. A change applies at the agent's next start;
 * the app says so.
 */
export function ModelPicker({ tiers, organization, value, onChange, readOnly, misfit: wrong, onOpenChange, menuNote, previewMenu, "data-testid": testId = "model-picker" }: ModelPickerProps) {
  const [open, setOpen] = useState(false);
  const describedBy = useId();
  const eff = effectiveModel({ tiers, organization, value });
  const words = modelWords(eff.tier, eff.harness);
  const name = `Model: ${words}${eff.isDefault ? " (organisation default)" : ""}`;
  const face = (
    <>
      {wrong ? <Icon name="warning" size={12} className={styles["danger"]} data-testid={`${testId}-misfit`} />
        : <Icon name="sparkle" size={12} className={styles["icon"]} />}
      <span className={cx(styles["tier"], "ds-cap")}>{eff.tier?.name ?? "No tier"}</span>
      <span className={cx(styles["harness"], "ds-cap")}>· {HARNESS_LABEL[eff.harness]}</span>
      {eff.isDefault ? <span className={styles["default"]} data-testid={`${testId}-default`}>default</span> : null}
    </>
  );
  const described = (chip: ReactElement) => wrong ? (
    <span className={styles["withMisfit"]}>
      {chip}
      <span id={describedBy} className={styles["misfit"]}>{wrong}</span>
    </span>
  ) : chip;
  if (readOnly || !onChange) {
    return described(
      <span className={cx(styles["chip"], styles["readOnly"])} data-testid={testId} data-readonly="" aria-describedby={wrong ? describedBy : undefined}>
        <span className="ds-sr-only">{name}</span>
        <span className={styles["face"]} aria-hidden>{face}</span>
      </span>,
    );
  }

  // Each item is checked against the other half as it would be after the pick.
  const harnessNow = eff.harness;
  const tierNow = eff.tier;
  const orgTierWords = organization.tier?.name ?? "no tier";
  const tierItems: RowMenuAction[] = [
    item(TIER_DEFAULT, `Organisation default (${orgTierWords})`, organization.tier?.model ?? undefined, misfit(harnessNow, organization.tier)),
    ...tiers.map((t) => item(t.id, t.name, t.model ?? undefined, t.model ? misfit(harnessNow, t) : NO_MODEL)),
  ];
  const harnessItems: RowMenuAction[] = [
    item(HARNESS_DEFAULT, `Organisation default (${HARNESS_LABEL[organization.harness]})`, undefined, misfit(organization.harness, tierNow)),
    ...HARNESSES.map((h) => item(h, HARNESS_LABEL[h], undefined, misfit(h, tierNow))),
  ];
  return described(
    <RowMenu
      align="start"
      className={styles["menu"]}
      open={previewMenu ? true : open}
      onOpenChange={(o) => {
        setOpen(o);
        onOpenChange?.(o);
      }}
      {...(previewMenu ? { forceMount: true as const, container: previewMenu } : {})}
      items={[
        ...(menuNote ? [{ kind: "note" as const, id: "model-note", text: menuNote }] : []),
        { kind: "radio", id: "tier", label: "Model tier", value: value.tier ?? TIER_DEFAULT, keepOpen: true,
          items: tierItems, onValueChange: (v) => onChange({ tier: v === TIER_DEFAULT ? null : v, harness: value.harness }) },
        { kind: "separator" },
        { kind: "radio", id: "harness", label: "Harness", value: value.harness ?? HARNESS_DEFAULT, keepOpen: true,
          items: harnessItems, onValueChange: (v) => onChange({ tier: value.tier, harness: v === HARNESS_DEFAULT ? null : (v as Harness) }) },
      ]}
      trigger={
        <button type="button" className={styles["chip"]} aria-label={name} aria-describedby={wrong ? describedBy : undefined} data-testid={testId}>
          {face}
          <Icon name="chevron-down" size={12} className={styles["icon"]} />
        </button>
      }
    />,
  );
}

// A refused item says why on its second line, in place of its model (the reason names it), so the
// words are read with the item and seen without hovering.
function item(id: string, label: string, model: string | undefined, reason: string | null): RowMenuAction {
  if (reason) return { id, label, description: reason, descriptionWraps: true, disabled: true };
  return { id, label, ...(model ? { description: model, descriptionMono: true } : {}) };
}

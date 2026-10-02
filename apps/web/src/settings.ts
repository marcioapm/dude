/**
 * What the settings screens compute from the API's settings: labels for
 * values, which roles a project changes, and a form's changes as a patch.
 * Pure, so the rules are tested apart from the screens.
 */

import type { DeliverySettings, FullDeliveryPolicy, RoleSettings } from "@dude/domain";

/** Time limits a person picks from, in minutes; null is none of the role's own. */
export const TIME_LIMITS: ReadonlyArray<number | null> = [null, 15, 20, 30, 45, 60, 120, 240, 480];

export function timeLimitLabel(minutes: number | null): string {
  if (minutes === null) return "No limit";
  if (minutes < 60) return `${minutes} min`;
  const h = minutes / 60;
  return Number.isInteger(h) ? `${h} ${h === 1 ? "hour" : "hours"}` : `${minutes} min`;
}

export function effortLabel(effort: string | null): string {
  return effort ? effort[0]!.toUpperCase() + effort.slice(1) : "Model’s default";
}

/** Whether a project changes anything about a role: a setting, or its prompt. */
export function roleChanged(role: RoleSettings): boolean {
  return (
    [role.model, role.effort, role.timeLimitMinutes, role.machineSize, role.image, role.enabled].some((s) => s?.source === "project") ||
    (role.prompt.project !== undefined && role.prompt.project.mode !== "inherit")
  );
}

/** How many delivery settings a project overrides. */
export function deliveryChanged(delivery: DeliverySettings): number {
  return Object.values(delivery).filter((s) => s.source === "project").length;
}

/**
 * A delivery form's changes: every field whose value differs from what the
 * settings say now. On a project each becomes an override; the rest keep
 * following the organization.
 */
export function deliveryPatch(current: DeliverySettings, form: FullDeliveryPolicy): Partial<FullDeliveryPolicy> {
  const out: Record<string, unknown> = {};
  for (const key of Object.keys(form) as Array<keyof FullDeliveryPolicy>) {
    if (!(key in current)) continue;
    if (JSON.stringify(form[key]) !== JSON.stringify(current[key].value)) out[key] = form[key];
  }
  return out as Partial<FullDeliveryPolicy>;
}

/** The delivery values the settings say, as a form starts from. */
export function deliveryValues(delivery: DeliverySettings): FullDeliveryPolicy {
  return Object.fromEntries(Object.entries(delivery).map(([k, s]) => [k, s.value])) as FullDeliveryPolicy;
}

/** The settings page a place names, if it is one of these; else the first. */
export function settingsPage<T extends string>(page: string | undefined, pages: readonly T[]): T {
  return pages.includes(page as T) ? (page as T) : pages[0]!;
}


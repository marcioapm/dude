import { stringSlot } from "@dude/design-system/components";

/** What dude goes by in a task's conversation and activity (docs/design/brand). */
export const DUDE_NAMES = ["The Dude", "El Duderino", "His Dudeness", "Duder"] as const;

/**
 * The name dude signs a task's messages with: one of his names, picked by
 * the task's id, so a task always hears from the same one and tasks vary.
 */
export function dudeName(taskId: string): (typeof DUDE_NAMES)[number] {
  return DUDE_NAMES[stringSlot(taskId, DUDE_NAMES.length)]!;
}

/**
 * dude himself: as drawn on a light theme, outlined in cream on a dark one,
 * where his navy hair would vanish. Both are in the page and CSS shows the
 * one the theme wants (app.css), so a theme switch needs no re-render.
 *
 * `size` in pixels, or "fill" to take the size of the box it sits in (a chat
 * avatar's, which follows the density).
 */
export function DudeMark({ size, className }: { size: number | "fill"; className?: string }) {
  const box = size === "fill" ? { width: "100%", height: "100%" } : { width: size, height: size };
  return (
    <span className={["dudeMark", className].filter(Boolean).join(" ")} style={box} aria-hidden="true">
      <img className="dudeMarkLight" src="/dude.svg" alt="" style={box} />
      <img className="dudeMarkDark" src="/dude-outlined.svg" alt="" style={box} />
    </span>
  );
}

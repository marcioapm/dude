/**
 * dude himself: as drawn on a light theme, outlined in cream on a dark one,
 * where his navy hair would vanish. Both are in the page and CSS shows the
 * one the theme wants (app.css), so a theme switch needs no re-render.
 */

/** What dude goes by in a task's conversation and activity (docs/design/brand). */
export const DUDE_NAMES = ["The Dude", "El Duderino", "His Dudeness", "Duder"] as const;

/**
 * The name dude signs a task's messages with: one of his names, picked by
 * the task's id, so a task always hears from the same one and tasks vary.
 */
export function dudeName(taskId: string): (typeof DUDE_NAMES)[number] {
  let h = 0;
  for (let i = 0; i < taskId.length; i++) h = (Math.imul(h, 31) + taskId.charCodeAt(i)) | 0;
  return DUDE_NAMES[Math.abs(h) % DUDE_NAMES.length]!;
}

export function DudeMark({ size, className }: { size: number; className?: string }) {
  return (
    <span className={["dudeMark", className].filter(Boolean).join(" ")} style={{ width: size, height: size }} aria-hidden="true">
      <img className="dudeMarkLight" src="/dude.svg" alt="" width={size} height={size} />
      <img className="dudeMarkDark" src="/dude-outlined.svg" alt="" width={size} height={size} />
    </span>
  );
}

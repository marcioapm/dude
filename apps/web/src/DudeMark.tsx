/**
 * dude himself: as drawn on a light theme, outlined in cream on a dark one,
 * where his navy hair would vanish. Both are in the page and CSS shows the
 * one the theme wants (app.css), so a theme switch needs no re-render.
 */

export function DudeMark({ size, className }: { size: number; className?: string }) {
  return (
    <span className={["dudeMark", className].filter(Boolean).join(" ")} style={{ width: size, height: size }} aria-hidden="true">
      <img className="dudeMarkLight" src="/dude.svg" alt="" width={size} height={size} />
      <img className="dudeMarkDark" src="/dude-outlined.svg" alt="" width={size} height={size} />
    </span>
  );
}

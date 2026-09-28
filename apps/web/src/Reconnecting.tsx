import { Icon } from "@dude/design-system";

/**
 * The live connection dropped: what is on screen may be behind. Says so
 * calmly and goes away by itself — the page re-reads once it is back.
 */
export function Reconnecting() {
  return (
    <div className="reconnecting" role="status" data-testid="reconnecting">
      <Icon name="retry" size={14} className="reconnectingIcon" />
      Reconnecting… what you see may be a moment behind.
    </div>
  );
}

import { Icon } from "@dude/design-system";
import { Button } from "@dude/design-system/primitives";

/**
 * The live connection dropped: what is on screen may be behind. Says so
 * calmly and goes away by itself — the page re-reads once it is back. For
 * a connection that does not come back, Reload is there.
 */
export function Reconnecting() {
  return (
    <div className="reconnecting" role="status" data-testid="reconnecting">
      <Icon name="retry" size={14} className="reconnectingIcon" />
      <span className="reconnectingText">Reconnecting… what you see may be a moment behind.</span>
      <Button size="sm" variant="quiet" onClick={() => window.location.reload()} data-testid="reconnecting-reload">
        Reload
      </Button>
    </div>
  );
}

import { useState, type ReactNode } from "react";
import { Button } from "../primitives/Button.tsx";
import { Dialog } from "../primitives/Dialog.tsx";
import { Input } from "../primitives/Input.tsx";
import { FormStack } from "../primitives/Layout.tsx";
import styles from "./KeyShownOnce.module.css";

export interface KeyShownOnceProps {
  readonly title: string;
  readonly secret: string;
  readonly onClose: () => void;
  readonly description?: string | undefined;
  /** How to use it, under the key. Strings are set as a code block. */
  readonly usage?: ReactNode;
  /** The label over `usage`. */
  readonly usageHeading?: string | undefined;
  /** Under the usage, in muted ink. */
  readonly usageNote?: ReactNode;
  /** Writes `secret` to the clipboard; the browser's by default. */
  readonly copy?: ((secret: string) => Promise<void>) | undefined;
}

/**
 * A secret, shown this once: the key in a read-only mono field that
 * selects itself on focus, Copy (which says Copied), and how to use it.
 * Done closes it; it is never shown again.
 */
export function KeyShownOnce({ title, secret, onClose, description, usage, usageHeading = "How to use it", usageNote, copy }: KeyShownOnceProps) {
  const [copied, setCopied] = useState(false);
  const write = copy ?? ((s: string) => navigator.clipboard?.writeText(s) ?? Promise.resolve());
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} size={usage ? "md" : "sm"} title={title}
      description={description ?? "Copy it now: it is not shown again."}
      footer={<Button variant="primary" onClick={onClose} data-testid="key-done">Done</Button>}>
      <FormStack>
        <Input label="Key" mono readOnly value={secret} onFocus={(e) => e.currentTarget.select()} data-testid="key-secret"
          trailing={<Button size="sm" variant="quiet" leadingIcon="copy" data-testid="key-copy"
            onClick={() => void write(secret).then(() => setCopied(true), () => undefined)}>{copied ? "Copied" : "Copy"}</Button>} />
        {usage ? (
          <div className={styles["usage"]}>
            <span className="ds-label">{usageHeading}</span>
            {typeof usage === "string" ? <pre className={styles["code"]} data-testid="key-usage"><code>{usage}</code></pre> : usage}
            {usageNote ? <p className={styles["note"]}>{usageNote}</p> : null}
          </div>
        ) : null}
      </FormStack>
    </Dialog>
  );
}

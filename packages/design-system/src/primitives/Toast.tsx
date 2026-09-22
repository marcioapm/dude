import * as RadixToast from "@radix-ui/react-toast";
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon, type IconName } from "../icons/index.tsx";
import { Button, IconButton } from "./Button.tsx";
import styles from "./Toast.module.css";

export type ToastTone = "neutral" | "info" | "success" | "attention" | "danger";

export interface ToastOptions {
  readonly title: ReactNode;
  readonly description?: ReactNode;
  readonly tone?: ToastTone | undefined;
  /** ms; `Infinity` keeps it until dismissed. Danger defaults to sticky. */
  readonly duration?: number | undefined;
  readonly action?: { readonly label: string; readonly onClick: () => void } | undefined;
}

interface ToastRecord extends ToastOptions {
  readonly id: number;
}

interface ToastApi {
  readonly toast: (options: ToastOptions) => number;
  readonly dismiss: (id: number) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

const TONE_ICON: Record<ToastTone, IconName | null> = {
  neutral: null,
  info: "info",
  success: "check",
  attention: "warning",
  danger: "alert",
};

/**
 * Toasts are for *transient outcomes of your own actions* ("Run aborted",
 * "Copied"). They are never the channel for agent events — those belong in
 * the event stream and the awaiting-human queue, where they persist.
 */
export function ToastProvider({ children }: { readonly children?: ReactNode }) {
  const [items, setItems] = useState<ToastRecord[]>([]);
  const dismiss = useCallback((id: number) => setItems((xs) => xs.filter((x) => x.id !== id)), []);
  const toast = useCallback((options: ToastOptions) => {
    const id = Date.now() + Math.random();
    setItems((xs) => [...xs.slice(-4), { ...options, id }]);
    return id;
  }, []);
  const api = useMemo<ToastApi>(() => ({ toast, dismiss }), [toast, dismiss]);

  return (
    <ToastContext.Provider value={api}>
      <RadixToast.Provider swipeDirection="right">
        {children}
        {items.map((t) => (
          <ToastItem key={t.id} record={t} onClose={() => dismiss(t.id)} />
        ))}
        <RadixToast.Viewport className={styles["viewport"]} label="Notifications" />
      </RadixToast.Provider>
    </ToastContext.Provider>
  );
}

function ToastItem({ record, onClose }: { readonly record: ToastRecord; readonly onClose: () => void }) {
  const tone = record.tone ?? "neutral";
  const icon = TONE_ICON[tone];
  const duration = record.duration ?? (tone === "danger" ? Number.POSITIVE_INFINITY : 5000);
  return (
    <RadixToast.Root
      className={cx(styles["root"], tone !== "neutral" && styles[tone])}
      duration={duration}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <span className={styles["icon"]} aria-hidden>
        {icon ? <Icon name={icon} size={14} /> : <span style={{ width: 14, display: "inline-block" }} />}
      </span>
      <div className={styles["text"]}>
        <RadixToast.Title className={styles["title"]}>{record.title}</RadixToast.Title>
        {record.description ? (
          <RadixToast.Description className={styles["description"]}>{record.description}</RadixToast.Description>
        ) : null}
      </div>
      <div className={styles["actions"]}>
        {record.action ? (
          <RadixToast.Action asChild altText={record.action.label}>
            <Button size="sm" variant="ghost" onClick={record.action.onClick}>
              {record.action.label}
            </Button>
          </RadixToast.Action>
        ) : null}
        <RadixToast.Close asChild>
          <IconButton icon="close" label="Dismiss" size="sm" />
        </RadixToast.Close>
      </div>
    </RadixToast.Root>
  );
}

export function useToast(): ToastApi {
  const api = useContext(ToastContext);
  if (!api) throw new Error("useToast must be used inside <ToastProvider>");
  return api;
}

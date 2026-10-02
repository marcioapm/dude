/**
 * Laying out and moving a field's images in MarkdownEditor's Preview. Each
 * edit rewrites the field's Markdown (imageLayout.ts) and hands it to the
 * editor's `setText`, the path typing takes, so the source stays the one
 * truth and the dialog's unsaved-changes logic sees it.
 *
 * A click selects an image and shows its toolbar; Alt+↑/↓ move it a block,
 * Delete/Backspace remove it, Esc or a click elsewhere deselects. A handle
 * on each side resizes it (pointer events: mouse, pen, touch). Dragging it
 * (HTML5 DnD) drops it on a slot: between the goal's blocks, or under a
 * criterion, in this field or another editor's on the page.
 */

import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type DragEvent, type KeyboardEvent, type PointerEvent, type ReactNode, type RefObject } from "react";
import { attachmentReferences } from "@dude/domain";
import { cx } from "../util/cx.ts";
import { IMAGE_SIZES, cutReference, insertReference, moveReference, moveReferenceTo, removeReference, snapWidth, withLayout, type FieldKind, type ImageLayout, type ImageSize } from "../util/imageLayout.ts";
import { ImageFigure, type AttachmentFrameProps } from "../components/Markdown.tsx";
import { IconButton } from "./Button.tsx";
import { Tooltip } from "./Tooltip.tsx";
import type { IconName } from "../icons/index.tsx";
import styles from "./ImageEditing.module.css";

const DRAG_TYPE = "application/x-dude-image";

/** The editors on the page that take a dragged image, by key: a drop on one cuts from the other. */
const fields = new Map<string, { kind: FieldKind; text: () => string; setText: (next: string) => void; select: (n: number | null) => void }>();

export interface ImageEditing {
  /** For Markdown's `attachmentFrame`; undefined when the field is not editable. */
  readonly frame: ((f: AttachmentFrameProps) => ReactNode) | undefined;
  /** Props for the Preview panel: drop handling, the slot line, undo. */
  readonly panel: {
    onDragOver?: (e: DragEvent<HTMLDivElement>) => void;
    onDragLeave?: (e: DragEvent<HTMLDivElement>) => void;
    onDrop?: (e: DragEvent<HTMLDivElement>) => void;
    onKeyDown?: (e: KeyboardEvent<HTMLDivElement>) => void;
  };
  /** The line where a dragged image would land. */
  readonly slotLine: ReactNode;
}

export function useImageEditing({ kind, enabled, text, setText, preview }: {
  kind: FieldKind | undefined;
  enabled: boolean;
  text: () => string;
  setText: (next: string) => void;
  preview: RefObject<HTMLDivElement | null>;
}): ImageEditing {
  const key = useId();
  const [selected, setSelected] = useState<number | null>(null);
  const [slot, setSlot] = useState<{ n: number; y: number } | null>(null);
  const history = useRef<string[]>([]);
  const on = Boolean(kind) && enabled;

  // Each edit is one step Ctrl/⌘+Z in Preview takes back.
  const edit = useCallback((next: string | null | undefined) => {
    const now = text();
    if (next == null || next === now) return;
    history.current.push(now);
    if (history.current.length > 100) history.current.shift();
    setText(next);
  }, [text, setText]);

  useEffect(() => {
    if (!on || !kind) return;
    fields.set(key, { kind, text, setText: (next) => edit(next), select: setSelected });
    return () => {
      fields.delete(key);
    };
  }, [on, kind, key, text, edit]);
  useEffect(() => {
    if (!on) setSelected(null);
  }, [on]);
  // One image is selected on the page at a time, whichever way it was picked.
  useEffect(() => {
    if (selected === null) return;
    for (const [k, f] of fields) if (k !== key) f.select(null);
  }, [selected, key]);

  // Esc deselects before the dialog sees it (Radix listens on the document,
  // in capture); a press outside the selected image deselects too.
  useEffect(() => {
    if (selected === null) return;
    const esc = (e: globalThis.KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      e.preventDefault();
      setSelected(null);
    };
    const outside = (e: globalThis.PointerEvent) => {
      const t = e.target as Element | null;
      if (t && (t.closest?.(`[data-image-key="${CSS.escape(key)}"][data-image-n="${selected}"]`) || t.closest?.("[data-radix-popper-content-wrapper]"))) return;
      setSelected(null);
    };
    window.addEventListener("keydown", esc, true);
    document.addEventListener("pointerdown", outside, true);
    return () => {
      window.removeEventListener("keydown", esc, true);
      document.removeEventListener("pointerdown", outside, true);
    };
  }, [selected, key]);

  const move = useCallback((n: number, dir: -1 | 1) => {
    if (!kind) return;
    const r = moveReference(text(), n, dir, kind);
    if (!r) return;
    edit(r.text);
    setSelected(r.index);
  }, [kind, text, edit]);

  const remove = useCallback((n: number) => {
    edit(removeReference(text(), n));
    setSelected(null);
  }, [text, edit]);

  const relayout = useCallback((n: number, layout: ImageLayout) => edit(withLayout(text(), n, layout)), [text, edit]);

  /** The slot nearest the pointer, and where its line is drawn, from the rendered blocks. */
  const slotAt = (clientY: number): { n: number; y: number } | null => {
    const panel = preview.current;
    const root = panel?.querySelector<HTMLElement>(":scope > div");
    if (!panel || !root || !kind) return null;
    const top = panel.getBoundingClientRect().top - panel.scrollTop;
    const edges: number[] = [];
    if (kind === "goal") {
      const blocks = [...root.children] as HTMLElement[];
      blocks.forEach((b) => edges.push(b.getBoundingClientRect().top));
      const last = blocks[blocks.length - 1];
      edges.push(last ? last.getBoundingClientRect().bottom : top);
    } else {
      // Under each criterion: its bottom edge.
      root.querySelectorAll<HTMLElement>(":scope > ul > li, :scope > ol > li").forEach((li) => edges.push(li.getBoundingClientRect().bottom));
      if (edges.length === 0) edges.push(root.getBoundingClientRect().bottom);
    }
    let best = 0;
    edges.forEach((y, i) => {
      if (Math.abs(y - clientY) < Math.abs((edges[best] ?? 0) - clientY)) best = i;
    });
    return { n: best, y: (edges[best] ?? top) - top };
  };

  const panel: ImageEditing["panel"] = on ? {
    onDragOver: (e) => {
      if (!e.dataTransfer.types.includes(DRAG_TYPE)) return;
      e.preventDefault();
      e.stopPropagation();
      e.dataTransfer.dropEffect = "move";
      setSlot(slotAt(e.clientY));
    },
    onDragLeave: (e) => {
      if (!(e.relatedTarget instanceof Node && e.currentTarget.contains(e.relatedTarget))) setSlot(null);
    },
    onDrop: (e) => {
      if (!e.dataTransfer.types.includes(DRAG_TYPE) || !kind) return;
      e.preventDefault();
      e.stopPropagation();
      const target = slotAt(e.clientY);
      setSlot(null);
      let from: { key: string; n: number };
      try {
        from = JSON.parse(e.dataTransfer.getData(DRAG_TYPE)) as { key: string; n: number };
      } catch {
        return;
      }
      const source = fields.get(from.key);
      if (!source || !target) return;
      if (from.key === key) {
        const r = moveReferenceTo(text(), from.n, target.n, kind);
        if (r) {
          edit(r.text);
          setSelected(r.index);
        }
        return;
      }
      const cut = cutReference(source.text(), from.n);
      if (!cut) return;
      const put = insertReference(text(), cut.ref, target.n, kind);
      source.setText(cut.text);
      source.select(null);
      edit(put.text);
      setSelected(indexAt(put.text, put.at));
    },
    onKeyDown: (e) => {
      if ((e.ctrlKey || e.metaKey) && !e.shiftKey && e.key.toLowerCase() === "z" && history.current.length > 0) {
        e.preventDefault();
        setText(history.current.pop()!);
        setSelected(null);
      }
    },
  } : {};

  const frame = on && kind ? (f: AttachmentFrameProps) => (
    <EditableImage key={`${f.n}:${f.id}`} {...f} fieldKey={key} selected={selected === f.n}
      onSelect={() => setSelected(f.n)} onLayout={(l) => relayout(f.n, l)} onMove={(d) => move(f.n, d)} onRemove={() => remove(f.n)}
      canMove={(d) => moveReference(text(), f.n, d, kind) !== null} />
  ) : undefined;

  const slotLine = slot ? <span className={styles["slot"]} style={{ top: slot.y }} data-testid="image-slot" data-slot={slot.n} aria-hidden /> : null;
  return { frame, panel, slotLine };
}

const indexAt = (text: string, at: number) => attachmentReferences(text).findIndex((r) => r.from === at);

const SIZES: ReadonlyArray<{ size: "small" | "medium" | "full"; icon: IconName; label: string }> = [
  { size: "small", icon: "image-small", label: "Small" },
  { size: "medium", icon: "image-medium", label: "Medium" },
  { size: "full", icon: "image-full", label: "Full width" },
];
const ALIGNS: ReadonlyArray<{ align: "left" | "center" | "right"; icon: IconName; label: string }> = [
  { align: "left", icon: "wrap-left", label: "Wrap left" },
  { align: "center", icon: "wrap-center", label: "Centre" },
  { align: "right", icon: "wrap-right", label: "Wrap right" },
];

function sizeName(size: ImageSize): string {
  return size === "full" ? "Full" : size === "small" ? "Small" : size === "medium" ? "Medium" : `${size} px`;
}

function EditableImage({ n, alt, layout, children, fieldKey, selected, onSelect, onLayout, onMove, onRemove, canMove }: AttachmentFrameProps & {
  fieldKey: string;
  selected: boolean;
  onSelect: () => void;
  onLayout: (l: ImageLayout) => void;
  onMove: (dir: -1 | 1) => void;
  onRemove: () => void;
  canMove: (dir: -1 | 1) => boolean;
}) {
  const ref = useRef<HTMLSpanElement>(null);
  const passClick = useRef(false);
  const resizing = useRef(false);
  const [drag, setDrag] = useState<{ width: number; label: string } | null>(null);
  // The frame clips what overflows it: with no room above the image in the panel, the toolbar goes below.
  const [below, setBelow] = useState(false);
  useLayoutEffect(() => {
    const fig = ref.current;
    const panel = fig?.closest<HTMLElement>('[role="tabpanel"]');
    if (!selected || !fig || !panel) return;
    setBelow(fig.getBoundingClientRect().top - panel.getBoundingClientRect().top < 44);
  }, [selected]);

  useEffect(() => {
    if (selected && !ref.current?.contains(document.activeElement)) ref.current?.focus({ preventScroll: true });
  }, [selected]);

  const open = () => {
    passClick.current = true;
    ref.current?.querySelector<HTMLElement>('[data-testid="markdown-image"]')?.click();
    passClick.current = false;
  };

  const onKey = (e: KeyboardEvent<HTMLSpanElement>) => {
    if (e.target !== e.currentTarget) return;
    if (e.altKey && (e.key === "ArrowUp" || e.key === "ArrowDown")) {
      e.preventDefault();
      onMove(e.key === "ArrowUp" ? -1 : 1);
    } else if (e.key === "Delete" || e.key === "Backspace") {
      e.preventDefault();
      onRemove();
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onSelect();
    }
  };

  /** A side handle: the width follows the pointer (twice as fast for a centred image, which grows both ways). */
  const startResize = (side: -1 | 1) => (e: PointerEvent<HTMLSpanElement>) => {
    const fig = ref.current;
    if (!fig || e.button !== 0) return;
    e.preventDefault();
    e.stopPropagation();
    const column = (fig.parentElement?.closest<HTMLElement>("[class]")?.clientWidth ?? fig.parentElement?.clientWidth) || 0;
    const max = Math.max(IMAGE_SIZES.small, fig.parentElement?.clientWidth || column);
    const startX = e.clientX;
    const startW = fig.getBoundingClientRect().width;
    const k = layout.align === "center" ? 2 : 1;
    const handle = e.currentTarget;
    handle.setPointerCapture?.(e.pointerId);
    resizing.current = true;
    let size: ImageSize = layout.size;
    const moveTo = (x: number) => {
      const raw = Math.min(max, Math.max(120, startW + side * k * (x - startX)));
      size = snapWidth(raw, max);
      const width = size === "full" ? max : typeof size === "number" ? size : IMAGE_SIZES[size];
      setDrag({ width, label: sizeName(size) });
    };
    const moved = (ev: globalThis.PointerEvent) => moveTo(ev.clientX);
    const done = () => {
      handle.removeEventListener("pointermove", moved);
      handle.removeEventListener("pointerup", done);
      handle.removeEventListener("pointercancel", done);
      resizing.current = false;
      setDrag(null);
      if (size !== layout.size) onLayout({ size, align: size === "full" ? "center" : layout.align });
    };
    handle.addEventListener("pointermove", moved);
    handle.addEventListener("pointerup", done);
    handle.addEventListener("pointercancel", done);
  };

  const full = layout.size === "full";
  return (
    <ImageFigure layout={layout} width={drag?.width}
      ref={ref}
      className={cx(styles["editable"], selected && styles["selected"])}
      tabIndex={0}
      role="group"
      aria-label={`Image ${alt || n + 1}: ${sizeName(layout.size)}, ${layout.align === "center" ? "on its own line" : `wrapped ${layout.align}`}`}
      data-image-key={fieldKey}
      data-image-n={n}
      data-selected={selected || undefined}
      draggable
      onClickCapture={(e) => {
        if (passClick.current) return;
        if (e.target instanceof Element && e.target.closest("[data-image-toolbar]")) return;
        e.preventDefault();
        e.stopPropagation();
        onSelect();
      }}
      onKeyDown={onKey}
      onDragStart={(e) => {
        if (resizing.current) {
          e.preventDefault();
          return;
        }
        e.stopPropagation();
        e.dataTransfer.effectAllowed = "move";
        e.dataTransfer.setData(DRAG_TYPE, JSON.stringify({ key: fieldKey, n }));
      }}
    >
      {children}
      {selected ? (
        <>
          <span className={cx(styles["toolbar"], layout.align === "right" && styles["toolbarRight"], below && styles["toolbarBelow"])} role="toolbar" aria-label="Image layout" data-image-toolbar
            onKeyDown={(e) => e.stopPropagation()}>
            {SIZES.map((s) => (
              <Tool key={s.size} icon={s.icon} label={s.label} pressed={layout.size === s.size}
                onClick={() => onLayout({ size: s.size, align: s.size === "full" ? "center" : layout.align })} />
            ))}
            <span className={styles["sep"]} aria-hidden />
            {ALIGNS.map((a) => (
              <Tool key={a.align} icon={a.icon} label={a.label} pressed={layout.align === a.align} disabled={full && a.align !== "center"}
                onClick={() => onLayout({ size: layout.size, align: a.align })} />
            ))}
            <span className={styles["sep"]} aria-hidden />
            <Tool icon="arrow-up" label="Move up" shortcut={["Alt", "↑"]} disabled={!canMove(-1)} onClick={() => onMove(-1)} />
            <Tool icon="arrow-down" label="Move down" shortcut={["Alt", "↓"]} disabled={!canMove(1)} onClick={() => onMove(1)} />
            <span className={styles["sep"]} aria-hidden />
            <Tool icon="external" label="Open" onClick={open} />
            <Tool icon="close" label="Remove" shortcut="Del" onClick={onRemove} />
          </span>
          <span className={cx(styles["handle"], styles["handleLeft"])} onPointerDown={startResize(-1)} draggable={false} aria-hidden data-testid="image-resize-left" />
          <span className={cx(styles["handle"], styles["handleRight"])} onPointerDown={startResize(1)} draggable={false} aria-hidden data-testid="image-resize-right" />
          {drag ? <span className={styles["sizeTip"]} data-testid="image-size-tip">{drag.label}</span> : null}
        </>
      ) : null}
    </ImageFigure>
  );
}

function Tool({ icon, label, pressed, disabled, shortcut, onClick }: {
  icon: IconName;
  label: string;
  pressed?: boolean;
  disabled?: boolean;
  shortcut?: string | ReadonlyArray<string>;
  onClick: () => void;
}) {
  return (
    <Tooltip content={label} shortcut={shortcut}>
      <IconButton size="sm" icon={icon} label={label} title={undefined} disabled={disabled}
        aria-pressed={pressed === undefined ? undefined : pressed} className={pressed ? styles["on"] : undefined}
        onClick={(e) => {
          e.stopPropagation();
          onClick();
        }} />
    </Tooltip>
  );
}

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

import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type DragEvent, type KeyboardEvent, type PointerEvent, type ReactNode, type RefObject } from "react";
import { cx } from "../util/cx.ts";
import { IMAGE_MIN_WIDTH, IMAGE_SIZES, cutReference, indexAt, insertReference, moveReference, moveReferenceTo, removeReference, snapWidth, withLayout, type FieldKind, type ImageLayout, type ImageSize } from "../util/imageLayout.ts";
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

export function useImageEditing({ kind, enabled, value, text, setText, preview }: {
  kind: FieldKind | undefined;
  enabled: boolean;
  /** The field's text as rendered. */
  value: string;
  text: () => string;
  setText: (next: string) => void;
  preview: RefObject<HTMLDivElement | null>;
}): ImageEditing {
  const key = useId();
  const [selected, setSelected] = useState<number | null>(null);
  const [slot, setSlot] = useState<{ n: number; y: number } | null>(null);
  const slotNow = useRef<{ n: number; y: number } | null>(null);
  const showSlot = useCallback((next: { n: number; y: number } | null) => {
    slotNow.current = next;
    setSlot(next);
  }, []);
  const history = useRef<Array<{ before: string; after: string }>>([]);
  const on = Boolean(kind) && enabled;

  // The selection is an index among the references: when the text changes by
  // any path but this hook's own (typing, an upload, Attach, the other field's
  // drop), it may name another image, so it goes. Cleared during render, so
  // no frame draws, or focuses, the wrong image.
  const own = useRef<string | null>(null);
  const [seen, setSeen] = useState(value);
  if (value !== seen) {
    setSeen(value);
    if (value !== own.current && selected !== null) setSelected(null);
  }
  const write = useCallback((next: string) => {
    own.current = next;
    setText(next);
  }, [setText]);

  // Each edit is one step Ctrl/⌘+Z in Preview takes back, while the field
  // still holds the text that edit produced.
  const edit = useCallback((next: string | null | undefined) => {
    const now = text();
    if (next == null || next === now) return;
    history.current.push({ before: now, after: next });
    if (history.current.length > 100) history.current.shift();
    write(next);
  }, [text, write]);

  useEffect(() => {
    if (!on || !kind) return;
    fields.set(key, { kind, text, setText, select: setSelected });
    return () => {
      fields.delete(key);
    };
  }, [on, kind, key, text, setText]);
  useEffect(() => {
    if (!on) setSelected(null);
  }, [on]);
  // One image is selected on the page at a time, whichever way it was picked.
  useEffect(() => {
    if (selected === null) return;
    for (const [k, f] of fields) if (k !== key) f.select(null);
  }, [selected, key]);
  useDeselectOutside(selected, key, preview, setSelected);

  // A move remounts the figure, which drops focus to the body. The moved
  // figure takes it back when focus was in this panel or the move was a drop;
  // a toolbar button that was pressed gets it back itself (`claim` names it).
  const claim = useRef<false | true | string>(false);
  const takeFocus = useCallback(() => {
    const c = claim.current;
    claim.current = false;
    return c;
  }, []);

  const move = useCallback((n: number, dir: -1 | 1) => {
    if (!kind) return;
    const r = moveReference(text(), n, dir, kind);
    if (!r) return;
    const active = document.activeElement;
    const button = active?.closest("[data-image-toolbar]") ? active.getAttribute("aria-label") : null;
    claim.current = button ?? Boolean(preview.current?.contains(active));
    edit(r.text);
    setSelected(r.index);
  }, [kind, text, edit, preview]);

  const remove = useCallback((n: number) => {
    const had = Boolean(preview.current?.contains(document.activeElement));
    edit(removeReference(text(), n));
    setSelected(null);
    // The figure is gone: focus stays in the field, on its panel.
    if (had) preview.current?.focus({ preventScroll: true });
  }, [text, edit, preview]);

  const relayout = useCallback((n: number, layout: ImageLayout) => edit(withLayout(text(), n, layout)), [text, edit]);

  const panel: ImageEditing["panel"] = on && kind ? {
    onDragOver: (e) => {
      if (!e.dataTransfer.types.includes(DRAG_TYPE)) return;
      e.preventDefault();
      e.stopPropagation();
      e.dataTransfer.dropEffect = "move";
      // dragover repeats while the pointer rests: an unchanged slot sets no state, so nothing renders.
      const next = slotAt(preview.current, kind, e.clientY);
      const s = slotNow.current;
      if (s === next || (s && next && s.n === next.n && s.y === next.y)) return;
      showSlot(next);
    },
    onDragLeave: (e) => {
      if (!(e.relatedTarget instanceof Node && e.currentTarget.contains(e.relatedTarget))) showSlot(null);
    },
    onDrop: (e) => {
      if (!e.dataTransfer.types.includes(DRAG_TYPE)) return;
      e.preventDefault();
      e.stopPropagation();
      const target = slotAt(preview.current, kind, e.clientY);
      showSlot(null);
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
          claim.current = true;
          edit(r.text);
          setSelected(r.index);
        }
        return;
      }
      const cut = cutReference(source.text(), from.n);
      if (!cut) return;
      const put = insertReference(text(), cut.ref, target.n, kind);
      // A drag between fields changes two texts: it is not an undo step in either, so neither goes through `edit`.
      source.setText(cut.text);
      source.select(null);
      claim.current = true;
      write(put.text);
      setSelected(indexAt(put.text, put.at));
    },
    onKeyDown: (e) => {
      if (!((e.ctrlKey || e.metaKey) && !e.shiftKey && e.key.toLowerCase() === "z")) return;
      const top = history.current[history.current.length - 1];
      if (!top) return;
      e.preventDefault();
      // Changed since by typing, an upload, or a drop: restoring a snapshot would lose that.
      if (text() !== top.after) {
        history.current = [];
        return;
      }
      history.current.pop();
      write(top.before);
      setSelected(null);
    },
  } : {};

  // Whether the selected image can move up and down: one pair of trial moves
  // per text and selection, not one per render (a resize renders per pointermove).
  const movable = useMemo(() => (on && kind && selected !== null
    ? { up: moveReference(value, selected, -1, kind) !== null, down: moveReference(value, selected, 1, kind) !== null }
    : { up: false, down: false }), [on, kind, value, selected]);

  const frame = on && kind ? (f: AttachmentFrameProps) => (
    <EditableImage key={`${f.n}:${f.id}`} {...f} fieldKey={key} selected={selected === f.n}
      onSelect={() => setSelected(f.n)} onDeselect={() => setSelected(null)} onLayout={(l) => relayout(f.n, l)} onMove={(d) => move(f.n, d)} onRemove={() => remove(f.n)}
      canUp={movable.up} canDown={movable.down} takeFocus={takeFocus} />
  ) : undefined;

  const slotLine = slot ? <span className={styles["slot"]} style={{ top: slot.y }} data-testid="image-slot" data-slot={slot.n} aria-hidden /> : null;
  return { frame, panel, slotLine };
}

/**
 * Esc inside this preview panel deselects before the dialog sees it (Radix
 * listens on the document, in capture); Esc anywhere else is left alone. A
 * press outside the selected image (Tooltip portals aside) deselects too.
 */
function useDeselectOutside(selected: number | null, key: string, preview: RefObject<HTMLDivElement | null>, select: (n: null) => void) {
  useEffect(() => {
    if (selected === null) return;
    const esc = (e: globalThis.KeyboardEvent) => {
      if (e.key !== "Escape" || !(e.target instanceof Node && preview.current?.contains(e.target))) return;
      e.stopPropagation();
      e.preventDefault();
      select(null);
    };
    const outside = (e: globalThis.PointerEvent) => {
      const t = e.target as Element | null;
      if (t && (t.closest?.(`[data-image-key="${CSS.escape(key)}"][data-image-n="${selected}"]`) || t.closest?.("[data-radix-popper-content-wrapper]"))) return;
      select(null);
    };
    window.addEventListener("keydown", esc, true);
    document.addEventListener("pointerdown", outside, true);
    return () => {
      window.removeEventListener("keydown", esc, true);
      document.removeEventListener("pointerdown", outside, true);
    };
  }, [selected, key, preview, select]);
}

/** The slot nearest the pointer, and where its line is drawn, from the rendered blocks. */
function slotAt(panel: HTMLDivElement | null, kind: FieldKind, clientY: number): { n: number; y: number } | null {
  const root = panel?.querySelector<HTMLElement>(":scope > div");
  if (!panel || !root) return null;
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
}

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

function EditableImage({ n, alt, layout, children, fieldKey, selected, onSelect, onDeselect, onLayout, onMove, onRemove, canUp, canDown, takeFocus }: AttachmentFrameProps & {
  fieldKey: string;
  selected: boolean;
  onSelect: () => void;
  onDeselect: () => void;
  onLayout: (l: ImageLayout) => void;
  onMove: (dir: -1 | 1) => void;
  onRemove: () => void;
  canUp: boolean;
  canDown: boolean;
  takeFocus: () => boolean | string;
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

  // Focus follows a selection made in this panel only, never pulled from elsewhere.
  useEffect(() => {
    const fig = ref.current;
    if (!selected || !fig || fig.contains(document.activeElement)) return;
    const inPanel = Boolean(fig.closest('[role="tabpanel"]')?.contains(document.activeElement));
    const claim = takeFocus();
    const button = typeof claim === "string" ? fig.querySelector<HTMLButtonElement>(`[data-image-toolbar] button[aria-label="${CSS.escape(claim)}"]`) : null;
    // Pressed again at the end of the list, the button is disabled: the figure takes focus.
    if (button && !button.disabled) button.focus({ preventScroll: true });
    else if (claim || inPanel) fig.focus({ preventScroll: true });
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
      const raw = Math.min(max, Math.max(IMAGE_MIN_WIDTH, startW + side * k * (x - startX)));
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
          <ImageToolbar layout={layout} below={below} canUp={canUp} canDown={canDown} onLayout={onLayout} onMove={onMove} onRemove={onRemove}
            onOpen={() => {
              open();
              // The viewer takes the next Esc, not this selection.
              onDeselect();
            }} />
          <span className={cx(styles["handle"], styles["handleLeft"])} onPointerDown={startResize(-1)} draggable={false} aria-hidden data-testid="image-resize-left" />
          <span className={cx(styles["handle"], styles["handleRight"])} onPointerDown={startResize(1)} draggable={false} aria-hidden data-testid="image-resize-right" />
          {drag ? <span className={styles["sizeTip"]} data-testid="image-size-tip">{drag.label}</span> : null}
        </>
      ) : null}
    </ImageFigure>
  );
}

function ImageToolbar({ layout, below, canUp, canDown, onLayout, onMove, onOpen, onRemove }: {
  layout: ImageLayout;
  below: boolean;
  canUp: boolean;
  canDown: boolean;
  onLayout: (l: ImageLayout) => void;
  onMove: (dir: -1 | 1) => void;
  onOpen: () => void;
  onRemove: () => void;
}) {
  const full = layout.size === "full";
  return (
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
      <Tool icon="arrow-up" label="Move up" shortcut={["Alt", "↑"]} disabled={!canUp} onClick={() => onMove(-1)} />
      <Tool icon="arrow-down" label="Move down" shortcut={["Alt", "↓"]} disabled={!canDown} onClick={() => onMove(1)} />
      <span className={styles["sep"]} aria-hidden />
      <Tool icon="external" label="Open" onClick={onOpen} />
      <Tool icon="close" label="Remove" shortcut="Del" onClick={onRemove} />
    </span>
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

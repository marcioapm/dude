import { useEffect, useRef } from "react";
import { autocompletion, closeBrackets, completionKeymap, type CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { HighlightStyle, StreamLanguage, syntaxHighlighting } from "@codemirror/language";
import { dockerFile } from "@codemirror/legacy-modes/mode/dockerfile";
import { lintGutter, setDiagnostics, type Diagnostic } from "@codemirror/lint";
import { highlightSelectionMatches, searchKeymap } from "@codemirror/search";
import { Compartment, EditorState, type Extension } from "@codemirror/state";
import { drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers } from "@codemirror/view";
import { tags as t } from "@lezer/highlight";
import type { CodeDiagnostic, CodeEditorProps } from "./CodeEditor.tsx";
import styles from "./CodeEditor.module.css";

/*
 * The editor itself, in the chunk CodeEditor loads lazily. Every colour is
 * a token, so light, dark and compact follow the page with no theme of
 * CodeMirror's own.
 */

const theme = EditorView.theme({
  "&": {
    color: "var(--ds-color-text-primary)",
    backgroundColor: "transparent",
    fontSize: "var(--ds-text-mono)",
    height: "100%",
  },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": { fontFamily: "var(--ds-font-mono)", lineHeight: "var(--ds-code-line)" },
  ".cm-content": { padding: "var(--ds-space-8) 0", caretColor: "var(--ds-color-text-primary)" },
  ".cm-line": { padding: "0 var(--ds-space-12) 0 var(--ds-space-8)" },
  ".cm-gutters": { backgroundColor: "transparent", color: "var(--ds-color-text-muted)", border: "none" },
  ".cm-lineNumbers .cm-gutterElement": { padding: "0 var(--ds-space-4) 0 var(--ds-space-12)", minWidth: "28px" },
  ".cm-activeLine": { backgroundColor: "var(--ds-color-hover-wash)" },
  ".cm-activeLineGutter": { backgroundColor: "var(--ds-color-hover-wash)", color: "var(--ds-color-text-primary)" },
  "&.cm-focused .cm-cursor": { borderLeftColor: "var(--ds-color-text-primary)" },
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": { backgroundColor: "var(--ds-color-selection) !important" },
  ".cm-selectionMatch": { backgroundColor: "var(--ds-color-active-wash)" },
  ".cm-lintRange-error": { backgroundImage: "none", textDecoration: "underline wavy var(--ds-tone-danger-fg)", textUnderlineOffset: "3px" },
  ".cm-lintRange-warning": { backgroundImage: "none", textDecoration: "underline wavy var(--ds-tone-attention-fg)", textUnderlineOffset: "3px" },
  ".cm-gutter-lint": { width: "14px" },
  ".cm-lint-marker": { width: "10px", height: "10px" },
  ".cm-tooltip": {
    backgroundColor: "var(--ds-color-overlay)",
    color: "var(--ds-color-text-primary)",
    border: "none",
    borderRadius: "var(--ds-radius-float)",
    boxShadow: "var(--ds-shadow-2)",
    overflow: "hidden",
  },
  ".cm-tooltip-lint": { padding: "var(--ds-space-4)" },
  ".cm-diagnostic": { padding: "var(--ds-space-4) var(--ds-space-8)", fontFamily: "var(--ds-font-sans)", fontSize: "var(--ds-text-sm)", borderLeft: "none" },
  ".cm-diagnostic-error": { color: "var(--ds-tone-danger-fg)" },
  ".cm-diagnostic-warning": { color: "var(--ds-tone-attention-fg)" },
  ".cm-tooltip.cm-tooltip-autocomplete > ul": { fontFamily: "var(--ds-font-mono)", maxHeight: "16em", padding: "var(--ds-space-4)" },
  ".cm-tooltip.cm-tooltip-autocomplete > ul > li": {
    padding: "2px var(--ds-space-8)",
    borderRadius: "var(--ds-radius-control)",
    lineHeight: "var(--ds-code-line)",
  },
  ".cm-tooltip-autocomplete ul li[aria-selected]": { backgroundColor: "var(--ds-color-accent-subtle)", color: "var(--ds-color-text-primary)" },
  ".cm-completionDetail": { fontFamily: "var(--ds-font-sans)", fontStyle: "normal", color: "var(--ds-color-text-muted)", marginLeft: "var(--ds-space-12)" },
  ".cm-completionMatchedText": { textDecoration: "underline", color: "var(--ds-color-accent-text)" },
  ".cm-completionIcon": { display: "none" },
  ".cm-panels": { backgroundColor: "var(--ds-color-chrome)", color: "var(--ds-color-text-primary)", border: "none" },
  ".cm-panel.cm-search": { fontFamily: "var(--ds-font-sans)", fontSize: "var(--ds-text-sm)", padding: "var(--ds-space-4) var(--ds-space-8)" },
  ".cm-textfield": {
    backgroundColor: "var(--ds-color-field-bg)",
    color: "var(--ds-color-text-primary)",
    border: "1px solid var(--ds-color-border)",
    borderRadius: "var(--ds-radius-control)",
  },
  ".cm-button": {
    backgroundImage: "none",
    backgroundColor: "var(--ds-color-raised)",
    color: "var(--ds-color-text-primary)",
    border: "none",
    borderRadius: "var(--ds-radius-control)",
  },
});

const highlight = HighlightStyle.define([
  { tag: t.keyword, color: "var(--ds-color-accent-text)", fontWeight: "600" },
  { tag: t.comment, color: "var(--ds-color-text-muted)", fontStyle: "italic" },
  { tag: [t.string, t.special(t.string)], color: "var(--ds-tone-success-fg)" },
  { tag: [t.variableName, t.definition(t.variableName)], color: "var(--ds-tone-attention-fg)" },
  { tag: [t.number, t.atom], color: "var(--ds-tone-info-fg)" },
]);

/** A diagnostic's line and columns as positions in the document. */
function toDiagnostics(state: EditorState, list: ReadonlyArray<CodeDiagnostic> | undefined): Diagnostic[] {
  const out: Diagnostic[] = [];
  for (const d of list ?? []) {
    if (d.line < 1 || d.line > state.doc.lines) continue;
    const line = state.doc.line(d.line);
    const from = Math.min(line.from + d.from, line.to);
    const to = Math.max(from, Math.min(line.from + d.to, line.to));
    out.push({ from, to, severity: d.severity, message: d.message });
  }
  return out;
}

export default function CodeEditorCore({ value, onChange, language = "plain", diagnostics, complete, readOnly, "aria-label": label }: CodeEditorProps) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const latest = useRef({ onChange, complete });
  latest.current = { onChange, complete };
  const editable = useRef(new Compartment());

  useEffect(() => {
    const source = (ctx: CompletionContext): CompletionResult | null => {
      const fn = latest.current.complete;
      if (!fn) return null;
      const word = ctx.matchBefore(/[\w:/.@-]*/);
      const line = ctx.state.doc.lineAt(ctx.pos);
      const before = line.text.slice(0, ctx.pos - line.from);
      if (!ctx.explicit && (!word || word.from === word.to)) return null;
      const options = fn({ before, word: word?.text ?? "" });
      if (!options || options.length === 0) return null;
      return {
        from: word?.from ?? ctx.pos,
        options: options.map((o) => ({ label: o.label, ...(o.detail ? { detail: o.detail } : {}), ...(o.type ? { type: o.type } : {}), ...(o.boost ? { boost: o.boost } : {}) })),
        validFor: /^[\w:/.@-]*$/,
      };
    };
    const extensions: Extension[] = [
      lineNumbers(),
      highlightActiveLineGutter(),
      lintGutter(),
      history(),
      drawSelection(),
      highlightActiveLine(),
      highlightSelectionMatches(),
      closeBrackets(),
      autocompletion({ override: [source], icons: false }),
      keymap.of([...completionKeymap, ...searchKeymap, ...historyKeymap, ...defaultKeymap, indentWithTab]),
      theme,
      syntaxHighlighting(highlight),
      editable.current.of([EditorState.readOnly.of(Boolean(readOnly)), EditorView.editable.of(!readOnly)]),
      EditorView.contentAttributes.of({ "aria-label": label }),
      EditorView.updateListener.of((u) => {
        if (u.docChanged) latest.current.onChange?.(u.state.doc.toString());
      }),
    ];
    if (language === "dockerfile") extensions.push(StreamLanguage.define(dockerFile));
    const v = new EditorView({ parent: host.current!, state: EditorState.create({ doc: value, extensions }) });
    view.current = v;
    return () => {
      v.destroy();
      view.current = null;
    };
    // The editor is made once; value, readOnly and diagnostics follow below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [language]);

  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } });
    }
  }, [value]);

  useEffect(() => {
    view.current?.dispatch({
      effects: editable.current.reconfigure([EditorState.readOnly.of(Boolean(readOnly)), EditorView.editable.of(!readOnly)]),
    });
  }, [readOnly]);

  useEffect(() => {
    const v = view.current;
    if (v) v.dispatch(setDiagnostics(v.state, toDiagnostics(v.state, diagnostics)));
  }, [diagnostics, value]);

  return <div ref={host} className={styles["cm"]} data-code-editor="" />;
}

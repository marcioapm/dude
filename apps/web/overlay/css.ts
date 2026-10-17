// The overlay's look, inside its closed shadow root: no page CSS reaches it
// and none of this leaks. Every value is a design-system token
// (tokens.gen.ts, generated from the DS source at build time, both themes),
// so it reads as dude does and draws in the person's light or dark: the
// OS's until dude answers, then dude's (data-theme on .root, set by
// overlay.ts). The pick is the accent, the colour of selection, with a white
// halo so it holds on any page. test/css.test.ts refuses a raw colour here.
export const CSS = `
:host{all:initial}
*{box-sizing:border-box}
.root{position:fixed;inset:0;pointer-events:none;z-index:2147483647;font-family:var(--ds-font-sans);font-size:var(--ds-text-sm);line-height:var(--ds-leading-snug);color:var(--ds-color-text-primary);-webkit-font-smoothing:antialiased}
.layer{display:contents;pointer-events:auto}
.mono{font-family:var(--ds-font-mono);font-size:var(--ds-text-xs)}
.dim{color:var(--ds-color-text-muted)}
.sp{flex:1}
b{font-weight:var(--ds-weight-semibold);color:var(--ds-color-text-strong)}
kbd{display:inline-grid;place-items:center;min-width:16px;height:16px;padding:0 var(--ds-space-4);margin:0 1px;border-radius:var(--ds-radius-mark);background:var(--ds-color-sunken);box-shadow:inset 0 0 0 1px var(--ds-color-border-subtle);font-family:var(--ds-font-mono);font-size:var(--ds-text-2xs);line-height:1;color:var(--ds-color-text-secondary)}
button,.btn{display:inline-flex;align-items:center;justify-content:center;gap:var(--ds-space-6);height:var(--ds-size-control-sm);padding:0 var(--ds-space-8);border:0;border-radius:var(--ds-radius-control);background:var(--ds-color-active-wash);color:var(--ds-color-text-primary);font-family:var(--ds-font-sans);font-size:var(--ds-text-sm);font-weight:var(--ds-weight-medium);line-height:1;cursor:pointer;white-space:nowrap;text-decoration:none;transition:background-color var(--ds-duration-fast) var(--ds-ease-standard)}
button:hover:not(:disabled),.btn:hover{background:color-mix(in srgb,var(--ds-color-text-primary) 13%,transparent)}
button>svg,.btn>svg{color:var(--ds-color-text-secondary)}
button:hover>svg,.btn:hover>svg{color:currentColor}
button:focus-visible,.btn:focus-visible,textarea:focus-visible,input:focus-visible,summary:focus-visible{outline:var(--ds-focus-ring-width) solid var(--ds-color-focus-ring);outline-offset:var(--ds-focus-ring-offset)}
button:disabled{opacity:.45;cursor:not-allowed}
.primary{background:var(--ds-color-accent);color:var(--ds-color-on-accent)}
.primary>svg,.primary:hover>svg{color:currentColor}
.primary:hover:not(:disabled),.btn.primary:hover{background:var(--ds-color-accent-hover)}
.primary:disabled{opacity:1;background:var(--ds-color-active-wash);color:var(--ds-color-text-secondary)}
.on{background:var(--ds-color-accent-subtle);color:var(--ds-color-accent-text)}
.on>svg,.on:hover>svg{color:currentColor}
.on:hover:not(:disabled){background:var(--ds-color-accent-subtle)}
.icon{width:var(--ds-size-control-sm);padding:0;background:transparent;color:var(--ds-color-text-secondary)}
.icon:hover:not(:disabled){background:var(--ds-color-hover-wash);color:var(--ds-color-text-primary)}
svg{flex:none}
.bar{position:fixed;right:20px;bottom:20px;display:flex;flex-direction:column;max-width:calc(100vw - 24px);border-radius:var(--ds-radius-float);background:var(--ds-color-overlay);box-shadow:var(--ds-shadow-3);overflow:hidden}
.row{display:flex;align-items:center;gap:var(--ds-space-8);padding:var(--ds-space-6) var(--ds-space-6) var(--ds-space-6) var(--ds-space-4)}
.grip{display:inline-grid;place-items:center;width:16px;height:var(--ds-size-control-sm);color:var(--ds-color-text-muted);cursor:grab;touch-action:none}
.who{display:inline-flex;align-items:center;gap:var(--ds-space-8);margin-right:var(--ds-space-4);min-width:0}
.mark{display:inline-grid;place-items:center;flex:none;width:var(--ds-size-chip);height:var(--ds-size-chip);border-radius:var(--ds-radius-control);background:var(--ds-color-accent);color:var(--ds-color-on-accent);font-weight:var(--ds-weight-semibold);font-size:var(--ds-text-sm)}
.whoText{display:flex;flex-direction:column;line-height:var(--ds-leading-tight);min-width:0}
.whoText b{font-size:var(--ds-text-xs);font-weight:var(--ds-weight-medium)}
.conn{font-size:var(--ds-text-2xs);color:var(--ds-tone-success-fg);white-space:nowrap}
.conn:before{content:"";display:inline-block;width:6px;height:6px;margin-right:var(--ds-space-4);border-radius:var(--ds-radius-full);background:currentColor;vertical-align:1px}
.off{color:var(--ds-tone-attention-fg)}
.drafts{display:inline-flex;align-items:center;gap:var(--ds-space-4);padding:0 var(--ds-space-4);font-size:var(--ds-text-xs);color:var(--ds-color-text-muted);white-space:nowrap}
.hint{display:flex;align-items:center;gap:var(--ds-space-12);padding:var(--ds-space-6) var(--ds-space-12);background:var(--ds-color-chrome);color:var(--ds-color-text-muted);font-size:var(--ds-text-xs)}
.pill{position:fixed;right:20px;bottom:20px;padding:var(--ds-space-4);height:auto;border-radius:var(--ds-radius-full);background:var(--ds-color-overlay);box-shadow:var(--ds-shadow-2)}
.pill:hover:not(:disabled){background:var(--ds-color-overlay)}
.pill .mark{border-radius:var(--ds-radius-full)}
.panel{display:flex;flex-direction:column;min-width:420px;max-width:100%;max-height:60vh;overflow:auto;background:var(--ds-color-overlay)}
.ph{display:flex;align-items:center;justify-content:space-between;gap:var(--ds-space-8);padding:var(--ds-space-12) var(--ds-space-12) var(--ds-space-6)}
.pp{margin:0;padding:0 var(--ds-space-12) var(--ds-space-12);color:var(--ds-color-text-secondary)}
.pf{display:flex;gap:var(--ds-space-8);padding:0 var(--ds-space-12) var(--ds-space-12)}
ul{list-style:none;margin:0;padding:0 var(--ds-space-6) var(--ds-space-6);display:flex;flex-direction:column;gap:var(--ds-space-2)}
li{display:flex;align-items:flex-start;gap:var(--ds-space-8);padding:var(--ds-space-6);border-radius:var(--ds-radius-control)}
li.focus{background:var(--ds-color-active-wash);box-shadow:inset 2px 0 0 var(--ds-tone-attention-fg)}
.lt{flex:1;min-width:0;display:flex;flex-direction:column;gap:var(--ds-space-2);overflow-wrap:anywhere}
.warnLine{display:inline-flex;align-items:center;gap:var(--ds-space-6);margin:0;color:var(--ds-tone-attention-fg);font-weight:var(--ds-weight-medium)}
.hl{position:fixed;pointer-events:none;border:2px solid var(--ds-color-accent);border-radius:var(--ds-radius-mark);background:var(--ds-color-selection);box-shadow:0 0 0 1px var(--ds-color-on-accent)}
.hl.picked{border-width:3px;box-shadow:0 0 0 1px var(--ds-color-on-accent),0 0 0 4000px var(--ds-color-scrim)}
.hl.warn{border-style:dashed;border-color:var(--ds-tone-attention-solid);background:repeating-linear-gradient(135deg,color-mix(in srgb,var(--ds-tone-attention-solid) 14%,transparent) 0 6px,transparent 6px 12px)}
.hl.show{border-width:3px;animation:ov 1.6s var(--ds-ease-standard) 2}
@keyframes ov{0%{box-shadow:0 0 0 1px var(--ds-color-on-accent),0 0 0 0 color-mix(in srgb,var(--ds-color-accent) 55%,transparent)}100%{box-shadow:0 0 0 1px var(--ds-color-on-accent),0 0 0 16px transparent}}
@media (prefers-reduced-motion:reduce){.hl.show{animation:none}}
.label{position:fixed;pointer-events:none;display:inline-flex;align-items:center;gap:var(--ds-space-8);max-width:520px;min-height:var(--ds-size-chip);padding:var(--ds-space-2) var(--ds-space-8);border-radius:var(--ds-radius-control);background:var(--ds-color-accent);color:var(--ds-color-on-accent);box-shadow:0 0 0 1px var(--ds-color-on-accent),var(--ds-shadow-2);font-size:var(--ds-text-xs);white-space:nowrap;overflow:hidden}
.label .mono{color:inherit}
.label .dim{color:color-mix(in srgb,var(--ds-color-on-accent) 80%,transparent)}
.label.warn{background:var(--ds-color-overlay);color:var(--ds-color-text-primary);box-shadow:inset 0 0 0 1px var(--ds-tone-attention-solid),var(--ds-shadow-2);white-space:normal;width:max-content;max-width:380px}
.label.warn svg{color:var(--ds-tone-attention-fg)}
.shield{position:fixed;inset:0;cursor:crosshair;background:transparent}
.box,.pop{position:fixed;display:flex;flex-direction:column;gap:var(--ds-space-12);width:380px;max-width:calc(100vw - 16px);max-height:calc(100vh - 16px);overflow:auto;padding:var(--ds-space-16);border-radius:var(--ds-radius-float);background:var(--ds-color-overlay);box-shadow:var(--ds-shadow-3)}
.pop{width:340px}
.sheet{left:8px!important;right:8px;top:auto!important;bottom:76px;width:auto;max-height:calc(100vh - 160px)}
.bh{display:flex;flex-direction:column;gap:var(--ds-space-2)}
.bh .dim{display:inline-flex;align-items:center;gap:var(--ds-space-4);font-size:var(--ds-text-xs)}
.bh b{font-weight:var(--ds-weight-medium)}
textarea{display:block;width:100%;resize:vertical;min-height:72px;padding:6px var(--ds-space-8);border:1px solid var(--ds-color-border);border-radius:var(--ds-radius-control);background:var(--ds-color-field-bg);color:var(--ds-color-text-primary);font-family:var(--ds-font-sans);font-size:var(--ds-text-md);line-height:20px;transition:border-color var(--ds-duration-fast) var(--ds-ease-standard)}
textarea:hover{border-color:var(--ds-color-border-strong)}
textarea:focus-visible{outline:none;border-color:var(--ds-color-accent);box-shadow:0 0 0 var(--ds-focus-ring-width) var(--ds-color-selection)}
textarea::placeholder{color:var(--ds-color-text-muted)}
label{display:inline-flex;align-items:center;gap:var(--ds-space-8);font-size:var(--ds-text-sm);cursor:pointer}
input[type=checkbox]{appearance:none;-webkit-appearance:none;display:inline-grid;place-items:center;flex:none;width:14px;height:14px;margin:0;border:1px solid var(--ds-color-border-strong);border-radius:var(--ds-radius-mark);background:var(--ds-color-field-bg);cursor:pointer}
input[type=checkbox]:hover:not(:disabled){border-color:var(--ds-color-text-muted)}
input[type=checkbox]:checked{background:var(--ds-color-accent);border-color:var(--ds-color-accent)}
input[type=checkbox]:checked:after{content:"";width:7px;height:4px;margin-top:-2px;border-left:2px solid var(--ds-color-on-accent);border-bottom:2px solid var(--ds-color-on-accent);transform:rotate(-45deg)}
input[type=checkbox]:disabled{opacity:.5;cursor:not-allowed}
details{border-radius:var(--ds-radius-control);background:var(--ds-color-chrome)}
summary{display:flex;align-items:center;gap:var(--ds-space-6);min-height:var(--ds-size-control-sm);padding:0 var(--ds-space-12);font-weight:var(--ds-weight-medium);font-size:var(--ds-text-sm);color:var(--ds-color-text-primary);cursor:pointer;border-radius:var(--ds-radius-control)}
summary:hover{background:var(--ds-color-hover-wash)}
summary:before{content:"▸";color:var(--ds-color-text-muted)}
details[open]>summary:before{content:"▾"}
details>p{margin:0;padding:0 var(--ds-space-12) var(--ds-space-12);font-size:var(--ds-text-xs)}
dl{display:grid;grid-template-columns:max-content minmax(0,1fr);column-gap:var(--ds-space-16);row-gap:var(--ds-space-6);margin:0;padding:var(--ds-space-4) var(--ds-space-12) var(--ds-space-12);font-size:var(--ds-text-sm)}
dt{color:var(--ds-color-text-muted)}
dd{margin:0;color:var(--ds-color-text-primary);overflow-wrap:anywhere}
.thumb{display:block;max-width:160px;max-height:120px;border-radius:var(--ds-radius-mark);background:var(--ds-color-sunken);box-shadow:inset 0 0 0 1px var(--ds-color-border-subtle)}
.foot{display:flex;align-items:center;gap:var(--ds-space-8);font-size:var(--ds-text-xs)}
.note{display:flex;align-items:flex-start;gap:var(--ds-space-6);margin:0;font-size:var(--ds-text-xs);color:var(--ds-color-text-secondary)}
.note.warn{color:var(--ds-tone-attention-fg)}
[data-testid=ov-problem]{color:var(--ds-tone-danger-fg)}
.note svg{margin-top:1px}
.from{margin:0;color:var(--ds-color-accent-text);font-size:var(--ds-text-xs);font-weight:var(--ds-weight-medium)}
.head{display:flex;align-items:center;gap:var(--ds-space-6);flex-wrap:wrap;font-size:var(--ds-text-sm)}
.head b{font-weight:var(--ds-weight-medium)}
.body{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;font-size:var(--ds-text-md)}
.entry{padding-left:var(--ds-space-12);box-shadow:inset 2px 0 0 var(--ds-color-border);font-size:var(--ds-text-sm)}
.entry b{font-weight:var(--ds-weight-medium)}
.entry p{margin:var(--ds-space-2) 0 0;white-space:pre-wrap}
.st{display:inline-flex;align-items:center;gap:var(--ds-space-4);flex:none;height:var(--ds-size-badge-md);padding:0 var(--ds-space-6);border-radius:var(--ds-radius-full);background:var(--ds-tone-neutral-bg);color:var(--ds-tone-neutral-fg);font-size:var(--ds-text-xs);font-weight:var(--ds-weight-medium);white-space:nowrap}
.pin{position:fixed;gap:var(--ds-space-2);height:24px;min-width:24px;padding:0 var(--ds-space-6);border:2px solid var(--ds-color-surface);border-radius:var(--ds-radius-full) var(--ds-radius-full) var(--ds-radius-full) var(--ds-radius-mark);background:var(--ds-tone-neutral-solid);color:var(--ds-tone-neutral-on-solid);font-size:var(--ds-text-2xs);font-weight:var(--ds-weight-semibold);box-shadow:var(--ds-shadow-2)}
.pin>svg,.pin:hover>svg{color:currentColor}
.pin:hover:not(:disabled){background:var(--ds-tone-neutral-solid);filter:brightness(1.1)}
.pin.inline{position:static;height:20px;min-width:20px;padding:0 var(--ds-space-4);border-width:1.5px;box-shadow:none;cursor:default}
.pin.draft,.pin.draft:hover:not(:disabled){background:var(--ds-color-surface);color:var(--ds-color-accent-text);border:2px dashed var(--ds-color-accent)}
.pin.delivered,.pin.delivered:hover:not(:disabled){background:var(--ds-tone-info-solid);color:var(--ds-tone-info-on-solid)}
.pin.resolved,.pin.resolved:hover:not(:disabled){background:var(--ds-tone-success-solid);color:var(--ds-tone-success-on-solid)}
.pin.gone{background:transparent;border:1.5px dashed var(--ds-tone-attention-solid);color:var(--ds-tone-attention-fg)}
.pin.active{outline:var(--ds-focus-ring-width) solid var(--ds-color-focus-ring);outline-offset:var(--ds-focus-ring-offset)}
.toast{display:flex;align-items:center;gap:var(--ds-space-8);padding:var(--ds-space-12);background:var(--ds-color-overlay);box-shadow:inset 3px 0 0 var(--ds-tone-success-fg);min-width:440px;max-width:100%}
.tt{flex:1;display:flex;flex-direction:column;gap:var(--ds-space-2);min-width:0;font-size:var(--ds-text-sm);color:var(--ds-color-text-secondary)}
.tt b{font-size:var(--ds-text-md);font-weight:var(--ds-weight-medium);color:var(--ds-color-text-primary)}
.tt .dim{color:var(--ds-color-text-secondary)}
.okc{display:inline-grid;place-items:center;flex:none;width:24px;height:24px;border-radius:var(--ds-radius-full);background:var(--ds-tone-success-solid);color:var(--ds-tone-success-on-solid)}
.narrow .bar{left:8px;right:8px!important;bottom:8px!important;max-width:none}
.narrow .panel,.narrow .toast{width:auto;min-width:0}
`;

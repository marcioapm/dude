// The overlay's own look, inside its closed shadow root: no page CSS reaches
// it and none of this leaks. One dark look on every app (the page's theme is
// not knowable); a violet few apps use for the pick.
export const CSS = `
:host{all:initial}
*{box-sizing:border-box}
.root{position:fixed;inset:0;pointer-events:none;z-index:2147483647;font:13px/1.4 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:#f3f4f8;
--ink:#15171f;--ink2:#1f2230;--ink3:#2b2f40;--line:#3a3f55;--dim:#a3a8bb;--pick:#7c5cff;--warn:#f2b13b;--ok:#33b87a;--info:#4c9cff}
.root>*{pointer-events:auto}
.mono{font:12px ui-monospace,"SF Mono",Menlo,Consolas,monospace}
.dim{color:var(--dim)}
.sp{flex:1}
kbd{display:inline-block;min-width:16px;padding:0 4px;margin:0 1px;border:1px solid var(--line);border-bottom-width:2px;border-radius:4px;font:11px/15px ui-monospace,Menlo,monospace;text-align:center;color:#f3f4f8}
button,.btn{display:inline-flex;align-items:center;gap:6px;height:28px;padding:0 10px;border:1px solid var(--line);border-radius:7px;background:var(--ink2);color:#f3f4f8;font:500 13px/1 system-ui,-apple-system,sans-serif;cursor:pointer;white-space:nowrap;text-decoration:none}
button:hover,.btn:hover{background:var(--ink3)}
button:focus-visible,.btn:focus-visible,textarea:focus-visible,input:focus-visible,summary:focus-visible{outline:2px solid var(--pick);outline-offset:2px}
button:disabled{opacity:.55;cursor:default}
.primary{background:var(--pick);border-color:var(--pick);color:#fff}
.primary:hover{background:#6a48f5}
.on{background:#fff;border-color:#fff;color:var(--ink)}
.on:hover{background:#e9e9f2}
.icon{width:28px;padding:0;justify-content:center;border:0;background:transparent;color:var(--dim)}
svg{flex:none}
.bar{position:fixed;right:20px;bottom:20px;display:flex;flex-direction:column;max-width:calc(100vw - 24px);border:1px solid var(--line);border-radius:12px;background:var(--ink);box-shadow:0 10px 30px rgba(0,0,0,.35);overflow:hidden}
.row{display:flex;align-items:center;gap:8px;padding:6px 6px 6px 4px}
.grip{display:inline-grid;place-items:center;width:16px;height:28px;color:var(--dim);cursor:grab;touch-action:none}
.who{display:inline-flex;align-items:center;gap:8px;margin-right:4px;min-width:0}
.mark{display:inline-grid;place-items:center;flex:none;width:22px;height:22px;border-radius:6px;background:var(--pick);color:#fff;font-weight:800;font-size:13px}
.whoText{display:flex;flex-direction:column;line-height:1.15;min-width:0}
.whoText b{font-size:12px}
.conn{font-size:11px;color:var(--ok);white-space:nowrap}
.conn:before{content:"";display:inline-block;width:6px;height:6px;margin-right:4px;border-radius:50%;background:currentColor;vertical-align:1px}
.off{color:var(--warn)}
.drafts{display:inline-flex;align-items:center;gap:4px;padding:0 4px;font-size:12px;color:var(--dim);white-space:nowrap}
.hint{display:flex;align-items:center;gap:12px;padding:6px 10px;border-top:1px solid var(--line);background:var(--ink2);color:var(--dim);font-size:12px}
.hint b{color:#f3f4f8}
.pill{position:fixed;right:20px;bottom:20px;padding:4px;height:auto;border-radius:999px;background:var(--ink);box-shadow:0 6px 18px rgba(0,0,0,.3)}
.pill .mark{border-radius:50%}
.panel{display:flex;flex-direction:column;min-width:420px;max-width:100%;max-height:60vh;overflow:auto;border-bottom:1px solid var(--line)}
.ph{display:flex;align-items:center;justify-content:space-between;gap:8px;padding:10px 12px 6px}
.pp{margin:0;padding:0 12px 10px;color:var(--dim)}
.pp b{color:#f3f4f8}
.pf{display:flex;gap:8px;padding:0 12px 12px}
ul{list-style:none;margin:0;padding:0 6px 6px;display:flex;flex-direction:column}
li{display:flex;align-items:flex-start;gap:8px;padding:6px;border-radius:8px}
li.focus{background:var(--ink2);box-shadow:inset 2px 0 0 var(--warn)}
.lt{flex:1;min-width:0;display:flex;flex-direction:column;gap:2px;overflow-wrap:anywhere}
.warnLine{display:inline-flex;align-items:center;gap:6px;margin:0;color:var(--warn);font-weight:500}
.hl{position:fixed;pointer-events:none;border:2px solid var(--pick);border-radius:4px;background:rgba(124,92,255,.12)}
.hl.picked{border-width:3px;box-shadow:0 0 0 4000px rgba(10,12,20,.28)}
.hl.warn{border-style:dashed;border-color:var(--warn);background:repeating-linear-gradient(135deg,rgba(242,177,59,.14) 0 6px,transparent 6px 12px)}
.hl.show{border-width:3px;animation:ov 1.6s ease-out 2}
@keyframes ov{0%{box-shadow:0 0 0 0 rgba(124,92,255,.55)}100%{box-shadow:0 0 0 16px rgba(124,92,255,0)}}
@media (prefers-reduced-motion:reduce){.hl.show{animation:none}}
.label{position:fixed;pointer-events:none;display:inline-flex;align-items:center;gap:8px;max-width:520px;min-height:22px;padding:2px 8px;border-radius:5px;background:var(--pick);color:#fff;font-size:12px;white-space:nowrap;overflow:hidden}
.label .dim{color:rgba(255,255,255,.75)}
.label.warn{background:var(--ink);border:1px solid var(--warn);white-space:normal;width:max-content;max-width:380px}
.shield{position:fixed;inset:0;cursor:crosshair;background:transparent}
.box,.pop{position:fixed;display:flex;flex-direction:column;gap:10px;width:380px;max-width:calc(100vw - 16px);max-height:calc(100vh - 16px);overflow:auto;padding:12px;border:1px solid var(--line);border-radius:12px;background:var(--ink);box-shadow:0 16px 40px rgba(0,0,0,.4)}
.pop{width:340px}
.sheet{left:8px!important;right:8px;top:auto!important;bottom:76px;width:auto;max-height:calc(100vh - 160px)}
.bh{display:flex;flex-direction:column;gap:2px}
.bh .dim{font-size:12px}
textarea{resize:vertical;min-height:72px;padding:8px 10px;border:1px solid var(--line);border-radius:8px;background:var(--ink2);color:#f3f4f8;font:14px/1.45 system-ui,-apple-system,sans-serif}
textarea::placeholder{color:var(--dim)}
label{display:inline-flex;align-items:center;gap:6px;font-size:12px;cursor:pointer}
input[type=checkbox]{accent-color:var(--pick);margin:0}
details{border:1px solid var(--line);border-radius:8px;background:var(--ink2)}
summary{padding:6px 10px;font-weight:600;font-size:12px;cursor:pointer}
details>p{margin:0;padding:0 10px 10px;font-size:12px}
dl{display:grid;grid-template-columns:76px minmax(0,1fr);gap:4px 8px;margin:0;padding:2px 10px 10px;font-size:12px}
dt{color:var(--dim)}
dd{margin:0;overflow-wrap:anywhere}
.thumb{display:block;max-width:160px;max-height:120px;border:1px solid var(--line);border-radius:6px}
.foot{display:flex;align-items:center;gap:8px;font-size:12px}
.note{margin:0;font-size:12px;color:var(--dim)}
.note.warn{color:var(--warn)}
.from{margin:0;color:#b9a8ff;font-size:12px;font-weight:600}
.head{display:flex;align-items:center;gap:6px;flex-wrap:wrap;font-size:12px}
.body{margin:0;white-space:pre-wrap;overflow-wrap:anywhere}
.entry{padding-left:10px;border-left:2px solid var(--line);font-size:12px}
.entry p{margin:2px 0 0;white-space:pre-wrap}
.st{display:inline-flex;align-items:center;gap:4px;flex:none;padding:1px 6px;border-radius:999px;background:var(--ink3);font-size:11px;white-space:nowrap}
.pin{position:fixed;gap:2px;height:24px;min-width:24px;padding:0 6px;border:2px solid #fff;border-radius:12px 12px 12px 3px;background:#5b6075;color:#fff;font:700 11px/1 system-ui,sans-serif;box-shadow:0 2px 6px rgba(0,0,0,.35)}
.pin.inline{position:static;height:20px;min-width:20px;padding:0 4px;border-width:1.5px;font-size:10px;box-shadow:none;cursor:default}
.pin.draft{background:var(--ink);border:2px dashed var(--pick)}
.pin.delivered{background:var(--info)}
.pin.resolved{background:var(--ok)}
.pin.gone{background:transparent;border:1.5px dashed var(--warn);color:var(--warn)}
.pin.active{outline:2px solid var(--pick);outline-offset:2px}
.toast{display:flex;align-items:center;gap:10px;padding:10px 12px;border-bottom:1px solid var(--line);background:var(--ink2);min-width:440px;max-width:100%}
.tt{flex:1;display:flex;flex-direction:column;min-width:0;font-size:12px}
.tt b{font-size:13px}
.okc{display:inline-grid;place-items:center;flex:none;width:24px;height:24px;border-radius:50%;background:var(--ok);color:#fff}
.narrow .bar{left:8px;right:8px!important;bottom:8px!important;max-width:none}
.narrow .panel,.narrow .toast{width:auto;min-width:0}
`;

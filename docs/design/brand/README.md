# Brand

## The Dude

dude's face is The Dude: long hair with an orange streak, orange shades, a
beard, a smug half-smile. `dude.png` is the source (1254×1254, transparent,
generated with ChatGPT, 26 Sep 2026); `dude.svg` is it traced, and what every
use is made from.

## Names

| Where | Name |
|---|---|
| the product, the repository, the CLI | dude |
| the sidebar's title | El Duderino |
| the installed app (manifest) — the name the OS shows on notifications | The Dude |
| every notification's title starts | His Dudeness · |

## Colours

| | Hex | Use |
|---|---|---|
| Ink | `#010d22` | outlines, the silhouette; theme colour |
| Navy | `#1e3a66` | hair highlights |
| Cream | `#fbf5e6` | skin; the outline on dark; app-icon plates |
| Orange | `#fb5f02` | the streak, the lenses |
| Glint | `#ebb68e` | the lenses' shine |

On a dark background his ink hair vanishes, so there he wears a cream
outline (`dude-outlined.svg`): the app switches by theme (`DudeMark`), the
tab icon by the browser's (`favicon.svg`). App icons sit on a cream plate.

## Files

`dude.svg` is traced with `python trace.py dude.png out.svg` (potrace, one
layer per colour, bottom up). In the app (`apps/web/public`):

| File | Where |
|---|---|
| `favicon.svg` | the browser tab; outlined when the browser is dark |
| `favicon-32.png` | the tab, for browsers without SVG favicons |
| `dude.svg`, `dude-outlined.svg` | the sidebar and the key prompt (`DudeMark`), by the app's theme |
| `apple-touch-180.png` | iOS home screen (iOS rounds it) |
| `app-192.png`, `app-512.png`, `maskable-512.png` | the web app manifest; maskable has margin for Android's circle |
| `icon-192.png` | notifications (outlined: their background is unknown) |

The PNGs are rendered from the SVGs. Nothing is drawn for 16px: a tab at 16
scales the SVG, or the 32. A raster source: for print or large display,
work from `dude.svg`.

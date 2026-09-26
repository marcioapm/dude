# dude's mascot

`dude.png` is the source: 1254×1254, transparent, five flat colours — ink
`#010d22`, navy `#1e3a66`, cream `#fbf5e6`, orange `#fb5f02` and a lens
glint `#ebb68e` — generated with ChatGPT (26 Sep 2026).

`dude.svg` is it traced (`python trace.py dude.png out.svg`: potrace, one layer per colour), and the
mark everything is made from. `dude-outlined.svg` adds a cream outline, for
dark backgrounds, where his navy hair would vanish.

In the app (`apps/web/public`):

| File | Where |
|---|---|
| `favicon.svg` | the browser tab; outlined when the browser is dark (a `prefers-color-scheme` rule inside the SVG) |
| `favicon-32.png` | the tab, for browsers without SVG favicons |
| `dude.svg`, `dude-outlined.svg` | the sidebar and the key prompt (`DudeMark`), by the app's theme |
| `apple-touch-180.png` | iOS home screen, on a cream plate (iOS rounds it) |
| `app-192.png`, `app-512.png`, `maskable-512.png` | the web app manifest (Android, installs); maskable has margin for Android's circle |
| `icon-192.png` | browser notifications (outlined: the notification's background is unknown) |

The PNGs are rendered from the SVGs. Nothing is drawn for 16px: a tab at 16
scales the SVG, or the 32.

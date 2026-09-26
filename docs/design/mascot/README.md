# dude's mascot

`dude.png` is the source: 1254×1254, transparent, generated with ChatGPT
(26 Sep 2026). The app's icons are cut from it (`apps/web/public`):

| File | Size | Where |
|---|---|---|
| `favicon.ico`, `favicon-32.png` | 32, 48 | browser tab — scaled down by the browser; nothing is drawn for 16 |
| `favicon-64.png` | 64 | high-DPI tabs; the sidebar's logo |
| `apple-touch-180.png` | 180 | iOS home screen (on a navy plate; iOS rounds it) |
| `app-192.png`, `app-512.png` | 192, 512 | web app manifest (Android, installs) |
| `maskable-512.png` | 512 | manifest, maskable: extra margin for Android's circle |
| `icon-192.png` | 192 | browser notifications |
| `dude.png` | 512 | the key prompt |

The navy plate is `#16203c`, the manifest's theme colour. A raster: for
print or anything larger than about 1000px it needs vectorising first.

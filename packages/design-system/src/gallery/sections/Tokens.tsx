import type { CSSProperties } from "react";
import { Block, Caption, Col, Label, Panes, Row, Section, type PaneMode } from "../Frame.tsx";
import styles from "../gallery.module.css";
import { AGENT_ROLE_NAMES, TONE_NAMES, accent, diff, neutral, roleColors, themeColors, tones } from "../../tokens/palette-and-themes.ts";
import { duration, easing, fontSize, radius, space, zIndex } from "../../tokens/scale.ts";
import { densityTokens, type Density, type DensityToken } from "../../tokens/density.ts";
import { ALL_STATUSES, STATUS_SPECS } from "../../tokens/status.ts";
import { Icon, ICON_NAMES } from "../../icons/index.tsx";
import { StatusBadge } from "../../components/StatusBadge.tsx";
import { cx } from "../../util/cx.ts";

function Swatch({ name, hex }: { readonly name: string; readonly hex: string }) {
  return (
    <div className={styles["swatch"]} title={`${name}: ${hex}`}>
      <div className={styles["swatchChip"]} style={{ background: hex }} />
      <span className={styles["swatchName"]}>{name}</span>
      <span className={styles["swatchHex"]}>{hex}</span>
    </div>
  );
}

const SEMANTIC_KEYS = [
  "canvas",
  "surface",
  "raised",
  "overlay",
  "sunken",
  "fieldBg",
  "borderSubtle",
  "border",
  "borderStrong",
  "textPrimary",
  "textSecondary",
  "textMuted",
  "textDisabled",
  "accent",
  "accentSubtle",
  "accentText",
  "focusRing",
  "live",
] as const;

export function TokensSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section
      id="tokens"
      title="Tokens"
      intro="Everything is authored in OKLCH and resolved to hex at build time, so lightness is perceptual and the shipped CSS needs no runtime support. Components reference only semantic roles; the raw ramp is never used directly."
    >
      <Block id="tokens-neutrals" title="Neutral ramp" note="13 steps, hue 250, chroma 0.0035 — a grey with the faintest cool cast. Dark surfaces are charcoal, steps 1–4; light surfaces are 12 + white. The text ladders are set in themes.ts against the surfaces, not taken from this ramp.">
        <div className={styles["ramp"]}>
          {neutral.map((hex, i) => (
            <div key={i} className={styles["rampStep"]} style={{ background: hex, color: i < 7 ? "#fff" : "#000" }}>
              {i}
            </div>
          ))}
        </div>
      </Block>

      <Block id="tokens-semantic" title="Semantic colors" note="The roles components actually use. Dark separates regions by small, even surface steps; light by canvas vs white and shadow. Hairlines only where they carry meaning.">
        <Panes mode={mode}>
          {(theme) => (
            <div className={styles["swatches"]}>
              {SEMANTIC_KEYS.map((k) => (
                <Swatch key={k} name={k} hex={themeColors[theme][k]} />
              ))}
            </div>
          )}
        </Panes>
      </Block>

      <Block id="tokens-tones" title="Status tones" note="Five tones only: neutral, info, attention, success, danger. Each has fg (≥4.5:1 on surface), bg, border and solid. The four chromatic fg values were chosen by search so every pair clears ΔE≥8 under protan/deutan simulation and ΔE≥15 in normal vision.">
        <Panes mode={mode}>
          {(theme) => (
            <div>
              <div className={styles["toneRow"]}>
                <Caption>tone</Caption>
                <Caption>fg on surface</Caption>
                <Caption>fg on bg</Caption>
                <Caption>border</Caption>
                <Caption>on solid</Caption>
              </div>
              {TONE_NAMES.map((t) => {
                const tone = tones[theme][t];
                return (
                  <div key={t} className={styles["toneRow"]}>
                    <span style={{ fontSize: 12 }}>{t}</span>
                    <div className={styles["toneCell"]} style={{ background: themeColors[theme].surface, color: tone.fg }}>
                      {tone.fg}
                    </div>
                    <div className={styles["toneCell"]} style={{ background: tone.bg, color: tone.fg }}>
                      {tone.bg}
                    </div>
                    <div className={styles["toneCell"]} style={{ border: `1px solid ${tone.border}`, color: tone.fg }}>
                      {tone.border}
                    </div>
                    <div className={styles["toneCell"]} style={{ background: tone.solid, color: tone.onSolid }}>
                      {tone.solid}
                    </div>
                  </div>
                );
              })}
              <Label>Colorblind check — the same four tones in grayscale still separate by lightness</Label>
              <div className={styles["cvdBar"]}>
                {(["info", "attention", "success", "danger"] as const).map((t) => (
                  <span key={t} style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
                    <span style={{ width: 28, height: 16, background: tones[theme][t].fg, borderRadius: 3 }} />
                    <span className={styles["cvdSim"]} style={{ width: 28, height: 16, background: tones[theme][t].fg, borderRadius: 3 }} />
                    <Caption>{t}</Caption>
                  </span>
                ))}
              </div>
            </div>
          )}
        </Panes>
      </Block>

      <Block id="tokens-roles" title="Agent role colors" note="Categorical identity for the six roles, fixed order, never reused for status. Lightness varies per role on purpose: it is what keeps violet/blue and teal/green apart for deutan viewers. All 15 pairs validated in both modes.">
        <Panes mode={mode}>
          {(theme) => (
            <div className={styles["swatches"]}>
              {AGENT_ROLE_NAMES.map((r) => (
                <div key={r} className={styles["swatch"]}>
                  <div className={styles["swatchChip"]} style={{ background: roleColors[theme][r].bg, color: roleColors[theme][r].fg, display: "grid", placeItems: "center" }}>
                    <Icon name={r} size={16} />
                  </div>
                  <span className={styles["swatchName"]}>{r}</span>
                  <span className={styles["swatchHex"]}>{roleColors[theme][r].fg}</span>
                </div>
              ))}
            </div>
          )}
        </Panes>
      </Block>

      <Block id="tokens-accent" title="Accent and diff" note="One blue for interaction (same hue as info). Diff colors are their own tokens — a diff is read for minutes, so these are softer than the status tones.">
        <Panes mode={mode}>
          {(theme) => (
            <Col>
              <div className={styles["swatches"]}>
                {Object.entries(accent[theme]).map(([k, v]) => (
                  <Swatch key={k} name={`accent.${k}`} hex={v} />
                ))}
              </div>
              <div className={styles["swatches"]}>
                {Object.entries(diff[theme]).map(([k, v]) => (
                  <Swatch key={k} name={`diff.${k}`} hex={v} />
                ))}
              </div>
            </Col>
          )}
        </Panes>
      </Block>

      <Block id="tokens-type" title="Type scale" note="The system UI face (SF Pro, Noto Sans, Segoe UI; Inter as a fallback). UI body 15px (14 compact); transcript text 16/22px; long-form Markdown 16px at 1.5. Mono tier is JetBrains Mono with tnum + slashed zero, used for anything an operator might copy or compare: IDs, SHAs, paths, timestamps, costs.">
        <Panes mode={mode}>
          {(_theme, density) => (
          <div className={styles["typeSample"]}>
            {(Object.keys(fontSize) as Array<keyof typeof fontSize>).map((k) => (
              <TypeRow key={k} k={k} density={density} />
            ))}
            <Caption>mono md</Caption>
            <span className="ds-mono" style={{ fontSize: 13 }}>
              ses_01J9K2 · a3f9c1e · 00:14:03.117 · $0.0842 · 1,024 tok
            </span>
            <Caption>tnum</Caption>
            <span className="ds-tnum" style={{ fontSize: 13, display: "inline-grid", gridTemplateColumns: "auto", lineHeight: 1.3 }}>
              <span>$1,284.10</span>
              <span>$0,011.00</span>
              <span>$9,999.99</span>
            </span>
            <Caption>proportional</Caption>
            <span style={{ fontSize: 13, display: "inline-grid", gridTemplateColumns: "auto", lineHeight: 1.3, fontVariantNumeric: "proportional-nums" }}>
              <span>$1,284.10</span>
              <span>$0,011.00</span>
              <span>$9,999.99</span>
            </span>
            <Caption>label caps</Caption>
            <span className="ds-label">Section label</span>
          </div>
          )}
        </Panes>
      </Block>

      <Block id="tokens-space" title="Spacing, radii, elevation" note="Spacing on a 4px grid with 2 and 6 for hairline gaps. Radii are soft but small — 6px is the default control radius, 8px cards, 12px dialogs; nothing should look like a pill except a status dot.">
        <Panes mode={mode}>
          {(theme) => (
            <Col>
              <div className={styles["typeSample"]}>
                {(Object.keys(space) as unknown as Array<keyof typeof space>).map((k) => (
                  <SpaceRow key={String(k)} k={String(k)} px={space[k]} />
                ))}
              </div>
              <Row top>
                {(Object.keys(radius) as Array<keyof typeof radius>).map((k) => (
                  <Col key={k}>
                    <div className={styles["radiusBox"]} style={{ borderRadius: radius[k] }} />
                    <Caption>
                      {k} {radius[k]}
                    </Caption>
                  </Col>
                ))}
              </Row>
              <div className={styles["grid3"]}>
                {([1, 2, 3] as const).map((n) => (
                  <div key={n} className={styles["shadowBox"]} style={{ boxShadow: `var(--ds-shadow-${n})` }}>
                    <Caption>
                      shadow-{n} ({theme})
                    </Caption>
                  </div>
                ))}
              </div>
            </Col>
          )}
        </Panes>
      </Block>

      <Block
        id="tokens-density"
        title="Density"
        note="Comfortable is the default; compact is data-density=&quot;compact&quot;. Only these tokens differ. The large layout spacing, row heights and the chat avatar give up real space; text, the default radius and medium controls lose 1–2px; everything smaller holds."
      >
        <DensityTable />
      </Block>

      <Block id="tokens-motion" title="Motion and layers" note="Four durations. Hover a row to see the easing. Everything 'live' pulses at 2.4s and is multiplied by --ds-motion-live, which reduced-motion sets to 0 — so live states become still instead of vanishing.">
        <Panes mode={mode}>
          <Col>
            {(Object.keys(duration) as Array<keyof typeof duration>).map((d) =>
              d === "instant" ? null : (
                <div key={d} className={styles["motionTrack"]} style={{ "--dur": `${duration[d]}ms`, "--ease": easing.standard } as CSSProperties}>
                  <Caption>
                    {d} {duration[d]}ms
                  </Caption>
                  <span className={styles["motionDot"]} />
                </div>
              ),
            )}
            <Row>
              {(Object.keys(easing) as Array<keyof typeof easing>).map((e) => (
                <Caption key={e}>
                  {e}: {easing[e]}
                </Caption>
              ))}
            </Row>
            <Row>
              {(Object.keys(zIndex) as Array<keyof typeof zIndex>).map((z) => (
                <Caption key={z}>
                  z-{z}={zIndex[z]}
                </Caption>
              ))}
            </Row>
          </Col>
        </Panes>
      </Block>

      <Block id="tokens-status" title="Status vocabulary" note="Every domain status, its tone, glyph, and default emphasis. This table is the contract: if a state is not here, it cannot be rendered.">
        <Panes mode={mode}>
          <div className={styles["statusMatrix"]}>
            <Caption>status</Caption>
            <Caption>default</Caption>
            <Caption>tone</Caption>
            <Caption>flags</Caption>
            <Caption>meaning</Caption>
            {ALL_STATUSES.map((s) => {
              const spec = STATUS_SPECS[s];
              return (
                <StatusRow key={s} s={s} spec={spec} />
              );
            })}
          </div>
        </Panes>
      </Block>

      <Block id="tokens-icons" title="Icons" note="16px grid, 1.5px stroke. Status glyphs are the shape channel and must be recognisable at 10px.">
        <Panes mode={mode}>
          <div className={styles["iconGrid"]}>
            {ICON_NAMES.map((n) => (
              <div key={n} className={styles["iconCell"]}>
                <Icon name={n} size={16} />
                <Caption>{n}</Caption>
              </div>
            ))}
          </div>
        </Panes>
      </Block>
    </Section>
  );
}

function TypeRow({ k, density }: { readonly k: keyof typeof fontSize; readonly density: Density }) {
  const token = `text-${k}`;
  const value = densityTokens[density][token as DensityToken] ?? `${fontSize[k]}px`;
  return (
    <>
      <Caption>
        {k} {value}
      </Caption>
      <span style={{ fontSize: `var(--ds-${token})`, lineHeight: 1.3 }}>Run 14 failed: 3 checks, 1 blocked on you</span>
    </>
  );
}

function DensityTable() {
  const keys = Object.keys(densityTokens.comfortable) as DensityToken[];
  return (
    <div className={styles["stateGrid"]} style={{ gridTemplateColumns: "max-content max-content max-content" }}>
      <Caption>token</Caption>
      <Caption>comfortable</Caption>
      <Caption>compact</Caption>
      {keys.map((k) => (
        <DensityRow key={k} k={k} />
      ))}
    </div>
  );
}
function DensityRow({ k }: { readonly k: DensityToken }) {
  const same = densityTokens.comfortable[k] === densityTokens.compact[k];
  return (
    <>
      <span className="ds-mono" style={{ fontSize: "var(--ds-text-xs)" }}>--ds-{k}</span>
      <Caption>{densityTokens.comfortable[k]}</Caption>
      <Caption>{same ? "—" : densityTokens.compact[k]}</Caption>
    </>
  );
}

function SpaceRow({ k, px }: { readonly k: string; readonly px: number }) {
  return (
    <>
      <Caption>
        space-{k} {px}px
      </Caption>
      <div className={styles["spaceBar"]} style={{ width: Math.max(px, 1) }} />
    </>
  );
}

function StatusRow({ s, spec }: { readonly s: (typeof ALL_STATUSES)[number]; readonly spec: (typeof STATUS_SPECS)[keyof typeof STATUS_SPECS] }) {
  return (
    <>
      <span className={cx("ds-mono")} style={{ fontSize: 12 }}>
        {s}
      </span>
      <StatusBadge status={s} />
      <Caption>{spec.tone}</Caption>
      <Caption>
        {[spec.live && "live", spec.needsHuman && "needsHuman", spec.terminal && "terminal"].filter(Boolean).join(" ") || "—"}
      </Caption>
      <span style={{ fontSize: 12, color: "var(--ds-color-text-secondary)" }}>{spec.description}</span>
    </>
  );
}

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { FeedbackSection } from "../src/gallery/sections/Feedback.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

test("the gallery's preview draft shows its picture and Show on preview, in each pane", async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider><PaneDensityContext.Provider value="both"><FeedbackSection mode="both" /></PaneDensityContext.Provider></TooltipProvider>));
  const cards = [...host.querySelectorAll("[data-testid=gallery-fb-preview-draft]")];
  expect(cards.length).toBeGreaterThan(0);
  for (const card of cards) {
    expect(card.querySelectorAll("[data-testid=fb-preview] img").length).toBe(1);
    expect(card.querySelectorAll("[data-testid=fb-show-on-preview]").length).toBe(1);
  }
});

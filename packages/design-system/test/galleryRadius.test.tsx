/**
 * The gallery is the living contract: a frame it draws around a control
 * takes the control radius role, so it follows density like the control
 * inside it. Stylesheets are covered by radius.test.ts; this covers the
 * inline styles of the steer-delivery block.
 */

import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ChatSection } from "../src/gallery/sections/Chat.tsx";

test("the steer-delivery composer frames carry no literal radius", () => {
  const html = renderToStaticMarkup(<ChatSection mode="light" />);
  const from = html.indexOf('id="ch-steer"');
  expect(from).toBeGreaterThan(-1);
  const block = html.slice(from, html.indexOf('id="ch-thread"', from));
  const literal = [...block.matchAll(/border-radius:\s*([^;"]+)/g)].map((m) => m[1]!.trim()).filter((v) => !v.startsWith("var(--ds-radius-"));
  expect(literal).toEqual([]);
});

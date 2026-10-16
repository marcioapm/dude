/**
 * The organisation's GitHub behaviour, as an admin reads it: what dude does
 * when a task's base moves ahead, in its exact words.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { GithubBehaviour } from "../src/screens/GithubSettings.tsx";
import { ToastProvider } from "@dude/design-system/primitives";

const mounted: (() => void)[] = [];
afterEach(() => { while (mounted.length) mounted.pop()!(); });

describe("when a task's base moves ahead", () => {
  test("offers keeping it up to date with an agent, or only telling the task's people", async () => {
    const { container, unmount } = await mount(<ToastProvider><GithubBehaviour client={new FixtureClient("a")} admin /></ToastProvider>);
    mounted.push(unmount);
    const select = await until(() => container.querySelector<HTMLElement>('[aria-label="When a task\'s base moves ahead"]'), "the setting");
    await act(async () => void select.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    await until(() => document.querySelector("[role=option]"), "its options");
    const options = [...document.querySelectorAll<HTMLElement>("[role=option]")].map((o) => [o.getAttribute("data-value"), o.textContent]);
    expect(options).toEqual([
      ["update", "Keep it up to date: merge the base in, and resolve conflicts with an agent"],
      ["tell", "Only tell the task's people"],
    ]);
  });
});

/**
 * A hidden tab polls nothing it doesn't need: the people are not read
 * while the page is hidden, and are read at once when it is shown; a Run
 * preparing its image is read again every few seconds only while shown.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { RUN_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { RunScreen } from "../src/screens/RunScreen.tsx";
import type { ApiClient, RunDetail } from "../src/api/client.ts";

let mounted: Array<() => Promise<void>> = [];
let hidden = false;
Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (hidden ? "hidden" : "visible") });
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  hidden = false;
});

async function setHidden(h: boolean) {
  hidden = h;
  await act(async () => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

class Counting extends FixtureClient {
  people = 0;
  runs = 0;
  override listPeople() {
    this.people++;
    return super.listPeople();
  }
  override async getRun(id: string): Promise<RunDetail> {
    this.runs++;
    return { ...(await super.getRun(id)), preparingImage: { kind: "build", buildId: "imb_1", state: "running", imageName: "acme-base", version: 1, builderOfflineSince: null } };
  }
}

describe("the people", () => {
  test("are not read while the page is hidden, and are read once it is shown", async () => {
    hidden = true;
    const client = new Counting("a");
    const { unmount } = await mount(<PeopleProvider client={client as unknown as ApiClient}><span /></PeopleProvider>);
    mounted.push(unmount);
    await settle(100);
    expect(client.people).toBe(0);
    await setHidden(false);
    await settle(50);
    expect(client.people).toBe(1);
  });
});

describe("a Run preparing its image", () => {
  test("is read again every few seconds while shown, and not while hidden", async () => {
    const client = new Counting("a");
    const { container, unmount } = await mount(<RunScreen client={client} runId={RUN_ID} onBack={() => {}} />);
    mounted.push(unmount);
    await until(() => container.querySelector("[data-testid=preparing-image]"), "the preparing callout");
    await settle(200);
    const shown = client.runs;
    await settle(4_300);
    expect(client.runs).toBeGreaterThan(shown);

    await setHidden(true);
    await settle(200);
    const atHide = client.runs;
    await settle(4_300);
    expect(client.runs).toBe(atHide);
  }, 15_000);
});

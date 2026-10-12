/**
 * A hidden tab polls nothing it doesn't need: the people are read each
 * minute while shown, not while hidden, and at once when shown again; a Run
 * preparing its image and the image library's live builds are read again
 * every few seconds only while shown.
 *
 * Interval time is virtual: setInterval/clearInterval are replaced for these
 * tests and `advance` runs the due callbacks in order.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { ImageBuild, ImageBuildWithLog, ImageDetail, ImagesResponse } from "@dude/domain";
import { act, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { RUN_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { RunScreen } from "../src/screens/RunScreen.tsx";
import { ImagesPage, useImages } from "../src/screens/ImagesSettings.tsx";
import type { ApiClient, RunDetail } from "../src/api/client.ts";

let mounted: Array<() => Promise<void>> = [];
let hidden = false;
Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
Object.defineProperty(document, "visibilityState", { configurable: true, get: () => (hidden ? "hidden" : "visible") });

const realSetInterval = globalThis.setInterval;
const realClearInterval = globalThis.clearInterval;
type Interval = { fn: () => void; ms: number; due: number };
let intervals = new Map<number, Interval>();
let clock = 0;
let nextId = 1;

beforeEach(() => {
  intervals = new Map();
  clock = 0;
  globalThis.setInterval = ((fn: () => void, ms = 0) => {
    const id = nextId++;
    intervals.set(id, { fn, ms: Math.max(1, ms), due: clock + Math.max(1, ms) });
    return id;
  }) as unknown as typeof setInterval;
  globalThis.clearInterval = ((id: number) => void intervals.delete(id)) as unknown as typeof clearInterval;
});

afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  hidden = false;
  globalThis.setInterval = realSetInterval;
  globalThis.clearInterval = realClearInterval;
});

/** Run every interval callback due within `ms` of virtual time, in order. */
async function advance(ms: number) {
  const end = clock + ms;
  for (;;) {
    let next: Interval | undefined;
    for (const entry of intervals.values()) if (entry.due <= end && (!next || entry.due < next.due)) next = entry;
    if (!next) break;
    clock = next.due;
    next.due += next.ms;
    const fn = next.fn;
    await act(async () => fn());
    await settle(5);
  }
  clock = end;
}

async function setHidden(h: boolean) {
  hidden = h;
  await act(async () => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

/** Mounted until the test ends. */
async function render(ui: Parameters<typeof mount>[0]) {
  const view = await mount(ui);
  mounted.push(view.unmount);
  return view;
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
  async function renderPeople(client: Counting) {
    await render(<PeopleProvider client={client as unknown as ApiClient}><span /></PeopleProvider>);
  }

  test("are not read while the page is hidden, and are read once it is shown", async () => {
    hidden = true;
    const client = new Counting("a");
    await renderPeople(client);
    await settle(100);
    expect(client.people).toBe(0);
    await setHidden(false);
    await settle(50);
    expect(client.people).toBe(1);
  });

  test("are read again each minute while shown, and not while hidden", async () => {
    const client = new Counting("a");
    await renderPeople(client);
    await settle(50);
    expect(client.people).toBe(1);
    await advance(60_000);
    expect(client.people).toBe(2);
    await setHidden(true);
    await advance(120_000);
    expect(client.people).toBe(2);
  });
});

describe("a Run preparing its image", () => {
  test("is read again every few seconds while shown, and not while hidden", async () => {
    const client = new Counting("a");
    const { container } = await render(<RunScreen client={client} runId={RUN_ID} onBack={() => {}} />);
    await until(() => container.querySelector("[data-testid=preparing-image]"), "the preparing callout");
    await settle(50);
    const shown = client.runs;
    await advance(4_300);
    expect(client.runs).toBeGreaterThan(shown);

    await setHidden(true);
    await settle(50);
    const atHide = client.runs;
    await advance(12_000);
    expect(client.runs).toBe(atHide);
  });
});

const NOW = new Date().toISOString();
const BUILD: ImageBuild = {
  id: "imb_1", imageId: "img_1", imageName: "acme-base", versionId: "imv_1", version: 1, kind: "build", state: "running",
  stage: "building", layerRef: null, requestedBy: null, requestedAt: NOW, startedAt: NOW, finishedAt: null, error: null,
  buildSeconds: null, pushSeconds: null, canRunContainers: false, containersCheck: null, checkSeconds: null, ahead: null,
};
const BUILDER = { available: true, layer: "registry.test/layer@sha256:a", cpus: 1, memoryMiB: 1024, lastSeenAt: NOW, offline: false };
const IMAGE = {
  id: "img_1", name: "acme-base", description: "", archivedAt: null, createdAt: NOW, createdBy: null, isDefault: false,
  published: null, pending: { versionId: "imv_1", number: 1, state: "building" as const, error: null }, draft: null,
  parents: [], from: "debian:bookworm-slim", usedBy: [], lastChange: { at: NOW, by: null, source: "person" as const },
};

/** The image library with one build running, counting each kind of read. */
function liveBuild() {
  const reads = { list: 0, image: 0, build: 0 };
  const client = Object.assign(new FixtureClient("a") as unknown as ApiClient, {
    images: (): Promise<ImagesResponse> => {
      reads.list++;
      return Promise.resolve({ images: [IMAGE], queue: [BUILD], defaultImageId: null, builder: BUILDER, canEdit: true });
    },
    image: (): Promise<ImageDetail> => {
      reads.image++;
      return Promise.resolve({ image: IMAGE, previewedBy: [], versions: [], builds: [BUILD], builder: BUILDER, canEdit: true });
    },
    imageBuild: (): Promise<ImageBuildWithLog> => {
      reads.build++;
      return Promise.resolve({ ...BUILD, log: "step 1\n", logStart: 0, logTotal: 7, published: null, note: "", builder: BUILDER });
    },
  });
  return { client, reads };
}

function Images({ client, sub }: { client: ApiClient; sub: string | undefined }) {
  const images = useImages(client);
  return <ImagesPage client={client} orgName="Acme" images={images} sub={sub} onSub={() => {}} />;
}

describe("the image library while a build runs", () => {
  for (const [what, sub, read] of [
    ["the list", undefined, "list"],
    ["an image", "img_1", "image"],
    ["a build's log", "builds/imb_1", "build"],
  ] as const) {
    test(`${what} is read again while shown, and not while hidden`, async () => {
      const { client, reads } = liveBuild();
      await render(
        <TooltipProvider><ToastProvider><Images client={client} sub={sub} /></ToastProvider></TooltipProvider>,
      );
      await settle(50);
      const shown = reads[read];
      await advance(8_000);
      expect(reads[read]).toBeGreaterThan(shown);

      await setHidden(true);
      const atHide = reads[read];
      await advance(16_000);
      expect(reads[read]).toBe(atHide);
    });
  }
});

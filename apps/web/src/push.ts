/**
 * This browser's notifications: whether they can work here, whether it is
 * subscribed, and subscribing or not (docs/design/notifications.md). The
 * service worker (public/sw.js) shows what dude pushes; it is registered
 * once, at start (startPush).
 *
 * A browser has one subscription, and the backend keeps it for one
 * organisation (`claim_push_subscription` moves it on each claim). Which
 * organisation that is, is recorded here per endpoint (PUSH_ORGANIZATION),
 * since no route answers it.
 */

import type { ApiClient } from "./api/client.ts";

export type PushState =
  | "unsupported" // no service workers or Push API, or not a secure context
  | "blocked" // the person, or the browser, said no
  | "off"
  | "on";

/** The organisation a subscription was claimed for. */
export interface PushOrganization {
  id: string;
  name: string;
}

/** localStorage key prefix: `<prefix><endpoint>` holds the PushOrganization it was claimed for. */
export const PUSH_ORGANIZATION = "dude.push.org:";

function supported(): boolean {
  return window.isSecureContext && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
}

async function subscription(): Promise<PushSubscription | null> {
  return (await navigator.serviceWorker.ready).pushManager.getSubscription();
}

function recorded(endpoint: string): PushOrganization | null {
  try {
    const value = JSON.parse(localStorage.getItem(PUSH_ORGANIZATION + endpoint) ?? "null") as PushOrganization | null;
    return value && typeof value.id === "string" && typeof value.name === "string" ? value : null;
  } catch {
    return null;
  }
}

async function claim(client: ApiClient, sub: PushSubscription, organization: PushOrganization | null): Promise<void> {
  await client.subscribePush(sub.toJSON());
  if (organization) localStorage.setItem(PUSH_ORGANIZATION + sub.endpoint, JSON.stringify(organization));
}

/**
 * At start: register the service worker, and if this browser is subscribed
 * and `reclaim` holds, tell dude again — idempotent, and it puts back a
 * subscription dude forgot (its push service said it was gone). Someone in
 * several organisations passes `reclaim: false`: a claim moves the one
 * subscription, so only Notify me in this browser does that.
 * A clicked notification opens its place here when the tab cannot be
 * navigated by the worker.
 */
export function startPush(client: ApiClient, open: (hash: string) => void,
  { reclaim = true, organization = null }: { reclaim?: boolean; organization?: PushOrganization | null } = {}): () => void {
  if (!supported()) return () => {};
  void navigator.serviceWorker.register("/sw.js").then(async () => {
    const sub = reclaim && Notification.permission === "granted" ? await subscription() : null;
    if (sub) await claim(client, sub, organization).catch(() => {});
  });
  const onMessage = (e: MessageEvent) => {
    if (e.data?.type === "dude.open" && typeof e.data.url === "string") open(e.data.url);
  };
  navigator.serviceWorker.addEventListener("message", onMessage);
  return () => navigator.serviceWorker.removeEventListener("message", onMessage);
}

export async function pushState(): Promise<PushState> {
  if (!supported()) return "unsupported";
  if (Notification.permission === "denied") return "blocked";
  return Notification.permission === "granted" && (await subscription()) ? "on" : "off";
}

/** The organisation this browser's subscription was last claimed for here, if it is subscribed and that was recorded. */
export async function pushOrganization(): Promise<PushOrganization | null> {
  if (!supported() || Notification.permission !== "granted") return null;
  const sub = await subscription();
  return sub ? recorded(sub.endpoint) : null;
}

/** Ask for permission, subscribe with dude's key, and claim it for `client`'s organisation. */
export async function turnPushOn(client: ApiClient, organization: PushOrganization | null = null): Promise<PushState> {
  if (!supported()) return "unsupported";
  const permission = await Notification.requestPermission();
  if (permission !== "granted") return permission === "denied" ? "blocked" : "off";
  const reg = await navigator.serviceWorker.ready;
  const { publicKey } = await client.pushKey();
  const sub =
    (await reg.pushManager.getSubscription()) ??
    (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: fromBase64Url(publicKey) }));
  await claim(client, sub, organization);
  return "on";
}

/**
 * Stop: dude forgets this browser, and the browser its subscription. Also on
 * sign-out. The removal is sent as the organisation it was claimed for when
 * that is recorded, else as `client`'s: a credential naming none is refused
 * for someone in several.
 */
export async function turnPushOff(client: ApiClient): Promise<PushState> {
  if (!supported()) return "unsupported";
  const registration = await navigator.serviceWorker.getRegistration();
  const sub = registration ? await registration.pushManager.getSubscription() : null;
  if (sub) {
    const owner = recorded(sub.endpoint);
    const remover = owner && owner.id !== client.organizationId ? client.root.inOrganization(owner.id) : client;
    await remover.unsubscribePush(sub.endpoint).catch(() => {});
    localStorage.removeItem(PUSH_ORGANIZATION + sub.endpoint);
    await sub.unsubscribe();
  }
  return "off";
}

/**
 * Sign-out's removal: through the mounted organisation's client when there
 * is one, else the session's (`root`), which names none.
 */
export const pushOffOnSignOut = (mounted?: ApiClient) => (root: ApiClient): Promise<PushState> => turnPushOff(mounted ?? root);

/** Shown by the service worker as a push would be, signed as it signs them (sw.js): that this browser shows them. */
export async function showTestNotification(): Promise<void> {
  const reg = await navigator.serviceWorker.ready;
  await reg.showNotification("His Dudeness", { body: "Notifications are on in this browser.", icon: "/icon-192.png", tag: "test" });
}

function fromBase64Url(s: string): Uint8Array<ArrayBuffer> {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (s.length % 4)) % 4);
  const raw = atob(b64);
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

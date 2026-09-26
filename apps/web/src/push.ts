/**
 * This browser's notifications: whether they can work here, whether it is
 * subscribed, and subscribing or not (docs/design/notifications.md). The
 * service worker (public/sw.js) shows what dude pushes; it is registered
 * once, at start (startPush).
 */

import type { ApiClient } from "./api/client.ts";

export type PushState =
  | "unsupported" // no service workers or Push API, or not a secure context
  | "blocked" // the person, or the browser, said no
  | "off"
  | "on";

function supported(): boolean {
  return window.isSecureContext && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
}

async function subscription(): Promise<PushSubscription | null> {
  return (await navigator.serviceWorker.ready).pushManager.getSubscription();
}

/**
 * At start: register the service worker, and if this browser is subscribed,
 * tell dude again — idempotent, and it puts back a subscription dude forgot
 * (its push service said it was gone) or one made for another organization.
 * A clicked notification opens its place here when the tab cannot be
 * navigated by the worker.
 */
export function startPush(client: ApiClient, open: (hash: string) => void): () => void {
  if (!supported()) return () => {};
  void navigator.serviceWorker.register("/sw.js").then(async () => {
    const sub = Notification.permission === "granted" ? await subscription() : null;
    if (sub) await client.subscribePush(sub.toJSON()).catch(() => {});
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

/** Ask for permission, subscribe with dude's key, and register. */
export async function turnPushOn(client: ApiClient): Promise<PushState> {
  if (!supported()) return "unsupported";
  const permission = await Notification.requestPermission();
  if (permission !== "granted") return permission === "denied" ? "blocked" : "off";
  const reg = await navigator.serviceWorker.ready;
  const { publicKey } = await client.pushKey();
  const sub =
    (await reg.pushManager.getSubscription()) ??
    (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: fromBase64Url(publicKey) }));
  await client.subscribePush(sub.toJSON());
  return "on";
}

/** Stop: dude forgets this browser, and the browser its subscription. Also on sign-out. */
export async function turnPushOff(client: ApiClient): Promise<PushState> {
  if (!supported()) return "unsupported";
  const sub = await subscription();
  if (sub) {
    await client.unsubscribePush(sub.endpoint).catch(() => {});
    await sub.unsubscribe();
  }
  return "off";
}

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

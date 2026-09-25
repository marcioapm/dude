/**
 * This browser's notifications: whether they can work here, whether it is
 * subscribed, and subscribing or not (docs/design/notifications.md). The
 * service worker (public/sw.js) shows what dude pushes.
 */

import type { ApiClient } from "./api/client.ts";

export type PushState =
  | "unsupported" // no service workers or Push API, or not a secure context
  | "blocked" // the person, or the browser, said no
  | "off"
  | "on";

export function pushSupported(): boolean {
  return typeof window !== "undefined" && window.isSecureContext && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
}

async function registration(): Promise<ServiceWorkerRegistration> {
  return navigator.serviceWorker.register("/sw.js");
}

export async function pushState(): Promise<PushState> {
  if (!pushSupported()) return "unsupported";
  if (Notification.permission === "denied") return "blocked";
  const reg = await navigator.serviceWorker.getRegistration("/");
  const sub = await reg?.pushManager.getSubscription();
  return sub && Notification.permission === "granted" ? "on" : "off";
}

/** Ask for permission, subscribe with dude's key, and register. */
export async function turnPushOn(client: ApiClient): Promise<PushState> {
  if (!pushSupported()) return "unsupported";
  const permission = await Notification.requestPermission();
  if (permission !== "granted") return permission === "denied" ? "blocked" : "off";
  const reg = await registration();
  await navigator.serviceWorker.ready;
  const { publicKey } = await client.pushKey();
  const sub =
    (await reg.pushManager.getSubscription()) ??
    (await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: fromBase64Url(publicKey) }));
  await client.subscribePush(sub.toJSON());
  return "on";
}

export async function turnPushOff(client: ApiClient): Promise<PushState> {
  const reg = await navigator.serviceWorker.getRegistration("/");
  const sub = await reg?.pushManager.getSubscription();
  if (sub) {
    await client.unsubscribePush(sub.endpoint);
    await sub.unsubscribe();
  }
  return "off";
}

/** Shown by the service worker, as a push would be: that this browser shows them. */
export async function showTestNotification(): Promise<void> {
  const reg = await registration();
  await reg.showNotification("dude", { body: "Notifications are on in this browser.", icon: "/icon.svg", tag: "test" });
}

function fromBase64Url(s: string): Uint8Array<ArrayBuffer> {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (s.length % 4)) % 4);
  const raw = atob(b64);
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

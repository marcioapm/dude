/**
 * This browser's notifications: whether they can work here, whether they
 * are on in an organisation, and turning them on or off
 * (docs/design/notifications.md). The service worker (public/sw.js) shows
 * what dude pushes; it is registered once, at start (startPush).
 *
 * A browser has one PushSubscription; the backend registers it per
 * organisation, so each organisation is on or off alone. The browser's
 * subscription is dropped only when no organisation still uses it, and on
 * sign-out, which removes it from every membership.
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

/** The browser's subscription without waiting for a worker: none when none was ever registered. */
async function existingSubscription(): Promise<PushSubscription | null> {
  const registration = await navigator.serviceWorker.getRegistration();
  return registration ? registration.pushManager.getSubscription() : null;
}

/**
 * At start: register the service worker, and if this browser is subscribed
 * and `reclaim` holds, tell dude again — idempotent, and it puts back a
 * subscription dude forgot (its push service said it was gone). Someone in
 * several organisations passes `reclaim: false`: each organisation is
 * turned on by its own Notify me, never by opening it.
 * A clicked notification opens its place here when the tab cannot be
 * navigated by the worker.
 */
export function startPush(client: ApiClient, open: (hash: string) => void, { reclaim = true }: { reclaim?: boolean } = {}): () => void {
  if (!supported()) return () => {};
  void navigator.serviceWorker.register("/sw.js").then(async () => {
    const sub = reclaim && Notification.permission === "granted" ? await subscription() : null;
    if (sub) await client.subscribePush(sub.toJSON()).catch(() => {});
  });
  const onMessage = (e: MessageEvent) => {
    if (e.data?.type === "dude.open" && typeof e.data.url === "string") open(e.data.url);
  };
  navigator.serviceWorker.addEventListener("message", onMessage);
  return () => navigator.serviceWorker.removeEventListener("message", onMessage);
}

/** Whether notifications are on in `client`'s organisation: this browser is subscribed and registered there. */
export async function pushState(client: ApiClient): Promise<PushState> {
  if (!supported()) return "unsupported";
  if (Notification.permission === "denied") return "blocked";
  if (Notification.permission !== "granted") return "off";
  const sub = await subscription();
  if (!sub) return "off";
  const { subscribed } = await client.pushSubscribed(sub.endpoint).catch(() => ({ subscribed: false }));
  return subscribed ? "on" : "off";
}

/** Ask for permission, subscribe with dude's key (or reuse the browser's subscription), and register it in `client`'s organisation. */
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

/**
 * Off in `client`'s organisation only. The browser's subscription is
 * dropped when none of `elsewhere` (your other organisations' clients)
 * still has it registered; one that cannot be asked counts as using it.
 */
export async function turnPushOff(client: ApiClient, elsewhere: readonly ApiClient[] = []): Promise<PushState> {
  if (!supported()) return "unsupported";
  const sub = await existingSubscription();
  if (!sub) return "off";
  await client.unsubscribePush(sub.endpoint);
  const used = await Promise.all(elsewhere.map((other) =>
    other.pushSubscribed(sub.endpoint).then((r) => r.subscribed, () => true)));
  if (!used.includes(true)) await sub.unsubscribe();
  return "off";
}

/**
 * Sign-out's removal: this browser's registration in every one of
 * `organizationIds` (your memberships), each through the session's
 * credential naming that organisation, then the browser's subscription.
 * A removal that fails does not keep the others, or the browser's
 * unsubscribe, from running: its row goes when the push service reports
 * the endpoint gone.
 */
export const pushOffOnSignOut = (organizationIds: readonly string[]) => async (root: ApiClient): Promise<PushState> => {
  if (!supported()) return "unsupported";
  const sub = await existingSubscription();
  if (!sub) return "off";
  await Promise.all(organizationIds.map((id) => root.inOrganization(id).unsubscribePush(sub.endpoint).catch(() => {})));
  await sub.unsubscribe();
  return "off";
};

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

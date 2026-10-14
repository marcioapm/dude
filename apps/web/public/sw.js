/*
 * dude's service worker: shows the notifications dude pushes when
 * something waits on you (docs/design/notifications.md), and opens the
 * conversation when one is clicked. It caches nothing.
 */

// Control the tab that registered it at once, not after a reload: a click
// can only navigate a tab this worker controls.
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));

// Every notification is signed by him, whatever it is about.
const SIGNED = "His Dudeness";
const signed = (title) => (title ? `${SIGNED} · ${title}` : SIGNED);

self.addEventListener("push", (event) => {
  let message = {};
  try {
    message = event.data ? event.data.json() : {};
  } catch {
    message = { body: event.data ? event.data.text() : "" };
  }
  event.waitUntil(
    self.registration.showNotification(signed(message.title), {
      body: message.body || "",
      // One per Run: a second ask from it replaces the first.
      tag: message.tag || undefined,
      renotify: Boolean(message.tag),
      data: { url: message.url || "" },
      icon: "/icon-192.png",
    }),
  );
});

// A notification's url is the app's hash: `#/o/<slug>/…` from a dude that
// names the organisation, or a bare `#/task/…` from an older one, which the
// app places among your organisations itself (/v1/locate).
const organisationOf = (hash) => {
  const m = /^#\/o\/([^/?#]+)\//.exec(hash);
  return m ? m[1] : null;
};

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const hash = event.notification.data?.url || "";
  const target = new URL("/" + hash, self.location.origin).href;
  const organisation = organisationOf(hash);
  event.waitUntil(
    (async () => {
      // An open dude tab goes there, one already in the notification's
      // organisation first; otherwise a new one. The hash is the app's
      // place, so a tab it cannot navigate is told by message.
      const tabs = (await self.clients.matchAll({ type: "window", includeUncontrolled: true }))
        .filter((tab) => new URL(tab.url).origin === self.location.origin);
      const tab = (organisation && tabs.find((t) => organisationOf(new URL(t.url).hash) === organisation)) || tabs[0];
      if (tab) {
        await tab.focus();
        try {
          return await tab.navigate(target);
        } catch {
          tab.postMessage({ type: "dude.open", url: hash });
          return undefined;
        }
      }
      return self.clients.openWindow(target);
    })(),
  );
});

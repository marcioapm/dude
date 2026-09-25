/*
 * dude's service worker: shows the notifications dude pushes when
 * something waits on you (docs/design/notifications.md), and opens the
 * conversation when one is clicked. It caches nothing.
 */

self.addEventListener("push", (event) => {
  let message = {};
  try {
    message = event.data ? event.data.json() : {};
  } catch {
    message = { title: "dude", body: event.data ? event.data.text() : "" };
  }
  event.waitUntil(
    self.registration.showNotification(message.title || "dude", {
      body: message.body || "",
      // One per Run: a second ask from it replaces the first.
      tag: message.tag || undefined,
      renotify: Boolean(message.tag),
      data: { url: message.url || "" },
      icon: "/icon.svg",
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const target = new URL("/" + (event.notification.data?.url || ""), self.location.origin).href;
  event.waitUntil(
    (async () => {
      // An open dude tab goes there; otherwise a new one.
      const tabs = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      for (const tab of tabs) {
        if (new URL(tab.url).origin === self.location.origin) {
          await tab.focus();
          return tab.navigate(target);
        }
      }
      return self.clients.openWindow(target);
    })(),
  );
});

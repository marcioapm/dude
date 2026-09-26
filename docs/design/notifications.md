# Telling a person something waits on them

## What

1. **Waiting on you**: a full view in the web app of everything waiting on
   a person, across projects, oldest first.
   - It reuses the design system: the sidebar's `AttentionList` rows,
     `QuestionCard` for questions, and the repository-request card.
   - The sidebar's "Needs you" section keeps its five rows and gains a
     link to the full view ("and N more" becomes that link).
2. **Browser notifications** (Web Push): when an agent asks a question or
   asks for a repository, each browser that opted in gets a system
   notification, even with the tab closed. Clicking it opens the Run's
   chat.

## Web Push, in short

- The web app registers a **service worker** (`/sw.js`), asks for permission
  and **subscribes** with dude's **VAPID public key**. The browser returns a
  subscription: an endpoint URL at the browser vendor's push service, plus
  two keys.
- dude stores the subscription (`push_subscriptions`, per organization and
  API key).
- When an ask is recorded, dude sends an **encrypted** message to each
  subscription's endpoint, signed with the VAPID private key. The browser
  wakes the service worker, which shows the notification.
- Chrome, Edge, Firefox and Safari (macOS; iOS 16.4+ for a web app added
  to the home screen) support it. Browsers need a secure context:
  `https`, or `localhost`, which counts as secure for development.

## Who sends

The **orchestrator** already sees every ask: `ask_person`,
`request_repository`, and escalations (`question.asked` with kind
`escalation`). It gets a small `notify` loop:

- It reads new `question.asked` and `repository.requested` events past a
  stored cursor (`notify_cursor`, one row).
- It sends one push per subscription of that organization.
- A subscription the push service answers with 404 or 410 has gone
  (unsubscribed or expired), so it is deleted.
- Sending is best effort. A failed push is logged, never retried forever,
  and never blocks the Run: the board still shows the ask.

Library: `github.com/SherClockHolmes/webpush-go` (VAPID and RFC 8291
encryption).

The keys come from `DUDE_VAPID_PUBLIC_KEY` and `DUDE_VAPID_PRIVATE_KEY`
(`DUDE_VAPID_SUBJECT` defaults to `mailto:` the operator). If they're
missing, the orchestrator generates a pair once and stores it in
`push_config`, so a development stack works with no setup.

## API (backend)

- `GET /v1/push/key`: the VAPID public key, which the browser needs to
  subscribe.
- `POST /v1/push/subscriptions {endpoint, keys}` and
  `DELETE /v1/push/subscriptions {endpoint}`: register or remove the
  calling browser.

## Settings

"You" gets a **Notifications** card:
- off, or on for this browser (asking permission);
- "Send a test notification";
- the state if the browser blocked it.

## What the notification says

- Title: `TEXT-19 · Implement asks` (the task key, then the role).
- Body: the question, or "Read web? — the client calls this API" for a
  repository request.
- Tag: the Run, so a second ask from the same Run replaces the first.
- Click: focuses an open dude tab (or opens one) at `#/run/<id>`.

## Later: desktop and mobile

- **Desktop:** a Tauri or Electron shell loads this same web app, so the
  service worker and Web Push work unchanged. Or, natively, the shell
  subscribes to the event stream and shows OS notifications itself.
- **Mobile:** native apps get pushes through **APNs** (Apple) and **FCM**
  (Google). The notify loop gains a second kind of subscription (a device
  token plus a platform) and a sender for each. The events, the cursor and
  the message stay the same; only delivery differs.

## Tests

- **Go:** the notify loop sends to a fake push service (an httptest
  server). The payload decrypts with the subscription's keys, one message
  goes per subscription, a 410 deletes the subscription, and the cursor
  moves on.
- **End to end (Playwright):** Chromium supports push only with a real
  push service. So the test grants the notification permission, subscribes
  through the app (the fake push service stands in via a subscription the
  test registers), and checks the fake push service receives the ask. The
  service worker's display code gets a unit test.
- **UI:** the "Waiting on you" view lists a question and a request,
  oldest first, and answers from it.

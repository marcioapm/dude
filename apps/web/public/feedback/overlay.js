// dude's preview overlay: lux adds <script src="<dude>/feedback/overlay.js"
// data-dude-origin="<dude>" async> to a branch preview's pages, and this is
// served to them from dude's origin with no credential. This release only
// says it loaded; commenting on the page comes with the overlay itself.
(function () {
  "use strict";
  if (window.__dudeOverlay) return;
  window.__dudeOverlay = true;
  console.debug("dude: preview overlay loaded; commenting is not available yet");
})();

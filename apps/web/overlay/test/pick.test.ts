import { expect, test } from "bun:test";
import { resolveOne, selectorFor } from "../pick.ts";
import { tooBig } from "../picture.ts";

test("a selector is built that finds the element and nothing else, also among look-alike siblings", () => {
  document.body.innerHTML = `<ul class="list"><li class="row"><a>one</a></li><li class="row"><a>two</a></li><li class="row"><a>three</a></li></ul>
    <section><button data-testid="go">Go</button></section><div id="k9f3a2b1c7d8e"><span>x</span></div>`;
  const second = document.querySelectorAll("li.row a")[1]!;
  const sel = selectorFor(second);
  expect(resolveOne(document, sel)).toBe(second);
  expect(selectorFor(document.querySelector("button")!)).toBe('button[data-testid="go"]');
  // A generated id is not trusted to last: the path is used instead, and still finds it alone.
  const span = document.querySelector("span")!;
  expect(selectorFor(span)).not.toContain("k9f3a2b1c7d8e");
  expect(resolveOne(document, selectorFor(span))).toBe(span);
});

test("resolveOne finds nothing for a selector matching several, none, or one that does not parse", () => {
  document.body.innerHTML = `<p class="a"></p><p class="a"></p>`;
  expect(resolveOne(document, "p.a")).toBeNull();
  expect(resolveOne(document, "p.b")).toBeNull();
  expect(resolveOne(document, "::nope(")).toBeNull();
  expect(resolveOne(document, "")).toBeNull();
});

test("a picture is off by default for an element larger than the window", () => {
  const el = document.createElement("div");
  const box = (width: number, height: number) => {
    el.getBoundingClientRect = () => ({ x: 0, y: 0, left: 0, top: 0, width, height, right: width, bottom: height, toJSON: () => ({}) }) as DOMRect;
  };
  const win = { innerWidth: 1280, innerHeight: 720 } as Window;
  box(400, 300);
  expect(tooBig(el, win)).toBe(false);
  box(1300, 300);
  expect(tooBig(el, win)).toBe(true);
  box(400, 900);
  expect(tooBig(el, win)).toBe(true);
});

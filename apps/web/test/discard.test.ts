/**
 * When closing a form asks before losing what was written.
 */

import { describe, expect, test } from "bun:test";
import { unsavedWords, wordCount } from "../src/hooks/discard.ts";

const words = (n: number) => Array.from({ length: n }, (_, i) => `w${i}`).join(" ");

describe("unsavedWords", () => {
  test("nothing changed: nothing to ask, however long", () => {
    expect(unsavedWords([words(500), "x"], [words(500), "x"])).toBe(0);
  });

  test("twenty new words or fewer are not worth a confirmation; twenty-one are", () => {
    expect(unsavedWords(["", ""], [words(19), ""])).toBe(0);
    expect(unsavedWords(["", ""], [words(20), ""])).toBe(0);
    expect(unsavedWords(["", ""], [words(21), ""])).toBe(21);
  });

  test("counts the words of every changed field and only those", () => {
    expect(unsavedWords([words(100), ""], [words(100), words(21)])).toBe(21);
    expect(unsavedWords(["a", "b"], [words(15), words(10)])).toBe(25);
  });

  test("an edit to a long existing text counts the whole text", () => {
    expect(unsavedWords([words(40)], [words(40) + "!"])).toBe(40);
  });

  test("clearing a field is not writing", () => {
    expect(unsavedWords([words(40)], [""])).toBe(0);
  });
});

describe("wordCount", () => {
  test("splits on any whitespace and ignores the ends", () => {
    expect(wordCount("")).toBe(0);
    expect(wordCount("  \n ")).toBe(0);
    expect(wordCount(" one\ttwo\n\nthree ")).toBe(3);
  });
});

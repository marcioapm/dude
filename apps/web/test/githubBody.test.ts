import { describe, expect, test } from "bun:test";
import { githubBody, type GitHubSection } from "../src/githubBody.ts";

const markdown = (source: string) => ({ kind: "markdown" as const, source });

describe("GitHub review bodies", () => {
  test("drops publication comments, inline checkbox metadata and the autofix block", () => {
    expect(githubBody(`**Actionable comments posted: 2**

<!-- autofix_checkbox_start -->
- [ ] <!-- {"checkboxId":"4b0d0e0a"} --> Fix comments on this PR
<!-- autofix_checkbox_end -->

- [ ] <!-- {"checkboxId":"585bb3f6"} --> Fix pre-merge checks

<!-- coderabbit-review-publication v1 -->`)).toEqual([
      markdown("**Actionable comments posted: 2**\n\n- [ ]  Fix pre-merge checks"),
    ]);
    expect(githubBody("before<!-- ` -->after ` text")).toEqual([markdown("beforeafter ` text")]);
    expect(githubBody("before<!-- autofix_checkbox_start -->after")).toEqual([markdown("beforeafter")]);
  });

  test("nested review info becomes disclosures with plain summaries", () => {
    expect(githubBody(`<details>
<summary><strong>Recent review info</strong></summary>
<dl><dd>
<details>
<summary><strong>Run configuration</strong></summary>
<dl><dd>
- **Review profile**: CHILL
</dd></dl>
</details>
Included review availability.
</dd></dl>
</details>`)).toEqual([{
      kind: "details", summary: "Recent review info", children: [
        { kind: "details", summary: "Run configuration", children: [markdown("- **Review profile**: CHILL")] },
        markdown("Included review availability."),
      ],
    }]);
    expect(githubBody("<details><summary></summary>Keep the body</details>")).toEqual([
      { kind: "details", summary: "Details", children: [markdown("Keep the body")] },
    ]);
    expect(githubBody("<details><details><summary>Inner</summary>Body</details></details>")).toEqual([
      { kind: "details", summary: "Details", children: [{ kind: "details", summary: "Inner", children: [markdown("Body")] }] },
    ]);
  });

  test("fences and inline code keep tags, comments, entities and blank lines literally", () => {
    const code = '```diff\n+ <strong>literal</strong>\n\n\n<!-- autofix_checkbox_start -->\n&amp;\n```';
    const tilde = '~~~~suggestion\n<details><summary>literal</summary></details>\n~~~\n~~~~';
    const inline = '`<img src="pixel"> <!-- literal --> &amp;` and `` `<details>` ``';
    for (const source of [code, tilde, inline, "```\n<unclosed>", "\\`<b>bold</b>"]) {
      expect(githubBody(source)).toEqual([markdown(source === "\\`<b>bold</b>" ? "\\`**bold**" : source)]);
    }
    expect(githubBody(`<details><summary>Suggested fix</summary>\n\n${code}\n\n</details>`)).toEqual([
      { kind: "details", summary: "Suggested fix", children: [markdown(code)] },
    ]);
  });

  test("a fence in a quote, an indent or a list item is code up to a closer in the same container", () => {
    // A fence closes only on its own character: ~~~ inside a backtick fence is code.
    expect(githubBody("```\n~~~\n<!-- kept -->\n```")).toEqual([markdown("```\n~~~\n<!-- kept -->\n```")]);
    expect(githubBody("~~~\n```\n<!-- kept -->\n~~~")).toEqual([markdown("~~~\n```\n<!-- kept -->\n~~~")]);
    const quoted = '> ```html\n> <details><summary>literal</summary></details>\n> <!-- literal --> &amp;\n> ```\n> <b>after</b>';
    expect(githubBody(quoted)).toEqual([markdown('> ```html\n> <details><summary>literal</summary></details>\n> <!-- literal --> &amp;\n> ```\n> **after**')]);
    expect(githubBody("  ```\n  <b>literal</b>\n  ```\n<b>after</b>")).toEqual([markdown("```\n  <b>literal</b>\n  ```\n**after**")]);
    expect(githubBody("- ~~~html\n  <details><summary>literal</summary></details>\n  ~~~\n<b>after</b>")).toEqual([
      markdown("- ~~~html\n  <details><summary>literal</summary></details>\n  ~~~\n**after**"),
    ]);
    expect(githubBody("```\n<b>literal</b>\n    ```\n<b>literal</b>\n```")).toEqual([markdown("```\n<b>literal</b>\n    ```\n<b>literal</b>\n```")]);
    expect(githubBody("```js`\n<b>bold</b>")).toEqual([markdown("```js`\n**bold**")]);
    expect(githubBody("1. ```\n   <b>literal</b>\n      ```\n<b>after</b>")).toEqual([markdown("1. ```\n   <b>literal</b>\n      ```\n**after**")]);
    // A quoted fence line is not the unquoted fence's closer.
    const unquoted = '```html\n> ```\n<!-- literal -->\n<img src="literal">\n```\n<b>after</b><!-- gone -->';
    expect(githubBody(unquoted)).toEqual([markdown('```html\n> ```\n<!-- literal -->\n<img src="literal">\n```\n**after**')]);
  });

  test("unclosed comments, disclosures and summaries keep what follows them", () => {
    expect(githubBody("Before<!-- unfinished")).toEqual([markdown("Before")]);
    expect(githubBody("<details><summary>Review info</summary>Body\n\nMore")).toEqual([
      { kind: "details", summary: "Review info", children: [markdown("Body\n\nMore")] },
    ]);
    expect(githubBody("<summary>Open <b>label</b>")).toEqual([markdown("Open label")]);
    expect(githubBody("Stray</details> <b>closer</b>")).toEqual([markdown("Stray **closer**")]);
    expect(githubBody("<details><summary>Open label")).toEqual([{ kind: "details", summary: "Open label", children: [] }]);
  });

  test("autolinks, non-HTML angle brackets and unknown tags stay literal", () => {
    expect(githubBody("See <https://example.com/review>.")).toEqual([markdown("See <https://example.com/review>.")]);
    expect(githubBody("Ask <review-bot@example.com>, <mailto:a@b.c>")).toEqual([markdown("Ask <review-bot@example.com>, <mailto:a@b.c>")]);
    expect(githubBody("Use a<b when c>d.")).toEqual([markdown("Use a<b when c>d.")]);
    expect(githubBody("List<String> and <custom-tag x=1>kept</custom-tag>")).toEqual([markdown("List<String> and <custom-tag x=1>kept</custom-tag>")]);
    expect(githubBody("<https://example.com/?a=1&amp;b=2>")).toEqual([markdown("<https://example.com/?a=1&amp;b=2>")]);
    // Past the length cap a `<` is text, so its entities decode.
    const overlong = `<https://example.com/${"a".repeat(2048)}&amp;>`;
    expect(githubBody(overlong)).toEqual([markdown(overlong.replace("&amp;", "&"))]);
    expect(githubBody('<b title="a\n>x')).toEqual([markdown('<b title="a\n>x')]);
    expect(githubBody('<a "unterminated\n<b>bold</b>')).toEqual([markdown('<a "unterminated\n**bold**')]);
  });

  test("disclosures past the nesting limit flatten into their parent", () => {
    const depth = (sections: GitHubSection[]): number =>
      Math.max(0, ...sections.map((s) => s.kind === "details" ? 1 + depth(s.children) : 0));
    const deep = githubBody("<details><summary>Level</summary>".repeat(40) + "Body" + "</details>".repeat(40) + "After");
    expect(depth(deep)).toBe(32);
    let innermost = deep;
    while (innermost[0]?.kind === "details") innermost = innermost[0].children;
    expect(innermost).toEqual([markdown(Array(8).fill("**Level**").join("\n\n") + "\n\nBody")]);
    expect(deep.at(-1)).toEqual(markdown("After"));
    expect(depth(githubBody("<details>".repeat(5000)))).toBe(32);
  });

  test("adversarial bodies parse in linear time", () => {
    let ticks = "";
    for (let n = 1; ticks.length < 400_000; n++) ticks += "x" + "`".repeat(n);
    // Each is about 400k characters: the parser reads one in tens of milliseconds, a quadratic scan in minutes.
    // The bound is loose so a busy runner cannot fail it.
    const bodies = [
      '<a "'.repeat(100_000),
      "<details>".repeat(45_000),
      "<details>".repeat(22_000) + "</details>".repeat(22_000),
      ticks,
      "\uE000".repeat(400_000),
      "<x:".repeat(133_334),
      // Fence lines at mismatched quote depths and indents: pairs that close in their own container, and one unclosed opener.
      "> ```\n```\n> > ```\n".repeat(22_000),
      "~~~~\n" + "> ~~~~\n      ~~~~\n```\n".repeat(20_000),
    ];
    for (const body of bodies) {
      const start = performance.now();
      expect(() => githubBody(body)).not.toThrow();
      expect(performance.now() - start).toBeLessThan(5000);
    }
    expect(githubBody("\uE000`a`\uE000")).toEqual([markdown("\uE000`a`\uE000")]);
  });

  test("drops picture pixels and empty image links, keeps safe links and unwraps other tags", () => {
    expect(githubBody(`<a href="https://app.coderabbit.ai/change-stack"><picture><source srcset="https://example.com/pixel"><img alt="Review in Change Stack" src="https://example.com/pixel"></picture></a>

<p><strong>Review</strong> <b>info</b> <em>now</em> <i>please</i> <code>isDue()</code><br>Next</p>
<div><dl><dt>Label</dt><dd>Value</dd></dl><sub>sub</sub><sup>sup</sup><unknown>text</unknown></div>
<a href="https://example.com/review?a=1&amp;b=2">Review &amp; checks</a>
<a href="javascript:alert(1)">Unsafe</a><picture><b>badge</b></picture>`)).toEqual([markdown('**Review** **info** *now* *please* ` isDue() `\nNext\nLabelValuesubsup<unknown>text</unknown>\n[Review & checks](https://example.com/review?a=1&b=2)\nUnsafe')]);
  });

  test("decodes common entities once without reinterpreting decoded tags", () => {
    expect(githubBody('<p>&amp; &lt;tags&gt; &quot;word&quot; &#39;quote&#39; &nbsp; &amp;lt;</p>')).toEqual([
      markdown('& <tags> "word" \'quote\'   &lt;'),
    ]);
    expect(githubBody('<details><summary><b>A &amp; B</b></summary>&lt;literal&gt;</details>')).toEqual([
      { kind: "details", summary: "A & B", children: [markdown("<literal>")] },
    ]);
    // A browser decodes entities inside an HTML <code>, so GitHub shows this as `<b>`.
    expect(githubBody("<code>&lt;b&gt;</code>")).toEqual([markdown("` <b> `")]);
  });

  test("collapses blank removal gaps but leaves jervasion Markdown unchanged", () => {
    expect(githubBody("Before\n\n<!-- review -->\n\n\nAfter")).toEqual([markdown("Before\n\nAfter")]);
    const source = `**CHANGES REQUESTED** - The slot calculation admits nonexistent loads.

### Findings
Three independently actionable findings are inline: midnight slot rollover and freshness gating.

### Verification
- \`./gradlew :previewer:test\`: 11 tests, 1 failure; all 9 new freshness tests pass.
- The formatter check fails in \`DataFreshness.java:32-43\`; run the repository formatter before merging.`;
    expect(githubBody(source)).toEqual([markdown(source)]);
  });
});

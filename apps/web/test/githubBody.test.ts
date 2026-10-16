import { describe, expect, test } from "bun:test";
import { githubBody } from "../src/githubBody.ts";

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

  test("drops picture pixels and empty image links, keeps safe links and unwraps other tags", () => {
    expect(githubBody(`<a href="https://app.coderabbit.ai/change-stack"><picture><source srcset="https://example.com/pixel"><img alt="Review in Change Stack" src="https://example.com/pixel"></picture></a>

<p><strong>Review</strong> <b>info</b> <em>now</em> <i>please</i> <code>isDue()</code><br>Next</p>
<div><dl><dt>Label</dt><dd>Value</dd></dl><sub>sub</sub><sup>sup</sup><unknown>text</unknown></div>
<a href="https://example.com/review?a=1&amp;b=2">Review &amp; checks</a>
<a href="javascript:alert(1)">Unsafe</a>`)).toEqual([markdown('**Review** **info** *now* *please* ` isDue() `\nNext\nLabelValuesubsuptext\n[Review & checks](https://example.com/review?a=1&b=2)\nUnsafe')]);
  });

  test("decodes common entities once without reinterpreting decoded tags", () => {
    expect(githubBody('<p>&amp; &lt;tags&gt; &quot;word&quot; &#39;quote&#39; &nbsp; &amp;lt;</p>')).toEqual([
      markdown('& <tags> "word" \'quote\'   &lt;'),
    ]);
    expect(githubBody('<details><summary><b>A &amp; B</b></summary>&lt;literal&gt;</details>')).toEqual([
      { kind: "details", summary: "A & B", children: [markdown("<literal>")] },
    ]);
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

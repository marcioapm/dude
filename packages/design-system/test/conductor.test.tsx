/**
 * A task's Chat in the design system: the history line, the turns of a
 * conversation with its conductor (a person's message, dude's briefing,
 * the conductor's answer), dude's signed notice, and the chat composer.
 * Server-rendered markup in happy-dom: what is shown and what is a button.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ChatComposer } from "../src/components/ChatComposer.tsx";
import { ChatMessage } from "../src/components/ChatMessage.tsx";
import { ChatNotice } from "../src/components/ChatNotice.tsx";
import { TaskHistory } from "../src/components/TaskHistory.tsx";
import { ROLE_LABEL } from "../src/components/AgentAvatar.tsx";

const html = (el: React.ReactElement) => renderToStaticMarkup(el);
const plain = (h: string) => h.replace(/<[^>]+>/g, "");

describe("TaskHistory", () => {
  test("says how it went, what ran with arrows between, and what it came to", () => {
    const h = html(<TaskHistory lead="Delivered automatically" steps={["implementer", "reviewers ×3", "PR #88"]} facts={["5 findings, all settled", "$9.80"]} />);
    expect(plain(h)).toBe("Delivered automatically · implementer → reviewers ×3 → PR #88 · 5 findings, all settled · $9.80");
    expect(h).toContain('aria-label="then"');
  });

  test("with nothing run, only the lead", () => {
    expect(plain(html(<TaskHistory lead="Not started" />))).toBe("Not started");
  });
});

describe("the turns of a Chat", () => {
  test("a person's message is signed and untagged; dude's briefing is a framed, tagged prompt", () => {
    const message = html(<ChatMessage role="human" name="Márcio" intent="message" content="why 8s?" />);
    expect(message).toContain('data-intent="message"');
    expect(plain(message)).toBe("Márciowhy 8s?");
    const briefing = html(<ChatMessage role="system" name="El Duderino" intent="briefing" content="Conductor, Márcio wrote…" />);
    expect(briefing).toContain('data-kind="human"');
    expect(briefing).toContain('data-intent="briefing"');
    expect(plain(briefing)).toContain("El DuderinoBriefing");
  });

  test("the conductor answers as itself", () => {
    expect(ROLE_LABEL.conductor).toBe("Conductor");
    const h = html(<ChatMessage role="conductor" content="8s: Tiago asked for it." />);
    expect(h).toContain('data-role="conductor"');
    expect(plain(h)).toContain("Conductor8s: Tiago asked for it.");
  });

  test("dude's notice says who says it", () => {
    expect(plain(html(<ChatNotice kind="parked" by="El Duderino" text="Parked while nobody is writing." at="2026-10-02T10:00:00Z" />)))
      .toStartWith("El Duderino: Parked while nobody is writing.");
  });
});

describe("the chat composer", () => {
  test("asks about the task, sends with Send, says where it goes, and never interrupts", () => {
    const h = html(<ChatComposer mode="chat" canInterrupt sentAs="Márcio" to={<>To <b>Conductor</b> · read-only</>} onSubmit={() => {}} />);
    expect(h).toContain('data-mode="chat"');
    expect(h).toContain('placeholder="Ask about this task…"');
    expect(plain(h)).toContain("To Conductor · read-only");
    expect(plain(h)).not.toContain("Sent as");
    expect(plain(h)).not.toContain("interrupt now");
    expect([...h.matchAll(/<button\b[^>]*type="submit"[^>]*>([\s\S]*?)<\/button>/g)].map((m) => plain(m[1]!))).toEqual(["Send"]);
  });
});

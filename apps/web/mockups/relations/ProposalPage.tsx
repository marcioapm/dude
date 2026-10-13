/*
 * Screen 5: a brainstorm session's proposal with relations. The transcript,
 * turns and composer are the design system's; the card is ProposalCard's
 * anatomy (Brainstorm.module.css) with what the proposal adds: each item's
 * number, its relations under its goal, the order in the head, a blocker
 * ticked with what waits on it, and the new reason an item can't be filed.
 */

import { ChatComposer, ChatMessage, ChatTranscript, ProjectAvatar, RefLead, ScreenHeader, SessionTitle, SharedMark, StatusMark } from "@dude/design-system/components";
import { Button, Checkbox } from "@dude/design-system/primitives";
import { Icon } from "@dude/design-system";
import { P, PROPOSAL, task, type ProposedTask } from "./data.ts";
import { RelationGlyph } from "./Relations.tsx";
import styles from "./pages.module.css";

export type ProposalState = "ready" | "refused";

const T0 = Date.UTC(2026, 9, 12, 13, 40, 0);

/** Each item's relations, inline: in-proposal ones by number, existing tasks by key and status. */
function ItemRelations({ item }: { readonly item: ProposedTask }) {
  return (
    <span className={styles["itemRels"]}>
      {item.blockedBy.map((b) => {
        if ("item" in b) {
          return (
            <span key={`i${b.item}`} className={styles["itemRel"]} aria-label={`Blocked by item ${b.item}`}>
              <RelationGlyph name="lock" size={12} /> Blocked by <b>item {b.item}</b>
            </span>
          );
        }
        const t = task(b.key);
        return (
          <span key={b.key} className={styles["itemRel"]} aria-label={`Blocked by ${t.key}, ${t.status === "review" ? "in review" : t.status}`}>
            <RelationGlyph name="lock" size={12} /> Blocked by <RefLead type="task" taskKey={t.key} status={t.status} /> <StatusMark status={t.status} size="sm" />
          </span>
        );
      })}
      {(item.related ?? []).map((r) => {
        const t = task(r.key);
        return (
          <span key={r.key} className={styles["itemRel"]}>
            <RelationGlyph name="link" size={12} /> Related <RefLead type="task" taskKey={t.key} status={t.status} />
          </span>
        );
      })}
    </span>
  );
}

function RelationProposal({ state }: { readonly state: ProposalState }) {
  const refused = state === "refused";
  // The person ticked 2 and 3; 1 is ticked with them, because both wait on it.
  const picked = new Set([2, 3]);
  const withThem = new Set([1]);
  const note = (n: number): string | null => {
    if (refused) {
      return n === 1
        ? "data-pipeline is no longer linked to this session."
        : "Waits on item 1, which can't be filed: link data-pipeline again, or ask the brainstorm to propose it without item 1.";
    }
    return withThem.has(n) ? "Ticked with 2 and 3: they wait on it. Untick them to leave it out." : null;
  };
  return (
    <section className={styles["card"]} aria-label="Proposed work" data-testid="proposal-card" data-state={state}>
      <header className={styles["cardHead"]}>
        <Icon name="list-check" size={14} />
        <b>Proposed work</b>
        <span className={styles["footWords"]}>· 3 tasks, 4 relations</span>
        <span className={styles["order"]} aria-label="Order: item 1, then items 2 and 3">
          Order <span className={styles["orderNo"]}>1</span> <Icon name="arrow-right" size={12} /> <span className={styles["orderNo"]}>2</span>
          <span className={styles["orderNo"]}>3</span>
        </span>
      </header>
      <ul className={styles["items"]}>
        {PROPOSAL.map((item) => {
          const blocked = refused;
          const ticked = !refused && (picked.has(item.n) || withThem.has(item.n));
          const why = note(item.n);
          return (
            <li key={item.n} className={[styles["item"], blocked ? styles["blocked"] : ""].join(" ")} data-item={item.n}>
              <span className={styles["tick"]}>
                <Checkbox aria-label={`File item ${item.n}: ${item.title}`} checked={ticked} disabled={blocked} onCheckedChange={() => undefined} />
              </span>
              <span className={styles["itemNo"]} aria-hidden>{item.n}</span>
              <div className={styles["itemBody"]}>
                <div className={styles["itemTitle"]}>{item.title}</div>
                <div className={styles["itemDetail"]}>{item.goal}</div>
                <ItemRelations item={item} />
                {why ? <div className={styles["note"]} data-testid={blocked ? "cannot-file" : "ticked-with"}>{why}</div> : null}
              </div>
              <span className={styles["where"]}>
                <ProjectAvatar project={{ name: item.project.name, id: item.project.key }} size={16} />{item.project.name}
              </span>
            </li>
          );
        })}
      </ul>
      <footer className={styles["cardFoot"]}>
        <span className={styles["footWords"]} id="proposal-foot">
          {refused ? "Nothing here is yours to file now: each item says why." : "Filing as Márcio: 3 tasks, 1 first, with their 4 relations. Nothing starts."}
        </span>
        <Button variant="primary" size="sm" disabled={refused} aria-describedby={refused ? "proposal-foot" : undefined} data-testid="file-proposal">
          {refused ? "File" : "File 3"}
        </Button>
      </footer>
    </section>
  );
}

export function ProposalPage({ state }: { readonly state: ProposalState }) {
  return (
    <div className="screen" data-testid="session-screen">
      <ScreenHeader fillTitle title={<SessionTitle title="Usage-based billing" />}
        meta={<><SharedMark owner={P.ana} label="Shared with 2" /><span>Brainstorm</span></>} />
      <ChatTranscript fill revision={state === "ready" ? 1 : 2} turns={3}
        footer={<ChatComposer mode="chat" placeholder="Message the brainstorm…" to={<>To <b>Brainstorm</b></>} sentAs="Márcio · everyone in the session sees it" onSubmit={() => undefined} />}>
        <ChatMessage role="human" name="Márcio" person={P.marcio} intent="message" startedAt={T0}
          content="Owners should hear when they get close to their plan's limit. Propose the tasks, in the order they can be built." />
        <ChatMessage role="brainstorm" name="Brainstorm" model="claude-opus-5-5" startedAt={T0 + 20_000} endedAt={T0 + 70_000} costUsd={0.41}
          content={"Three tasks. The first records the crossings, so it waits on **CP-41** (the hourly meter, in review now); the email and the chart marker both read those rows, so they wait on the first. The marker sits on CP-44's chart, so I related them.\n\nFiling 2 or 3 files 1 with it."}
          attachments={<RelationProposal state={state} />} />
      </ChatTranscript>
    </div>
  );
}

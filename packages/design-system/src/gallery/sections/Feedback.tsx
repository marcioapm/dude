import { useState } from "react";
import type { FeedbackCodeAnchor, RunDiffFile } from "@dude/domain";
import { Block, Col, Label, Panes, Section, type PaneMode } from "../Frame.tsx";
import { LiveDiff, type DiffRange } from "../../components/LiveDiff.tsx";
import { AgentAvatar } from "../../components/AgentAvatar.tsx";
import { PersonAvatar } from "../../components/PersonAvatar.tsx";
import {
  CommentEditor, DraftsTray, FeedbackCard, SubmitFeedbackDialog, type FeedbackCardItem, type FeedbackThreadEntry,
} from "../../components/FeedbackCard.tsx";
import { Icon } from "../../icons/index.tsx";

const ana = { id: "p_ana", name: "Ana Ribeiro" };
const marcio = { id: "p_marcio", name: "Márcio Martins" };
const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
const l = (kind: " " | "+" | "-", old: number | null, n: number | null, text: string) => ({ kind, old, new: n, text });

const FILE: RunDiffFile = {
  path: "orchestrator/internal/shim/acp.go", status: "M", additions: 5, deletions: 1,
  hunks: [{ header: "@@ -38,6 +38,10 @@", lines: [
    l(" ", 38, 38, "// toolTracker follows the agent's tool calls from the ACP stream."),
    l(" ", 39, 39, "type toolTracker struct {"),
    l("-", 40, null, "\topen int"),
    l("+", null, 40, "\tmu      sync.Mutex"),
    l("+", null, 41, "\topen    map[string]time.Time"),
    l("+", null, 42, "\tsettled chan struct{}"),
    l(" ", 41, 43, "}"),
    l("+", null, 44, ""),
    l("+", null, 45, "func newToolTracker() *toolTracker {"),
  ] }],
};

const anchor = (start: number, end: number, excerpt = FILE.hunks[0]!.lines.filter((x) => x.new !== null && x.new >= start && x.new <= end)): FeedbackCodeAnchor => ({
  kind: "code", runId: "run_fix", repo: "dude", path: FILE.path, side: "new", startLine: start, endLine: end, baseSha: "4be19c2", headSha: null,
  diffChecksum: "c1", excerpt,
});

const thread: FeedbackThreadEntry[] = [
  { id: "h1", kind: "reply", who: "The fixer", face: <AgentAvatar role="implementer" size="sm" />, commit: null, at: ago(9),
    body: "I kept the stop: the task asks for **at most 2 minutes**, and a pause that never stops would hold the machine." },
  { id: "h2", kind: "resolve", who: "Márcio", face: <PersonAvatar person={marcio} size={20} />, commit: null, at: ago(3), body: "" },
];

const item = (over: Partial<FeedbackCardItem>): FeedbackCardItem => ({
  id: "f", status: "open", resolutionPending: false, resolvedCommit: null, anchor: anchor(40, 42), author: ana, at: ago(40),
  body: "A map keyed by tool call id is right, but nothing removes an entry for a call that **never** gets an update.", routed: "to the fixer at work", ...over,
});
const ok = async () => true;

/** A picture as the overlay draws one: the element alone on white, outlined. */
const BUTTON_SHOT = `data:image/svg+xml;utf8,${encodeURIComponent(`<svg xmlns="http://www.w3.org/2000/svg" width="212" height="76" viewBox="0 0 212 76">
<rect width="212" height="76" fill="#fff"/><rect x="8" y="8" width="196" height="60" rx="8" fill="#1f7a5a"/>
<text x="106" y="44" text-anchor="middle" font-family="system-ui,sans-serif" font-size="18" font-weight="600" fill="#fff">Pay now</text>
<rect x="5" y="5" width="202" height="66" rx="10" fill="none" stroke="#7c5cff" stroke-width="3"/></svg>`)}`;

function DiffDemo({ view }: { readonly view: "unified" | "split" }) {
  const [selection, setSelection] = useState<DiffRange | null>({ path: FILE.path, side: "new", startLine: 40, endLine: 41 });
  const [editing, setEditing] = useState(true);
  const [text, setText] = useState("Clear `open` when the session ends.");
  return (
    <div style={{ height: 520, display: "flex" }}>
      <LiveDiff files={[FILE]} base="4be19c2a91" defaultView={view} fileList={false} commenting={{
        canStart: true, maxLines: 200, selection,
        onSelectionChange: (r, done) => { setSelection(r); setEditing(done && r !== null); },
        renderFileHead: () => <FeedbackCard inline placement="outdated" item={item({ id: "o", author: marcio, anchor: anchor(44, 45, [l("+", null, 44, "func newTracker() *toolTracker {")]) })} onReply={ok} onResolve={ok} />,
        renderAfterLine: (_p, side, n) => (
          <>
            {side === "new" && n === 43 ? <FeedbackCard inline item={item({ id: "s", status: "replied", anchor: anchor(43, 43) })} thread={thread.slice(0, 1)} onReply={ok} onReopen={ok} /> : null}
            {editing && selection && selection.side === side && selection.endLine === n ? (
              <CommentEditor where={<>Comment on <b>lines {selection.startLine}–{selection.endLine}</b></>} value={text} onChange={setText} autoFocus={false}
                onSave={() => setEditing(false)} onCancel={() => { setEditing(false); setSelection(null); }} />
            ) : null}
          </>
        ),
        fileMark: () => <><Icon name="comments" size={12} />2</>,
      }} />
    </div>
  );
}

function TrayDemo({ full = false }: { readonly full?: boolean }) {
  const [open, setOpen] = useState(!full);
  const [confirm, setConfirm] = useState(false);
  const drafts = [
    { id: "d1", anchor: anchor(40, 42), body: "This returns early on `in_progress` updates; treat every terminal status as the end." },
    { id: "d2", anchor: { kind: "general" as const }, body: "Before the PR: add a test where the tool call never ends." },
  ];
  return (
    <div style={{ position: "relative", minHeight: 220, display: "flex", flexDirection: "column", justifyContent: "flex-end" }}>
      <DraftsTray drafts={drafts} max={full ? 2 : 50} destination="the fixer at work" open={open} onOpenChange={setOpen}
        onSubmit={() => setConfirm(true)} onJump={() => {}} onEdit={() => {}} onDelete={() => {}} />
      {confirm ? (
        <SubmitFeedbackDialog drafts={drafts} onCancel={() => setConfirm(false)} onConfirm={() => setConfirm(false)}
          destination={{ line: "The fixer at work gets them at its next step.", detail: "Nothing is interrupted: it reads them before its next tool call." }}
          holds={<>Pull request <b>#482</b> can't merge and the task can't be marked done. An answer, or your own Resolve, lets it go.</>} />
      ) : null}
    </div>
  );
}

export function FeedbackSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="feedback" title="People's feedback" intro="Comments on a Run's lines and on the task as a whole: private drafts until submitted as one batch, then items with a status and a thread until they are handled.">
      <Block id="fb-diff" title="Commenting on a diff" note="LiveDiff's commenting props: the gutter + on the line under the pointer (a real button, Tab reaches it; Enter picks its line, Shift+Enter stretches the range), press and drag over numbers to pick lines on one side within one hunk, at most 200. renderAfterLine draws the editor and the items under the range's last line, in that side's column in Split; renderFileHead draws what lost its lines.">
        <Panes mode={mode}><DiffDemo view="unified" /></Panes>
        <div style={{ height: 8 }} />
        <Panes mode={mode}><DiffDemo view="split" /></Panes>
      </Block>
      <Block id="fb-cards" title="FeedbackCard" note="Who, when, where it went, its status (tone, glyph and words), what it is on, its words and thread, and the actions its status allows: Reply and Resolve while it needs handling; Reopen once answered or resolved; Confirm resolved while its commit is unpublished. A draft carries the dashed bar of something not sent.">
        <Panes mode={mode}>
          <Col>
            <Label>Draft</Label>
            <FeedbackCard item={item({ status: "draft", routed: undefined, at: ago(2) })} onSaveDraft={ok} onDelete={ok} />
            <Label>With an agent</Label>
            <FeedbackCard item={item({ status: "delivered", withWhom: "Fix" })} onReply={ok} onResolve={ok} onJump={() => {}} />
            <Label>Answered, then resolved by a person</Label>
            <FeedbackCard item={item({ status: "resolved", anchor: { kind: "general" } })} thread={thread} onReply={ok} onReopen={ok} />
            <Label>Resolved in a commit not yet published</Label>
            <FeedbackCard item={item({ status: "resolved", resolutionPending: true, resolvedCommit: "9c1e4a7d0e", anchor: { kind: "general" } })}
              thread={[{ id: "h3", kind: "resolve", who: "The fixer", face: <AgentAvatar role="implementer" size="sm" />, commit: "9c1e4a7d0e", at: ago(4), body: "`pauseWords` names the person." }]}
              onReply={ok} onResolve={ok} onReopen={ok} />
            <Label>On a preview's page</Label>
            <FeedbackCard item={item({ status: "replied", author: marcio, anchor: { kind: "preview", url: "https://wi-2408.preview.acme.dev/runs/run_88", viewport: { width: 1440, height: 900 },
              selector: "header > div.actions > button[data-testid=pause]", text: "Pause", component: { name: "SessionHeader", file: "src/components/ChatTranscript.tsx", line: 318 } } })}
              onReply={ok} onReopen={ok} />
            <Label>A draft on a preview's page, with its picture: Show on preview (onJump); Page can use onOpenPage(url), returning true when handled</Label>
            <FeedbackCard data-testid="gallery-fb-preview-draft" item={item({ status: "draft", routed: undefined, author: marcio, at: ago(1), body: "Green, and on the same line as the total.",
              anchor: { kind: "preview", url: "https://web-wi-2408.preview.acme.dev/cart", viewport: { width: 1280, height: 720 }, selector: "form#checkout > button.pay",
                rect: { x: 152, y: 431, width: 196, height: 60 }, text: "Pay now", component: { name: "PayButton", file: "src/cart/PayButton.tsx", line: 14 } } })}
              imageUrl={BUTTON_SHOT} onSaveDraft={ok} onDelete={ok} onJump={() => {}} />
            <Label>An earlier attempt's: to read</Label>
            <FeedbackCard item={item({ status: "open" })} readOnly />
          </Col>
        </Panes>
      </Block>
      <Block id="fb-editor" title="CommentEditor" note="⌘/Ctrl+Enter saves, Escape cancels; a private draft until submitted. At the drafts' limit the field is off and says why.">
        <Panes mode={mode}>
          <Col>
            <CommentEditor where={<><Icon name="message" size={14} /> <b>General</b></>} value="" onChange={() => {}} onSave={() => {}} onCancel={() => {}} autoFocus={false} />
            <CommentEditor where="Comment on line 41" value="" onChange={() => {}} onSave={() => {}} onCancel={() => {}} autoFocus={false}
              full="You have 50 drafts on this task, the most kept. Submit or delete some to write more." />
          </Col>
        </Panes>
      </Block>
      <Block id="fb-tray" title="DraftsTray and SubmitFeedbackDialog" note="At the foot of a task's pages while you have drafts there: how many, that only you see them, where a submit goes now, the list (jump, edit, delete), and Submit N…, which asks first: what goes, where, and what it holds.">
        <Panes mode={mode}><TrayDemo /></Panes>
        <div style={{ height: 8 }} />
        <Panes mode={mode}><TrayDemo full /></Panes>
      </Block>
    </Section>
  );
}

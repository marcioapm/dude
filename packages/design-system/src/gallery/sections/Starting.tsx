import { useEffect, useState } from "react";
import { MOVE_STAGES, RESUME_STAGES, type StartStage } from "@dude/domain";
import { Block, Label, Panes, Section, type PaneMode } from "../Frame.tsx";
import { AgentAvatar } from "../../components/AgentAvatar.tsx";
import { StartChip, StartProgress, startSteps, type StartKind } from "../../components/StartProgress.tsx";
import { Button } from "../../primitives/Button.tsx";

/** Gallery times are offsets from when the page opened, so each clock ticks from a believable value. */
const OPENED = Date.now();
const ago = (s: number) => OPENED - s * 1000;

/** The six fresh stages, one after another, as lux announces them. */
const PLAY: ReadonlyArray<{ atMs: number; stage: StartStage }> = [
  { atMs: 0, stage: "waiting" }, { atMs: 3_000, stage: "image" }, { atMs: 7_000, stage: "volumes" },
  { atMs: 8_000, stage: "repositories" }, { atMs: 11_000, stage: "container" }, { atMs: 12_500, stage: "running" },
];

function Playing() {
  const [from, setFrom] = useState(() => Date.now());
  const [now, setNow] = useState(from);
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 250);
    return () => clearInterval(id);
  }, []);
  const at = PLAY.filter((p) => p.atMs <= now - from).length - 1;
  const done = PLAY.slice(0, at).map((p, i) => ({ stage: p.stage, tookMs: PLAY[i + 1]!.atMs - p.atMs }));
  const steps = startSteps({ stage: PLAY[at]!.stage, since: from + PLAY[at]!.atMs, done });
  return (
    <div>
      <StartProgress steps={steps} startedAt={from} avatar={<AgentAvatar role="brainstorm" size="chat" />} defaultOpen data-testid="start-playing" />
      <Button size="sm" variant="quiet" leadingIcon="retry" onClick={() => {
        const t = Date.now();
        setFrom(t);
        setNow(t);
      }}>Play again</Button>
    </div>
  );
}

function Example({ kind, verb, stage, since, startedAt, done, stages, pool, slow, failed, open, role = "brainstorm", id }: {
  kind?: StartKind; verb?: string; stage: StartStage | null; since: number; startedAt: number;
  done?: ReadonlyArray<{ stage: StartStage; tookMs: number }>; stages?: readonly StartStage[]; pool?: string;
  slow?: { note: string; reason?: string }; failed?: string | null; open?: boolean;
  role?: "brainstorm" | "implementer" | "conductor"; id: string;
}) {
  const steps = startSteps({ stage, since, done, failed: failed !== undefined, stages, pool });
  return (
    <StartProgress kind={kind} verb={verb} steps={steps} startedAt={startedAt} avatar={<AgentAvatar role={role} size="chat" />}
      slowNote={slow?.note} slowReason={slow?.reason} defaultOpen={open} data-testid={id}
      failure={failed !== undefined ? { reason: failed, onRetry: () => undefined } : undefined} />
  );
}

const RESUME_DONE = [{ stage: "waiting" as const, tookMs: 9_400 }, { stage: "image" as const, tookMs: 600 },
  { stage: "volumes" as const, tookMs: 8_000 }, { stage: "repositories" as const, tookMs: 900 }];

export function StartingSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="starting" title="Starting"
      intro="An agent that is starting says where it is, where its first turn will be. One map of words for lux's stages, shared with branch previews; the caller gives the stage list, so stages that do not occur are not drawn, and the verb, so a resume or a move reads as one.">
      <Block id="st-play" title="StartProgress: a fresh start, playing"
        note="The six stages of a fresh start, one after another, opened. The current stage breathes (frozen under reduced motion); done stages keep their times; the total shows once a stage has passed. A polite live region says each stage once; the clocks are timers outside it.">
        <Panes mode={mode}>{() => <Playing />}</Panes>
      </Block>
      <Block id="st-states" title="StartProgress: resumed, slow, older lux"
        note={`A resume shows every stage, the image and repositories included, as lux announces them on each resume. Past the app's threshold, one calm line in secondary ink, lux's reason in muted mono; never amber. An older lux names no stage: "Starting" and the total clock, nothing to open.`}>
        <Panes mode={mode}>
          <Label>resumed, open</Label>
          <Example id="start-resume" kind="resume" stage="container" since={ago(4)} startedAt={ago(22)} done={RESUME_DONE} stages={RESUME_STAGES} open />
          <Label>slow waiting</Label>
          <Example id="start-slow" stage="waiting" since={ago(100)} startedAt={ago(100)}
            slow={{ note: "No machine free yet: dude is waiting for one. Your message is kept; nothing is spent meanwhile.",
              reason: "waiting for capacity: 2 hosts in its pool lack cpus (requested 4)" }} />
          <Label>older lux: no stage</Label>
          <Example id="start-legacy" stage={null} since={ago(12)} startedAt={ago(12)} />
        </Panes>
      </Block>
      <Block id="st-failed" title="StartProgress: failed"
        note="The transcript's danger Callout, naming the stage and lux's reason, with Try again; the stages folded under it, the failed one crossed.">
        <Panes mode={mode}>
          <Example id="start-failure" stage="image" since={ago(47)} startedAt={ago(53)} done={[{ stage: "waiting", tookMs: 5_800 }]}
            failed="pull failed: manifest unknown" role="implementer" open />
        </Panes>
      </Block>
      <Block id="st-move" title="StartProgress: a move"
        note={`A Run moved to a larger machine: verb "Moving to Large", its stages led by park (stopped, its work kept), then a resume's; given its pool, waiting names it ("Waiting for a machine in highmem"). A drain or preempt reads "Moving to another machine" with the same stages.`}>
        <Panes mode={mode}>
          <Example id="start-move" kind="move" verb="Moving to Large" stage="waiting" since={ago(6)} startedAt={ago(15)}
            done={[{ stage: "park", tookMs: 9_000 }]} stages={MOVE_STAGES} pool="highmem" role="implementer" open />
        </Panes>
      </Block>
      <Block id="st-chip" title="StartChip"
        note="The same state in a header: a session's meta with the mark, a Run's statusNote bare beside its Starting status; failed, the danger mark.">
        <Panes mode={mode} surface>
          <Label>session meta</Label>
          <StartChip kind="resume" step={startSteps({ stage: "volumes", since: ago(12), stages: RESUME_STAGES }).find((s) => s.state === "current")} data-testid="chip-session" />
          <Label>a Run's statusNote, bare</Label>
          <StartChip bare step={startSteps({ stage: "container", since: ago(3) }).find((s) => s.state === "current")} data-testid="chip-bare" />
          <Label>older lux</Label>
          <StartChip startedAt={ago(8)} data-testid="chip-legacy" />
          <Label>failed</Label>
          <StartChip step={startSteps({ stage: "image", since: ago(40), failed: true }).find((s) => s.state === "failed")} data-testid="chip-failed" />
          <Label>a move</Label>
          <StartChip kind="move" verb="Moving to Large" step={startSteps({ stage: "park", since: ago(5), stages: MOVE_STAGES }).find((s) => s.state === "current")} data-testid="chip-move" />
        </Panes>
      </Block>
    </Section>
  );
}

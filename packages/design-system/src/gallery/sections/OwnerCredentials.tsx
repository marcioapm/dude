import { Block, Panes, Section, type PaneMode } from "../Frame.tsx";
import { CredentialCard, CredentialWarning, credentialWords } from "../../components/CredentialCard.tsx";
import { StatusMark } from "../../components/StatusMark.tsx";

const BRANCH = "dude/abs-38-1";
const told = new Date(Date.now() - 42 * 60_000).toISOString();
const noop = () => undefined;

/**
 * A pull request that waits on its owner's GitHub connection (#513): the
 * header chip, the card in Chat and Overview in its three readers' views,
 * and the warning before work starts.
 */
export function OwnerCredentialsSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="owner-credentials" title="Owner credentials"
      intro="dude opens a task's pull request as its owner. When the owner has not connected GitHub, the pull request waits on them: a person item, theirs to act on, and a plain fact for everyone else. It never blocks anyone who has connected.">
      <Block id="oc-chip" title="Header chip" note="The attention StatusMark with the wait's own label: Needs you for the owner, Needs <first name> for everyone else. Glyph, word and tone together.">
        <Panes mode={mode}>
          <div style={{ display: "flex", gap: 12, flexWrap: "wrap" }} data-shot="oc-chip">
            <StatusMark status="awaiting_input" label={credentialWords("Christophe Moreau", true).label} size="sm" />
            <StatusMark status="awaiting_input" label={credentialWords("Christophe Moreau", false).label} size="sm" />
            <StatusMark status="awaiting_input" label={credentialWords("Christophe Moreau", true, true).label} size="sm" />
          </div>
        </Panes>
      </Block>
      <Block id="oc-card-owner" title="CredentialCard · the owner's" note="The owner's turn, where the step parked: what is pushed, a primary Connect GitHub that comes back to this task, and a quiet Why as me? that opens one sentence. Reconnect when their connection stopped working.">
        <Panes mode={mode}>
          <div data-shot="oc-card-owner" style={{ display: "grid", gap: 12 }}>
            <CredentialCard person="Christophe Moreau" yours branch={BRANCH} askedAt={told} onConnect={noop} />
            <CredentialCard person="Christophe Moreau" yours reconnect branch={BRANCH} askedAt={told} onConnect={noop} />
          </div>
        </Panes>
      </Block>
      <Block id="oc-card-others" title="CredentialCard · everyone else's" note="Who it waits on and when they were told. Take over and open it as me shows only to a reader who has connected GitHub; nobody else is offered a way that would wait again.">
        <Panes mode={mode}>
          <div data-shot="oc-card-others" style={{ display: "grid", gap: 12 }}>
            <CredentialCard person="Christophe Moreau" yours={false} branch={BRANCH} askedAt={told} onTakeOver={noop} />
            <CredentialCard person="Christophe Moreau" yours={false} branch={BRANCH} askedAt={told} />
          </div>
        </Panes>
      </Block>
      <Block id="oc-warning" title="CredentialWarning · filing and starting" note="An attention Callout under the owner field, wherever a person files, starts or reads a task whose owner has not connected. The owner gets Connect GitHub; anyone else is told to pick another owner.">
        <Panes mode={mode}>
          <div data-shot="oc-warning" style={{ display: "grid", gap: 12 }}>
            <CredentialWarning person="Christophe Moreau" yours onConnect={noop} />
            <CredentialWarning person="Christophe Moreau" yours={false} />
          </div>
        </Panes>
      </Block>
    </Section>
  );
}

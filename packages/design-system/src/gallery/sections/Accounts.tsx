import { useState } from "react";
import { Block, Caption, Label, Panes, Row, Section, type PaneMode } from "../Frame.tsx";
import { OrgFace } from "../../components/OrgFace.tsx";
import { OrgSwitcher, type OrgInvitation, type OrgMembership } from "../../components/OrgSwitcher.tsx";
import { EmailChipsInput } from "../../components/EmailChipsInput.tsx";
import { InvitationLinkList, type InvitationLink } from "../../components/InvitationLinkList.tsx";
import { KeyShownOnce } from "../../components/KeyShownOnce.tsx";
import { Button } from "../../primitives/Button.tsx";
import { Card, CardBody, CardFooter } from "../../primitives/Card.tsx";
import { KeyValueList } from "../../primitives/Layout.tsx";
import { FrontDoor, FrontDoorLink, FrontDoorMessage, HowList } from "../../components/FrontDoor.tsx";
import { emailChipViews, parseEmailEntries, type EmailEntry } from "../../util/emails.ts";
import { people } from "../navFixtures.ts";

const ACME = { id: "org_acme", name: "Acme", slug: "acme" };
const NORTHWIND = { id: "org_northwind", name: "Northwind", slug: "northwind" };
const GLOBEX = { id: "org_globex", name: "Globex", slug: "globex" };
const YOU = people["marcio"]!;
const MEMBERSHIPS: OrgMembership[] = [{ org: ACME, role: "admin", waiting: 2 }, { org: NORTHWIND, role: "member", waiting: 1 }];
const INVITES: OrgInvitation[] = [{ code: "Gx4tR8mQ2vLp9sKd3wNb7a", org: GLOBEX, inviter: "Lin Zhao", expires: "in 9 days" }];

/** Every chip state at once, as a pasted list would give them. */
const PASTE = `Priya Raman <priya@acme.dev>
Noah Berg\tnoah.berg@acme.dev
"Jules Moreau" <jules@acme.dev>
lin@acme.dev
tomás@
kai.nakamura@acme.dev, priya@acme.dev`;

function classify(email: string) {
  if (email === "lin@acme.dev") return { kind: "member" as const, note: "already in Acme" };
  if (email === "jules@acme.dev") return { kind: "invited" as const, note: "already invited today", action: { label: "Copy their link", onClick: () => undefined } };
  if (email === "kai.nakamura@acme.dev") return { kind: "renew" as const, note: "invitation expired 2 days ago: Invite renews it" };
  return null;
}

function Chips({ start }: { readonly start: string }) {
  const [entries, setEntries] = useState<EmailEntry[]>(() => parseEmailEntries(start));
  return <EmailChipsInput entries={entries} onChange={setEntries} views={emailChipViews(entries, classify)} summaryWords={{ member: "already in Acme" }}
    placeholder="ana@acme.dev, Tom Okafor <tom@acme.dev>"
    hint="Type or paste. Enter, comma or space adds one; pasting “Name <email>” or a column from a spreadsheet keeps the names." />;
}

const LINKS: InvitationLink[] = [
  { email: "priya@acme.dev", name: "Priya Raman", role: "Member", url: "https://dude.acme.dev/#/invite/Rq6tY1uI4oP7aS0dF3gH8j" },
  { email: "noah.berg@acme.dev", name: "Noah Berg", role: "Member", url: "https://dude.acme.dev/#/invite/Bn2mV5cX8zL1kJ4hG7fD0s" },
  { email: "kai.nakamura@acme.dev", role: "Member", url: "https://dude.acme.dev/#/invite/Kn2rZ6tB9yH3mQ7cX4vA8d", renewed: true },
];

/** The switcher with its menu drawn open in place, in its pane's theme. */
function OpenSwitcher({ variant }: { readonly variant: "band" | "rail" }) {
  const [host, setHost] = useState<HTMLElement | null>(null);
  return (
    <div ref={setHost} style={{ position: "relative", minHeight: 420, display: "flex", alignItems: "flex-end", width: variant === "band" ? 284 : 56 }}>
      {host ? <OrgSwitcher you={YOU} current={ACME} memberships={MEMBERSHIPS} invitations={INVITES} superAdmin variant={variant}
        onSwitch={() => undefined} onJoin={() => undefined} onSettings={() => undefined} onAdmin={() => undefined} onSignOut={() => undefined}
        signOutNote="Of Cloudflare Access, on this browser" open forceMount container={host} /> : null}
    </div>
  );
}

function KeyDemo() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button onClick={() => setOpen(true)}>Show a new key</Button>
      {open ? <KeyShownOnce title="Key “Laptop” for Acme" secret="dude_sk_N7wq2c4ZkLr0sYb1pFhT8eXuAm5dJgVo" onClose={() => setOpen(false)}
        usage={"dude login https://dude.acme.dev\n# paste the key; the CLI keeps it as \"acme\"\ndude whoami"}
        usageNote="With keys for several organisations, the CLI uses the last one you logged in with; --org acme picks one." /> : null}
    </>
  );
}

export function AccountsSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="accounts" title="Accounts"
      intro="Being in several organisations: the organisation's face, the switcher in the profile band, inviting by email with a link per person, and a key shown once. docs/design/accounts.md has the flows.">
      <Block id="ac-face" title="OrgFace" note="An organisation's face. Organisations have no image or colour, so it is a project's rounded square with the organisation's initials on the identity colour its id hashes to: the same organisation is the same colour on every screen.">
        <Panes mode={mode}>
          <Row>
            {[14, 16, 24, 32, 48].map((s) => <OrgFace key={s} org={NORTHWIND} size={s} />)}
            <OrgFace org={ACME} size={32} />
            <OrgFace org={GLOBEX} size={32} />
          </Row>
        </Panes>
      </Block>
      <Block id="ac-switcher" title="OrgSwitcher"
        note="You at the foot of the sidebar, and the one menu for you. band: SidebarProfile's anatomy (your face and name, the organisation's face, name and your role under it, a dot when an invitation waits, a sort chevron). rail: your face with the organisation's in its corner. The menu is RowMenu: your organisations as radio items with their face, your role and what waits on you there (the needs-you pill at the end), the current one ticked; Join… for each invitation; Your settings; All organisations for a super-admin; Sign out. Enter or Space opens, arrows move, Escape closes.">
        <Panes mode={mode}>
          <Label>closed: in Acme, an invitation waiting; without one</Label>
          <Row>
            <div style={{ width: 284 }}>
              <OrgSwitcher you={YOU} current={ACME} memberships={MEMBERSHIPS} invitations={INVITES} onSwitch={() => undefined} onSettings={() => undefined} onSignOut={() => undefined} />
            </div>
            <div style={{ width: 284 }}>
              <OrgSwitcher you={YOU} current={NORTHWIND} memberships={MEMBERSHIPS} onSwitch={() => undefined} onSettings={() => undefined} onSignOut={() => undefined} />
            </div>
            <OrgSwitcher variant="rail" you={YOU} current={ACME} memberships={MEMBERSHIPS} invitations={INVITES} onSwitch={() => undefined} onSettings={() => undefined} onSignOut={() => undefined} />
          </Row>
          <Label>open, from the band and from the rail</Label>
          <Row top>
            <OpenSwitcher variant="band" />
            <OpenSwitcher variant="rail" />
          </Row>
        </Panes>
      </Block>
      <Block id="ac-chips" title="EmailChipsInput"
        note="Emails as chips, for an invite. Enter, a comma, a space or leaving the field make chips of what is typed; a paste is split by line, comma and semicolon, keeping Name <email> and a spreadsheet row's name. Each chip says what will happen to it by glyph and words: ✓ new; invitation expired, renewed (same link); not an email; already a member (struck through); already invited, with Copy their link; twice in the list. Backspace in the empty field takes the last back; each has its ✕. The line under it counts them.">
        <Panes mode={mode} surface>
          <Label>every state</Label>
          <div style={{ maxWidth: 680 }}><Chips start={PASTE} /></div>
          <Label>empty</Label>
          <div style={{ maxWidth: 680 }}><Chips start="" /></div>
        </Panes>
      </Block>
      <Block id="ac-links" title="InvitationLinkList"
        note="Each invitee and their link, to send themselves: name over address, role and “renewed, same link”; the link in mono; Copy. Under 640px the link drops under the name. Copy all (the dialog's) puts “Name, email, link” on a line each.">
        <Panes mode={mode} surface>
          <div style={{ maxWidth: 760 }}><InvitationLinkList links={LINKS} onCopy={() => undefined} /></div>
          <Caption>one, as Create organisation shows its first admin's</Caption>
          <div style={{ maxWidth: 760 }}><InvitationLinkList links={[{ email: "bill@initech.com", role: "Admin", url: "https://dude.acme.dev/#/invite/Rq6tY1uI4oP7aS0dF3gH8j" }]} onCopy={() => undefined} /></div>
        </Panes>
      </Block>
      <Block id="ac-key" title="KeyShownOnce"
        note="A secret, shown this once: the key in a read-only mono field that selects itself, Copy (Copied), and a usage slot for the CLI lines in a sunken well. Done closes it for good.">
        <Panes mode={mode}>
          <KeyDemo />
        </Panes>
      </Block>
      <Block id="ac-door" title="FrontDoor / FrontDoorMessage / HowList"
        note="A page that is one centred column: the app's face, a heading and its line, cards, and fine print with an inline FrontDoorLink at the foot. outside (No organisation, You're invited, an invitation's page) sits on the canvas with no sidebar; inside (Welcome to Northwind) on the surface. FrontDoorMessage is one message in a card: a face or a glyph on a sunken disc, an h1, a line, and its facts. HowList is how something works in a few lines.">
        <Panes mode={mode}>
          <div style={{ display: "flex", height: 520 }}>
            <FrontDoor mark={<OrgFace org={NORTHWIND} size={48} />} footer={<p>Signed in as <b>chloe@hey.com</b> with Cloudflare Access. <FrontDoorLink>Sign out</FrontDoorLink></p>}>
              <Card>
                <CardBody>
                  <FrontDoorMessage lead={<OrgFace org={NORTHWIND} size={48} />} title="Join Northwind as member" line="Invited by Gus Lindqvist on 11 October."
                    facts={<KeyValueList items={[{ label: "For", value: "chloe@hey.com" }, { label: "Expires", value: "in 12 days" }]} />} />
                </CardBody>
                <CardFooter><Button variant="quiet">Not now</Button><Button variant="primary">Join Northwind</Button></CardFooter>
              </Card>
            </FrontDoor>
          </div>
          <Label>a message with a glyph; HowList</Label>
          <Card><CardBody><FrontDoorMessage icon="link" title="This invitation can't be used" line="Ask whoever sent it for a new link." /></CardBody></Card>
          <HowList label="How dude works" items={[
            { icon: "conductor", title: "Talk it through", body: "On a task: you and the conductor work it out in Chat." },
            { icon: "git-pr", title: "Deliver", body: "dude implements, reviews and opens a pull request, and stops there." },
          ]} />
        </Panes>
      </Block>
    </Section>
  );
}

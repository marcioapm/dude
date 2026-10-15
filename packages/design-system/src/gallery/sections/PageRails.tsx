import { useState } from "react";
import { Block, Panes, Section, type PaneMode } from "../Frame.tsx";
import { FacetList, PageRail, PageRailBlock, PageWithRail } from "../../components/PageWithRail.tsx";
import { Button } from "../../primitives/Button.tsx";

export function RailDemo({ empty = false }: { readonly empty?: boolean }) {
  const [value, setValue] = useState<string[]>(["open"]);
  const [actions, setActions] = useState(0);
  return <PageWithRail data-testid="page-rail-demo" rail={<PageRail aria-label="List controls">
    <PageRailBlock label="Status">
      <FacetList label="Status filters" options={empty ? [] : [
        { value: "open", label: "Open", count: 4 },
        { value: "settled", label: "Settled", count: 0 },
        { value: "unavailable", label: "Unavailable", count: 2, disabled: true, description: "Read-only scope" },
      ]} value={value} onValueChange={setValue} empty="No statuses available" />
    </PageRailBlock>
    <PageRailBlock label="Actions"><Button variant="secondary" onClick={() => setActions((n) => n + 1)}>Apply action</Button></PageRailBlock>
  </PageRail>}>
    <div style={{ padding: "var(--ds-space-16)", overflowWrap: "anywhere" }}>
      <h3>Main content fills the remaining column</h3>
      <p>Selected: {value.join(", ") || "all"}</p>
      <p role="status">Actions performed: {actions}</p>
      <p>Tab to the facets, use Space to change selection, then reach the action. Narrow controls stay above this content without remounting.</p>
    </div>
  </PageWithRail>;
}

export function PageRailSection({ mode }: { readonly mode: PaneMode }) {
  return <Section id="page-rails" title="Page rails" intro="One desktop rail; compact controls above the content when the container cannot leave a usable main column.">
    <Block id="rail-populated" title="Populated · selected, zero count, disabled, keyboard focus" note="Use a single theme pane to see the desktop split; both themes show compact controls when space is limited.">
      <Panes mode={mode}>{() => <RailDemo />}</Panes>
    </Block>
    <Block id="rail-empty" title="Empty facets"><Panes mode={mode}>{() => <RailDemo empty />}</Panes></Block>
    <Block id="rail-narrow" title="Narrow · access preserved"><Panes mode={mode}>{() => <div style={{ maxWidth: "var(--ds-size-page-rail)" }}><RailDemo /></div>}</Panes></Block>
  </Section>;
}

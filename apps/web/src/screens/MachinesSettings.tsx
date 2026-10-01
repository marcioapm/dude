/**
 * Organisation settings → Machines: the sizes an agent's machine can be
 * (everyone reads; admins add, edit, make one the default and remove one),
 * the pools lux has for them, and why a run gets a little less memory than
 * it asks for.
 */

import { useEffect, useMemo, useState } from "react";
import {
  AgentAvatar,
  EntityLine,
  FitBar,
  ProjectAvatar,
  ProportionBar,
  ReservedSwatch,
  SettingsExplainer,
  SettingsHeader,
  SettingsMeta,
  SettingsNote,
  SettingsSection,
  UsedBy,
} from "@dude/design-system/components";
import { formatTimestamp } from "@dude/design-system";
import {
  Badge,
  Button,
  Callout,
  Checkbox,
  Dialog,
  FormRow,
  FormStack,
  Input,
  NumberInput,
  RowMenu,
  Select,
  Spinner,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
} from "@dude/design-system/primitives";
import {
  fitProblem,
  gib,
  machineFit,
  machineSpec,
  type MachinePools,
  type MachineSize,
  type MachineSizeUse,
  type MachineSizeWithUse,
} from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";
import {
  asInput,
  draftOf,
  draftProblems,
  fitWords,
  hostSpec,
  knownPools,
  poolKnownFrom,
  poolLabel,
  poolMachines,
  poolOptionLabel,
  STEP_HINT,
  useLine,
  usedByWords,
  type SizeDraft,
} from "../machines.ts";

export type Sizes = { sizes: MachineSizeWithUse[]; canEdit: boolean };

/**
 * The organisation's sizes, loaded once per client by the settings screen
 * and passed to every page under it; `setSizes` takes what a change on the
 * Machines page answered, so the menu's count and the Machine fields follow.
 * A failed load leaves no sizes, so Machine fields still offer the inherited
 * one; `problem` is for the Machines page's banner.
 */
export function useMachineSizes(client: ApiClient) {
  const [sizes, setSizes] = useState<Sizes | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  useEffect(() => {
    client.machineSizes().then(setSizes, (err: unknown) => {
      setProblem(errorText(err));
      setSizes({ sizes: [], canEdit: false });
    });
  }, [client]);
  return { sizes, setSizes, problem };
}

/** lux's pools, read when the Machines page opens. */
function usePools(client: ApiClient) {
  const [pools, setPools] = useState<MachinePools | null>(null);
  useEffect(() => {
    client.machinePools().then(setPools, () => setPools({ pools: [], readAt: new Date().toISOString(), problem: "the pools could not be read" }));
  }, [client]);
  return pools;
}

const faceOf = (use: MachineSizeUse) =>
  use.kind === "organization"
    ? <AgentAvatar key={`r-${use.role}`} role={use.role === "fixer" ? "implementer" : ((use.role ?? "implementer") as "implementer")} size="xs" />
    : <ProjectAvatar key={`p-${use.project!.id}`} project={use.project!} size={16} />;

/** One face per role and per project, in the order they are named. */
function faces(uses: readonly MachineSizeUse[]) {
  const seen = new Set<string>();
  return uses.filter((u) => {
    const key = u.kind === "organization" ? `r-${u.role}` : `p-${u.project!.id}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  }).slice(0, 4).map(faceOf);
}

export function MachinesPage({ client, orgName, sizes, problem, setSizes }: {
  client: ApiClient;
  orgName: string;
  /** The screen's sizes (useMachineSizes); null while loading. */
  sizes: Sizes | null;
  problem: string | null;
  setSizes: (s: Sizes) => void;
}) {
  const pools = usePools(client);
  const [editing, setEditing] = useState<MachineSizeWithUse | "new" | null>(null);
  const [removing, setRemoving] = useState<MachineSizeWithUse | null>(null);
  const { save, problem: saveProblem } = useSave();

  if (!sizes || problem) return <div className="centered">{problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />}</div>;
  const canEdit = sizes.canEdit;
  const poolList = pools?.pools ?? [];
  const known = knownPools(pools);
  const defaultSize = sizes.sizes.find((s) => s.isDefault);

  return (
    <div className="settingsPage" data-testid="machines-page">
      <SettingsHeader title="Machines"
        description="The sizes an agent’s machine can be. Each agent runs on the size its settings name — in Agents, here or in a project — and on the default when it names none." />
      <SettingsNote icon="info">
        Only admins change these. A change applies to sessions that start after it; sessions already running keep the machine they started on.
      </SettingsNote>
      {saveProblem ? <Callout tone="danger">{saveProblem}</Callout> : null}

      <SettingsSection title="Sizes" data-testid="machine-sizes"
        actions={canEdit ? <Button size="sm" variant="secondary" leadingIcon="plus" onClick={() => setEditing("new")} data-testid="add-machine-size">Add size</Button> : undefined}>
        <Table>
          <THead>
            <Tr>
              <Th>Name</Th>
              <Th align="right">CPUs</Th>
              <Th align="right">Memory</Th>
              <Th align="right">Disk</Th>
              <Th hideWhenNarrow>Pool</Th>
              <Th hideWhenNarrow>Fits</Th>
              <Th hideWhenNarrow>Used by</Th>
              {canEdit ? <Th align="right" width="48px"><span className="ds-sr-only">Actions</span></Th> : null}
            </Tr>
          </THead>
          <TBody>
            {sizes.sizes.map((s) => {
              const sized = machineFit(s, known);
              const fit = fitWords(sized);
              return (
                <Tr key={s.id} data-size={s.name}>
                  <Td fit>
                    <EntityLine size="sm" name={s.name} />{" "}
                    {s.isDefault ? <Badge tone="info" size="sm" icon="system">Default</Badge> : null}
                  </Td>
                  <Td align="right" fit className="ds-tnum">{s.cpus} CPUs</Td>
                  <Td align="right" fit className="ds-tnum">{gib(s.memoryMiB * 1024 * 1024)} GiB</Td>
                  <Td align="right" fit className="ds-tnum">{s.diskGiB} GiB</Td>
                  {sized.kind === "gone" ? (
                    <Td fit hideWhenNarrow data-pool-gone><Badge tone="danger" size="sm" icon="warning">Pool gone from lux</Badge></Td>
                  ) : (
                    <Td fit hideWhenNarrow mono={s.poolId !== null} muted={s.poolId === null}>{poolLabel(s, known)}</Td>
                  )}
                  <Td fit hideWhenNarrow><FitBar share={fit.share}>{fit.text}</FitBar></Td>
                  <Td wrap hideWhenNarrow><UsedBy faces={faces(s.usedBy)}>{usedByWords(s, s.usedBy)}</UsedBy></Td>
                  {canEdit ? (
                    <Td align="right">
                      <RowMenu size="sm" label={`Actions for ${s.name}`} items={[
                        { id: "edit", label: "Edit", icon: "edit", onSelect: () => setEditing(s) },
                        s.isDefault
                          ? { id: "default", label: "Default already", icon: "system", disabled: true }
                          : { id: "default", label: "Make default", icon: "system",
                              onSelect: () => void save(async () => setSizes(await client.makeDefaultMachineSize(s.id)), undefined, `${s.name} is the default`) },
                        { kind: "separator" },
                        { id: "remove", label: "Remove…", tone: "danger", icon: "cross", onSelect: () => setRemoving(s),
                          disabled: s.isDefault, disabledReason: s.isDefault ? "Make another the default first" : undefined },
                      ]} />
                    </Td>
                  ) : null}
                </Tr>
              );
            })}
          </TBody>
        </Table>
      </SettingsSection>

      <SettingsSection title="Pools, from lux" data-testid="machine-pools"
        actions={pools ? <SettingsMeta>Read with {orgName}’s lux key · {formatTimestamp(pools.readAt, "relative")}</SettingsMeta> : undefined}>
        {pools?.problem ? <Callout tone="neutral">No pools to show: {pools.problem}. Sizes still work; whether they fit is unknown.</Callout> : null}
        {poolList.length ? (
          <Table>
            <THead>
              <Tr>
                <Th>Pool</Th>
                <Th hideWhenNarrow>Machines</Th>
                <Th align="right">CPUs</Th>
                <Th align="right">Memory</Th>
                <Th align="right">Disk</Th>
                <Th hideWhenNarrow>Known from</Th>
              </Tr>
            </THead>
            <TBody>
              {poolList.map((p) => (
                <Tr key={p.id} data-pool={p.name}>
                  <Td fit>
                    <span className="ds-mono">{p.name}</span>{" "}
                    {p.isDefault ? <Badge size="sm">{orgName}’s default</Badge> : null}{" "}
                    {p.platform ? <Badge size="sm">Platform</Badge> : null}
                  </Td>
                  <Td fit hideWhenNarrow>{poolMachines(p)}</Td>
                  <Td align="right" fit className="ds-tnum">{p.hostSize ? p.hostSize.cpus : "—"}</Td>
                  <Td align="right" fit className="ds-tnum">{p.hostSize ? `${gib(p.hostSize.memory)} GiB` : "—"}</Td>
                  <Td align="right" fit muted={p.hostSize?.disk === 0} className="ds-tnum">
                    {p.hostSize ? (p.hostSize.disk > 0 ? `${gib(p.hostSize.disk)} GiB` : "not reserved") : "—"}
                  </Td>
                  <Td fit muted hideWhenNarrow>{poolKnownFrom(p)}</Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        ) : null}
      </SettingsSection>

      <SettingsExplainer title="Not all of a machine’s memory can go to runs" data-testid="memory-share">
        <p>
          Sizes are in the machine’s own terms: a 32 GiB machine takes runs asking for 32 GiB in total. Linux and the host keep a
          little of it, so every run on that host gets the same share of what it asked for — no run pays more than another.
        </p>
        <ProportionBar aria-label="A 32 GiB machine: Linux and the host keep 2 GiB; two runs asking for 16 GiB each get 15"
          segments={[
            { id: "host", value: 2, kind: "reserved" },
            { id: "a", value: 15, label: "Run A · asks 16 · gets 15" },
            { id: "b", value: 15, label: "Run B · asks 16 · gets 15" },
          ]}
          legend={<><ReservedSwatch />Linux and the host · 2 GiB</>}
          total="c7a.4xlarge · 32 GiB" />
      </SettingsExplainer>

      {editing ? (
        <SizeDialog client={client} orgName={orgName} existing={editing === "new" ? null : editing} pools={pools}
          defaultName={defaultSize?.name ?? null} onClose={() => setEditing(null)} onSaved={setSizes} />
      ) : null}
      {removing ? (
        <RemoveDialog client={client} orgName={orgName} size={removing} others={sizes.sizes.filter((s) => s.id !== removing.id)}
          onClose={() => setRemoving(null)} onRemoved={setSizes} />
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Adding and editing a size
// ---------------------------------------------------------------------------

const DEFAULT_POOL = "__default__";

function SizeDialog({ client, orgName, existing, pools, defaultName, onClose, onSaved }: {
  client: ApiClient;
  orgName: string;
  existing: MachineSizeWithUse | null;
  pools: MachinePools | null;
  defaultName: string | null;
  onClose: () => void;
  onSaved: (s: Sizes) => void;
}) {
  const [draft, setDraft] = useState<SizeDraft>(() => draftOf(existing));
  const { busy, problem, save } = useSave();
  const set = <K extends keyof SizeDraft>(k: K, v: SizeDraft[K]) => setDraft((d) => ({ ...d, [k]: v }));
  const problems = draftProblems(draft);
  const known = knownPools(pools);
  const input = asInput(draft);
  const fit = Object.keys(problems).some((k) => k !== "name") ? null : machineFit(input, known);
  const tooBig = fit?.kind === "too_big";
  // The draft names a pool lux no longer lists: saving would be refused.
  const poolGone = draft.poolId !== null && known !== null && !known.some((p) => p.id === draft.poolId);
  // Beside the field that does not fit, what one host has of it.
  const over = (what: "cpus" | "memory" | "disk") => {
    if (fit?.kind !== "too_big") return undefined;
    const o = fit.over.find((x) => x.what === what);
    if (!o) return undefined;
    return what === "cpus" ? `Most a ${fit.pool.name} host has: ${o.offers}` : `Most a ${fit.pool.name} host has: ${gib(o.offers)} GiB`;
  };
  const users = existing?.usedBy ?? [];
  const valid = Object.keys(problems).length === 0 && !tooBig && !poolGone;

  return (
    <FormDialog open onOpenChange={(open) => !open && onClose()} size="md"
      title={existing ? `Edit ${existing.name}` : "Add a machine size"}
      description={existing ? `Every agent set to ${existing.name} gets this size from its next session.` : "Agents can be given it in Agents, here or in a project."}
      submitLabel={existing ? "Save" : "Add size"} submitTestId="machine-size-save" canSubmit={!busy && valid} problem={problem}
      footerStart={existing?.updatedBy ? `Last changed by ${existing.updatedBy.name} · ${formatTimestamp(existing.updatedAt, "relative")}` : undefined}
      onSubmit={() => void save(async () => {
        const next = existing ? await client.updateMachineSize(existing.id, input) : await client.addMachineSize(input);
        onSaved(next);
        onClose();
      }, undefined, existing ? `${input.name} saved` : `${input.name} added`)}>
      {existing && users.length ? (
        <Callout tone="info" data-testid="machine-size-users">
          Used by {users.map((u) => useLine(u, orgName).what).join(", ")}. Sessions running on {existing.name} now keep what they started with.
        </Callout>
      ) : null}
      <Input label="Name" autoFocus value={draft.name} maxLength={40} onChange={(e) => set("name", e.target.value)} data-testid="machine-size-name"
        hint={`What people pick from. Unique in ${orgName}.`} error={draft.name.trim() && problems.name ? problems.name : undefined} />
      <FormRow>
        <NumberInput label="CPUs" unit="CPUs" value={draft.cpus} step={0.5} min={0.5} onValueChange={(v) => set("cpus", v)}
          hint={STEP_HINT.cpus} error={problems.cpus ?? over("cpus")} data-testid="machine-size-cpus" />
        <NumberInput label="Memory" unit="GiB" value={draft.memoryGiB} step={0.5} min={0.5} onValueChange={(v) => set("memoryGiB", v)}
          hint={STEP_HINT.memory} error={problems.memory ?? over("memory")} data-testid="machine-size-memory" />
        <NumberInput label="Disk" unit="GiB" value={draft.diskGiB} step={5} min={5} onValueChange={(v) => set("diskGiB", v)}
          hint={STEP_HINT.disk} error={problems.disk ?? over("disk")} data-testid="machine-size-disk" />
      </FormRow>
      <PoolField orgName={orgName} pools={pools} value={draft.poolId} currentName={existing?.poolName ?? null} gone={poolGone}
        onChange={(v) => set("poolId", v)} />
      {poolGone ? (
        <Callout tone="danger" data-testid="machine-pool-gone">
          <b>Its pool is gone from lux.</b> Sessions on {existing?.name ?? "this size"} fail until it runs in another: choose one, or {orgName}’s default pool.
        </Callout>
      ) : null}
      {fit && fit.kind !== "gone" ? <FitCallout fit={fit} memoryGiB={draft.memoryGiB ?? 0} /> : null}
      {existing?.isDefault ? (
        <Checkbox checked disabled label="The default" description="Make another size the default to change this." />
      ) : (
        <Checkbox checked={draft.isDefault} onCheckedChange={(c) => set("isDefault", c === true)} label="Make it the default"
          description={`For every agent that names no size.${defaultName ? ` Now: ${defaultName}.` : ""}`} data-testid="machine-size-default" />
      )}
    </FormDialog>
  );
}

/**
 * The pool picker: its value is lux's id for the pool, its label lux's
 * current name. A pool the size names that lux no longer lists stays the
 * value, labelled as gone, until another is chosen.
 */
function PoolField({ orgName, pools, value, currentName, gone, onChange }: {
  orgName: string;
  pools: MachinePools | null;
  value: string | null;
  /** The name the API last read for `value`, for when lux's list is not to hand. */
  currentName: string | null;
  gone: boolean;
  onChange: (poolId: string | null) => void;
}) {
  const list = pools?.pools ?? [];
  const listed = value === null || list.some((p) => p.id === value);
  const options = [
    // None named is the pool lux calls the default; when lux says which, it is that pool's row
    // (unless the size names that pool by its id).
    ...(list.some((p) => p.isDefault) ? [] : [{ value: DEFAULT_POOL, label: `${orgName}’s default pool` }]),
    ...list.map((p) => ({ value: p.isDefault && p.id !== value ? DEFAULT_POOL : p.id, label: poolOptionLabel(p, orgName) })),
    ...(listed ? [] : [{ value: value!, label: gone ? "Pool gone from lux" : (currentName ?? value!), disabled: gone }]),
  ];
  let hint = `Where lux runs sessions of this size. The list is lux’s: the pools ${orgName}’s key can use.`;
  if (pools?.problem) hint = `lux could not be read (${pools.problem}): a size in a named pool can’t be saved until it can.`;
  else if (gone) hint = "lux no longer has this pool. Choose another.";
  return (
    <Select label="Pool" aria-label="Pool" value={value ?? DEFAULT_POOL} options={options}
      onValueChange={(v) => onChange(v === DEFAULT_POOL ? null : v)} data-testid="machine-size-pool" hint={hint} />
  );
}

/** Whether the size fits one host of its pool, and what a session gets. */
function FitCallout({ fit, memoryGiB }: { fit: Exclude<ReturnType<typeof machineFit>, { kind: "gone" }>; memoryGiB: number }) {
  const hint = (
    <SettingsMeta>
      A session gets a little less than {memoryGiB} GiB: Linux keeps some of every machine, and each run on it gives up the same share.
    </SettingsMeta>
  );
  if (fit.kind === "too_big") {
    return (
      <Callout tone="danger" data-testid="machine-fit" data-fit="too_big">
        <b>No host in ‘{fit.pool.name}’ can hold this.</b> Sessions on it would wait for a machine that never comes: {fitProblem(fit.over)}.
      </Callout>
    );
  }
  if (fit.kind === "unknown") {
    return (
      <>
        <Callout tone="neutral" data-testid="machine-fit" data-fit="unknown">
          {fit.pool ? `lux does not know how big a ‘${fit.pool.name}’ host is yet` : "lux has not said how big its hosts are"}, so whether this fits is unknown. It can be saved; lux will place it or say why.
        </Callout>
        {hint}
      </>
    );
  }
  const host = hostSpec(fit.pool)!;
  const what = fit.pool.instanceType ?? (fit.pool.platform ? "platform" : "host");
  return (
    <>
      <Callout tone="success" data-testid="machine-fit" data-fit="fits">
        Fits ‘{fit.pool.name}’ — {Math.round(fit.share * 100)}% of one {what} ({host}).
        {fit.diskReserved ? null : " Its hosts don’t reserve disk, so lux won’t hold the disk for it."}
      </Callout>
      {hint}
    </>
  );
}

// ---------------------------------------------------------------------------
// Removing a size in use
// ---------------------------------------------------------------------------

const NO_SIZE = "__none__";

function RemoveDialog({ client, orgName, size, others, onClose, onRemoved }: {
  client: ApiClient;
  orgName: string;
  size: MachineSizeWithUse;
  others: MachineSize[];
  onClose: () => void;
  onRemoved: (s: Sizes) => void;
}) {
  const { busy, problem, save } = useSave();
  const inUse = size.usedBy.length > 0;
  const [to, setTo] = useState<string>(() => others.find((s) => !s.isDefault)?.id ?? NO_SIZE);
  const options = useMemo(() => [
    ...others.map((s) => ({ value: s.id, label: s.name, meta: machineSpec(s) })),
    { value: NO_SIZE, label: "No size set", meta: "follows the default" },
  ], [others]);
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()} tone="danger" size="md" title={`Remove ${size.name}?`}
      description={inUse ? "It is in use. Choose the size its agents run on instead; nothing is left pointing at a size that is gone." : "Nothing names it. Sessions running on it now finish on it."}
      footer={
        <>
          <Button variant="quiet" onClick={onClose}>Cancel</Button>
          <Button variant="danger" solid disabled={busy} data-testid="remove-machine-size"
            onClick={() => void save(async () => {
              onRemoved(await client.removeMachineSize(size.id, to === NO_SIZE ? null : to));
              onClose();
            }, undefined, `${size.name} removed`)}>
            {inUse ? "Remove and move them" : "Remove"}
          </Button>
        </>
      }>
      <FormStack>
        {inUse ? (
          <Table density="compact" data-testid="machine-size-uses">
            <TBody>
              {size.usedBy.map((u, i) => {
                const line = useLine(u, orgName);
                return (
                  <Tr key={i}>
                    <Td wrap><UsedBy faces={[faceOf(u)]}>{line.what}</UsedBy></Td>
                    <Td align="right" muted fit>{line.where}</Td>
                  </Tr>
                );
              })}
            </TBody>
          </Table>
        ) : null}
        {inUse ? (
          <Select label="Move them to" aria-label="Move them to" value={to} onValueChange={setTo} options={options} data-testid="machine-size-move"
            hint={`Or to “No size set”, which follows the default. Sessions running on ${size.name} now finish on it; its past sessions keep saying ${size.name}.`} />
        ) : null}
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
      </FormStack>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// The Machine field: an agent role's, or a project's previews'
// ---------------------------------------------------------------------------

/** What names no size here: the organisation's default, or its value under a project. */
export const INHERIT = "__inherit__";

/**
 * The size picker: the first option is what naming none means here —
 * "Default · Standard" on the organisation, "From Acme · Large" on a
 * project, "The implementer's · Large" for a fixer — then every size with
 * its spec as muted meta. The footer says where sizes are managed.
 */
export function MachineSelect({ sizes, value, inherited, inheritLabel, inheritDescription, footer, disabled, onChange, id, testId }: {
  /** The organisation's sizes, each with lux's current name for its pool when it names one. */
  sizes: readonly MachineSizeWithUse[];
  /** The size set at this layer; null: none, so `inherited`. */
  value: string | null;
  /** What naming none resolves to here. */
  inherited: MachineSize | null;
  inheritLabel: string;
  inheritDescription?: string | undefined;
  footer: React.ReactNode;
  disabled?: boolean | undefined;
  onChange: (sizeId: string | null) => void;
  id?: string | undefined;
  testId?: string | undefined;
}) {
  const options = [
    { value: INHERIT, label: inheritLabel, meta: inherited ? `${inherited.name} · ${machineSpec(inherited)}` : undefined, description: inheritDescription },
    ...sizes.map((s) => ({ value: s.id, label: s.name, meta: `${machineSpec(s)}${s.poolName ? ` · ${s.poolName}` : ""}` })),
  ];
  return (
    <Select id={id} aria-label="Machine" value={value !== null && sizes.some((s) => s.id === value) ? value : INHERIT} disabled={disabled}
      options={options} footer={footer} data-testid={testId} onValueChange={(v) => onChange(v === INHERIT ? null : v)} />
  );
}

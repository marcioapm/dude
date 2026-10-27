import { useId, type ReactNode } from "react";
import { CONDITION_OP_LABEL, CONDITION_OPS, describeCondition, REFUSAL_LABEL, type ConditionOp, type RefusalCode } from "@dude/domain";
import { Icon, type IconName } from "../icons/index.tsx";
import { Button, IconButton } from "../primitives/Button.tsx";
import { Input } from "../primitives/Input.tsx";
import { Select } from "../primitives/Select.tsx";
import { cx } from "../util/cx.ts";
import { ChipsInput } from "./Automations.tsx";
import styles from "./Incoming.module.css";

/*
 * Incoming webhooks' pieces: the conditions on a delivery's JSON body, what
 * dude did with one delivery, and the trigger's one-line summary. The
 * operators and their words are the domain's (packages/domain/src/incoming.ts).
 */

export type ConditionOpName = ConditionOp;

export interface TriggerCondition {
  readonly path: string;
  readonly op: ConditionOpName;
  readonly values: readonly string[];
}

/** A condition in words: "status is firing", "commonLabels.severity one of critical, high". */
export function conditionWords(c: TriggerCondition): string {
  return describeCondition({ ...c, values: [...c.values] });
}

/** "Incoming", then its first condition and "+N" for the rest, or "any JSON", in muted mono. */
export function IncomingSummary({ conditions, wrap }: { readonly conditions: readonly TriggerCondition[]; readonly wrap?: boolean | undefined }) {
  const first = conditions[0];
  const rest = first ? `${conditionWords(first)}${conditions.length > 1 ? ` +${conditions.length - 1}` : ""}` : "any JSON";
  return (
    <span className={cx(styles["summary"], wrap && styles["summaryWrap"])} data-trigger="incoming">
      <span className={styles["summaryLead"]}><Icon name="arrow-down-to-line" size={14} className={styles["glyph"]} /><span>Incoming</span></span>
      {wrap ? <wbr /> : null}
      <span className={styles["muted"]}>{"·\u00a0"}<span className="ds-mono">{rest}</span></span>
    </span>
  );
}

export interface ConditionResultMark {
  readonly ok: boolean;
  /** What the body had at the path, as text ("missing" for nothing). */
  readonly actual: string;
}

/**
 * An incoming webhook's conditions on its JSON body, AND'd: each row a mono
 * path, an operator and its value (a chips input for one of, nothing for
 * exists). With `results` each row leads with a ✓ or ✗ named Holds / Does
 * not hold, and a missed row says what the body had. Without `onChange`, or
 * `disabled`, the rows are read-only and have no add or remove.
 */
export function ConditionRows({ value, onChange, disabled, results, paths }: {
  readonly value: readonly TriggerCondition[];
  readonly onChange?: ((next: TriggerCondition[]) => void) | undefined;
  readonly disabled?: boolean | undefined;
  readonly results?: readonly ConditionResultMark[] | undefined;
  /** Offered as the path is typed: the last delivery's keys. */
  readonly paths?: readonly string[] | undefined;
}) {
  const listId = useId();
  const off = disabled || !onChange;
  const set = (i: number, c: TriggerCondition) => onChange?.(value.map((x, j) => (j === i ? c : x)));
  return (
    <div className={styles["conditions"]} data-testid="condition-rows">
      {value.length === 0 ? <p className={styles["muted"]}>No conditions: every JSON delivery starts a run.</p> : (
        <ol className={styles["list"]} aria-label="Conditions">
          {value.map((c, i) => {
            const r = results?.[i];
            const n = i + 1;
            return (
              <li key={i} className={styles["row"]} data-ok={r ? String(r.ok) : undefined}>
                {r ? (
                  <span className={styles["mark"]} data-ok={String(r.ok)} role="img" aria-label={r.ok ? "Holds" : "Does not hold"}>
                    <Icon name={r.ok ? "check" : "cross"} size={12} />
                  </span>
                ) : <span className={styles["and"]} aria-hidden>{i === 0 ? "When" : "and"}</span>}
                <Input aria-label={`Condition ${n}: path`} mono size="sm" value={c.path} disabled={off} placeholder="status"
                  list={paths ? listId : undefined} className={styles["path"]} onChange={(e) => set(i, { ...c, path: e.target.value })} />
                <Select<ConditionOpName> aria-label={`Condition ${n}: operator`} size="sm" value={c.op} disabled={off}
                  options={CONDITION_OPS.map((o) => ({ value: o, label: CONDITION_OP_LABEL[o] }))}
                  onValueChange={(op) => set(i, { ...c, op, values: op === "exists" ? [] : op === "one_of" ? c.values : c.values.slice(0, 1) })} />
                <span className={styles["value"]}>
                  {c.op === "exists" ? <span className={styles["muted"]}>any value</span>
                    : c.op === "one_of" ? (
                      <ChipsInput label={`Condition ${n}: values`} values={c.values} disabled={off} placeholder="critical, high"
                        onChange={(values) => set(i, { ...c, values })} />
                    ) : (
                      <Input aria-label={`Condition ${n}: value`} mono size="sm" value={c.values[0] ?? ""} disabled={off}
                        onChange={(e) => set(i, { ...c, values: [e.target.value] })} />
                    )}
                </span>
                {off ? null : <IconButton icon="close" size="sm" label={`Remove condition ${n}`}
                  onClick={() => onChange?.(value.filter((_, j) => j !== i))} />}
                {r && !r.ok ? <span className={styles["why"]}>the body has <code className="ds-mono">{r.actual}</code></span> : null}
              </li>
            );
          })}
        </ol>
      )}
      {paths ? <datalist id={listId}>{paths.map((p) => <option key={p} value={p} />)}</datalist> : null}
      {off ? null : (
        <span>
          <Button size="sm" variant="secondary" leadingIcon="plus" data-testid="add-condition"
            onClick={() => onChange?.([...value, { path: "", op: "is", values: [] }])}>Condition</Button>
        </span>
      )}
    </div>
  );
}

export type DeliveryVerdictValue =
  | { readonly kind: "started" | "sent"; readonly runId: string | null }
  | { readonly kind: "skipped"; readonly because: string }
  | { readonly kind: "refused"; readonly reason: RefusalCode }
  | { readonly kind: "pending" };

const VERDICT: Record<DeliveryVerdictValue["kind"], { tone: string; icon: IconName; word: string }> = {
  started: { tone: "success", icon: "play", word: "Started run" },
  sent: { tone: "info", icon: "send", word: "Sent to running agent" },
  skipped: { tone: "neutral", icon: "circle-dotted", word: "Skipped" },
  refused: { tone: "danger", icon: "circle-x", word: "Refused" },
  pending: { tone: "neutral", icon: "clock", word: "Waiting" },
};

/**
 * What dude did with one delivery, as a chip: Started run, Sent to running
 * agent, Skipped (and, with `detail`, the condition that missed), Refused ·
 * its reason, or Waiting while the loop has not read it. With `onRun` a
 * verdict with a run is a button named "<verdict>: open the run".
 */
export function DeliveryVerdict({ verdict, onRun, detail = true }: {
  readonly verdict: DeliveryVerdictValue;
  readonly onRun?: ((runId: string) => void) | undefined;
  readonly detail?: boolean | undefined;
}) {
  const v = VERDICT[verdict.kind];
  const chip = (
    <span className={styles["verdict"]} data-tone={v.tone} data-verdict={verdict.kind}>
      <Icon name={v.icon} size={12} />
      <span>{verdict.kind === "refused" ? `${v.word} · ${REFUSAL_LABEL[verdict.reason]}` : v.word}</span>
    </span>
  );
  const run = (verdict.kind === "started" || verdict.kind === "sent") && verdict.runId && onRun ? verdict.runId : null;
  return (
    <span className={styles["verdictLine"]}>
      {run ? (
        <button type="button" className={styles["verdictLink"]} aria-label={`${v.word}: open the run`}
          onClick={(e) => { e.stopPropagation(); onRun?.(run); }}>{chip}</button>
      ) : chip}
      {detail && verdict.kind === "skipped" && verdict.because ? <span className={styles["why"]}>{verdict.because}</span> : null}
    </span>
  );
}

/**
 * A test body's verdict, live: a square sunken panel of lines (its group,
 * each condition), then `outcome`, set apart by space, in danger when it
 * would be skipped. A status region: a screen reader hears each change.
 */
export function IncomingVerdictPanel({ outcome, ok, children, ...rest }: {
  readonly outcome?: ReactNode;
  /** Whether the outcome is a run; false reads it in danger. */
  readonly ok?: boolean | undefined;
  readonly children: ReactNode;
  readonly "data-testid"?: string | undefined;
}) {
  return (
    <div className={styles["panel"]} role="status" {...rest}>
      {children}
      {outcome ? <p className={styles["outcome"]} data-ok={String(ok ?? true)}>{outcome}</p> : null}
    </div>
  );
}

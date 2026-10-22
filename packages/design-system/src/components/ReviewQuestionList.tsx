import { useId, useState, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Button } from "../primitives/Button.tsx";
import { Textarea } from "../primitives/Textarea.tsx";
import { Markdown } from "./Markdown.tsx";
import styles from "./ReviewQuestionList.module.css";

/** Who settled a question: a person (by name when known), or the task's conductor. */
export type ReviewQuestionAnswerer = { readonly kind: "person"; readonly name?: string | undefined } | { readonly kind: "conductor" };

export interface ReviewQuestionItem {
  readonly id: string;
  /** The asking reviewer's category. */
  readonly category: string;
  readonly question: string;
  readonly whyItMatters?: string | undefined;
  readonly evidenceChecked?: string | undefined;
  readonly neededFrom?: string | undefined;
  /** The finding it is about: a link or button the app renders, labelled with its title. */
  readonly about?: ReactNode;
  /** Default open. A withdrawn one is not shown. */
  readonly status?: "open" | "answered" | "withdrawn" | undefined;
  /** An answered one's answer, and who gave it. */
  readonly answer?: string | undefined;
  readonly answeredBy?: ReviewQuestionAnswerer | undefined;
}

export interface ReviewQuestionListProps extends Omit<HTMLAttributes<HTMLElement>, "title"> {
  readonly questions: ReadonlyArray<ReviewQuestionItem>;
  /** Section title. Default "Questions from reviewers". */
  readonly title?: ReactNode;
  /**
   * Answer an open question; with onWithdraw, each open one gets the form.
   * A rejection's message is shown under the form, which keeps its text.
   */
  readonly onAnswer?: ((id: string, answer: string) => Promise<void>) | undefined;
  /** Withdraw an open question as moot, with the reason. */
  readonly onWithdraw?: ((id: string, reason: string) => Promise<void>) | undefined;
  /** Why the reader may not answer (not the task's owner): the form is shown disabled, saying so. */
  readonly notAllowed?: string | undefined;
}

/**
 * Questions reviewers asked a person instead of guessing: each open one with
 * who can answer it, why it matters, what the reviewer checked and the
 * finding it is about, and, given onAnswer, a form to answer or withdraw it.
 * An answered one folds to one line with its answer and who gave it; a
 * withdrawn one is left out. Nothing at all when there are none to show.
 */
export function ReviewQuestionList({ questions, title = "Questions from reviewers", onAnswer, onWithdraw, notAllowed, className, ...rest }: ReviewQuestionListProps) {
  const headingId = useId();
  const shown = questions.filter((q) => q.status !== "withdrawn");
  if (shown.length === 0) return null;
  const open = shown.filter((q) => (q.status ?? "open") === "open").length;
  return (
    <section className={cx(styles["group"], className)} aria-labelledby={headingId} {...rest}>
      <header className={styles["groupHead"]}>
        <span id={headingId} className={cx("ds-label", styles["groupTitle"])}>{title}</span>
        <span className={styles["count"]}>{open} open</span>
      </header>
      <ul className={styles["list"]}>
        {shown.map((q) => (q.status ?? "open") === "open" ? (
          <li key={q.id} className={styles["item"]} data-question={q.id} data-status="open">
            <div className={styles["row"]}>
              <Icon name="message" size={12} className={styles["glyph"]} />
              <span className={styles["category"]} title={q.category}>{q.category}</span>
              <span className={styles["question"]}>{q.question}</span>
            </div>
            <dl className={styles["fields"]}>
              {q.neededFrom ? <Field label="Needed from">{q.neededFrom}</Field> : null}
              {q.about ? <Field label="About">{q.about}</Field> : null}
              {q.whyItMatters ? <Field label="Why it matters"><Markdown unmeasured source={q.whyItMatters} className={styles["prose"]} /></Field> : null}
              {q.evidenceChecked ? <Field label="Checked"><Markdown unmeasured source={q.evidenceChecked} className={styles["prose"]} /></Field> : null}
            </dl>
            {onAnswer && onWithdraw ? <AnswerForm id={q.id} onAnswer={onAnswer} onWithdraw={onWithdraw} notAllowed={notAllowed} /> : null}
          </li>
        ) : (
          <li key={q.id} className={cx(styles["item"], styles["settled"])} data-question={q.id} data-status={q.status}>
            <div className={styles["row"]}>
              <Icon name="check" size={12} className={styles["glyph"]} />
              <span className={styles["category"]} title={q.category}>{q.category}</span>
              <span className={styles["settledQuestion"]} title={q.question}>{q.question}</span>
            </div>
            <p className={styles["answer"]}>
              <span className={styles["by"]} data-testid="review-question-answered-by">{answeredBy(q.answeredBy)}</span>{" "}
              <span data-testid="review-question-answer">{q.answer}</span>
            </p>
          </li>
        ))}
      </ul>
    </section>
  );
}

function answeredBy(by: ReviewQuestionAnswerer | undefined): string {
  if (by?.kind === "conductor") return "The conductor answered:";
  return by?.name ? `${by.name} answered:` : "A person answered:";
}

/** An open question's answer box and Answer, then Withdraw, which asks for its reason first. */
function AnswerForm({ id, onAnswer, onWithdraw, notAllowed }: {
  readonly id: string;
  readonly onAnswer: (id: string, answer: string) => Promise<void>;
  readonly onWithdraw: (id: string, reason: string) => Promise<void>;
  readonly notAllowed?: string | undefined;
}) {
  const reasonId = useId();
  const [answer, setAnswer] = useState("");
  const [withdrawing, setWithdrawing] = useState(false);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState<"answer" | "withdraw" | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const refused = notAllowed !== undefined;
  const act = async (which: "answer" | "withdraw") => {
    setBusy(which);
    setProblem(null);
    try {
      await (which === "answer" ? onAnswer(id, answer.trim()) : onWithdraw(id, reason.trim()));
    } catch (err) {
      setProblem(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  };
  const describedBy = refused ? reasonId : undefined;
  return (
    <div className={styles["form"]} data-testid="review-question-form">
      {withdrawing ? (
        <>
          <Textarea aria-label="Why it no longer matters" placeholder="Why it no longer matters" rows={2} maxRows={6} value={reason}
            onChange={(e) => setReason(e.target.value)} disabled={refused || busy !== null} aria-describedby={describedBy}
            data-testid="review-question-reason" />
          <div className={styles["actions"]}>
            <Button size="sm" variant="secondary" disabled={refused || busy !== null || reason.trim() === ""} loading={busy === "withdraw"}
              onClick={() => void act("withdraw")} aria-describedby={describedBy} data-testid="review-question-withdraw-confirm">Withdraw</Button>
            <Button size="sm" variant="quiet" disabled={busy !== null} onClick={() => setWithdrawing(false)}>Keep it</Button>
          </div>
        </>
      ) : (
        <>
          <Textarea aria-label="Answer" placeholder="The fact the reviewer asked for" rows={2} maxRows={8} value={answer}
            onChange={(e) => setAnswer(e.target.value)} disabled={refused || busy !== null} aria-describedby={describedBy}
            data-testid="review-question-answer-text" />
          <div className={styles["actions"]}>
            <Button size="sm" variant="secondary" disabled={refused || busy !== null || answer.trim() === ""} loading={busy === "answer"}
              onClick={() => void act("answer")} aria-describedby={describedBy} data-testid="review-question-answer-send">Answer</Button>
            <Button size="sm" variant="quiet" disabled={refused || busy !== null} onClick={() => setWithdrawing(true)}
              aria-describedby={describedBy} data-testid="review-question-withdraw">Withdraw…</Button>
          </div>
        </>
      )}
      {refused ? <p id={reasonId} className={styles["hint"]} data-testid="review-question-not-allowed">{notAllowed}</p> : null}
      {problem ? <p className={styles["problem"]} role="alert">{problem}</p> : null}
    </div>
  );
}

function Field({ label, children }: { readonly label: string; readonly children: ReactNode }) {
  return (
    <div className={styles["field"]}>
      <dt className={cx("ds-label", styles["label"])}>{label}</dt>
      <dd className={styles["value"]}>{children}</dd>
    </div>
  );
}

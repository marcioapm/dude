import { useId, type HTMLAttributes, type ReactNode } from "react";
import { cx } from "../util/cx.ts";
import { Icon } from "../icons/index.tsx";
import { Markdown } from "./Markdown.tsx";
import styles from "./ReviewQuestionList.module.css";

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
}

export interface ReviewQuestionListProps extends Omit<HTMLAttributes<HTMLElement>, "title"> {
  readonly questions: ReadonlyArray<ReviewQuestionItem>;
  /** Section title. Default "Questions from reviewers". */
  readonly title?: ReactNode;
}

/**
 * Questions reviewers asked a person instead of guessing: each the
 * question, who can answer it, why it matters, what the reviewer checked,
 * and the finding it is about. Read-only. Nothing at all when there are none.
 */
export function ReviewQuestionList({ questions, title = "Questions from reviewers", className, ...rest }: ReviewQuestionListProps) {
  const headingId = useId();
  if (questions.length === 0) return null;
  return (
    <section className={cx(styles["group"], className)} aria-labelledby={headingId} {...rest}>
      <header className={styles["groupHead"]}>
        <span id={headingId} className={cx("ds-label", styles["groupTitle"])}>{title}</span>
        <span className={styles["count"]}>{questions.length} open</span>
      </header>
      <ul className={styles["list"]}>
        {questions.map((q) => (
          <li key={q.id} className={styles["item"]} data-question={q.id}>
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
          </li>
        ))}
      </ul>
    </section>
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

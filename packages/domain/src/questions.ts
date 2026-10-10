/**
 * An agent's ask: one to four questions put to a person at once
 * (ask_person, migration 104), and the person's answer to each.
 * `questions.items` / `questions.answers`, and the `items` of
 * `question.asked` and `answers` of `question.answered`, have these shapes.
 */

export interface AskChoice {
  label: string;
  /** One line of why, under the label; "" for none. */
  description: string;
  /** The agent's suggestion: shown, never preselected. At most one per question. */
  recommended: boolean;
}

export interface AskItem {
  /** A few words naming it, its tab; "" for a question asked alone the old way. */
  header: string;
  question: string;
  choices: AskChoice[];
  /** Several choices may be picked. */
  multiple: boolean;
}

/** A person's answer to one item: the choices picked, by index, and their own words ("" for none). */
export interface ItemAnswer {
  choices: number[];
  text: string;
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");

/** An ask's items as an event or row carries them; one item from `prompt` and `options` when it carries none (before 104). */
export function askItems(items: unknown, prompt: unknown, options: unknown): AskItem[] {
  if (Array.isArray(items) && items.length > 0) {
    return items.map((raw) => {
      const it = (raw ?? {}) as Record<string, unknown>;
      return {
        header: str(it.header),
        question: str(it.question),
        multiple: it.multiple === true,
        choices: Array.isArray(it.choices) ? it.choices.map((c) => {
          const ch = (c ?? {}) as Record<string, unknown>;
          return { label: str(ch.label), description: str(ch.description), recommended: ch.recommended === true };
        }) : [],
      };
    });
  }
  const labels = Array.isArray(options) ? options.map(String) : [];
  return [{ header: "", question: str(prompt), multiple: false, choices: labels.map((label) => ({ label, description: "", recommended: false })) }];
}

/** An answer's per-item answers as `question.answered` carries them; null when it carries none (an answer before 104). */
export function itemAnswers(answers: unknown): ItemAnswer[] | null {
  if (!Array.isArray(answers)) return null;
  return answers.map((raw) => {
    const a = (raw ?? {}) as Record<string, unknown>;
    return { choices: Array.isArray(a.choices) ? a.choices.filter((n): n is number => Number.isInteger(n)) : [], text: str(a.text) };
  });
}

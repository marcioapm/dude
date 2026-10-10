-- 104_question_items.sql — one ask_person may put up to four questions.
--
-- items: what was asked, normalized, one object per question:
--   {"header": text, "question": text, "multiple": bool,
--    "choices": [{"label": text, "description": text, "recommended": bool}]}
-- answers: what the person answered, one object per item, in order:
--   {"choices": [int, …], "text": text}   (picked indexes and/or own words)
-- NULL until answered (and for an answer recorded before this migration).
--
-- prompt, options and answer stay what every reader already reads: for one
-- item the question, its choices' labels and the answer's text as before;
-- for several a one-line summary, no options, and the rendered answers.

ALTER TABLE questions
  ADD COLUMN items jsonb,
  ADD COLUMN answers jsonb;

-- The one item a question asked the old way is: its prompt, its options'
-- labels as choices (delivery.SingleItem).
CREATE FUNCTION question_items_of(prompt text, options jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
  SELECT jsonb_build_array(jsonb_build_object(
    'header', '', 'question', prompt, 'multiple', false,
    'choices', COALESCE((SELECT jsonb_agg(jsonb_build_object('label', o #>> '{}', 'description', '', 'recommended', false)
                                          ORDER BY n)
                         FROM jsonb_array_elements(CASE jsonb_typeof(options) WHEN 'array' THEN options ELSE '[]' END)
                              WITH ORDINALITY AS t(o, n)), '[]'::jsonb)))
$$;

-- Every existing question is one item.
UPDATE questions SET items = question_items_of(prompt, options);

-- Every writer names its items; this keeps an INSERT that names none (test
-- fixtures, an older caller) valid with the same one item.
CREATE FUNCTION questions_default_items() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.items IS NULL THEN
    NEW.items := question_items_of(NEW.prompt, NEW.options);
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER questions_default_items BEFORE INSERT ON questions
  FOR EACH ROW EXECUTE FUNCTION questions_default_items();

ALTER TABLE questions ALTER COLUMN items SET NOT NULL;
ALTER TABLE questions
  ADD CONSTRAINT questions_items_check
    CHECK (jsonb_typeof(items) = 'array' AND jsonb_array_length(items) BETWEEN 1 AND 4),
  ADD CONSTRAINT questions_answers_check
    CHECK (answers IS NULL OR (jsonb_typeof(answers) = 'array' AND jsonb_array_length(answers) = jsonb_array_length(items)));

-- The scripted agent's fake/ask-several (its implementer asks several
-- questions in one ask_person) is a test model a tier may request.
ALTER TABLE model_tiers DROP CONSTRAINT model_tiers_model_check;
ALTER TABLE model_tiers ADD CONSTRAINT model_tiers_model_check
  CHECK (model IN ('fake/scripted', 'fake/hang', 'fake/tools', 'fake/request', 'fake/wait', 'fake/live', 'fake/ask', 'fake/ask-several',
                   'fake/command', 'fake/stuck', 'fake/stall', 'fake/silent', 'fake/lookup')
         OR (length(model) BETWEEN 1 AND 200 AND model !~ '[[:space:]/]'));

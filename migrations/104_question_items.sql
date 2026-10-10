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

-- Every existing question is one item: its prompt and its options.
UPDATE questions SET items = jsonb_build_array(jsonb_build_object(
  'header', '', 'question', prompt, 'multiple', false,
  'choices', COALESCE((SELECT jsonb_agg(jsonb_build_object('label', o #>> '{}', 'description', '', 'recommended', false)
                                        ORDER BY n)
                       FROM jsonb_array_elements(CASE jsonb_typeof(options) WHEN 'array' THEN options ELSE '[]' END)
                            WITH ORDINALITY AS t(o, n)), '[]'::jsonb)));

-- A writer that names no items (a decision recorded as an answered
-- question, an older caller) gets the same one item from prompt and options.
CREATE FUNCTION questions_default_items() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.items IS NULL THEN
    NEW.items := jsonb_build_array(jsonb_build_object(
      'header', '', 'question', NEW.prompt, 'multiple', false,
      'choices', COALESCE((SELECT jsonb_agg(jsonb_build_object('label', o #>> '{}', 'description', '', 'recommended', false)
                                            ORDER BY n)
                           FROM jsonb_array_elements(CASE jsonb_typeof(NEW.options) WHEN 'array' THEN NEW.options ELSE '[]' END)
                                WITH ORDINALITY AS t(o, n)), '[]'::jsonb)));
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

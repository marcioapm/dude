-- 083_finding_topic.sql — a finding's category is its reviewer's flavour.

-- review_findings.category is which reviewer flavour found it (012), what
-- re-review routing groups by. Reviewers wrote their own words there too
-- (storybook, state, accessibility…), so a finding could name a category
-- no reviewer has and never be judged again. The reviewer's own word is
-- kept here, shown beside the flavour; category is the reporting Run's.
ALTER TABLE review_findings ADD COLUMN topic text;

UPDATE review_findings f
  SET topic = CASE WHEN f.category IS DISTINCT FROM r.category THEN f.category END,
      category = r.category
  FROM runs r
  WHERE r.id = f.run_id AND r.category IS NOT NULL AND f.category IS DISTINCT FROM r.category;

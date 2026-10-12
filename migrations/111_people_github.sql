-- 111_people_github.sql — who a person is on GitHub, for the
-- Co-authored-by trailers on the commits an agent makes for their task.
--
-- github_login and github_user_id together make GitHub's noreply address
-- (<id>+<login>@users.noreply.github.com), which GitHub links to the
-- account whatever addresses it keeps private; the id is resolved from the
-- login when it is saved, and stays null when GitHub could not be asked.
-- commit_email, when set, is the address used instead. people.email (the
-- address the person signs in with) is the last resort.
--
-- Nullable columns with no default: a catalogue change only, no rewrite of
-- people and no lock held past the ALTER.

ALTER TABLE people
  ADD COLUMN github_login   text,
  ADD COLUMN github_user_id bigint,
  ADD COLUMN commit_email   citext;

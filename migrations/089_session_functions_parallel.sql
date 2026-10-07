-- 089_session_functions_parallel.sql — session_role and memory_visible
-- only read session_people, so a parallel worker may run them: unmarked,
-- they default to PARALLEL UNSAFE and keep every memory search, the
-- organisation's own memories included, to a single process.

ALTER FUNCTION session_role(text, text) PARALLEL SAFE;
ALTER FUNCTION memory_visible(text, text, text) PARALLEL SAFE;

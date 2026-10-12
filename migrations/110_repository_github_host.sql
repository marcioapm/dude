-- 110_repository_github_host.sql — a repository at https://www.github.com/
-- is stored at https://github.com/.
--
-- GitHub redirects www.github.com to github.com, and git does not send
-- lux's Authorization header (http.extraHeader) to the redirected host:
-- a private repository clones as anonymous and fails. The backend writes
-- the host canonical from now on (canonicalRepositoryUrl, structure.ts);
-- this rewrites what was stored before. Only the host of an https URL
-- changes: ssh and git:// are not redirected.
--
-- Lock-light: an UPDATE of the matching rows only, which takes row locks
-- and ROW EXCLUSIVE on repositories, so reads and other rows' writes go on.
-- repositories has no triggers, and its unique index (094) is on the name,
-- which this leaves alone.

UPDATE repositories SET url = 'https://github.com/' || substr(url, length('https://www.github.com/') + 1)
WHERE url ILIKE 'https://www.github.com/%';

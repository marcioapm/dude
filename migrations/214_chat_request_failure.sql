-- 214_chat_request_failure.sql — why a chat request failed, apart from the
-- routing model's reasoning: the model or proxy error, the provider's
-- refusal, or the reason dude gave up. Requests shows it on a failed row,
-- with made_route: the route a failed request still made something as, so
-- its label says what was made, not what was asked.
--
-- Catalog changes only: the checks are NOT VALID, so they bind every write
-- from now on without scanning chat_requests under the ACCESS EXCLUSIVE
-- lock; every existing row holds NULL. 215 fills in the rows that already failed.

ALTER TABLE chat_requests ADD COLUMN failure text, ADD COLUMN made_route text;
ALTER TABLE chat_requests ADD CONSTRAINT chat_requests_failure_check
  CHECK (failure IS NULL OR length(failure) <= 2000) NOT VALID;
ALTER TABLE chat_requests ADD CONSTRAINT chat_requests_made_route_check
  CHECK (made_route IN ('brainstorm', 'task', 'epic', 'resume', 'task_link')) NOT VALID;

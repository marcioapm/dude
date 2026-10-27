-- 214_chat_request_failure.sql — why a chat request failed, apart from the
-- routing model's reasoning: the model or proxy error, the provider's
-- refusal, or the reason dude gave up. Requests shows it on a failed row.
--
-- Catalog changes only: the check is NOT VALID, so it binds every write from
-- now on without scanning chat_requests under the ACCESS EXCLUSIVE lock;
-- every existing row holds NULL. 215 fills in the rows that already failed.

ALTER TABLE chat_requests ADD COLUMN failure text;
ALTER TABLE chat_requests ADD CONSTRAINT chat_requests_failure_check
  CHECK (failure IS NULL OR length(failure) <= 2000) NOT VALID;

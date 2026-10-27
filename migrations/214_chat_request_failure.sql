-- 214_chat_request_failure.sql — why a chat request failed, apart from the
-- routing model's reasoning: the model or proxy error, the provider's
-- refusal, or the reason dude gave up. Requests shows it on a failed row.

ALTER TABLE chat_requests ADD COLUMN failure text CHECK (failure IS NULL OR length(failure) <= 2000);

-- Failed requests recorded before kept their cause only in reasoning: the
-- whole of it when dude wrote it (a failed routing call, giving up), else
-- the last parenthesised clause the failure appended to the model's words.
UPDATE chat_requests
  SET failure = left(CASE
      WHEN reasoning LIKE 'the routing call failed: %' OR reasoning LIKE 'dude could not act on it: %'
        OR reasoning LIKE '% did not answer after % tries' THEN reasoning
      ELSE COALESCE(substring(reasoning FROM '\(([^()]*)\)$'), reasoning) END, 2000)
  WHERE route = 'failed' AND reasoning <> '';

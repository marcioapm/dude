-- A work item ready to merge is told to people like an ask is: the
-- notifier's index covers its event too.
DROP INDEX events_asks_idx;
CREATE INDEX events_asks_idx ON events (cursor)
  WHERE event_type IN ('question.asked', 'repository.requested', 'work_item.ready_to_merge');

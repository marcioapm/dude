-- 114_preview_wake_timeouts.sql — how long lux's waking page waits for a
-- preview, longer until it has served once.
--
-- A project's preview settings gain firstWakeTimeoutMinutes (default 15),
-- the wake timeout of its previews' servers until the preview has served,
-- and wakeTimeoutMinutes (default 5), for every wake after. Rows saved
-- before this have neither and read the defaults.
CREATE OR REPLACE FUNCTION preview_settings(p projects) RETURNS json LANGUAGE sql STABLE AS $$
  SELECT json_build_object('image', p.preview_settings->'image',
    'imageId', p.preview_image_id,
    'egress', COALESCE(p.preview_settings->'egress', '[]'::jsonb),
    'idleTimeoutMinutes', COALESCE((p.preview_settings->>'idleTimeoutMinutes')::float8, 15),
    'firstWakeTimeoutMinutes', COALESCE((p.preview_settings->>'firstWakeTimeoutMinutes')::int, 15),
    'wakeTimeoutMinutes', COALESCE((p.preview_settings->>'wakeTimeoutMinutes')::int, 5),
    'machineSize', p.preview_settings->'machineSize')
$$;

-- When a wakeable preview's server first became ready (lux's server.state
-- on the feed): from then on its servers get the later wake timeout.
-- Previews from before this count as never ready until they next serve.
-- wake_timeouts_checked_at: when the sweep last made its servers' wake
-- timeouts what the settings say; one it could not shorten (a wake under
-- way) is looked at again 30 seconds later, not every sweep.
ALTER TABLE runs ADD COLUMN preview_ready_at timestamptz,
  ADD COLUMN wake_timeouts_checked_at timestamptz;

-- The wake timeout dude last set on the server in lux, in seconds; NULL:
-- not known (made before this), so it is set at the next sweep.
ALTER TABLE preview_servers ADD COLUMN wake_timeout_s integer;

-- 195_chat.sql — chat providers (Slack first): an organisation installs a
-- provider's app in one workspace, its people link their provider
-- accounts, and a mention of dude there is routed to a session, a task or
-- an epic. Every table is keyed by `provider`, an adapter's id ("slack"):
-- nothing here knows one provider from another.

-- One workspace of one provider is bound to one organisation, and an
-- organisation has at most one workspace per provider. The unique
-- (provider, external_workspace_id) is global, across tenants: installing a
-- workspace another organisation holds is refused by it.
CREATE TABLE chat_installations (
  id                    text PRIMARY KEY,
  organization_id       text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  provider              text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9_]{1,31}$'),
  external_workspace_id text NOT NULL CHECK (external_workspace_id <> ''),
  workspace_name        text NOT NULL DEFAULT '',
  workspace_domain      text NOT NULL DEFAULT '',
  bot_user_id           text NOT NULL DEFAULT '',
  -- The bot token the provider issued, stored as forge_credentials.secret
  -- is: in this row, behind row-level security, never returned by a route.
  bot_token             text NOT NULL,
  scopes                text[] NOT NULL DEFAULT '{}',
  installed_by          text REFERENCES people(id) ON DELETE SET NULL,
  installed_at          timestamptz NOT NULL DEFAULT now(),
  last_event_at         timestamptz,
  -- Calls to the provider that failed today (failures_day), and the last.
  failed_today          integer NOT NULL DEFAULT 0,
  failures_day          date,
  last_failure          text,
  -- Provider-specific, opaque to shared code. Every provider may carry
  -- additional_instructions (≤ 4000 characters), told after the
  -- organisation's own.
  settings              jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(settings) = 'object'
      AND char_length(COALESCE(settings->>'additional_instructions', '')) <= 4000),
  UNIQUE (provider, external_workspace_id),
  UNIQUE (organization_id, provider)
);
ALTER TABLE chat_installations ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_installations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chat_installations
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON chat_installations TO dude_app;
-- The receiver finds the organisation a delivery is for by its workspace.
GRANT SELECT (id, organization_id, provider, external_workspace_id) ON chat_installations TO dude_sweeper;

-- A provider account linked to a dude account, once, by the person signing
-- in with the provider while signed in to dude: never matched by email. A
-- provider account links to at most one account per workspace, and an
-- account to at most one provider account there.
CREATE TABLE chat_identities (
  organization_id       text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  provider              text NOT NULL,
  external_workspace_id text NOT NULL,
  external_user_id      text NOT NULL CHECK (external_user_id <> ''),
  account_id            text NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  -- The account's membership in the organisation when it linked.
  person_id             text NOT NULL REFERENCES people(id) ON DELETE CASCADE,
  handle                text NOT NULL DEFAULT '',
  display_name          text NOT NULL DEFAULT '',
  linked_at             timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (provider, external_workspace_id, external_user_id),
  UNIQUE (provider, external_workspace_id, account_id)
);
CREATE INDEX chat_identities_person_idx ON chat_identities (person_id);
ALTER TABLE chat_identities ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_identities FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chat_identities
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON chat_identities TO dude_app;

-- One-time states, 15 minutes each, kept as their sha256. A `prompt` is the
-- private Connect button dude offered someone not linked yet: it names the
-- provider account and keeps the mention to route once they link. An
-- `oauth` is a sign-in with the provider started by a person in dude, for
-- the callback to find; it carries the prompt's mention when it began
-- from one.
CREATE TABLE chat_connect_states (
  state_hash            text PRIMARY KEY,
  organization_id       text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  kind                  text NOT NULL CHECK (kind IN ('prompt', 'oauth', 'install')),
  provider              text NOT NULL,
  external_workspace_id text,
  external_user_id      text,
  person_id             text REFERENCES people(id) ON DELETE CASCADE,
  -- Where the callback lands: Your settings, or the connect page.
  return_to             text NOT NULL DEFAULT 'settings' CHECK (return_to IN ('settings', 'connect')),
  pending_mention       jsonb,
  -- An install's or sign-in's: the sha256 of a cookie set on the browser
  -- that started it, so a callback is finished only in that browser; and
  -- the redirect address the provider was given, which its token exchange
  -- must repeat.
  browser_hash          text,
  redirect_uri          text,
  created_at            timestamptz NOT NULL DEFAULT now(),
  expires_at            timestamptz NOT NULL DEFAULT now() + interval '15 minutes',
  CHECK (kind <> 'oauth' OR person_id IS NOT NULL),
  CHECK (kind <> 'install' OR person_id IS NOT NULL),
  CHECK (kind <> 'prompt' OR (external_workspace_id IS NOT NULL AND external_user_id IS NOT NULL))
);
ALTER TABLE chat_connect_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_connect_states FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chat_connect_states
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, DELETE ON chat_connect_states TO dude_app;

-- Which organisation a state is for, and whether it is still good: the
-- connect page reads it before it knows (a person signed in elsewhere is
-- told "wrong organisation", not "expired"). Returns nothing else.
CREATE FUNCTION chat_connect_state_org(p_state_hash text) RETURNS TABLE (organization_id text, expired boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT organization_id, expires_at <= now() FROM chat_connect_states WHERE state_hash = p_state_hash
$$;
REVOKE ALL ON FUNCTION chat_connect_state_org(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION chat_connect_state_org(text) TO dude_app, dude_sweeper;

-- Deliveries from a provider (its events and interactions), stored before
-- they are acted on, as webhook_deliveries are GitHub's: the backend hands
-- them to the orchestrator, which verifies, stores and answers at once; the
-- chat loop routes them. The provider's event id is the key, so a retry is
-- recognised rather than routed twice. `mention` is a neutral mention dude
-- kept itself (one held for a Connect, routed once the person linked).
-- The chat loop claims deliveries with a lease (claimed_until), routes
-- different threads at once and one thread's in order (thread_key), and
-- keeps the routing judgement's answer (decision) so a retry after it acts
-- on that answer rather than asking the model again. A delivery is tried
-- at most 6 times; processed ones are kept 7 days.
CREATE TABLE chat_events (
  provider        text NOT NULL,
  event_id        text NOT NULL,
  organization_id text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  kind            text NOT NULL CHECK (kind IN ('event', 'interaction', 'mention')),
  payload         jsonb NOT NULL,
  -- provider, workspace, conversation and thread; '' for a delivery no
  -- other waits on (an uninstall).
  thread_key      text NOT NULL DEFAULT '',
  received_at     timestamptz NOT NULL DEFAULT now(),
  processed_at    timestamptz,
  attempts        integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz,
  claimed_until   timestamptz,
  decision        jsonb,
  last_error      text,
  -- When dude put its working mark (👀) on the mention, as the delivery's
  -- first stage: set once, so a retry, a reclaimed lease or a mention kept
  -- for a Connect marks nothing again.
  working_at      timestamptz,
  PRIMARY KEY (provider, event_id)
);
CREATE INDEX chat_events_pending_idx ON chat_events (received_at) WHERE processed_at IS NULL AND attempts < 6;
CREATE INDEX chat_events_thread_pending_idx ON chat_events (thread_key, received_at) WHERE processed_at IS NULL AND attempts < 6;
CREATE INDEX chat_events_received_idx ON chat_events (received_at);
ALTER TABLE chat_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_events FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chat_events
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE ON chat_events TO dude_app;
GRANT SELECT, DELETE ON chat_events TO dude_sweeper;
GRANT UPDATE (claimed_until) ON chat_events TO dude_sweeper;

-- A provider thread bound to what a mention in it made: exactly one of a
-- session, a task or an epic. A later mention in the thread goes there.
CREATE TABLE chat_threads (
  organization_id       text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  provider              text NOT NULL,
  external_workspace_id text NOT NULL,
  conversation_id       text NOT NULL,
  thread_id             text NOT NULL,
  conversation_kind     text NOT NULL CHECK (conversation_kind IN ('public', 'private', 'group', 'dm')),
  conversation_name     text NOT NULL DEFAULT '',
  session_id            text REFERENCES sessions(id) ON DELETE CASCADE,
  task_id               text REFERENCES tasks(id) ON DELETE CASCADE,
  epic_id               text REFERENCES epics(id) ON DELETE CASCADE,
  bound_by              text REFERENCES people(id) ON DELETE SET NULL,
  -- The last thread message the session was given; later ones follow the
  -- next mention in.
  last_read_message_id  text NOT NULL DEFAULT '',
  created_at            timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (provider, external_workspace_id, conversation_id, thread_id),
  CHECK (num_nonnulls(session_id, task_id, epic_id) = 1)
);
CREATE INDEX chat_threads_session_idx ON chat_threads (session_id) WHERE session_id IS NOT NULL;
CREATE INDEX chat_threads_task_idx ON chat_threads (task_id) WHERE task_id IS NOT NULL;
CREATE INDEX chat_threads_epic_idx ON chat_threads (epic_id) WHERE epic_id IS NOT NULL;
ALTER TABLE chat_threads ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_threads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chat_threads
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON chat_threads TO dude_app;

-- Every mention dude routed or refused in an installed workspace: where,
-- who, what was decided and why, and what it made. Admins read it
-- (Requests); kept 30 days.
CREATE TABLE chat_requests (
  id                    text PRIMARY KEY,
  organization_id       text NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  provider              text NOT NULL,
  -- The delivery it answers (an event, a kept mention, a clicked choice):
  -- one request each, so a delivery handled twice makes nothing twice.
  event_id              text NOT NULL,
  external_workspace_id text NOT NULL,
  conversation_id       text NOT NULL,
  conversation_kind     text CHECK (conversation_kind IN ('public', 'private', 'group', 'dm')),
  conversation_name     text NOT NULL DEFAULT '',
  thread_id             text NOT NULL DEFAULT '',
  external_user_id      text NOT NULL,
  external_user_name    text NOT NULL DEFAULT '',
  person_id             text REFERENCES people(id) ON DELETE SET NULL,
  text                  text NOT NULL DEFAULT '',
  permalink             text NOT NULL DEFAULT '',
  -- sha256 of the thread as the model was sent it.
  context_hash          text,
  route                 text NOT NULL CHECK (route IN ('brainstorm', 'task', 'epic', 'resume', 'task_link', 'clarify',
                          'not_connected', 'not_member', 'failed')),
  -- What the model answered, when dude went another way.
  asked_route           text,
  confidence            real CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
  reasoning             text NOT NULL DEFAULT '',
  rounds                integer NOT NULL DEFAULT 0,
  result_kind           text CHECK (result_kind IN ('session', 'task', 'epic')),
  result_id             text,
  shared_added          text[] NOT NULL DEFAULT '{}',
  shared_skipped        text[] NOT NULL DEFAULT '{}',
  model_call_id         text REFERENCES model_calls(id) ON DELETE SET NULL,
  -- The neutral mention, kept so a clarifying answer can route it again,
  -- and the question asked with its choices and the message it is in.
  mention               jsonb,
  question              jsonb,
  question_message_id   text,
  answered_at           timestamptz,
  -- The clicked choice that answered it: its delivery's event id.
  answered_by_event     text,
  -- What dude posts for it (its reply, question or private note, and
  -- reactions), recorded with the request and posted after it commits;
  -- replied_at once posted, or given up.
  effects               jsonb NOT NULL DEFAULT '[]'::jsonb,
  replied_at            timestamptz,
  -- A request whose result is a session keeps its 👀 until that session's
  -- agent shows life: its first agent.message or agent.tool.called past
  -- this cursor (the session's last event once the mention went in) puts ✅,
  -- its agent ending first ❌, and 24 hours of neither leaves 👀
  -- (expired). NULL: nothing to wait for; the mark went with the reply.
  awaits_session_after  bigint,
  mark_settled          text CHECK (mark_settled IN ('done', 'failed', 'expired')),
  mark_settled_at       timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, event_id)
);
CREATE INDEX chat_requests_created_idx ON chat_requests (organization_id, created_at DESC);
-- Requests filtered by route or provider read one range each.
CREATE INDEX chat_requests_route_idx ON chat_requests (organization_id, route, created_at DESC);
CREATE INDEX chat_requests_provider_idx ON chat_requests (organization_id, provider, created_at DESC);
-- The retention delete, across organisations. BRIN gives no order, so the
-- planner never reads the Requests list through it instead of
-- chat_requests_created_idx (a cross-tenant btree on created_at it would).
CREATE INDEX chat_requests_age_idx ON chat_requests USING brin (created_at);
-- Clarify rounds are counted per thread.
CREATE INDEX chat_requests_thread_idx ON chat_requests (provider, external_workspace_id, conversation_id, thread_id, created_at);
-- A person's own mentions of the last 30 days read only their rows.
CREATE INDEX chat_requests_person_idx ON chat_requests (person_id, created_at DESC);
ALTER TABLE chat_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chat_requests
  USING (organization_id = current_organization_id()) WITH CHECK (organization_id = current_organization_id());
GRANT SELECT, INSERT, UPDATE, DELETE ON chat_requests TO dude_app;
-- The marks still waiting on their session, read across tenants by the
-- chat-reactions loop in (created_at, id) order, a page a pass.
CREATE INDEX chat_requests_awaiting_idx ON chat_requests (created_at, id)
  WHERE awaits_session_after IS NOT NULL AND mark_settled_at IS NULL;
GRANT SELECT (id, created_at, organization_id, provider, result_id, replied_at, awaits_session_after, mark_settled_at), DELETE
  ON chat_requests TO dude_sweeper;

-- The organisation's additional routing instructions, told to the routing
-- call for every provider after dude's built-in ones.
ALTER TABLE organizations ADD COLUMN chat_instructions text NOT NULL DEFAULT ''
  CHECK (char_length(chat_instructions) <= 4000);

-- Where a task or epic came from outside dude: { provider, workspaceId,
-- conversationId, conversationName, threadId, permalink, requestedBy }.
ALTER TABLE tasks ADD COLUMN origin jsonb CHECK (origin IS NULL OR jsonb_typeof(origin) = 'object');
ALTER TABLE epics ADD COLUMN origin jsonb CHECK (origin IS NULL OR jsonb_typeof(origin) = 'object');
-- And a session: the thread it was made from (shown as its routing line).
ALTER TABLE sessions ADD COLUMN origin jsonb CHECK (origin IS NULL OR jsonb_typeof(origin) = 'object');
-- The routing call's recent chat-born sessions, newest first, per organisation.
CREATE INDEX sessions_chat_recent_idx ON sessions (organization_id, created_at DESC) WHERE origin IS NOT NULL;

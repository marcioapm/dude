package chat

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
	"github.com/marciomartins/dude/orchestrator/internal/judge"
)

// Service is dude's side of every chat provider: storing what they send,
// installing and linking, and routing mentions (route.go). It knows
// providers only through Registry.
type Service struct {
	DB        *db.DB
	Providers Registry
	// The judgement tier answers the routing call.
	Judge *judge.Service
	// auth.public_url: where the links dude replies with point. "" sends
	// relative links, which a provider shows as text.
	PublicURL string
	Log       *slog.Logger
	// Wakes the chat loop once a delivery was stored.
	Kick func()
	// Wakes the loops that start runs, once a commit started or resumed one.
	RunStarted func()
	// The clock, for tests of the 15-minute states.
	Now func() time.Time
	// Deliveries routed at once; DefaultWorkers when 0.
	Workers int

	serviceState
	nameCache nameCache
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) kick() {
	if s.Kick != nil {
		s.Kick()
	}
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Refusal is a request the caller made that dude declines, with a code the
// backend passes on (and the web reads).
type Refusal struct {
	Status int
	Code   string
	Msg    string
}

func (r *Refusal) Error() string { return r.Msg }

func refuse(status int, code, format string, a ...any) error {
	return &Refusal{Status: status, Code: code, Msg: fmt.Sprintf(format, a...)}
}

// ErrUnknownProvider is a {provider} dude does not have.
var ErrUnknownProvider = refuse(404, "not_found", "no such chat provider")

// provider is the registered provider id names.
func (s *Service) provider(id string) (Provider, error) {
	p, ok := s.Providers.Get(id)
	if !ok {
		return nil, ErrUnknownProvider
	}
	return p, nil
}

// hashState is a state as stored: its sha256, hex.
func hashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

// newState is 32 random bytes, URL-safe.
func newState() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// stateTTL is how long a connect, install or prompt state is good.
const stateTTL = 15 * time.Minute

// Receive is a delivery to /v1/webhooks/chat/{provider}/events or
// /interactions, as the backend passed it on: verified, parsed, and stored
// for the chat loop, keyed on the provider's event id so a retry is not
// routed twice. It answers at once; nothing slow happens here. The answer
// body is what the provider is sent (URL verification's challenge).
func (s *Service) Receive(ctx context.Context, providerID string, interaction bool, header map[string][]string, body []byte) (any, error) {
	p, err := s.provider(providerID)
	if err != nil {
		return nil, err
	}
	only := http.Header{}
	for _, name := range p.RequestHeaders() {
		if v := http.Header(header).Values(name); len(v) > 0 {
			only[http.CanonicalHeaderKey(name)] = v
		}
	}
	if err := p.VerifyRequest(only, body, s.now()); err != nil {
		return nil, refuse(401, "unauthorized", "%s", err.Error())
	}
	var (
		kind, id, workspace string
		payload             any
	)
	if interaction {
		in, err := p.ParseInteraction(body)
		if err != nil {
			return map[string]any{"ok": true, "ignored": err.Error()}, nil
		}
		kind, id, workspace, payload = "interaction", in.ID, in.WorkspaceID, in
	} else {
		ev, err := p.ParseEvent(body)
		if err != nil {
			return nil, refuse(400, "bad_request", "%s", err.Error())
		}
		switch ev.Kind {
		case EventURLVerification:
			return map[string]any{"challenge": ev.Challenge}, nil
		case EventIgnored:
			return map[string]any{"ok": true}, nil
		}
		kind, id, workspace, payload = "event", ev.ID, ev.WorkspaceID, storedEvent{Kind: ev.Kind, Mention: ev.Mention,
			RevokedBots: ev.RevokedBots, RevokedUsers: ev.RevokedUsers}
	}
	org, err := s.orgOf(ctx, providerID, workspace)
	if err != nil {
		return nil, err
	}
	if org == "" {
		// A workspace no organisation installed dude in: nothing to answer
		// with, since a reply needs an installation's token.
		return map[string]any{"ok": true, "ignored": "not installed"}, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	key := ""
	switch v := payload.(type) {
	case Interaction:
		key = threadKey(providerID, v.WorkspaceID, v.ConversationID, v.ThreadID)
	case storedEvent:
		if v.Mention != nil {
			key = threadKey(providerID, v.Mention.WorkspaceID, v.Mention.ConversationID, v.Mention.ThreadID)
		}
	}
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO chat_events (provider, event_id, organization_id, kind, payload, thread_key)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (provider, event_id) DO NOTHING`, providerID, id, org, kind, raw, key); err != nil {
			return err
		}
		// At most once a minute: a burst of deliveries does not queue on this row.
		_, err := tx.Exec(ctx, `UPDATE chat_installations SET last_event_at = now()
			WHERE provider = $1 AND (last_event_at IS NULL OR last_event_at < now() - interval '1 minute')`, providerID)
		return err
	}); err != nil {
		return nil, err
	}
	s.kick()
	return map[string]any{"ok": true}, nil
}

// storedEvent is an event as chat_events keeps it.
type storedEvent struct {
	Kind         string   `json:"kind"`
	Mention      *Mention `json:"mention,omitempty"`
	RevokedBots  []string `json:"revokedBots,omitempty"`
	RevokedUsers []string `json:"revokedUsers,omitempty"`
}

// orgOf is the organisation that installed workspace, read across tenants
// before any is known; "" for none.
func (s *Service) orgOf(ctx context.Context, provider, workspace string) (string, error) {
	var org string
	err := s.DB.InSystem(ctx, "chat-receive", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id FROM chat_installations WHERE provider = $1 AND external_workspace_id = $2`,
			provider, workspace).Scan(&org)
	})
	if db.IsNotFound(err) {
		return "", nil
	}
	return org, err
}

// loadInstallation is the organisation's installation of provider; false without one.
func loadInstallation(ctx context.Context, tx pgx.Tx, provider string) (Installation, bool, error) {
	var in Installation
	err := tx.QueryRow(ctx, `SELECT id, organization_id, provider, external_workspace_id, workspace_name, workspace_domain,
			bot_user_id, bot_token, scopes, COALESCE(installed_by, '')
		FROM chat_installations WHERE provider = $1`, provider).
		Scan(&in.ID, &in.OrganizationID, &in.Provider, &in.WorkspaceID, &in.WorkspaceName, &in.WorkspaceDomain,
			&in.BotUserID, &in.Token, &in.Scopes, &in.InstalledBy)
	if db.IsNotFound(err) {
		return in, false, nil
	}
	return in, err == nil, err
}

// noteFailure counts a failed call to the provider for the installation's health.
func (s *Service) noteFailure(ctx context.Context, org, provider string, failure error) {
	if failure == nil {
		return
	}
	s.log().Warn("chat provider call failed", "organization", org, "provider", provider, "error", failure)
	_ = s.DB.InOrg(context.WithoutCancel(ctx), org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE chat_installations SET last_failure = $2,
			failed_today = CASE WHEN failures_day = current_date THEN failed_today + 1 ELSE 1 END, failures_day = current_date
			WHERE provider = $1`, provider, clip(failure.Error(), 500))
		return err
	})
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Begin is an admin's install or a member's link started from dude: the
// provider's authorization URL to send their browser to, carrying a
// one-time state bound to that browser (browserHash, a cookie's sha256).
type Begin struct {
	Provider, Person, BrowserHash, Redirect string
	Admin                                   bool
	// A member's link begun from the private Connect button: its state.
	Prompt string
}

// BeginInstall is an admin starting to install provider's app.
func (s *Service) BeginInstall(ctx context.Context, org string, b Begin) (string, error) {
	p, err := s.provider(b.Provider)
	if err != nil {
		return "", err
	}
	if !b.Admin {
		return "", refuse(403, "forbidden", "only an organisation admin installs a chat provider")
	}
	if b.Person == "" || b.BrowserHash == "" || b.Redirect == "" {
		return "", refuse(400, "bad_request", "an install names who, the browser and the redirect")
	}
	state := newState()
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM chat_connect_states WHERE expires_at <= now()`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO chat_connect_states (state_hash, organization_id, kind, provider, person_id, browser_hash,
			redirect_uri, expires_at) VALUES ($1, $2, 'install', $3, $4, $5, $6, $7)`,
			hashState(state), org, b.Provider, b.Person, b.BrowserHash, b.Redirect, s.now().Add(stateTTL))
		return err
	}); err != nil {
		return "", err
	}
	return p.InstallURL(state, b.Redirect), nil
}

// Callback is the provider sending a browser back: the state, and a code
// or an error, and the browser's cookie hash.
type Callback struct {
	Provider, State, Code, Error, BrowserHash string
}

// Landing is where a callback sends the browser: the outcome, and the
// organisation's slug and what the state was begun from.
type Landing struct {
	Outcome, Slug, ReturnTo string
}

// consumeState deletes the state of kind and returns it, in the
// organisation it names; ok false when there is none, it expired, or
// another browser began it.
func (s *Service) consumeState(ctx context.Context, kind string, c Callback) (st connectState, ok bool, err error) {
	var org string
	var expired bool
	err = s.DB.InSystem(ctx, "chat-callback", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id, expired FROM chat_connect_state_org($1)`, hashState(c.State)).Scan(&org, &expired)
	})
	if db.IsNotFound(err) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `DELETE FROM chat_connect_states WHERE state_hash = $1 AND kind = $2 AND provider = $3
			RETURNING organization_id, COALESCE(person_id, ''), COALESCE(browser_hash, ''), COALESCE(redirect_uri, ''), return_to,
				pending_mention, COALESCE(external_user_id, ''), expires_at, (SELECT slug FROM organizations WHERE id = organization_id)`,
			hashState(c.State), kind, c.Provider).
			Scan(&st.Org, &st.Person, &st.BrowserHash, &st.Redirect, &st.ReturnTo, &st.Pending, &st.ExternalUser, &st.ExpiresAt, &st.Slug)
	})
	if db.IsNotFound(err) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if !st.ExpiresAt.After(s.now()) || st.BrowserHash == "" || st.BrowserHash != c.BrowserHash {
		return st, false, nil
	}
	return st, true, nil
}

type connectState struct {
	Org, Person, BrowserHash, Redirect, ReturnTo, ExternalUser, Slug string
	Pending                                                          []byte
	ExpiresAt                                                        time.Time
}

// CompleteInstall finishes an admin's install: the provider's token for
// the workspace, bound to this organisation, unless another organisation
// holds that workspace (workspace_taken). Installing a different workspace
// replaces this organisation's.
func (s *Service) CompleteInstall(ctx context.Context, c Callback) (Landing, error) {
	p, err := s.provider(c.Provider)
	if err != nil {
		return Landing{}, err
	}
	st, ok, err := s.consumeState(ctx, "install", c)
	if err != nil {
		return Landing{}, err
	}
	land := Landing{Slug: st.Slug, ReturnTo: "install"}
	if !ok {
		land.Outcome = "invalid_or_expired"
		return land, nil
	}
	if c.Error != "" || c.Code == "" {
		land.Outcome = "authorization_denied"
		return land, nil
	}
	inst, err := p.CompleteInstall(ctx, c.Code, st.Redirect)
	if err != nil {
		s.log().Warn("chat install failed", "provider", c.Provider, "error", err)
		land.Outcome = "failed"
		return land, nil
	}
	holder, err := s.orgOf(ctx, c.Provider, inst.WorkspaceID)
	if err != nil {
		return land, err
	}
	if holder != "" && holder != st.Org {
		land.Outcome = "workspace_taken"
		return land, nil
	}
	err = s.DB.InOrg(ctx, st.Org, func(tx pgx.Tx) error {
		// Still an admin: one demoted since the state began installs nothing.
		var admin bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM people WHERE id = $1 AND role = 'admin' AND removed_at IS NULL)`,
			st.Person).Scan(&admin); err != nil || !admin {
			if err == nil {
				err = refuse(403, "forbidden", "only an organisation admin installs a chat provider")
			}
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM chat_installations WHERE provider = $1 AND external_workspace_id <> $2`,
			c.Provider, inst.WorkspaceID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO chat_installations (id, organization_id, provider, external_workspace_id, workspace_name,
				workspace_domain, bot_user_id, bot_token, scopes, installed_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (organization_id, provider) DO UPDATE SET workspace_name = EXCLUDED.workspace_name,
				workspace_domain = EXCLUDED.workspace_domain, bot_user_id = EXCLUDED.bot_user_id, bot_token = EXCLUDED.bot_token,
				scopes = EXCLUDED.scopes, installed_by = EXCLUDED.installed_by, installed_at = now(),
				failed_today = 0, failures_day = NULL, last_failure = NULL`,
			ids.New(ids.ChatInstallation), st.Org, c.Provider, inst.WorkspaceID, inst.WorkspaceName, inst.WorkspaceDomain,
			inst.BotUserID, inst.Token, db.NonNil(inst.Scopes), st.Person)
		if err != nil {
			return err
		}
		return chatLedger(ctx, tx, st.Org, EvChatInstalled, "person", st.Person, map[string]any{"provider": c.Provider,
			"workspaceId": inst.WorkspaceID, "workspaceName": inst.WorkspaceName})
	})
	var pgErr *pgconn.PgError
	var ref *Refusal
	switch {
	case errors.As(err, &pgErr) && pgErr.ConstraintName == "chat_installations_provider_external_workspace_id_key":
		land.Outcome = "workspace_taken"
		return land, nil
	case errors.As(err, &ref):
		land.Outcome = "authorization_denied"
		return land, nil
	case err != nil:
		return land, err
	}
	land.Outcome = "installed"
	return land, nil
}

// Disconnect removes the organisation's installation of provider and
// revokes its token at the provider (best effort: the rows go whatever
// the provider says), with everything bound to the workspace.
func (s *Service) Disconnect(ctx context.Context, org, providerID, person string, admin bool) error {
	p, err := s.provider(providerID)
	if err != nil {
		return err
	}
	if !admin {
		return refuse(403, "forbidden", "only an organisation admin disconnects a chat provider")
	}
	var inst Installation
	var ok bool
	if err := s.DB.InOrg(ctx, org, func(tx pgx.Tx) (err error) {
		if inst, ok, err = loadInstallation(ctx, tx, providerID); err != nil || !ok {
			return err
		}
		return removeInstallationTx(ctx, tx, org, inst, person, "disconnected")
	}); err != nil {
		return err
	}
	if !ok {
		return refuse(404, "not_found", "%s is not installed", p.Info().Name)
	}
	if err := p.Revoke(ctx, inst); err != nil {
		s.log().Warn("revoking a chat installation's token failed", "provider", providerID, "error", err)
	}
	return nil
}

// removeInstallation is the workspace taking the app out (uninstalled, or
// its bot token revoked): nothing it issued works now.
func (s *Service) removeInstallation(ctx context.Context, org, provider, person, reason string) error {
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		inst, ok, err := loadInstallation(ctx, tx, provider)
		if err != nil || !ok {
			return err
		}
		return removeInstallationTx(ctx, tx, org, inst, person, reason)
	})
}

// removeInstallationTx deletes the installation and what is keyed on its
// workspace: people's links, Connect states and thread bindings, so the
// workspace may be installed by another organisation with nothing of this
// one's in its way. Sessions, tasks and epics keep their origin; Requests
// keep their history.
func removeInstallationTx(ctx context.Context, tx pgx.Tx, org string, inst Installation, person, reason string) error {
	for _, q := range []string{
		`DELETE FROM chat_identities WHERE provider = $1 AND external_workspace_id = $2`,
		`DELETE FROM chat_threads WHERE provider = $1 AND external_workspace_id = $2`,
		`DELETE FROM chat_connect_states WHERE provider = $1 AND external_workspace_id = $2`,
		`DELETE FROM chat_installations WHERE provider = $1 AND external_workspace_id = $2`,
	} {
		if _, err := tx.Exec(ctx, q, inst.Provider, inst.WorkspaceID); err != nil {
			return err
		}
	}
	actorType := "system"
	if person != "" {
		actorType = "person"
	}
	return chatLedger(ctx, tx, org, EvChatUninstalled, actorType, person, map[string]any{"provider": inst.Provider,
		"workspaceId": inst.WorkspaceID, "workspaceName": inst.WorkspaceName, "reason": reason})
}

// BeginConnect is a member starting to link their provider account: from
// Your settings, or from the connect page the private Connect button
// opened (b.Prompt), whose kept mention follows the state.
func (s *Service) BeginConnect(ctx context.Context, org string, b Begin) (string, error) {
	p, err := s.provider(b.Provider)
	if err != nil {
		return "", err
	}
	if b.Person == "" || b.BrowserHash == "" || b.Redirect == "" {
		return "", refuse(400, "bad_request", "a connect names who, the browser and the redirect")
	}
	state := newState()
	var workspace string
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		inst, ok, err := loadInstallation(ctx, tx, b.Provider)
		if err != nil {
			return err
		}
		if !ok {
			return refuse(409, "workspace_not_installed", "%s is not installed in this organisation", p.Info().Name)
		}
		workspace = inst.WorkspaceID
		var pending []byte
		var external *string
		returnTo := "settings"
		if b.Prompt != "" {
			var expires time.Time
			err := tx.QueryRow(ctx, `DELETE FROM chat_connect_states WHERE state_hash = $1 AND kind = 'prompt' AND provider = $2
				RETURNING pending_mention, external_user_id, expires_at`, hashState(b.Prompt), b.Provider).Scan(&pending, &external, &expires)
			if db.IsNotFound(err) || err == nil && !expires.After(s.now()) {
				return refuse(410, "invalid_or_expired", "that Connect button has expired; mention dude again for a new one")
			}
			if err != nil {
				return err
			}
			returnTo = "connect"
		}
		if _, err := tx.Exec(ctx, `DELETE FROM chat_connect_states WHERE expires_at <= now()`); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO chat_connect_states (state_hash, organization_id, kind, provider, external_workspace_id,
				external_user_id, person_id, return_to, pending_mention, browser_hash, redirect_uri, expires_at)
			VALUES ($1, $2, 'oauth', $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			hashState(state), org, b.Provider, workspace, external, b.Person, returnTo, pending, b.BrowserHash, b.Redirect, s.now().Add(stateTTL))
		return err
	})
	if err != nil {
		return "", err
	}
	return p.IdentityURL(state, b.Redirect, workspace), nil
}

// CompleteConnect finishes a member's link: who signed in at the provider,
// linked to the account of the person who began it, once — a provider
// account already linked to another account, or this account to another
// provider account, is already_linked. A mention kept for the Connect is
// then routed, if it was this provider account's.
func (s *Service) CompleteConnect(ctx context.Context, c Callback) (Landing, error) {
	p, err := s.provider(c.Provider)
	if err != nil {
		return Landing{}, err
	}
	st, ok, err := s.consumeState(ctx, "oauth", c)
	if err != nil {
		return Landing{}, err
	}
	land := Landing{Slug: st.Slug, ReturnTo: st.ReturnTo}
	if !ok {
		land.Outcome = "invalid_or_expired"
		return land, nil
	}
	if c.Error != "" || c.Code == "" {
		land.Outcome = "authorization_denied"
		return land, nil
	}
	who, err := p.CompleteIdentity(ctx, c.Code, st.Redirect)
	if err != nil {
		s.log().Warn("chat sign-in failed", "provider", c.Provider, "error", err)
		land.Outcome = "failed"
		return land, nil
	}
	err = s.DB.InOrg(ctx, st.Org, func(tx pgx.Tx) error {
		inst, ok, err := loadInstallation(ctx, tx, c.Provider)
		if err != nil {
			return err
		}
		if !ok || inst.WorkspaceID != who.WorkspaceID {
			land.Outcome = "workspace_not_installed"
			return nil
		}
		var account string
		if err := tx.QueryRow(ctx, `SELECT account_id FROM people WHERE id = $1 AND removed_at IS NULL AND account_id IS NOT NULL`,
			st.Person).Scan(&account); db.IsNotFound(err) {
			land.Outcome = "failed"
			return nil
		} else if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO chat_identities (organization_id, provider, external_workspace_id, external_user_id,
				account_id, person_id, handle, display_name)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING`,
			st.Org, c.Provider, who.WorkspaceID, who.ID, account, st.Person, who.Handle, who.DisplayName)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var same bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM chat_identities WHERE provider = $1 AND external_workspace_id = $2
				AND external_user_id = $3 AND account_id = $4)`, c.Provider, who.WorkspaceID, who.ID, account).Scan(&same); err != nil {
				return err
			}
			if !same {
				land.Outcome = "already_linked"
				return nil
			}
		} else if err := chatLedger(ctx, tx, st.Org, EvChatLinked, "person", st.Person, map[string]any{"provider": c.Provider,
			"personId": st.Person, "handle": who.Handle}); err != nil {
			return err
		}
		land.Outcome = "connected"
		if len(st.Pending) == 0 || st.ExternalUser != who.ID {
			return nil
		}
		// The kept mention, routed by the chat loop as if it came now, in
		// order with its thread's later mentions.
		key := ""
		var kept Mention
		if json.Unmarshal(st.Pending, &kept) == nil {
			key = threadKey(c.Provider, kept.WorkspaceID, kept.ConversationID, kept.ThreadID)
		}
		_, err = tx.Exec(ctx, `INSERT INTO chat_events (provider, event_id, organization_id, kind, payload, thread_key)
			VALUES ($1, $2, $3, 'mention', $4, $5) ON CONFLICT DO NOTHING`, c.Provider, "connect:"+hashState(c.State), st.Org, st.Pending, key)
		return err
	})
	if err != nil {
		return land, err
	}
	s.kick()
	return land, nil
}

// Unlink removes the person's own link to provider.
func (s *Service) Unlink(ctx context.Context, org, providerID, person string) error {
	if _, err := s.provider(providerID); err != nil {
		return err
	}
	return s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM chat_identities WHERE provider = $1 AND account_id =
			(SELECT account_id FROM people WHERE id = $2)`, providerID, person)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return chatLedger(ctx, tx, org, EvChatUnlinked, "person", person, map[string]any{"provider": providerID,
			"personId": person, "reason": "unlinked"})
	})
}

// ConnectState is what the connect page shows for a prompt's state before
// the person signs in at the provider: ready, connected already, expired,
// or a state of another organisation (the person is signed in elsewhere).
func (s *Service) ConnectState(ctx context.Context, org, providerID, person, state string) (map[string]any, error) {
	p, err := s.provider(providerID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"provider": p.Info(), "workspaceName": nil, "workspaceDomain": nil, "connected": nil, "backUrl": nil, "asker": nil}
	var stateOrg string
	var expired bool
	err = s.DB.InSystem(ctx, "chat-connect-state", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id, expired FROM chat_connect_state_org($1)`, hashState(state)).Scan(&stateOrg, &expired)
	})
	switch {
	case db.IsNotFound(err):
		expired = true
	case err != nil:
		return nil, err
	case stateOrg != org:
		out["status"] = "wrong_organization"
		return out, nil
	}
	var inst Installation
	var asker string
	err = s.DB.InOrg(ctx, org, func(tx pgx.Tx) error {
		var ok bool
		var err error
		inst, ok, err = loadInstallation(ctx, tx, providerID)
		if err != nil || !ok {
			return err
		}
		out["workspaceName"] = inst.WorkspaceName
		if inst.WorkspaceDomain != "" {
			out["workspaceDomain"] = inst.WorkspaceDomain
		}
		// Where "Back to <provider>" goes: the message that offered the
		// button; and who wrote it, the account the sign-in is to link.
		var pending []byte
		if err := tx.QueryRow(ctx, `SELECT pending_mention FROM chat_connect_states WHERE state_hash = $1 AND kind = 'prompt'`,
			hashState(state)).Scan(&pending); err == nil && len(pending) > 0 {
			var m Mention
			if json.Unmarshal(pending, &m) == nil {
				if link := p.Permalink(inst, m.ConversationID, m.MessageID, m.ThreadID); link != "" {
					out["backUrl"] = link
				}
				asker = m.UserID
			}
		} else if err != nil && !db.IsNotFound(err) {
			return err
		}
		var handle, name string
		err = tx.QueryRow(ctx, `SELECT i.handle, i.display_name FROM chat_identities i JOIN people me ON me.account_id = i.account_id
			WHERE me.id = $1 AND i.provider = $2 AND i.external_workspace_id = $3`, person, providerID, inst.WorkspaceID).Scan(&handle, &name)
		if err == nil {
			out["connected"] = map[string]string{"handle": handle, "displayName": name}
		} else if !db.IsNotFound(err) {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	switch {
	case out["connected"] != nil:
		out["status"] = "connected"
	case expired:
		out["status"] = "expired"
	default:
		out["status"] = "ready"
		// The account the prompt was sent to, as the provider names it; its
		// id alone when the provider cannot be read now.
		if asker != "" {
			who, err := p.User(ctx, inst, asker)
			if err != nil {
				s.noteFailure(ctx, org, providerID, err)
				who = ExternalUser{ID: asker}
			}
			out["asker"] = map[string]string{"id": asker, "handle": firstNonEmpty(who.Handle, asker), "displayName": who.DisplayName}
		}
	}
	return out, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// trimmed is s with surrounding space removed and inner runs collapsed.
func trimmed(s string) string { return strings.Join(strings.Fields(s), " ") }

package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/marciomartins/dude/orchestrator/internal/db"
	"github.com/marciomartins/dude/orchestrator/internal/ids"
)

// OwnerAuthorizationError means the frozen task owner must connect GitHub.
type OwnerAuthorizationError struct{}

func (OwnerAuthorizationError) Error() string {
	return "The task owner needs Connect GitHub in their profile before this pull request can be opened"
}

// OwnerToken never changes installation credentials or the shared App cache.
func OwnerToken(ctx context.Context, database *db.DB, org, person string) (string, error) {
	reconnect := OwnerAuthorizationError{}
	for {
		var token, refresh, clientID, clientSecret, credential, claim string
		var version int64
		var expires, refreshExpires time.Time
		var required bool
		err := database.InOrg(ctx, org, func(tx pgx.Tx) error {
			err := tx.QueryRow(ctx, `SELECT a.access_token, a.refresh_token, a.expires_at, a.refresh_expires_at,
     a.reconnect_required, c.oauth_client_id, c.oauth_client_secret, c.id, c.oauth_version
     FROM github_user_authorizations a JOIN forge_credentials c ON c.id = a.credential_id
     JOIN people p ON p.id = a.person_id AND p.organization_id = a.organization_id AND p.removed_at IS NULL
     WHERE a.person_id = $1 AND a.credential_version = c.oauth_version AND c.auth = 'github_app'`, person).
				Scan(&token, &refresh, &expires, &refreshExpires, &required, &clientID, &clientSecret, &credential, &version)
			if err != nil {
				return err
			}
			if required || !refreshExpires.After(time.Now()) {
				return reconnect
			}
			if expires.After(time.Now().Add(time.Minute)) {
				return nil
			}
			claim = ids.New("oauth")
			tag, err := tx.Exec(ctx, `UPDATE github_user_authorizations SET refresh_claim = $1, refresh_claim_until = now() + interval '30 seconds'
     WHERE person_id = $2 AND credential_id = $3 AND credential_version = $4
       				AND expires_at <= now() + interval '1 minute'
				AND (refresh_claim_until IS NULL OR refresh_claim_until <= now())`, claim, person, credential, version)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				claim = ""
			}
			return nil
		})
		if db.IsNotFound(err) {
			return "", reconnect
		}
		if err != nil {
			return "", err
		}
		if expires.After(time.Now().Add(time.Minute)) {
			return token, nil
		}
		if claim == "" {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		rotated, exchangeErr := refreshUserToken(ctx, clientID, clientSecret, refresh)
		invalid := exchangeErr == reconnectGrant
		retry := false
		persistErr := database.InOrg(ctx, org, func(tx pgx.Tx) error {
			if exchangeErr != nil {
				_, err := tx.Exec(ctx, `UPDATE github_user_authorizations SET reconnect_required = $1,
      refresh_claim = NULL, refresh_claim_until = NULL WHERE person_id = $2 AND credential_id = $3
      AND credential_version = $4 AND refresh_claim = $5`, invalid, person, credential, version, claim)
				return err
			}
			tag, err := tx.Exec(ctx, `UPDATE github_user_authorizations SET access_token = $1, refresh_token = $2,
     expires_at = $3, refresh_expires_at = $4, refresh_claim = NULL, refresh_claim_until = NULL
     WHERE person_id = $5 AND credential_id = $6 AND credential_version = $7 AND refresh_claim = $8
       AND reconnect_required = false`, rotated.Access, rotated.Refresh, rotated.Expires, rotated.RefreshExpires,
				person, credential, version, claim)
			if err == nil && tag.RowsAffected() == 0 {
				retry = true
			}
			return err
		})
		if persistErr != nil {
			return "", persistErr
		}
		if retry {
			continue
		}
		if invalid {
			return "", reconnect
		}
		if exchangeErr != nil {
			if expires.After(time.Now()) {
				return token, nil
			}
			return "", exchangeErr
		}
		return rotated.Access, nil
	}
}

var reconnectGrant = fmt.Errorf("GitHub authorization requires reconnect")

type userTokens struct {
	Access, Refresh         string
	Expires, RefreshExpires time.Time
}

func refreshUserToken(ctx context.Context, clientID, secret, refresh string) (userTokens, error) {
	body, _ := json.Marshal(map[string]string{"client_id": clientID, "client_secret": secret, "refresh_token": refresh, "grant_type": "refresh_token"})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(string(body)))
	if err != nil {
		return userTokens{}, fmt.Errorf("GitHub authorization refresh failed")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return userTokens{}, &Error{Status: 503, Message: "GitHub authorization refresh temporarily unavailable"}
	}
	defer res.Body.Close()
	if res.StatusCode >= 500 {
		return userTokens{}, &Error{Status: 503, Message: "GitHub authorization refresh temporarily unavailable"}
	}
	var out struct {
		Access         string `json:"access_token"`
		Refresh        string `json:"refresh_token"`
		Expires        int64  `json:"expires_in"`
		RefreshExpires int64  `json:"refresh_token_expires_in"`
		Error          string `json:"error"`
	}
	if json.NewDecoder(res.Body).Decode(&out) != nil {
		return userTokens{}, &Error{Status: 503, Message: "GitHub authorization refresh returned an invalid response"}
	}
	if out.Error != "" || res.StatusCode != 200 {
		return userTokens{}, reconnectGrant
	}
	if out.Access == "" || out.Refresh == "" || out.Expires <= 0 || out.RefreshExpires <= 0 {
		return userTokens{}, reconnectGrant
	}
	now := time.Now()
	return userTokens{out.Access, out.Refresh, now.Add(time.Duration(out.Expires) * time.Second), now.Add(time.Duration(out.RefreshExpires) * time.Second)}, nil
}

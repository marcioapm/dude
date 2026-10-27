package chat

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// Ledger events of an organisation's chat providers (packages/domain's
// EventTypes names them too).
const (
	// An admin installed a provider's app in a workspace. Payload:
	// { provider, workspaceId, workspaceName }.
	EvChatInstalled = "chat_provider.installed"
	// The installation went: an admin disconnected it, or the workspace
	// removed the app or revoked its token. Payload: { provider,
	// workspaceId, workspaceName, reason }.
	EvChatUninstalled = "chat_provider.removed"
	// A person linked their provider account. Payload: { provider, personId, handle }.
	EvChatLinked = "chat_identity.linked"
	// A link went: the person unlinked, or the provider revoked their
	// sign-in. Payload: { provider, personId, reason }.
	EvChatUnlinked = "chat_identity.unlinked"
	// The projects a conversation is usually about changed. Payload:
	// { provider, conversationId, conversationName, added, removed, via }.
	EvChatChannelProjects = "chat_channel.projects_set"
)

// chatLedger appends an organisation-level chat event, by a person or the system.
func chatLedger(ctx context.Context, tx pgx.Tx, org, typ, actorType, actor string, payload map[string]any) error {
	if actorType == "" || actor == "" {
		actorType, actor = ledger.ActorSystem, "chat"
	}
	_, err := ledger.Append(ctx, tx, ledger.Event{Type: typ, OrganizationID: org, ActorType: actorType, ActorID: actor,
		Source: ledger.SourceOrchestrator, Payload: payload})
	return err
}

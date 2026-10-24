// Package chat is dude's side of chat providers (Slack first; Discord or
// Teams later): the neutral types every provider parses into and is called
// with, the Provider interface an adapter implements, and the registry the
// routes resolve {provider} through. Routing, the checks, thread binding,
// session access and the Requests log (route.go, sweep.go) use only what is
// here and know nothing of any one provider.
package chat

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"
)

// Info is a provider as shared surfaces draw it: its name, its glyph's id
// in the web's icon set, and what it calls a workspace.
type Info struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Glyph         string `json:"glyph"`
	WorkspaceWord string `json:"workspaceWord"`
}

// Conversation kinds: where a mention was made, which decides who gets a
// session made from it.
const (
	KindPublic  = "public"
	KindPrivate = "private"
	KindGroup   = "group"
	KindDM      = "dm"
)

// Installation is one workspace's install of a provider's app, with the
// credential its calls use. Token never leaves the orchestrator.
type Installation struct {
	ID, OrganizationID, Provider string
	WorkspaceID, WorkspaceName   string
	WorkspaceDomain, BotUserID   string
	Token                        string
	Scopes                       []string
	InstalledBy                  string
}

// ExternalUser is a person's account at the provider.
type ExternalUser struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	Handle      string `json:"handle,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Bot         bool   `json:"bot,omitempty"`
}

// Name is how the person reads in dude: their display name, else handle, else id.
func (u ExternalUser) Name() string {
	switch {
	case u.DisplayName != "":
		return u.DisplayName
	case u.Handle != "":
		return u.Handle
	}
	return u.ID
}

// Mention is a message addressed to dude, as every provider parses it:
// the bot's handle stripped from Text. ThreadID is the thread's root
// message (the mention itself when it starts one). Stored as JSON when a
// mention waits on its author connecting.
type Mention struct {
	Provider       string `json:"provider"`
	WorkspaceID    string `json:"workspaceId"`
	EventID        string `json:"eventId"`
	ConversationID string `json:"conversationId"`
	ThreadID       string `json:"threadId"`
	MessageID      string `json:"messageId"`
	UserID         string `json:"userId"`
	Text           string `json:"text"`
	// A direct message to dude: never bound, a forwarded thread routes afresh.
	Direct bool `json:"direct,omitempty"`
}

// Event kinds a delivery parses into.
const (
	EventMention         = "mention"
	EventUninstalled     = "uninstalled"
	EventTokensRevoked   = "tokens_revoked"
	EventURLVerification = "url_verification"
	EventIgnored         = "ignored"
)

// Event is one provider delivery, parsed.
type Event struct {
	Kind        string
	ID          string
	WorkspaceID string
	// URL verification's challenge, answered as it came.
	Challenge string
	Mention   *Mention
	// tokens_revoked: the bot users and the people whose tokens went.
	RevokedBots, RevokedUsers []string
}

// Interaction is a click on a button dude posted: a clarifying question's
// choice. Value is what dude put on the button.
type Interaction struct {
	ID             string       `json:"id"`
	WorkspaceID    string       `json:"workspaceId"`
	ConversationID string       `json:"conversationId"`
	ThreadID       string       `json:"threadId"`
	MessageID      string       `json:"messageId"`
	User           ExternalUser `json:"user"`
	Value          string       `json:"value"`
}

// ConversationInfo is where a conversation is and what it is called.
type ConversationInfo struct {
	Kind string
	// As people read it: "#support", "a group message", "a direct message".
	Name string
}

// ThreadMessage is one message of a thread, oldest first.
type ThreadMessage struct {
	ID     string
	UserID string
	Text   string
	Bot    bool
}

// Reply is what dude posts: words, and optionally a link with its label
// (a button where the provider has them).
type Reply struct {
	Text      string
	LinkURL   string
	LinkLabel string
}

// Choice is one button of a question; Value comes back in the Interaction.
type Choice struct {
	Label string
	Value string
}

// Question is a clarifying question with 2–4 choices.
type Question struct {
	Text    string
	Choices []Choice
}

// Reactions dude puts on a mention: working on it, its session's agent
// booting, done, failed; cleared takes the working or booting mark off and
// puts nothing (a private refusal says nothing in public). Booting, done,
// failed and cleared each replace the mark before them.
type Reaction int

const (
	ReactionWorking Reaction = iota
	ReactionDone
	ReactionFailed
	ReactionCleared
	ReactionBooting
)

// ErrUnverified is a delivery whose signature or timestamp does not hold.
var ErrUnverified = errors.New("the request is not signed by the provider")

// TransientError is a failure worth trying again later: the provider
// unreachable, slow, answering 5xx, or rate-limiting (RetryAfter, its
// Retry-After). Anything else a provider answers is deterministic.
type TransientError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *TransientError) Error() string { return e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }

// Transient marks err as worth trying again, no sooner than after.
func Transient(err error, after time.Duration) error {
	return &TransientError{Err: err, RetryAfter: after}
}

// Provider is everything provider-specific. Shared code sees only the
// types above.
type Provider interface {
	Info() Info

	// Webhooks. RequestHeaders are the headers VerifyRequest and the
	// parsers read: the only ones of a delivery passed to them.
	RequestHeaders() []string
	VerifyRequest(h http.Header, body []byte, now time.Time) error
	ParseEvent(body []byte) (Event, error)
	ParseInteraction(body []byte) (Interaction, error)

	// Reading the conversation.
	FetchThread(ctx context.Context, inst Installation, conversation, thread, since string) ([]ThreadMessage, error)
	Conversation(ctx context.Context, inst Installation, conversation string) (ConversationInfo, error)
	ListMembers(ctx context.Context, inst Installation, conversation string) ([]string, error)
	User(ctx context.Context, inst Installation, userID string) (ExternalUser, error)
	Permalink(inst Installation, conversation, message, thread string) string

	// Writing back.
	PostReply(ctx context.Context, inst Installation, conversation, thread string, r Reply) (string, error)
	PostEphemeral(ctx context.Context, inst Installation, conversation, thread, user string, r Reply) error
	PostQuestion(ctx context.Context, inst Installation, conversation, thread string, q Question) (string, error)
	UpdateQuestion(ctx context.Context, inst Installation, conversation, message string, chosenBy ExternalUser, label string) error
	React(ctx context.Context, inst Installation, conversation, message string, r Reaction) error

	// OAuth: an admin installs the app; a member signs in to link.
	InstallURL(state, redirect string) string
	CompleteInstall(ctx context.Context, code, redirect string) (Installation, error)
	IdentityURL(state, redirect, workspace string) string
	CompleteIdentity(ctx context.Context, code, redirect string) (ExternalUser, error)
	Revoke(ctx context.Context, inst Installation) error
}

// Registry is the providers dude has, by id.
type Registry map[string]Provider

// Get is the provider id names; false for one dude does not have.
func (r Registry) Get(id string) (Provider, bool) {
	p, ok := r[id]
	return p, ok
}

// Infos are the registered providers, by id.
func (r Registry) Infos() []Info {
	out := make([]Info, 0, len(r))
	for _, p := range r {
		out = append(out, p.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

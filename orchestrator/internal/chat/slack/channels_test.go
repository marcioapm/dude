package slack_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/chat"
	"github.com/marciomartins/dude/orchestrator/internal/chat/slack"
	"github.com/marciomartins/dude/orchestrator/internal/fakeslack"
)

// channelWorkspace is a fake workspace of n public channels c000…, the bot
// in each, served pageSize to a conversations.list page.
func channelWorkspace(t *testing.T, n, pageSize int) (*fakeslack.Server, *slack.Provider, chat.Installation) {
	t.Helper()
	fake := fakeslack.New("cid", "csecret")
	fake.AddTeam("T1", "Acme")
	for i := range n {
		fake.AddConversation(fakeslack.Conversation{ID: fmt.Sprintf("C%08d", i), Team: "T1", Name: fmt.Sprintf("c%03d", i), Kind: "public",
			Members: []string{"UBT1"}})
	}
	fake.AddConversation(fakeslack.Conversation{ID: "C0RANDOMXX", Team: "T1", Name: "random", Kind: "public"})
	srv := httptest.NewServer(fake.WithChannelPages(pageSize))
	t.Cleanup(srv.Close)
	p := slack.New(slack.Config{ClientID: "cid", ClientSecret: "csecret", APIURL: srv.URL + "/api"})
	return fake, p, chat.Installation{ID: "chi_" + t.Name(), WorkspaceID: "T1", Token: fake.Token("T1")}
}

func TestFindChannelWalksConversationsListPages(t *testing.T) {
	fake, p, inst := channelWorkspace(t, 25, 10)
	id, member, found, err := p.FindChannel(context.Background(), inst, "#c023")
	if err != nil || !found || !member || id != "C00000023" {
		t.Fatalf("c023, on the third page: %q %v %v %v", id, member, found, err)
	}
	if n := fake.Calls("conversations.list"); n != 3 {
		t.Errorf("%d pages read, want 3", n)
	}
	if _, _, found, err := p.FindChannel(context.Background(), inst, "nowhere"); err != nil || found {
		t.Errorf("a name on no page: found=%v %v", found, err)
	}
}

// Past the page bound (50 pages) the lookup says it could not finish, never
// that the channel does not exist.
func TestFindChannelAtThePageBoundSaysItCouldNotFinish(t *testing.T) {
	fake, p, inst := channelWorkspace(t, 60, 1)
	_, _, found, err := p.FindChannel(context.Background(), inst, "c055")
	if !errors.Is(err, chat.ErrChannelLookupCut) || found {
		t.Fatalf("c055, on page 56: found=%v %v", found, err)
	}
	if n := fake.Calls("conversations.list"); n != 50 {
		t.Errorf("%d pages read, want 50", n)
	}
	if _, _, found, err := p.FindChannel(context.Background(), inst, "c049"); err != nil || !found {
		t.Errorf("c049, on page 50: found=%v %v", found, err)
	}
}

// A channel the bot is in is not looked up again for a while; one it is not
// in is (it may be invited meanwhile).
func TestFindChannelRemembersAChannelTheBotIsIn(t *testing.T) {
	fake, p, inst := channelWorkspace(t, 3, 0)
	for range 3 {
		if id, _, _, err := p.FindChannel(context.Background(), inst, "c002"); err != nil || id != "C00000002" {
			t.Fatalf("c002: %q %v", id, err)
		}
	}
	if n := fake.Calls("conversations.list"); n != 1 {
		t.Errorf("%d lookups of a channel the bot is in, want 1", n)
	}
	for range 2 {
		if _, member, _, err := p.FindChannel(context.Background(), inst, "random"); err != nil || member {
			t.Fatalf("random: member=%v %v", member, err)
		}
	}
	if n := fake.Calls("conversations.list"); n != 3 {
		t.Errorf("%d lookups in all, want 3", n)
	}
	// Forgotten (a post there was refused), it is looked up again.
	p.ForgetChannel(inst, "#C002")
	if _, _, _, err := p.FindChannel(context.Background(), inst, "c002"); err != nil {
		t.Fatal(err)
	}
	if n := fake.Calls("conversations.list"); n != 4 {
		t.Errorf("%d lookups after ForgetChannel, want 4", n)
	}
	// Another installation on the same provider remembers nothing of this one's.
	other := inst
	other.ID = "chi_other"
	if _, _, _, err := p.FindChannel(context.Background(), other, "c002"); err != nil {
		t.Fatal(err)
	}
	if n := fake.Calls("conversations.list"); n != 5 {
		t.Errorf("%d lookups after another installation's, want 5", n)
	}
}

func TestFindChannelPassesSlacksErrorThrough(t *testing.T) {
	fake, p, inst := channelWorkspace(t, 3, 0)
	fake.FailNext("conversations.list", "missing_scope")
	var apiErr *slack.APIError
	if _, _, _, err := p.FindChannel(context.Background(), inst, "c001"); !errors.As(err, &apiErr) || apiErr.Code != "missing_scope" {
		t.Fatalf("missing_scope: %v", err)
	}
}

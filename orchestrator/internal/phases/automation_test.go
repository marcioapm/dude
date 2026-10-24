package phases

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http/httptest"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// Q5: an organisation's automation agent acts as the GitHub App: its
// commits carry the App's bot identity and it checks out with the App's
// installation token. With a personal token there is no bot identity.
func TestAutomationAgentActsAsTheApp(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fakeApp := fakegithub.NewApp(9103, "acme-dude", &key.PublicKey)
	fakeApp.Installations["acme"] = 71
	fake := httptest.NewServer(fakeApp.Handler())
	defer fake.Close()
	block := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	app := forge.NewGitHub(forge.Credential{Auth: "github_app", Secret: string(block), AppID: "9103", APIBaseURL: fake.URL})
	pat := forge.NewGitHub(forge.Credential{Auth: "pat", Secret: "ghp_person"})

	in := AutomationSpecInput{OrganizationID: "org_acme", AutomationID: "aut_triage", RunID: "arn_1", Kind: "agent",
		Role: "agent", Harness: "opencode", Model: "fake/scripted", Prompt: "Triage",
		Repos: []AutomationRepo{{Name: "web", URL: "https://github.com/acme/web.git", Ref: "main"}}}
	cfg := AgentConfig{DefaultImage: "agent:1"}
	secret := func(spec lux.Spec, name string) string {
		for _, s := range spec.Secrets {
			if s.Name == name {
				return s.Value
			}
		}
		return ""
	}

	spec, err := AutomationSpec(context.Background(), cfg, nil, app, in)
	if err != nil {
		t.Fatal(err)
	}
	const bot = "acme-dude[bot]"
	if spec.Env["GIT_AUTHOR_NAME"] != bot || spec.Env["GIT_COMMITTER_NAME"] != bot ||
		spec.Env["GIT_AUTHOR_EMAIL"] != "4242+acme-dude[bot]@users.noreply.github.com" {
		t.Errorf("as the App: env %v", spec.Env)
	}
	token := secret(spec, gitTokenSecret)
	if token == "" || fakeApp.Installation(token) != 71 {
		t.Errorf("as the App: GIT_TOKEN %q is not acme's installation token", token)
	}

	spec, err = AutomationSpec(context.Background(), cfg, nil, pat, in)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := spec.Env["GIT_AUTHOR_NAME"]; ok {
		t.Errorf("with a personal token: a bot author %q", name)
	}
	if got := secret(spec, gitTokenSecret); got != "ghp_person" {
		t.Errorf("with a personal token: GIT_TOKEN %q", got)
	}
}

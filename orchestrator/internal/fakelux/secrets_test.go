package fakelux_test

// The fake's secrets against lux v0.1.11's contract, through dude's real
// lux client: the stored spec it answers with, and what reaches a
// server's process and its log.

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// lux answers a submit, and a repeat of its idempotency key, with the
// spec it stored from the first: each secret's name and normalized as, no
// value, a credential runnerOnly. A repeat with another spec changes
// nothing of it.
func TestASubmitAnswersWithTheFirstSubmitsStoredSecrets(t *testing.T) {
	fake := fakelux.New(t.TempDir(), "k", nil)
	_, c, _ := startedWith(t, fake, preview)
	ctx := context.Background()
	first := preview
	first.Secrets = []lux.Secret{{Name: "GIT_TOKEN", Value: "token"}, {Name: "ORIGINAL_KEY", Value: "original", As: "env"}}
	first.Git = &lux.Git{Repositories: []lux.Repository{{Name: "app", URL: "https://github.com/acme/app.git", Credential: "GIT_TOKEN"}}}
	want := []lux.StoredSecret{{Name: "GIT_TOKEN", As: "none", RunnerOnly: true}, {Name: "ORIGINAL_KEY", As: "env"}}
	r, err := c.Submit(ctx, first, "preview-key")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Spec.Secrets, want) {
		t.Errorf("submit answered secrets %+v; want %+v", r.Spec.Secrets, want)
	}
	retry := preview
	retry.Secrets = []lux.Secret{{Name: "ADDED_KEY", Value: "added", As: "env"}}
	again, err := c.Submit(ctx, retry, "preview-key")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != r.ID || !slices.Equal(again.Spec.Secrets, want) {
		t.Errorf("repeat answered %s with %+v; want %s with %+v", again.ID, again.Spec.Secrets, r.ID, want)
	}
	got, err := c.Get(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Spec.Secrets, want) {
		t.Errorf("GET answered secrets %+v; want %+v", got.Spec.Secrets, want)
	}
}

// gitFixture is a repository the fake can check out, for a spec whose git
// credential is a secret.
func gitFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "fixture"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v: %s", err, out)
		}
	}
	return repo
}

// A git or registry credential is the runner's alone (lux marks it
// runnerOnly whatever its as): a server's process never has it, even
// declared as: env.
func TestARunnerCredentialNeverReachesAServerEvenAsEnv(t *testing.T) {
	for _, via := range []string{"git", "registry"} {
		t.Run(via, func(t *testing.T) {
			spec := lux.Spec{
				Image:    lux.Image{Ref: "node:22"},
				Workload: lux.Workload{Adapter: "generic", Command: []string{"sleep", "infinity"}},
				Secrets: []lux.Secret{{Name: "CREDENTIAL", Value: "credential-canary", As: "env"},
					{Name: "SEED_LLM_KEY", Value: "seed-value", As: "env"}},
			}
			if via == "registry" {
				spec.Image.RegistryAuth = []lux.RegistryAuth{{Registry: "ghcr.io", Secret: "CREDENTIAL"}}
			} else {
				spec.Git = &lux.Git{Repositories: []lux.Repository{{Name: "target", URL: "https://github.com/acme/target.git", Credential: "CREDENTIAL"}}}
			}
			fake, c, run := startedWith(t, fakelux.New(gitFixture(t), "k", nil), spec)
			if _, err := c.AddServer(context.Background(), run, lux.ServerInput{
				Name: "probe", Port: 4100, Command: []string{"sh", "-c", "npm run probe"},
			}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "probe ready", func() bool { return serverState(c, run, "probe").State == "ready" })
			env := fake.ServerEnv(run, "probe")
			if _, ok := env["CREDENTIAL"]; ok {
				t.Errorf("the runner's credential reached the server: %v", env)
			}
			if env["SEED_LLM_KEY"] != "seed-value" {
				t.Errorf("the server's environment = %v; want SEED_LLM_KEY", env)
			}
		})
	}
}

// lux's shim writes a server's output and its exit's error only through
// its redactor, keeping stdout and stderr apart: a command that prints its
// env secrets on both streams and fails leaves none of them in its log.
func TestAFailingServerCommandsLogHasNoSecretValue(t *testing.T) {
	const seed, pem = "preview-secret-log-canary-8c29e37f", "-----BEGIN KEY-----\nPEMCANARYLINE1\nPEMCANARYLINE2\n-----END KEY-----"
	_, c, run := started(t, lux.Spec{
		Image:    lux.Image{Ref: "node:22"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"sleep", "infinity"}},
		Secrets: []lux.Secret{{Name: "SEED_LLM_KEY", Value: seed, As: "env"},
			{Name: "SIGNING_KEY_PEM", Value: pem, As: "env"}},
	})
	ctx := context.Background()
	if _, err := c.AddServer(ctx, run, lux.ServerInput{
		Name: "probe", Port: 4100,
		Command: []string{"sh", "-c", `printf 'out %s\n' "$SEED_LLM_KEY"; printf 'err %s\n' "$SIGNING_KEY_PEM" >&2; exit 1 # fakelux-run`},
	}); err != nil {
		t.Fatal(err)
	}
	type line struct{ Stream, Text string }
	var lines []line
	var raw json.RawMessage
	waitFor(t, "the failed command's exit in its log", func() bool {
		var err error
		if raw, err = c.ServerLog(ctx, run, "probe", 100); err != nil {
			t.Fatal(err)
		}
		var out struct{ Lines []line }
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		lines = out.Lines
		return slices.ContainsFunc(lines, func(l line) bool { return strings.Contains(l.Text, "exit status 1") })
	})
	for _, v := range []string{seed, "PEMCANARYLINE1", "PEMCANARYLINE2"} {
		if strings.Contains(string(raw), v) {
			t.Errorf("the server's log carries a secret's value: %s", raw)
		}
	}
	has := func(stream, text string) bool {
		return slices.ContainsFunc(lines, func(l line) bool { return l.Stream == stream && strings.Contains(l.Text, text) })
	}
	if !has("stdout", "out [REDACTED:SEED_LLM_KEY]") || !has("stderr", "err [REDACTED:SIGNING_KEY_PEM]") {
		t.Errorf("want each stream's line, redacted, on its own stream: %+v", lines)
	}
}

// A secret printed as a JSON string, by an encoder that escapes HTML and by
// one that does not (lux's shim redacts both forms): neither is in the
// server's log.
func TestAServersLogHasNoSecretInEitherJSONForm(t *testing.T) {
	const secret = "json<canary>&\"7d1e\"\nline two"
	var forms []string
	for _, escapeHTML := range []bool{true, false} {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(escapeHTML)
		if err := enc.Encode(secret); err != nil {
			t.Fatal(err)
		}
		forms = append(forms, strings.TrimSuffix(b.String(), "\n"))
	}
	if forms[0] == forms[1] {
		t.Fatalf("the secret's two JSON forms are the same: %s", forms[0])
	}
	_, c, run := started(t, lux.Spec{
		Image:    lux.Image{Ref: "node:22"},
		Workload: lux.Workload{Adapter: "generic", Command: []string{"sleep", "infinity"}},
		Secrets:  []lux.Secret{{Name: "JSON_KEY", Value: secret, As: "env"}},
	})
	ctx := context.Background()
	if _, err := c.AddServer(ctx, run, lux.ServerInput{
		Name: "probe", Port: 4100, Env: map[string]string{"ESCAPED": forms[0], "PLAIN": forms[1]},
		Command: []string{"sh", "-c", `printf 'escaped %s\n' "$ESCAPED"; printf 'plain %s\n' "$PLAIN"; exit 1 # fakelux-run`},
	}); err != nil {
		t.Fatal(err)
	}
	type line struct{ Stream, Text string }
	var lines []line
	var raw json.RawMessage
	waitFor(t, "the command's exit in its log", func() bool {
		var err error
		if raw, err = c.ServerLog(ctx, run, "probe", 100); err != nil {
			t.Fatal(err)
		}
		var out struct{ Lines []line }
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		lines = out.Lines
		return slices.ContainsFunc(lines, func(l line) bool { return strings.Contains(l.Text, "exit status 1") })
	})
	text := ""
	for _, l := range lines {
		text += l.Text
	}
	for _, canary := range []string{"canary", "7d1e", "line two"} {
		if strings.Contains(text, canary) {
			t.Errorf("the server's log carries the secret in a JSON form: %+v", lines)
		}
	}
	if !strings.Contains(text, `escaped "[REDACTED:JSON_KEY]"`) || !strings.Contains(text, `plain "[REDACTED:JSON_KEY]"`) {
		t.Errorf("want both forms redacted: %+v", lines)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/config"
	"github.com/marciomartins/dude/orchestrator/internal/registry"
)

// loadConfig resolves the orchestrator's config from a file holding text
// (none when text is "") and vars as the whole environment.
func loadConfig(t *testing.T, text string, mode os.FileMode, vars map[string]string) *config.Config {
	t.Helper()
	env := map[string]string{}
	for k, v := range vars {
		env[k] = v
	}
	if text != "" {
		path := filepath.Join(t.TempDir(), "dude.toml")
		if err := os.WriteFile(path, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		env["DUDE_CONFIG"] = path
	}
	cfg, err := config.Load(config.Orchestrator, config.Options{Getenv: func(k string) string { return env[k] },
		DefaultPath: filepath.Join(t.TempDir(), "absent.toml")})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func mustSettings(t *testing.T, cfg *config.Config) settings {
	t.Helper()
	s, err := settingsFrom(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const required = `
[database]
url = "postgres://file/dude"
[orchestrator]
token = "file-token"
[lux]
url = "https://lux.file"
api_key = "lux-file-key"
`

func TestSettingsComeFromTheFileAlone(t *testing.T) {
	s := mustSettings(t, loadConfig(t, `
[database]
url = "postgres://file/dude"
[orchestrator]
token = "file-token"
listen = "127.0.0.1:4100"
pr_reconcile = "30m"
park_after = "20m"
idle_after = "1h"
diff_every = "30s"
machine_usd_per_hour = 0.35
lux_cost_every = "5m"
[lux]
url = "https://lux.file"
api_key = "lux-file-key"
console_url = "https://console.file"
[llm]
url = "https://llm.file/v1"
key = "llm-file-key"
[embeddings]
model = "file-model"
[agent]
image = "img:file"
[registry]
auth = "ecr"
ecr_role_arn = "arn:aws:iam::123456789012:role/file"
[tools]
listen = "0.0.0.0:3200"
url = "http://10.0.0.5:3200"
[vapid]
public_key = "BFile"
private_key = "vapid-file-private"
subject = "mailto:file@example.com"
[factory]
logins = ["file-bot"]
`, 0o600, nil))
	want := settings{
		DatabaseURL: "postgres://file/dude", Token: "file-token", Listen: "127.0.0.1:4100", ToolsListen: "0.0.0.0:3200",
		LuxURL: "https://lux.file", LuxKey: "lux-file-key", ConsoleURL: "https://console.file",
		Registry:       registrySettings{Mode: "ecr", ECRRoleARN: "arn:aws:iam::123456789012:role/file"},
		ReconcileEvery: 30 * time.Minute, ParkAfter: 20 * time.Minute, IdleAfter: time.Hour, DiffEvery: 30 * time.Second,
		MachineUSDPerHour: 0.35, LuxCostEvery: 5 * time.Minute, FactoryLogins: []string{"file-bot"},
		VAPIDPublic: "BFile", VAPIDPrivate: "vapid-file-private", VAPIDSubject: "mailto:file@example.com",
		Embeddings: embeddingsConfig{URL: "https://llm.file/v1", Key: "llm-file-key",
			URLFrom: "DUDE_LLM_URL", KeyFrom: "DUDE_LLM_KEY"},
		EmbeddingsModel: "file-model", EmbeddingsDimension: 768,
	}
	agent := s.Agent
	s.Agent = want.Agent
	if !reflect.DeepEqual(s, want) {
		t.Errorf("settings =\n%+v\nwant\n%+v", s, want)
	}
	if agent.LLMURL != "https://llm.file/v1" || agent.LLMKey != "llm-file-key" || agent.DefaultImage != "img:file" ||
		agent.ToolsURL != "http://10.0.0.5:3200" || string(agent.ToolsKey) != "file-token" {
		t.Errorf("agent = %+v", agent)
	}
	// The registry getter hands registry.FromEnv exactly these.
	for env, want := range map[string]string{"DUDE_REGISTRY_AUTH": "ecr", "DUDE_REGISTRY": "",
		"DUDE_ECR_ROLE_ARN": "arn:aws:iam::123456789012:role/file", "DUDE_REGISTRY_CREDENTIAL": ""} {
		if got := s.Registry.getenv(env); got != want {
			t.Errorf("registry getenv(%s) = %q, want %q", env, got, want)
		}
	}
}

func TestTheEnvironmentOverridesTheFileInSettings(t *testing.T) {
	s := mustSettings(t, loadConfig(t, required+`[registry]
auth = "none"
[vapid]
subject = "mailto:file@example.com"
`, 0o600, map[string]string{
		"DATABASE_URL": "postgres://env/dude", "DUDE_ORCHESTRATOR_TOKEN": "env-token", "LUX_URL": "https://lux.env",
		"LUX_API_KEY": "lux-env-key", "DUDE_ORCHESTRATOR_LISTEN": "127.0.0.1:4200", "DUDE_TOOLS_LISTEN": "0.0.0.0:4300",
		"DUDE_PR_RECONCILE": "5m", "DUDE_PARK_AFTER": "1m", "DUDE_IDLE_AFTER": "2m", "DUDE_DIFF_EVERY": "3s",
		"DUDE_MACHINE_USD_PER_HOUR": "1.5", "DUDE_LUX_COST_EVERY": "30s", "DUDE_FACTORY_LOGINS": "a,b",
		"DUDE_REGISTRY_AUTH": "static", "DUDE_REGISTRY": "ghcr.io", "DUDE_REGISTRY_CREDENTIAL": "u:p",
		"DUDE_VAPID_PUBLIC_KEY": "BEnv", "DUDE_VAPID_PRIVATE_KEY": "vapid-env", "DUDE_VAPID_SUBJECT": "mailto:env@example.com",
		"DUDE_EMBEDDINGS_URL": "https://emb.env/v1", "DUDE_EMBEDDINGS_KEY": "emb-env", "DUDE_EMBEDDINGS_MODEL": "env-model",
	}))
	if s.DatabaseURL != "postgres://env/dude" || s.Token != "env-token" || s.LuxURL != "https://lux.env" ||
		s.LuxKey != "lux-env-key" || s.Listen != "127.0.0.1:4200" || s.ToolsListen != "0.0.0.0:4300" ||
		s.ReconcileEvery != 5*time.Minute || s.ParkAfter != time.Minute || s.IdleAfter != 2*time.Minute ||
		s.DiffEvery != 3*time.Second || s.MachineUSDPerHour != 1.5 || s.LuxCostEvery != 30*time.Second || !reflect.DeepEqual(s.FactoryLogins, []string{"a", "b"}) ||
		s.Registry != (registrySettings{Mode: "static", Host: "ghcr.io", Credential: "u:p"}) ||
		s.VAPIDPublic != "BEnv" || s.VAPIDPrivate != "vapid-env" || s.VAPIDSubject != "mailto:env@example.com" ||
		s.Embeddings.URL != "https://emb.env/v1" || s.Embeddings.Key != "emb-env" || s.EmbeddingsModel != "env-model" {
		t.Errorf("settings = %+v", s)
	}
	// With no console URL of its own, lux's URL is the env's too.
	if s.ConsoleURL != "https://lux.env" {
		t.Errorf("console url = %q", s.ConsoleURL)
	}
}

func TestStaticRegistrySettingsReachTheRegistryLogin(t *testing.T) {
	file := required + `[registry]
auth = "static"
host = "registry.file.example:5000"
credential = "file-user:file-pass"
`
	for name, tc := range map[string]struct {
		vars                 map[string]string
		wantHost, wantSecret string
	}{
		"file": {wantHost: "registry.file.example:5000", wantSecret: "file-user:file-pass"},
		"env host": {vars: map[string]string{"DUDE_REGISTRY": "ghcr.io"},
			wantHost: "ghcr.io", wantSecret: "file-user:file-pass"},
		"env credential": {vars: map[string]string{"DUDE_REGISTRY_CREDENTIAL": "env-user:env-pass"},
			wantHost: "registry.file.example:5000", wantSecret: "env-user:env-pass"},
	} {
		s := mustSettings(t, loadConfig(t, file, 0o600, tc.vars))
		login, err := registry.FromEnv(context.Background(), s.Registry.getenv, s.Agent.DefaultImage)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if login == nil {
			t.Fatalf("%s: no registry login", name)
		}
		secret, err := login.Credential(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if login.Registry() != tc.wantHost || secret != tc.wantSecret {
			t.Errorf("%s: login = %q %q, want %q %q", name, login.Registry(), secret, tc.wantHost, tc.wantSecret)
		}
	}
}

func TestSettingsDefaults(t *testing.T) {
	s := mustSettings(t, loadConfig(t, required, 0o600, nil))
	if s.Listen != "127.0.0.1:3100" || s.ToolsListen != "" || s.ReconcileEvery != 15*time.Minute ||
		s.DiffEvery != 15*time.Second || s.ParkAfter != 0 || s.IdleAfter != 0 || s.MachineUSDPerHour != 0.2 || s.LuxCostEvery != 2*time.Minute ||
		s.VAPIDSubject != "mailto:dude@localhost" || s.Registry.Mode != "none" || s.FactoryLogins != nil ||
		s.Embeddings.URL != "" || s.Embeddings.Off == "" || s.Agent.DefaultImage != "localhost/dude-runtime:dev" {
		t.Errorf("settings = %+v", s)
	}
	// An empty listen in the file is unset: the default, never Go's ":http".
	s = mustSettings(t, loadConfig(t, strings.Replace(required, "[orchestrator]\n", "[orchestrator]\nlisten = \"\"\n", 1), 0o600, nil))
	if s.Listen != "127.0.0.1:3100" {
		t.Errorf("listen = %q, want the default", s.Listen)
	}
}

func TestTheConsoleURLFallsBackToLux(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		vars map[string]string
		want string
	}{
		"absent":         {text: required, want: "https://lux.file"},
		"explicit":       {text: strings.Replace(required, "[lux]\n", "[lux]\nconsole_url = \"https://console.file\"\n", 1), want: "https://console.file"},
		"empty file key": {text: strings.Replace(required, "[lux]\n", "[lux]\nconsole_url = \"\"\n", 1), want: "https://lux.file"},
		"env":            {text: required, vars: map[string]string{"LUX_CONSOLE_URL": "https://console.env"}, want: "https://console.env"},
	} {
		if got := mustSettings(t, loadConfig(t, tc.text, 0o600, tc.vars)).ConsoleURL; got != tc.want {
			t.Errorf("%s: console url = %q, want %q", name, got, tc.want)
		}
	}
}

func TestDimensionsOtherThanTheIndexAreRefusedOnlyWithEmbeddings(t *testing.T) {
	llm := "[llm]\nurl = \"https://llm.file/v1\"\nkey = \"k\"\n"
	_, err := settingsFrom(loadConfig(t, required+llm+"[embeddings]\ndimensions = 1536\n", 0o600, nil))
	if err == nil || !strings.Contains(err.Error(), "embeddings.dimensions (DUDE_EMBEDDINGS_DIMENSIONS)") {
		t.Errorf("err = %v, want 1536 refused naming the key", err)
	}
	_, err = settingsFrom(loadConfig(t, required+llm, 0o600, map[string]string{"DUDE_EMBEDDINGS_DIMENSIONS": "1536"}))
	if err == nil || !strings.Contains(err.Error(), "DUDE_EMBEDDINGS_DIMENSIONS") {
		t.Errorf("env: err = %v, want 1536 refused", err)
	}
	// Off, the size is never used.
	for name, vars := range map[string]map[string]string{
		"no llm":        nil,
		"embedding off": {"DUDE_LLM_URL": "https://llm.env/v1", "DUDE_LLM_KEY": "k", "DUDE_EMBEDDINGS_URL": "off"},
	} {
		s, err := settingsFrom(loadConfig(t, required+"[embeddings]\ndimensions = 1536\n", 0o600, vars))
		if err != nil || s.Embeddings.URL != "" {
			t.Errorf("%s: err = %v, embeddings %+v; want them off and no error", name, err, s.Embeddings)
		}
	}
}

func TestAMissingRequiredSettingIsNamed(t *testing.T) {
	for drop, want := range map[string]string{
		"url = \"postgres://file/dude\"\n": "database.url (DATABASE_URL) is required",
		"token = \"file-token\"\n":         "orchestrator.token (DUDE_ORCHESTRATOR_TOKEN) is required",
		"url = \"https://lux.file\"\n":     "lux.url (LUX_URL) is required",
		"api_key = \"lux-file-key\"\n":     "lux.api_key (LUX_API_KEY) is required",
	} {
		_, err := settingsFrom(loadConfig(t, strings.Replace(required, drop, "", 1), 0o600, nil))
		var missing missingError
		if !errors.As(err, &missing) || err.Error() != want {
			t.Errorf("without %q: err = %v, want %q", drop, err, want)
		}
	}
	// Empty in the file is missing too.
	_, err := settingsFrom(loadConfig(t, strings.Replace(required, "\"file-token\"", "\"\"", 1), 0o600, nil))
	if err == nil || err.Error() != "orchestrator.token (DUDE_ORCHESTRATOR_TOKEN) is required" {
		t.Errorf("empty token: err = %v", err)
	}
	if _, err := settingsFrom(loadConfig(t, required, 0o600, map[string]string{"DUDE_MACHINE_USD_PER_HOUR": "-1"})); err == nil {
		t.Error("a negative rate was accepted")
	}
	for _, v := range []string{"0s", "-1m"} {
		if _, err := settingsFrom(loadConfig(t, required, 0o600, map[string]string{"DUDE_LUX_COST_EVERY": v})); err == nil ||
			!strings.Contains(err.Error(), "orchestrator.lux_cost_every (DUDE_LUX_COST_EVERY)") {
			t.Errorf("lux cost cadence %s: err = %v, want refused", v, err)
		}
	}
}

func TestTheLoadersWarningsAreLoggedWithoutTheirSecrets(t *testing.T) {
	const sentinel = "S3NT1NEL-lux-key-9f2c"
	text := "[lux]\nurl = \"https://lux.file\"\napi_key = \"" + sentinel + "\"\n"
	// logged is logConfig's records, decoded, and its raw output.
	logged := func(cfg *config.Config) ([]map[string]any, string) {
		var buf bytes.Buffer
		logConfig(slog.New(slog.NewJSONHandler(&buf, nil)), cfg)
		var recs []map[string]any
		for dec := json.NewDecoder(bytes.NewReader(buf.Bytes())); dec.More(); {
			var rec map[string]any
			if err := dec.Decode(&rec); err != nil {
				t.Fatal(err)
			}
			recs = append(recs, rec)
		}
		return recs, buf.String()
	}
	cfg := loadConfig(t, text, 0o644, nil)
	recs, out := logged(cfg)
	if len(recs) != 2 || recs[0]["msg"] != "configuration file read" || recs[0]["path"] != cfg.Path ||
		recs[1]["level"] != "WARN" || !strings.Contains(recs[1]["msg"].(string), "lux.api_key") {
		t.Errorf("log = %q, want the file read at %s and a warning naming lux.api_key", out, cfg.Path)
	}
	if strings.Contains(out, sentinel) {
		t.Errorf("log carries the secret: %q", out)
	}
	for name, cfg := range map[string]*config.Config{
		"private file": loadConfig(t, text, 0o600, nil),
		"no secret":    loadConfig(t, "[lux]\nurl = \"https://lux.file\"\n", 0o644, nil),
	} {
		if recs, out := logged(cfg); len(recs) != 1 || recs[0]["path"] != cfg.Path || recs[0]["level"] != "INFO" {
			t.Errorf("%s: log = %q, want the file named and no warning", name, out)
		}
	}
}

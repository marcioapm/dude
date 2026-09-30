package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// env is a getenv over a map, so no test depends on the real environment.
func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func writeFile(t *testing.T, text string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dude.toml")
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// noDefault is a default path that never exists.
func noDefault(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.toml") }

func load(t *testing.T, p Process, vars map[string]string) (*Config, error) {
	t.Helper()
	return Load(p, Options{Getenv: env(vars), DefaultPath: noDefault(t)})
}

func mustLoad(t *testing.T, p Process, vars map[string]string) *Config {
	t.Helper()
	c, err := load(t, p, vars)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWithoutAFileTheEnvironmentAloneConfigures(t *testing.T) {
	c := mustLoad(t, Orchestrator, map[string]string{
		"LUX_URL": "https://lux.example", "DUDE_PR_RECONCILE": "5m", "DUDE_TOOLS_SERVICE": "off",
		"DUDE_AGENT_EGRESS": "a.example, b.example,", "DUDE_MACHINE_USD_PER_HOUR": "0.5",
	})
	if c.Path != "" {
		t.Errorf("path = %q, want none", c.Path)
	}
	if c.String("LUX_URL") != "https://lux.example" || c.Duration("DUDE_PR_RECONCILE") != 5*time.Minute ||
		c.Bool("DUDE_TOOLS_SERVICE") || !reflect.DeepEqual(c.List("DUDE_AGENT_EGRESS"), []string{"a.example", "b.example"}) ||
		c.Float("DUDE_MACHINE_USD_PER_HOUR") != 0.5 {
		t.Errorf("config = %+v", c.Set())
	}
	// Unset, each setting is its default.
	if c.String("DUDE_ORCHESTRATOR_LISTEN") != "127.0.0.1:3100" || c.Int("DUDE_EMBEDDINGS_DIMENSIONS") != 768 ||
		c.Duration("DUDE_DIFF_EVERY") != 15*time.Second || c.String("DUDE_AGENT_IMAGE") != "localhost/dude-runtime:dev" ||
		c.List("DUDE_FACTORY_LOGINS") != nil || c.String("DUDE_LLM_URL") != "" {
		t.Error("defaults not applied")
	}
	if c.From("LUX_URL") != "env" || c.From("DUDE_ORCHESTRATOR_LISTEN") != "" {
		t.Error("From does not say where settings came from")
	}
}

func TestAFileAloneConfigures(t *testing.T) {
	path := writeFile(t, `
[lux]
url = "https://lux.example"
[orchestrator]
pr_reconcile = "5m"
machine_usd_per_hour = 1
[tools]
service = false
[agent]
egress = ["a.example"]
`, 0o600)
	c := mustLoad(t, Orchestrator, map[string]string{"DUDE_CONFIG": path})
	if c.Path != path || c.String("LUX_URL") != "https://lux.example" || c.Duration("DUDE_PR_RECONCILE") != 5*time.Minute ||
		c.Bool("DUDE_TOOLS_SERVICE") || !reflect.DeepEqual(c.List("DUDE_AGENT_EGRESS"), []string{"a.example"}) ||
		c.Float("DUDE_MACHINE_USD_PER_HOUR") != 1 || c.From("LUX_URL") != "file" {
		t.Errorf("config = %+v", c.Set())
	}
}

func TestTheEnvironmentOverridesTheFileKeyByKey(t *testing.T) {
	path := writeFile(t, `
[lux]
url = "https://file.example"
console_url = "https://console.file.example"
[embeddings]
dimensions = 768
[tools]
service = false
[factory]
logins = ["file-bot"]
`, 0o600)
	c := mustLoad(t, Orchestrator, map[string]string{
		"DUDE_CONFIG": path, "LUX_URL": "https://env.example", "DUDE_EMBEDDINGS_DIMENSIONS": "1536",
		"DUDE_TOOLS_SERVICE": "on", "DUDE_FACTORY_LOGINS": "env-bot, other-bot",
	})
	if c.String("LUX_URL") != "https://env.example" || c.Int("DUDE_EMBEDDINGS_DIMENSIONS") != 1536 ||
		!c.Bool("DUDE_TOOLS_SERVICE") || !reflect.DeepEqual(c.List("DUDE_FACTORY_LOGINS"), []string{"env-bot", "other-bot"}) {
		t.Errorf("config = %+v", c.Set())
	}
	// A key only the file sets keeps the file's value.
	if c.String("LUX_CONSOLE_URL") != "https://console.file.example" {
		t.Errorf("console_url = %q", c.String("LUX_CONSOLE_URL"))
	}
	// An empty variable is unset, as it always was: the file's value stands.
	c = mustLoad(t, Orchestrator, map[string]string{"DUDE_CONFIG": path, "LUX_URL": ""})
	if c.String("LUX_URL") != "https://file.example" {
		t.Errorf("empty variable overrode the file: %q", c.String("LUX_URL"))
	}
}

func TestAnEmptyStringInTheFileIsUnset(t *testing.T) {
	path := writeFile(t, `
[orchestrator]
listen = ""
pr_reconcile = ""
[agent]
image = ""
[registry]
auth = ""
[vapid]
subject = ""
[embeddings]
model = ""
[tools]
listen = ""
[lux]
url = ""
`, 0o600)
	c := mustLoad(t, Orchestrator, map[string]string{"DUDE_CONFIG": path})
	for env, want := range map[string]string{
		"DUDE_ORCHESTRATOR_LISTEN": "127.0.0.1:3100", "DUDE_AGENT_IMAGE": "localhost/dude-runtime:dev",
		"DUDE_REGISTRY_AUTH": "none", "DUDE_VAPID_SUBJECT": "mailto:dude@localhost",
		"DUDE_EMBEDDINGS_MODEL": "gemini-embedding-2", "DUDE_TOOLS_LISTEN": "", "LUX_URL": "",
	} {
		if got := c.String(env); got != want || c.From(env) != "" {
			t.Errorf("%s = %q from %q, want the default %q", env, got, c.From(env), want)
		}
	}
	if c.Duration("DUDE_PR_RECONCILE") != 15*time.Minute {
		t.Errorf("pr_reconcile = %v, want the default", c.Duration("DUDE_PR_RECONCILE"))
	}
	if len(c.Set()) != 0 {
		t.Errorf("empty strings were recorded as set: %v", c.Set())
	}
	// The variable still overrides an empty file value.
	c = mustLoad(t, Orchestrator, map[string]string{"DUDE_CONFIG": path, "DUDE_ORCHESTRATOR_LISTEN": "127.0.0.1:9"})
	if c.String("DUDE_ORCHESTRATOR_LISTEN") != "127.0.0.1:9" {
		t.Errorf("listen = %q", c.String("DUDE_ORCHESTRATOR_LISTEN"))
	}
	c = mustLoad(t, Backend, map[string]string{"DUDE_CONFIG": writeFile(t, "[s3]\nregion = \"\"\n", 0o600)})
	if c.String("DUDE_S3_REGION") != "us-east-1" {
		t.Errorf("s3.region = %q, want the default", c.String("DUDE_S3_REGION"))
	}
}

func TestANonFiniteRateIsRefused(t *testing.T) {
	for _, v := range []string{"nan", "inf", "-inf", "+inf", "+nan"} {
		path := writeFile(t, "[orchestrator]\nmachine_usd_per_hour = "+v+"\n", 0o600)
		_, err := load(t, Orchestrator, map[string]string{"DUDE_CONFIG": path})
		if err == nil || !strings.Contains(err.Error(), "orchestrator.machine_usd_per_hour (DUDE_MACHINE_USD_PER_HOUR)") {
			t.Errorf("%s: err = %v, want it refused naming the key", v, err)
		}
	}
	for v, want := range map[string]float64{"2": 2, "0.35": 0.35, "0": 0} {
		path := writeFile(t, "[orchestrator]\nmachine_usd_per_hour = "+v+"\n", 0o600)
		c := mustLoad(t, Orchestrator, map[string]string{"DUDE_CONFIG": path})
		if got := c.Float("DUDE_MACHINE_USD_PER_HOUR"); got != want {
			t.Errorf("%s: rate = %v, want %v", v, got, want)
		}
	}
	for _, v := range []string{"NaN", "Inf", "-Inf"} {
		if _, err := load(t, Orchestrator, map[string]string{"DUDE_MACHINE_USD_PER_HOUR": v}); err == nil {
			t.Errorf("DUDE_MACHINE_USD_PER_HOUR=%s accepted", v)
		}
	}
}

func TestKeysAreMatchedExactlyAndCaseSensitively(t *testing.T) {
	for text, want := range map[string]string{
		"[Orchestrator]\nlisten = \"127.0.0.1:1\"\n":                         "Orchestrator",
		"[orchestrator]\nListen = \"127.0.0.1:1\"\n":                         "orchestrator.Listen",
		"[orchestrator]\nlisten = \"127.0.0.1:1\"\nListen = \"0.0.0.0:1\"\n": "orchestrator.Listen",
		"[auth.Cloudflare_Access]\nteam = \"x\"\n":                           "auth.Cloudflare_Access",
	} {
		for _, p := range []Process{Orchestrator, Backend} {
			_, err := load(t, p, map[string]string{"DUDE_CONFIG": writeFile(t, text, 0o600)})
			if err == nil || !strings.Contains(err.Error(), "unknown key "+want) {
				t.Errorf("%s %q: err = %v, want unknown key %s", p, text, err, want)
			}
		}
	}
}

func TestAnUnknownKeyIsRefusedByName(t *testing.T) {
	for text, want := range map[string]string{
		"[lux]\nurl = \"x\"\ntoken = \"y\"\n":           "lux.token",
		"[nonsense]\nx = 1\n":                           "nonsense",
		"top = 1\n":                                     "top",
		"[auth.cloudflare_access]\nteam_name = \"x\"\n": "auth.cloudflare_access.team_name",
	} {
		for _, p := range []Process{Orchestrator, Backend} {
			_, err := load(t, p, map[string]string{"DUDE_CONFIG": writeFile(t, text, 0o600)})
			if err == nil || !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), want) {
				t.Errorf("%s %q: err = %v, want unknown key %s", p, text, err, want)
			}
		}
	}
}

func TestAWrongTypeIsRefusedByName(t *testing.T) {
	for text, want := range map[string]string{
		"[embeddings]\ndimensions = \"768\"\n":  "embeddings.dimensions",
		"[tools]\nservice = \"off\"\n":          "tools.service",
		"[agent]\negress = \"a.example\"\n":     "agent.egress",
		"[agent]\negress = [1]\n":               "agent.egress",
		"[orchestrator]\npr_reconcile = 15\n":   "orchestrator.pr_reconcile",
		"[orchestrator]\npark_after = \"soon\"": "orchestrator.park_after",
		"[lux]\nurl = 1\n":                      "lux.url",
		"[lux]\nurl = \"x\n":                    "",
	} {
		_, err := load(t, Orchestrator, map[string]string{"DUDE_CONFIG": writeFile(t, text, 0o600)})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want it to name %s", text, err, want)
		}
	}
	for name, raw := range map[string]string{
		"DUDE_EMBEDDINGS_DIMENSIONS": "seven", "DUDE_TOOLS_SERVICE": "maybe", "DUDE_PR_RECONCILE": "15",
		"DUDE_MACHINE_USD_PER_HOUR": "cheap",
	} {
		_, err := load(t, Orchestrator, map[string]string{name: raw})
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%q: err = %v, want it named", name, raw, err)
		}
	}
}

func TestAMissingDUDE_CONFIGIsRefused(t *testing.T) {
	_, err := load(t, Orchestrator, map[string]string{"DUDE_CONFIG": filepath.Join(t.TempDir(), "missing.toml")})
	if err == nil || !strings.Contains(err.Error(), "DUDE_CONFIG") {
		t.Errorf("err = %v, want DUDE_CONFIG refused", err)
	}
}

func TestTheDefaultPathIsReadWhenPresent(t *testing.T) {
	path := writeFile(t, "[lux]\nurl = \"https://default.example\"\n", 0o600)
	c, err := Load(Orchestrator, Options{Getenv: env(nil), DefaultPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if c.Path != path || c.String("LUX_URL") != "https://default.example" {
		t.Errorf("path %q, lux.url %q", c.Path, c.String("LUX_URL"))
	}
	// DUDE_CONFIG wins over the default path.
	other := writeFile(t, "[lux]\nurl = \"https://named.example\"\n", 0o600)
	c, err = Load(Orchestrator, Options{Getenv: env(map[string]string{"DUDE_CONFIG": other}), DefaultPath: path})
	if err != nil || c.String("LUX_URL") != "https://named.example" {
		t.Errorf("DUDE_CONFIG did not win: %v %q", err, c.String("LUX_URL"))
	}
}

func TestEachProcessAcceptsTheOthersSections(t *testing.T) {
	// A wrong type in a key the loading process does not use is the other
	// process's to refuse; an unknown key is refused by both (above).
	text := `
[backend]
port = 3000
web_dir = "/web"
[s3]
bucket = "b"
[auth]
provider = "cloudflare_access"
auto_create = true
[lux]
url = "https://lux.example"
`
	path := writeFile(t, text, 0o600)
	if _, err := load(t, Orchestrator, map[string]string{"DUDE_CONFIG": path}); err != nil {
		t.Errorf("orchestrator refused the backend's sections: %v", err)
	}
	if _, err := load(t, Backend, map[string]string{"DUDE_CONFIG": path}); err != nil {
		t.Errorf("backend refused the orchestrator's sections: %v", err)
	}
	wrong := writeFile(t, "[backend]\nport = \"x\"\n", 0o600)
	if _, err := load(t, Orchestrator, map[string]string{"DUDE_CONFIG": wrong}); err != nil {
		t.Errorf("orchestrator refused a backend key: %v", err)
	}
	if _, err := load(t, Backend, map[string]string{"DUDE_CONFIG": wrong}); err == nil || !strings.Contains(err.Error(), "backend.port") {
		t.Errorf("backend accepted a wrong port: %v", err)
	}
	// Nor does a variable of the other process's reach it, or fail it.
	if _, err := load(t, Orchestrator, map[string]string{"PORT": "not a port"}); err != nil {
		t.Errorf("orchestrator read PORT: %v", err)
	}
}

func TestAFileOthersCanReadWithASecretWarns(t *testing.T) {
	const sentinel = "S3NT1NEL-lux-api-key-7d1e"
	secret := "[lux]\nurl = \"https://lux.example\"\napi_key = \"" + sentinel + "\"\n"
	c := mustLoad(t, Orchestrator, map[string]string{"DUDE_CONFIG": writeFile(t, secret, 0o644)})
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "lux.api_key") || strings.Contains(c.Warnings[0], sentinel) {
		t.Errorf("warnings = %q, want one naming lux.api_key and not its value", c.Warnings)
	}
	// The file still holds the secret for anyone to read, whatever the
	// environment overrides it with.
	c = mustLoad(t, Orchestrator, map[string]string{"DUDE_CONFIG": writeFile(t, secret, 0o644), "LUX_API_KEY": "env-key"})
	if c.String("LUX_API_KEY") != "env-key" || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "lux.api_key") {
		t.Errorf("env override: key %q, warnings = %q; want the warning still", c.String("LUX_API_KEY"), c.Warnings)
	}
	for name, cfg := range map[string]*Config{
		"private file":  mustLoad(t, Orchestrator, map[string]string{"DUDE_CONFIG": writeFile(t, secret, 0o600)}),
		"no secret":     mustLoad(t, Orchestrator, map[string]string{"DUDE_CONFIG": writeFile(t, "[lux]\nurl = \"x\"\n", 0o644)}),
		"secret in env": mustLoad(t, Orchestrator, map[string]string{"DUDE_CONFIG": writeFile(t, "", 0o644), "LUX_API_KEY": "k"}),
	} {
		if len(cfg.Warnings) != 0 {
			t.Errorf("%s: warnings = %q", name, cfg.Warnings)
		}
	}
	c = mustLoad(t, Backend, map[string]string{"DUDE_CONFIG": writeFile(t, "[database]\nurl = \"postgres://x\"\n", 0o640)})
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "database.url") {
		t.Errorf("group-readable: warnings = %q", c.Warnings)
	}
}

// fixture is tests/fixtures/config: a file with every key and what both
// processes must resolve it to. The TypeScript suite reads the same files.
func fixture(t *testing.T, name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "../../../tests/fixtures/config", name)
}

func TestTheSharedFixtureResolvesAsBothSuitesExpect(t *testing.T) {
	raw, err := os.ReadFile(fixture(t, "full.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	// Every key of the schema is in the fixture, and nothing else: the
	// TypeScript schema is checked against the same list.
	var envs []string
	for _, k := range Keys() {
		envs = append(envs, k.Env)
		if _, ok := want[k.Env]; !ok {
			t.Errorf("fixture lacks %s", k.Label())
		}
	}
	if len(envs) != len(want) {
		t.Errorf("schema has %d keys, fixture %d", len(envs), len(want))
	}
	for _, p := range []Process{Orchestrator, Backend} {
		c := mustLoad(t, p, map[string]string{"DUDE_CONFIG": fixture(t, "full.toml")})
		got := map[string]any{}
		for k, v := range c.Set() {
			switch v := v.(type) {
			case int64:
				got[k] = float64(v)
			case []string:
				items := make([]any, len(v))
				for i, s := range v {
					items[i] = s
				}
				got[k] = items
			default:
				got[k] = v
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: resolved map differs from full.json", p)
			for k := range want {
				if !reflect.DeepEqual(got[k], want[k]) {
					t.Errorf("%s: %s = %#v, want %#v", p, k, got[k], want[k])
				}
			}
			for k := range got {
				if _, ok := want[k]; !ok {
					t.Errorf("%s: %s = %#v is not in full.json", p, k, got[k])
				}
			}
		}
	}
}

// invalid/*.toml are files both processes, in both languages, must refuse;
// each file's first line is "# <the error it must contain>".
func TestTheSharedInvalidFilesAreRefused(t *testing.T) {
	paths, err := filepath.Glob(fixture(t, "invalid/*.toml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no invalid fixtures: %v", err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		first, _, _ := strings.Cut(string(raw), "\n")
		want := strings.TrimPrefix(first, "# ")
		for _, p := range []Process{Orchestrator, Backend} {
			_, err := load(t, p, map[string]string{"DUDE_CONFIG": path})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s %s: err = %v, want %q", p, filepath.Base(path), err, want)
			}
		}
	}
}

func TestTheDocumentedExampleLoads(t *testing.T) {
	for _, p := range []Process{Orchestrator, Backend} {
		if _, err := load(t, p, map[string]string{"DUDE_CONFIG": fixture(t, "../../../docs/dude.example.toml")}); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// keys.json is the schema as data: every key's name, variable, kind, user,
// default and secrecy. The TypeScript suite compares its schema to the same
// file, so the two languages cannot drift apart.
func TestTheSchemaIsTheSharedKeyTable(t *testing.T) {
	raw, err := os.ReadFile(fixture(t, "keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want []struct {
		Name, Env, Kind, Use, Default string
		Secret                        bool
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got := Keys()
	if len(got) != len(want) {
		t.Fatalf("schema has %d keys, keys.json %d", len(got), len(want))
	}
	for i, w := range want {
		if g := got[i]; g.Name != w.Name || g.Env != w.Env || g.Kind != w.Kind || g.Use != w.Use ||
			g.Default != w.Default || g.Secret != w.Secret {
			t.Errorf("key %d: schema %+v, keys.json %+v", i, g, w)
		}
	}
}

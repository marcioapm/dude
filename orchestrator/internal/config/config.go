// Package config resolves the settings of dude's two processes from one
// TOML file and the environment.
//
// The file is DUDE_CONFIG if set, else DefaultPath if it exists, else none.
// Every setting also has an environment variable, and a non-empty variable
// overrides the file. The backend (apps/control-plane/src/config.ts) reads
// the same file with the same schema: a key either process knows is
// accepted by both, and each process checks the types of only the keys it
// uses.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// DefaultPath is where the file is looked for when DUDE_CONFIG is unset.
const DefaultPath = "/etc/dude/dude.toml"

// Process names which of dude's processes is loading, to decide which keys
// it checks and reads.
type Process string

const (
	Backend      Process = "backend"
	Orchestrator Process = "orchestrator"
)

// schema is every key of the file. Each leaf is `any` so that a key another
// process uses decodes whatever its type; tags say its variable, its kind,
// who uses it ("backend", "orchestrator" or "both"), its default, and
// whether it is a secret.
type schema struct {
	Database struct {
		URL any `toml:"url" env:"DATABASE_URL" kind:"string" use:"both" secret:"true"`
	} `toml:"database"`
	Backend struct {
		Port   any `toml:"port" env:"PORT" kind:"int" use:"backend" default:"3000"`
		WebDir any `toml:"web_dir" env:"DUDE_WEB_DIR" kind:"string" use:"backend"`
	} `toml:"backend"`
	Orchestrator struct {
		URL               any `toml:"url" env:"DUDE_ORCHESTRATOR_URL" kind:"string" use:"backend"`
		Token             any `toml:"token" env:"DUDE_ORCHESTRATOR_TOKEN" kind:"string" use:"both" secret:"true"`
		Listen            any `toml:"listen" env:"DUDE_ORCHESTRATOR_LISTEN" kind:"string" use:"orchestrator" default:"127.0.0.1:3100"`
		PRReconcile       any `toml:"pr_reconcile" env:"DUDE_PR_RECONCILE" kind:"duration" use:"orchestrator" default:"15m"`
		ParkAfter         any `toml:"park_after" env:"DUDE_PARK_AFTER" kind:"duration" use:"orchestrator" default:"0s"`
		IdleAfter         any `toml:"idle_after" env:"DUDE_IDLE_AFTER" kind:"duration" use:"orchestrator" default:"0s"`
		DiffEvery         any `toml:"diff_every" env:"DUDE_DIFF_EVERY" kind:"duration" use:"orchestrator" default:"15s"`
		MachineUSDPerHour any `toml:"machine_usd_per_hour" env:"DUDE_MACHINE_USD_PER_HOUR" kind:"float" use:"orchestrator" default:"0.20"`
	} `toml:"orchestrator"`
	S3 struct {
		Bucket    any `toml:"bucket" env:"DUDE_S3_BUCKET" kind:"string" use:"backend"`
		Endpoint  any `toml:"endpoint" env:"DUDE_S3_ENDPOINT" kind:"string" use:"backend"`
		Region    any `toml:"region" env:"DUDE_S3_REGION" kind:"string" use:"backend" default:"us-east-1"`
		AccessKey any `toml:"access_key" env:"DUDE_S3_ACCESS_KEY" kind:"string" use:"backend"`
		SecretKey any `toml:"secret_key" env:"DUDE_S3_SECRET_KEY" kind:"string" use:"backend" secret:"true"`
	} `toml:"s3"`
	Lux struct {
		URL        any `toml:"url" env:"LUX_URL" kind:"string" use:"orchestrator"`
		ConsoleURL any `toml:"console_url" env:"LUX_CONSOLE_URL" kind:"string" use:"orchestrator"`
		APIKey     any `toml:"api_key" env:"LUX_API_KEY" kind:"string" use:"orchestrator" secret:"true"`
	} `toml:"lux"`
	LLM struct {
		URL any `toml:"url" env:"DUDE_LLM_URL" kind:"string" use:"orchestrator"`
		Key any `toml:"key" env:"DUDE_LLM_KEY" kind:"string" use:"orchestrator" secret:"true"`
	} `toml:"llm"`
	Embeddings struct {
		URL        any `toml:"url" env:"DUDE_EMBEDDINGS_URL" kind:"string" use:"orchestrator"`
		Key        any `toml:"key" env:"DUDE_EMBEDDINGS_KEY" kind:"string" use:"orchestrator" secret:"true"`
		Model      any `toml:"model" env:"DUDE_EMBEDDINGS_MODEL" kind:"string" use:"orchestrator" default:"gemini-embedding-2"`
		Dimensions any `toml:"dimensions" env:"DUDE_EMBEDDINGS_DIMENSIONS" kind:"int" use:"orchestrator" default:"768"`
	} `toml:"embeddings"`
	Agent struct {
		Image   any `toml:"image" env:"DUDE_AGENT_IMAGE" kind:"string" use:"orchestrator" default:"localhost/dude-runtime:dev"`
		Timeout any `toml:"timeout" env:"DUDE_AGENT_TIMEOUT" kind:"string" use:"orchestrator"`
		Egress  any `toml:"egress" env:"DUDE_AGENT_EGRESS" kind:"list" use:"orchestrator"`
	} `toml:"agent"`
	Registry struct {
		Auth       any `toml:"auth" env:"DUDE_REGISTRY_AUTH" kind:"string" use:"orchestrator" default:"none"`
		Host       any `toml:"host" env:"DUDE_REGISTRY" kind:"string" use:"orchestrator"`
		Credential any `toml:"credential" env:"DUDE_REGISTRY_CREDENTIAL" kind:"string" use:"orchestrator" secret:"true"`
		ECRRoleARN any `toml:"ecr_role_arn" env:"DUDE_ECR_ROLE_ARN" kind:"string" use:"orchestrator"`
	} `toml:"registry"`
	Tools struct {
		Listen  any `toml:"listen" env:"DUDE_TOOLS_LISTEN" kind:"string" use:"orchestrator"`
		URL     any `toml:"url" env:"DUDE_TOOLS_URL" kind:"string" use:"orchestrator"`
		Service any `toml:"service" env:"DUDE_TOOLS_SERVICE" kind:"bool" use:"orchestrator" default:"true"`
		Key     any `toml:"key" env:"DUDE_TOOLS_KEY" kind:"string" use:"orchestrator" secret:"true"`
	} `toml:"tools"`
	VAPID struct {
		PublicKey  any `toml:"public_key" env:"DUDE_VAPID_PUBLIC_KEY" kind:"string" use:"orchestrator"`
		PrivateKey any `toml:"private_key" env:"DUDE_VAPID_PRIVATE_KEY" kind:"string" use:"orchestrator" secret:"true"`
		Subject    any `toml:"subject" env:"DUDE_VAPID_SUBJECT" kind:"string" use:"orchestrator" default:"mailto:dude@localhost"`
	} `toml:"vapid"`
	Factory struct {
		Logins any `toml:"logins" env:"DUDE_FACTORY_LOGINS" kind:"list" use:"orchestrator"`
	} `toml:"factory"`
	Auth struct {
		Provider            any `toml:"provider" env:"DUDE_AUTH_PROVIDER" kind:"string" use:"backend" default:"api_key"`
		PublicURL           any `toml:"public_url" env:"DUDE_AUTH_PUBLIC_URL" kind:"string" use:"backend"`
		AutoCreate          any `toml:"auto_create" env:"DUDE_AUTH_AUTO_CREATE" kind:"bool" use:"backend" default:"true"`
		DefaultOrganization any `toml:"default_organization" env:"DUDE_AUTH_DEFAULT_ORGANIZATION" kind:"string" use:"backend"`
		CloudflareAccess    struct {
			Team any `toml:"team" env:"DUDE_AUTH_CLOUDFLARE_ACCESS_TEAM" kind:"string" use:"backend"`
			AUD  any `toml:"aud" env:"DUDE_AUTH_CLOUDFLARE_ACCESS_AUD" kind:"string" use:"backend"`
		} `toml:"cloudflare_access"`
	} `toml:"auth"`
}

// Key is one setting of the schema.
type Key struct {
	Name    string // the file key, dotted: "llm.url"
	Env     string // its variable: "DUDE_LLM_URL"
	Kind    string // string, int, float, bool, list or duration
	Use     string // backend, orchestrator or both
	Default string // "" for none
	Secret  bool
}

func (k Key) usedBy(p Process) bool { return k.Use == "both" || k.Use == string(p) }

// Label names a setting in errors and logs: its file key and its variable.
func (k Key) Label() string { return k.Name + " (" + k.Env + ")" }

// Keys lists the schema, in file order.
func Keys() []Key { return keys }

var keys, byEnv = func() ([]Key, map[string]Key) {
	var out []Key
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		for i := range t.NumField() {
			f := t.Field(i)
			name := prefix + f.Tag.Get("toml")
			if f.Type.Kind() == reflect.Struct {
				walk(f.Type, name+".")
				continue
			}
			out = append(out, Key{Name: name, Env: f.Tag.Get("env"), Kind: f.Tag.Get("kind"), Use: f.Tag.Get("use"),
				Default: f.Tag.Get("default"), Secret: f.Tag.Get("secret") == "true"})
		}
	}
	walk(reflect.TypeFor[schema](), "")
	m := make(map[string]Key, len(out))
	for _, k := range out {
		m[k.Env] = k
	}
	return out, m
}()

// Options are where Load looks. The zero value is the production one.
type Options struct {
	// Getenv reads the environment; os.Getenv when nil.
	Getenv func(string) string
	// DefaultPath replaces DefaultPath, for tests.
	DefaultPath string
}

// Config is the resolved settings of one process.
type Config struct {
	process Process
	// Path is the file read, "" for none.
	Path   string
	values map[string]any    // by variable; only keys set in the file or the environment
	from   map[string]string // by variable: "file" or "env"
	// Warnings are problems that do not stop startup, for the caller to log.
	Warnings []string
}

// Load resolves p's settings. It fails on an unreadable DUDE_CONFIG, a key
// neither process knows, and a value of the wrong type for a key p uses,
// from either source.
func Load(p Process, opts Options) (*Config, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	c := &Config{process: p, values: map[string]any{}, from: map[string]string{}}
	path, text, err := readFile(getenv, opts.DefaultPath)
	if err != nil {
		return nil, err
	}
	if path != "" {
		c.Path = path
		if err := c.loadFile(text); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if w := secretWarning(path, c.from); w != "" {
			c.Warnings = append(c.Warnings, w)
		}
	}
	for _, k := range keys {
		if !k.usedBy(p) {
			continue
		}
		raw := getenv(k.Env)
		if raw == "" {
			continue
		}
		v, err := fromEnv(k, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k.Env, err)
		}
		c.values[k.Env], c.from[k.Env] = v, "env"
	}
	return c, nil
}

func readFile(getenv func(string) string, defaultPath string) (string, []byte, error) {
	if path := getenv("DUDE_CONFIG"); path != "" {
		text, err := os.ReadFile(path)
		if err != nil {
			return "", nil, fmt.Errorf("DUDE_CONFIG: %w", err)
		}
		return path, text, nil
	}
	if defaultPath == "" {
		defaultPath = DefaultPath
	}
	text, err := os.ReadFile(defaultPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil, nil
	case err != nil:
		return "", nil, err
	}
	return defaultPath, text, nil
}

func (c *Config) loadFile(text []byte) error {
	var doc schema
	err := toml.NewDecoder(bytes.NewReader(text)).DisallowUnknownFields().Decode(&doc)
	var strict *toml.StrictMissingError
	var decode *toml.DecodeError
	switch {
	case errors.As(err, &strict):
		var unknown []string
		for _, e := range strict.Errors {
			unknown = append(unknown, strings.Join(e.Key(), "."))
		}
		return fmt.Errorf("unknown key %s", strings.Join(unknown, ", "))
	case errors.As(err, &decode):
		row, col := decode.Position()
		if key := strings.Join(decode.Key(), "."); key != "" {
			return fmt.Errorf("%s: %v (line %d column %d)", key, err, row, col)
		}
		return fmt.Errorf("%v (line %d column %d)", err, row, col)
	case err != nil:
		return err
	}
	i := 0
	var walk func(v reflect.Value) error
	walk = func(v reflect.Value) error {
		for f := range v.NumField() {
			field := v.Field(f)
			if field.Kind() == reflect.Struct {
				if err := walk(field); err != nil {
					return err
				}
				continue
			}
			k := keys[i]
			i++
			if field.IsNil() {
				continue
			}
			val, err := fromFile(k, field.Interface())
			if err != nil {
				if !k.usedBy(c.process) {
					continue // the other process's to refuse
				}
				return fmt.Errorf("%s: %w", k.Label(), err)
			}
			c.values[k.Env], c.from[k.Env] = val, "file"
		}
		return nil
	}
	return walk(reflect.ValueOf(doc))
}

// fromFile checks a decoded TOML value against k's kind and returns it as
// the kind's Go type: string, int64, float64, bool or []string.
func fromFile(k Key, v any) (any, error) {
	switch k.Kind {
	case "string":
		if s, ok := v.(string); ok {
			return s, nil
		}
	case "duration":
		if s, ok := v.(string); ok {
			if _, err := time.ParseDuration(s); err != nil {
				return nil, fmt.Errorf("not a duration: %q", s)
			}
			return s, nil
		}
	case "int":
		if n, ok := v.(int64); ok {
			return n, nil
		}
	case "float":
		switch n := v.(type) {
		case float64:
			return n, nil
		case int64:
			return float64(n), nil
		}
	case "bool":
		if b, ok := v.(bool); ok {
			return b, nil
		}
	case "list":
		if items, ok := v.([]any); ok {
			out := make([]string, 0, len(items))
			for _, item := range items {
				s, ok := item.(string)
				if !ok {
					return nil, errors.New("want an array of strings")
				}
				out = append(out, s)
			}
			return out, nil
		}
		return nil, errors.New("want an array of strings")
	}
	return nil, fmt.Errorf("want %s, not %s", article(k.Kind), tomlType(v))
}

// fromEnv parses a variable's text as k's kind. Lists are comma-separated.
func fromEnv(k Key, raw string) (any, error) {
	switch k.Kind {
	case "duration":
		if _, err := time.ParseDuration(raw); err != nil {
			return nil, fmt.Errorf("not a duration: %q", raw)
		}
	case "int":
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("not an integer: %q", raw)
		}
		return n, nil
	case "float":
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("not a number: %q", raw)
		}
		return n, nil
	case "bool":
		switch strings.ToLower(raw) {
		case "true", "on", "1":
			return true, nil
		case "false", "off", "0":
			return false, nil
		}
		return nil, fmt.Errorf("want true, false, on, off, 1 or 0, not %q", raw)
	case "list":
		out := []string{}
		for _, v := range strings.Split(raw, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out, nil
	}
	return raw, nil
}

func article(kind string) string {
	switch kind {
	case "int":
		return "an integer"
	case "float":
		return "a number"
	case "bool":
		return "a boolean"
	case "duration":
		return "a duration string"
	}
	return "a " + kind
}

func tomlType(v any) string {
	switch v.(type) {
	case string:
		return "a string"
	case int64:
		return "an integer"
	case float64:
		return "a float"
	case bool:
		return "a boolean"
	case []any:
		return "an array"
	case map[string]any:
		return "a table"
	}
	return fmt.Sprintf("%T", v)
}

// secretWarning is set when the file others on the host can read holds a
// secret: it belongs in the environment, or the file must be 0600/0640.
func secretWarning(path string, from map[string]string) string {
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o044 == 0 {
		return ""
	}
	var secrets []string
	for _, k := range keys {
		if k.Secret && from[k.Env] == "file" {
			secrets = append(secrets, k.Name)
		}
	}
	if len(secrets) == 0 {
		return ""
	}
	return fmt.Sprintf("%s is readable by group or others (mode %04o) and holds secrets: %s; "+
		"chmod it 0600 or supply them by environment", path, info.Mode().Perm(), strings.Join(secrets, ", "))
}

func (c *Config) key(env string) Key {
	k, ok := byEnv[env]
	if !ok || !k.usedBy(c.process) {
		panic(fmt.Sprintf("config: %s is not a %s setting", env, c.process))
	}
	return k
}

func (c *Config) value(env, kind string) any {
	k := c.key(env)
	if k.Kind != kind {
		panic(fmt.Sprintf("config: %s is %s, not %s", env, k.Kind, kind))
	}
	if v, ok := c.values[env]; ok {
		return v
	}
	if k.Default == "" {
		return nil
	}
	v, err := fromEnv(k, k.Default)
	if err != nil {
		panic(fmt.Sprintf("config: default of %s: %v", env, err))
	}
	return v
}

// String is a string setting, its default, or "".
func (c *Config) String(env string) string {
	v, _ := c.value(env, "string").(string)
	return v
}

// Int is an integer setting or its default.
func (c *Config) Int(env string) int64 {
	v, _ := c.value(env, "int").(int64)
	return v
}

// Float is a number setting or its default.
func (c *Config) Float(env string) float64 {
	v, _ := c.value(env, "float").(float64)
	return v
}

// Bool is a boolean setting or its default; false with neither.
func (c *Config) Bool(env string) bool {
	v, _ := c.value(env, "bool").(bool)
	return v
}

// List is a list setting; nil when unset.
func (c *Config) List(env string) []string {
	v, _ := c.value(env, "list").([]string)
	return v
}

// Duration is a duration setting or its default; Load checked it parses.
func (c *Config) Duration(env string) time.Duration {
	s, _ := c.value(env, "duration").(string)
	d, _ := time.ParseDuration(s)
	return d
}

// Getenv reads a setting as its variable's text would be, "" when unset:
// for code that takes a getter. Lists are joined with commas.
func (c *Config) Getenv(env string) string {
	k := c.key(env)
	v := c.value(env, k.Kind)
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case []string:
		return strings.Join(v, ",")
	}
	return fmt.Sprint(v)
}

// From says where a setting came from: "file", "env", or "" for its default.
func (c *Config) From(env string) string {
	c.key(env)
	return c.from[env]
}

// Label names a setting in errors: its file key and its variable.
func (c *Config) Label(env string) string { return c.key(env).Label() }

// Set is every setting the file or the environment gave, by variable,
// including the other process's file keys of the right type. For tests
// comparing the two processes' reading of one file.
func (c *Config) Set() map[string]any {
	out := make(map[string]any, len(c.values))
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/config"
)

const goodLayer = "r.example/dude/layer@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func check(t *testing.T, text string, env map[string]string) (int, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dude.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "DUDE_CONFIG" {
			return path
		}
		return env[k]
	}
	var out, errb bytes.Buffer
	code := validate(nil, config.Options{Getenv: getenv, DefaultPath: filepath.Join(t.TempDir(), "none")}, &out, &errb)
	return code, out.String(), errb.String()
}

func TestValidateAcceptsTheBuildersSettings(t *testing.T) {
	code, out, errs := check(t, `[images]
layer = "`+goodLayer+`"
[builder]
database_url = "postgres://dude_builder:x@127.0.0.1/dude"
repository = "123456789012.dkr.ecr.eu-north-1.amazonaws.com/dude/custom"
`, nil)
	if code != 0 || !strings.HasPrefix(out, "ok: ") {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
}

func TestValidateRefusesWhatStartupWould(t *testing.T) {
	for name, c := range map[string]struct{ text, want string }{
		"nothing":         {"", "builder.database_url (DUDE_BUILDER_DATABASE_URL) is required"},
		"a tagged layer":  {"[images]\nlayer = \"r.example/dude/layer:latest\"\n", "must name the layer by digest"},
		"a tagged repo":   {"[builder]\nrepository = \"r.example/dude/custom:v1\"\n", "must be <registry>/<repository> with no tag"},
		"memory in words": {"[builder]\nmemory = \"1.5 GB\"\n", "podman's memory notation"},
		"an unknown key":  {"[builder]\ncpu = 2\n", "unknown key builder.cpu"},
	} {
		code, _, errs := check(t, c.text, nil)
		if code != 1 || !strings.Contains(errs, c.want) {
			t.Errorf("%s: exit %d, %q; want %q", name, code, errs, c.want)
		}
	}
}

func TestTheBuilderIgnoresTheOtherProcessesKeys(t *testing.T) {
	// The orchestrator's and the backend's settings in the same file are not
	// the builder's to check: a bad orchestrator value passes here.
	code, out, errs := check(t, `[orchestrator]
park_after = 12
[images]
layer = "`+goodLayer+`"
[builder]
database_url = "postgres://x"
repository = "r.example/dude/custom"
`, nil)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
}

func TestExtraArgumentsAreUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := validate([]string{"x"}, config.Options{}, &out, &errb); code != 2 {
		t.Errorf("exit %d", code)
	}
}

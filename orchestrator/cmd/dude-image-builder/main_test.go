package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/config"
	"github.com/marciomartins/dude/orchestrator/internal/images"
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
		"nothing":           {"", "builder.database_url (DUDE_BUILDER_DATABASE_URL) is required"},
		"a tagged layer":    {"[images]\nlayer = \"r.example/dude/layer:latest\"\n", "must name the layer by digest"},
		"a tagged repo":     {"[builder]\nrepository = \"r.example/dude/custom:v1\"\n", "must be <registry>/<repository> with no tag"},
		"memory in words":   {"[builder]\nmemory = \"1.5 GB\"\n", "must be whole bytes or a whole number of k, m or g"},
		"fractional memory": {"[builder]\nmemory = \"1.5g\"\n", "must be whole bytes or a whole number of k, m or g"},
		"an unknown key":    {"[builder]\ncpu = 2\n", "unknown key builder.cpu"},
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

// fakePodmanBin is a podman that answers `podman info` with controllers.
func fakePodmanBin(t *testing.T, controllers string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "podman")
	script := "#!/bin/sh\n[ \"$1 $3\" = \"info {{json .Host.CgroupControllers}}\" ] || exit 9\necho '" + controllers + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestTheBuilderDoesNotStartWherePodmanCannotLimitABuild(t *testing.T) {
	for _, c := range []struct{ controllers, want string }{
		{`["cpu","memory","pids"]`, ""},
		{`["pids"]`, "podman cannot limit builds: the cpu and memory cgroup controllers are not delegated to this user (podman has [pids]); delegate cpu and memory to its user@.service"},
		{`["cpuset","cpu","pids"]`, "podman cannot limit builds: the memory cgroup controller is not delegated to this user (podman has [cpuset cpu pids]); delegate cpu and memory to its user@.service"},
	} {
		err := images.CheckLimits(context.Background(), images.CLI{Bin: fakePodmanBin(t, c.controllers)})
		if got := fmt.Sprint(err); (c.want == "" && err != nil) || (c.want != "" && got != c.want) {
			t.Errorf("%s: %v", c.controllers, err)
		}
	}
}

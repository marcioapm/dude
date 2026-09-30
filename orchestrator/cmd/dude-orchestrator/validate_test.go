package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/config"
)

// TestValidateHelper is the orchestrator's main for TestValidate, with the
// arguments in DUDE_TEST_VALIDATE_ARGS; alone it does nothing.
func TestValidateHelper(t *testing.T) {
	if os.Getenv("DUDE_TEST_VALIDATE_HELPER") != "1" {
		t.Skip("started by TestValidate")
	}
	os.Args = append([]string{"dude-orchestrator"}, strings.Fields(os.Getenv("DUDE_TEST_VALIDATE_ARGS"))...)
	main()
	os.Exit(0)
}

// listener counts connections to an address the test owns.
func listener(t *testing.T) (addr string, connections func() int64) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	var n atomic.Int64
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			c.Close()
		}
	}()
	return l.Addr().String(), n.Load
}

type validateRun struct {
	exit           int
	stdout, stderr string
}

func configFile(t *testing.T, text string, mode os.FileMode) string {
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

// runMain runs the orchestrator's main with args, configured by the file at
// path and env alone: none of this machine's own settings.
func runMain(t *testing.T, path string, env []string, args ...string) validateRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestValidateHelper$", "-test.count=1")
	cmd.Env = append([]string{"DUDE_TEST_VALIDATE_HELPER=1", "DUDE_TEST_VALIDATE_ARGS=" + strings.Join(args, " "),
		"DUDE_CONFIG=" + path, "HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%q did not exit: %s", args, stderr.String())
	}
	r := validateRun{stdout: stdout.String(), stderr: stderr.String()}
	if exit, ok := err.(*exec.ExitError); ok {
		r.exit = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return r
}

func runValidate(t *testing.T, text string, mode os.FileMode, args ...string) (validateRun, string) {
	t.Helper()
	path := configFile(t, text, mode)
	return runMain(t, path, nil, append([]string{"validate"}, args...)...), path
}

// startupError is the error startup's resolve gives for the file at path.
func startupError(t *testing.T, path string) string {
	t.Helper()
	_, _, err := resolve(config.Options{Getenv: func(k string) string {
		if k == "DUDE_CONFIG" {
			return path
		}
		return ""
	}, DefaultPath: filepath.Join(t.TempDir(), "absent.toml")})
	if err == nil {
		t.Fatal("startup accepts the file")
	}
	return err.Error()
}

// validFile is a complete orchestrator file whose every address is addr: a
// connection to any of them is one validate must not make.
func validFile(addr, luxKey string) string {
	return `[database]
url = "postgres://dude:pw@` + addr + `/dude?connect_timeout=2"
[orchestrator]
token = "file-token"
listen = "` + addr + `"
[lux]
url = "http://` + addr + `"
api_key = "` + luxKey + `"
[llm]
url = "http://` + addr + `/v1"
key = "llm-key"
[tools]
listen = "` + addr + `"
url = "http://` + addr + `"
`
}

func TestValidate(t *testing.T) {
	const sentinel = "S3NT1NEL-validate-lux-key-7c1e"
	addr, connections := listener(t)
	good := validFile(addr, sentinel)
	check := func(name string, r validateRun, exit int, stdout string, stderrHas ...string) {
		t.Helper()
		if r.exit != exit || r.stdout != stdout {
			t.Errorf("%s: exit %d stdout %q, want %d %q; stderr %q", name, r.exit, r.stdout, exit, stdout, r.stderr)
		}
		for _, s := range stderrHas {
			if !strings.Contains(r.stderr, s) {
				t.Errorf("%s: stderr %q, want %q in it", name, r.stderr, s)
			}
		}
		if strings.Contains(r.stdout+r.stderr, sentinel) {
			t.Errorf("%s: the secret was printed: %q %q", name, r.stdout, r.stderr)
		}
	}

	r, path := runValidate(t, good, 0o600)
	check("valid", r, 0, "ok: "+path+"\n")
	if r.stderr != "" {
		t.Errorf("valid: stderr %q, want nothing", r.stderr)
	}

	r, path = runValidate(t, good+"[agent]\nimgae = \"x\"\n", 0o600)
	check("unknown key", r, 1, "", "unknown key agent.imgae")
	if want := startupError(t, path) + "\n"; r.stderr != want {
		t.Errorf("unknown key: stderr %q, want startup's %q", r.stderr, want)
	}

	r, _ = runValidate(t, strings.Replace(good, `api_key = "`+sentinel+`"`, "", 1), 0o600)
	check("missing", r, 1, "")
	if r.stderr != "lux.api_key (LUX_API_KEY) is required\n" {
		t.Errorf("missing: stderr %q", r.stderr)
	}

	r, path = runValidate(t, good+"[registry]\nauth = \"ecr\"\n", 0o600)
	check("settings", r, 1, "", "needs DUDE_AGENT_IMAGE in an ECR registry")
	if want := startupError(t, path) + "\n"; r.stderr != want {
		t.Errorf("settings: stderr %q, want startup's %q", r.stderr, want)
	}

	r, path = runValidate(t, good, 0o644)
	check("world-readable secret", r, 0, "ok: "+path+"\n", "warning: ", "lux.api_key", "mode 0644")
	if n := strings.Count(r.stderr, "\n"); n != 1 {
		t.Errorf("world-readable secret: %d stderr lines, want 1: %q", n, r.stderr)
	}

	r, _ = runValidate(t, good, 0o600, "extra")
	check("extra argument", r, 2, "", "usage: dude-orchestrator validate")

	if n := connections(); n != 0 {
		t.Errorf("validate made %d connections", n)
	}
}

// Startup (no arguments) and validate, run on the same file, refuse it with
// the same diagnostic, and neither reaches the addresses in it.
func TestStartupRefusesWhatValidateRefuses(t *testing.T) {
	addr, connections := listener(t)
	good := validFile(addr, "lux-key")
	cases := []struct {
		name, text  string
		startupExit int
		diagnostic  string
	}{
		{"loader error", good + "[agent]\nimgae = \"x\"\n", 1, "unknown key agent.imgae"},
		{"missing database.url", strings.Replace(good, `url = "postgres://dude:pw@`+addr+`/dude?connect_timeout=2"`+"\n", "", 1),
			2, "database.url (DATABASE_URL) is required"},
		{"missing lux.url", strings.Replace(good, "[lux]\nurl = \"http://"+addr+"\"\n", "[lux]\n", 1),
			2, "lux.url (LUX_URL) is required"},
		{"missing lux.api_key", strings.Replace(good, "api_key = \"lux-key\"\n", "", 1),
			2, "lux.api_key (LUX_API_KEY) is required"},
		{"missing orchestrator.token", strings.Replace(good, "token = \"file-token\"\n", "", 1),
			2, "orchestrator.token (DUDE_ORCHESTRATOR_TOKEN) is required"},
		{"invalid registry", good + "[registry]\nauth = \"ecr\"\n", 1, "needs DUDE_AGENT_IMAGE in an ECR registry"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.text == good {
				t.Fatal("the case does not change the file")
			}
			path := configFile(t, c.text, 0o600)
			v := runMain(t, path, nil, "validate")
			if v.exit != 1 || v.stdout != "" || !strings.Contains(v.stderr, c.diagnostic) {
				t.Fatalf("validate: exit %d stdout %q stderr %q, want 1 and %q", v.exit, v.stdout, v.stderr, c.diagnostic)
			}
			diagnostic := strings.TrimSuffix(v.stderr, "\n")
			s := runMain(t, path, nil)
			// slog quotes the error, escaping any quotes in it.
			quoted := strconv.Quote(diagnostic)
			if s.exit != c.startupExit || !(strings.Contains(s.stderr, diagnostic) || strings.Contains(s.stderr, quoted[1:len(quoted)-1])) {
				t.Errorf("startup: exit %d stderr %q, want %d and validate's %q", s.exit, s.stderr, c.startupExit, diagnostic)
			}
		})
	}
	if n := connections(); n != 0 {
		t.Errorf("startup or validate made %d connections", n)
	}
}

func TestValidateWithNoFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	vars := map[string]string{"DATABASE_URL": "postgres://x/y", "DUDE_ORCHESTRATOR_TOKEN": "t",
		"LUX_URL": "https://lux.example", "LUX_API_KEY": "k"}
	exit := validate(nil, config.Options{Getenv: func(k string) string { return vars[k] },
		DefaultPath: filepath.Join(t.TempDir(), "absent.toml")}, &stdout, &stderr)
	if exit != 0 || stdout.String() != "ok: no file\n" || stderr.String() != "" {
		t.Errorf("exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
}
